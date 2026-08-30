// Package bus implements the WebSocket transport and wire protocol for
// node-to-node task delegation (design doc §10).
package bus

import (
	"encoding/json"
	"fmt"
)

// Message types. These strings are the routing key in the envelope's "type"
// field and are part of the cross-node protocol.
const (
	MsgHello        = "hello"
	MsgJoin         = "join"
	MsgHeartbeat    = "heartbeat"
	MsgTaskDelegate = "task_delegate"
	MsgTaskAccept   = "task_accept"
	MsgTaskDecline  = "task_decline"
	MsgTaskProgress = "task_progress"
	MsgTaskResult   = "task_result"
	MsgTaskRetry    = "task_retry"
	MsgTaskTransfer = "task_transfer"
	MsgTaskCancel   = "task_cancel"
	// MsgTaskResume is the delegator's approval for a task the executor
	// parked in review for lack of tier-2 consent: the user granted it, and
	// the re-run belongs on the node that holds the task (its capability
	// match, context, and worktree), not wherever the approval was given.
	MsgTaskResume   = "task_resume"
	MsgContextFetch = "context_fetch"
	MsgContextAck   = "context_ack"
	// Artifact transfer is a pull in fixed-size chunks: the node that needs a
	// task's output asks the node that holds it, one offset at a time. It is
	// chunked because a single frame may not exceed readLimit (4 MiB) and an
	// artifact is a build tree or a trained model.
	MsgArtifactFetch = "artifact_fetch"
	MsgArtifactChunk = "artifact_chunk"
)

// Envelope is the JSON wire format (design doc §10.3). MsgID is a UUIDv7
// used for dedup at the receiving end.
type Envelope struct {
	V       int             `json:"v"`
	Type    string          `json:"type"`
	MsgID   string          `json:"msg_id"`
	From    string          `json:"from"`
	To      string          `json:"to,omitempty"`
	TS      int64           `json:"ts"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// NewEnvelope builds an envelope. msgID must be non-empty for idempotency.
//
// A payload that carries process output gets a chance to shrink first
// (wireClamper). The frame limit is this package's own constraint, so honouring
// it belongs here rather than in every caller: a sender is free to write a frame
// larger than readLimit, and the cost lands on the receiver, which closes the
// connection instead of delivering the message. Clamping at the point every
// message is built means a completed task cannot lose its result — and take the
// peer link with it — because its log was long.
func NewEnvelope(typ, from, msgID string, payload any) (Envelope, error) {
	var raw json.RawMessage
	if payload != nil {
		if c, ok := payload.(wireClamper); ok {
			payload = c.clampForWire()
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, fmt.Errorf("marshal payload: %w", err)
		}
		raw = b
	}
	return Envelope{
		V: 1, Type: typ, MsgID: msgID, From: from, Payload: raw,
		TS: nowUnix(),
	}, nil
}

// wireClamper is a payload that knows how to bound its own oversized fields.
// clampForWire returns a clamped copy (payloads travel by value), so the
// caller's own struct is never mutated behind its back.
type wireClamper interface {
	clampForWire() any
}

// PayloadInto decodes the envelope payload into v.
func (e *Envelope) PayloadInto(v any) error {
	if len(e.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("decode %s payload: %w", e.Type, err)
	}
	return nil
}

// nowUnix is a variable so tests can stub time.
var nowUnix = func() int64 { return unixNow() }
