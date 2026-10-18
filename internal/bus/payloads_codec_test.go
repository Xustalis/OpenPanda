// SPDX-License-Identifier: AGPL-3.0-or-later

package bus

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestChunkWireRoundTrip covers the fast-path codec end to end: the payload
// goes through NewEnvelope's marshalPayload type switch, the envelope through
// marshalEnvelope, and a stock decoder must read back the same fields —
// interop with the unoptimized codec is the contract.
func TestChunkWireRoundTrip(t *testing.T) {
	cases := []ArtifactChunkPayload{
		{TaskID: "t-1", Hash: "abc", Offset: 0, Data: []byte("hi"), Total: 5, EOF: true, OK: true},
		{TaskID: "t", OK: false, Reason: `say "hi" \n`},
		{TaskID: "empty", OK: true}, // nil data → field omitted
		{TaskID: "bin", Hash: "h", Data: make([]byte, 70000), OK: true},
	}
	for i, p := range cases {
		env, err := NewEnvelope(MsgArtifactChunk, "node-a", "m-1", p)
		if err != nil {
			t.Fatalf("case %d NewEnvelope: %v", i, err)
		}
		raw, err := marshalEnvelope(env)
		if err != nil {
			t.Fatalf("case %d marshalEnvelope: %v", i, err)
		}
		var wire Envelope
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("case %d envelope decode: %v", i, err)
		}
		var got ArtifactChunkPayload
		if err := wire.PayloadInto(&got); err != nil {
			t.Fatalf("case %d payload decode: %v", i, err)
		}
		if got.TaskID != p.TaskID || got.Hash != p.Hash || got.Offset != p.Offset ||
			got.Total != p.Total || got.EOF != p.EOF || got.OK != p.OK || got.Reason != p.Reason ||
			!bytes.Equal(got.Data, p.Data) {
			t.Fatalf("case %d round-trip: %+v", i, got)
		}
		if !bytes.Contains(raw, []byte(`"payload":`)) && len(p.Data) > 0 {
			t.Fatalf("case %d: payload field missing from wire", i)
		}
	}
}

// TestEnvelopeSpliceMatchesStock asserts marshalEnvelope's output is
// field-for-field identical to stock encoding — the splice may reorder
// nothing and drop nothing.
func TestEnvelopeSpliceMatchesStock(t *testing.T) {
	env, err := NewEnvelope(MsgArtifactChunk, "node-a", "m-2",
		ArtifactChunkPayload{TaskID: "t", Hash: "h", Data: []byte("x"), OK: true})
	if err != nil {
		t.Fatal(err)
	}
	fast, err := marshalEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	stock, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	// Both decode to identical envelopes; the wire bytes need not be equal
	// only where field order legitimately differs — compare decoded.
	var a, b Envelope
	if err := json.Unmarshal(fast, &a); err != nil {
		t.Fatalf("fast bytes invalid: %v", err)
	}
	if err := json.Unmarshal(stock, &b); err != nil {
		t.Fatalf("stock bytes invalid: %v", err)
	}
	if a.V != b.V || a.Type != b.Type || a.MsgID != b.MsgID || a.From != b.From ||
		a.To != b.To || a.TS != b.TS || !bytes.Equal(a.Payload, b.Payload) {
		t.Fatal("spliced envelope diverged from stock encoding")
	}
}

func TestPushWireRoundTrip(t *testing.T) {
	p := ArtifactPushPayload{TaskID: "t", Hash: "h", Offset: 3, Data: []byte("push"), Total: 9}
	raw, err := p.marshalWire()
	if err != nil {
		t.Fatal(err)
	}
	var got ArtifactPushPayload
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.TaskID != p.TaskID || got.Hash != p.Hash || got.Offset != p.Offset ||
		got.Total != p.Total || !bytes.Equal(got.Data, p.Data) {
		t.Fatalf("got %+v want %+v", got, p)
	}
}
