package core

import (
	"context"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

// newCoreWithResources builds a Core whose card declares hardware as well as an
// ability — the deployment shape of R2, where a node is capable of starting work
// it has no hardware to finish.
func newCoreWithResources(t *testing.T, id string, native ledger.NativeAbility,
	res ledger.ResourceProfile, maxConcurrent int) *Core {
	t.Helper()
	card := ledger.Card{
		Device:          id,
		ResourceClass:   "Standard",
		Native:          []ledger.NativeAbility{native},
		Capacity:        ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: maxConcurrent},
		ResourceProfile: res,
	}
	c := NewCore(openTestDB(t), id, card, 5, testLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	return c
}

// TestResourceProfileCrossesTheWire is [P0-4] end to end at the node level: the
// Orange Pi learns over hello that the Windows box has a GPU, and that fact is
// what keeps a training task off the Pi.
//
// It is deliberately not a scheduler unit test. The routing logic is covered in
// internal/scheduler; what can only be checked here is the chain that feeds it —
// card → summary() → hello → UpsertRemote → employee_cache → ledger.Query. Before
// v0.0.6 that chain had exactly one break, UpsertRemote passing "" for the
// resource column, and a unit test on either side of it would still have passed.
func TestResourceProfileCrossesTheWire(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The Pi can launch a training run (the ability is real) and has no VRAM.
	pi := newCoreWithResources(t, "pi", ledger.NativeAbility{
		ID: "gpu:train", Command: "sh", Args: []string{"-c", "true"}, Tier: 1,
	}, ledger.ResourceProfile{CPU: 4, RAMGB: 2, GPUVRAMGB: 0, DurationHint: "short"}, 1)
	win := newCoreWithResources(t, "win", ledger.NativeAbility{
		ID: "gpu:train", Command: "sh", Args: []string{"-c", "true"}, Tier: 1,
	}, ledger.ResourceProfile{CPU: 16, RAMGB: 32, GPUVRAMGB: 12, DurationHint: "long"}, 3)
	pi.SetWorkDir(t.TempDir())
	win.SetWorkDir(t.TempDir())

	startPair(t, ctx, pi, win, "127.0.0.1:17994", "127.0.0.1:17995")

	// The peer's declared hardware arrived with its capability summary.
	nodes := pi.onlineEmployees(ctx)
	var peer *ledger.Node
	for i := range nodes {
		if nodes[i].ID == "win" {
			peer = &nodes[i]
		}
	}
	if peer == nil {
		t.Fatalf("pi does not know win: %+v", nodes)
	}
	if peer.ResourceProfile.GPUVRAMGB != 12 {
		t.Fatalf("win's VRAM = %d, want 12 (resource profile dropped on the wire)",
			peer.ResourceProfile.GPUVRAMGB)
	}

	// And the requirement is decisive: the same task, routed by the same node,
	// with and without a declared GPU need.
	train := `{"cpu":4,"ram_gb":8,"gpu_vram_gb":8,"duration_hint":"long"}`
	if got := pi.routeTargetForTest(ctx, []string{"gpu:train"}, train); got != "win" {
		t.Errorf("training task routed to %q, want win (the Pi has 0 VRAM)", got)
	}
	if got := pi.routeTargetForTest(ctx, []string{"gpu:train"}, ""); got != "pi" {
		t.Errorf("task with no declared requirement routed to %q, want pi (stay home)", got)
	}
}

// routeTargetForTest asks this node the routing question the way Submit does and
// names the winner: its own id for a local decision, the peer's for a forward,
// empty for a decline. It exists so the assertions above read as "where does this
// task go" without a full delegation round trip, which is covered elsewhere.
func (c *Core) routeTargetForTest(ctx context.Context, requires []string, resourceJSON string) string {
	d := scheduler.Route(c.nodeID, []string{c.nodeID}, c.onlineEmployees(ctx), c.localMatch(),
		requires, resourceRequirement(resourceJSON), "")
	switch d.Action {
	case scheduler.ActionLocal:
		return c.nodeID
	case scheduler.ActionForward:
		return d.Target
	default:
		return ""
	}
}
