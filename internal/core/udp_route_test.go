package core

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestUDPRouteExpiry covers the liveness sweep: a route whose last
// authenticated inbound traffic is older than udpRouteTTL must be reaped.
// UDP writes return success to dead endpoints, so a stale route otherwise
// blackholes every envelope sendTo routes through it — while reporting the
// "delivered" outbox row for deletion — and keeps the punch-maintain loop
// from ever re-offering.
func TestUDPRouteExpiry(t *testing.T) {
	c := newCoreWithNative(t, "udp-exp", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	defer c.Shutdown(context.Background())

	live := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5001}
	dead := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5002}
	unstamped := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5003}

	c.udpMu.Lock()
	c.udpRoutes = map[string]*net.UDPAddr{"live": live, "dead": dead, "unstamped": unstamped}
	c.udpHeard = map[string]time.Time{
		"live": time.Now(),
		"dead": time.Now().Add(-2 * udpRouteTTL),
	}
	stale := c.sweepStaleUDPRoutesLocked(time.Now())
	if len(stale) != 2 {
		c.udpMu.Unlock()
		t.Fatalf("expected 2 stale routes, got %v", stale)
	}
	if c.udpRoutes["dead"] != nil || c.udpRoutes["unstamped"] != nil {
		c.udpMu.Unlock()
		t.Fatal("stale routes survived the sweep")
	}
	if c.udpRoutes["live"] != live {
		c.udpMu.Unlock()
		t.Fatal("fresh route reaped")
	}
	if _, ok := c.udpHeard["dead"]; ok {
		c.udpMu.Unlock()
		t.Fatal("liveness entry left behind by sweep")
	}
	c.udpMu.Unlock()
}

// TestUDPKeepaliveRefreshesRoute pins the noteUDPAlive contract: a sealed
// keepalive proves mesh membership but carries no identity, so it may only
// renew the route bound to its exact source endpoint. A NAT-rebound port or
// an alien address must not keep the old binding alive — sends would still
// go to the dead port.
func TestUDPKeepaliveRefreshesRoute(t *testing.T) {
	c := newCoreWithNative(t, "udp-ka", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	defer c.Shutdown(context.Background())

	ep := &net.UDPAddr{IP: net.ParseIP("10.0.0.9"), Port: 7777}
	c.udpMu.Lock()
	c.udpRoutes = map[string]*net.UDPAddr{"peer": ep}
	c.udpHeard = map[string]time.Time{"peer": time.Now().Add(-time.Hour)}
	c.udpMu.Unlock()

	// Same IP, different port: no refresh — the route points at the old port
	// and must still expire so a new punch can rebind the real endpoint.
	c.noteUDPAlive(&net.UDPAddr{IP: ep.IP, Port: 8888})
	c.udpMu.Lock()
	if time.Since(c.udpHeard["peer"]) < udpRouteTTL {
		c.udpMu.Unlock()
		t.Fatal("keepalive from an alien endpoint refreshed the route")
	}
	c.udpMu.Unlock()

	c.noteUDPAlive(ep)
	c.udpMu.Lock()
	if time.Since(c.udpHeard["peer"]) > time.Second {
		c.udpMu.Unlock()
		t.Fatal("keepalive from the bound endpoint did not refresh")
	}
	c.udpMu.Unlock()
}

// TestReplyFallsBackToUDP: a peer reachable only over a punched datagram
// route (no WS conn) must still get its reply. Before reply went through
// sendTo, decline/result/ack traffic looked up connFor alone and failed
// "no peer" — silently dropping the answer on NAT-bound links.
func TestReplyFallsBackToUDP(t *testing.T) {
	ctx := context.Background()
	sender := newCoreWithNative(t, "rep-src", "127.0.0.1:0", ledger.NativeAbility{ID: "x", Command: "true"})
	if err := sender.ListenUDP(ctx, "127.0.0.1:0", nil); err != nil {
		t.Fatal(err)
	}
	defer sender.Shutdown(ctx)

	// The receiving end is a bare datagram plane: its callback is wired
	// before ReadLoop starts, the same ordering ListenUDP guarantees for the
	// core's own handlers — the hook write must precede any datagram read.
	recv, err := bus.ListenUDP("127.0.0.1:0", testSharedSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()
	got := make(chan bus.Envelope, 1)
	recv.OnEnvelope = func(env bus.Envelope, src *net.UDPAddr) { got <- env }
	go recv.ReadLoop(ctx)

	// Inject the endpoint a punch handshake would have bound.
	sender.udpMu.Lock()
	sender.udpRoutes = map[string]*net.UDPAddr{"rep-dst": recv.LocalAddr()}
	sender.udpHeard = map[string]time.Time{"rep-dst": time.Now()}
	sender.udpMu.Unlock()

	req := bus.Envelope{From: "rep-dst"}
	if err := sender.reply(ctx, req, bus.MsgContextAck, bus.ContextAckPayload{
		TaskID: "t1", Hash: "h", OK: true,
	}); err != nil {
		t.Fatalf("reply over udp route: %v", err)
	}

	select {
	case env := <-got:
		if env.Type != bus.MsgContextAck || env.From != "rep-src" || env.To != "rep-dst" {
			t.Fatalf("unexpected reply envelope: %+v", env)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reply never arrived over the udp route")
	}
}
