// SPDX-License-Identifier: AGPL-3.0-or-later

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

// TestProvenanceWireRoundTrip pins the build-provenance field on the two
// directory frames: a stamped value survives marshal → PayloadInto on both
// hello and heartbeat, and a pre-provenance frame (field absent outright)
// decodes to "" rather than failing — the legacy-peer compat contract.
func TestProvenanceWireRoundTrip(t *testing.T) {
	env, err := NewEnvelope(MsgHello, "n1", "m-1", HelloPayload{NodeID: "n1", Ver: "v", Prov: "self"})
	if err != nil {
		t.Fatalf("new envelope: %v", err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var hp HelloPayload
	if err := got.PayloadInto(&hp); err != nil {
		t.Fatalf("hello decode: %v", err)
	}
	if hp.Prov != "self" {
		t.Fatalf("hello prov = %q, want self", hp.Prov)
	}

	env, err = NewEnvelope(MsgHeartbeat, "n1", "m-2", HeartbeatPayload{Status: "online", Prov: "release"})
	if err != nil {
		t.Fatalf("new heartbeat envelope: %v", err)
	}
	var got2 Envelope
	raw, _ = json.Marshal(env)
	if err := json.Unmarshal(raw, &got2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var bp HeartbeatPayload
	if err := got2.PayloadInto(&bp); err != nil {
		t.Fatalf("heartbeat decode: %v", err)
	}
	if bp.Prov != "release" {
		t.Fatalf("heartbeat prov = %q, want release", bp.Prov)
	}

	// Legacy frame: prov absent outright decodes to "" (old-peer unknown).
	var legacy HelloPayload
	if err := json.Unmarshal([]byte(`{"node_id":"old","ver":"v","sig":"s"}`), &legacy); err != nil {
		t.Fatalf("legacy hello decode: %v", err)
	}
	if legacy.Prov != "" {
		t.Fatalf("legacy hello prov = %q, want empty", legacy.Prov)
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
	// docs/protocol.md: heartbeat uses short field names and stays tiny.
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

func TestEnvelopeRequiresMsgID(t *testing.T) {
	_, err := NewEnvelope(MsgTaskAccept, "opi", "", nil)
	if err == nil {
		t.Fatalf("expected error when msgID is empty, got nil")
	}
}
