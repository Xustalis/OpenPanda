package scheduler

import (
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// TestFreshnessDecay verifies the TMB delayed-discount weight: a 5-minute-old
// heartbeat keeps most of its weight, a 2-hour-old one is heavily discounted,
// and missing data is neutral rather than fatal.
func TestFreshnessDecay(t *testing.T) {
	now := time.Now().Unix()
	fresh := Freshness(now-5*60, now)
	stale := Freshness(now-2*3600, now)
	unknown := Freshness(0, now)

	if unknown != 1 {
		t.Fatalf("no-data freshness = %v, want neutral 1", unknown)
	}
	if fresh < 0.85 || fresh > 0.95 {
		t.Fatalf("5-min freshness = %v, want ≈0.89", fresh)
	}
	if stale < 0.03 || stale > 0.10 {
		t.Fatalf("2-hour freshness = %v, want ≈0.06", stale)
	}
	if !(fresh > stale) {
		t.Fatalf("freshness must decrease with age: fresh=%v stale=%v", fresh, stale)
	}
	if f := Freshness(now, now); f != 1 {
		t.Fatalf("just-seen freshness = %v, want 1", f)
	}
}

// TestScoredRankingPrefersFreshAndFree verifies the DCPS weighted score:
// between equal-tier matching nodes, the fresher/less-loaded one wins; a stale
// heartbeat can demote even a higher-tier node.
func TestScoredRankingPrefersFreshAndFree(t *testing.T) {
	now := time.Now().Unix()
	mk := func(id string, tier int, lastSeen int64, maxConc, current int) ledger.Node {
		return ledger.Node{
			ID: id, Name: id, Status: "online", LastSeen: lastSeen, SchedulerTier: tier,
			Native:   []ledger.NativeAbility{{ID: "build"}},
			Capacity: ledger.Capacity{MaxConcurrent: maxConc, CurrentTasks: current},
		}
	}
	neverLocal := func([]string) bool { return false }

	// Same tier: the idle node beats the saturated one.
	d := RouteAt("self", []string{"self"}, []ledger.Node{
		mk("busy", 5, now, 4, 4),
		mk("idle", 5, now, 4, 0),
	}, neverLocal, []string{"build"}, ledger.ResourceProfile{}, "", now)
	if d.Action != ActionForward || d.Target != "idle" {
		t.Fatalf("efficiency ranking: %+v, want forward to idle", d)
	}

	// A higher-tier node with a 2-hour-stale heartbeat loses to a fresh
	// lower-tier node (TMB discount outweighs the tier weight at that age).
	d = RouteAt("self", []string{"self"}, []ledger.Node{
		mk("stale-full", 10, now-2*3600, 4, 0),
		mk("fresh-micro", 1, now, 4, 0),
	}, neverLocal, []string{"build"}, ledger.ResourceProfile{}, "", now)
	if d.Action != ActionForward || d.Target != "fresh-micro" {
		t.Fatalf("freshness ranking: %+v, want forward to fresh-micro", d)
	}

	// Determinism: identical scores break ties by lowest id, so every node
	// ranks the candidate set identically (no routing loops).
	d = RouteAt("self", []string{"self"}, []ledger.Node{
		mk("b-node", 5, now, 4, 0),
		mk("a-node", 5, now, 4, 0),
	}, neverLocal, []string{"build"}, ledger.ResourceProfile{}, "", now)
	if d.Target != "a-node" {
		t.Fatalf("tie-break: %+v, want a-node", d)
	}
}

// TestPreferredStillAuthoritative: a user-named node wins over scoring.
func TestPreferredStillAuthoritative(t *testing.T) {
	now := time.Now().Unix()
	d := RouteAt("self", []string{"self"}, []ledger.Node{
		{ID: "strong", Name: "strong", Status: "online", LastSeen: now, SchedulerTier: 10,
			Native: []ledger.NativeAbility{{ID: "build"}}, Capacity: ledger.Capacity{MaxConcurrent: 8}},
		{ID: "named", Name: "named", Status: "online", LastSeen: now - 3600, SchedulerTier: 1,
			Native: []ledger.NativeAbility{{ID: "build"}}, Capacity: ledger.Capacity{MaxConcurrent: 1, CurrentTasks: 1}},
	}, func([]string) bool { return false }, []string{"build"}, ledger.ResourceProfile{}, "named", now)
	if d.Action != ActionForward || d.Target != "named" {
		t.Fatalf("preferred node: %+v, want forward to named", d)
	}
}
