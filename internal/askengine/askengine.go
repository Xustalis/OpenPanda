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
	"sync"
	"sync/atomic"
	"time"

	"github.com/xenith/openpanda/internal/config"
	"github.com/xenith/openpanda/internal/core"
	"github.com/xenith/openpanda/internal/entry"
	"github.com/xenith/openpanda/internal/ledger"
	"github.com/xenith/openpanda/internal/mcp"
	"github.com/xenith/openpanda/internal/memory"
	"github.com/xenith/openpanda/internal/reminders"
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
	client   atomic.Pointer[entry.Client]
	injector *memory.Injector

	// registry is swapped whole by SetMCPCommand; regMu guards the swap and
	// gives in-flight Asks a stable registry reference.
	regMu    sync.RWMutex
	registry *entry.Registry

	hermes   *memory.Hermes
	projects *memory.Projects
	remind   *reminders.Store

	mcp        *mcp.Client
	mcpCommand string
	logger     *slog.Logger

	// sched is non-nil exactly when Options.CardPath was set. schedMu
	// serializes task submission: a submit may temporarily pin the core's
	// work dir to a session worktree, which must not interleave.
	sched       *core.Core
	schedMu     sync.Mutex
	schedCtx    context.Context
	schedCancel context.CancelFunc
}

// SetModel hot-swaps the entry model client at runtime (the settings page):
// a failed build leaves the previous client serving.
func (e *Engine) SetModel(mc config.ModelConfig) error {
	c, err := entry.NewClient(mc)
	if err != nil {
		return err
	}
	e.client.Store(c)
	return nil
}

// ModelConfig returns the engine's current model configuration.
func (e *Engine) ModelConfig() config.ModelConfig { return e.cfg.Model }

// Config returns the engine's loaded configuration.
func (e *Engine) Config() *config.Config { return e.cfg }

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

// SetMCPCommand hot-swaps the stdio MCP server at runtime (the settings
// page): a new registry is built (memory + system + reminder tools), the new
// server spawned and its tools imported, then the swap happens atomically.
// An empty command disables MCP. A failed spawn leaves the previous registry
// and server serving. The ctx spawns the new server; the engine's own
// lifetime is unchanged.
func (e *Engine) SetMCPCommand(ctx context.Context, cmd string) error {
	e.regMu.Lock()
	defer e.regMu.Unlock()

	reg := buildToolRegistry(e.hermes, e.projects, e.remind)
	var client *mcp.Client
	if cmd != "" {
		parts := splitCommand(cmd)
		if len(parts) == 0 {
			return fmt.Errorf("askengine: empty MCP command")
		}
		var err error
		client, err = mcp.NewStdioClient(ctx, parts[0], nil, parts[1:]...)
		if err != nil {
			return fmt.Errorf("askengine: start MCP server: %w", err)
		}
		if err := registerMCPTools(ctx, reg, client); err != nil {
			client.Close()
			return fmt.Errorf("askengine: register MCP tools: %w", err)
		}
	}

	old := e.mcp
	e.registry = reg
	e.mcp = client
	e.mcpCommand = cmd
	if old != nil {
		old.Close()
	}
	return nil
}

// MCPCommand returns the current MCP server command ("" = disabled).
func (e *Engine) MCPCommand() string {
	e.regMu.RLock()
	defer e.regMu.RUnlock()
	return e.mcpCommand
}

// currentRegistry snapshots the registry for one Ask.
func (e *Engine) currentRegistry() *entry.Registry {
	e.regMu.RLock()
	defer e.regMu.RUnlock()
	return e.registry
}

// New builds an engine from cfg: opens storage, wires memory stores and the
// tool registry, connects the entry model client, optionally loads the
// capability card (creating the scheduler core and dialing peers once so the
// first classification already sees remote capabilities), and optionally
// spawns the MCP server. An Options.MCPCommand of "" falls back to
// config.yaml's mcp.command, so a configured server needs no CLI flag.
func New(ctx context.Context, cfg *config.Config, opts Options) (*Engine, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		return nil, fmt.Errorf("askengine: open database: %w", err)
	}
	if err := storage.Migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("askengine: migrate database: %w", err)
	}

	hermes := memory.NewHermes(cfg.Storage.MemoryPath)
	projects := memory.NewProjects(cfg.Storage.ProjectsPath)
	injector := memory.NewInjector(hermes, projects)
	remind := reminders.NewStore(db)
	registry := buildToolRegistry(hermes, projects, remind)

	client, err := entry.NewClient(cfg.Model)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("askengine: model client: %w", err)
	}

	e := &Engine{
		cfg:      cfg,
		db:       db,
		registry: registry,
		injector: injector,
		hermes:   hermes,
		projects: projects,
		remind:   remind,
		logger:   logger,
	}
	e.client.Store(client)

	mcpCmd := opts.MCPCommand
	if mcpCmd == "" {
		mcpCmd = cfg.MCP.Command
	}
	if mcpCmd != "" {
		if err := e.SetMCPCommand(ctx, mcpCmd); err != nil {
			e.Close()
			return nil, err
		}
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

// StreamCallbacks receives live progress while an ask converges. OnDelta
// delivers answer text incrementally (streaming); OnStatus delivers one-line
// progress notes (tool calls, task submission).
type StreamCallbacks struct {
	OnDelta  func(text string)
	OnStatus func(text string)
}

// Ask runs one prompt through the unified entry model. A tool_call intent is
// executed (memory/MCP tools) and its result fed back, converging to a final
// answer or task; a task is submitted through the scheduler core (local or
// delegated) and its outcome returned.
func (e *Engine) Ask(ctx context.Context, prompt string, authorize bool) (*Result, error) {
	return e.AskTurns(ctx, nil, prompt, "", authorize, StreamCallbacks{})
}

// AskTurns is the session-aware ask: history carries the conversation so far
// (plain user/assistant turns), workDir optionally pins where a classified
// task executes (a session's git worktree), and the callbacks stream live
// progress. A nil OnDelta still streams internally — it just is not forwarded.
func (e *Engine) AskTurns(ctx context.Context, history []entry.Turn, prompt, workDir string, authorize bool, cb StreamCallbacks) (*Result, error) {
	client := e.client.Load()

	// Devices visible to classification: the local capability directory
	// (populated by the daemon's heartbeats and our own peer dials).
	devices, err := ledger.Query(e.db, "online", "")
	if err != nil {
		devices = nil
	}

	// Memory wall (design §17.2): Hermes personal memory enters only
	// project-free conversations. A pinned workDir marks a project/workspace
	// conversation (a session's worktree or the shared work path) — its
	// classification and any task it spawns must stay untainted by personal
	// memory, so nothing is loaded at all. Project memory reaches execution
	// later via ContextPack on the executing node, never this prompt.
	conversationMemory := ""
	if workDir == "" {
		var merr error
		conversationMemory, merr = e.injector.Conversation(prompt)
		if merr != nil {
			e.logger.Warn("load memory", "err", merr)
		}
	}

	turns := make([]entry.Turn, 0, len(history)+1)
	turns = append(turns, history...)
	turns = append(turns, entry.Turn{Role: "user", Content: prompt})
	reg := e.currentRegistry()
	const maxRounds = 6
	for round := 0; round < maxRounds; round++ {
		var out entry.Output
		var err error
		if cb.OnDelta != nil {
			out, err = entry.ClassifyStreamWithTools(ctx, client, devices, conversationMemory, turns, reg, cb.OnDelta)
		} else {
			out, err = entry.ClassifyTurnsWithTools(ctx, client, devices, conversationMemory, turns, reg)
		}
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
			if cb.OnStatus != nil {
				cb.OnStatus(fmt.Sprintf("submitting task: %s", out.Task.Title))
			}
			return e.submitTask(out.Task, authorize, workDir), nil
		case entry.KindToolCall:
			if cb.OnStatus != nil {
				cb.OnStatus(fmt.Sprintf("running tool %s…", out.Tool.Tool))
			}
			result := executeTool(ctx, e.registry, out.Tool)
			turns = appendToolTurns(turns, out.Tool, out.Note, result)
		default:
			return &Result{Kind: "answer", Answer: out.Answer}, nil
		}
	}
	return nil, fmt.Errorf("reached max tool rounds (%d) without converging", maxRounds)
}

// WorkPath returns the configured work directory — the project workspace
// panel sessions execute in. It lets the panel pin non-repo sessions to the
// work path so the memory wall (§17.2) holds for them too.
func (e *Engine) WorkPath() string { return e.cfg.Storage.WorkPath }

// submitTask executes a classified task spec through the scheduler core and
// maps the outcome to a Result. workDir, when set, temporarily pins the core's
// execution directory (a session worktree); the configured work path is
// restored afterwards. The schedMu lock keeps concurrent submits from
// interleaving the work-dir swap.
func (e *Engine) submitTask(spec *entry.TaskSpec, authorized bool, workDir string) *Result {
	in := toTaskInput(spec)
	in.Authorized = authorized
	e.schedMu.Lock()
	defer e.schedMu.Unlock()
	if workDir != "" {
		e.sched.SetWorkDir(workDir)
		defer e.sched.SetWorkDir(e.cfg.Storage.WorkPath)
	}
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
