package scheduler

import (
	"strings"
	"time"

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
// It is RouteAt evaluated at the current time; tests and callers that need a
// deterministic freshness discount use RouteAt.
func Route(self string, chain []string, employees []ledger.Node, localMatch func(required []string) bool, required []string, preferred string) Decision {
	return RouteAt(self, chain, employees, localMatch, required, preferred, time.Now().Unix())
}

// RouteAt decides where a task with the given required abilities should run,
// scoring candidates as of now (Unix seconds).
//
// Local capability wins first — native > agent > manual, as judged by the
// injected localMatch predicate (the core's commander router). Otherwise, a
// user-named node (preferred) that is online, not already on the chain, and
// matches the required abilities is honored over scored ranking: when the user
// says "run it on the Orange Pi", the task goes there even if a higher-scoring
// peer also advertises the ability. Absent a preference, candidates are ranked
// by the DCPS weighted score (design §6.3: resource_efficiency 0.4 +
// scheduler_tier 0.2 + wait_time 0.1, with user_priority handled by the
// preferred short-circuit) discounted by the TMB heartbeat-freshness weight
// (exp decay, 30-minute half-life). If no peer matches, it falls back to a
// sub-scheduler (tier > 1) — a peer that can route the task further downstream
// even though it cannot execute it itself. Only when neither exists does it
// decline.
func RouteAt(self string, chain []string, employees []ledger.Node, localMatch func(required []string) bool, required []string, preferred string, now int64) Decision {
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

	// A named node is authoritative when it can take the task; otherwise fall
	// through to scored ranking so the task still runs somewhere capable. Match
	// on either the node id (the routing key) or its display name, since the
	// entry model sees the latter in the device summary.
	if preferred != "" {
		for _, n := range matching {
			if n.ID == preferred || n.Name == preferred {
				return Decision{Action: ActionForward, Target: n.ID}
			}
		}
	}

	if target := pickBest(matching, now); target != "" {
		return Decision{Action: ActionForward, Target: target}
	}
	if target := pickBest(subs, now); target != "" {
		return Decision{Action: ActionForward, Target: target}
	}
	return Decision{
		Action: ActionDecline,
		Reason: "no capability matches required: " + strings.Join(required, ","),
	}
}
