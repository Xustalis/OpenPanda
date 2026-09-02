// Package core hosts the node lifecycle and the task state machine.
package core

import (
	"errors"

	"github.com/Xustalis/OpenPanda/internal/bus"
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

	// EvRecover records that a daemon restart normalized a task left in an
	// active state by the previous process instance: running/waiting_context
	// become failed (their execution is gone), dispatched/submitted return to
	// queued. Written per task so the audit chain explains a state change that
	// no user or peer requested. review is never recovered — see
	// TaskStore.Recover.
	EvRecover = "recover"
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
	TaskID       string
	ParentID     string
	Project      string
	Title        string
	State        string
	OwnerNode    string
	AttemptID    string
	StateVersion int
	Chain        []string
	Intent       string
	SpecJSON     string
	ResultJSON   string
	ContextType  string
	ContextHash  string
	Complexity   float64
	Risk         string
	ResourceJSON string
	// Authorized records whether the user consented to executing tier-2
	// (irreversible) commands. It is server-side state (design §16 / P0-1), not
	// wire-carried, so a delegated task cannot forge authorization.
	Authorized bool
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
	Scheduled    bool

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
		return to == StateQueued || to == StateCancelled
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
