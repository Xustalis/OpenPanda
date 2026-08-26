package scheduler

import (
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// alwaysLocal is the commander router saying "yes, I have that ability" — the
// premise of every test in this file, since what is under test is what happens
// *after* local capability is established rather than whether it exists.
func alwaysLocal([]string) bool { return true }

// TestRouteBalancesOntoIdlePeer is R5: publishing several tasks at once must
// spread them, not pile them on the entry node. Before v0.0.6 RouteAt opened
// with "if I can do it, I do it", so a node that was already saturated still
// swallowed every task it was nominally capable of and its idle peers never saw
// one. Scoring self as a candidate is what fixes it, and this is the case that
// tells the fix from the bug: both nodes can do the work, so the only thing that
// can move the task is load.
func TestRouteBalancesOntoIdlePeer(t *testing.T) {
	now := time.Now().Unix()
	self := ledger.Node{ID: "pi", Name: "pi", Status: "online", LastSeen: now, SchedulerTier: 5,
		Native:   []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 5, CurrentTasks: 3}} // busy
	peer := ledger.Node{ID: "mac", Name: "mac", Status: "online", LastSeen: now, SchedulerTier: 5,
		Native:   []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 5, CurrentTasks: 0}} // idle

	d := RouteAt("pi", []string{"pi"}, []ledger.Node{self, peer}, alwaysLocal,
		[]string{"build"}, ledger.ResourceProfile{}, "", now)
	if d.Action != ActionForward || d.Target != "mac" {
		t.Fatalf("busy self vs idle peer: %+v, want forward to mac", d)
	}
}

// TestRouteKeepsSingleTaskLocal is the other half of the same knob: localBias
// exists so the balancing above does not turn into needless device-hopping. With
// nothing running anywhere, an equivalent peer is not better than home, and
// shipping the context across the network to find that out is pure cost.
func TestRouteKeepsSingleTaskLocal(t *testing.T) {
	now := time.Now().Unix()
	self := ledger.Node{ID: "pi", Name: "pi", Status: "online", LastSeen: now, SchedulerTier: 5,
		Native:   []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 5},
	}
	peer := self
	peer.ID, peer.Name = "mac", "mac"

	d := RouteAt("pi", []string{"pi"}, []ledger.Node{self, peer}, alwaysLocal,
		[]string{"build"}, ledger.ResourceProfile{}, "", now)
	if d.Action != ActionLocal {
		t.Fatalf("idle self vs equivalent idle peer: %+v, want local", d)
	}

	// And one task in flight is not enough to give up the local advantage: an
	// 8-wide node loses 0.05 of resource_efficiency per occupied slot, which is
	// less than localBias. Two or three are, which is the case above.
	self.Capacity = ledger.Capacity{MaxConcurrent: 8, CurrentTasks: 1}
	d = RouteAt("pi", []string{"pi"}, []ledger.Node{self, peer}, alwaysLocal,
		[]string{"build"}, ledger.ResourceProfile{}, "", now)
	if d.Action != ActionLocal {
		t.Fatalf("one task in flight: %+v, want local (bias exceeds one slot)", d)
	}
}

// TestRouteResourcesKeepTrainingOffThePi is R2, at the routing layer: the entry
// node has the ability to launch a training run and no GPU to finish it, and the
// requirement travels with the task rather than being a property of whoever
// happened to receive it. The Pi must decline itself while it is still cheap to
// do so — before the context is packed and an agent is started.
func TestRouteResourcesKeepTrainingOffThePi(t *testing.T) {
	now := time.Now().Unix()
	pi := ledger.Node{ID: "pi", Name: "pi", Status: "online", LastSeen: now, SchedulerTier: 5,
		Native:          []ledger.NativeAbility{{ID: "gpu:train"}},
		Capacity:        ledger.Capacity{MaxConcurrent: 5},
		ResourceProfile: ledger.ResourceProfile{CPU: 4, RAMGB: 4, GPUVRAMGB: 0}}
	win := ledger.Node{ID: "win", Name: "win", Status: "online", LastSeen: now, SchedulerTier: 5,
		Native:          []ledger.NativeAbility{{ID: "gpu:train"}},
		Capacity:        ledger.Capacity{MaxConcurrent: 5, CurrentTasks: 4}, // busier, and still the answer
		ResourceProfile: ledger.ResourceProfile{CPU: 16, RAMGB: 32, GPUVRAMGB: 12}}
	train := ledger.ResourceProfile{GPUVRAMGB: 8}

	d := RouteAt("pi", []string{"pi"}, []ledger.Node{pi, win}, alwaysLocal,
		[]string{"gpu:train"}, train, "", now)
	if d.Action != ActionForward || d.Target != "win" {
		t.Fatalf("GPU task on a GPU-less entry node: %+v, want forward to win", d)
	}

	// The same requirement with no capable node anywhere declines, and says why:
	// "nobody has the ability" and "nobody has the hardware" have different fixes.
	d = RouteAt("pi", []string{"pi"}, []ledger.Node{pi}, alwaysLocal,
		[]string{"gpu:train"}, train, "", now)
	if d.Action != ActionDecline {
		t.Fatalf("no node fits: %+v, want decline", d)
	}
	if d.Reason == "" {
		t.Errorf("a declined task must carry a reason")
	}

	// Without a declared requirement the same task stays home: the requirement is
	// the only thing that moved it, so an absent one must not.
	d = RouteAt("pi", []string{"pi"}, []ledger.Node{pi, win}, alwaysLocal,
		[]string{"gpu:train"}, ledger.ResourceProfile{}, "", now)
	if d.Action != ActionLocal {
		t.Fatalf("undeclared requirement: %+v, want local", d)
	}
}

// TestRoutePreferredSelfStaysLocal guards the seam the scored-candidate change
// opened: "run it here" used to be answered by the local short-circuit before
// the preferred block was ever reached. Now that local is scored, a user naming
// this node must still be obeyed rather than out-scored by an idler peer.
func TestRoutePreferredSelfStaysLocal(t *testing.T) {
	now := time.Now().Unix()
	self := ledger.Node{ID: "pi", Name: "Orange Pi", Status: "online", LastSeen: now, SchedulerTier: 1,
		Native:   []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 2, CurrentTasks: 2}}
	peer := ledger.Node{ID: "mac", Name: "mac", Status: "online", LastSeen: now, SchedulerTier: 10,
		Native:   []ledger.NativeAbility{{ID: "build"}},
		Capacity: ledger.Capacity{MaxConcurrent: 8}}
	nodes := []ledger.Node{self, peer}

	for _, named := range []string{"pi", "Orange Pi"} {
		d := RouteAt("pi", []string{"pi"}, nodes, alwaysLocal,
			[]string{"build"}, ledger.ResourceProfile{}, named, now)
		if d.Action != ActionLocal {
			t.Errorf("preferred %q: %+v, want local", named, d)
		}
	}

	// Naming this node does not override the hardware check, though: consent to a
	// slower device is not consent to a device that cannot run the work at all.
	self.ResourceProfile = ledger.ResourceProfile{CPU: 4, RAMGB: 4}
	nodes = []ledger.Node{self, peer}
	d := RouteAt("pi", []string{"pi"}, nodes, alwaysLocal,
		[]string{"build"}, ledger.ResourceProfile{GPUVRAMGB: 8}, "pi", now)
	if d.Action == ActionLocal {
		t.Errorf("preferred self without the hardware: %+v, want anything but local", d)
	}
}
