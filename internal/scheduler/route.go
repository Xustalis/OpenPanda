package scheduler

import (
	"sort"
	"strings"

	"github.com/xenith/panda/internal/ledger"
)

// Action is the routing outcome for a task arriving at this node.
type Action string

const (
	// ActionLocal means this node can execute the task itself.
	ActionLocal Action = "local"
	// ActionForward means this node cannot execute the task but knows a peer
	// that can; it should forward the delegation along.
	ActionForward Action = "forward"
	// ActionDecline means no node in the known network (including this one)
	// matches the task's required abilities.
	ActionDecline Action = "decline"
)

// Decision is the result of Route.
type Decision struct {
	Action Action
	Target string // ActionForward: the peer node id to forward to
	Reason string // ActionDecline: why no route exists
}

// Route decides where a task with the given required abilities should run.
//
// Local capability wins first — native > agent > manual, as judged by the
// injected localMatch predicate (the core's commander router). Otherwise it
// picks the lowest-id online peer that matches and is not already on the chain
// (so a forwarded task never loops back through an earlier hop). If no peer
// matches, it falls back to a sub-scheduler (tier > 1) — a peer that can route
// the task further downstream even though it cannot execute it itself. Only
// when neither exists does it decline.
//
// MVP scope: this is deterministic capability matching, not scored ranking —
// priority scoring (design doc §6.3) is a later sprint.
func Route(self string, chain []string, employees []ledger.Node, localMatch func(required []string) bool, required []string) Decision {
	if localMatch(required) {
		return Decision{Action: ActionLocal}
	}

	seen := make(map[string]bool, len(chain))
	for _, n := range chain {
		seen[n] = true
	}

	var matching, subs []ledger.Node
	for _, n := range employees {
		if n.ID == self || n.Status != "online" || seen[n.ID] {
			continue
		}
		if n.Matches(required) {
			matching = append(matching, n)
		} else if n.SchedulerTier > 1 {
			// A non-matching node can still forward onward if it is a
			// sub-scheduler (Standard/Full), not a leaf worker (Micro).
			subs = append(subs, n)
		}
	}

	if target := pickLowestID(matching); target != "" {
		return Decision{Action: ActionForward, Target: target}
	}
	if target := pickLowestID(subs); target != "" {
		return Decision{Action: ActionForward, Target: target}
	}
	return Decision{
		Action: ActionDecline,
		Reason: "no capability matches required: " + strings.Join(required, ","),
	}
}

// pickLowestID returns the lowest-id node from a non-empty slice, or "" if the
// slice is empty. Deterministic ordering keeps routing reproducible in tests.
func pickLowestID(nodes []ledger.Node) string {
	if len(nodes) == 0 {
		return ""
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	return nodes[0].ID
}
