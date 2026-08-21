package core

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestDaemonStyleMutualDialNoStorm reproduces the real-device reconnect
// storm: two daemons list each other as peers, both run the cmd/panda
// maintain loop (MaintainPeer + immediate 1s-cadence redial on nil return),
// and the dedup loser side must NOT end up redialing every interval forever.
// The losing side's MaintainPeer is expected to block while the surviving
// edge (the peer's own conn to us) stays alive, so over the observation
// window only a couple of dials should happen — not one per cadence tick.
func TestDaemonStyleMutualDialNoStorm(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Node ids mirror the real deployment: "macbook" < "orangepi3b", so the
	// macbook-initiated conn wins the mutual-dial tie-break on both sides.
	a := newTaggedCore(t, "A", "macbook", "127.0.0.1:17961")
	b := newTaggedCore(t, "B", "orangepi3b", "127.0.0.1:17962")
	for _, c := range []*Core{a, b} {
		if err := c.Register(ctx); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	go func() { _ = a.Listen(ctx, "127.0.0.1:17961") }()
	go func() { _ = b.Listen(ctx, "127.0.0.1:17962") }()
	time.Sleep(200 * time.Millisecond)

	// Mac role: one MaintainPeer — its outbound conn is the dedup winner and
	// the loop just blocks serving it.
	go func() { _ = a.MaintainPeer(ctx, "127.0.0.1:17962") }()
	time.Sleep(300 * time.Millisecond)

	// Pi role: the daemon loop — MaintainPeer; on nil return reconnect after
	// a short cadence (production uses 1s; the test compresses it).
	var dials atomic.Int64
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		for {
			if err := b.MaintainPeer(ctx, "127.0.0.1:17961"); err != nil {
				t.Errorf("maintain peer: %v", err)
				return
			}
			dials.Add(1)
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()

	time.Sleep(3 * time.Second)
	n := dials.Load()
	if n > 4 {
		t.Fatalf("reconnect storm: losing side dialed %d times in 3s (cadence 50ms) — MaintainPeer returned immediately instead of waiting on the surviving edge", n)
	}
	t.Logf("losing side dialed %d times in 3s — edge held without flapping", n)
}

// newTaggedCore is newCore with a per-side log prefix so interleaved output
// from both cores can be told apart while debugging the dial loop.
func newTaggedCore(t *testing.T, tag, id, addr string) *Core {
	t.Helper()
	db := openTestDB(t)
	card := ledger.Card{
		Device:        id,
		ResourceClass: "Standard",
		Native:        []ledger.NativeAbility{{ID: "sys:info", Command: "uname"}},
		Capacity:      ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil)).With("side", tag)
	c := NewCore(db, id, card, 5, logger, config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	return c
}
