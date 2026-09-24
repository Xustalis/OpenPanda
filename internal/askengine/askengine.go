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
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/carddetect"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/mcp"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/plan"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/providers"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// Options tunes how the engine is built.
type Options struct {
	// CardPath points at capabilities.yaml. When empty, the engine derives the
	// path from config or the default location and lazily initializes the
	// scheduler on the first task dispatch or card mutation, so a missing
	// CardPath no longer restricts the engine to answers and memory tools.
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
	// Locale specifies the user's preferred language (en, zh-CN, etc.).
	// When empty, it is detected from the environment or config.
	Locale i18n.Locale
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
	skills   *skills.Store

	mcp        *mcp.Client
	mcpCommand string
	logger     *slog.Logger

	// sched powers task execution once initialized — either eagerly from
	// Options.CardPath/config or lazily on first use (tryAutoInitScheduler).
	// schedMu serializes task submission: a submit may temporarily pin the
	// core's work dir to a session worktree, which must not interleave.
	// (Queue mode never swaps the global work dir — it travels per task.)
	sched       *core.Core
	schedMu     sync.Mutex
	schedCtx    context.Context
	schedCancel context.CancelFunc

	// onReview is the embedder's "a task needs a human" hook (the panel's push
	// notification). It lives on the engine rather than on one store instance
	// because the review transition happens inside the scheduler core's own
	// store: a callback installed on a separately constructed TaskStore over
	// the same database would never fire. Holding it here also re-attaches it
	// when the scheduler is rebuilt (card reload), so a pending approval keeps
	// reaching the user.
	onReviewMu sync.RWMutex
	onReview   func(core.Task)

	// project is the ambient project: the one the user entered, which every task
	// this engine submits belongs to unless the classifier named a different one.
	// It is a field rather than a per-call argument because "which project am I
	// in" is state the user set once, and threading it through every Ask would
	// make the caller responsible for remembering it on every turn.
	//
	// projectDir is that project's work dir, used as the task's working directory
	// when the caller did not pin one of its own (a session worktree wins, since
	// it is the more specific choice).
	projectMu  sync.RWMutex
	project    string
	projectDir string
	// queueTasks mirrors Options.QueueTasks.
	queueTasks bool
	// asyncPeers mirrors Options.AsyncPeers.
	asyncPeers bool
	// replyASCII mirrors Options.ReplyASCII (per-engine classify option).
	replyASCII bool
	// cardPath mirrors Options.CardPath: the capabilities.yaml the engine
	// loaded its scheduler from, reported by the system_status tool.
	cardPath string
	// locale is the user's active UI/prompt locale.
	locale         i18n.Locale
	explicitLocale bool

	fallbacksMu sync.RWMutex
	fallbacks   []*entry.Client

	breakerMu sync.Mutex
	breaker   map[string]time.Time // modelName -> cooldown until
}

func (e *Engine) isModelHealthy(name string) bool {
	if name == "" {
		return true
	}
	e.breakerMu.Lock()
	defer e.breakerMu.Unlock()
	if e.breaker == nil {
		return true
	}
	until, ok := e.breaker[name]
	if !ok {
		return true
	}
	if time.Now().After(until) {
		delete(e.breaker, name)
		return true
	}
	return false
}

func (e *Engine) recordModelFailure(name string) {
	if name == "" {
		return
	}
	e.breakerMu.Lock()
	defer e.breakerMu.Unlock()
	if e.breaker == nil {
		e.breaker = make(map[string]time.Time)
	}
	e.breaker[name] = time.Now().Add(30 * time.Second)
}

func (e *Engine) recordModelSuccess(name string) {
	if name == "" {
		return
	}
	e.breakerMu.Lock()
	defer e.breakerMu.Unlock()
	if e.breaker != nil {
		delete(e.breaker, name)
	}
}

// healthyClient returns an active client: the primary if healthy, or the first
// healthy fallback when the primary is in circuit-breaker cooldown.
func (e *Engine) healthyClient() (*entry.Client, string) {
	client := e.client.Load()
	if client == nil || e.isModelHealthy(client.ModelName()) {
		return client, ""
	}
	for _, fb := range e.getFallbacks() {
		if fb.ModelName() == client.ModelName() {
			continue
		}
		if e.isModelHealthy(fb.ModelName()) {
			return fb, fb.ModelName()
		}
	}
	return client, ""
}

// getFallbacks returns a snapshot of the configured fallback clients.
func (e *Engine) getFallbacks() []*entry.Client {
	e.fallbacksMu.RLock()
	defer e.fallbacksMu.RUnlock()
	return append([]*entry.Client(nil), e.fallbacks...)
}

func (e *Engine) buildFallbacks(primary config.ModelConfig, db *sql.DB) []*entry.Client {
	if e.cfg == nil {
		return nil
	}
	var fallbacks []*entry.Client
	for _, m := range e.cfg.Models {
		p, hasProvider := providers.Lookup(m.Provider)
		if m.APIKey == "" && !m.NoAuth && (!hasProvider || !p.NoAuth) {
			continue
		}
		if m.Model == primary.Model && m.BaseURL == primary.BaseURL {
			continue
		}
		if fc, err := entry.NewClient(m); err == nil {
			if db != nil {
				fc.SetDiskCache(entry.NewDiskCache(db))
			}
			fallbacks = append(fallbacks, fc)
		}
	}
	return fallbacks
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
	e.cfg.Model = mc
	e.fallbacksMu.Lock()
	e.fallbacks = e.buildFallbacks(mc, e.db)
	e.fallbacksMu.Unlock()
	return nil
}

// ModelConfig returns the engine's current model configuration.
func (e *Engine) ModelConfig() config.ModelConfig { return e.cfg.Model }

// CancelTask cancels taskID and its subtree through the scheduler core when
// the engine has one, so the cancel reaches remote executors too. Without the
// core (or when the scheduler is down to a plain store) it degrades to the
// local row update — the same behaviour the direct store call had.
//
// The engine's core is an ephemeral sibling of the kernel daemon's: it shares
// the task database, and its bus connections are the dialed peers, so
// forwardCancelDownstream can deliver the task_cancel the daemon would have
// sent had the cancel arrived on the wire.
func (e *Engine) CancelTask(ctx context.Context, taskID string) ([]string, error) {
	if e.sched == nil {
		return core.NewTaskStore(e.db, e.logger).CancelCascade(ctx, taskID)
	}
	return e.sched.CancelTree(ctx, taskID)
}

// Config returns the engine's loaded configuration.
func (e *Engine) Config() *config.Config { return e.cfg }

// TaskStore returns the task store on the engine's database, for callers that
// need read-level access (reference resolution) without a scheduler core.
func (e *Engine) TaskStore() *core.TaskStore {
	return core.NewTaskStore(e.db, e.logger)
}

// SetOnReview installs the callback fired when a task enters review — i.e. when
// work is waiting on a human decision. It is attached to the store the scheduler
// core actually transitions tasks on (and re-attached across card reloads), so
// an embedder's notification path cannot silently miss a pending approval. The
// callback must not block; it may not decide the approval, only announce it.
func (e *Engine) SetOnReview(fn func(core.Task)) {
	e.onReviewMu.Lock()
	e.onReview = fn
	e.onReviewMu.Unlock()
	e.schedMu.Lock()
	defer e.schedMu.Unlock()
	if e.sched != nil && e.sched.TaskStore() != nil {
		e.sched.TaskStore().SetOnReview(fn)
	}
}

// SetLocale updates the active user locale for this engine.
func (e *Engine) SetLocale(loc i18n.Locale) {
	if loc != "" {
		e.locale = loc
		e.explicitLocale = true
	}
}

// Locale returns the active user locale for this engine.
func (e *Engine) Locale() i18n.Locale {
	if e.locale != "" {
		return e.locale
	}
	return i18n.Detect()
}

// Result is the outcome of one Ask call.
type Result struct {
	// Kind is "answer", "task" or "plan" — the final converged intent.
	Kind string
	// Answer carries the model's text reply when Kind == "answer".
	Answer string
	// Note is an incidental model note emitted alongside a tool call.
	Note string
	// Thought is the model's reasoning/thinking chain before producing the result.
	Thought string

	// Task fields, valid when Kind == "task".
	TaskID    string
	TaskTitle string // for conversation history: "the task that ran" in one line
	TaskState string
	OK        bool
	Stdout    string
	Stderr    string
	ExitCode  int
	Agent     string
	Model     string
	Injected  bool
	// Executor is the node that actually ran the task — this node's id for a
	// local run, the peer's id for a delegated one. Empty when the result
	// never reached an executor (route miss, early failure).
	Executor string
	// EntryModel is the model that served this ask's entry calls (triage,
	// classify, answer stream) — the configured primary, or the fallback the
	// circuit breaker routed to mid-ask. UI surfaces render it next to the
	// execution attribution so "which model did what" is never a guess.
	EntryModel string
	// Report is the LLM-generated summary of the task outcome. It is filled
	// by SummarizeResult after every inline task (success or failure) so the
	// user sees a human-readable summary instead of raw stdout/stderr. A
	// model failure leaves it empty, and the caller falls back to raw output.
	Report string

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
	Cost         float64

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

	reg := buildToolRegistry(e, e.hermes, e.projects, e.remind)
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

	client, err := entry.NewClient(cfg.Model)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("askengine: model client: %w", err)
	}
	// Disk cache for entry-model decisions (classify/supervise): identical
	// inputs skip the LLM call entirely. Best-effort by design.
	client.SetDiskCache(entry.NewDiskCache(db))

	var skillStore *skills.Store
	if cfg != nil && strings.TrimSpace(cfg.Storage.SkillsPath) != "" {
		skillStore = skills.NewStore(cfg.Storage.SkillsPath)
		_ = skillStore.EnsureBuiltins()
	}

	var explicitLocale bool
	loc := opts.Locale
	if loc != "" {
		explicitLocale = true
	} else if cfg != nil && cfg.UI.Locale != "" {
		loc = i18n.Parse(cfg.UI.Locale)
		if loc != "" {
			explicitLocale = true
		}
	}
	if loc == "" {
		loc = i18n.Detect()
	}

	e := &Engine{
		cfg:            cfg,
		db:             db,
		injector:       injector,
		hermes:         hermes,
		projects:       projects,
		remind:         remind,
		skills:         skillStore,
		logger:         logger,
		queueTasks:     opts.QueueTasks,
		asyncPeers:     opts.AsyncPeers,
		replyASCII:     opts.ReplyASCII,
		cardPath:       opts.CardPath,
		locale:         loc,
		explicitLocale: explicitLocale,
	}
	e.fallbacks = e.buildFallbacks(cfg.Model, db)
	// The registry is built with the engine itself: the management tools hold
	// it and dereference lazily, so a scheduler attached below (or never,
	// without CardPath) is seen at call time.
	e.registry = buildToolRegistry(e, hermes, projects, remind)
	if e.skills != nil {
		registerSkillTools(e.registry, e)
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

	cardTarget := opts.CardPath
	explicitCard := opts.CardPath != ""
	if cardTarget == "" && cfg != nil {
		cardTarget = cfg.EffectiveCardPath()
	}
	if cardTarget == "" {
		cardTarget = config.DefaultCardTarget("")
	}
	if cardTarget != "" {
		if _, _, err := carddetect.EnsureCard(cardTarget); err != nil {
			if fallback := config.DefaultCardTarget(""); fallback != "" && fallback != cardTarget {
				if _, _, fbErr := carddetect.EnsureCard(fallback); fbErr == nil {
					cardTarget = fallback
					_ = e.initSchedulerLocked(cardTarget)
				}
			}
			if e.sched == nil && explicitCard {
				e.Close()
				return nil, fmt.Errorf("askengine: load capabilities: %w", err)
			}
		} else {
			if err := e.initSchedulerLocked(cardTarget); err != nil {
				if explicitCard {
					e.Close()
					return nil, fmt.Errorf("askengine: init scheduler: %w", err)
				}
				logger.Warn("init scheduler failed", "card", cardTarget, "err", err)
			}
		}
	}

	return e, nil
}

// tryAutoInitScheduler attempts to dynamically ensure a capability card and
// initialize the scheduler if it has not yet been started.
func (e *Engine) tryAutoInitScheduler() {
	e.schedMu.Lock()
	defer e.schedMu.Unlock()
	if e.sched != nil {
		return
	}
	target := e.cardPath
	if target == "" && e.cfg != nil {
		target = e.cfg.EffectiveCardPath()
	}
	if target == "" {
		target = config.DefaultCardTarget("")
	}
	if target == "" {
		return
	}
	if _, _, err := carddetect.EnsureCard(target); err == nil {
		if err := e.initSchedulerLocked(target); err != nil {
			e.logger.Warn("askengine: auto-init scheduler failed", "card", target, "err", err)
		}
	} else if fallback := config.DefaultCardTarget(""); fallback != "" && fallback != target {
		if _, _, err2 := carddetect.EnsureCard(fallback); err2 == nil {
			if err := e.initSchedulerLocked(fallback); err != nil {
				e.logger.Warn("askengine: auto-init scheduler failed", "card", fallback, "err", err)
			}
		}
	}
}

func (e *Engine) initSchedulerLocked(cardPath string) error {
	card, err := ledger.LoadCard(cardPath)
	if err != nil {
		return fmt.Errorf("askengine: load capabilities: %w", err)
	}
	// Same pruning the daemon does: a native ability whose command is not
	// installed here would win the native plan and fail at exec.
	if dropped := card.PruneUnavailableNative(); len(dropped) > 0 {
		e.logger.Warn("native abilities dropped: command not found on this host", "ids", dropped)
	}
	if dropped := card.PruneUnavailableActuators(); len(dropped) > 0 {
		e.logger.Warn("actuators dropped: command not found on this host", "ids", dropped)
	}
	// Mirror the daemon's card enrichment: kind/identity come from the
	// config (the card file may omit them), and the node is registered
	// under the same stable runtime ID the daemon uses. Without this, a
	// fresh database shows an empty device list to the entry model —
	// every ask degrades to "no devices available" even though this
	// process can execute the card's tasks locally. The upsert is
	// idempotent with the daemon's own registration.
	card.NodeKind = e.cfg.Node.Kind
	card.NodeIdentity = e.cfg.Node.EffectiveIdentity()
	stableID := core.RuntimeNodeID(e.cfg.Node.Name, e.cfg.Node.Kind, card.NodeIdentity)
	if err := ledger.Register(e.db, card, stableID, schedulerTier(e.cfg.Node.ResourceClass)); err != nil {
		e.logger.Warn("self-register failed", "node", stableID, "err", err)
	}
	// The engine's scheduler uses stableID so tasks created, claimed and queued
	// belong to this machine's stable identity rather than a fleeting hash
	// that leaves tasks stranded when the process exits.
	sched := core.NewCore(e.db, stableID, card, schedulerTier(e.cfg.Node.ResourceClass), e.logger, e.cfg.Model)
	sched.SetRouterPolicy(e.cfg.Injection, e.cfg.Routing)
	sched.AttachSupervisor(e.cfg.Model)
	if e.skills != nil {
		sched.SetMemoryStores(e.injector, memory.NewDaily(e.hermes.WarmDir()), e.skills)
	}
	sched.SetProjectStores(projectstore.NewStore(e.db), e.cfg.Storage.ProjectsPath)
	sched.SetWorkDir(e.cfg.Storage.WorkPath)
	sched.SetHostStatePaths(hostStatePaths(e.cfg))
	sched.SetSharedSecret(e.cfg.Network.SharedSecret)
	sched.SetTimeouts(e.cfg.Timeouts)

	if e.schedCancel != nil {
		e.schedCancel()
	}
	schedCtx, cancel := context.WithCancel(context.Background())
	e.sched = sched
	e.schedCtx = schedCtx
	e.schedCancel = cancel
	e.cardPath = cardPath
	// Re-arm the review hook on the new core's store: the notification that a
	// task is waiting for the user must not be lost to a card reload.
	e.onReviewMu.RLock()
	fn := e.onReview
	e.onReviewMu.RUnlock()
	if fn != nil && sched.TaskStore() != nil {
		sched.TaskStore().SetOnReview(fn)
	}

	// Clean up stale leases from interrupted runs so tasks aren't stranded in running.
	if sched.TaskStore() != nil {
		if expired, err := sched.TaskStore().ExpireTasks(context.Background()); err != nil {
			e.logger.Debug("expire stale tasks at startup", "err", err)
		} else if len(expired) > 0 {
			e.logger.Info("expired stale tasks from previous run", "count", len(expired), "tasks", expired)
		}
	}

	// Only queue-mode surfaces own a background consumer. One-shot ask/REPL
	// engines submit inline and must not unexpectedly drain persisted work.
	if e.queueTasks {
		sched.StartQueueScheduler(schedCtx)
		// A queue engine executes tasks off the shared store, so it also owns
		// the task-lifecycle sweeps: without them a waiting_context park never
		// times out, an orphaned forward is never rescued, and an expired
		// lease is never enforced — the task would stall with nobody told.
		go sched.RunTaskMonitor(schedCtx)
	}
	// Every engine reconciles its own executions against the store: a task
	// cancelled or failed by another process must stop here too, not run on
	// as a zombie the user believes is still working.
	go sched.RunReconcile(schedCtx)

	if e.asyncPeers {
		for _, peer := range e.cfg.Network.Peers {
			go func(p string) {
				if err := sched.DialPeer(schedCtx, p); err != nil {
					e.logger.Debug("peer dial failed", "peer", p, "err", err)
				}
			}(peer)
		}
	} else {
		var wg sync.WaitGroup
		for _, peer := range e.cfg.Network.Peers {
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				if err := sched.DialPeer(schedCtx, p); err != nil {
					e.logger.Debug("peer dial failed", "peer", p, "err", err)
				}
			}(peer)
		}
		wg.Wait()
		if len(e.cfg.Network.Peers) > 0 {
			waitForPeers(schedCtx, e.db, 2*time.Second)
		}
	}
	return nil
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
	// GetSteer is polled at the start of each round to consume user steering ideas
	// injected at runtime, appending them to conversation turns.
	GetSteer func() string
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
//
// Round/Budget locate a supervision step inside its loop ("round 2/5"): set
// on exec/judge milestones when the trace event carries them, zero otherwise.
// Renderers stay silent about single-round runs (Budget ≤ 1).
type Progress struct {
	Kind   ProgressKind
	Name   string
	Model  string
	Round  int
	Budget int
}

// roundNote phrases the loop position for the English status fallback; "" for
// single-round runs, which gain nothing from a "round 1/1" decoration.
func roundNote(p Progress) string {
	if p.Budget <= 1 {
		return ""
	}
	return fmt.Sprintf(" (%d/%d)", p.Round, p.Budget)
}

// progress reports one event to whichever callback the caller supplied. The
// English prose lives here, in one place, so it stays a fallback rather than
// the only phrasing available.
func (cb StreamCallbacks) progress(p Progress) {
	if cb.OnProgress != nil {
		cb.OnProgress(p)
		return
	}
	if cb.OnStatus == nil {
		return
	}
	switch p.Kind {
	case ProgressTask:
		cb.OnStatus(fmt.Sprintf("submitting task: %s", p.Name))
	case ProgressPlan:
		cb.OnStatus(fmt.Sprintf("starting plan: %s", p.Name))
	case ProgressTool:
		cb.OnStatus(fmt.Sprintf("running tool %s…", p.Name))
	case ProgressRoute:
		cb.OnStatus(fmt.Sprintf("routing to %s…", p.Name))
	case ProgressExec:
		extra := ""
		if p.Model != "" {
			extra = fmt.Sprintf(" (%s)", p.Model)
		}
		cb.OnStatus(fmt.Sprintf("running %s%s%s…", p.Name, extra, roundNote(p)))
	case ProgressJudge:
		// A judge_start marker arrives with no verdict yet; phrase it without
		// the empty parens the missing verdict used to leave behind.
		if p.Name == "" {
			cb.OnStatus(fmt.Sprintf("reviewing result%s…", roundNote(p)))
			return
		}
		cb.OnStatus(fmt.Sprintf("reviewing result (%s)%s…", p.Name, roundNote(p)))
	}
}

// Ask runs one prompt through the unified entry model. A tool_call intent is
// executed (memory/MCP tools) and its result fed back, converging to a final
// answer or task; a task is submitted through the scheduler core (local or
// delegated) and its outcome returned.
func (e *Engine) Ask(ctx context.Context, prompt string, authorize bool) (*Result, error) {
	return e.AskTurns(ctx, nil, prompt, "", authorize, StreamCallbacks{})
}

// SetRouterPolicy re-applies the injection and routing policy to the engine's
// scheduler core, so a policy edited at runtime (the console's settings view)
// takes effect without a restart. A no-op on an engine with no card, which has no
// router to configure.
func (e *Engine) SetRouterPolicy(injection config.InjectionConfig, routing config.RoutingConfig) {
	if e.sched == nil {
		return
	}
	e.sched.SetRouterPolicy(injection, routing)
}

// SetProject names the ambient project for the tasks this engine submits, and
// the directory they run in. Both may be empty, which is "not in a project".
func (e *Engine) SetProject(name, workDir string) {
	e.projectMu.Lock()
	e.project, e.projectDir = name, workDir
	e.projectMu.Unlock()
}

// Project reports the ambient project and its work dir.
func (e *Engine) Project() (string, string) {
	e.projectMu.RLock()
	defer e.projectMu.RUnlock()
	return e.project, e.projectDir
}

// AskScope is immutable context attached to one ask. Project and WorkDir are
// threaded through every classified and tool-call task path instead of mutating
// the Engine's ambient project or the scheduler Core's default directory.
type AskScope struct {
	Project string
	WorkDir string
	// Mode is the slash-prefix interaction mode the user picked for this turn
	// ("goal", "plan", "spec"); empty leaves classification to the model.
	Mode string

	ambientProjectFallback bool
}

// AskTurns is the backward-compatible session-aware entry point. Callers that
// own explicit project context should use AskTurnsScoped.
func (e *Engine) AskTurns(ctx context.Context, history []entry.Turn, prompt, workDir string, authorize bool, cb StreamCallbacks) (res *Result, err error) {
	return e.AskTurnsScoped(ctx, history, prompt, AskScope{WorkDir: workDir, ambientProjectFallback: true}, authorize, cb)
}

// AskTurnsMode is AskTurns plus the slash-prefix interaction mode the user
// picked (/goal, /plan, /spec). "" is exactly AskTurns.
func (e *Engine) AskTurnsMode(ctx context.Context, history []entry.Turn, prompt, workDir, mode string, authorize bool, cb StreamCallbacks) (res *Result, err error) {
	return e.AskTurnsScoped(ctx, history, prompt, AskScope{WorkDir: workDir, Mode: mode, ambientProjectFallback: true}, authorize, cb)
}

// AskTurnsScoped is the session-aware ask with request-scoped project/workspace.
func (e *Engine) AskTurnsScoped(ctx context.Context, history []entry.Turn, prompt string, scope AskScope, authorize bool, cb StreamCallbacks) (res *Result, err error) {
	workDir := scope.WorkDir
	client, fallbackUsed := e.healthyClient()
	if fallbackUsed != "" {
		e.logger.Info("askengine: primary model in circuit breaker cooldown, routing directly to fallback", "primary", e.client.Load().ModelName(), "fallback", fallbackUsed)
		cb.progress(Progress{Kind: ProgressRoute, Name: fallbackUsed})
	}

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
			res.Cost = client.EstimateCost(d.InputTokens, d.OutputTokens)
			res.EntryModel = client.ModelName()
			if fallbackUsed != "" && res.Note == "" {
				res.Note = fmt.Sprintf("主模型不可用，已自动切换至备用模型: %s", fallbackUsed)
			}
			// Reasoning backstop (D14): every return path funnels through
			// here, so one strip covers the Answer this engine hands to
			// conversation history and the panel, even if a future provider
			// path forgets the per-parse removal.
			res.Answer = entry.StripThinking(res.Answer)
		}
		e.recordEntryUsage(context.WithoutCancel(ctx), res, client, usageBefore, time.Since(askStart))
	}()

	turns := make([]entry.Turn, 0, len(history)+1)
	turns = append(turns, history...)
	turns = append(turns, entry.Turn{Role: "user", Content: prompt})

	effectiveLocale := e.locale
	if !e.explicitLocale && !e.replyASCII {
		if containsHan(prompt) {
			effectiveLocale = i18n.ChineseSimp
		}
	}

	var classifyOpts []entry.ClassifyOption
	if e.replyASCII {
		classifyOpts = append(classifyOpts, entry.WithASCIIOnly())
	}
	if effectiveLocale != "" {
		classifyOpts = append(classifyOpts, entry.WithLocale(effectiveLocale))
	}
	if scope.Mode != "" {
		classifyOpts = append(classifyOpts, entry.WithRequestMode(scope.Mode))
	}

	// Memory wall (design §17.2): Hermes personal memory enters only
	// project-free conversations. A pinned workDir marks a project/workspace
	// conversation (a session's worktree or the shared work path) — its
	// classification and any task it spawns must stay untainted by personal
	// memory, so nothing is loaded at all. Project memory never enters this
	// prompt either; the execution path loads it selectively (A1), not here.
	conversationMemory := ""
	if workDir == "" && e.injector != nil {
		var merr error
		conversationMemory, merr = e.injector.Conversation(prompt)
		if merr != nil {
			e.logger.Warn("load memory", "err", merr)
		}
	}

	reg := e.currentRegistry()

	// Tier 1 Fast-Path Triage: if this is a standalone conceptual/conversational
	// query without prior task state or action intent, stream directly with a micro-prompt
	// (including personal memory), bypassing device ledger scanning, MCP tools schemas,
	// and task JSON rules. The gate is the triage verdict itself, NOT registry
	// emptiness: New() always registers the built-in tools, so a registry-based
	// check would make this path unreachable in every real engine (the tests
	// that hand-build an Engine are the only place a nil registry ever occurs).
	if len(history) == 0 {
		if triage := entry.FastTriage(prompt, history); triage.IsFastPath {
			fastSystem := entry.FastPathPrompt(e.replyASCII, effectiveLocale)
			if conversationMemory != "" {
				fastSystem += "\n\n═══ User Memory ═══\n" + conversationMemory
			}
			var resp entry.Response
			var rerr error
			streamedAny := false
			wrapDelta := func(chunk string) {
				if chunk != "" {
					streamedAny = true
				}
				if cb.OnDelta != nil {
					cb.OnDelta(chunk)
				}
			}
			if cb.OnDelta != nil || cb.OnReasoning != nil {
				resp, rerr = client.StreamTurnsWithTools(ctx, fastSystem, turns, nil, wrapDelta, cb.OnReasoning)
			} else {
				resp, rerr = client.CompleteTurnsWithTools(ctx, fastSystem, turns, nil)
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if rerr == nil && resp.Text != "" && !entry.ContainsDSMLToolCall(resp.Text) {
				return &Result{Kind: "answer", Answer: resp.Text}, nil
			}
			if streamedAny {
				if rerr != nil {
					return nil, rerr
				}
				// The stream guard withheld DSML markup from the user's view;
				// hand back the prose around it, never the raw tags.
				return &Result{Kind: "answer", Answer: entry.StripDSMLToolCalls(resp.Text)}, nil
			}
			// If fast path fails before emitting any token — or answered with
			// textual tool-call markup the stream guard suppressed — fall
			// through to the full pipeline, whose tool roster lets the model
			// make a real call.
		}
	}
	// The tool gate shares the task gate's consent semantics: "never"
	// auto-consents, an explicit session grant (--authorize / /authorize /
	// the panel's authorize field) satisfies on-request, and "always"
	// withholds consent at submission no matter the grant — every tier-2
	// operation is decided per-action in the foreground.
	toolAuthorized := gateAuthorized(e.cfg.Approval.NormalizedMode(), authorize)
	const maxRounds = 6
	// maxTasks bounds how many sub-agent rounds one ask may run. A task round
	// is minutes of work and a full agent transcript of tokens, so its budget
	// is separate from (and inside) the round budget: the loop may still spend
	// its remaining rounds converging on a report.
	const maxTasks = 3
	taskRounds := 0
	// lastTask keeps the most recent task outcome so the converged Result
	// carries the task fields (id/state/output) alongside the model's report.
	var lastTask *Result
	finalizeLastTask := func(t *Result) *Result {
		if t != nil && strings.TrimSpace(t.Answer) == "" && strings.TrimSpace(t.Report) == "" {
			if report, rerr := entry.SummarizeResult(ctx, client, t.TaskTitle, "", t.OK, t.ExitCode, t.Stdout, t.Stderr, effectiveLocale); rerr == nil {
				t.Report = report
			}
		}
		return t
	}
	var accumulatedReasoning strings.Builder
	// toolDigest records each executed tool call's result so a late model
	// failure degrades to an honest summary of work already done rather than a
	// bare loop error (the "reached max tool rounds" report that hid six
	// successfully-executed queue operations).
	var toolDigest []string
	digestOrErr := func(err error) (*Result, error) {
		if digest := toolResultsDigest(toolDigest, effectiveLocale); digest != "" {
			if lastTask != nil {
				lastTask.Answer = digest
				return finalizeLastTask(lastTask), nil
			}
			return &Result{Kind: "answer", Answer: digest}, nil
		}
		return nil, err
	}
	// Devices visible to classification: the local capability directory
	// (populated by the daemon's heartbeats and our own peer dials).
	devices, err := ledger.Query(e.db, "online", "")
	if err != nil {
		devices = nil
	}

	// Per-ask registry copy carrying task_submit: the dispatch bridge for
	// entry models behind compatible endpoints that drive everything through
	// tool calls and never emit the task JSON directive (the observed failure:
	// six rounds of taskq_list poking, zero tasks). Registered for every ask —
	// intent-keyword gating was tried and abandoned: any phrasing the gate
	// missed left tool-call-only models with no legal way to create a task.
	// The tool closes over this ask's prompt/workDir/consent, and submission
	// goes through the same submitTask path (scheduler, approval gate) as a
	// KindTask directive.
	var taskCapture taskDispatchCapture
	if reg != nil {
		reg = reg.Copy()
		reg.Register(e.dispatchTaskTool(prompt, scope, authorize, cb, &taskCapture, effectiveLocale))
	}

rounds:
	for round := 0; round < maxRounds; round++ {
		if cb.GetSteer != nil {
			for {
				idea := cb.GetSteer()
				if idea == "" {
					break
				}
				steerPrefix := i18n.T(effectiveLocale, "tui.turn.steerPrefix")
				if steerPrefix == "tui.turn.steerPrefix" {
					steerPrefix = "[Steering]: "
				}
				turns = append(turns, entry.Turn{Role: "user", Content: steerPrefix + idea})
			}
		}
		trackReasoning := func(chunk string) {
			accumulatedReasoning.WriteString(chunk)
			if cb.OnReasoning != nil {
				cb.OnReasoning(chunk)
			}
		}
		var out entry.Output
		var err error
		if cb.OnDelta != nil || cb.OnReasoning != nil {
			out, err = entry.ClassifyStreamWithTools(ctx, client, devices, conversationMemory, turns, reg, cb.OnDelta, trackReasoning, classifyOpts...)
		} else {
			out, err = entry.ClassifyTurnsWithTools(ctx, client, devices, conversationMemory, turns, reg, classifyOpts...)
		}
		if err != nil {
			if entry.IsFatalModelError(err) {
				e.recordModelFailure(client.ModelName())
				fallbacks := e.getFallbacks()
				fallbackSuccess := false
				for _, fb := range fallbacks {
					if fb.ModelName() == client.ModelName() {
						continue
					}
					e.logger.Warn("askengine: primary model fatal error, attempting fallback", "primary", client.ModelName(), "fallback", fb.ModelName(), "err", err)
					cb.progress(Progress{Kind: ProgressRoute, Name: fb.ModelName()})
					var fbOut entry.Output
					var fbErr error
					if cb.OnDelta != nil || cb.OnReasoning != nil {
						fbOut, fbErr = entry.ClassifyStreamWithTools(ctx, fb, devices, conversationMemory, turns, reg, cb.OnDelta, trackReasoning, classifyOpts...)
					} else {
						fbOut, fbErr = entry.ClassifyTurnsWithTools(ctx, fb, devices, conversationMemory, turns, reg, classifyOpts...)
					}
					if fbErr == nil {
						e.recordModelSuccess(fb.ModelName())
						client = fb
						usageBefore = fb.Usage()
						fallbackUsed = fb.ModelName()
						out = fbOut
						err = nil
						fallbackSuccess = true
						break
					} else if entry.IsFatalModelError(fbErr) {
						e.recordModelFailure(fb.ModelName())
					}
				}
				if !fallbackSuccess {
					return digestOrErr(err)
				}
			} else {
				return digestOrErr(err)
			}
		} else {
			e.recordModelSuccess(client.ModelName())
		}

		switch out.Kind {
		case entry.KindAnswer:
			if lastTask != nil {
				// The model has seen the task's outcome and is reporting it:
				// the report is the answer, the task fields ride along.
				lastTask.Answer = out.Answer
				if lastTask.Thought == "" {
					lastTask.Thought = accumulatedReasoning.String()
				}
				return finalizeLastTask(lastTask), nil
			}
			return &Result{Kind: "answer", Answer: out.Answer, Thought: accumulatedReasoning.String()}, nil
		case entry.KindTask:
			if taskRounds >= maxTasks {
				// Refuse before submitTask: the budget limits actual task
				// execution, not merely how many outcomes are replayed.
				turns = append(turns,
					entry.Turn{Role: "assistant", Content: taskDispatchNote(out.Task, effectiveLocale)},
					entry.Turn{Role: "user", Content: taskBudgetNote(maxTasks, effectiveLocale)},
				)
				break rounds
			}
			if e.sched == nil {
				e.tryAutoInitScheduler()
			}
			if e.sched == nil {
				return nil, fmt.Errorf("task output requires a capability card (scheduler initialization failed)")
			}
			cb.progress(Progress{Kind: ProgressTask, Name: out.Task.Title})
			res := e.submitTask(ctx, out.Task, prompt, authorize, scope, accumulatedReasoning.String(), cb, effectiveLocale)
			if e.queueTasks {
				// Async mode: the board product. The queued pointer is the
				// result — the session streams the task's progress and the
				// finalizer folds its summary, so there is no observation to
				// feed back and nothing to report in this turn.
				return res, nil
			}
			if res.NeedsApproval {
				// The inline gate parks the ask; the front-end prompts on its
				// own event loop and resumes via ResumeApprovedReport.
				return res, nil
			}
			taskRounds++
			lastTask = res
			// The sub-agent round: the task is one step of this conversation,
			// not its end. Replay the dispatch as the model's own words, feed
			// the outcome back as the observation it reports on, and let the
			// loop converge — to a report, a follow-up task, or a question.
			// Dedicated SummarizeResult is deferred as a lazy fallback if the loop
			// does not converge on an answer, eliminating redundant LLM latency.
			turns = append(turns,
				entry.Turn{Role: "assistant", Content: taskDispatchNote(out.Task, effectiveLocale)},
				entry.Turn{Role: "user", Content: taskObservation(res, effectiveLocale)},
			)
		case entry.KindPlan:
			cb.progress(Progress{Kind: ProgressPlan, Name: out.Plan.Goal})
			return e.startClassifiedPlan(ctx, out.Plan, authorize)
		case entry.KindToolCall:
			calls := out.ToolCalls()
			if len(calls) == 0 {
				break rounds
			}
			// Execute EVERY call the model emitted this round. Running only the
			// first forced the model to re-emit the rest on later turns — one
			// round trip per call — until a batch intent like "clean the queue"
			// exhausted the round budget on work that was fully specified up
			// front (and the final tool-free round then failed on the DSML
			// markup the model fell back to).
			results := make([]string, len(calls))
			var dispatched []*Result
			for i, call := range calls {
				cb.progress(Progress{Kind: ProgressTool, Name: call.Tool})
				if call.Tool == "task_submit" && taskRounds >= maxTasks {
					// task_submit is a real delegation hidden behind the native
					// tool protocol. Refuse it before executeTool for the same
					// hard execution budget as a classified task directive.
					results[i] = taskBudgetNote(maxTasks, effectiveLocale)
					continue
				}
				// Execute against the same registry snapshot classification
				// saw: a mid-ask SetMCPCommand swap would otherwise make the
				// model's tool call hit a registry that no longer knows it.
				results[i] = executeTool(ctx, reg, call, toolAuthorized, e.cfg.Approval.NormalizedMode(), effectiveLocale)
				toolDigest = append(toolDigest, call.Tool+": "+results[i])
				for _, d := range taskCapture.takeAll() {
					dispatched = append(dispatched, d)
					if !d.NeedsApproval && d.TaskID != "" {
						taskRounds++
					}
				}
			}
			turns = appendToolCalls(turns, calls, out.Note, results, effectiveLocale)

			for _, d := range dispatched {
				if e.queueTasks || d.NeedsApproval || d.TaskID == "" {
					return d, nil
				}
				lastTask = d
			}
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
		if entry.IsFatalModelError(ferr) {
			e.recordModelFailure(client.ModelName())
			fallbacks := e.getFallbacks()
			for _, fb := range fallbacks {
				if fb.ModelName() == client.ModelName() {
					continue
				}
				e.logger.Warn("askengine: final round fatal error, attempting fallback", "primary", client.ModelName(), "fallback", fb.ModelName(), "err", ferr)
				cb.progress(Progress{Kind: ProgressRoute, Name: fb.ModelName()})
				var fbFinal entry.Output
				var fbErr error
				if cb.OnDelta != nil || cb.OnReasoning != nil {
					fbFinal, fbErr = entry.ClassifyStreamWithTools(ctx, fb, devices, conversationMemory, turns, nil, cb.OnDelta, cb.OnReasoning, classifyOpts...)
				} else {
					fbFinal, fbErr = entry.ClassifyTurns(ctx, fb, devices, conversationMemory, turns, classifyOpts...)
				}
				if fbErr == nil {
					e.recordModelSuccess(fb.ModelName())
					client = fb
					usageBefore = fb.Usage()
					fallbackUsed = fb.ModelName()
					final = fbFinal
					ferr = nil
					break
				} else if entry.IsFatalModelError(fbErr) {
					e.recordModelFailure(fb.ModelName())
				}
			}
		}
		if ferr != nil {
			// The tools already ran — report what they did rather than a bare
			// loop error. toolDigest is empty only when nothing ever executed,
			// in which case the wrapped error stands.
			return digestOrErr(fmt.Errorf("reached max tool rounds (%d): %w", maxRounds, ferr))
		}
	} else {
		e.recordModelSuccess(client.ModelName())
	}
	if final.Kind == entry.KindTask {
		if e.sched == nil {
			e.tryAutoInitScheduler()
		}
		if e.sched == nil {
			return &Result{Kind: "answer", Answer: fmt.Sprintf("已连续调用 %d 轮工具未收敛；模型最终建议任务「%s」，但当前未加载能力卡片，无法提交。", maxRounds, final.Task.Title)}, nil
		}
		if lastTask != nil && taskRounds >= maxTasks {
			// The loop exhausted the task budget and the model still wants
			// another delegation: surface what ran instead of exceeding it.
			return finalizeLastTask(lastTask), nil
		}
		cb.progress(Progress{Kind: ProgressTask, Name: final.Task.Title})
		res := e.submitTask(ctx, final.Task, prompt, authorize, scope, accumulatedReasoning.String(), cb)
		if !res.NeedsApproval && !e.queueTasks {
			// No rounds left to converge through, so produce the report in
			// one shot rather than returning raw output.
			if report, rerr := e.reportTaskOutcome(ctx, client, turns, devices, conversationMemory, classifyOpts, final.Task, res); rerr == nil {
				res.Answer = report
			} else {
				e.logger.Warn("askengine: task report degraded", "task", res.TaskID, "err", rerr)
			}
		}
		return res, nil
	}
	if final.Kind == entry.KindPlan {
		if e.sched == nil {
			e.tryAutoInitScheduler()
		}
		if e.sched == nil {
			return &Result{Kind: "answer", Answer: fmt.Sprintf("已连续调用 %d 轮工具未收敛；模型最终建议多阶段计划「%s」，但当前未加载能力卡片，无法启动。", maxRounds, final.Plan.Goal)}, nil
		}
		cb.progress(Progress{Kind: ProgressPlan, Name: final.Plan.Goal})
		return e.startClassifiedPlan(ctx, final.Plan, authorize)
	}
	if lastTask != nil {
		lastTask.Answer = final.Answer
		return finalizeLastTask(lastTask), nil
	}
	return &Result{Kind: "answer", Answer: final.Answer}, nil
}

// WorkPath returns the configured work directory — the project workspace
// panel sessions execute in. It lets the panel pin non-repo sessions to the
// work path so the memory wall (§17.2) holds for them too.
func (e *Engine) WorkPath() string { return e.cfg.Storage.WorkPath }

// CardPath returns the capabilities.yaml the engine's scheduler was built
// from — the file /card edits and reloads.
func (e *Engine) CardPath() string { return e.cardPath }

// ReloadCard hot-swaps the scheduler's capability card (the /card edit path):
// the core re-reads the file, rebuilds its router, re-registers, and tells
// connected peers. The engine's own cardPath is what the system_status tool
// reports, so it moves too. Errors when the engine runs cardless — there is
// nothing to reload.
func (e *Engine) ReloadCard(path string) error {
	e.schedMu.Lock()
	defer e.schedMu.Unlock()
	if e.sched == nil {
		return e.initSchedulerLocked(path)
	}
	if err := e.sched.ReloadCard(e.schedCtx, path); err != nil {
		return err
	}
	e.cardPath = path
	return nil
}

// DialPeer dials addr on the engine's scheduler right now — /nodes add's
// live-connect path, so a peer freshly appended to the config joins this
// session without a restart. The connection registers itself (hello exchange)
// on success; the caller decides whether to wait or dial in the background.
func (e *Engine) DialPeer(ctx context.Context, addr string) error {
	if e.sched == nil {
		return fmt.Errorf("askengine: no scheduler (card missing or scheduler initialization failed)")
	}
	return e.sched.DialPeer(ctx, addr)
}

// EnqueueTask routes a directly-created task (the panel's board "new task"
// form) through the async queue: it lands in queued and the scheduler starts
// it when resources allow. Needs a capability card, like task submission.
func (e *Engine) EnqueueTask(ctx context.Context, in core.TaskInput, q core.QueueSpec) (core.Task, error) {
	if e.sched == nil {
		return core.Task{}, fmt.Errorf("task creation requires a capability card (scheduler initialization failed)")
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
		return "", fmt.Errorf("starting a plan requires a capability card (scheduler initialization failed)")
	}
	return e.sched.StartPlan(ctx, p, q)
}

// PlanStages returns every stage of one plan, for following a run.
func (e *Engine) PlanStages(ctx context.Context, planID string) ([]core.Task, error) {
	if e.sched == nil {
		return nil, fmt.Errorf("reading a plan requires a capability card (scheduler initialization failed)")
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
		return nil, fmt.Errorf("plan output requires a capability card (scheduler initialization failed)")
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
//   - always      — consent is always withheld at submission: every tier-2
//     task parks in review and is decided per-task in the foreground. A
//     standing session grant must not silently satisfy the gate — "每次都需
//     审批" means the approval happens after the task exists and says what it
//     will do, not as a flag covering whatever the session produces. The
//     approval itself (ResumeApproved) carries its own consent and does not
//     pass through this function.
func gateAuthorized(mode string, sessionAuthorized bool) bool {
	switch mode {
	case config.ApprovalModeNever:
		return true
	case config.ApprovalModeAlways:
		return false
	default:
		return sessionAuthorized
	}
}

// submitTask executes a classified task spec through the scheduler core and
// maps the outcome to a Result. In queue mode the task is Enqueued and the
// call returns immediately (TaskState "queued"); the queue scheduler starts
// it when resources allow and the session streams its progress. In inline
// mode a per-task WorkDir is persisted before routing and execution, avoiding
// process-wide scheduler directory swaps between concurrent asks.
func (e *Engine) submitTask(ctx context.Context, spec *entry.TaskSpec, prompt string, authorized bool, scope AskScope, reasoning string, cb StreamCallbacks, loc ...i18n.Locale) *Result {
	targetLoc := e.locale
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	} else if !e.explicitLocale && !e.replyASCII && (containsHan(prompt) || containsHan(spec.Title)) {
		targetLoc = i18n.ChineseSimp
	}
	in := toTaskInput(spec, targetLoc)
	workDir := scope.WorkDir
	// Explicit per-request scope wins. Existing CLI callers that omit a project
	// retain the entered ambient project as a compatibility default.
	project, projectDir := scope.Project, ""
	if scope.ambientProjectFallback {
		project, projectDir = e.Project()
	}
	if project != "" {
		if in.Project == "" {
			in.Project = project
		}
		if workDir == "" && projectDir != "" {
			workDir = projectDir
		}
	}
	if workDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			workDir = cwd
		}
	}
	if in.RepoPath == "" && workDir != "" {
		in.RepoPath = workDir
	}
	// Approval mode is the real tier-2 gate (design §16): "never" auto-consents
	// so an irreversible task runs as classified; "on-request" withholds consent
	// until the user approves at the inline gate below, with a session-level
	// authorization (/authorize on, --authorize) as the standing consent it
	// honors. "always" withholds regardless: every tier-2 task parks in review
	// and is decided per-task in the foreground.
	authorized = gateAuthorized(e.cfg.Approval.NormalizedMode(), authorized)
	in.Authorized = authorized
	in.WorkDir = workDir
	// classify_result is traced inside core's createTask — before routing,
	// before the queue claims the task — so no emission happens here.
	in.ClassifyKind = "task"
	if prompt != "" {
		// Carry the user's original words into the agent prompt as a fidelity
		// backstop: the intent above is the entry model's distillation, which
		// can drop detail. The raw query lets the agent recover it, and it
		// travels with the intent (persisted + delegated), so retries and
		// peers that pick up the task also see it.
		rawLabel := i18n.T(targetLoc, "prompt.task.user_raw_request")
		in.Intent += "\n\n" + rawLabel + "\n" + prompt
	}
	if e.queueTasks {
		q := core.DefaultQueueSpec()
		q.WorkDir = workDir // travels per task; "" falls back to the core's work dir
		task, err := e.sched.Enqueue(ctx, in, q)
		if err != nil {
			return &Result{Kind: "task", TaskState: "failed", Stderr: err.Error(), ExitCode: 1}
		}
		if reasoning != "" {
			e.sched.EvTrace(ctx, task.TaskID, core.EvReasoning, map[string]any{"thought": reasoning})
		}
		return &Result{Kind: "task", TaskID: task.TaskID, TaskTitle: task.Title, TaskState: task.State, Thought: reasoning}
	}
	// Bridge the core's lifecycle trace events to the caller's progress feed for
	// the duration of this synchronous run, so a blocking agent execution shows
	// routing → executing → judging instead of a frozen spinner. AddOnEvent
	// allows concurrent subagent runs without serializing submissions.
	// A caller with no progress sink installs nothing.
	if cb.OnProgress != nil || cb.OnStatus != nil {
		store := e.sched.TaskStore()
		unsub := store.AddOnEvent(func(_, typ string, data any) {
			if p, ok := progressForEvent(typ, data); ok {
				cb.progress(p)
			}
		})
		defer unsub()
	}
	task, result, err := e.sched.Submit(ctx, in)
	if err != nil {
		return &Result{Kind: "task", TaskState: "failed", Stderr: err.Error(), ExitCode: 1}
	}
	if reasoning != "" {
		e.sched.EvTrace(ctx, task.TaskID, core.EvReasoning, map[string]any{"thought": reasoning})
	}
	res := &Result{
		Kind:      "task",
		Thought:   reasoning,
		TaskID:    task.TaskID,
		TaskTitle: task.Title,
		TaskState: task.State,
		OK:        result.OK,
		Stdout:    result.Stdout,
		Stderr:    result.Stderr,
		ExitCode:  result.ExitCode,
		Agent:     result.Agent,
		Model:     result.Model,
		Injected:  result.Injected,
		Executor:  result.Executor,
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
			resumed := e.resumeLocked(ctx, req.TaskID)
			sumClient, _ := e.healthyClient()
			if report, rerr := entry.SummarizeResult(ctx, sumClient, resumed.TaskTitle, in.Intent, resumed.OK, resumed.ExitCode, resumed.Stdout, resumed.Stderr, targetLoc); rerr == nil {
				resumed.Report = report
			}
			return resumed
		}
	}
	return res
}

// progressForEvent maps a scheduler-core trace event to a Progress the caller
// can render, or reports ok=false for events with no live-progress meaning. It
// is the one place that knows which core event types (internal/core/trace.go,
// state.go) correspond to the CLI's routing/executing/judging phases. Event
// data arrives as it was recorded — a map before marshaling, or JSON bytes
// after — so it is read through eventField/intField, which handle both.
func progressForEvent(typ string, data any) (Progress, bool) {
	switch typ {
	case core.EvRouteDecision:
		name := eventField(data, "target_node")
		if name == "" {
			name = eventField(data, "action") // "local" when no target node
		}
		return Progress{Kind: ProgressRoute, Name: name}, true
	case core.EvExecAgentStart:
		name := eventField(data, "agent")
		if name == "" {
			name = eventField(data, "adapter")
		}
		model := eventField(data, "model")
		return Progress{Kind: ProgressExec, Name: name, Model: model,
			Round: intField(data, "round"), Budget: intField(data, "budget")}, true
	case core.EvProgress, core.EvSubagentEvent:
		note := eventField(data, "note")
		if note != "" {
			return Progress{Kind: ProgressTool, Name: note}, true
		}
	case core.EvModelInjection:
		name := eventField(data, "agent")
		model := eventField(data, "model")
		return Progress{Kind: ProgressExec, Name: name, Model: model}, true
	case core.EvJudgeStart:
		// Opening marker for the reviewing stage; the round's result event
		// below repeats the phase, and the renderer dedupes the second one.
		return Progress{Kind: ProgressJudge,
			Round: intField(data, "round"), Budget: intField(data, "budget")}, true
	case core.EvSupervisionRound:
		return Progress{Kind: ProgressJudge, Name: eventField(data, "verdict_status"),
			Round: intField(data, "round"), Budget: intField(data, "budget")}, true
	}
	return Progress{}, false
}

// eventField extracts a string field from a recorded event's data, which is
// either a map[string]any (pre-marshal) or JSON bytes (post-marshal in the
// state-transition path). Missing/absent yields "". Named string types
// (scheduler.Action and friends) count as strings: the route event stores its
// action as its own type, and a bare .(string) assertion silently degraded
// the progress line to "routing to …" with nothing after the preposition.
func eventField(data any, key string) string {
	switch v := data.(type) {
	case map[string]any:
		return stringField(v[key])
	case []byte:
		var m map[string]any
		if json.Unmarshal(v, &m) == nil {
			return stringField(m[key])
		}
	}
	return ""
}

// stringField reads a map value as a string, accepting named string types.
func stringField(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.String {
		return rv.String()
	}
	return ""
}

// intField extracts a numeric field the same way eventField does for strings:
// counters arrive as Go ints when the event is recorded directly, and as
// float64 after the state-transition path marshals the data to JSON.
func intField(data any, key string) int {
	switch v := data.(type) {
	case map[string]any:
		return numberField(v[key])
	case []byte:
		var m map[string]any
		if json.Unmarshal(v, &m) == nil {
			return numberField(m[key])
		}
	}
	return 0
}

// numberField reads a map value as an int, accepting JSON's float64 numbers.
func numberField(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// resultFromTask maps one persisted task/result pair into the typed result used
// by every interactive surface. Accepting reviewed work may run without a
// scheduler, so this mapping deliberately depends only on storage.
func resultFromTask(task core.Task, result bus.TaskResultPayload) *Result {
	return &Result{
		Kind:      "task",
		TaskID:    task.TaskID,
		TaskTitle: task.Title,
		TaskState: task.State,
		OK:        result.OK,
		Stdout:    result.Stdout,
		Stderr:    result.Stderr,
		ExitCode:  result.ExitCode,
		Agent:     result.Agent,
		Model:     result.Model,
		Injected:  result.Injected,
		Executor:  result.Executor,
	}
}

func (e *Engine) acceptReviewedWork(ctx context.Context, taskID string) *Result {
	store := e.TaskStore()
	task, err := store.Get(ctx, taskID)
	if err != nil {
		return &Result{Kind: "task", TaskID: taskID, TaskState: core.StateReview, Stderr: err.Error(), ExitCode: 1}
	}
	result := bus.TaskResultPayload{OK: true}
	if task.ResultJSON != "" {
		if err := json.Unmarshal([]byte(task.ResultJSON), &result); err != nil {
			return &Result{Kind: "task", TaskID: taskID, TaskTitle: task.Title, TaskState: task.State, Stderr: err.Error(), ExitCode: 1}
		}
	}
	if err := store.Approve(ctx, taskID); err != nil {
		return &Result{Kind: "task", TaskID: taskID, TaskTitle: task.Title, TaskState: task.State, Stderr: err.Error(), ExitCode: 1}
	}
	final, err := store.Get(ctx, taskID)
	if err != nil {
		return &Result{Kind: "task", TaskID: taskID, TaskTitle: task.Title, TaskState: task.State, Stderr: err.Error(), ExitCode: 1}
	}
	result.TaskID = final.TaskID
	result.AttemptID = final.AttemptID
	result.State = final.State
	return resultFromTask(final, result)
}

// resumeLocked re-runs an approved review-parked task synchronously and maps
// the outcome to a Result. The caller must hold schedMu (submitTask does), so
// any pinned session work dir is still in effect for the re-run.
func (e *Engine) resumeLocked(ctx context.Context, taskID string) *Result {
	task, result, err := e.sched.ResumeApproved(ctx, taskID)
	if err != nil {
		state := core.StateReview
		if current, getErr := e.sched.TaskStore().Get(context.WithoutCancel(ctx), taskID); getErr == nil {
			state = current.State
		}
		return &Result{Kind: "task", TaskID: taskID, TaskState: state, Stderr: err.Error(), ExitCode: 1}
	}
	return resultFromTask(task, result)
}

// ResumeApproved accepts or re-runs a reviewed task under the caller's context.
// workDir optionally overrides the persisted task directory for an active
// originating session; progress is bridged from the same core event stream as
// initial foreground submission.
func (e *Engine) ResumeApproved(ctx context.Context, taskID, workDir string, cb StreamCallbacks) *Result {
	if e == nil || e.db == nil {
		return &Result{Kind: "task", TaskID: taskID, TaskState: core.StateFailed, Stderr: "task approval requires an initialized engine", ExitCode: 1}
	}
	disposition, err := e.TaskStore().ApprovalDisposition(ctx, taskID)
	if err != nil {
		state := core.StateReview
		if task, getErr := e.TaskStore().Get(context.WithoutCancel(ctx), taskID); getErr == nil {
			state = task.State
		}
		return &Result{Kind: "task", TaskID: taskID, TaskState: state, Stderr: err.Error(), ExitCode: 1}
	}
	switch disposition {
	case core.ApprovalAcceptWork:
		return e.acceptReviewedWork(ctx, taskID)
	case core.ApprovalNeedsChangedInput:
		return &Result{Kind: "task", TaskID: taskID, TaskState: core.StateReview, Stderr: core.ErrApprovalNeedsChangedInput.Error(), ExitCode: 1}
	case core.ApprovalResumeExecution:
		// Continue below and execute exactly once under the approval claim.
	default:
		return &Result{Kind: "task", TaskID: taskID, TaskState: core.StateReview, Stderr: "unknown approval disposition", ExitCode: 1}
	}
	if e.sched == nil {
		return &Result{Kind: "task", TaskID: taskID, TaskState: core.StateReview, Stderr: "task execution requires a capability card", ExitCode: 1}
	}
	if workDir != "" {
		if err := e.sched.TaskStore().SetWorkDir(ctx, taskID, workDir); err != nil {
			return &Result{Kind: "task", TaskID: taskID, TaskState: "review", Stderr: err.Error(), ExitCode: 1}
		}
	}
	if cb.OnProgress != nil || cb.OnStatus != nil {
		store := e.sched.TaskStore()
		unsub := store.AddOnEvent(func(id, typ string, data any) {
			if id != taskID {
				return
			}
			if p, ok := progressForEvent(typ, data); ok {
				cb.progress(p)
			}
		})
		defer unsub()
	}
	res := e.resumeLocked(ctx, taskID)
	if ctx.Err() != nil {
		return res
	}
	sumClient, _ := e.healthyClient()
	if report, rerr := entry.SummarizeResult(ctx, sumClient, res.TaskTitle, "", res.OK, res.ExitCode, res.Stdout, res.Stderr, e.locale); rerr == nil {
		res.Report = report
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
	cost := client.EstimateCost(delta.InputTokens, delta.OutputTokens)
	store := core.NewTaskStore(e.db, e.logger)
	if err := store.RecordDelegationMetric(ctx, taskID, e.cfg.Node.Name, "entry:"+client.ModelName(),
		nil, true, latency.Milliseconds(), int(delta.Total()), cost); err != nil {
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
