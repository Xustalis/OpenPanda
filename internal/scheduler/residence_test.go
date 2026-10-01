package scheduler

import (
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// residentEmployees builds a directory with a capable self row and one equally
// capable peer that advertises a checkout of project "p" — the minimal scene
// for the residence term: identical nodes, differing only in where the code
// already lives.
func residentEmployees() []ledger.Node {
	return []ledger.Node{
		{
			ID: "self", Status: "online", SchedulerTier: 5,
			Native:   []ledger.NativeAbility{{ID: "code:modify"}},
			Capacity: ledger.Capacity{MaxConcurrent: 4, CurrentTasks: 0},
			LastSeen: time.Now().Unix(),
		},
		{
			ID: "peer-res", Status: "online", SchedulerTier: 5,
			Native:   []ledger.NativeAbility{{ID: "code:modify"}},
			Capacity: ledger.Capacity{MaxConcurrent: 4, CurrentTasks: 0},
			Projects: []string{"p"},
			LastSeen: time.Now().Unix(),
		},
	}
}

// TestResidenceWinsProjectRoute is the routing-honesty contract: when self and
// a peer are equally capable, a task bound to project "p" goes to the node
// whose checkout already holds it — the tree costs nothing to move there, and
// 0.2 of residence beats 0.15 of local bias.
func TestResidenceWinsProjectRoute(t *testing.T) {
	d := RouteP("self", []string{"self"}, residentEmployees(),
		func([]string) bool { return true }, []string{"code:modify"},
		ledger.ResourceProfile{}, "", "p")
	if d.Action != ActionForward || d.Target != "peer-res" {
		t.Fatalf("decision = %+v, want forward to peer-res", d)
	}
}

// TestResidenceTermSilentWithoutProject verifies the term is inert for tasks
// with no project: the same nodes route local, the way they always have.
func TestResidenceTermSilentWithoutProject(t *testing.T) {
	d := RouteP("self", []string{"self"}, residentEmployees(),
		func([]string) bool { return true }, []string{"code:modify"},
		ledger.ResourceProfile{}, "", "")
	if d.Action != ActionLocal {
		t.Fatalf("decision = %+v, want local (no project = no residence)", d)
	}
}

// TestResidenceIsScoreNotGate proves residence is a term, not a filter: when
// the resident peer cannot do the work, a non-resident capable node still
// takes it — the tree can travel even when it doesn't already live there.
func TestResidenceIsScoreNotGate(t *testing.T) {
	emps := residentEmployees()
	emps[1].Native = nil // resident peer loses the ability entirely
	d := RouteP("self", []string{"self"}, emps,
		func([]string) bool { return true }, []string{"code:modify"},
		ledger.ResourceProfile{}, "", "p")
	if d.Action != ActionLocal {
		t.Fatalf("decision = %+v, want local (incapable resident skipped)", d)
	}
}

// TestScoreBreakdownResidence exposes the term in the orbit breakdown: the
// resident shows 1.0, the non-resident 0 — "the code already lives there" must
// be readable out of the score, not hidden inside it.
func TestScoreBreakdownResidence(t *testing.T) {
	now := time.Now().Unix()
	emps := residentEmployees()
	res := scoreBreakdown(emps[1], now, "", "p")
	if res.ProjectResidence != 1 {
		t.Fatalf("resident breakdown = %+v, want residence 1", res)
	}
	non := scoreBreakdown(emps[0], now, "", "p")
	if non.ProjectResidence != 0 {
		t.Fatalf("non-resident breakdown = %+v, want residence 0", non)
	}
	if res.Total <= non.Total {
		t.Fatalf("resident total %f <= non-resident %f", res.Total, non.Total)
	}
}
