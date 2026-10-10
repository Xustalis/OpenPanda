// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/gorilla/websocket"
)

// pairClient is the initiator side of a pairing handshake in tests — a raw
// websocket speaking the pair sub-protocol, standing in for `panda pair`.
type pairClient struct {
	ws      *websocket.Conn
	session string
	initKey *ecdh.PrivateKey
	initPub ed25519.PublicKey
	nonce   []byte
	respX   []byte
	respPub []byte
	respN   []byte
	dh      []byte
}

func dialPair(t *testing.T, addr string) *pairClient {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed key: %v", err)
	}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("x25519 key: %v", err)
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	c := &pairClient{ws: ws, session: "sess-" + t.Name(), initKey: key, initPub: pub, nonce: nonce}
	env, err := bus.NewEnvelope(bus.MsgPairHello, "pair-init", "m-"+c.session, bus.PairHelloPayload{
		Session: c.session,
		X:       hex.EncodeToString(key.PublicKey().Bytes()),
		Pub:     hex.EncodeToString(pub),
		Nonce:   hex.EncodeToString(nonce),
		Addr:    "0.0.0.0:19999",
		Name:    "pair-init",
		NodeID:  "pair-init",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if err := ws.WriteJSON(env); err != nil {
		t.Fatalf("pair_hello: %v", err)
	}
	return c
}

// awaitReady reads the responder's pair_ready and completes the DH.
func (c *pairClient) awaitReady(t *testing.T) {
	t.Helper()
	var env bus.Envelope
	if err := c.ws.ReadJSON(&env); err != nil {
		t.Fatalf("read pair_ready: %v", err)
	}
	if env.Type != bus.MsgPairReady {
		t.Fatalf("expected pair_ready, got %s", env.Type)
	}
	var r bus.PairReadyPayload
	if err := env.PayloadInto(&r); err != nil || r.Session != c.session {
		t.Fatalf("bad pair_ready: %v", r.Session)
	}
	c.respX, _ = hex.DecodeString(r.X)
	c.respPub, _ = hex.DecodeString(r.Pub)
	c.respN, _ = hex.DecodeString(r.Nonce)
	xk, err := ecdh.X25519().NewPublicKey(c.respX)
	if err != nil {
		t.Fatalf("peer x key: %v", err)
	}
	dh, err := c.initKey.ECDH(xk)
	if err != nil {
		t.Fatalf("dh: %v", err)
	}
	c.dh = dh
}

// awaitReply reads the next pair frame — pair_secret or pair_reject.
func (c *pairClient) awaitReply(t *testing.T) bus.Envelope {
	t.Helper()
	var env bus.Envelope
	if err := c.ws.ReadJSON(&env); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	return env
}

// TestPairHandshakeDeliversSecret runs the whole ceremony on a real
// listener: pair_hello → pair_ready → SAS visible in the row → operator
// confirms → pair_secret arrives sealed and opens to the mesh secret.
func TestPairHandshakeDeliversSecret(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	srv := newCore(t, "pair-host", "127.0.0.1:18010")
	if err := srv.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	go func() { _ = srv.Listen(ctx, "127.0.0.1:18010") }()
	time.Sleep(200 * time.Millisecond)

	cli := dialPair(t, "127.0.0.1:18010")
	defer cli.ws.Close()
	cli.awaitReady(t)

	// The responder's session row must be listed with a code that matches
	// the initiator's own derivation — that equality IS the compare.
	sessions, err := ListPairSessions(srv.db)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("no pair session listed: %v", err)
	}
	row := sessions[0]
	want := PairCode(cli.initKey.PublicKey().Bytes(), cli.respX, cli.initPub, cli.respPub, cli.nonce, cli.respN, cli.dh)
	if row.SAS != want {
		t.Fatalf("SAS mismatch: row %q, initiator %q", row.SAS, want)
	}
	if row.State != PairStateReady {
		t.Fatalf("session state %q, want ready", row.State)
	}

	if err := AnswerPairSession(srv.db, row.ID, PairStateConfirmed); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	env := cli.awaitReply(t)
	if env.Type != bus.MsgPairSecret {
		t.Fatalf("expected pair_secret, got %s", env.Type)
	}
	var ps bus.PairSecretPayload
	if err := env.PayloadInto(&ps); err != nil {
		t.Fatalf("payload: %v", err)
	}
	key, err := PairDeriveKey(cli.dh, cli.nonce, cli.respN)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	box, err := base64.StdEncoding.DecodeString(ps.Box)
	if err != nil {
		t.Fatalf("box b64: %v", err)
	}
	plain, err := PairOpen(key, box)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(plain) != testSharedSecret {
		t.Fatalf("delivered secret %q, want %q", plain, testSharedSecret)
	}
}

// TestPairRejectedByOperator checks the refusal path: rejecting a ready
// session ends it with a pair_reject on the wire.
func TestPairRejectedByOperator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	srv := newCore(t, "pair-host", "127.0.0.1:18011")
	if err := srv.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	go func() { _ = srv.Listen(ctx, "127.0.0.1:18011") }()
	time.Sleep(200 * time.Millisecond)

	cli := dialPair(t, "127.0.0.1:18011")
	defer cli.ws.Close()
	cli.awaitReady(t)

	sessions, err := ListPairSessions(srv.db)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("no session: %v", err)
	}
	if err := AnswerPairSession(srv.db, sessions[0].ID, PairStateRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}
	env := cli.awaitReply(t)
	if env.Type != bus.MsgPairReject {
		t.Fatalf("expected pair_reject, got %s", env.Type)
	}
	var rj bus.PairRejectPayload
	_ = env.PayloadInto(&rj)
	if rj.Reason != "rejected" {
		t.Fatalf("reason %q, want rejected", rj.Reason)
	}
}

// TestPairHelloOnBoundConnRefused: a pair_hello riding an already
// authenticated mesh conn must not open a session — the peer demonstrably
// holds the secret already.
func TestPairHelloOnBoundConnRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	a := newCore(t, "pair-a", "127.0.0.1:18012")
	b := newCore(t, "pair-b", "127.0.0.1:18013")
	if err := a.Register(ctx); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := b.Register(ctx); err != nil {
		t.Fatalf("register b: %v", err)
	}
	go func() { _ = a.Listen(ctx, "127.0.0.1:18012") }()
	go func() { _ = b.Listen(ctx, "127.0.0.1:18013") }()
	time.Sleep(200 * time.Millisecond)
	if err := a.DialPeer(ctx, "127.0.0.1:18013"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	waitPeer(t, a, "pair-b")
	waitPeer(t, b, "pair-a")

	env, err := bus.NewEnvelope(bus.MsgPairHello, "pair-a", "bound-pair", bus.PairHelloPayload{
		Session: "bound", X: "00", Pub: "00", Nonce: "00", Addr: "x", Name: "n",
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if err := a.sendTo("pair-b", env); err != nil {
		t.Fatalf("send: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	sessions, _ := ListPairSessions(b.db)
	for _, s := range sessions {
		if s.ID == "bound" && s.State == PairStateReady {
			t.Fatal("bound conn opened a ready pair session")
		}
	}
}

// TestPairSASDeterministicAndBinding: same inputs → same code; any changed
// input → (overwhelmingly) a different one.
func TestPairSASDeterministicAndBinding(t *testing.T) {
	a := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	b := []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	p1 := []byte("pppppppppppppppppppppppppppppppp")
	p2 := []byte("qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq")
	n1 := []byte("nnnnnnnnnnnnnnnn")
	n2 := []byte("mmmmmmmmmmmmmmmm")
	dh := []byte("shared-secret-material-here-12345")

	c1 := pairSAS(a, b, p1, p2, n1, n2, dh)
	if c1 != pairSAS(a, b, p1, p2, n1, n2, dh) {
		t.Fatal("SAS not deterministic")
	}
	if len(c1) != 7 || c1[3] != '-' {
		t.Fatalf("SAS format %q, want NNN-NNN", c1)
	}
	if c2 := pairSAS(a, b, p1, p2, n1, n2, []byte("different-dh-material-0000000")); c2 == c1 {
		t.Fatal("SAS did not bind the DH secret")
	}
	if c2 := pairSAS(b, a, p1, p2, n1, n2, dh); c2 == c1 {
		t.Fatal("SAS did not bind key ordering")
	}
}
