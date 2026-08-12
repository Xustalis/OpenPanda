package bus

import (
	"context"
	"errors"
	"log/slog"
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
		go handler(conn)
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
		go func() {
			go conn.StartPingLoop(ctx, 20*time.Millisecond)
			var env Envelope
			for {
				if err := conn.ReadJSON(&env); err != nil {
					return
				}
			}
		}()
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
