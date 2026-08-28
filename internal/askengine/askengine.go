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
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/mcp"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/storage"
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
	// QueueTasks routes classified tasks through the async queue (core.Enqueue)
	// instead of blocking inline submission: the task lands in queued and the
	// queue scheduler starts it when resources allow — the panel's mode, where
	// progress streams into the session. The CLI keeps inline (blocking) mode.
	QueueTasks bool
	// ReplyASCII makes the entry model answer in English/ASCII. Set it when
	// the client runs on a bare Linux console whose font has no CJK glyphs
	// (Chinese replies would otherwise render as diamonds).
	ReplyASCII bool
	// AsyncPeers dials configured peers in the background instead of waiting
	// for the dials (and a settle window) before New returns. Interactive
	// surfaces (the REPL) want this: an offline peer's dial timeout is
	// routine in a long-lived session, not worth dead air before the banner.
	// One-shot callers (panda ask) leave it off — their routing decision runs
	// immediately and needs the conns settled first.
	AsyncPeers bool
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
	// work dir to a session worktree, which must not interleave. (Queue
	// mode never swaps the global work dir — it travels per task.)
	sched       *core.Core
	schedMu     sync.Mutex
	schedCtx    context.Context
	schedCancel context.CancelFunc
	// queueTasks mirrors Options.QueueTasks.
	queueTasks bool
	// replyASCII mirrors Options.ReplyASCII (per-engine classify option).
	replyASCII bool
}

// SetModel hot-swaps the entry model client at runtime (the settings page):
// a failed build leaves the previous client serving.
func (e *Engine) SetModel(mc config.ModelConfig) error {
	c, err := entry.NewClient(mc)
	if err != nil {
		return err
	}
	c.SetDiskCache(entry.NewDiskCache(e.db))
	e.client.Store(c)
	return nil
}

// ModelConfig returns the engine's current model configuration.
func (e *Engine) ModelConfig() config.ModelConfig { return e.cfg.Model }

// Config returns the engine's loaded configuration.
func (e *Engine) Config() *config.Config { return e.cfg }

// Result is the outcome of one Ask call.
type Result struct {
	// Kind is "answer", "task" or "plan" — the final converged intent.
	Kind string
	// Answer carries the model's text reply when Kind == "answer".
	Answer string
	// Note is an incidental model note emitted alongside a tool call.
	Note string

	// Task fields, valid when Kind == "task".
	TaskID    string
	TaskTitle string // for conversation history: "the task that ran" in one line
	TaskState string
	OK        bool
	Stdout    string
	Stderr    string
	ExitCode  int

	// Plan fields, valid when Kind == "plan". A plan is asynchronous by nature —
	// its stages run on other machines, in waves — so the call returns as soon as
	// the stages exist and the first wave is released; PlanID is how the caller
	// follows it from there.
	PlanID     string
	PlanGoal   string
	PlanStages []core.Task

	// Cost of this ask, as reported by the entry model's provider (zero for
	// providers that report no usage). The CLI shows them on its closing status
	// line; the panel bills them through RecordDelegationMetric.
	InputTokens  int64
	OutputTokens int64
	Latency      time.Duration

	// NeedsApproval is set on a task Result when execution refused for lack of
	// tier-2 (irreversible) consent and the caller supplied no OnApproval
	// callback: the task is parked in review and Approval carries what a person
	// must sign off on. A caller whose UI cannot answer a synchronous callback
	// (the termios REPL, whose interrupt watcher owns the terminal mid-ask)
	// reads this after the ask returns, prompts on its own event loop, and calls
	// ResumeApproved. When OnApproval is set the engine consults it inline and
	// this stays false.
	NeedsApproval bool
	Approval      *ApprovalRequest
}

// ApprovalRequest describes a tier-2 (irreversible) action awaiting the user's
// consent at the inline approval gate: the task the entry model routed and the
// executor's refusal reason. It is what an OnApproval callback renders and what
// a NeedsApproval Result carries back for a caller that prompts out-of-band.
type ApprovalRequest struct {
	TaskID string
	Title  string
	Intent string
	Reason string // the executor's authorization-refusal message
}

// Tokens is the ask's total token count (input + output), 0 when the provider
// reports no usage.
func (r *Result) Tokens() int64 {
	if r == nil {
		return 0
	}
	return r.InputTokens + r.OutputTokens
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

	limits := memory.Limits{
		User:    cfg.Memory.Limits.User,
		Memory:  cfg.Memory.Limits.Memory,
		Project: cfg.Memory.Limits.Project,
	}
	hermes := memory.NewHermesWithLimits(cfg.Storage.MemoryPath, limits)
	projects := memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, limits)
	injector := memory.NewInjector(hermes, projects)
	remind := reminders.NewStore(db)
	registry := buildToolRegistry(hermes, projects, remind)

	client, err := entry.NewClient(cfg.Model)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("askengine: model client: %w", err)
	}
	// Disk cache for entry-model decisions (classify/supervise): identical
	// inputs skip the LLM call entirely. Best-effort by design.
	client.SetDiskCache(entry.NewDiskCache(db))

	e := &Engine{
		cfg:        cfg,
		db:         db,
		registry:   registry,
		injector:   injector,
		hermes:     hermes,
		projects:   projects,
		remind:     remind,
		logger:     logger,
		queueTasks: opts.QueueTasks,
		replyASCII: opts.ReplyASCII,
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
		// Same pruning the daemon does: a native ability whose command is not
		// installed here would win the native plan and fail at exec.
		if dropped := card.PruneUnavailableNative(); len(dropped) > 0 {
			logger.Warn("native abilities dropped: command not found on this host", "ids", dropped)
		}
		// Mirror the daemon's card enrichment: kind/identity come from the
		// config (the card file may omit them), and the node is registered
		// under the same stable runtime ID the daemon uses. Without this, a
		// fresh database shows an empty device list to the entry model —
		// every ask degrades to "no devices available" even though this
		// process can execute the card's tasks locally. The upsert is
		// idempotent with the daemon's own registration.
		card.NodeKind = cfg.Node.Kind
		card.NodeIdentity = cfg.Node.EffectiveIdentity()
		stableID := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, card.NodeIdentity)
		if err := ledger.Register(db, card, stableID, schedulerTier(cfg.Node.ResourceClass)); err != nil {
			logger.Warn("self-register failed", "node", stableID, "err", err)
		}
		// The engine's scheduler is a short-lived/ephemeral participant: its
		// node id never collides with the concurrently running daemon on the
		// same node (the daemon owns the stable identity and listener). The
		// ephemeral id derives from the *stable* runtime id, not the bare
		// config name, so a VM ask session still trims back to the same
		// "name@vm-…" row the daemon registered and routing sees its own
		// capacity (scheduler.IsSelfRow strips the 8-hex suffix).
		sched := core.NewCore(db, core.EphemeralNodeID(stableID), card, schedulerTier(cfg.Node.ResourceClass), logger, cfg.Model)
		sched.SetRouterPolicy(cfg.Injection, cfg.Routing)
		sched.AttachSupervisor(cfg.Model)
		sched.SetMemoryStores(injector, memory.NewDaily(hermes.WarmDir()), skills.NewStore(cfg.Storage.SkillsPath))
		sched.SetWorkDir(cfg.Storage.WorkPath)
		sched.SetHostStatePaths(hostStatePaths(cfg))
		sched.SetSharedSecret(cfg.Network.SharedSecret)
		schedCtx, cancel := context.WithCancel(context.Background())
		e.sched = sched
		e.schedCtx = schedCtx
		e.schedCancel = cancel

		// Queue mode runs the node-local queue scheduler so enqueued tasks
		// execute here even if the kernel daemon is down (ClaimLocal's CAS
		// keeps the two instances from double-running a task).
		if opts.QueueTasks {
			sched.StartQueueScheduler(schedCtx)
		}

		if opts.AsyncPeers {
			// Interactive surfaces: dial in the background and never hold the
			// first prompt — an offline peer can burn the dialer's full 10s
			// timeout, and it used to (serially, before the banner). The conns
			// land in the registry whenever they land; a delegation arriving
			// before that sees the same state as a peer that is offline. Dial
			// failures log at debug: an offline peer is routine in a
			// long-lived session, and a WARN on stderr would land mid-keystroke
			// on the line editor.
			for _, peer := range cfg.Network.Peers {
				go func(p string) {
					if err := sched.DialPeer(schedCtx, p); err != nil {
						logger.Debug("peer dial failed", "peer", p, "err", err)
					}
				}(peer)
			}
		} else {
			// One-shot callers need the conns before the first routing
			// decision, but not one at a time: dials run concurrently so an
			// unreachable peer's timeout does not gate a reachable one.
			var wg sync.WaitGroup
			for _, peer := range cfg.Network.Peers {
				wg.Add(1)
				go func(p string) {
					defer wg.Done()
					if err := sched.DialPeer(schedCtx, p); err != nil {
						logger.Warn("peer dial failed", "peer", p, "err", err)
					}
				}(peer)
			}
			wg.Wait()
			if len(cfg.Network.Peers) > 0 {
				waitForPeers(schedCtx, db, 2*time.Second)
			}
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
// delivers answer text incrementally (streaming); OnReasoning delivers the
// model's chain-of-thought live (display-only, kept out of the answer and
// history per D14); OnStatus delivers one-line progress notes (tool calls, task
// submission) as ready-made English prose, and OnProgress delivers the same
// events structured, for a caller that owns a locale and wants to phrase them
// itself. Set whichever fits — OnProgress wins when both are present.
type StreamCallbacks struct {
	OnDelta     func(text string)
	OnReasoning func(text string)
	OnStatus    func(text string)
	OnProgress  func(Progress)
	// OnApproval is consulted when an inline task refuses for lack of tier-2
	// (irreversible) consent and the approval mode is not "never". Returning
	// true re-runs the task authorized in the same round-trip (ResumeApproved);
	// returning false leaves it parked in review. A nil callback means the
	// caller cannot answer synchronously: the engine returns a NeedsApproval
	// Result instead, for the caller to handle on its own event loop.
	OnApproval func(ApprovalRequest) bool
}

// ProgressKind names what the engine is about to do.
type ProgressKind string

const (
	ProgressTask ProgressKind = "task" // submitting a classified task
	ProgressPlan ProgressKind = "plan" // starting a multi-stage plan
	ProgressTool ProgressKind = "tool" // running a tool
	// The following are lifecycle milestones bridged from the scheduler core's
	// trace events while a synchronous Submit blocks — so the CLI shows the run
	// advancing (routing → executing → judging) instead of a frozen spinner.
	ProgressRoute ProgressKind = "route" // scheduler picked a node
	ProgressExec  ProgressKind = "exec"  // the agent/adapter started running
	ProgressJudge ProgressKind = "judge" // a supervision round is evaluating the result
)

// Progress is one structured progress event: the action, and the name of what
// it acts on (a task title, a plan goal, a tool name). The engine deliberately
// holds no locale — the CLI translates these through internal/i18n and the
// panel through the browser's language, from the same event.
type Progress struct {
	Kind ProgressKind
	Name string
}

// progress reports one event to whichever callback the caller supplied. The
// English prose lives here, in one place, so it stays a fallback rather than
// the only phrasing available.
func (cb StreamCallbacks) progress(kind ProgressKind, name string) {
	if cb.OnProgress != nil {
		cb.OnProgress(Progress{Kind: kind, Name: name})
		return
	}
	if cb.OnStatus == nil {
		return
	}
	switch kind {
	case ProgressTask:
		cb.OnStatus(fmt.Sprintf("submitting task: %s", name))
	case ProgressPlan:
		cb.OnStatus(fmt.Sprintf("starting plan: %s", name))
	case ProgressTool:
		cb.OnStatus(fmt.Sprintf("running tool %s…", name))
	case ProgressRoute:
		cb.OnStatus(fmt.Sprintf("routing to %s…", name))
	case ProgressExec:
		cb.OnStatus(fmt.Sprintf("running %s…", name))
	case ProgressJudge:
		cb.OnStatus(fmt.Sprintf("reviewing result (%s)…", name))
	}
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
func (e *Engine) AskTurns(ctx context.Context, history []entry.Turn, prompt, workDir string, authorize bool, cb StreamCallbacks) (res *Result, err error) {
	client := e.client.Load()

	// Bill the commander model's own token consumption for this ask into the
	// delegation metrics once it finishes (whatever the outcome), so the
	// panel's tokens column shows entry-model cost alongside adapter
	// delegations. The record survives the ask's context via WithoutCancel.
	usageBefore := client.Usage()
	askStart := time.Now()
	defer func() {
		// Same numbers, two consumers: the result carries them back to the
		// caller (the CLI's closing "1.8s · 1.2k tokens" line) and the metrics
		// row bills them.
		if res != nil {
			d := client.Usage().Sub(usageBefore)
			res.InputTokens, res.OutputTokens = d.InputTokens, d.OutputTokens
			res.Latency = time.Since(askStart)
			// Reasoning backstop (D14): every return path funnels through
			// here, so one strip covers the Answer this engine hands to
			// conversation history and the panel, even if a future provider
			// path forgets the per-parse removal.
			res.Answer = entry.StripThinking(res.Answer)
		}
		e.recordEntryUsage(context.WithoutCancel(ctx), res, client, usageBefore, time.Since(askStart))
	}()

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
	// memory, so nothing is loaded at all. Project memory never enters this
	// prompt either; the execution path loads it selectively (A1), not here.
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
	var classifyOpts []entry.ClassifyOption
	if e.replyASCII {
		classifyOpts = append(classifyOpts, entry.WithASCIIOnly())
	}
	const maxRounds = 6
	for round := 0; round < maxRounds; round++ {
		var out entry.Output
		var err error
		if cb.OnDelta != nil || cb.OnReasoning != nil {
			out, err = entry.ClassifyStreamWithTools(ctx, client, devices, conversationMemory, turns, reg, cb.OnDelta, cb.OnReasoning, classifyOpts...)
		} else {
			out, err = entry.ClassifyTurnsWithTools(ctx, client, devices, conversationMemory, turns, reg, classifyOpts...)
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
			cb.progress(ProgressTask, out.Task.Title)
			return e.submitTask(out.Task, prompt, authorize, workDir, cb), nil
		case entry.KindPlan:
			cb.progress(ProgressPlan, out.Plan.Goal)
			return e.startClassifiedPlan(ctx, out.Plan, authorize)
		case entry.KindToolCall:
			cb.progress(ProgressTool, out.Tool.Tool)
			// Execute against the same registry snapshot classification saw:
			// a mid-ask SetMCPCommand swap would otherwise make the model's
			// tool call hit a registry that no longer knows it.
			result := executeTool(ctx, reg, out.Tool)
			turns = appendToolTurns(turns, out.Tool, out.Note, result)
		default:
			return &Result{Kind: "answer", Answer: out.Answer}, nil
		}
	}

	// Round budget exhausted without a converged intent: run one final
	// tool-free call over the accumulated history. Without tools the model
	// can only answer in text — the tools already ran and their results are
	// in the turns — so the ask converges to something useful instead of
	// surfacing a loop error to the user.
	var final entry.Output
	var ferr error
	if cb.OnDelta != nil || cb.OnReasoning != nil {
		final, ferr = entry.ClassifyStreamWithTools(ctx, client, devices, conversationMemory, turns, nil, cb.OnDelta, cb.OnReasoning, classifyOpts...)
	} else {
		final, ferr = entry.ClassifyTurns(ctx, client, devices, conversationMemory, turns, classifyOpts...)
	}
	if ferr != nil {
		return nil, fmt.Errorf("reached max tool rounds (%d): %w", maxRounds, ferr)
	}
	if final.Kind == entry.KindTask {
		if e.sched == nil {
			return &Result{Kind: "answer", Answer: fmt.Sprintf("已连续调用 %d 轮工具未收敛；模型最终建议任务「%s」，但当前未加载能力卡片，无法提交。", maxRounds, final.Task.Title)}, nil
		}
		cb.progress(ProgressTask, final.Task.Title)
		return e.submitTask(final.Task, prompt, authorize, workDir, cb), nil
	}
	if final.Kind == entry.KindPlan {
		if e.sched == nil {
			return &Result{Kind: "answer", Answer: fmt.Sprintf("已连续调用 %d 轮工具未收敛；模型最终建议多阶段计划「%s」，但当前未加载能力卡片，无法启动。", maxRounds, final.Plan.Goal)}, nil
		}
		cb.progress(ProgressPlan, final.Plan.Goal)
		return e.startClassifiedPlan(ctx, final.Plan, authorize)
	}
	return &Result{Kind: "answer", Answer: final.Answer}, nil
}

// WorkPath returns the configured work directory — the project workspace
// panel sessions execute in. It lets the panel pin non-repo sessions to the
// work path so the memory wall (§17.2) holds for them too.
func (e *Engine) WorkPath() string { return e.cfg.Storage.WorkPath }

// EnqueueTask routes a directly-created task (the panel's board "new task"
// form) through the async queue: it lands in queued and the scheduler starts
// it when resources allow. Needs a capability card, like task submission.
func (e *Engine) EnqueueTask(ctx context.Context, in core.TaskInput, q core.QueueSpec) (core.Task, error) {
	if e.sched == nil {
		return core.Task{}, fmt.Errorf("task creation requires a capability card (engine built without CardPath)")
	}
	return e.sched.Enqueue(ctx, in, q)
}

// StartPlan hands a multi-stage plan to the scheduler core, which creates one
// task per stage and releases the ones with no dependencies. It is the plan
// plane's only entry point outside the daemon's own completion hooks: without it
// the flagship cross-device pipeline — develop where a coding agent lives, train
// where the GPU lives, report where the user is — was reachable only from a test.
// Needs a capability card for the same reason task submission does: a plan whose
// stages cannot be routed is a plan that cannot start.
func (e *Engine) StartPlan(ctx context.Context, p plan.Plan, q core.QueueSpec) (string, error) {
	if e.sched == nil {
		return "", fmt.Errorf("starting a plan requires a capability card (engine built without CardPath)")
	}
	return e.sched.StartPlan(ctx, p, q)
}

// PlanStages returns every stage of one plan, for following a run.
func (e *Engine) PlanStages(ctx context.Context, planID string) ([]core.Task, error) {
	if e.sched == nil {
		return nil, fmt.Errorf("reading a plan requires a capability card (engine built without CardPath)")
	}
	return e.sched.TaskStore().PlanStages(ctx, planID)
}

// startClassifiedPlan turns a model-emitted plan into a running pipeline. It is
// the other half of the plan entry point: `panda plan run` covers the pipeline
// you keep as a file, this covers the one you ask for in a sentence — which is
// the case the project exists for, since the point of a plan is not having to
// visit three machines yourself.
//
// The plan's `authorize` flag is deliberately ignored: a stage never carries
// tier-2 consent (core.StartPlan), so an irreversible stage parks in review for
// a person instead of inheriting a blanket approval given to the whole sentence.
// Consent for one shell command is not consent for a three-machine pipeline.
func (e *Engine) startClassifiedPlan(ctx context.Context, spec *entry.PlanSpec, _ bool) (*Result, error) {
	if e.sched == nil {
		return nil, fmt.Errorf("plan output requires a capability card (engine built without CardPath)")
	}
	p, err := plan.FromSpec(*spec)
	if err != nil {
		// A plan the model got wrong has created nothing, so the useful answer is
		// the defect itself rather than a failed run: the user (or the next turn)
		// can see that the stages did not hang together.
		return &Result{Kind: "answer", Answer: "计划无法执行：" + err.Error()}, nil
	}
	q := core.DefaultQueueSpec()
	// No work dir, for the same reason `panda plan run` sets none: a path on this
	// machine means nothing on the machine that runs the stage.
	q.WorkDir = ""
	planID, err := e.sched.StartPlan(ctx, p, q)
	if err != nil {
		return &Result{Kind: "plan", PlanID: planID, PlanGoal: p.Goal, Stderr: err.Error(), ExitCode: 1}, nil
	}
	stages, serr := e.sched.TaskStore().PlanStages(ctx, planID)
	if serr != nil {
		e.logger.Warn("askengine: read plan stages", "plan", planID, "err", serr)
	}
	// The per-stage classify_result events are traced inside core.StartPlan at
	// stage creation, before AdvancePlan releases anything — leading each
	// stage's own execution events in the orbit timeline.
	return &Result{Kind: "plan", PlanID: planID, PlanGoal: p.Goal, PlanStages: stages, OK: true}, nil
}

// gateAuthorized resolves the effective tier-2 consent for a task from the
// configured approval mode and any standing session authorization. It is the
// single decision point for the three modes so the semantics stay auditable
// (and unit-testable) in one place:
//
//   - never       — tier-2 runs as classified; consent is implied.
//   - on-request  — the default; consent is withheld until the user approves
//     at the inline gate (a tier-2 task parks in review otherwise), unless an
//     explicit session grant (--authorize / /authorize on) already consented.
//   - always      — same as on-request at this layer. The extra "confirm every
//     run" strictness is a UI concern enforced by the caller (it does not cache
//     a prior yes across turns); an explicit grant still satisfies the gate.
//
// A session grant (sessionAuthorized: --authorize / /authorize on) is an
// explicit standing consent and satisfies every mode.
func gateAuthorized(mode string, sessionAuthorized bool) bool {
	if mode == config.ApprovalModeNever {
		return true
	}
	return sessionAuthorized
}

// submitTask executes a classified task spec through the scheduler core and
// maps the outcome to a Result. In queue mode the task is Enqueued and the
// call returns immediately (TaskState "queued"); the queue scheduler starts
// it when resources allow and the session streams its progress. In inline
// mode workDir, when set, temporarily pins the core's execution directory (a
// session worktree); the configured work path is restored afterwards. The
// schedMu lock keeps concurrent inline submits from interleaving the
// work-dir swap.
func (e *Engine) submitTask(spec *entry.TaskSpec, prompt string, authorized bool, workDir string, cb StreamCallbacks) *Result {
	in := toTaskInput(spec)
	// Approval mode is the real tier-2 gate (design §16): "never" auto-consents
	// so an irreversible task runs as classified; "on-request"/"always" withhold
	// consent until the user approves at the inline gate below. A session-level
	// authorization (/authorize on, --authorize) is an explicit standing consent
	// and always satisfies the gate regardless of mode.
	authorized = gateAuthorized(e.cfg.Approval.NormalizedMode(), authorized)
	in.Authorized = authorized
	// classify_result is traced inside core's createTask — before routing,
	// before the queue claims the task — so no emission happens here.
	in.ClassifyKind = "task"
	if prompt != "" {
		// Carry the user's original words into the agent prompt as a fidelity
		// backstop: the intent above is the entry model's distillation, which
		// can drop detail. The raw query lets the agent recover it, and it
		// travels with the intent (persisted + delegated), so retries and
		// peers that pick up the task also see it.
		in.Intent += "\n\n用户原始请求（上下文参考，以任务指令为准）：\n" + prompt
	}
	if e.queueTasks {
		q := core.DefaultQueueSpec()
		q.WorkDir = workDir // travels per task; "" falls back to the core's work dir
		task, err := e.sched.Enqueue(e.schedCtx, in, q)
		if err != nil {
			return &Result{Kind: "task", TaskState: "failed", Stderr: err.Error(), ExitCode: 1}
		}
		return &Result{Kind: "task", TaskID: task.TaskID, TaskTitle: task.Title, TaskState: task.State}
	}
	e.schedMu.Lock()
	defer e.schedMu.Unlock()
	if workDir != "" {
		e.sched.SetWorkDir(workDir)
		defer e.sched.SetWorkDir(e.cfg.Storage.WorkPath)
	}
	// Bridge the core's lifecycle trace events to the caller's progress feed for
	// the duration of this synchronous run, so a blocking agent execution shows
	// routing → executing → judging instead of a frozen spinner. schedMu already
	// serializes runs, so the observer sees only this ask's events; it is cleared
	// on return. A caller with no progress sink installs nothing.
	if cb.OnProgress != nil || cb.OnStatus != nil {
		store := e.sched.TaskStore()
		store.SetOnEvent(func(_, typ string, data any) {
			if kind, name, ok := progressForEvent(typ, data); ok {
				cb.progress(kind, name)
			}
		})
		defer store.SetOnEvent(nil)
	}
	task, result, err := e.sched.Submit(e.schedCtx, in)
	if err != nil {
		return &Result{Kind: "task", TaskState: "failed", Stderr: err.Error(), ExitCode: 1}
	}
	res := &Result{
		Kind:      "task",
		TaskID:    task.TaskID,
		TaskTitle: task.Title,
		TaskState: task.State,
		OK:        result.OK,
		Stdout:    result.Stdout,
		Stderr:    result.Stderr,
		ExitCode:  result.ExitCode,
	}
	// Inline approval closure: a tier-2 task with no standing consent parks in
	// review with an authorization-refusal reason. Turn that dead end into a
	// decision — consult the caller's OnApproval, and on a yes re-run the same
	// task authorized in one round-trip (agent scheduling completes without any
	// background scheduler). A caller with no synchronous callback gets a
	// NeedsApproval Result to handle on its own event loop.
	if !authorized && task.State == core.StateReview && commander.IsAuthorizationRefusal(result.Stderr) {
		req := ApprovalRequest{TaskID: task.TaskID, Title: task.Title, Intent: in.Intent, Reason: result.Stderr}
		if cb.OnApproval == nil {
			res.NeedsApproval = true
			res.Approval = &req
			return res
		}
		if cb.OnApproval(req) {
			return e.resumeLocked(req.TaskID)
		}
	}
	return res
}

// progressForEvent maps a scheduler-core trace event to a Progress the caller
// can render, or reports ok=false for events with no live-progress meaning. It
// is the one place that knows which core event types (internal/core/trace.go,
// state.go) correspond to the CLI's routing/executing/judging phases. Event
// data arrives as it was recorded — a map before marshaling, or JSON bytes
// after — so it is read through eventField, which handles both.
func progressForEvent(typ string, data any) (ProgressKind, string, bool) {
	switch typ {
	case core.EvRouteDecision:
		name := eventField(data, "target_node")
		if name == "" {
			name = eventField(data, "action") // "local" when no target node
		}
		return ProgressRoute, name, true
	case core.EvExecAgentStart:
		name := eventField(data, "agent")
		if name == "" {
			name = eventField(data, "adapter")
		}
		return ProgressExec, name, true
	case core.EvSupervisionRound:
		name := eventField(data, "verdict")
		return ProgressJudge, name, true
	}
	return "", "", false
}

// eventField extracts a string field from a recorded event's data, which is
// either a map[string]any (pre-marshal) or JSON bytes (post-marshal in the
// state-transition path). Missing/absent yields "".
func eventField(data any, key string) string {
	switch v := data.(type) {
	case map[string]any:
		if s, ok := v[key].(string); ok {
			return s
		}
	case []byte:
		var m map[string]any
		if json.Unmarshal(v, &m) == nil {
			if s, ok := m[key].(string); ok {
				return s
			}
		}
	}
	return ""
}

// resumeLocked re-runs an approved review-parked task synchronously and maps
// the outcome to a Result. The caller must hold schedMu (submitTask does), so
// any pinned session work dir is still in effect for the re-run.
func (e *Engine) resumeLocked(taskID string) *Result {
	task, result, err := e.sched.ResumeApproved(e.schedCtx, taskID)
	if err != nil {
		return &Result{Kind: "task", TaskID: taskID, TaskState: "review", Stderr: err.Error(), ExitCode: 1}
	}
	return &Result{
		Kind:      "task",
		TaskID:    task.TaskID,
		TaskTitle: task.Title,
		TaskState: task.State,
		OK:        result.OK,
		Stdout:    result.Stdout,
		Stderr:    result.Stderr,
		ExitCode:  result.ExitCode,
	}
}

// ResumeApproved re-runs a task the user approved out-of-band (a NeedsApproval
// Result the caller surfaced and confirmed) with tier-2 consent, on this node,
// synchronously. It is the counterpart to submitTask's inline OnApproval path
// for callers — the termios REPL — that prompt after the ask returns rather
// than from within a callback. workDir pins the re-run's execution directory
// (the session worktree), matching the original submission.
func (e *Engine) ResumeApproved(taskID, workDir string) *Result {
	if e.sched == nil {
		return &Result{Kind: "task", TaskState: "failed", Stderr: "task execution requires a capability card", ExitCode: 1}
	}
	e.schedMu.Lock()
	defer e.schedMu.Unlock()
	if workDir != "" {
		e.sched.SetWorkDir(workDir)
		defer e.sched.SetWorkDir(e.cfg.Storage.WorkPath)
	}
	return e.resumeLocked(taskID)
}

// recordEntryUsage bills the entry (commander) model's own token consumption
// for one ask into the delegation metrics, so the panel's tokens column
// reflects the commander's cost alongside adapter delegations. The executor
// label "entry:<model>" keeps the rows distinguishable; providers that do not
// report usage (delta zero) record nothing.
func (e *Engine) recordEntryUsage(ctx context.Context, res *Result, client *entry.Client, before entry.Usage, latency time.Duration) {
	delta := client.Usage().Sub(before)
	if delta.Total() == 0 {
		return
	}
	taskID := ""
	if res != nil {
		taskID = res.TaskID
	}
	store := core.NewTaskStore(e.db, e.logger)
	if err := store.RecordDelegationMetric(ctx, taskID, e.cfg.Node.Name, "entry:"+client.ModelName(),
		nil, true, latency.Milliseconds(), int(delta.Total())); err != nil {
		e.logger.Warn("askengine: record entry usage", "err", err)
	}
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
