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
		// same node (the daemon owns the stable identity and listener).
		sched := core.NewCore(db, core.EphemeralNodeID(cfg.Node.Name), card, schedulerTier(cfg.Node.ResourceClass), logger, cfg.Model)
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
func (e *Engine) AskTurns(ctx context.Context, history []entry.Turn, prompt, workDir string, authorize bool, cb StreamCallbacks) (res *Result, err error) {
	client := e.client.Load()

	// Bill the commander model's own token consumption for this ask into the
	// delegation metrics once it finishes (whatever the outcome), so the
	// panel's tokens column shows entry-model cost alongside adapter
	// delegations. The record survives the ask's context via WithoutCancel.
	usageBefore := client.Usage()
	askStart := time.Now()
	defer func() {
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
		if cb.OnDelta != nil {
			out, err = entry.ClassifyStreamWithTools(ctx, client, devices, conversationMemory, turns, reg, cb.OnDelta, classifyOpts...)
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
			if cb.OnStatus != nil {
				cb.OnStatus(fmt.Sprintf("submitting task: %s", out.Task.Title))
			}
			return e.submitTask(out.Task, prompt, authorize, workDir), nil
		case entry.KindPlan:
			if cb.OnStatus != nil {
				cb.OnStatus(fmt.Sprintf("starting plan: %s", out.Plan.Goal))
			}
			return e.startClassifiedPlan(ctx, out.Plan, authorize)
		case entry.KindToolCall:
			if cb.OnStatus != nil {
				cb.OnStatus(fmt.Sprintf("running tool %s…", out.Tool.Tool))
			}
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
	if cb.OnDelta != nil {
		final, ferr = entry.ClassifyStreamWithTools(ctx, client, devices, conversationMemory, turns, nil, cb.OnDelta, classifyOpts...)
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
		if cb.OnStatus != nil {
			cb.OnStatus(fmt.Sprintf("submitting task: %s", final.Task.Title))
		}
		return e.submitTask(final.Task, prompt, authorize, workDir), nil
	}
	if final.Kind == entry.KindPlan {
		if e.sched == nil {
			return &Result{Kind: "answer", Answer: fmt.Sprintf("已连续调用 %d 轮工具未收敛；模型最终建议多阶段计划「%s」，但当前未加载能力卡片，无法启动。", maxRounds, final.Plan.Goal)}, nil
		}
		if cb.OnStatus != nil {
			cb.OnStatus(fmt.Sprintf("starting plan: %s", final.Plan.Goal))
		}
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
	return &Result{Kind: "plan", PlanID: planID, PlanGoal: p.Goal, PlanStages: stages, OK: true}, nil
}

// submitTask executes a classified task spec through the scheduler core and
// maps the outcome to a Result. In queue mode the task is Enqueued and the
// call returns immediately (TaskState "queued"); the queue scheduler starts
// it when resources allow and the session streams its progress. In inline
// mode workDir, when set, temporarily pins the core's execution directory (a
// session worktree); the configured work path is restored afterwards. The
// schedMu lock keeps concurrent inline submits from interleaving the
// work-dir swap.
func (e *Engine) submitTask(spec *entry.TaskSpec, prompt string, authorized bool, workDir string) *Result {
	in := toTaskInput(spec)
	in.Authorized = authorized
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
	return res
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
