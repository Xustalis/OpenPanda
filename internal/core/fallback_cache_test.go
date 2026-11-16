// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

func TestFallbackRelayReloadDuringResolution(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "relay-reload")
	setRelay := func(id string) {
		c.SetNetworkPolicy(config.NetworkConfig{Peers: []config.PeerEntry{{
			Addr: "punch:" + id, RelayPriority: config.RelayPriorityFallback,
		}}})
	}
	setRelay("old-relay")

	// Hold the only connection until the old policy's resolver is waiting
	// on its store read, then install the new policy before letting it finish.
	c.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, err := c.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	waits := c.db.Stats().WaitCount
	done := make(chan struct{})
	go func() {
		c.fallbackRelays()
		close(done)
	}()
	for c.db.Stats().WaitCount == waits {
		select {
		case <-ctx.Done():
			t.Fatal("resolver did not reach the blocked store read")
		case <-time.After(time.Millisecond):
		}
	}
	setRelay("new-relay")
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("old resolver did not finish")
	}

	if got := c.fallbackRelays(); len(got) != 1 || got[0] != "new-relay" {
		t.Fatalf("relays after reload = %v, want [new-relay]", got)
	}
}

func TestFallbackRelayCacheRefreshesOnPolicyReload(t *testing.T) {
	c := newCoreOnDB(t, openTestDB(t), "relay-binding")
	policy := config.NetworkConfig{Peers: []config.PeerEntry{{
		Addr: "relay.example:7836", RelayPriority: config.RelayPriorityFallback,
	}}}
	c.SetNetworkPolicy(policy)
	if err := ledger.RecordPeerAddr(c.db, "relay.example:7836", "first-relay"); err != nil {
		t.Fatal(err)
	}
	first := c.fallbackRelays()
	if len(first) != 1 || first[0] != "first-relay" {
		t.Fatal(first)
	}
	first[0] = "caller mutation"
	if err := ledger.RecordPeerAddr(c.db, "relay.example:7836", "replacement-relay"); err != nil {
		t.Fatal(err)
	}
	if got := c.fallbackRelays(); len(got) != 1 || got[0] != "first-relay" {
		t.Fatalf("cached binding was not retained safely: %v", got)
	}
	// Even an unchanged address list must invalidate cached identity bindings.
	c.SetNetworkPolicy(policy)
	if got := c.fallbackRelays(); len(got) != 1 || got[0] != "replacement-relay" {
		t.Fatalf("binding after reload = %v, want [replacement-relay]", got)
	}
}
