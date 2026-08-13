package bus

import (
	"time"
)

func unixNow() int64 { return time.Now().Unix() }

// Message payloads. Each type has a Go struct used for typed (de)serialization
// at the handler layer.

// HelloPayload is sent when a node connects to declare its identity.
type HelloPayload struct {
	NodeID string `json:"node_id"`
	Ver    string `json:"ver"`
}

// HeartbeatPayload carries status + capacity.
type HeartbeatPayload struct {
	Status   string  `json:"status"`   // online|busy|offline
	Load     float64 `json:"load"`     // 0.0-1.0
	Capacity string  `json:"capacity"` // raw JSON from the card
}

// TaskDelegatePayload is the task handoff (design doc §10.3 example).
type TaskDelegatePayload struct {
	TaskID      string   `json:"task_id"`
	ParentID    string   `json:"parent_id,omitempty"`
	Project     string   `json:"project,omitempty"`
	Title       string   `json:"title,omitempty"`
	ContextType string   `json:"context_type,omitempty"`
	ContextHash string   `json:"context_hash,omitempty"`
	Intent      string   `json:"intent"`
	SpecJSON    string   `json:"spec_json,omitempty"`
	Requires    []string `json:"requires,omitempty"`
	Chain       []string `json:"chain"`
	TimeoutMS   int64    `json:"timeout_ms,omitempty"`
	MaxRetries  int      `json:"max_retries,omitempty"`
	Complexity  float64  `json:"complexity,omitempty"`
	Risk        string   `json:"risk,omitempty"`
	AttemptID   string   `json:"attempt_id,omitempty"`
}

// TitleOrDefault returns the explicit title, falling back to the intent.
func (p *TaskDelegatePayload) TitleOrDefault() string {
	if p.Title != "" {
		return p.Title
	}
	return p.Intent
}

// TaskAcceptPayload confirms acceptance.
type TaskAcceptPayload struct {
	TaskID string `json:"task_id"`
}

// TaskDeclinePayload rejects with a reason.
type TaskDeclinePayload struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
}

// TaskResultPayload is the completion result.
type TaskResultPayload struct {
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
	OK        bool   `json:"ok"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Artifacts string `json:"artifacts,omitempty"`
}

// TaskCancelPayload requests cancellation.
type TaskCancelPayload struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason,omitempty"`
}

// ContextFetchPayload asks the source node for a full context snapshot.
type ContextFetchPayload struct {
	TaskID      string `json:"task_id"`
	Hash        string `json:"hash"`
	ContextType string `json:"context_type,omitempty"`
}

// ContextAckPayload acknowledges receipt.
type ContextAckPayload struct {
	TaskID string `json:"task_id"`
	Hash   string `json:"hash"`
	OK     bool   `json:"ok"`
}
