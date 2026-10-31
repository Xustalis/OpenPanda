// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/carddetect"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/hwinfo"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/mcp"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/nodeidentity"
	"github.com/Xustalis/OpenPanda/internal/plan"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/providers"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/self"
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
	// progress streams into the session. The CLI keeps inline (blocking) mode
	// unless AwaitQueued is also set.
	QueueTasks bool
	// AwaitQueued turns queue-mode submission back into a synchronous-looking
	// round: after Enqueue the ask follows the row through awaitSettled until
	// it settles, bridging the consumer's persisted timeline into the turn's
	// progress. Execution then belongs to the queue scheduler — resource-keyed
	// (project:<name> serializes same-worktree tasks, disjoint projects run in
	// parallel up to max_concurrent_tasks) and process-death-safe (the row is
	// scheduled, so a dead submitter strands nothing). Interactive surfaces
	// (REPL/TUI, panda ask) set it alongside QueueTasks; the panel leaves it
	// off for receipt semantics. ctx ending mid-wait detaches the watch only —
	// the task keeps running and the front end's task watcher announces the
	// outcome, the same detach the inline wait's parked-task path has.
	AwaitQueued bool
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
	// MeshNode makes the engine a full network participant whenever it owns
	// the node row (no daemon running): the WS listener comes up on
	// network.listen_addr, configured peers get reconnect keepalives via
	// SyncPeers, and LAN discovery gains auto-dial. `panda web` and the
	// panel sidecar set it — both are long-lived, and without it a web-only
	// node is an island: it could drain the queue but never be dialed, and
	// a dropped peer edge stayed dead until restart. Interactive seats keep
	// it off: the flap a short-lived REPL causes on peers outweighs the
	// benefit (and a co-resident daemon makes the row borrowed anyway).
	MeshNode bool
	// LocalOnly marks a process that writes to the store and exits: `panda
	// task add`, `task send`, `task continue`. Such a command needs the
	// schema, the card and the task store — not a network presence. Opening
	// the LAN-discovery socket for the milliseconds between argv and exit
	// buys nothing (nobody can pair with a process that is already gone) and
	// costs real things: a wildcard UDP bind on every short command, a
	// `bind: address already in use` warning when several run at once or a
	// daemon holds the port, and a one-shot peer dial whose conns are torn
	// down before a single hello completes. LocalOnly skips both; everything
	// a write needs (schema, store, card, identity key) still happens.
	LocalOnly bool
	// ConfigPath is the file cfg was loaded from — the same path the caller
	// passed to config.Load. It joins the sandbox's write-deny set so a
	// sandboxed subprocess cannot rewrite the node's own configuration.
	// Empty resolves like config.Load does (flag > env > user > system).
	ConfigPath string
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
	// daily is the warm-layer diary the Dreaming engine consolidates from.
	// Task summaries land here as *trusted* lines (the model's own words,
	// not verbatim user text — see noteDailyObservation), which is what
	// makes a Deep-phase promotion possible at all: task titles are written
	// with AppendExternal and can never open the provenance gate alone.
	daily  *memory.Daily
	remind *reminders.Store
	skills *skills.Store

	mcp        *mcp.Client
	mcpCommand string
	logger     *slog.Logger

	// sched powers task execution once initialized — either eagerly from
	// Options.CardPath/config or lazily on first use (tryAutoInitScheduler).
	// It is an atomic pointer because it is read from many goroutines that do
	// not hold schedMu (CancelTask, DialPeer, per-ask submission checks) while
	// the lazy init writes it under schedMu.
	// schedMu serializes scheduler init/reload and task submission: a submit
	// may temporarily pin the core's work dir to a session worktree, which
	// must not interleave. schedCtx/schedCancel are init-time companions,
	// only ever touched under schedMu.
	// (Queue mode never swaps the global work dir — it travels per task.)
	sched       atomic.Pointer[core.Core]
	schedMu     sync.Mutex
	schedCtx    context.Context
	schedCancel context.CancelFunc
	// bg tracks the long-lived duties this engine starts (heartbeat, task
	// monitor, reconcile, discovery, ws listen, peer dials, the node-row
	// watch). Close waits on it before closing the DB: cancelling schedCtx
	// stops them, but it does not join them — a beat writing its liveness
	// row after the close reopens a WAL file, and a test's temp dir is then
	// removed under a live writer.
	bg sync.WaitGroup

	// selfRowForeign/selfRowWriteAt bracket this engine's own Register writes:
	// the newest last_seen stamp observed strictly above our last write (a
	// beat nobody in this process wrote), and that write's unix time. The
	// store-bound consumer probe (queueConsumerAlive) uses the pair to keep
	// our own stamp from reading as a live foreign heartbeat — the split-db
	// dead-letter case the identity lock cannot see.
	selfRowForeign atomic.Int64
	selfRowWriteAt atomic.Int64

	// cardPath mirrors Options.CardPath: the capabilities.yaml the engine
	// loaded its scheduler from, reported by the system_status tool. Lazy
	// writes (checkCardPath, card reload) happen off the init path, so cardMu
	// guards every access.
	cardMu   sync.RWMutex
	cardPath string

	// configPath mirrors Options.ConfigPath (resolved like config.Load when
	// empty): the file cfg was read from, enrolled in the sandbox's
	// write-deny set so a spawned subprocess cannot rewrite the node's own
	// configuration.
	configPath string

	// cfgMu guards post-init mutation of the shared *config.Config: the REPL
	// aliases it as r.cfg and rewrites hot fields (model, peers, approval
	// mode, work path) from its own goroutines while ask/tool goroutines read
	// them. Writers go through MutateConfig (or SetModel for the model);
	// readers of mutable fields take the read lock. Fields that are immutable
	// after New (node identity, storage roots) may be read unlocked.
	cfgMu sync.RWMutex

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
	// awaitQueued mirrors Options.AwaitQueued.
	awaitQueued bool
	// asyncPeers mirrors Options.AsyncPeers.
	asyncPeers bool
	// meshNode mirrors Options.MeshNode.
	meshNode bool
	// localOnly mirrors Options.LocalOnly.
	localOnly bool
	// replyASCII mirrors Options.ReplyASCII (per-engine classify option).
	replyASCII bool
	// locale is the user's active UI/prompt locale. localeMu guards both
	// fields: SetLocale runs on a front-end goroutine while ask/tool paths
	// read them on engine goroutines.
	localeMu       sync.Mutex
	locale         i18n.Locale
	explicitLocale bool

	fallbacksMu sync.RWMutex
	fallbacks   []*entry.Client

	breakerMu sync.Mutex
	breaker   map[string]modelBreaker // entry.Client.Key() -> cooldown state

	// projStore reads the per-project approval policy (mode/scope/decision)
	// the tier-2 gate resolves against; decisions holds the session-scoped
	// remembered answers, keyed by decisionKey(sessionID, project) so one
	// conversation's remember never leaks into another's.
	projStore   *projectstore.Store
	decisionsMu sync.RWMutex
	decisions   map[string]string
}

// Model circuit-breaker timings. A fatal provider failure cools one client
// down; repeated failures escalate (30s, 60s, 120s, 240s, then the 5-minute
// ceiling) so a prolonged outage stops being retried every half minute, and
// one success clears the record entirely.
const (
	breakerBaseCooldown = 30 * time.Second
	breakerMaxCooldown  = 5 * time.Minute
	// breakerResetAfter is how long a client must stay free of fatal failures
	// before its consecutive-failure count is forgotten. Cooldown expiry alone
	// must not reset it (a flapping endpoint would never escalate), but a
	// failure a day later must not inherit yesterday's streak either.
	breakerResetAfter = 30 * time.Minute
)

// modelBreaker is one client's cooldown state: when it may be tried again and
// how many consecutive fatal failures put it there (the escalation exponent).
type modelBreaker struct {
	until time.Time
	fails int
}

func (e *Engine) isModelHealthy(key string) bool {
	if key == "" {
		return true
	}
	e.breakerMu.Lock()
	defer e.breakerMu.Unlock()
	st, ok := e.breaker[key]
	if !ok {
		return true
	}
	// An expired cooldown makes the model usable again but keeps its failure
	// count: the escalation ladder (30s → 60s → 120s → 240s → 5m) only works
	// if the streak survives the cooldown. Deleting the record here reset the
	// count on every probe, so a flapping endpoint retried at the base delay
	// forever and only a success cleared it.
	return time.Now().After(st.until)
}

func (e *Engine) recordModelFailure(key string) {
	if key == "" {
		return
	}
	e.breakerMu.Lock()
	defer e.breakerMu.Unlock()
	if e.breaker == nil {
		e.breaker = make(map[string]modelBreaker)
	}
	st := e.breaker[key]
	// A failure long after the last cooldown ended starts a new streak: the
	// record outlives the cooldown so the ladder can escalate, but not so long
	// that a client which failed once yesterday opens today at the ceiling.
	if !st.until.IsZero() && time.Since(st.until) > breakerResetAfter {
		st.fails = 0
	}
	st.fails++
	steps := st.fails - 1
	if steps > 4 {
		steps = 4
	}
	cooldown := breakerBaseCooldown << steps
	if cooldown > breakerMaxCooldown {
		cooldown = breakerMaxCooldown
	}
	st.until = time.Now().Add(cooldown)
	e.breaker[key] = st
}

func (e *Engine) recordModelSuccess(key string) {
	if key == "" {
		return
	}
	e.breakerMu.Lock()
	defer e.breakerMu.Unlock()
	if e.breaker != nil {
		delete(e.breaker, key)
	}
}

// healthyClient returns an active client: the primary if healthy, or the first
// healthy fallback when the primary is in circuit-breaker cooldown. A missing
// primary (model-less config with a populated models list) is served by the
// first usable fallback rather than failing the ask outright.
func (e *Engine) healthyClient() (*entry.Client, string) {
	client := e.client.Load()
	if client == nil {
		for _, fb := range e.getFallbacks() {
			if e.isModelHealthy(fb.Key()) {
				return fb, fb.ModelName()
			}
		}
		return nil, ""
	}
	if e.isModelHealthy(client.Key()) {
		return client, ""
	}
	for _, fb := range e.getFallbacks() {
		if fb.Key() == client.Key() {
			continue
		}
		if e.isModelHealthy(fb.Key()) {
			return fb, fb.ModelName()
		}
	}
	return client, ""
}

// modelFallbacks returns the configured fallbacks minus the client that just
// failed, deduplicated by endpoint identity and ordered healthy-first (stable
// within each group). A cooling client is not dropped: the cooldown is a hint
// about what is likely to fail, and with every candidate cooling the only way
// forward is to try them anyway — the last one last.
func (e *Engine) modelFallbacks(skipKey string) []*entry.Client {
	all := e.getFallbacks()
	healthy := make([]*entry.Client, 0, len(all))
	cooling := make([]*entry.Client, 0, len(all))
	seen := map[string]bool{skipKey: true}
	for _, fb := range all {
		key := fb.Key()
		if seen[key] {
			continue
		}
		seen[key] = true
		if e.isModelHealthy(key) {
			healthy = append(healthy, fb)
		} else {
			cooling = append(cooling, fb)
		}
	}
	return append(healthy, cooling...)
}

// tryFallbackModels runs one model call on each remaining fallback until one
// answers, recording breaker state per attempt. The client that failed stays
// the caller's primary when nothing succeeds, so the caller reports the
// original error rather than a chain of fallback errors. announce, when set,
// receives each model name as it is tried (the progress line a fallback emits).
func (e *Engine) tryFallbackModels(ctx context.Context, primary *entry.Client, run func(*entry.Client) (entry.Output, error), announce func(string)) (*entry.Client, entry.Output, bool) {
	for _, fb := range e.modelFallbacks(primary.Key()) {
		if e.logger != nil {
			e.logger.Warn("askengine: fatal model error, attempting fallback",
				"primary", primary.ModelName(), "fallback", fb.ModelName())
		}
		if announce != nil {
			announce(fb.ModelName())
		}
		out, err := run(fb)
		if err == nil {
			e.recordModelSuccess(fb.Key())
			return fb, out, true
		}
		if entry.IsFatalModelError(err) {
			e.recordModelFailure(fb.Key())
		}
	}
	return primary, entry.Output{}, false
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
	// cfgMu covers both writes: the cfg.Model field the REPL's r.cfg aliases,
	// and the Models registry buildFallbacks reads for fallbacks.
	e.cfgMu.Lock()
	e.cfg.Model = mc
	e.fallbacksMu.Lock()
	e.fallbacks = e.buildFallbacks(mc, e.db)
	e.fallbacksMu.Unlock()
	e.cfgMu.Unlock()
	return nil
}

// ModelConfig returns the engine's current model configuration.
func (e *Engine) ModelConfig() config.ModelConfig {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return e.cfg.Model
}

// MutateConfig applies fn to the engine's shared config under the write lock.
// Front ends alias e.cfg (the REPL's r.cfg is the same pointer) and use this
// for every post-init field write, so ask/tool goroutines reading those
// fields under the read lock never observe a torn mutation.
func (e *Engine) MutateConfig(fn func(*config.Config)) {
	e.cfgMu.Lock()
	if e.cfg != nil {
		fn(e.cfg)
	}
	e.cfgMu.Unlock()
	e.applyNetworkConfig()
}

// MutateConfigErr is MutateConfig for fallible mutations — typically a
// config-file persist paired with the in-memory write. Running the file I/O
// inside the lock serializes it with every other surface's mutate (REPL
// /model, panel settings saves) so concurrent writers cannot interleave a
// read-modify-write of config.yaml into a lost update.
func (e *Engine) MutateConfigErr(fn func(*config.Config) error) error {
	e.cfgMu.Lock()
	if e.cfg == nil {
		e.cfgMu.Unlock()
		return nil
	}
	err := fn(e.cfg)
	e.cfgMu.Unlock()
	if err == nil {
		e.applyNetworkConfig()
	}
	return err
}

// applyNetworkConfig pushes the mutable network.* fields onto a live
// scheduler after a config mutation: a mesh node's SyncPeers reconciles the
// peer keepalive set (a /nodes add connects now, a /nodes disconnect drops
// the loop and conn), and every engine picks up secret/cleartext edits for
// its next outbound dial. On a non-mesh engine the peer sync is a no-op —
// SyncPeers is still safe to call (it just starts loops), so the meshNode
// guard decides which half applies.
func (e *Engine) applyNetworkConfig() {
	sched := e.sched.Load()
	if sched == nil {
		return
	}
	e.cfgMu.RLock()
	n := e.cfg.Network
	reputation := e.cfg.Routing.Reputation
	e.cfgMu.RUnlock()
	if e.meshNode {
		sched.ApplyNetworkConfig(e.schedCtx, n)
		sched.SetReputationEnabled(reputation)
		return
	}
	sched.SetSharedSecret(n.SharedSecret)
	sched.SetAllowCleartext(n.AllowCleartext)
	sched.SetCleartextAllowlist(n.AllowCleartextFor)
	sched.SetTrustConfig(n.Trust)
	sched.SetPeerTrustHints(n.PeerTrustHints())
	sched.SetReputationEnabled(reputation)
}

// ReadConfig runs fn under the read lock — the read half of MutateConfig, for
// callers that need several fields observed as one consistent snapshot.
// A nil cfg skips fn: no config loaded means nothing to snapshot.
func (e *Engine) ReadConfig(fn func(*config.Config)) {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	if e.cfg != nil {
		fn(e.cfg)
	}
}

// cardPathNow / setCardPath serialize the lazily-mutating cardPath field:
// checkCardPath fills it on tool goroutines while ReloadCard and the status
// tool touch it from others.
func (e *Engine) cardPathNow() string {
	e.cardMu.RLock()
	defer e.cardMu.RUnlock()
	return e.cardPath
}

func (e *Engine) setCardPath(path string) {
	e.cardMu.Lock()
	e.cardPath = path
	e.cardMu.Unlock()
}

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
	sched := e.sched.Load()
	if sched == nil {
		return core.NewSigningTaskStore(e.db, e.logger).CancelCascade(ctx, taskID)
	}
	return sched.CancelTree(ctx, taskID)
}

// RejectTask fails a reviewed task and propagates the rejection to the remote
// executor holding its delegated review copy, on the same cancel path
// CancelTask uses. Without the forward the executor's copy stayed in review
// and a local approve there would run work the origin explicitly denied.
func (e *Engine) RejectTask(ctx context.Context, taskID, reason string) error {
	sched := e.sched.Load()
	if sched == nil {
		return core.NewSigningTaskStore(e.db, e.logger).Reject(ctx, taskID, reason)
	}
	return sched.RejectTree(ctx, taskID, reason)
}

// TaskStore returns the task store on the engine's database, for callers that
// need read-level access (reference resolution) without a scheduler core.
func (e *Engine) TaskStore() *core.TaskStore {
	return core.NewSigningTaskStore(e.db, e.logger)
}

// stableNodeID is the runtime identity this engine registers under — the
// same value initSchedulerLocked computes for the scheduler core. Valid even
// when the scheduler never came up (card init failure): the owner guard on
// the scheduler-less message path needs it regardless.
func (e *Engine) stableNodeID() string {
	return core.RuntimeNodeID(e.cfg.Node.Name, e.cfg.Node.Kind, e.cfg.Node.EffectiveIdentity())
}

// SendTaskMessage delivers a user message into a task — the two send modes
// (steer.go). Queue posts a durable advisory the agent reads at its next
// supervision round; inject additionally aborts the in-flight round when one
// is live so the message leads the next one as a directive. The returned
// token is "interrupted" (a running turn was aborted), "queued" (the message
// waits for the next round — or the task's first run), and callers localize
// it for their surface.
//
// The warning mirrors EnqueueTask: a task still queued with no consumer on
// this store never reaches a round, so a note delivered to it is durable but
// unread — say so instead of reporting a silent success.
func (e *Engine) SendTaskMessage(ctx context.Context, taskID, text string, inject bool) (status, warning string, err error) {
	res, err := e.SendTaskNote(ctx, taskID, text, nil, inject)
	return res.Status, res.Warning, err
}

// TaskNoteResult is the outcome of one note into a task: the board slot it
// occupies (so the caller can edit, drop or promote that exact row), how it
// landed, and the advisory the surface should show beside the outcome.
type TaskNoteResult struct {
	Sender  string `json:"sender"`
	Seq     int64  `json:"seq"`
	Status  string `json:"status"`
	Warning string `json:"warning,omitempty"`
}

// SendTaskNote is SendTaskMessage with attachments and the landed slot. The
// text and the files travel together: the store appends one `@path` line per
// attachment to the body, so the agent reads a self-contained note.
func (e *Engine) SendTaskNote(ctx context.Context, taskID, text string, attachments []string, inject bool) (TaskNoteResult, error) {
	res := TaskNoteResult{}
	sched := e.sched.Load()
	switch {
	case sched == nil:
		// A message needs the store's board only; a scheduler-less engine
		// still delivers it to the row — with the same owner guard, so a
		// remote-executor task refuses instead of silently swallowing the note.
		if inject {
			return res, fmt.Errorf("inject requires the scheduler (capability card initialization failed)")
		}
		msg, err := core.NewSigningTaskStore(e.db, e.logger).
			PostUserNote(ctx, taskID, text, attachments, core.MsgGradeStatus, e.stableNodeID())
		if err != nil {
			return res, err
		}
		res.Sender, res.Seq, res.Status = msg.Sender, msg.SenderSeq, "queued"
	case inject:
		msg, status, err := sched.InjectUserNote(ctx, taskID, text, attachments)
		if err != nil {
			return res, err
		}
		res.Sender, res.Seq, res.Status = msg.Sender, msg.SenderSeq, status
	default:
		msg, err := sched.QueueUserNote(ctx, taskID, text, attachments)
		if err != nil {
			return res, err
		}
		res.Sender, res.Seq, res.Status = msg.Sender, msg.SenderSeq, "queued"
	}
	res.Warning = e.noteWarning(ctx, taskID, inject, res.Status)
	return res, nil
}

// PromoteTaskNote escalates a queued note to an inject without posting a
// second row — the second-Enter path. The returned status is the same
// "interrupted"/"queued" pair SendTaskNote reports.
func (e *Engine) PromoteTaskNote(ctx context.Context, taskID, sender string) (status, warning string, err error) {
	sched := e.sched.Load()
	if sched == nil {
		return "", "", fmt.Errorf("inject requires the scheduler (capability card initialization failed)")
	}
	status, err = sched.PromoteUserNote(ctx, taskID, sender)
	if err != nil {
		return "", "", err
	}
	return status, e.noteWarning(ctx, taskID, true, status), nil
}

// EditTaskNote rewrites a pending note. Errors carry the reason the store
// refused (unknown row, already read, terminal task, remote executor) so the
// surface can map them onto an HTTP status without guessing.
func (e *Engine) EditTaskNote(ctx context.Context, taskID, sender, text string, attachments []string) error {
	_, err := core.NewSigningTaskStore(e.db, e.logger).
		EditUserNote(ctx, taskID, sender, text, attachments, e.stableNodeID())
	return err
}

// DeleteTaskNote drops a pending note from the board.
func (e *Engine) DeleteTaskNote(ctx context.Context, taskID, sender string) error {
	return core.NewSigningTaskStore(e.db, e.logger).DeleteUserNote(ctx, taskID, sender)
}

// noteWarning phrases the two ways a delivered note still fails to reach a
// round: a queued task with no consumer, and an inject that could not abort
// because the running round lives in another process.
func (e *Engine) noteWarning(ctx context.Context, taskID string, inject bool, status string) string {
	t, terr := core.NewSigningTaskStore(e.db, e.logger).Get(ctx, taskID)
	if terr != nil {
		return ""
	}
	if t.State == core.StateQueued && !e.queueConsumerAlive(ctx) {
		return i18n.T(e.Locale(), "cli.queue.noConsumer")
	}
	// An inject only aborts a round this process is running — the cancel
	// handle lives in memory (steer.go). When the row is running somewhere
	// else the note still queued for the next round, which is fine, but
	// silently reporting the same "queued" a same-process inject reports
	// would tell the user they cut the round short when nothing was cut.
	if inject && status == "queued" && t.State == core.StateRunning {
		return i18n.T(e.Locale(), "cli.task.send.injectNoInterrupt")
	}
	return ""
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
	if sched := e.sched.Load(); sched != nil && sched.TaskStore() != nil {
		sched.TaskStore().SetOnReview(fn)
	}
}

// SetLocale updates the active user locale for this engine.
func (e *Engine) SetLocale(loc i18n.Locale) {
	if loc == "" {
		return
	}
	e.localeMu.Lock()
	e.locale = loc
	e.explicitLocale = true
	e.localeMu.Unlock()
}

// Locale returns the active user locale for this engine.
func (e *Engine) Locale() i18n.Locale {
	e.localeMu.Lock()
	loc := e.locale
	e.localeMu.Unlock()
	if loc != "" {
		return loc
	}
	return i18n.Detect()
}

// localeNow snapshots both locale fields under one lock; the explicit flag
// decides whether prompt-side auto-detection may override the value.
func (e *Engine) localeNow() (i18n.Locale, bool) {
	e.localeMu.Lock()
	defer e.localeMu.Unlock()
	return e.locale, e.explicitLocale
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
	// Dispatched names every task this ask enqueued, in dispatch order, when a
	// single round fanned out several `task_submit` calls. The task fields
	// above still describe the last one — the receipt this Result has always
	// carried — so a surface that draws one card keeps working unchanged. A
	// round that dispatched exactly one task leaves this empty: the receipt
	// already says everything there is to say.
	Dispatched []DispatchedTask
	// Deferred marks a parked-for-delivery outcome: the approval was
	// accepted into an outbox because the executor's link is down, so the
	// correct rendering is "queued for delivery" with exit 0 — not a
	// failure. Mirrors TaskResultPayload.Deferred.
	Deferred bool
	Agent    string
	Model    string
	Injected bool
	// Executor is the node that actually ran the task — this node's id for a
	// local run, the peer's id for a delegated one. Empty when the result
	// never reached an executor (route miss, early failure).
	Executor string
	// WorkDir is the workspace the executor ran in (the derived directory,
	// not the submitter's pin) — carried on the wire so the origin can
	// locate the produced files on the machine that made them.
	WorkDir string
	// RouteFallback names the peer the task was routed to before the link
	// failed at send time and it ran locally instead. Empty for a clean
	// local run or a real delegation — non-empty means Executor's local id
	// understates the user's intent ("on the Mac") and the report should say
	// so. The marker lives in the task's audit events (EvRouteFallback).
	RouteFallback string
	// Question carries the agent's clarification when the task parked in
	// review on a PANDA_QUESTION marker (core §4.3): the surface should show
	// it verbatim — it is the one thing standing between the task and done.
	Question string
	// FilesChanged lists the workdir-relative paths the run touched — the
	// result's footprint, so a surface can show what moved instead of trusting
	// the agent's prose.
	FilesChanged []string
	// Verified and Verification carry the delivery contract's verdict:
	// "verified" when every selected check ran and passed, "failed" when one
	// did not, "partial" when some were skipped, "" when the task carried no
	// contract. A done task with an empty or non-verified verdict is exactly
	// what a surface must not present as an unqualified success.
	Verified     bool
	Verification string
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
	// Warning is a non-fatal advisory the surface should print — currently
	// "no queue consumer is running" when the plan's stages were parked but
	// nothing on this node will drain them. It does not change OK.
	Warning string

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

	// Denied is set when a remembered "deny" answered the tier-2 gate before
	// the task existed: nothing was created and no prompt was shown. Kind
	// stays "task" so the refusal renders where a task card would have been.
	Denied bool
	// ConsentSource records which remembered scope supplied the consent when
	// one did ("session" or "project"), so surfaces can show the task ran on
	// a standing answer rather than a fresh prompt.
	ConsentSource string
}

// DispatchedTask is one row of a fan-out: a task this ask enqueued, named the
// way the transcript names it. It carries only what a surface needs to draw a
// receipt — the row itself is read back through the task API, which is where
// state, traces and the employee's face live.
type DispatchedTask struct {
	TaskID    string `json:"task_id"`
	TaskTitle string `json:"title,omitempty"`
	TaskState string `json:"state,omitempty"`
}

// dispatchedBriefs names every task a round handed out, in dispatch order.
//
// The receipt the Result carries is the last dispatch only, because that is
// what a one-card surface can draw. A batch — the model emitting several
// `task_submit` calls in one round — is invisible from that single receipt:
// the conversation shows one card while the board shows every row. Rows
// without an id (a submission that failed before a row existed) are skipped,
// and a batch of one returns nil so callers keep their old single-card path.
func dispatchedBriefs(ds []*Result) []DispatchedTask {
	var out []DispatchedTask
	for _, d := range ds {
		if d == nil || d.TaskID == "" {
			continue
		}
		out = append(out, DispatchedTask{TaskID: d.TaskID, TaskTitle: d.TaskTitle, TaskState: d.TaskState})
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// ApprovalRequest describes a tier-2 (irreversible) action awaiting the user's
// consent at the inline approval gate: the task the entry model routed and the
// executor's refusal reason. It is what an OnApproval callback renders and what
// a NeedsApproval Result carries back for a caller that prompts out-of-band.
type ApprovalRequest struct {
	TaskID  string
	Title   string
	Intent  string
	Reason  string // the executor's authorization-refusal message
	Project string // the task's project — the scope a project-level "remember" writes to
	Scope   string // the resolved default remember scope (once|session|project)
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
		if !errors.Is(err, entry.ErrNoModel) {
			db.Close()
			return nil, fmt.Errorf("askengine: model client: %w", err)
		}
		// No model configured: the engine still serves the model-free surface —
		// task/plan enqueue, cancel/approve, the queue board, session ops — on
		// nodes whose only job is executing delegated work (a Micro edge node
		// has no API key and needs none). Only the ask path wants a client, and
		// it fails lazily there with ErrNoModel instead of taking down New.
		client = nil
	}
	if client != nil {
		// Disk cache for entry-model decisions (classify/supervise): identical
		// inputs skip the LLM call entirely. Best-effort by design.
		client.SetDiskCache(entry.NewDiskCache(db))
	}

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
		daily:          memory.NewDaily(hermes.WarmDir()),
		projects:       projects,
		remind:         remind,
		skills:         skillStore,
		logger:         logger,
		queueTasks:     opts.QueueTasks,
		awaitQueued:    opts.AwaitQueued,
		asyncPeers:     opts.AsyncPeers,
		meshNode:       opts.MeshNode,
		localOnly:      opts.LocalOnly,
		replyASCII:     opts.ReplyASCII,
		cardPath:       opts.CardPath,
		configPath:     config.ResolvePath(opts.ConfigPath),
		locale:         loc,
		explicitLocale: explicitLocale,
	}
	if db != nil {
		// The gate fails closed without the store: approvalFor just reads
		// no project policy, so remembered/session answers still resolve.
		e.projStore = projectstore.NewStore(db)
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
			if e.sched.Load() == nil && explicitCard {
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
	if e.sched.Load() != nil {
		return
	}
	target := e.cardPathNow()
	if target == "" && e.cfg != nil {
		e.cfgMu.RLock()
		target = e.cfg.EffectiveCardPath()
		e.cfgMu.RUnlock()
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
	// The shared config supplies most of the scheduler's inputs; hold cfgMu
	// for the build so a concurrent /model or /nodes mutation cannot tear a
	// read mid-initialization. Lock order: schedMu → cfgMu (callers already
	// hold schedMu; nothing acquires schedMu while holding cfgMu).
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
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
	// Same machine facts the daemon stamps: the REPL self-registers too, and
	// its row must not read as "unknown device" just because a CLI wrote it.
	card.FormFactor = hwinfo.FormFactor()
	stableID := core.RuntimeNodeID(e.cfg.Node.Name, e.cfg.Node.Kind, card.NodeIdentity)
	// Snapshot the row's heartbeat stamp before Register overwrites it — but
	// only a stamp strictly above our own last write counts as foreign: a row
	// still carrying our stamp on a re-register (card reload) is not evidence
	// a consumer beats this store, and must not re-arm freshness on its own.
	if cur := core.NodeRowLastSeen(context.Background(), e.db, stableID); cur > e.selfRowWriteAt.Load() {
		e.selfRowForeign.Store(cur)
	}
	if err := ledger.Register(e.db, card, stableID, schedulerTier(e.cfg.Node.ResourceClass)); err != nil {
		e.logger.Warn("self-register failed", "node", stableID, "err", err)
	}
	e.selfRowWriteAt.Store(time.Now().Unix())
	// The engine's scheduler uses stableID so tasks created, claimed and queued
	// belong to this machine's stable identity rather than a fleeting hash
	// that leaves tasks stranded when the process exits.
	sched := core.NewCore(e.db, stableID, card, schedulerTier(e.cfg.Node.ResourceClass), e.logger, e.cfg.Model)
	sched.SetDelegationLimits(e.cfg.Delegation)
	// Self-evolution merge approvals may land on this engine (panda approve).
	// oneShot=true: an engine process must never re-exec itself onto the fresh
	// binary — the swap's install.RestartDaemon already bounces the service.
	if e.cfg.SelfEvolution.Enabled {
		sched.SetSelfEvolution(self.New(e.cfg.SelfEvolutionDir(), e.cfg.SelfEvolution.Repo, e.logger), true, e.cfg.SelfEvolution.Producers)
	}
	// Row ownership probe: the identity lock tells a borrowed engine (a CLI
	// sharing the daemon's stable node id) from the row's owner. A live daemon
	// holds the lock and heartbeats the row, so a borrowed engine's Shutdown
	// must not mark it offline — otherwise every `task add`/`ask` exit flips
	// the daemon's self row until the next beat, and routing sees the local
	// node as gone. Probe-only: the lock is released immediately so this
	// process never blocks a real daemon from starting.
	//
	// ownsRow=false carries the same signal further: the daemon already runs
	// the queue scheduler, task-lifecycle sweeps, peer links and discovery for
	// this identity. A second consumer in the same process family (the web
	// console, a REPL with QueueTasks) would race the daemon for claims on the
	// shared store and its sibling conns only churn the daemon's held edges —
	// one node identity, one work pipeline. Borrowed engines submit and read.
	ownsRow := true
	if lock, err := nodeidentity.Acquire(e.cfg.Node.Kind, card.NodeIdentity); err == nil {
		_ = lock.Release()
	} else if errors.Is(err, nodeidentity.ErrAlreadyRunning) {
		sched.SetOwnsNodeRow(false)
		ownsRow = false
	} else {
		// Anything else (an unwritable lock directory, a filesystem error) is
		// not evidence that this process owns the row. The permissive default
		// made it consume the same queue as the daemon — double claims, tasks
		// run twice — so an unknown state is treated as "borrowed" and logged
		// rather than assumed to be ours.
		sched.SetOwnsNodeRow(false)
		ownsRow = false
		e.logger.Warn("askengine: node identity lock unavailable; scheduling as a borrowed engine", "err", err)
	}
	sched.SetRouterPolicy(e.cfg.Injection, e.cfg.Routing)
	sched.AttachSupervisor(e.cfg.Model)
	if e.skills != nil {
		sched.SetMemoryStores(e.injector, e.daily, e.skills)
	}
	sched.SetProjectStores(projectstore.NewStore(e.db), e.cfg.Storage.ProjectsPath)
	sched.SetWorkDir(e.cfg.Storage.WorkPath)
	// Same workspace-scratch rule as daemon wiring: a work path that is not
	// the node data dir is a user-visible workspace, so runtime scratch
	// (task/plan dirs, landed remote projects) hides under .openpanda there
	// rather than appearing as three visible directories in the user's tree.
	if sr := e.cfg.ScratchRoot(); sr != "" {
		sched.SetScratchRoot(sr)
	}
	sched.SetHostStatePaths(e.cfg.HostStatePaths())
	sched.SetSharedSecret(e.cfg.Network.SharedSecret)
	sched.SetAllowCleartext(e.cfg.Network.AllowCleartext)
	sched.SetCleartextAllowlist(e.cfg.Network.AllowCleartextFor)
	sched.SetTrustConfig(e.cfg.Network.Trust)
	sched.SetPeerTrustHints(e.cfg.Network.PeerTrustHints())
	sched.SetReputationEnabled(e.cfg.Routing.Reputation)
	// The artifact pool is the data plane: a stage's packed output, named by
	// its hash, that a successor on another node pulls over the bus. The
	// daemon attaches one at startup; without the same attachment here, an
	// engine instance executing a plan stage packed nothing — silently,
	// because the pool was nil — and every successor ran against an empty
	// directory with its predecessor's files nowhere in the mesh.
	artifactStore := artifact.NewStore(e.cfg.Storage.ArtifactPath, e.cfg.Storage.ArtifactExtraPaths...)
	artifactStore.SetMaxBytes(e.cfg.Storage.ArtifactMaxBytes)
	artifactStore.SetMinFreeBytes(e.cfg.Storage.ArtifactMinFreeBytes)
	sched.SetArtifactStore(artifactStore)
	// OS sandbox (sandbox.*): the same contract the daemon installs — the
	// environment filter always applies, and a configured mode wraps every
	// subprocess this engine spawns in the platform's confinement with the
	// node's own state write-protected. Called unconditionally on every
	// scheduler init (not just when a mode is set) so a config reload that
	// turned the sandbox off resets the process-wide base policy instead of
	// leaving the previous mode armed.
	if backend := commander.SetSandboxConfig(e.cfg.Sandbox,
		commander.ProtectedPaths(e.cfg, e.configPath, cardPath),
		commander.SecretReadDenyPaths(e.cfg, e.configPath, cardPath)); e.cfg.Sandbox.NormalizedMode() != "off" {
		if backend == "" {
			e.logger.Warn("sandbox mode configured but no backend on this platform",
				"mode", e.cfg.Sandbox.NormalizedMode())
		} else {
			e.logger.Info("subprocess sandbox enabled",
				"mode", e.cfg.Sandbox.NormalizedMode(), "backend", backend)
		}
	}
	sched.SetTimeouts(e.cfg.Timeouts)
	sched.SetAcceptanceConfig(e.cfg.Execution.Acceptance)
	sched.SetMessageBudget(e.cfg.Messages.Budget)
	sched.SetRetentionDays(e.cfg.Storage.EffectiveTaskRetentionDays())

	if e.schedCancel != nil {
		e.schedCancel()
	}
	schedCtx, cancel := context.WithCancel(context.Background())
	e.schedCtx = schedCtx
	e.schedCancel = cancel
	e.sched.Store(sched) // store last: a non-nil load implies ctx/cancel set

	// ownCtx scopes the duties only a node-row owner may run — the mesh
	// listener, discovery, outbound edge keepalives, the queue consumer and
	// the lifecycle monitor. This engine owns the row only provisionally:
	// it probed the lock free at init because no daemon was up, and the
	// moment a real daemon takes the lock these duties must die with it —
	// or the stray keeps the listen port the daemon needs to bind and
	// churns edges it no longer owns. watchNodeRow is the demotion tripwire.
	ownCtx := schedCtx
	if ownsRow {
		var ownCancel context.CancelFunc
		ownCtx, ownCancel = context.WithCancel(schedCtx)
		e.goBG("askengine: node row watch", func() {
			e.watchNodeRow(ownCtx, sched, ownCancel, card)
		})
	}

	// The embedded core owns the LAN-discovery listener in a process that
	// never runs `panda daemon` (the interactive REPL/TUI seat): without it
	// the seat sees only fleet rows it already paired, never the pending
	// beacons discovery exists to surface. A port held by a daemon on the
	// same box degrades to warn-and-idle — the loser keeps its fleet, loses
	// nothing else. A borrowed engine skips it entirely: the daemon holds
	// the socket already.
	sched.EnsureNodeKey()
	if ownsRow && !e.localOnly && e.cfg.Network.DiscoveryAddr != "off" {
		// Auto-dial only for a mesh-owning process (web console, sidecar): a
		// long-lived owner behaves like the daemon. An interactive seat is
		// short-lived, so edges it opens would flap for peers on every exit —
		// the seat listens for pending hints only.
		e.goBG("askengine: discovery", func() {
			sched.RunDiscovery(ownCtx, e.cfg.Network.DiscoveryAddrOrDefault(), e.cfg.Network.ListenAddr, e.meshNode)
		})
	}
	// A mesh-owning engine also serves inbound peers and holds the outbound
	// keepalives — the transport ownership a web-only node was missing when
	// it could consume queued work but never be reached or redialed.
	if ownsRow && e.meshNode {
		if e.cfg.Network.SharedSecret != "" {
			// Bind the address eagerly: a config reload swaps e.cfg, and a
			// closure that reads it lazily would listen on the new address.
			listenAddr := e.cfg.Network.ListenAddr
			e.goBG("askengine: ws listen", func() {
				if err := sched.Listen(ownCtx, listenAddr); err != nil {
					e.logger.Warn("websocket listener failed", "addr", listenAddr, "err", err)
				}
			})
		}
		sched.SyncPeers(ownCtx, e.cfg.Network.PeerAddrs())
	}
	e.setCardPath(cardPath)
	// Re-arm the review hook on the new core's store: the notification that a
	// task is waiting for the user must not be lost to a card reload.
	e.onReviewMu.RLock()
	fn := e.onReview
	e.onReviewMu.RUnlock()
	if fn != nil && sched.TaskStore() != nil {
		sched.TaskStore().SetOnReview(fn)
	}

	// Clean up stale leases from interrupted runs so tasks aren't stranded in
	// running: with retry budget left they re-queue for another execution
	// rather than dying on a lease their previous process stopped renewing.
	if sched.TaskStore() != nil {
		if expired, err := sched.TaskStore().ExpireTasks(context.Background(), sched.NodeID()); err != nil {
			e.logger.Debug("expire stale tasks at startup", "err", err)
		} else if len(expired) > 0 {
			e.logger.Info("settled stale tasks from previous run", "count", len(expired), "tasks", expired)
		}
	}

	// Only the row owner heartbeats: a borrowed engine writing last_seen would
	// fight the daemon's ownership of the row. But an owning engine (a `panda
	// web` console standing alone, a QueueTasks REPL) must beat or its own row
	// goes stale minutes after init — peers then mark the node offline and
	// stop routing it work, and store-bound liveness probes (StoreConsumerFresh)
	// would read its live queue consumer as dead. The wire half of the beat is
	// a no-op when the engine has no peer conns.
	if ownsRow && (e.meshNode || e.queueTasks) {
		e.goBG("askengine: heartbeat", func() { sched.RunHeartbeat(ownCtx) })
	}

	// Only queue-mode surfaces own a background consumer, and only the row
	// owner's engine may be it: a borrowed engine that also drained the shared
	// queue would race the daemon's claims and run two work pipelines for one
	// node — the split the row-ownership rule exists to prevent. One-shot
	// ask/REPL engines submit inline and must not unexpectedly drain
	// persisted work either way.
	if e.queueTasks && ownsRow {
		sched.StartQueueScheduler(ownCtx)
		// A queue engine executes tasks off the shared store, so it also owns
		// the task-lifecycle sweeps: without them a waiting_context park never
		// times out, an orphaned forward is never rescued, and an expired
		// lease is never enforced — the task would stall with nobody told.
		e.goBG("askengine: task monitor", func() { sched.RunTaskMonitor(ownCtx) })
	}
	// Every engine reconciles its own executions against the store: a task
	// cancelled or failed by another process must stop here too, not run on
	// as a zombie the user believes is still working.
	e.goBG("askengine: reconcile", func() { sched.RunReconcile(schedCtx) })

	if !ownsRow {
		// The daemon owns the peer edges for this identity — its keepalive
		// conns are the incumbents our hello arbitration holds, so a borrowed
		// engine's startup dials would land as same-id sibling sessions that
		// get held, answered and closed on the far side: pure churn. Tasks the
		// borrowed engine enqueues route and forward on the daemon's links
		// through the shared store. On-demand Engine.DialPeer stays available
		// for an explicit "nodes add".
		return nil
	}
	if e.meshNode {
		// SyncPeers above already owns every configured outbound edge (with
		// reconnect keepalive), so the one-shot dial pass below would only
		// double-dial into the arbitration it exists to deduplicate.
		return nil
	}
	if e.localOnly {
		// A write-and-exit command has no use for a live edge: the task it
		// just enqueued is routed by whatever daemon or console owns the
		// queue, and a conn this process opened would close with it.
		return nil
	}
	peerAddrs := e.cfg.Network.PeerAddrs()
	if e.asyncPeers {
		for _, peer := range peerAddrs {
			e.goBG("askengine: peer dial "+peer, func() {
				if err := sched.DialPeer(schedCtx, peer); err != nil {
					e.logger.Debug("peer dial failed", "peer", peer, "err", err)
				}
			})
		}
	} else {
		var wg sync.WaitGroup
		for _, peer := range peerAddrs {
			wg.Add(1)
			e.goBG("askengine: peer dial "+peer, func() {
				defer wg.Done()
				if err := sched.DialPeer(schedCtx, peer); err != nil {
					e.logger.Debug("peer dial failed", "peer", peer, "err", err)
				}
			})
		}
		wg.Wait()
		if len(peerAddrs) > 0 {
			waitForPeers(schedCtx, e.db, 2*time.Second)
		}
	}
	return nil
}

// watchNodeRow demotes a provisional row owner back to borrowed status.
// Every engine-side owner is provisional by construction: the real daemon is
// a separate process, and an engine only probed the lock free because no
// daemon was up at init. Once a daemon takes the node-identity lock, this
// engine must shed the owner's duties — the mesh listener (which holds the
// port the daemon needs to bind), LAN discovery, outbound edge keepalives,
// the queue consumer and the lifecycle monitor — or it becomes the stray
// that wedges the real node out. The probe is a cheap flock attempt; on
// ErrAlreadyRunning the duties' ownCtx dies and the core stops claiming the
// row, so its remaining conns arbitrate as ordinary siblings.
func (e *Engine) watchNodeRow(ctx context.Context, sched *core.Core, demote context.CancelFunc, card ledger.Card) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		held, err := nodeidentity.Held(e.cfg.Node.Kind, card.NodeIdentity)
		if err != nil {
			continue // a transient lock failure is not proof of a new owner
		}
		if !held {
			continue // still the only claimant — stay owner
		}
		e.logger.Warn("node row claimed by the daemon — demoting borrowed mesh duties")
		sched.SetOwnsNodeRow(false)
		demote()
		return
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
	// OnSteerConsumed fires once per idea GetSteer handed over, with the round
	// that will carry it. A surface that accepted a note has only proved it
	// reached a channel; this is the proof the model will see it, and without
	// it "已注入" is a claim the caller cannot back up — the last round of a
	// turn can start and finish before anything polls the channel again.
	OnSteerConsumed func(text string, round int)
	// OnTaskRow fires once, with the id of the task row this turn created,
	// before that task executes. The Result carries the same id, but it
	// arrives after the work is over — far too late for a surface that wants
	// to type into the running task (queue on the first Enter, inject on the
	// second). Nil for callers that only read the outcome.
	OnTaskRow func(taskID string)
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
	// ProgressSpawn is the fan-out milestone: a running agent emitted a
	// PANDA_DELEGATE request and the runtime spawned a causal child task for
	// it. It makes parallel decomposition visible — several in a row read as
	// the orchestrator dispatching sub-agents, not as repeated routing noise.
	ProgressSpawn ProgressKind = "spawn" // a delegate child task was spawned
	ProgressJudge ProgressKind = "judge" // a supervision round is evaluating the result
	// ProgressWait is the round following a task the queue parked — its link
	// is not live yet, so the work has not started. Name carries the observed
	// state ("queued", "dispatched"), emitted on entering the wait and on
	// every state change, so the card keeps advancing while the round waits
	// instead of sitting frozen at the dispatch.
	ProgressWait ProgressKind = "wait"
	// ProgressRetry is emitted each time a model call schedules a transport
	// retry (429, 5xx, network drop): Attempt counts to MaxAttempt while
	// Delay is the backoff about to be slept, Status the HTTP answer the
	// provider gave (0 when no response arrived), and Reason the machine
	// error class (entry.ErrKind*). Without it a rate-limited provider sits
	// behind a motionless spinner for the whole retry ladder.
	ProgressRetry ProgressKind = "retry"
	// ProgressOffline is emitted while the offline gate (entry/offline.go)
	// waits for a lost link: Detail is "waiting" on entering the wait and
	// "resumed" once the network answered again, Delay how long the outage
	// lasted, Name the model whose call stalled. It is a separate kind from
	// ProgressRetry on purpose — "the link is down" and "the provider failed"
	// are different problems, and a surface that shows the second for the
	// first sends the user to the wrong page.
	ProgressOffline ProgressKind = "offline"
	// ProgressAgentText carries one block of the delegated agent's own prose
	// (or an errored tool result) verbatim in Detail. It is live display
	// material, not a lifecycle milestone: a task that runs for twenty
	// minutes must show the work happening, not a frozen spinner.
	ProgressAgentText ProgressKind = "agent_text"
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
	// Detail qualifies the milestone without a locale: for a spawn it is
	// the parallel-mode descriptor — "isolated" for a private-copy child,
	// the comma-joined lane paths for a scoped one, "" for the default
	// whole-project claim; for an offline milestone it is "waiting" or
	// "resumed". Renderers phrase it in their own language.
	Detail string
	// Retry fields, valid when Kind == ProgressRetry: Attempt/MaxAttempt are
	// the retry's position inside the call's budget, Delay the backoff about
	// to be slept, Status the provider's HTTP answer (0 on transport failure),
	// Reason the entry.ErrKind* class. Delay doubles as the outage's length on
	// a resumed ProgressOffline.
	Attempt    int
	MaxAttempt int
	Delay      time.Duration
	Status     int
	Reason     string
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
// English prose lives in StatusText — kept separate so a structured consumer
// (the panel's SSE stream) can send the same sentence plus the fields it
// needs to re-phrase it in the browser's own locale.
func (cb StreamCallbacks) progress(p Progress) {
	if cb.OnProgress != nil {
		cb.OnProgress(p)
		return
	}
	if cb.OnStatus == nil {
		return
	}
	cb.OnStatus(StatusText(p))
}

// StatusText phrases one progress event as one-line English prose — the
// fallback wording surfaces use when they did not bind OnProgress, and the
// wire text the panel re-localizes for the browser from the same event.
func StatusText(p Progress) string {
	switch p.Kind {
	case ProgressTask:
		return fmt.Sprintf("submitting task: %s", p.Name)
	case ProgressPlan:
		return fmt.Sprintf("starting plan: %s", p.Name)
	case ProgressTool:
		return fmt.Sprintf("running tool %s…", p.Name)
	case ProgressRoute:
		return fmt.Sprintf("routing to %s…", p.Name)
	case ProgressExec:
		extra := ""
		if p.Model != "" {
			extra = fmt.Sprintf(" (%s)", p.Model)
		}
		return fmt.Sprintf("running %s%s%s…", p.Name, extra, roundNote(p))
	case ProgressSpawn:
		if p.Detail != "" {
			// The mode descriptor says how the child relates to the shared
			// tree — a private copy, or the lanes it claims.
			return fmt.Sprintf("spawning sub-agent %s (%s)…", p.Name, p.Detail)
		}
		return fmt.Sprintf("spawning sub-agent %s…", p.Name)
	case ProgressJudge:
		// A judge_start marker arrives with no verdict yet; phrase it without
		// the empty parens the missing verdict used to leave behind.
		if p.Name == "" {
			return fmt.Sprintf("reviewing result%s…", roundNote(p))
		}
		return fmt.Sprintf("reviewing result (%s)%s…", p.Name, roundNote(p))
	case ProgressWait:
		return fmt.Sprintf("watching the task (%s)…", p.Name)
	case ProgressRetry:
		reason := "connection failed"
		if p.Status > 0 {
			reason = fmt.Sprintf("HTTP %d", p.Status)
		}
		return fmt.Sprintf("model call failed (%s) — retrying in %ds · %d/%d",
			reason, int(p.Delay.Seconds()), p.Attempt, p.MaxAttempt)
	case ProgressOffline:
		// The link, not the provider: phrase it as something being waited
		// out, never as a failure the user has to act on.
		if p.Detail == "resumed" {
			return fmt.Sprintf("network restored after %ds — replaying the model call%s",
				int(p.Delay.Seconds()), modelNote(p))
		}
		return fmt.Sprintf("network unreachable — waiting for it to come back, then replaying automatically%s",
			modelNote(p))
	case ProgressAgentText:
		// The agent's own words: already prose, already in its own language.
		// Bounded because the fallback sentence is a status line, not a report.
		return truncateRunes(p.Detail, 200)
	}
	return p.Name
}

// modelNote renders the "(model)" tail a progress line carries when the event
// names one, and "" otherwise.
func modelNote(p Progress) string {
	if p.Name == "" {
		return ""
	}
	return fmt.Sprintf(" (%s)", p.Name)
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
	sched := e.sched.Load()
	if sched == nil {
		return
	}
	sched.SetRouterPolicy(injection, routing)
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
	// SessionID keys the conversation a remembered "session" approval belongs
	// to — the REPL's active session, a panel chat's id. Empty means the
	// session-less bucket (a bare REPL, `panda ask`), whose decisions live and
	// die with the process.
	SessionID string

	// AmbientProject falls back to the engine's ambient (entered) project
	// when Project is empty — the compatibility default for CLI callers that
	// do not pin a project per ask. Exported so scoped callers outside this
	// package (panel sessions) can make the same choice explicitly.
	AmbientProject bool

	// Node pins this ask's classified task to a preferred executor node
	// (id or name). A model-emitted spec.node always wins — the scope value
	// is the composer's explicit pick, used when the model leaves routing
	// to the scheduler. Plans are unaffected: per-stage routing is the
	// point of a pipeline, so a flat node pin would defeat it.
	Node string

	// Agents is the thread's roster: the agent names this conversation is
	// allowed to hand work to. It is copied into every classified task's spec
	// (allow_agents) and enforced at routing time on whichever node executes
	// the task, so it constrains plans, sub-agents and remote hops alike
	// rather than being a hint the model may ignore. Empty means no
	// restriction — the scheduler routes by capability, as before.
	Agents []string

	// PermissionMode is the conversation's permission policy (defense.Mode*:
	// auto, manual, edits, plan, bypass). It rides every tool gate and is
	// copied into each classified task's spec so the executor — possibly a
	// peer — applies the same policy. Empty reads as auto.
	PermissionMode string

	// Acceptance is the user's own contract for any task this ask produces
	// (CLI --accept). It is appended to the classified task's spec and, unlike
	// a model-selected check, bypasses the project shortlist: the user is the
	// authority on how their own work is judged.
	Acceptance []contract.Check
}

// AskTurns is the backward-compatible session-aware entry point. Callers that
// own explicit project context should use AskTurnsScoped.
func (e *Engine) AskTurns(ctx context.Context, history []entry.Turn, prompt, workDir string, authorize bool, cb StreamCallbacks) (res *Result, err error) {
	return e.AskTurnsScoped(ctx, history, prompt, AskScope{WorkDir: workDir, AmbientProject: true}, authorize, cb)
}

// AskTurnsSession is AskTurnsScoped plus the session id a remembered "session"
// approval binds to. "" degrades to the session-less bucket.
func (e *Engine) AskTurnsSession(ctx context.Context, history []entry.Turn, prompt, workDir, mode, sessionID string, authorize bool, cb StreamCallbacks) (res *Result, err error) {
	return e.AskTurnsScoped(ctx, history, prompt, AskScope{WorkDir: workDir, Mode: mode, SessionID: sessionID, AmbientProject: true}, authorize, cb)
}

// AskTurnsScoped is the session-aware ask with request-scoped project/workspace.
func (e *Engine) AskTurnsScoped(ctx context.Context, history []entry.Turn, prompt string, scope AskScope, authorize bool, cb StreamCallbacks) (res *Result, err error) {
	workDir := scope.WorkDir
	client, fallbackUsed := e.healthyClient()
	if client == nil {
		return nil, entry.ErrNoModel
	}
	if fallbackUsed != "" {
		primaryName := "<none>"
		if p := e.client.Load(); p != nil {
			primaryName = p.ModelName()
		}
		e.logger.Info("askengine: primary model unavailable or in circuit breaker cooldown, routing directly to fallback", "primary", primaryName, "fallback", fallbackUsed)
		cb.progress(Progress{Kind: ProgressRoute, Name: fallbackUsed})
	}

	// Report every scheduled model retry as a progress event so a 429/5xx or
	// network drop shows "retrying in Ns" on the surface's status line instead
	// of looking like a silent hang for the whole backoff ladder. The hook
	// rides the ask's context: whichever client eventually serves (primary or
	// a mid-ask fallback) reports under its own name.
	ctx = entry.WithRetryHook(ctx, func(n entry.RetryNote) {
		kind, status := entry.APIErrorKind(n.Err)
		name := ""
		if c := client; c != nil {
			name = c.ModelName()
		}
		cb.progress(Progress{
			Kind: ProgressRetry, Name: name,
			Attempt: n.Attempt, MaxAttempt: n.MaxAttempts,
			Delay: n.Delay, Status: status, Reason: kind,
		})
	})
	// The offline gate reports on its own channel: "the link is down, waiting
	// for it" is not a retry milestone, and a surface that folded the two
	// together would blame the provider for the user's Wi-Fi.
	ctx = entry.WithOfflineHook(ctx, func(n entry.OfflineNote) {
		name := ""
		if c := client; c != nil {
			name = c.ModelName()
		}
		detail := "waiting"
		if !n.Waiting {
			detail = "resumed"
		}
		cb.progress(Progress{Kind: ProgressOffline, Name: name, Detail: detail, Delay: n.Elapsed})
	})
	// One engine serves every conversation on this node, while a routing
	// gateway (OpenCode) keys its session, cache and rate accounting on a
	// per-request header. Scoping the ask to its thread keeps two chats from
	// sharing provider-side state; an ask with no thread keeps the client's
	// own id.
	ctx = entry.WithSessionID(ctx, scope.SessionID)

	// The reply locale is resolved up here rather than next to the classifier
	// options because the deferred catch-all below also speaks it: an escaped
	// model error is re-dressed in the ask's own language.
	effectiveLocale, explicit := e.localeNow()
	if !explicit && !e.replyASCII {
		if containsHan(prompt) {
			effectiveLocale = i18n.ChineseSimp
		}
	}

	// Bill the commander model's own token consumption for this ask into the
	// delegation metrics once it finishes (whatever the outcome), so the
	// panel's tokens column shows entry-model cost alongside adapter
	// delegations. The record survives the ask's context via WithoutCancel.
	usageBefore := client.Usage()
	askStart := time.Now()
	defer func() {
		// Catch-all for model-call failures that escaped unwrapped (a raw
		// statusError/retryableError reads as provider JSON soup to a user).
		// Non-model failures pass through untouched: a storage or routing
		// error keeps its own wording rather than wearing a model message.
		if err != nil {
			err = entry.EnsureAPIError(err, effectiveLocale)
		}
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
				loc, _ := e.localeNow()
				res.Note = i18n.Tf(loc, "entry.note.fallback", "name", fallbackUsed)
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
	// The thread's roster goes into the prompt as well as into the task spec.
	// The spec is what the executor enforces; the prompt is what keeps the
	// entry model from proposing an agent the executor will refuse — asked to
	// "test the agents I picked" with no roster in sight, it invented
	// claude_code and codex for a thread whose team was deepseek_harness and
	// opencode.
	if len(scope.Agents) > 0 {
		classifyOpts = append(classifyOpts, entry.WithAgentRoster(scope.Agents))
	}

	// The workspace's own verification entries, offered as a shortlist. The
	// model selects from it; it never authors a command. Without this the
	// classifier has no grounded way to name an acceptance check, and every
	// task it creates is done-but-unverified.
	if workDir != "" {
		if cands := contract.Enumerate(workDir); len(cands) > 0 {
			classifyOpts = append(classifyOpts, entry.WithAcceptanceCandidates(cands))
		}
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
					// The fast path bypasses the classify wrapper, so its
					// failures arrive raw — dress them before they escape
					// or the user sees "entry: api status 429: {json}"
					// instead of the actionable message.
					return nil, entry.EnsureAPIError(rerr, effectiveLocale)
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
	// The tool gate shares the task gate's consent semantics, resolved through
	// the same verdict: "never" auto-consents, a remembered session/project
	// answer is replayed, an explicit session grant (--authorize / /authorize
	// / the panel's authorize field) satisfies on-request, and "always"
	// withholds consent no matter the grant — every tier-2 operation is
	// decided per-action in the foreground.
	scopeProj, _ := e.scopeProject(scope)
	verdict := e.approvalFor(scope.SessionID, scopeProj).withPermission(scope.PermissionMode)
	toolAuthorized, toolDenied := verdict.gate(authorize)
	approvalMode := verdict.mode
	const maxRounds = 6
	// The sub-agent budget is derived, not fixed: a task round is minutes of
	// work and a full agent transcript of tokens, so it stays separate from
	// (and inside) the round budget — but its width comes from what the user
	// actually asked for. The roster is their own count of hands; task_budget
	// lets the model declare more for a fan-out nobody enumerated. See
	// budget.go for why the prompt itself is not parsed.
	budget := newTaskBudget(len(scope.Agents))
	// dispatchedThisAsk answers a re-sent task_submit with the id it already
	// has: a refusal teaches the model to retry, and retrying the whole batch
	// must not run the tasks that already went out a second time.
	dispatchedThisAsk := make(map[string]string)
	// budgetAdvisory rides every exit: a dispatch the budget turned away is a
	// decision the user must be able to see, not a task that quietly never
	// existed.
	budgetAdvisory := func(r *Result) *Result {
		if r == nil {
			return r
		}
		note := budgetWarning(budget, effectiveLocale)
		if note == "" {
			return r
		}
		if r.Warning == "" {
			r.Warning = note
		} else {
			r.Warning += " · " + note
		}
		return r
	}
	// lastTask keeps the most recent task outcome so the converged Result
	// carries the task fields (id/state/output) alongside the model's report.
	var lastTask *Result
	finalizeLastTask := func(t *Result) *Result {
		if t != nil && strings.TrimSpace(t.Answer) == "" && strings.TrimSpace(t.Report) == "" {
			if report, rerr := entry.SummarizeResult(ctx, client, t.TaskTitle, "", t.OK, t.ExitCode, t.Stdout, t.Stderr, effectiveLocale); rerr == nil {
				t.Report = report
				e.noteDailyObservation(report)
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
		if lastTask != nil {
			// A task ran: its outcome IS the answer. Dumping the raw tool
			// results beside it buries the outcome in logs the user never
			// asked for (the live Windows report showed a ten-line task-list
			// dump after a successful browser-open task); a compact
			// interruption note keeps the honesty without the noise, and the
			// transcript stays one `task show` away.
			if len(toolDigest) > 0 {
				note := interruptionNote(len(toolDigest), lastTask.TaskID, effectiveLocale)
				if strings.TrimSpace(lastTask.Answer) == "" {
					lastTask.Answer = note
				} else if !strings.Contains(lastTask.Answer, note) {
					lastTask.Answer = strings.TrimRight(lastTask.Answer, "\n") + "\n\n" + note
				}
			}
			return finalizeLastTask(lastTask), nil
		}
		if digest := toolResultsDigest(toolDigest, effectiveLocale); digest != "" {
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
		// task_budget sits beside task_submit: the limit is derived (roster
		// size or defaultTaskBudget), and this is how a model widens it for a
		// fan-out the user described in prose instead of in the team picker.
		reg.Register(e.taskBudgetTool(budget, effectiveLocale))
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
				// Tell the surface the note is now this round's input — the
				// moment "queued" becomes "delivered".
				if cb.OnSteerConsumed != nil {
					cb.OnSteerConsumed(idea, round)
				}
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
				e.recordModelFailure(client.Key())
				run := func(c *entry.Client) (entry.Output, error) {
					if cb.OnDelta != nil || cb.OnReasoning != nil {
						return entry.ClassifyStreamWithTools(ctx, c, devices, conversationMemory, turns, reg, cb.OnDelta, trackReasoning, classifyOpts...)
					}
					return entry.ClassifyTurnsWithTools(ctx, c, devices, conversationMemory, turns, reg, classifyOpts...)
				}
				fb, fbOut, ok := e.tryFallbackModels(ctx, client, run, func(name string) {
					cb.progress(Progress{Kind: ProgressRoute, Name: name})
				})
				if !ok {
					return digestOrErr(err)
				}
				client = fb
				usageBefore = fb.Usage()
				fallbackUsed = fb.ModelName()
				out = fbOut
				err = nil
			} else {
				return digestOrErr(err)
			}
		} else {
			e.recordModelSuccess(client.Key())
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
				return budgetAdvisory(finalizeLastTask(lastTask)), nil
			}
			return budgetAdvisory(&Result{Kind: "answer", Answer: out.Answer, Thought: accumulatedReasoning.String()}), nil
		case entry.KindTask:
			if budget.exhausted() {
				// Refuse before submitTask: the budget limits actual task
				// execution, not merely how many outcomes are replayed. The
				// refusal is recorded so the surface can say what is missing
				// — and so the model knows it may raise the limit and retry.
				budget.refuse(out.Task.Title)
				turns = append(turns,
					entry.Turn{Role: "assistant", Content: taskDispatchNote(out.Task, effectiveLocale)},
					entry.Turn{Role: "user", Content: budgetRefusalNote(budget, out.Task.Title, effectiveLocale)},
				)
				break rounds
			}
			if e.sched.Load() == nil {
				e.tryAutoInitScheduler()
			}
			if e.sched.Load() == nil {
				return nil, fmt.Errorf("task output requires a capability card (scheduler initialization failed)")
			}
			cb.progress(Progress{Kind: ProgressTask, Name: out.Task.Title})
			res := e.submitTask(ctx, out.Task, prompt, authorize, scope, accumulatedReasoning.String(), cb, e.awaitQueued, effectiveLocale)
			if e.queueTasks && !e.awaitQueued {
				// Receipt mode: the board product. The queued pointer is the
				// result — the session streams the task's progress and the
				// finalizer folds its summary, so there is no observation to
				// feed back and nothing to report in this turn. Await mode
				// (awaitQueued) already followed the row to settle inside
				// submitTask, so its res falls through to the ordinary
				// observation loop like an inline outcome.
				return budgetAdvisory(res), nil
			}
			if res.NeedsApproval {
				// The inline gate parks the ask; the front-end prompts on its
				// own event loop and resumes via ResumeApprovedReport.
				return res, nil
			}
			if ctx.Err() != nil {
				// The wait was released mid-flight (Esc / Ctrl-C): report the
				// task's last observed state and end the round. The cancelled
				// context would fail the next model call anyway, and the task
				// itself keeps running — a released front end never cancels
				// queued work.
				return budgetAdvisory(res), nil
			}
			budget.spend()
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
			return e.startClassifiedPlan(ctx, out.Plan, scope, authorize, cb)
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
			refusedThisRound := 0
			for i, call := range calls {
				cb.progress(Progress{Kind: ProgressTool, Name: call.Tool})
				if call.Tool == entry.ToolNameTaskSubmit {
					// A refused dispatch invites a retry: the model raises the
					// budget and re-sends the batch. Re-sending the whole batch
					// would re-run the tasks that already went out, so the same
					// dispatch is answered with the id it already has instead of
					// being executed twice.
					key := dispatchKey(call.Arguments)
					if id, done := dispatchedThisAsk[key]; done {
						results[i] = alreadyDispatchedNote(id, effectiveLocale)
						continue
					}
					if budget.exhausted() {
						// task_submit is a real delegation hidden behind the
						// native tool protocol. Refuse it before executeTool for
						// the same hard execution budget as a classified task
						// directive — and say which task, so the model can raise
						// the limit (task_budget) and re-dispatch instead of
						// losing it.
						title, _ := call.Arguments["title"].(string)
						budget.refuse(title)
						refusedThisRound++
						results[i] = budgetRefusalNote(budget, strings.TrimSpace(title), effectiveLocale)
						continue
					}
				}
				// Execute against the same registry snapshot classification
				// saw: a mid-ask SetMCPCommand swap would otherwise make the
				// model's tool call hit a registry that no longer knows it.
				results[i] = executeTool(ctx, reg, call, toolAuthorized, toolDenied, approvalMode, effectiveLocale)
				toolDigest = append(toolDigest, call.Tool+": "+results[i])
				for _, d := range taskCapture.takeAll() {
					dispatched = append(dispatched, d)
					if !d.NeedsApproval && d.TaskID != "" {
						budget.spend()
						// A task refused in an earlier round and dispatched
						// now is no longer missing.
						budget.settled(d.TaskTitle)
						dispatchedThisAsk[dispatchKey(call.Arguments)] = d.TaskID
					}
				}
			}
			turns = appendToolCalls(turns, calls, out.Note, results, effectiveLocale)

			if e.queueTasks && e.awaitQueued {
				// Foreground multi-task: the tool already enqueued every
				// dispatch in parallel above. Follow each row to settle —
				// concurrently — and feed all outcomes back so the next round
				// reports real results instead of a bare "queued".
				for _, d := range e.awaitDispatched(ctx, e.sched.Load(), dispatched, cb) {
					if d == nil {
						continue
					}
					if d.NeedsApproval {
						return d, nil
					}
					lastTask = d
					turns = append(turns, entry.Turn{Role: "user", Content: taskObservation(d, effectiveLocale)})
				}
				break
			}
			var queuedReceipt *Result
			for _, d := range dispatched {
				switch {
				case e.queueTasks && d.TaskID != "" && !d.NeedsApproval:
					// Dispatch receipts accumulate: a round that fans out
					// several task_submit calls (multi-project work) has
					// enqueued them all in parallel — report the last receipt
					// below rather than dropping the rest.
					queuedReceipt = d
				case e.queueTasks || d.NeedsApproval || d.TaskID == "":
					return d, nil
				default:
					lastTask = d
				}
			}
			if queuedReceipt != nil {
				queuedReceipt.Dispatched = dispatchedBriefs(dispatched)
				if refusedThisRound == 0 {
					return budgetAdvisory(queuedReceipt), nil
				}
				// Something was turned away this round. Ending the turn on the
				// receipt is what made a dropped agent invisible: the receipt
				// says "dispatched" and nothing says what did not go out. Give
				// the model its next turn to act on the refusal (task_budget,
				// then re-dispatch the named tasks), and keep the receipt as
				// lastTask so a model that reports instead still lands on the
				// task card — with the advisory attached.
				lastTask = queuedReceipt
			}
		default:
			return budgetAdvisory(&Result{Kind: "answer", Answer: out.Answer}), nil
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
			e.recordModelFailure(client.Key())
			run := func(c *entry.Client) (entry.Output, error) {
				if cb.OnDelta != nil || cb.OnReasoning != nil {
					return entry.ClassifyStreamWithTools(ctx, c, devices, conversationMemory, turns, nil, cb.OnDelta, cb.OnReasoning, classifyOpts...)
				}
				return entry.ClassifyTurns(ctx, c, devices, conversationMemory, turns, classifyOpts...)
			}
			fb, fbOut, ok := e.tryFallbackModels(ctx, client, run, func(name string) {
				cb.progress(Progress{Kind: ProgressRoute, Name: name})
			})
			if ok {
				client = fb
				usageBefore = fb.Usage()
				fallbackUsed = fb.ModelName()
				final = fbOut
				ferr = nil
			}
		}
		if ferr != nil {
			// The tools already ran — report what they did rather than a bare
			// loop error. toolDigest is empty only when nothing ever executed,
			// in which case the wrapped error stands.
			return digestOrErr(fmt.Errorf("reached max tool rounds (%d): %w", maxRounds, ferr))
		}
	} else {
		e.recordModelSuccess(client.Key())
	}
	if final.Kind == entry.KindTask {
		if e.sched.Load() == nil {
			e.tryAutoInitScheduler()
		}
		if e.sched.Load() == nil {
			return &Result{Kind: "answer", Answer: i18n.Tf(effectiveLocale, "ask.loop.noConvergeTask", "n", strconv.Itoa(maxRounds), "title", final.Task.Title)}, nil
		}
		if lastTask != nil && budget.exhausted() {
			// The loop exhausted the task budget and the model still wants
			// another delegation: surface what ran instead of exceeding it.
			budget.refuse(final.Task.Title)
			return budgetAdvisory(finalizeLastTask(lastTask)), nil
		}
		cb.progress(Progress{Kind: ProgressTask, Name: final.Task.Title})
		res := e.submitTask(ctx, final.Task, prompt, authorize, scope, accumulatedReasoning.String(), cb, e.awaitQueued)
		if !res.NeedsApproval && (!e.queueTasks || e.awaitQueued) {
			// No rounds left to converge through, so produce the report in
			// one shot rather than returning raw output.
			if report, rerr := e.reportTaskOutcome(ctx, client, turns, devices, conversationMemory, classifyOpts, final.Task, res); rerr == nil {
				res.Answer = report
			} else {
				e.logger.Warn("askengine: task report degraded", "task", res.TaskID, "err", rerr)
			}
		}
		return budgetAdvisory(res), nil
	}
	if final.Kind == entry.KindPlan {
		if e.sched.Load() == nil {
			e.tryAutoInitScheduler()
		}
		if e.sched.Load() == nil {
			return &Result{Kind: "answer", Answer: i18n.Tf(effectiveLocale, "ask.loop.noConvergePlan", "n", strconv.Itoa(maxRounds), "goal", final.Plan.Goal)}, nil
		}
		cb.progress(Progress{Kind: ProgressPlan, Name: final.Plan.Goal})
		return e.startClassifiedPlan(ctx, final.Plan, scope, authorize, cb)
	}
	if lastTask != nil {
		lastTask.Answer = final.Answer
		return budgetAdvisory(finalizeLastTask(lastTask)), nil
	}
	return budgetAdvisory(&Result{Kind: "answer", Answer: final.Answer}), nil
}

// WorkPath returns the configured work directory — the project workspace
// panel sessions execute in. It lets the panel pin non-repo sessions to the
// work path so the memory wall (§17.2) holds for them too.
func (e *Engine) WorkPath() string {
	e.cfgMu.RLock()
	defer e.cfgMu.RUnlock()
	return e.cfg.Storage.WorkPath
}

// CardPath returns the capabilities.yaml the engine's scheduler was built
// from — the file /card edits and reloads.
func (e *Engine) CardPath() string { return e.cardPathNow() }

// ReloadCard hot-swaps the scheduler's capability card (the /card edit path):
// the core re-reads the file, rebuilds its router, re-registers, and tells
// connected peers. The engine's own cardPath is what the system_status tool
// reports, so it moves too. Errors when the engine runs cardless — there is
// nothing to reload.
func (e *Engine) ReloadCard(path string) error {
	e.schedMu.Lock()
	defer e.schedMu.Unlock()
	sched := e.sched.Load()
	if sched == nil {
		return e.initSchedulerLocked(path)
	}
	if err := sched.ReloadCard(e.schedCtx, path); err != nil {
		return err
	}
	e.setCardPath(path)
	return nil
}

// DialPeer dials addr on the engine's scheduler right now — /nodes add's
// live-connect path, so a peer freshly appended to the config joins this
// session without a restart. The connection registers itself (hello exchange)
// on success; the caller decides whether to wait or dial in the background.
func (e *Engine) DialPeer(ctx context.Context, addr string) error {
	sched := e.sched.Load()
	if sched == nil {
		return fmt.Errorf("askengine: no scheduler (card missing or scheduler initialization failed)")
	}
	return sched.DialPeer(ctx, addr)
}

// queueConsumerAlive reports whether the queue rows this engine's store
// accumulates have any process that will drain them. Three cases:
//
//   - this engine owns the node row and runs the queue scheduler → it is the
//     consumer itself.
//   - a borrowed engine (daemon holds the identity lock) → a consumer exists
//     only when the owner heartbeats into THIS store AND a consumer process is
//     actually up. The process probes alone cannot tell a daemon sharing this
//     db from one draining another db_path — the row heartbeat is the only
//     store-bound signal.
//   - no scheduler at all → only a heartbeat written into THIS store proves a
//     consumer. The process probes cannot: a panel's own listening port would
//     count as a consumer even though this engine has no queue scheduler, and
//     a daemon draining another db_path would count too.
func (e *Engine) queueConsumerAlive(ctx context.Context) bool {
	sched := e.sched.Load()
	if sched == nil {
		return core.StoreRowHeartbeating(ctx, e.db, e.stableNodeID())
	}
	if e.queueTasks && sched.OwnsNodeRow() {
		return true // this engine is the consumer
	}
	// Every other engine — a borrowed queue surface or a one-shot `task
	// add` — submits rows only, and only a process heartbeating THIS store
	// can ever run them. The store probe sees through the "daemon is alive
	// but drains another db_path" split the lock/port checks cannot;
	// lastForeign/selfWriteAt keep this engine's own Register stamp from
	// reading as that heartbeat.
	fresh := core.StoreConsumerFresh(ctx, e.db, sched.NodeID(), e.selfRowForeign.Load(), e.selfRowWriteAt.Load())
	e.logger.Debug("queue consumer probe", "node", sched.NodeID(), "fresh", fresh,
		"lastForeign", e.selfRowForeign.Load(), "selfWriteAt", e.selfRowWriteAt.Load(),
		"cur", core.NodeRowLastSeen(ctx, e.db, sched.NodeID()), "ownsRow", sched.OwnsNodeRow(), "queueTasks", e.queueTasks)
	if !fresh {
		return false
	}
	e.cfgMu.RLock()
	cfg := e.cfg
	e.cfgMu.RUnlock()
	return core.QueueConsumerAlive(cfg)
}

// EnqueueTask routes a directly-created task (the panel's board "new task"
// form, `panda task add`) through the async queue: it lands in queued and the
// scheduler starts it when resources allow. Needs a capability card, like
// task submission. The warn result carries a localized advisory when the task
// was accepted but this store has no live queue consumer — the row is durable
// and runs once a consumer starts, but a silent "queued" that never moves is
// the worst possible failure mode to surface as success.
//
// The same tier-2 verdict the ask loop resolves applies here: the task's own
// project supplies the effective mode, a remembered project approve supplies
// consent without a prompt, a remembered deny refuses before the row exists,
// "always" still parks every tier-2 task for a foreground decision. Session
// remembers cannot apply — the linked session does not exist yet (the board
// creates it after a successful enqueue).
func (e *Engine) EnqueueTask(ctx context.Context, in core.TaskInput, q core.QueueSpec) (core.Task, string, error) {
	sched := e.sched.Load()
	if sched == nil {
		return core.Task{}, "", fmt.Errorf("task creation requires a capability card (scheduler initialization failed)")
	}
	verdict := e.approvalFor("", in.Project)
	authorized, denied := verdict.gate(in.Authorized)
	if denied {
		return core.Task{}, "", fmt.Errorf("%w (scope: %s)", ErrApprovalDenied, verdict.decisionScope)
	}
	in.Authorized = authorized
	task, err := sched.Enqueue(ctx, in, q)
	if err != nil {
		return task, "", err
	}
	// Dead-letter guard: a queued row only runs if some process drains THIS
	// store. Borrowed engines never consume; when no owner heartbeats this
	// store the task parks forever — return the row (durable, runs the moment
	// a consumer comes up) plus an advisory instead of silent "queued".
	if !e.queueConsumerAlive(ctx) {
		return task, i18n.T(e.Locale(), "cli.queue.noConsumer"), nil
	}
	return task, "", nil
}

// StartPlan hands a multi-stage plan to the scheduler core, which creates one
// task per stage and releases the ones with no dependencies. It is the plan
// plane's only entry point outside the daemon's own completion hooks: without it
// the flagship cross-device pipeline — develop where a coding agent lives, train
// where the GPU lives, report where the user is — was reachable only from a test.
// Needs a capability card for the same reason task submission does: a plan whose
// stages cannot be routed is a plan that cannot start.
func (e *Engine) StartPlan(ctx context.Context, p plan.Plan, q core.QueueSpec) (string, error) {
	sched := e.sched.Load()
	if sched == nil {
		return "", fmt.Errorf("starting a plan requires a capability card (scheduler initialization failed)")
	}
	// The same tier-2 verdict the enqueue path resolves applies to every
	// stage: a plan submitted under approval.mode "never" (or with a
	// remembered project approval) must carry the consent its stages'
	// remote runs need. Without this, stages kept authorized=false and a
	// delegated stage on a node whose agents have no restricted face (e.g.
	// opencode) refused with a tier-2 authorization error — even though
	// the operator had configured the fleet never to ask.
	verdict := e.approvalFor("", "")
	authorized, denied := verdict.gate(false)
	if denied {
		return "", fmt.Errorf("%w (scope: %s)", ErrApprovalDenied, verdict.decisionScope)
	}
	q.Authorized = authorized
	return sched.StartPlan(ctx, p, q)
}

// PlanStages returns every stage of one plan, for following a run.
func (e *Engine) PlanStages(ctx context.Context, planID string) ([]core.Task, error) {
	sched := e.sched.Load()
	if sched == nil {
		return nil, fmt.Errorf("reading a plan requires a capability card (scheduler initialization failed)")
	}
	return sched.TaskStore().PlanStages(ctx, planID)
}

// startClassifiedPlan turns a model-emitted plan into a running pipeline. It is
// the other half of the plan entry point: `panda plan run` covers the pipeline
// you keep as a file, this covers the one you ask for in a sentence — which is
// the case the project exists for, since the point of a plan is not having to
// visit three machines yourself.
//
// Ordinary asks retain per-stage consent. The authenticated hardware entry can
// additionally carry explicit owner consent for this request's whole plan;
// it still passes through the existing approval policy before rows are created.
func (e *Engine) startClassifiedPlan(ctx context.Context, spec *entry.PlanSpec, scope AskScope, authorize bool, cb StreamCallbacks) (*Result, error) {
	sched := e.sched.Load()
	if sched == nil {
		return nil, fmt.Errorf("plan output requires a capability card (scheduler initialization failed)")
	}
	p, err := plan.FromSpec(*spec)
	if err != nil {
		// A plan the model got wrong has created nothing, so the useful answer is
		// the defect itself rather than a failed run: the user (or the next turn)
		// can see that the stages did not hang together.
		return &Result{Kind: "answer", Answer: i18n.Tf(e.Locale(), "ask.plan.invalid", "err", err.Error())}, nil
	}
	q := core.DefaultQueueSpec()
	if hardwarePlanConsent(ctx) && authorize {
		project, _ := e.scopeProject(scope)
		authorized, denied := e.approvalFor(scope.SessionID, project).gate(authorize)
		if denied {
			return &Result{Kind: "plan", PlanGoal: p.Goal, Denied: true, Stderr: ErrApprovalDenied.Error(), ExitCode: 1}, nil
		}
		q.Authorized = authorized
	}
	// No work dir, for the same reason `panda plan run` sets none: a path on this
	// machine means nothing on the machine that runs the stage.
	q.WorkDir = ""
	// The stages belong to the conversation that asked for the plan. Without
	// the link a plan's stages settled into silence: the thread got the receipt
	// ("3 stages started") and never heard which employee finished what, while
	// the panel's session finalizer — which reports a settled task back into
	// its thread — had nothing to match on.
	q.SessionID = scope.SessionID
	planID, err := sched.StartPlan(ctx, p, q)
	if err != nil {
		return &Result{Kind: "plan", PlanID: planID, PlanGoal: p.Goal, Stderr: err.Error(), ExitCode: 1}, nil
	}
	stages, serr := sched.TaskStore().PlanStages(ctx, planID)
	if serr != nil {
		e.logger.Warn("askengine: read plan stages", "plan", planID, "err", serr)
	}
	res := &Result{Kind: "plan", PlanID: planID, PlanGoal: p.Goal, PlanStages: stages, OK: true}
	// A plan is queue work: with no consumer anywhere the stages park forever
	// and the "plan started" receipt reads like a handoff that never happened.
	// This engine consuming (queueTasks AND owning the node row) counts; so
	// does a live daemon or web console heartbeating into THIS store — a
	// process draining a different db_path would leave the stages parked and
	// must not read as a consumer.
	consumer := e.queueConsumerAlive(ctx)
	if !consumer {
		res.Warning = i18n.T(e.Locale(), "cli.queue.noConsumer")
	}
	// "Plan started" is a receipt, not an outcome. Like a queued task the
	// pipeline is followed until every stage settles (or one parks for a
	// human): the round keeps streaming stage progress and closes on the real
	// verdict instead of going quiet at dispatch. Receipt-mode surfaces (the
	// panel) keep the immediate receipt; an awaitQueued turn follows exactly
	// like an inline one. With no consumer anywhere the wait could never
	// move, so the warning above stands alone.
	if (!e.queueTasks || e.awaitQueued) && consumer {
		if board := e.awaitPlanSettled(ctx, sched, planID, cb); len(board) > 0 {
			res.PlanStages = board
			e.planBoardVerdict(ctx, sched.TaskStore(), res, board)
		}
	}
	// The per-stage classify_result events are traced inside core.StartPlan at
	// stage creation, before AdvancePlan releases anything — leading each
	// stage's own execution events in the orbit timeline.
	return res, nil
}

// submitTask executes a classified task spec through the scheduler core and
// maps the outcome to a Result. In queue mode the task is Enqueued and the
// call returns immediately (TaskState "queued"); the queue scheduler starts
// it when resources allow and the session streams its progress. In inline
// mode a per-task WorkDir is persisted before routing and execution, avoiding
// process-wide scheduler directory swaps between concurrent asks.
func (e *Engine) submitTask(ctx context.Context, spec *entry.TaskSpec, prompt string, authorized bool, scope AskScope, reasoning string, cb StreamCallbacks, awaitQueued bool, loc ...i18n.Locale) *Result {
	targetLoc, explicit := e.localeNow()
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	} else if !explicit && !e.replyASCII && (containsHan(prompt) || containsHan(spec.Title)) {
		targetLoc = i18n.ChineseSimp
	}
	// The composer's explicit node pick fills spec.node only when the model
	// left routing to the scheduler — a model-emitted pin always wins.
	if scope.Node != "" && spec.Spec.Node == "" {
		spec.Spec.Node = scope.Node
	}
	// The thread's roster rides the spec, not this call: spec_json is what
	// persists on the row and what a delegated task carries to its executor,
	// so the restriction is enforced wherever the work actually runs. An
	// explicit roster always wins over the model's own requirements — the
	// user said which hands may touch this conversation.
	if len(scope.Agents) > 0 {
		spec.Spec.AllowAgents = append([]string(nil), scope.Agents...)
	}
	// The conversation's permission mode rides the spec for the same reason
	// the roster does: the executor (a peer included) must apply the policy
	// the user picked, not the one this process happens to default to. Auto
	// is left off so an untouched thread's spec stays byte-identical to the
	// pre-mode shape.
	if perm := defense.NormalizeMode(scope.PermissionMode); perm != defense.ModeAuto {
		spec.Spec.PermissionMode = perm
	}
	in := toTaskInput(spec, targetLoc)
	workDir := scope.WorkDir
	// Explicit per-request scope wins. Existing CLI callers that omit a project
	// retain the entered ambient project as a compatibility default.
	project, projectDir := e.scopeProject(scope)
	if project != "" {
		if in.Project == "" {
			in.Project = project
		}
		if workDir == "" && projectDir != "" {
			workDir = projectDir
		}
	}
	// Canonicalize the name against the registry regardless of who supplied
	// it: the classifier emits spec.project in the casing it inferred from
	// the tree ("Needlen" for a "needlen" row), and an unnormalized variant
	// misses this project's approval policy, lands a different lock key, and
	// — when the scope carried no directory — leaves workDir on the caller's
	// cwd so the agent reads one tree while its footprint is judged on
	// another (observed live: a Needlen-scoped ask ran in the panda repo and
	// tripped scope-drift on files the agent never touched).
	if in.Project != "" && e.projStore != nil {
		if p, perr := e.projStore.GetFold(in.Project); perr == nil {
			in.Project = p.Name
			// UsableWorkDir, not WorkDir: a row that names the home directory
			// (created by home adoption before the breadth rule existed) would
			// otherwise hand every ask a work dir the executor refuses, and the
			// task fails at creation on a rule the user never invoked.
			if workDir == "" {
				workDir = p.UsableWorkDir()
			}
		}
	}
	// Last resort: the process's own directory. It answers to the same breadth
	// rule a pin does — `panda ask` run from $HOME has no project and no pin,
	// and taking $HOME as the work dir failed the task at creation with "work
	// dir is too broad" instead of running it in its own private directory the
	// way any other anonymous task does. A refused cwd leaves workDir empty.
	if workDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			if projectstore.ValidateWorkDir(cwd) == nil {
				workDir = cwd
			}
		}
	}
	if in.RepoPath == "" && workDir != "" {
		in.RepoPath = workDir
	}
	// A model may only select acceptance checks the workspace actually
	// declares. It sees the shortlist in its prompt, but a prompt is not an
	// enforcement point: an invented command would run under the
	// orchestrator's own authority, so anything outside the enumeration is
	// dropped before the row exists.
	in.SpecJSON = restrictAcceptanceToProject(in.SpecJSON, workDir)
	in.SpecJSON = appendUserAcceptance(in.SpecJSON, scope.Acceptance)
	// Approval mode is the real tier-2 gate (design §16), resolved against the
	// task's own project: a project approval_mode overrides the global mode;
	// a remembered session/project decision answers before any prompt; an
	// explicit session grant (/authorize on, --authorize) is the standing
	// consent on-request honors; "always" withholds regardless so every tier-2
	// task is decided per-task in the foreground.
	verdict := e.approvalFor(scope.SessionID, in.Project).withPermission(scope.PermissionMode)
	authorized, denied := verdict.gate(authorized)
	consentSrc := ""
	if authorized && verdict.decisionScope != "" && verdict.decision == projectstore.DecisionApprove {
		consentSrc = verdict.decisionScope
	}
	if denied {
		// A remembered "deny" answers the gate before a task row exists:
		// nothing is created, nothing runs, and the model loop sees the
		// standing refusal through the denied Result (or the task_submit
		// tool's refusal string) instead of re-prompting forever.
		return &Result{
			Kind:      "task",
			Thought:   reasoning,
			TaskTitle: in.Title,
			TaskState: "denied",
			Denied:    true,
			OK:        false,
			ExitCode:  1,
			Stderr:    i18n.Tf(targetLoc, "repl.approval.autoDenied", "scope", scopeLabel(verdict.decisionScope)),
		}
	}
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
	// Snapshot the scheduler once: the pointer is only ever stored non-nil in
	// production (lazy init or reload), but a consistent handle keeps the
	// atomic load honest across this whole submission.
	sched := e.sched.Load()
	if sched == nil {
		return &Result{Kind: "task", TaskState: "failed", Stderr: "task submission requires a capability card (scheduler initialization failed)", ExitCode: 1}
	}
	var task core.Task
	var result bus.TaskResultPayload
	if e.queueTasks {
		q := core.DefaultQueueSpec()
		q.WorkDir = workDir           // travels per task; "" falls back to the core's work dir
		q.SessionID = scope.SessionID // links the row to its conversation for followup
		var err error
		task, err = sched.Enqueue(ctx, in, q)
		if err != nil {
			return &Result{Kind: "task", TaskState: "failed", Stderr: err.Error(), ExitCode: 1}
		}
		if cb.OnTaskRow != nil {
			cb.OnTaskRow(task.TaskID)
		}
		if !awaitQueued {
			// Receipt mode (the panel's board): the queued pointer IS the
			// result — the session streams the row's progress and the
			// finalizer folds its verdict, so nothing here waits on settle.
			// Dead-letter guard: when this engine does not consume and no
			// owner heartbeats this store, the receipt is permanent parked
			// work, not dispatch — attach the advisory so the surface can say
			// so instead of showing a confident "queued".
			warn := ""
			if !e.queueConsumerAlive(ctx) {
				warn = i18n.T(targetLoc, "cli.queue.noConsumer")
			}
			if reasoning != "" {
				sched.EvTrace(ctx, task.TaskID, core.EvReasoning, map[string]any{"thought": reasoning})
			}
			if consentSrc != "" {
				sched.EvTrace(ctx, task.TaskID, "approval_auto", map[string]any{"scope": consentSrc})
			}
			return &Result{Kind: "task", TaskID: task.TaskID, TaskTitle: task.Title, TaskState: task.State, Thought: reasoning, ConsentSource: consentSrc, Warning: warn}
		}
		// Await mode: the queue (not this turn) owns execution — a scheduler
		// in the daemon or this own process claims the row when its resource
		// keys are free. Follow the row to settle and bridge the consumer's
		// persisted timeline into progress, so the turn looks exactly like an
		// inline run while the work survives this process dying.
		result = bus.TaskResultPayload{TaskID: task.TaskID, AttemptID: task.AttemptID, State: task.State}
		task, result = e.awaitSettled(ctx, sched, task, result, cb)
	} else {
		// Bridge the core's lifecycle trace events to the caller's progress
		// feed for the duration of this synchronous run, so a blocking agent
		// execution shows routing → executing → judging instead of a frozen
		// spinner. AddOnEvent allows concurrent subagent runs without
		// serializing submissions. A caller with no progress sink installs
		// nothing.
		if cb.OnProgress != nil || cb.OnStatus != nil {
			store := sched.TaskStore()
			unsub := store.AddOnEvent(func(_, typ string, data any) {
				if p, ok := progressForEvent(typ, data); ok {
					cb.progress(p)
				}
			})
			defer unsub()
		}
		var err error
		// Submit is synchronous, so the row id is otherwise unknowable until
		// it returns. The observer fires inside createTask — the one moment
		// the id exists while the work has not started yet.
		submitCtx := ctx
		if cb.OnTaskRow != nil {
			var once sync.Once
			submitCtx = core.WithTaskObserver(ctx, func(t core.Task) {
				once.Do(func() { cb.OnTaskRow(t.TaskID) })
			})
		}
		task, result, err = sched.Submit(submitCtx, in)
		if err != nil {
			return &Result{Kind: "task", TaskState: "failed", Stderr: err.Error(), ExitCode: 1}
		}
		// A parked task is not an outcome. When the queue holds the task — the
		// pinned target's link is not live yet — Submit returns the receipt
		// immediately, and converging the round on "queued" would end the session
		// while the work has not started. Follow the row instead: the queue
		// consumer claims it when the link comes up, and this round reports the
		// outcome it actually reached.
		if !settledState(task.State) {
			task, result = e.awaitSettled(ctx, sched, task, result, cb)
		}
	}
	if reasoning != "" {
		sched.EvTrace(ctx, task.TaskID, core.EvReasoning, map[string]any{"thought": reasoning})
	}
	res := &Result{
		Kind:         "task",
		Thought:      reasoning,
		TaskID:       task.TaskID,
		TaskTitle:    task.Title,
		TaskState:    task.State,
		OK:           result.OK,
		Stdout:       result.Stdout,
		Stderr:       result.Stderr,
		ExitCode:     result.ExitCode,
		Agent:        result.Agent,
		Model:        result.Model,
		Injected:     result.Injected,
		Executor:     result.Executor,
		WorkDir:      result.WorkDir,
		Verified:     result.Verified,
		Verification: result.Verification,
	}
	res.Question = result.Question
	res.FilesChanged = result.FilesChanged
	res.RouteFallback = routeFallbackPeer(ctx, sched.TaskStore(), task.TaskID)
	res.ConsentSource = consentSrc
	if consentSrc != "" {
		sched.EvTrace(ctx, task.TaskID, "approval_auto", map[string]any{"scope": consentSrc})
	}
	// Inline approval closure: a tier-2 task with no standing consent parks in
	// review with an authorization-refusal reason. Turn that dead end into a
	// decision — consult the caller's OnApproval, and on a yes re-run the same
	// task authorized in one round-trip (agent scheduling completes without any
	// background scheduler). A caller with no synchronous callback gets a
	// NeedsApproval Result to handle on its own event loop.
	if !authorized && task.State == core.StateReview && commander.IsAuthorizationRefusal(result.Stderr) {
		req := ApprovalRequest{TaskID: task.TaskID, Title: task.Title, Intent: in.Intent, Reason: result.Stderr, Project: in.Project, Scope: verdict.scope}
		if cb.OnApproval == nil {
			res.NeedsApproval = true
			res.Approval = &req
			return res
		}
		if cb.OnApproval(req) {
			resumed := e.resumeLocked(ctx, req.TaskID, "")
			sumClient, _ := e.healthyClient()
			if report, rerr := entry.SummarizeResult(ctx, sumClient, resumed.TaskTitle, in.Intent, resumed.OK, resumed.ExitCode, resumed.Stdout, resumed.Stderr, targetLoc); rerr == nil {
				resumed.Report = report
				e.noteDailyObservation(report)
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
	case "delegate_request":
		// The orchestrator's fan-out: one per PANDA_DELEGATE request, named
		// after what the child is for (title → capabilities → pinned node →
		// its task id) so a parallel spawn reads as N labelled sub-agents.
		name := eventField(data, "title")
		if name == "" {
			name = strings.Join(eventFieldList(data, "requires"), ", ")
		}
		if name == "" {
			name = eventField(data, "node")
		}
		if name == "" {
			name = eventField(data, "child")
		}
		// The mode descriptor: how the child relates to the shared tree.
		detail := ""
		if boolField(data, "isolate") {
			detail = "isolated"
		} else if paths := eventFieldList(data, "paths"); len(paths) > 0 {
			detail = strings.Join(paths, ", ")
		}
		return Progress{Kind: ProgressSpawn, Name: name, Detail: detail}, true
	case core.EvProgress, core.EvSubagentEvent:
		note := eventField(data, "note")
		if note != "" {
			return Progress{Kind: ProgressTool, Name: note}, true
		}
	case core.EvAgentEvent:
		// The delegated agent's own activity, streamed live. Without this the
		// only thing a caller sees between "running <agent>" and the final
		// verdict is the watchdog's one-line "no output for Ns" — the run is
		// working for twenty minutes and the screen looks frozen. Tool calls
		// reuse ProgressTool so every existing renderer already knows how to
		// phrase them; assistant prose gets its own kind because it is text,
		// not a milestone.
		switch eventField(data, "ev") {
		case "tool_use":
			label := agentToolLabel(eventField(data, "name"), eventInput(data))
			if label == "" {
				return Progress{}, false
			}
			return Progress{Kind: ProgressTool, Name: label}, true
		case "text":
			text := strings.TrimSpace(eventField(data, "text"))
			if text == "" {
				return Progress{}, false
			}
			return Progress{Kind: ProgressAgentText, Detail: text}, true
		case "tool_result":
			if !boolField(data, "is_error") {
				return Progress{}, false
			}
			text := strings.TrimSpace(eventField(data, "content"))
			if text == "" {
				return Progress{}, false
			}
			return Progress{Kind: ProgressAgentText, Detail: text, Name: "error"}, true
		}
		return Progress{}, false
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

// eventInput returns the tool_use input object recorded on a structured
// agent_event, for either shape the store hands back (a map before marshaling,
// JSON bytes after).
func eventInput(data any) map[string]any {
	switch v := data.(type) {
	case map[string]any:
		if m, ok := v["input"].(map[string]any); ok {
			return m
		}
	case []byte:
		var m map[string]any
		if json.Unmarshal(v, &m) == nil {
			if in, ok := m["input"].(map[string]any); ok {
				return in
			}
		}
	}
	return nil
}

// agentToolLabel renders one delegated tool call as a single line: the tool's
// name plus whichever input field says what it acts on, in the order that
// reads best in a terminal. Empty when there is nothing to show.
func agentToolLabel(name string, input map[string]any) string {
	name = strings.TrimSpace(name)
	detail := ""
	for _, key := range []string{"description", "command", "file_path", "path", "pattern", "query", "url", "prompt"} {
		if v, ok := input[key].(string); ok && strings.TrimSpace(v) != "" {
			detail = strings.TrimSpace(v)
			break
		}
	}
	if detail == "" && len(input) > 0 {
		if blob, err := json.Marshal(input); err == nil {
			detail = string(blob)
		}
	}
	detail = truncateRunes(strings.Join(strings.Fields(detail), " "), 120)
	switch {
	case name != "" && detail != "":
		return name + ": " + detail
	case name != "":
		return name
	default:
		return detail
	}
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

// boolField reads a map field that holds a boolean — the isolate flag of a
// delegate_request survives both the pre-marshal bool and the post-JSON-round-
// trip forms (bool, or a "true"/"false" string).
func boolField(data any, key string) bool {
	var raw any
	switch v := data.(type) {
	case map[string]any:
		raw = v[key]
	case []byte:
		var m map[string]any
		if json.Unmarshal(v, &m) == nil {
			raw = m[key]
		}
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}

// eventFieldList reads a map field that holds a list of strings — the
// requires array of a delegate_request arrives as []string pre-marshal and as
// []any after a state-transition JSON round trip.
func eventFieldList(data any, key string) []string {
	var raw any
	switch v := data.(type) {
	case map[string]any:
		raw = v[key]
	case []byte:
		var m map[string]any
		if json.Unmarshal(v, &m) == nil {
			raw = m[key]
		}
	}
	var out []string
	switch list := raw.(type) {
	case []string:
		out = list
	case []any:
		for _, it := range list {
			out = append(out, stringField(it))
		}
	}
	return out
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
func resultFromTask(task core.Task, result bus.TaskResultPayload, routeFallback string) *Result {
	return &Result{
		Kind:          "task",
		TaskID:        task.TaskID,
		TaskTitle:     task.Title,
		TaskState:     task.State,
		OK:            result.OK,
		Stdout:        result.Stdout,
		Stderr:        result.Stderr,
		ExitCode:      result.ExitCode,
		Deferred:      result.Deferred,
		Agent:         result.Agent,
		Model:         result.Model,
		Injected:      result.Injected,
		Executor:      result.Executor,
		WorkDir:       result.WorkDir,
		RouteFallback: routeFallback,
		Question:      result.Question,
		FilesChanged:  result.FilesChanged,
	}
}

// routeFallbackPeer scans the task's audit trail for the newest
// route_fallback event — the marker the queue scheduler writes when the
// send to the routed peer failed and the task ran locally instead. ""
// means the run went exactly where routing intended.
func routeFallbackPeer(ctx context.Context, store *core.TaskStore, taskID string) string {
	events, err := store.Events(ctx, taskID)
	if err != nil {
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != core.EvRouteFallback {
			continue
		}
		var d struct {
			Intended string `json:"intended"`
		}
		if err := json.Unmarshal([]byte(events[i].DataJSON), &d); err == nil {
			return d.Intended
		}
		return ""
	}
	return ""
}

func (e *Engine) acceptReviewedWork(ctx context.Context, taskID string) *Result {
	store := e.TaskStore()
	task, err := store.Get(ctx, taskID)
	if err != nil {
		return &Result{Kind: "task", TaskID: taskID, TaskState: core.StateReview, Stderr: err.Error(), ExitCode: 1}
	}
	// Prefer the scheduler core when the engine carries one: its ResumeApproved
	// also mirrors the accept to the sibling copy parked on the other node
	// (forwardAcceptDownstream) — a store-only approve left whichever side
	// approved second's peer parked in review forever. A core-less engine
	// degrades to the plain row update; the peer's own human can still
	// approve its copy locally.
	if sched := e.sched.Load(); sched != nil {
		final, result, rerr := sched.ResumeApproved(ctx, taskID)
		if rerr != nil {
			state := core.StateReview
			if current, getErr := store.Get(context.WithoutCancel(ctx), taskID); getErr == nil {
				state = current.State
			}
			return &Result{Kind: "task", TaskID: taskID, TaskTitle: task.Title, TaskState: state, Stderr: rerr.Error(), ExitCode: 1}
		}
		return resultFromTask(final, result, routeFallbackPeer(ctx, store, taskID))
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
	return resultFromTask(final, result, routeFallbackPeer(ctx, store, taskID))
}

// resumeLocked re-runs an approved review-parked task synchronously and maps
// the outcome to a Result. The caller must hold schedMu (submitTask does), so
// any pinned session work dir is still in effect for the re-run.
func (e *Engine) resumeLocked(ctx context.Context, taskID, answer string) *Result {
	sched := e.sched.Load()
	task, result, err := sched.ResumeApproved(ctx, taskID, answer)
	if err != nil {
		state := core.StateReview
		if current, getErr := sched.TaskStore().Get(context.WithoutCancel(ctx), taskID); getErr == nil {
			state = current.State
		}
		return &Result{Kind: "task", TaskID: taskID, TaskState: state, Stderr: err.Error(), ExitCode: 1}
	}
	return resultFromTask(task, result, routeFallbackPeer(ctx, sched.TaskStore(), taskID))
}

// ContinueReviewed requeues a follow-up-parked task: the supervised round
// budget ran out mid-work (needs_followup) and the operator chose "keep
// going" over accept-half or throw-away. The call only marks the row — the
// store's queue scheduler runs the continuation, which is what lets a CLI or
// console continue work a daemon owns. The optional note travels as a
// steer-grade board note that leads the continued round's intent.
func (e *Engine) ContinueReviewed(ctx context.Context, taskID, note string, cb StreamCallbacks) *Result {
	if e == nil || e.db == nil {
		return &Result{Kind: "task", TaskID: taskID, TaskState: core.StateFailed, Stderr: "task continuation requires an initialized engine", ExitCode: 1}
	}
	sched := e.sched.Load()
	if sched == nil {
		return &Result{Kind: "task", TaskID: taskID, TaskState: core.StateReview, Stderr: "task execution requires a capability card", ExitCode: 1}
	}
	task, err := sched.ContinueReviewed(ctx, taskID, note)
	if err != nil {
		state := core.StateReview
		if current, getErr := sched.TaskStore().Get(context.WithoutCancel(ctx), taskID); getErr == nil {
			state = current.State
		}
		return &Result{Kind: "task", TaskID: taskID, TaskTitle: task.Title, TaskState: state, Stderr: err.Error(), ExitCode: 1}
	}
	res := &Result{Kind: "task", TaskID: task.TaskID, TaskTitle: task.Title, TaskState: task.State, OK: true}
	// A requeue with no live consumer is the same dead-letter the enqueue
	// path warns about — surface it identically.
	if !e.queueConsumerAlive(ctx) {
		res.Warning = i18n.T(e.Locale(), "cli.queue.noConsumer")
	}
	return res
}

// ResumeApproved accepts or re-runs a reviewed task under the caller's context.
// workDir optionally overrides the persisted task directory for an active
// originating session; progress is bridged from the same core event stream as
// initial foreground submission.
func (e *Engine) ResumeApproved(ctx context.Context, taskID, workDir string, cb StreamCallbacks, answers ...string) *Result {
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
	case core.ApprovalSelfMerge:
		// A self-merge approval IS the pipeline (worktree apply, gate, build,
		// swap) — no agent run, but it still needs the scheduler core, which
		// carries the self-evolution manager and the idle/schema gates.
		// Continue below like ResumeExecution; core.resumeApproved dispatches
		// on the disposition.
	default:
		return &Result{Kind: "task", TaskID: taskID, TaskState: core.StateReview, Stderr: "unknown approval disposition", ExitCode: 1}
	}
	sched := e.sched.Load()
	if sched == nil {
		return &Result{Kind: "task", TaskID: taskID, TaskState: core.StateReview, Stderr: "task execution requires a capability card", ExitCode: 1}
	}
	if workDir != "" {
		if err := sched.TaskStore().SetWorkDir(ctx, taskID, workDir); err != nil {
			return &Result{Kind: "task", TaskID: taskID, TaskState: "review", Stderr: err.Error(), ExitCode: 1}
		}
	}
	if cb.OnProgress != nil || cb.OnStatus != nil {
		store := sched.TaskStore()
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
	answer := ""
	if len(answers) > 0 {
		answer = answers[0]
	}
	res := e.resumeLocked(ctx, taskID, answer)
	if ctx.Err() != nil {
		return res
	}
	if res.Deferred {
		// The approval parked for delivery is itself the honest answer —
		// a model pass over it invents a stale "waiting for approval /
		// re-run the task" story the receipt already contradicts (the
		// consent IS granted; the work resumes on the next link, no
		// re-submission exists to be told about).
		res.Answer = res.Stdout
		return res
	}
	sumClient, _ := e.healthyClient()
	loc, _ := e.localeNow()
	if report, rerr := entry.SummarizeResult(ctx, sumClient, res.TaskTitle, "", res.OK, res.ExitCode, res.Stdout, res.Stderr, loc); rerr == nil {
		res.Report = report
		e.noteDailyObservation(report)
	}
	return res
}

// noteDailyObservation records an agent-authored digest into the warm-layer
// daily log as a trusted line (Daily.Append, not AppendExternal). The digest
// is the model's own summary of a task outcome — "the agent's own
// observation" in the Dreaming taint model (design §17.3) — and it is the
// only trusted writer in the system: without it every candidate carries an
// external provenance and Deep can never promote. A failed append is logged,
// never fatal to the answer path.
func (e *Engine) noteDailyObservation(text string) {
	if e.daily == nil || strings.TrimSpace(text) == "" {
		return
	}
	if err := e.daily.Append(time.Now(), text); err != nil && e.logger != nil {
		e.logger.Warn("append daily observation", "err", err)
	}
}

// recordEntryUsage bills the entry (commander) model's own token consumption
// for one ask into the delegation metrics, so the panel's tokens column
// reflects the commander's cost alongside adapter delegations. The executor
// label "entry:<model>" keeps the rows distinguishable; providers that do not
// report usage (delta zero) record nothing.
func (e *Engine) recordEntryUsage(ctx context.Context, res *Result, client *entry.Client, before entry.Usage, latency time.Duration) {
	if client == nil {
		return
	}
	delta := client.Usage().Sub(before)
	if delta.Total() == 0 {
		return
	}
	taskID := ""
	if res != nil {
		taskID = res.TaskID
	}
	cost := client.EstimateCost(delta.InputTokens, delta.OutputTokens)
	store := core.NewSigningTaskStore(e.db, e.logger)
	if err := store.RecordDelegationMetric(ctx, taskID, e.cfg.Node.Name, "entry:"+client.ModelName(),
		nil, true, latency.Milliseconds(), int(delta.Total()), cost); err != nil {
		e.logger.Warn("askengine: record entry usage", "err", err)
	}
}

// Close releases the engine's resources (DB handle, scheduler core, MCP
// server). The engine must not be used afterwards.
// goBG starts a long-lived duty tracked by e.bg, so Close can join it
// before closing the store. The guard still recovers a panic; the WaitGroup
// is what makes shutdown observe completion.
func (e *Engine) goBG(name string, fn func()) {
	e.bg.Add(1)
	guard.Go(e.logger, name, nil, func() {
		defer e.bg.Done()
		fn()
	})
}

func (e *Engine) Close() {
	e.schedMu.Lock()
	cancel := e.schedCancel
	e.schedMu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Wait for in-flight queue runners BEFORE the core's shutdown and the
	// db close: cancelling schedCtx stops dispatch, but a runner already
	// inside Run keeps going until it finishes, and its writes (memory,
	// artifacts, events) would otherwise land on a closed store — or after
	// a test's temp dir was removed. Bounded, so a wedged adapter cannot
	// hold shutdown open.
	if sched := e.sched.Load(); sched != nil {
		wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
		sched.WaitQueue(wctx)
		wcancel()
	}
	// schedCtx cancellation only stops the scheduler's goroutines; peer
	// conns survive until their read deadline unless Shutdown closes them —
	// and the node stays "online" in the ledger. Do it before db.Close so
	// the offline marking can still reach SQLite.
	if sched := e.sched.Load(); sched != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		sched.Shutdown(ctx)
		cancel()
	}
	e.regMu.RLock()
	mcp := e.mcp
	e.regMu.RUnlock()
	if mcp != nil {
		mcp.Close()
	}
	// Join the long-lived duties (heartbeat, monitor, reconcile, discovery,
	// ws, peer dials) before the store closes: their writes would otherwise
	// land on a closed DB — or recreate a WAL file in a directory the caller
	// is already tearing down. Bounded like the queue drain above.
	{
		done := make(chan struct{})
		go func() {
			e.bg.Wait()
			close(done)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		select {
		case <-done:
		case <-ctx.Done():
			e.logger.Warn("engine duties still running at shutdown deadline", "err", ctx.Err())
		}
		cancel()
	}
	if e.db != nil {
		e.db.Close()
	}
}
