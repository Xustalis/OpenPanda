// Package askengine is the unified entry engine shared by `panda ask` (CLI)
// and the web panel: one prompt in, three intents out — answer (pure LLM
// reply), tool_call (memory tools, executed and fed back), task (submitted to
// the node network, locally or delegated). It owns the whole pipeline —
// config, storage, memory injection, tool registry, entry model client, and
// the optional P2P scheduler core — so both front-ends run exactly the same
// classification and execution path with zero duplicated logic.
package askengine

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/xenith/openpanda/internal/config"
	"github.com/xenith/openpanda/internal/core"
	"github.com/xenith/openpanda/internal/entry"
	"github.com/xenith/openpanda/internal/ledger"
	"github.com/xenith/openpanda/internal/mcp"
	"github.com/xenith/openpanda/internal/memory"
	"github.com/xenith/openpanda/internal/skills"
	"github.com/xenith/openpanda/internal/storage"
)

// Options tunes how the engine is built.
type Options struct {
	// CardPath points at capabilities.yaml. Only when set can classified
	// tasks actually execute (the card powers the local scheduler core);
	// without it the engine answers and runs memory tools only.
	CardPath string
	// MCPCommand is an optional space-separated stdio MCP server command
	// whose tools are imported into the registry.
	MCPCommand string
	// Logger defaults to a warn-level stderr handler.
	Logger *slog.Logger
}

// Engine is the long-lived unified entry engine. It is safe for concurrent
// Ask calls (the entry client and registry are; the scheduler core is).
type Engine struct {
	cfg      *config.Config
	db       *sql.DB
	client   *entry.Client
	registry *entry.Registry
	injector *memory.Injector

	mcp    *mcp.Client
	logger *slog.Logger

	// sched is non-nil exactly when Options.CardPath was set.
	sched       *core.Core
	schedCtx    context.Context
	schedCancel context.CancelFunc
}

// Result is the outcome of one Ask call.
type Result struct {
	// Kind is "answer" or "task" — the final converged intent.
	Kind string
	// Answer carries the model's text reply when Kind == "answer".
	Answer string
	// Note is an incidental model note emitted alongside a tool call.
	Note string

	// Task fields, valid when Kind == "task".
	TaskID    string
	TaskState string
	OK        bool
	Stdout    string
	Stderr    string
	ExitCode  int
}

// New builds an engine from cfg: opens storage, wires memory stores and the
// tool registry, connects the entry model client, optionally loads the
// capability card (creating the scheduler core and dialing peers once so the
// first classification already sees remote capabilities), and optionally
// spawns the MCP server.
func New(ctx context.Context, cfg *config.Config, opts Options) (*Engine, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	hermes := memory.NewHermes(cfg.Storage.MemoryPath)
	projects := memory.NewProjects(cfg.Storage.ProjectsPath)
	injector := memory.NewInjector(hermes, projects)
	registry := buildToolRegistry(hermes, projects)

	var mcpClient *mcp.Client
	if cmd := opts.MCPCommand; cmd != "" {
		parts := splitCommand(cmd)
		if len(parts) == 0 {
			return nil, fmt.Errorf("askengine: empty MCP command")
		}
		var err error
		mcpClient, err = mcp.NewStdioClient(ctx, parts[0], nil, parts[1:]...)
		if err != nil {
			return nil, fmt.Errorf("askengine: start MCP server: %w", err)
		}
		if err := registerMCPTools(ctx, registry, mcpClient); err != nil {
			mcpClient.Close()
			return nil, fmt.Errorf("askengine: register MCP tools: %w", err)
		}
	}

	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		return nil, fmt.Errorf("askengine: open database: %w", err)
	}
	if err := storage.Migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("askengine: migrate database: %w", err)
	}

	client, err := entry.NewClient(cfg.Model)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("askengine: model client: %w", err)
	}

	e := &Engine{
		cfg:      cfg,
		db:       db,
		client:   client,
		registry: registry,
		injector: injector,
		mcp:      mcpClient,
		logger:   logger,
	}

	if opts.CardPath != "" {
		card, err := ledger.LoadCard(opts.CardPath)
		if err != nil {
			e.Close()
			return nil, fmt.Errorf("askengine: load capabilities: %w", err)
		}
		// The engine's scheduler is a short-lived/ephemeral participant: its
		// node id never collides with the concurrently running daemon on the
		// same node (the daemon owns the stable identity and listener).
		sched := core.NewCore(db, core.EphemeralNodeID(cfg.Node.Name), card, schedulerTier(cfg.Node.ResourceClass), logger, cfg.Model)
		sched.SetMemoryStores(injector, memory.NewDaily(hermes.WarmDir()), skills.NewStore(cfg.Storage.SkillsPath))
		sched.SetWorkDir(cfg.Storage.WorkPath)
		sched.SetHostStatePaths(hostStatePaths(cfg))
		sched.SetSharedSecret(cfg.Network.SharedSecret)
		schedCtx, cancel := context.WithCancel(context.Background())
		e.sched = sched
		e.schedCtx = schedCtx
		e.schedCancel = cancel

		for _, peer := range cfg.Network.Peers {
			if err := sched.DialPeer(schedCtx, peer); err != nil {
				logger.Warn("peer dial failed", "peer", peer, "err", err)
			}
		}
		if len(cfg.Network.Peers) > 0 {
			waitForPeers(schedCtx, db, 2*time.Second)
		}
	}

	return e, nil
}

// MaintainPeers keeps redialing configured peers in the background until ctx
// ends — for long-lived embedders (the web panel). Short-lived CLI asks skip
// it: New's one-shot dial plus waitForPeers already covers them.
func (e *Engine) MaintainPeers(ctx context.Context) {
	if e.sched == nil {
		return
	}
	for _, peer := range e.cfg.Network.Peers {
		go func(p string) {
			backoff := time.Second
			for {
				err := e.sched.MaintainPeer(ctx, p)
				if err != nil {
					e.logger.Warn("peer dial failed", "peer", p, "err", err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(backoff):
					}
					backoff = min(backoff*2, 30*time.Second)
					continue
				}
				backoff = time.Second
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
			}
		}(peer)
	}
}

// Ask runs one prompt through the unified entry model. A tool_call intent is
// executed (memory/MCP tools) and its result fed back, converging to a final
// answer or task; a task is submitted through the scheduler core (local or
// delegated) and its outcome returned.
func (e *Engine) Ask(ctx context.Context, prompt string, authorize bool) (*Result, error) {
	// Devices visible to classification: the local capability directory
	// (populated by the daemon's heartbeats and our own peer dials).
	devices, err := ledger.Query(e.db, "online", "")
	if err != nil {
		devices = nil
	}

	// Hermes memory relevant to this prompt; a load failure degrades to
	// classifying without memory rather than failing the ask.
	conversationMemory, merr := e.injector.Conversation(prompt)
	if merr != nil {
		e.logger.Warn("load memory", "err", merr)
	}

	turns := []entry.Turn{{Role: "user", Content: prompt}}
	const maxRounds = 6
	for round := 0; round < maxRounds; round++ {
		out, err := entry.ClassifyTurnsWithTools(ctx, e.client, devices, conversationMemory, turns, e.registry)
		if err != nil {
			return nil, err
		}

		switch out.Kind {
		case entry.KindAnswer:
			return &Result{Kind: "answer", Answer: out.Answer}, nil
		case entry.KindTask:
			if e.sched == nil {
				return nil, fmt.Errorf("task output requires a capability card (engine built without CardPath)")
			}
			return e.submitTask(out.Task, authorize), nil
		case entry.KindToolCall:
			result := executeTool(ctx, e.registry, out.Tool)
			turns = appendToolTurns(turns, out.Tool, out.Note, result)
		default:
			return &Result{Kind: "answer", Answer: out.Answer}, nil
		}
	}
	return nil, fmt.Errorf("reached max tool rounds (%d) without converging", maxRounds)
}

// submitTask executes a classified task spec through the scheduler core and
// maps the outcome to a Result.
func (e *Engine) submitTask(spec *entry.TaskSpec, authorized bool) *Result {
	in := toTaskInput(spec)
	in.Authorized = authorized
	task, result, err := e.sched.Submit(e.schedCtx, in)
	if err != nil {
		return &Result{Kind: "task", TaskState: "failed", Stderr: err.Error(), ExitCode: 1}
	}
	res := &Result{
		Kind:      "task",
		TaskID:    task.TaskID,
		TaskState: task.State,
		OK:        result.OK,
		Stdout:    result.Stdout,
		Stderr:    result.Stderr,
		ExitCode:  result.ExitCode,
	}
	return res
}

// Close releases the engine's resources (DB handle, scheduler core, MCP
// server). The engine must not be used afterwards.
func (e *Engine) Close() {
	if e.schedCancel != nil {
		e.schedCancel()
	}
	if e.mcp != nil {
		e.mcp.Close()
	}
	if e.db != nil {
		e.db.Close()
	}
}
