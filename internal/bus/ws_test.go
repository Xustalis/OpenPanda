package bus

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// startTestServer boots a Server on addr, returning a cancel func and a done
// channel for Listen. Connections are handed to handler.
func startTestServer(t *testing.T, addr string, handler func(*Conn)) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := NewServer(addr, testLogger(), func(conn *Conn, _ string) {
		handler(conn)
	})
	done := make(chan error, 1)
	go func() { done <- srv.Listen(ctx) }()
	time.Sleep(150 * time.Millisecond)
	return cancel, done
}

// TestServerAcceptsAndEchoes verifies an envelope sent by a raw client is
// received by the server and echoed back.
func TestServerAcceptsAndEchoes(t *testing.T) {
	got := make(chan Envelope, 1)
	cancel, _ := startTestServer(t, "127.0.0.1:17876", func(conn *Conn) {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			return
		}
		got <- env
		_ = conn.Send(env)
	})
	defer cancel()

	ws, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:17876/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	env, _ := NewEnvelope(MsgHello, "client-a", "m-1", HelloPayload{NodeID: "client-a", Ver: "t"})
	if err := ws.WriteJSON(env); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case gotEnv := <-got:
		if gotEnv.Type != MsgHello || gotEnv.From != "client-a" {
			t.Fatalf("server got unexpected envelope: %+v", gotEnv)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not receive envelope")
	}

	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var echoed Envelope
	if err := ws.ReadJSON(&echoed); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if echoed.MsgID != "m-1" {
		t.Fatalf("echo mismatch: %+v", echoed)
	}
}

// TestClientDialConnects verifies the Client dials a server and survives a
// hello round trip.
func TestClientDialConnects(t *testing.T) {
	got := make(chan Envelope, 1)
	cancel, _ := startTestServer(t, "127.0.0.1:17878", func(conn *Conn) {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			return
		}
		got <- env
	})
	defer cancel()

	client := NewClient("ws://127.0.0.1:17878/ws", testLogger())
	conn, err := client.Dial(context.Background())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	env, _ := NewEnvelope(MsgHello, "client-b", "m-2", HelloPayload{NodeID: "client-b"})
	if err := conn.Send(env); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case gotEnv := <-got:
		if gotEnv.From != "client-b" {
			t.Fatalf("client id mismatch: %+v", gotEnv)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not receive client envelope")
	}
}

// TestPingLoopSendsPings verifies StartPingLoop issues pings at the given
// cadence; the gorilla client auto-answers pongs, so the loop is the thing
// under test.
func TestPingLoopSendsPings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer("127.0.0.1:17877", testLogger(), func(conn *Conn, _ string) {
		go conn.StartPingLoop(ctx, 20*time.Millisecond)
		var env Envelope
		for {
			if err := conn.ReadJSON(&env); err != nil {
				return
			}
		}
	})
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Listen(ctx) }()
	time.Sleep(150 * time.Millisecond)

	ws, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:17877/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// Keep the connection alive for ~10 ping periods (~200ms) by sending
	// data; the ping loop must keep the read path functional throughout.
	for i := 0; i < 5; i++ {
		if err := ws.WriteJSON(Envelope{Type: MsgHello, MsgID: "ping-test"}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		time.Sleep(40 * time.Millisecond)
	}

	// Final read: a clean timeout (no frame) is fine; a closed connection
	// means the ping loop failed to keep the peer alive.
	_ = ws.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, _, err = ws.ReadMessage()
	if err != nil && !isReadTimeout(err) {
		t.Fatalf("connection closed during ping loop: %v", err)
	}
}

func isReadTimeout(err error) bool {
	var netErr interface{ Timeout() bool }
	return errors.As(err, &netErr) && netErr.Timeout()
}

// TestServerDropsSlowHello verifies an inbound WebSocket connection that never
// sends a hello is closed within helloTimeout.
func TestServerDropsSlowHello(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := NewServer("127.0.0.1:17879", testLogger(), func(conn *Conn, _ string) {
		var env Envelope
		for {
			if err := conn.ReadJSON(&env); err != nil {
				return
			}
		}
	})
	srv.helloTimeout = 500 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- srv.Listen(ctx) }()
	time.Sleep(100 * time.Millisecond)

	ws, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:17879/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	start := time.Now()
	// Read without sending anything; the server should close us once
	// srv.helloTimeout expires.
	_ = ws.SetReadDeadline(time.Now().Add(srv.helloTimeout + 500*time.Millisecond))
	_, _, err = ws.ReadMessage()
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected slow hello to be closed")
	}
	if elapsed < srv.helloTimeout-100*time.Millisecond {
		t.Fatalf("closed too fast: %v", elapsed)
	}
}

// TestServerEnforcesConnectionLimits verifies global and per-IP connection
// limits are applied before the WebSocket upgrade.
func TestServerEnforcesConnectionLimits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conns := make(chan *Conn, 4)
	// Use two separate channels instead of reassigning one variable: the handler
	// closure captures the variable by reference, so reassignment races with the
	// goroutine still reading the first channel.
	block1 := make(chan struct{})
	block2 := make(chan struct{})
	var block atomic.Value
	block.Store(block1)
	srv := NewServer("127.0.0.1:17880", testLogger(), func(conn *Conn, _ string) {
		conns <- conn
		// Block until the test releases so connections count as active.
		<-block.Load().(chan struct{})
	})

	done := make(chan error, 1)
	go func() { done <- srv.Listen(ctx) }()
	time.Sleep(100 * time.Millisecond)

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 2 * time.Second

	// Per-IP limit: allow only one connection from the same IP.
	srv.SetLimits(10, 1)

	ws1, resp, err := dialer.Dial("ws://127.0.0.1:17880/ws", nil)
	if err != nil {
		t.Fatalf("first dial: %v (status %d)", err, statusOf(resp))
	}
	defer ws1.Close()

	_, resp, err = dialer.Dial("ws://127.0.0.1:17880/ws", nil)
	if err == nil {
		t.Fatalf("second same-IP dial should have been rejected")
	}
	if statusOf(resp) != http.StatusServiceUnavailable {
		t.Fatalf("second same-IP status = %d, want 503", statusOf(resp))
	}

	// Release the first connection and reset limits for the global-limit check.
	// With global=2 and per-IP=10, three same-IP connections should hit the
	// global limit on the third.
	close(block1)
	// Wait for the handler goroutine to unblock and decrement counters.
	time.Sleep(100 * time.Millisecond)

	block.Store(block2)
	srv.SetLimits(2, 10)

	ws2, resp, err := dialer.Dial("ws://127.0.0.1:17880/ws", nil)
	if err != nil {
		t.Fatalf("global-limit first dial: %v (status %d)", err, statusOf(resp))
	}
	defer ws2.Close()

	ws3, resp, err := dialer.Dial("ws://127.0.0.1:17880/ws", nil)
	if err != nil {
		t.Fatalf("global-limit second dial: %v (status %d)", err, statusOf(resp))
	}
	defer ws3.Close()

	_, resp, err = dialer.Dial("ws://127.0.0.1:17880/ws", nil)
	if err == nil {
		t.Fatalf("third dial should have been rejected by global limit")
	}
	if statusOf(resp) != http.StatusServiceUnavailable {
		t.Fatalf("third dial status = %d, want 503", statusOf(resp))
	}
	close(block2)
}

func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}
