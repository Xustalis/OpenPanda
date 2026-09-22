package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

func TestAgentNegotiationSignaling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := newCore(t, "node-a", "127.0.0.1:17971")
	b := newCore(t, "node-b", "127.0.0.1:17972")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(a.Register(ctx))
	must(b.Register(ctx))

	go func() { _ = a.Listen(ctx, "127.0.0.1:17971") }()
	go func() { _ = b.Listen(ctx, "127.0.0.1:17972") }()
	time.Sleep(100 * time.Millisecond)

	must(a.DialPeer(ctx, "127.0.0.1:17972"))
	time.Sleep(200 * time.Millisecond)

	// Send negotiation signal from node-a to node-b
	err := a.SendNegotiation(ctx, "node-b", bus.AgentNegotiatePayload{
		FromNode:  "node-a",
		FromAgent: "claude_code_01",
		Timestamp: time.Now().UnixMilli(),
		Weight:    85,
		TargetScope: bus.TargetScope{
			Repo:   "openpanda/core",
			File:   "internal/servo/driver.go",
			Symbol: "func RotateServo",
		},
		Intent:        "modify_signature_for_voice_control",
		ActionPreview: "add_param_speed",
	})
	if err != nil {
		t.Fatalf("send negotiation: %v", err)
	}

	// Give a moment for b to receive and process
	time.Sleep(200 * time.Millisecond)
}
