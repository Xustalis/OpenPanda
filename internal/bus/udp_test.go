package bus

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// TestUDPEnvelopeRoundTrip seals an envelope on one socket and receives it on
// another — the base property the whole datagram plane rests on.
func TestUDPEnvelopeRoundTrip(t *testing.T) {
	secret := "mesh-secret"
	a, err := ListenUDP("127.0.0.1:0", secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := ListenUDP("127.0.0.1:0", secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	got := make(chan Envelope, 1)
	b.OnEnvelope = func(env Envelope, src *net.UDPAddr) { got <- env }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.ReadLoop(ctx)

	env, err := NewEnvelope(MsgHeartbeat, "node-a", "msg-1", HeartbeatPayload{Status: "online"})
	if err != nil {
		t.Fatal(err)
	}
	env.To = "node-b"
	if err := a.SendEnvelope(env, b.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	select {
	case env2 := <-got:
		if env2.From != "node-a" || env2.Type != MsgHeartbeat || env2.MsgID != "msg-1" || env2.To != "node-b" {
			t.Fatalf("envelope mismatch: %+v", env2)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("envelope not received")
	}
}

// TestUDPSealedTamper covers the AEAD boundary: a datagram sealed under a
// different key, or whose ciphertext is corrupted, is dropped silently.
func TestUDPSealedTamper(t *testing.T) {
	recv, err := ListenUDP("127.0.0.1:0", "right-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()
	got := make(chan Envelope, 1)
	recv.OnEnvelope = func(env Envelope, src *net.UDPAddr) { got <- env }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.ReadLoop(ctx)

	// Wrong key: the sender seals under a different secret.
	wrong, err := ListenUDP("127.0.0.1:0", "wrong-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	env, _ := NewEnvelope(MsgHeartbeat, "evil", "m1", nil)
	if err := wrong.SendEnvelope(env, recv.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
		t.Fatal("envelope sealed under wrong key was delivered")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestUDPDatagramTooBig verifies the MTU guard refuses oversize frames
// instead of fragmenting.
func TestUDPDatagramTooBig(t *testing.T) {
	u, err := ListenUDP("127.0.0.1:0", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	big := TaskResultPayload{TaskID: "t", AttemptID: "a", Stdout: strings.Repeat("x", UDPMaxDatagram)}
	env, err := NewEnvelope(MsgTaskResult, "a", "m1", big)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.SendEnvelope(env, u.LocalAddr()); err != ErrDatagramTooBig {
		t.Fatalf("oversize envelope err = %v, want ErrDatagramTooBig", err)
	}
}

// TestPunchFrame verifies the pinhole proof: correct signature + fresh ts
// accepts; tampering or staleness refuses.
func TestPunchFrame(t *testing.T) {
	secret := "s"
	now := time.Now()
	f := PunchFrame{Nonce: "n1", From: "a", TS: now.Unix()}
	f.Sig = PunchSig(secret, f.Nonce, f.From, f.TS)
	if !VerifyPunch(secret, f, now) {
		t.Fatal("valid punch rejected")
	}
	f.Sig = PunchSig(secret, f.Nonce, "b", f.TS) // signed for another sender
	if VerifyPunch(secret, f, now) {
		t.Fatal("punch signed for another sender accepted")
	}
	stale := PunchFrame{Nonce: "n1", From: "a", TS: now.Add(-2 * MaxHelloAge).Unix()}
	stale.Sig = PunchSig(secret, stale.Nonce, stale.From, stale.TS)
	if VerifyPunch(secret, stale, now) {
		t.Fatal("stale punch accepted")
	}
	if VerifyPunch("", f, now) || VerifyPunch(secret, PunchFrame{}, now) {
		t.Fatal("empty secret/fields accepted")
	}
}

// TestSTUNRoundTrip runs a binding request against a panda UDP socket and
// parses the XOR-MAPPED-ADDRESS answer — the zero-infrastructure reflexive
// discovery path.
func TestSTUNRoundTrip(t *testing.T) {
	srv, err := ListenUDP("127.0.0.1:0", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.ReadLoop(ctx)

	cli, err := ListenUDP("127.0.0.1:0", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	go cli.ReadLoop(ctx)

	addr, err := cli.STUNBinding(ctx, srv.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	if addr.Port != cli.LocalAddr().Port || !addr.IP.IsLoopback() {
		t.Fatalf("reflexive addr = %v, want %v", addr, cli.LocalAddr())
	}
}

// TestSTUNRateLimit verifies the per-source cap on binding answers: one
// source burns its window quota, a different source still gets answers, and
// an expired window resets the budget.
func TestSTUNRateLimit(t *testing.T) {
	u, err := ListenUDP("127.0.0.1:0", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	for i := 0; i < stunRateMax; i++ {
		if !u.allowSTUN("203.0.113.9") {
			t.Fatalf("answer %d of %d refused", i+1, stunRateMax)
		}
	}
	if u.allowSTUN("203.0.113.9") {
		t.Fatal("over-quota answer allowed")
	}
	if !u.allowSTUN("198.51.100.7") {
		t.Fatal("a different source was throttled by another's quota")
	}
	// An expired window opens a fresh budget.
	u.stunRateMu.Lock()
	u.stunRate["203.0.113.9"].reset = time.Now().Add(-time.Second)
	u.stunRateMu.Unlock()
	if !u.allowSTUN("203.0.113.9") {
		t.Fatal("expired window did not reset the budget")
	}
}

// TestSTUNRateCap verifies the tracked-source map is bounded: once it holds
// stunRateCap distinct sources, new IPs stop getting answers rather than
// growing memory under a spoofed-source flood.
func TestSTUNRateCap(t *testing.T) {
	u, err := ListenUDP("127.0.0.1:0", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	for i := 0; i < stunRateCap; i++ {
		u.stunRate[fmt.Sprintf("198.51.%d.%d", i>>8, i&0xff)] = &stunRateState{reset: time.Now().Add(time.Hour)}
	}
	if u.allowSTUN("203.0.113.99") {
		t.Fatal("new source answered past the tracking cap")
	}
}

// TestSTUNParseErrors covers the malformed-input boundary.
func TestSTUNParseErrors(t *testing.T) {
	var txn [stunTxnLen]byte
	if _, err := parseBindingResponse([]byte("short"), txn); err == nil {
		t.Fatal("short message parsed")
	}
	msg, txn2, err := buildBindingRequest()
	if err != nil {
		t.Fatal(err)
	}
	msg[0], msg[1] = 0x01, 0x01 // response type
	if _, err := parseBindingResponse(msg, txn2); err == nil {
		t.Fatal("empty response parsed")
	}
	if isSTUN([]byte("PN\x01\x03garbage")) {
		t.Fatal("PN frame classified as STUN")
	}
}

// TestUDPKeepalive verifies a keepalive datagram is accepted (sealed) and
// produces no envelope callback.
func TestUDPKeepalive(t *testing.T) {
	a, err := ListenUDP("127.0.0.1:0", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := ListenUDP("127.0.0.1:0", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	got := make(chan Envelope, 1)
	b.OnEnvelope = func(env Envelope, src *net.UDPAddr) { got <- env }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.ReadLoop(ctx)
	if err := a.SendKeepalive(b.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	select {
	case env := <-got:
		t.Fatalf("keepalive produced envelope %+v", env)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestPunchDatagram verifies a punch frame round-trips and an ack is
// distinguished from an opener.
func TestPunchDatagram(t *testing.T) {
	a, err := ListenUDP("127.0.0.1:0", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := ListenUDP("127.0.0.1:0", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	type hit struct {
		f     PunchFrame
		isAck bool
	}
	got := make(chan hit, 2)
	b.OnPunch = func(f PunchFrame, src *net.UDPAddr, isAck bool) { got <- hit{f, isAck} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.ReadLoop(ctx)

	f := PunchFrame{Nonce: "n", From: "a", TS: time.Now().Unix()}
	f.Sig = PunchSig("s", f.Nonce, f.From, f.TS)
	if err := a.SendPunch(f, b.LocalAddr(), false); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-got:
		if h.isAck || h.f.From != "a" || h.f.Nonce != "n" {
			t.Fatalf("punch frame mismatch: %+v", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("punch not received")
	}
}
