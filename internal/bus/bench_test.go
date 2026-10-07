// SPDX-License-Identifier: AGPL-3.0-or-later

package bus

import (
	"encoding/json"
	"testing"
)

// BenchmarkEnvelopeHeartbeatMarshal measures the control-plane codec: build
// and marshal a heartbeat-sized envelope (the payload every node sends every
// few seconds, and the shape every other small message shares).
func BenchmarkEnvelopeHeartbeatMarshal(b *testing.B) {
	p := HeartbeatPayload{
		Status:    "online",
		Load:      0.3,
		Capacity:  `{"cpu_cores":8,"ram_gb":16,"max_concurrent":4,"current_tasks":1}`,
		Neighbors: []string{"node-a", "node-b", "node-c"},
		Links:     []LinkMetric{{Peer: "node-a", RTTms: 3}, {Peer: "node-b", RTTms: 12}},
		Projects:  []string{"openpanda"},
		Ver:       "0.0.10",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env, err := NewEnvelope(MsgHeartbeat, "node-a", "m-heartbeat", p)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := json.Marshal(env); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEnvelopeChunkMarshal measures the data-plane codec on the send
// side: a full 1 MiB artifact chunk, base64-encoded into its envelope. The
// reported MB/s is artifact bytes, not wire bytes (the wire form is ~4/3
// larger).
func BenchmarkEnvelopeChunkMarshal(b *testing.B) {
	data := make([]byte, ArtifactChunkBytes)
	for i := range data {
		data[i] = byte(i)
	}
	p := ArtifactChunkPayload{
		TaskID: "t-1", Hash: "abc", Offset: 0,
		Data: data, Total: int64(len(data)), EOF: true, OK: true,
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env, err := NewEnvelope(MsgArtifactChunk, "node-a", "m-chunk", p)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := json.Marshal(env); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEnvelopeChunkUnmarshal measures the receive side of the same
// frame: envelope decode (which validates the raw payload) plus the payload
// decode into the typed chunk.
func BenchmarkEnvelopeChunkUnmarshal(b *testing.B) {
	data := make([]byte, ArtifactChunkBytes)
	p := ArtifactChunkPayload{
		TaskID: "t-1", Hash: "abc", Offset: 0,
		Data: data, Total: int64(len(data)), EOF: true, OK: true,
	}
	env, err := NewEnvelope(MsgArtifactChunk, "node-a", "m-chunk", p)
	if err != nil {
		b.Fatal(err)
	}
	frame, err := json.Marshal(env)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var got Envelope
		if err := json.Unmarshal(frame, &got); err != nil {
			b.Fatal(err)
		}
		var cp ArtifactChunkPayload
		if err := got.PayloadInto(&cp); err != nil {
			b.Fatal(err)
		}
		if len(cp.Data) != len(data) {
			b.Fatalf("chunk data = %d bytes, want %d", len(cp.Data), len(data))
		}
	}
}
