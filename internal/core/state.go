// SPDX-License-Identifier: AGPL-3.0-or-later

// Package core hosts the node lifecycle and the task state machine.
package core

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// State-machine errors. Callers should wrap them with %w to preserve
// errors.Is checks.
var (
	// ErrIllegal reports a transition not present in the state table.
	ErrIllegal = errors.New("illegal state transition")
	// ErrConflict reports a state/owner/version mismatch (stale write).
	ErrConflict = errors.New("state conflict")
	// ErrCancelled reports that execution finished after the task was
	// cancelled; callers should not report a result for a cancelled task.
	ErrCancelled = errors.New("task cancelled")
	// ErrAlreadyRunning reports that run() found the task already executing
	// under another goroutine (a duplicated resume/context_ack raced in). It
	// wraps ErrCancelled so every caller treats it as a quiet non-error: the
	// first runner owns execution and reports the outcome, and this
	// invocation must neither re-execute nor disturb the live row.
	ErrAlreadyRunning = fmt.Errorf("%w: already running on this node", ErrCancelled)
	// ErrApprovalNeedsChangedInput reports that a reviewed task cannot be
	// approved as-is. Its input, scope, or context must change before a new
	// run. The message doubles as user-facing copy, so it names the action.
	ErrApprovalNeedsChangedInput = errors.New("approval requires changed input — resubmit the task with a corrected prompt instead")
	ErrApprovalNeedsFollowup     = fmt.Errorf("%w: work is incomplete — use panda task continue to finish it, or reject it", ErrIllegal)
)

// Task states. These strings are part of the wire protocol across nodes,
// so treat them as stable identifiers.
const (
	StateSubmitted  = "submitted"
	StateQueued     = "queued"
	StateDispatched = "dispatched"
	StateWaitingCtx = "waiting_context"
	StateRunning    = "running"
	StateReview     = "review"
	StateDone       = "done"
	StateFailed     = "failed"
	StateCancelled  = "cancelled"
	StateExpired    = "expired"
)

// TaskStates lists every state a task row can hold, in lifecycle order. The
// constants above are the vocabulary; this is that vocabulary as data, for the
// callers that have to check a state they did not produce — `panda queue
// --state <s>` validating a typed filter is the reason it exists. Keeping it
// next to the constants is what keeps the two from drifting.
func TaskStates() []string {
	return []string{
		StateSubmitted, StateQueued, StateDispatched, StateWaitingCtx,
		StateRunning, StateReview, StateDone, StateFailed,
		StateCancelled, StateExpired,
	}
}

// IsTaskState reports whether s names a task state.
func IsTaskState(s string) bool {
	for _, st := range TaskStates() {
		if st == s {
			return true
		}
	}
	return false
}

// Task event types recorded in task_events.
const (
	EvSubmit   = "submit"
	EvQueue    = "queue"
	EvDelegate = "delegate"
	EvAccept   = "accept"
	EvDecline  = "decline"
	EvProgress = "progress"
	EvResult   = "result"
	EvReview   = "review"
	EvRetry    = "retry"
	EvTransfer = "transfer"
	EvCancel   = "cancel"
	EvExpire   = "expire"

	// EvModelInjection records that panda injected its model endpoint into
	// the agent subprocess (injection visibility, A1): the Web task detail
	// replays it like any other lifecycle event.
	EvModelInjection = "model_injection"
	// EvAgentFallback records that the scored primary agent was unavailable
	// and execution fell back to a runner-up (A2 fallback chain).
	EvAgentFallback = "agent_fallback"
	// EvAgentSwitch records the same swap the moment it happens inside
	// execAgent — exec_agent_start names the plan's primary, and without a
	// live correction the task card attributes a long fallback run to an
	// agent that never launched.
	EvAgentSwitch = "agent_switch"
	// EvMemoryPromotion records that the Dreaming engine promoted an entry
	// into MEMORY.md (A4 visibility): the console shows what was memorized
	// — and whether it came through the repeated-emphasis whitelist channel —
	// so the user can correct or delete it via the memory API.
	EvMemoryPromotion = "memory_promotion"

	// EvSupervise records one round of the execute → judge → re-delegate loop:
	// whether the reviewing model judged an agent's result complete ("done") or
	// in need of more work ("continue"), plus the follow-up instruction on a
	// "continue". The Web task detail replays it so the user can see the
	// superior's reasoning behind a re-delegation or a review parking.
	EvSupervise = "supervise"

	// EvRecoveryAssessment records what the supervisor intends to do about a
	// failed attempt (retry, session resume, human review, stop). It is written
	// after the failed result and before the retry decision, so it is the only
	// event that says whether a "failed" row will move again — the row alone
	// cannot tell a task that is about to be retried from one that is finished.
	EvRecoveryAssessment = "recovery_assessment"

	// EvRecover records that a daemon restart normalized a task left in an
	// active state by the previous process instance: running/waiting_context
	// become failed (their execution is gone), dispatched/submitted return to
	// queued. Written per task so the audit chain explains a state change that
	// no user or peer requested. review is never recovered — see
	// TaskStore.Recover.
	EvRecover = "recover"

	// EvTrustPolicy records the trust-zone accept policy's verdict for an
	// inbound delegate: the tier judged, the policy_llm setting, and the
	// classifier's answer when one ran. Shadow mode exists so the operator can
	// diff model verdicts against rule outcomes before enforcing them.
	EvTrustPolicy = "trust_policy"

	// EvContextDegraded records that a task's context (e.g. project memory pack,
	// work tree artifact) failed to attach or land, running in a degraded state
	// instead of failing silently.
	EvContextDegraded = "context_degraded"

	// EvRouteFallback records that routing chose a peer but the task ran
	// locally anyway — the link to the intended executor failed at send
	// time. Without this event the timeline only shows the corrective
	// retarget to self, and "meant for the Mac, ran here" is invisible.
	EvRouteFallback = "route_fallback"

	// EvContractSet records the acceptance contract a task will be judged
	// against. It is written once before execution so the timeline can answer
	// "what did this run promise?" even after the spec is amended.
	EvContractSet = "contract_set"
	// EvCheckResult records one acceptance check's outcome, per phase. The
	// baseline phase runs before the agent touches the tree; the final phase
	// runs after it claims done. The pair is what separates "was already
	// broken" from "this run broke it".
	EvCheckResult = "check_result"
	// EvVerdict records the contract's combined judgment (verified / failed /
	// partial) and the checks that drove it.
	EvVerdict = "verdict"
)

// Task priority levels for the panel queue (smaller runs first). The DB
// column defaults to PriorityNormal so pre-redesign rows keep FIFO behavior.
const (
	PriorityHigh   = 0
	PriorityNormal = 1
	PriorityLow    = 2
)

// Task is the persisted task row (Phase 0 subset of the schema).
type Task struct {
	TaskID                string
	ParentID              string
	Project               string
	Title                 string
	State                 string
	OwnerNode             string
	AttemptID             string
	DeliveryAcceptAttempt string // human accepted; executor delivery remains pending
	StateVersion          int
	Chain                 []string
	Intent                string
	SpecJSON              string
	ResultJSON            string
	// Verification is the contract verdict: "verified", "failed", "partial",
	// "skipped", or "" when the task carried no acceptance checks. It is a
	// property of the outcome, not a state: a task can be done and unverified
	// (the user forced it) and the two must stay distinguishable.
	Verification string
	ContextType  string
	ContextHash  string
	Complexity   float64
	Risk         string
	ResourceJSON string
	// ApprovalDisposition is persisted whenever the task enters review. Empty is
	// reserved for legacy rows and is classified conservatively from their audit
	// trail; new rows use one of the stable Approval* protocol strings.
	ApprovalDisposition ApprovalDisposition
	// OperationDecisionJSON carries the concrete pre-operation decision/evidence
	// that caused a review. V17 reserves the durable field; the typed arbitration
	// layer populates it when an adapter can report the operation before execution.
	OperationDecisionJSON string
	// Authorized records whether the user consented to executing tier-2
	// (irreversible) commands. It is server-side state (design §16 / P0-1), not
	// wire-carried, so a delegated task cannot forge authorization.
	Authorized bool
	// AuthSig/AuthPub/AuthTs persist the origin node's Ed25519 consent grant
	// alongside the flag, so a relay re-dispatching this task re-emits the
	// signed consent instead of degrading it to the bare flag (P2-8).
	AuthSig string
	AuthPub string
	AuthTs  int64
	// Requires is the capability set the task was routed with. Persisted so a
	// decline can be re-routed to the next-best node without the original wire
	// payload (P1-5).
	Requires     []string
	LeaseExpires int64
	CreatedAt    int64
	UpdatedAt    int64

	// Queue-scheduling metadata (panel queue redesign). Priority/Seq order the
	// board and the local scheduler (seq>0 = user-dragged, wins over priority);
	// SessionID links the task to its panel conversation; ResourceKeys declare
	// the resources the task occupies for conflict detection; WorkDir pins
	// execution to a session worktree; Scheduled marks tasks owned by the local
	// queue scheduler (never the delegation re-routing path).
	Priority     int
	Seq          int64
	SessionID    string
	ResourceKeys []string
	WorkDir      string
	// ExecWorkDir is the workspace the run ACTUALLY used, persisted when the
	// run starts (SetExecWorkDir). WorkDir above is a submitter pin and is
	// empty for ordinary tasks; this one answers "where did it run?" on the
	// queue board, `task show`, and the panel.
	ExecWorkDir string
	Scheduled   bool
	// AgentSessionID is the adapter's own conversation handle, persisted each
	// round so an interrupted run (yield, restart, redelegation back here)
	// resumes the agent's session instead of cold-starting (§5.2). The handle
	// only means something to the node that minted it — AgentSessionNode —
	// so a delegator may offer it back to that node (ResumeSessionID on the
	// wire) but must never adopt it for its own adapter or hand it onward.
	AgentSessionID   string
	AgentSessionNode string
	// AgentSessionAdapter is the adapter file that minted AgentSessionID
	// ("opencode.py") — the handle's second affinity axis alongside the node.
	// A retry or fallback that lands on a different adapter must not resume
	// the foreign handle: the adapter cannot parse it (opencode rejects
	// non-"ses" ids outright), the attempt dies on a provider error, and a
	// bounded retry loop re-burns a whole cold-start run per round.
	AgentSessionAdapter string

	// Plan-plane metadata (v0.0.6). A stage of a plan is an ordinary task, so
	// these are the only things it carries beyond one: PlanID/StageID name its
	// place in the plan, Needs is the stage_ids it waits for, and the artifacts
	// are the data plane — Inputs are the trees this stage starts from (each with
	// a node known to hold it), OutputArtifact the tree it produced for its
	// successors. All empty on a standalone task.
	PlanID         string
	StageID        string
	Needs          []string
	Inputs         []bus.ArtifactRef
	OutputArtifact string
	// ScreenOutput marks a task whose run drove a screen_sensing actuator —
	// its produced artifact IS display content. The executor stamps it at
	// claim time (its card is authoritative for operator-defined actuator
	// ids) and every delegator hop re-stamps it from the task_result flag,
	// so every node holding the row recognizes the output's sensitivity
	// without needing the producer's card.
	ScreenOutput bool

	// Remote is stamped at intake when this row was created by the delegate
	// handler — i.e. the intent text arrived over the wire — versus a local
	// submit. It is the authoritative provenance bit: chain[0] is
	// peer-supplied and can be written to claim local origin, so provenance
	// cannot be re-derived from Chain.
	Remote bool

	// UserLocale is the user's language preference ("en", "zh-CN", etc.).
	UserLocale string

	// Transport marks the transport mode for this task ("live" or "dtn").
	Transport string
	// DeadlineUnix is the absolute TTL expiration timestamp for DTN tasks (§8.2).
	DeadlineUnix int64
	// DelegationBudget is the remaining mesh-wide delegation quota (§6.1):
	// each forward hop decrements it, and a task that runs out may no longer
	// be delegated — the mesh-wide convergence bound alongside MaxChainDepth.
	DelegationBudget int
	// SpawnDepth is the sub-agent generation this task sits at in its causal
	// tree: a user-submitted root is 0, its PANDA_DELEGATE children 1, their
	// children 2, and so on. It rides the wire's Depth field (whose §6.1
	// comment already meant this) and persists here so every node a subtree
	// walks through enforces the same delegation.max_depth — refusing a
	// spawn needs the row's truth, not the spawning process's memory.
	SpawnDepth int
	// SpawnCap is the tightest delegation.max_depth seen anywhere along this
	// task's delegation path: 0 means "no inherited bound — the local node's
	// config applies". A delegate payload advertises the effective cap the
	// sender enforced (its own config tightened by whatever ITS parent
	// carried), the receiver persists min(its config, the wire value), and
	// the subtree thereafter enforces the most conservative bound any hop's
	// operator configured — so a permissive origin cannot launder children
	// through a strict node.
	SpawnCap int
	// TokenBudget is the remaining LLM token quota the task may spend across
	// the mesh (§6.1): 0 means unbounded (a legacy wire or a pre-budget row),
	// a positive value is the spendable remainder, and -1 is the persisted
	// exhaustion marker — an exhausted task may neither execute another
	// metered round nor be delegated onward, since either would launder the
	// bound into fresh budget on the next hop.
	TokenBudget int64
	// ScopePaths is the project-relative directory set a delegated child
	// declared it owns. It persists so every later hop and requeue re-derives
	// the same scope leaf locks (disjoint siblings run in parallel, the
	// whole-project fallback stays for undeclared rows) instead of silently
	// widening the child's claim to the entire worktree.
	ScopePaths []string
	// Isolate marks a delegated child that runs in a private copy of the
	// project tree instead of the shared one. It persists for the same
	// reason ScopePaths does: a requeue, forward or restart must keep
	// executing isolated — dropping the flag would run the child against
	// the shared tree with a lock set that protects nothing.
	Isolate bool
}

// NeedsFollowup reports whether this row parked in review with the
// supervisor's "more work remains" flag — the signal that makes "continue"
// an honest third answer alongside approve and reject. The flag lives in the
// stored result JSON, not a column, because it describes the result, not the
// task's identity.
func (t Task) NeedsFollowup() bool {
	return t.State == StateReview && needsFollowupResult(t.ResultJSON)
}

// GetUserLocale returns the task's language preference as an i18n.Locale,
// checking UserLocale first, then parsing SpecJSON, falling back to English.
func (t Task) GetUserLocale() i18n.Locale {
	if t.UserLocale != "" {
		if loc := i18n.Parse(t.UserLocale); loc != "" {
			return loc
		}
	}
	if t.SpecJSON != "" {
		var m struct {
			UserLocale string `json:"user_locale,omitempty"`
		}
		if err := json.Unmarshal([]byte(t.SpecJSON), &m); err == nil && m.UserLocale != "" {
			if loc := i18n.Parse(m.UserLocale); loc != "" {
				return loc
			}
		}
	}
	return i18n.English
}

// TaskDetail is the entry-model-derived task metadata (design doc §6.1 tasks
// schema). It is written once, shortly after creation, by SetDetail; the
// fields stay zero/empty for tasks that predate the entry model or arrived
// via a Phase 0 payload without detail. model_tier is intentionally absent —
// it is a Phase 3 feature (model-tier selection) and will be added when that
// lands.
type TaskDetail struct {
	ContextType  string
	ContextHash  string
	Intent       string
	SpecJSON     string
	Complexity   float64
	Risk         string
	ResourceJSON string
	// Requires carries the routing capability set so it survives past the
	// original delegate payload (decline re-routing, P1-5).
	Requires []string
	// UserLocale carries the user's language preference.
	UserLocale string
	// Transport carries the preferred transport mode ("live" or "dtn").
	Transport string
	// DeadlineUnix carries the absolute TTL expiration timestamp for DTN tasks.
	DeadlineUnix int64
}

// Terminal reports whether s has no valid outgoing transition.
func Terminal(s string) bool {
	return s == StateDone || s == StateCancelled || s == StateExpired
}

// CanTransition reports whether from -> to is legal per the Phase 0 state
// table (design doc §5.3.1).
func CanTransition(from, to string) bool {
	switch from {
	case StateSubmitted:
		// → review is the trust-zone admission park: a delegate from a
		// semi-trusted origin can be held for the operator before it ever
		// claims a slot (network.trust.accept.*.mode = review).
		return to == StateQueued || to == StateCancelled || to == StateReview
	case StateQueued:
		return to == StateDispatched || to == StateCancelled || to == StateExpired
	case StateDispatched:
		return to == StateRunning || to == StateWaitingCtx ||
			to == StateQueued || to == StateFailed || to == StateCancelled
	case StateWaitingCtx:
		return to == StateRunning || to == StateFailed || to == StateExpired ||
			to == StateCancelled
	case StateRunning:
		return to == StateReview || to == StateDone || to == StateFailed ||
			to == StateCancelled || to == StateQueued
	case StateReview:
		return to == StateDone || to == StateQueued || to == StateFailed ||
			to == StateCancelled
	case StateFailed:
		return to == StateQueued || to == StateCancelled || to == StateExpired ||
			to == StateReview
	default:
		return false // terminal states have no outgoing edges
	}
}
