package bus

import (
	"encoding/json"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	env, err := NewEnvelope(MsgTaskDelegate, "mac", "msg-1", TaskDelegatePayload{
		TaskID: "task-1",
		Intent: "build ios",
		Chain:  []string{"mac", "opi"},
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}
	if env.V != 1 || env.Type != MsgTaskDelegate || env.From != "mac" {
		t.Fatalf("bad envelope header: %+v", env)
	}

	// Serialize → deserialize → decode payload.
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var p TaskDelegatePayload
	if err := got.PayloadInto(&p); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if p.TaskID != "task-1" || p.Intent != "build ios" || len(p.Chain) != 2 {
		t.Fatalf("payload mismatch: %+v", p)
	}
}

func TestEnvelopeEmptyPayload(t *testing.T) {
	env, err := NewEnvelope(MsgTaskAccept, "opi", "msg-2", nil)
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}
	if len(env.Payload) != 0 {
		t.Fatalf("expected empty payload, got %s", env.Payload)
	}
	var out struct{ TaskID string }
	if err := env.PayloadInto(&out); err != nil {
		t.Fatalf("decode empty payload: %v", err)
	}
}

func TestHeartbeatPayloadCompact(t *testing.T) {
	// Design doc §10.6: heartbeat uses short field names and stays tiny.
	env, err := NewEnvelope(MsgHeartbeat, "opi", "msg-3", HeartbeatPayload{
		Status:   "online",
		Load:     0.15,
		Capacity: "{\"cpu\":4}",
	})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}
	raw, _ := json.Marshal(env)
	if len(raw) > 512 {
		t.Fatalf("heartbeat too large: %d bytes", len(raw))
	}
}
