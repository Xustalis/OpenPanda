// Package core hosts the node lifecycle and the task state machine.
package core

import "errors"

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
	LeaseExpires int64
	CreatedAt    int64
	UpdatedAt    int64
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
