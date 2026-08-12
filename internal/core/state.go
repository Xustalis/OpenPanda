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
	CreatedAt    int64
	UpdatedAt    int64
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
		return to == StateRunning || to == StateFailed || to == StateExpired
	case StateRunning:
		return to == StateReview || to == StateDone || to == StateFailed ||
			to == StateCancelled || to == StateQueued
	case StateReview:
		return to == StateDone || to == StateQueued || to == StateFailed ||
			to == StateCancelled
	case StateFailed:
		return to == StateQueued || to == StateCancelled || to == StateExpired
	default:
		return false // terminal states have no outgoing edges
	}
}
