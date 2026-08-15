package bus

import (
	"encoding/json"
	"time"
)

func unixNow() int64 { return time.Now().Unix() }

// Message payloads. Each type has a Go struct used for typed (de)serialization
// at the handler layer.

// HelloPayload is sent when a node connects to declare its identity. Card is
// the node's capability summary (a compact JSON object); it is carried as raw
// JSON so the transport stays decoupled from the ledger package that owns the
// CapabilitySummary type. Sig is the HMAC-SHA256 (hex) of NodeID under the
// shared secret, proving the identity was minted by a node that holds the
// secret (design §16 / P0-1).
type HelloPayload struct {
	NodeID string          `json:"node_id"`
	Ver    string          `json:"ver"`
	Card   json.RawMessage `json:"card,omitempty"`
	Sig    string          `json:"sig"`
}

// HeartbeatPayload carries status + capacity.
type HeartbeatPayload struct {
	Status   string  `json:"status"`   // online|busy|offline
	Load     float64 `json:"load"`     // 0.0-1.0
	Capacity string  `json:"capacity"` // raw JSON from the card
}

// TaskDelegatePayload is the task handoff (design doc §10.3 example). The
// context transfer fields (design doc §12.4) select how the executor obtains
// the task's full context:
//   - context_level "pointer": context_hash references a snapshot the executor
//     may already have; on miss it fetches from the source node.
//   - context_level "summary": the intent/spec carried on the wire is the whole
//     context (no snapshot transfer).
//   - context_level "full": context_data carries the inline snapshot (base64).
type TaskDelegatePayload struct {
	TaskID       string   `json:"task_id"`
	ParentID     string   `json:"parent_id,omitempty"`
	Project      string   `json:"project,omitempty"`
	Title        string   `json:"title,omitempty"`
	ContextType  string   `json:"context_type,omitempty"`
	ContextHash  string   `json:"context_hash,omitempty"`
	ContextLevel string   `json:"context_level,omitempty"` // pointer|summary|full
	ContextData  []byte   `json:"context_data,omitempty"`  // inline full snapshot
	Intent       string   `json:"intent"`
	SpecJSON     string   `json:"spec_json,omitempty"`
	Requires     []string `json:"requires,omitempty"`
	PreferredNode string  `json:"preferred_node,omitempty"` // user-named node; honored when it matches
	Chain        []string `json:"chain"`
	TimeoutMS    int64    `json:"timeout_ms,omitempty"`
	MaxRetries   int      `json:"max_retries,omitempty"`
	Complexity   float64  `json:"complexity,omitempty"`
	Risk         string   `json:"risk,omitempty"`
	AttemptID    string   `json:"attempt_id,omitempty"`
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

// ContextAckPayload is the source node's response to a context_fetch. On OK it
// carries the full snapshot blob (base64) whose SHA-256 must equal Hash; on
// failure OK is false and the executor fails the task rather than guessing.
type ContextAckPayload struct {
	TaskID string   `json:"task_id"`
	Hash   string   `json:"hash"`
	OK     bool     `json:"ok"`
	Data   []byte   `json:"data,omitempty"`
	Refs   []string `json:"refs,omitempty"`
}
