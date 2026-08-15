package scheduler

import (
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
// picks the best online peer that matches and is not already on the chain (so a
// forwarded task never loops back through an earlier hop): highest scheduler
// tier first, then lowest id as a deterministic tiebreak. If no peer matches,
// it falls back to a sub-scheduler (tier > 1) — a peer that can route the task
// further downstream even though it cannot execute it itself. Only when neither
// exists does it decline.
//
// MVP scope: this is deterministic capability + tier matching, not full scored
// ranking — capacity/load/cost weighting (design doc §6.3) is a later sprint.
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

	if target := pickBest(matching); target != "" {
		return Decision{Action: ActionForward, Target: target}
	}
	if target := pickBest(subs); target != "" {
		return Decision{Action: ActionForward, Target: target}
	}
	return Decision{
		Action: ActionDecline,
		Reason: "no capability matches required: " + strings.Join(required, ","),
	}
}

// pickBest returns the id of the best node from a non-empty slice: highest
// scheduler tier first (Full over Standard over Micro), then lowest id for a
// deterministic tiebreak. This is a first step toward the §6.3 scored ranking —
// capacity, load, and cost are not yet tracked, but preferring a more capable
// node beats an arbitrary lowest-id pick.
func pickBest(nodes []ledger.Node) string {
	if len(nodes) == 0 {
		return ""
	}
	best := nodes[0]
	for _, n := range nodes[1:] {
		if n.SchedulerTier > best.SchedulerTier || (n.SchedulerTier == best.SchedulerTier && n.ID < best.ID) {
			best = n
		}
	}
	return best.ID
}
