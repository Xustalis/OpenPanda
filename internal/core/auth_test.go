package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xenith/panda/internal/bus"
	"github.com/xenith/panda/internal/ledger"
)

// rawDial opens a raw WebSocket to addr's control endpoint, so a test can send
// hand-crafted envelopes (including forged ones) that the typed client cannot.
func rawDial(t *testing.T, addr string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

// startListener boots one core's WebSocket server and waits for it to accept.
func startListener(t *testing.T, c *Core, addr string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- c.Listen(ctx, addr) }()
	time.Sleep(200 * time.Millisecond)
}

// isTimeout reports whether err is a read-deadline (no frame) error rather than
// a real close.
func isTimeout(err error) bool {
	var netErr interface{ Timeout() bool }
	return errors.As(err, &netErr) && netErr.Timeout()
}

// taskEventsContain reports whether any task_events data_json for taskID
// contains substr.
func taskEventsContain(c *Core, ctx context.Context, taskID, substr string) bool {
	evs, err := c.store.Events(ctx, taskID)
	if err != nil {
		return false
	}
	for _, e := range evs {
		if strings.Contains(e.DataJSON, substr) {
			return true
		}
	}
	return false
}

// TestRejectBadHelloSig verifies a hello carrying a bad HMAC is rejected: the
// peer is not registered and no hello reply is sent (design §16 / P0-1).
func TestRejectBadHelloSig(t *testing.T) {
	ctx := context.Background()
	worker := newCoreWithNative(t, "worker", "127.0.0.1:17970", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	if err := worker.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	startListener(t, worker, "127.0.0.1:17970")

	ws := rawDial(t, "127.0.0.1:17970")
	env, _ := bus.NewEnvelope(bus.MsgHello, "attacker", "h-1", bus.HelloPayload{
		NodeID: "attacker", Ver: "t", Sig: bus.HelloSig("wrong-secret", "attacker"),
	})
	if err := ws.WriteJSON(env); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	// No hello reply should arrive: the bad signature is rejected. A read
	// timeout (no frame) is the expected outcome.
	_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := ws.ReadMessage(); !isTimeout(err) {
		t.Fatalf("expected no hello reply (timeout), got err=%v", err)
	}

	worker.mu.RLock()
	defer worker.mu.RUnlock()
	if _, ok := worker.peers["attacker"]; ok {
		t.Fatalf("attacker was registered despite a bad signature")
	}
}

// TestRejectSpoofedSender verifies that once a connection is authenticated as
// one node id, a message claiming a different sender is dropped and the
// connection is closed (design §16 / P0-1 identity binding).
func TestRejectSpoofedSender(t *testing.T) {
	ctx := context.Background()
	worker := newCoreWithNative(t, "worker", "127.0.0.1:17971", ledger.NativeAbility{ID: "sys:info", Command: "uname"})
	if err := worker.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	startListener(t, worker, "127.0.0.1:17971")

	ws := rawDial(t, "127.0.0.1:17971")

	// Valid hello as "attacker".
	hello, _ := bus.NewEnvelope(bus.MsgHello, "attacker", "h-1", bus.HelloPayload{
		NodeID: "attacker", Ver: "t", Sig: bus.HelloSig(testSharedSecret, "attacker"),
	})
	if err := ws.WriteJSON(hello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var reply bus.Envelope
	if err := ws.ReadJSON(&reply); err != nil {
		t.Fatalf("read hello reply: %v", err)
	}
	if reply.Type != bus.MsgHello {
		t.Fatalf("expected hello reply, got %q", reply.Type)
	}

	// Spoof a different sender after authenticating as "attacker".
	spoof, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "victim", "m-1", bus.TaskDelegatePayload{
		TaskID: "spoofed", Intent: "x", Requires: []string{"sys:info"}, Chain: []string{"victim"},
	})
	if err := ws.WriteJSON(spoof); err != nil {
		t.Fatalf("write spoofed delegate: %v", err)
	}

	// The worker must drop the connection (a non-timeout read error).
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil || isTimeout(err) {
		t.Fatalf("expected connection closed after spoofed sender, got err=%v", err)
	}

	// The spoofed task must not have been created.
	if _, err := worker.store.Get(ctx, "spoofed"); err == nil {
		t.Fatalf("spoofed task was created")
	}
}

// TestDelegatedTier2CannotForgeAuthorization verifies a delegated task cannot
// escalate to tier-2 authorization: the wire payload no longer carries an
// authorized flag, so the worker reads it from the DB (default false) and denies
// the tier-2 command (design §16 / P0-1).
func TestDelegatedTier2CannotForgeAuthorization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-t2", "127.0.0.1:17980")
	worker := newCoreWithNative(t, "worker-t2", "127.0.0.1:17981", ledger.NativeAbility{
		ID: "sys:tier2", Command: "echo", Args: []string{"hello"}, Tier: 2,
	})
	startPair(t, ctx, entry, worker, "127.0.0.1:17980", "127.0.0.1:17981")

	env, _ := bus.NewEnvelope(bus.MsgTaskDelegate, "entry-t2", "m1", bus.TaskDelegatePayload{
		TaskID: "tier2-forged", Intent: "run tier2", Requires: []string{"sys:tier2"},
		Chain: []string{"entry-t2"},
	})
	if err := entry.sendTo("worker-t2", env); err != nil {
		t.Fatalf("delegate: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := worker.store.Get(ctx, "tier2-forged")
		if err == nil && tk.State == StateFailed {
			if tk.Authorized {
				t.Fatalf("delegated task was authorized; the wire must not set authorized")
			}
			if !taskEventsContain(worker, ctx, "tier2-forged", "requires authorization") {
				t.Fatalf("expected tier-2 denial in task events")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	tk, err := worker.store.Get(ctx, "tier2-forged")
	if err != nil {
		t.Fatalf("worker never created tier-2 task: %v", err)
	}
	evs, _ := worker.store.Events(ctx, "tier2-forged")
	t.Logf("worker task state=%s authorized=%v events=%d", tk.State, tk.Authorized, len(evs))
	for _, e := range evs {
		t.Logf("  event %s data=%s", e.Type, e.DataJSON)
	}
	t.Fatalf("worker did not fail tier-2 delegated task within deadline")
}

// TestLocalTier2AuthorizedRuns is the positive contrast: a local task with
// explicit user authorization may run a tier-2 command, proving authorization is
// server-side state that only the local entry path can set.
func TestLocalTier2AuthorizedRuns(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithNative(t, "local-t2", "", ledger.NativeAbility{
		ID: "sys:tier2", Command: "echo", Args: []string{"hello"}, Tier: 2,
	})
	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title: "tier2", Intent: "run tier2", Requires: []string{"sys:tier2"}, Authorized: true,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("local authorized tier-2 state = %s, want done", task.State)
	}
	if !result.OK {
		t.Fatalf("local authorized tier-2 run failed: %s", result.Stderr)
	}
}
