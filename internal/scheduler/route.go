package scheduler

import (
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
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
func Route(self string, chain []string, employees []ledger.Node, localMatch func(required []string) bool, required []string, req ledger.ResourceProfile, preferred string) Decision {
	return RouteAt(self, chain, employees, localMatch, required, req, preferred, time.Now().Unix())
}

// IsSelfRow reports whether the capability-directory row id names the same
// physical node as the routing participant self. self may be an ephemeral
// identity — core.EphemeralNodeID appends an 8-hex random suffix to the
// stable id ("macbook-1f3a2b4c") so a short-lived ask session never collides
// with the daemon on the bus, while the directory row carries the stable id
// the daemon registered ("macbook", or "macbook@vm-…" for a VM). Without the
// suffix strip, the ask session never recognizes its own row: haveSelf stays
// false, RouteAt falls into its no-row "stay local" default, and load
// balancing silently degrades to run-everything-here.
func IsSelfRow(id, self string) bool {
	if id == self {
		return true
	}
	base, ok := EphemeralBase(self)
	return ok && id == base
}

// SameRuntimeIdentity reports whether two authenticated routing participants
// belong to the same configured runtime node. Unlike IsSelfRow, it is symmetric
// and accepts two ephemeral siblings; it is intentionally reserved for narrow
// restart-continuity checks such as approval ownership and task_resume.
func SameRuntimeIdentity(a, b string) bool {
	if a == b {
		return true
	}
	aBase, aEphemeral := EphemeralBase(a)
	bBase, bEphemeral := EphemeralBase(b)
	switch {
	case aEphemeral && bEphemeral:
		return aBase == bBase
	case aEphemeral:
		return aBase == b
	case bEphemeral:
		return bBase == a
	default:
		return false
	}
}

// EphemeralBase validates and strips the random suffix produced by
// core.EphemeralNodeID. Stable names are returned unchanged with ok=false.
// The strip is purely syntactic — a stable id that itself ends in "-"+8hex
// would be mistaken for an ephemeral sibling of its own base, so the daemon
// refuses such a node name at startup (cmd/panda main.go).
func EphemeralBase(id string) (base string, ok bool) {
	i := strings.LastIndex(id, "-")
	if i <= 0 || len(id)-i-1 != 8 {
		return id, false
	}
	for _, c := range id[i+1:] {
		if !isHexDigit(c) {
			return id, false
		}
	}
	return id[:i], true
}

func isHexDigit(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// localBias is how much this node's own score is favoured over an equally-loaded
// peer's. It is the cost of a hop expressed in the same units as the score:
// moving a task means shipping its context and pulling its artifacts back, so an
// idle local node must beat an idle peer, and a single task on an otherwise quiet
// network must never bounce across devices for nothing. 0.15 is a little more
// than one slot of an 8-wide node's resource_efficiency (0.4/8 = 0.05) and a
// little less than two, so a peer wins once it is meaningfully freer — which is
// exactly the load-balancing case: half-busy self 0.40+0.15 loses to idle peer
// 0.50+0.20·tier.
const localBias = 0.15

// RouteAt decides where a task with the given required abilities should run,
// scoring candidates as of now (Unix seconds).
//
// This node is one candidate among many, not a short-circuit. Before v0.0.6 the
// first line was "if I can do it, I do it", which made load balancing impossible
// by construction: the entry node swallowed every task it was nominally capable
// of, however busy it already was, and a burst of tasks published at once queued
// behind each other on one device while its peers sat idle. So local capability
// is scored like everything else and given localBias, which keeps the single-task
// case at home without letting a saturated node hoard a queue.
//
// A task's declared hardware requirement (req) is a hard filter on both sides,
// unlike capacity, which is a soft score. The difference is what happens when
// every candidate fails: a busy network should queue, and does, because
// MaxConcurrent is enforced by the local queue scheduler rather than here — but a
// network with no GPU genuinely cannot train a model, and saying so is better
// than parking the task forever. Undeclared hardware passes (see Node.Fits).
//
// Otherwise the shape is unchanged: a user-named node (preferred) that is online,
// not already on the chain, and capable is honoured over scored ranking; ranking
// is the DCPS weighted score (design §6.3: resource_efficiency 0.4 +
// user_priority 0.3 + scheduler_tier 0.2 + wait_time 0.1) discounted by the
// TMB heartbeat-freshness weight (exp decay, 30-minute half-life); with no
// capable peer it falls back to a sub-scheduler (tier > 1) that can route the
// task further downstream even though it cannot execute it; only when neither
// exists does it decline.
func RouteAt(self string, chain []string, employees []ledger.Node, localMatch func(required []string) bool, required []string, req ledger.ResourceProfile, preferred string, now int64) Decision {
	seen := make(map[string]bool, len(chain))
	for _, n := range chain {
		seen[n] = true
	}

	// This node's own directory row, which carries the capacity its heartbeat
	// publishes and is therefore what makes self comparable to a peer. A node
	// with no row of its own (a fresh process, a test fixture) still routes:
	// haveSelf just stays false and local capability wins outright.
	var selfNode ledger.Node
	haveSelf := false

	var matching, subs []ledger.Node
	for _, n := range employees {
		if IsSelfRow(n.ID, self) {
			selfNode, haveSelf = n, true
			continue
		}
		if n.Status != "online" || seen[n.ID] {
			continue
		}
		if !n.Fits(req) {
			// Declared hardware says this peer cannot run the work. Nor can it be
			// trusted to relay it: a sub-scheduler that forwards onward would be
			// choosing among the same directory this node already sees.
			continue
		}
		if n.Matches(required) || len(required) == 0 {
			matching = append(matching, n)
		} else if n.SchedulerTier > 1 {
			// A non-matching node can still forward onward if it is a
			// sub-scheduler (Standard/Full), not a leaf worker (Micro).
			subs = append(subs, n)
		}
	}

	// The local candidate: capable by the commander router, and — when this node
	// declares its hardware — big enough for the work. This is R2: the Orange Pi
	// has the ability to start a training run and 0 GiB of VRAM to finish it, so
	// it declines itself and looks outward instead of failing at the last moment.
	//
	// An empty requirement is no constraint rather than an unsatisfiable one: a
	// plan stage may declare only hardware (plan.Validate asks for an intent, not
	// an ability), and Matches of an empty list matches nobody. Reading it as
	// "nobody can do this" would send such a stage to a sub-scheduler or decline
	// it, when in fact every node can run it and only the hardware filter has an
	// opinion.
	canLocal := (len(required) == 0 || localMatch(required)) && (!haveSelf || selfNode.Fits(req))

	// A named node is authoritative when it can take the task; otherwise fall
	// through to scored ranking so the task still runs somewhere capable. Match
	// on either the node id (the routing key) or its display name, since the
	// entry model sees the latter in the device summary.
	if preferred != "" {
		// Match the participant id (self) AND the directory row id
		// (selfNode.ID): the two differ when self is an ephemeral ask-session
		// identity ("macbook-1f3a2b4c") while the row — and the node name the
		// user actually names — carries the stable id ("macbook"). Naming the
		// stable id must still land the task at home.
		if canLocal && (preferred == self || strings.EqualFold(preferred, self) ||
			(haveSelf && (preferred == selfNode.ID || strings.EqualFold(preferred, selfNode.ID) ||
				preferred == selfNode.Name || strings.EqualFold(preferred, selfNode.Name)))) {
			return Decision{Action: ActionLocal}
		}
		for _, n := range matching {
			if n.ID == preferred || n.Name == preferred || strings.EqualFold(n.ID, preferred) || strings.EqualFold(n.Name, preferred) {
				return Decision{Action: ActionForward, Target: n.ID}
			}
		}
	}

	target, peerScore := pickBestScored(matching, now, preferred)
	if canLocal {
		if !haveSelf {
			// No row of our own to score against. Absence of evidence about our
			// own load is not evidence of being busy, and every real node
			// registers itself, so this is the fixture case: stay local.
			return Decision{Action: ActionLocal}
		}
		if localScore := score(selfNode, now, preferred) + localBias; target == "" || localScore >= peerScore {
			return Decision{Action: ActionLocal}
		}
	}
	if target != "" {
		return Decision{Action: ActionForward, Target: target}
	}
	// §9.3 graph routing: no capable direct peer — walk the link-state graph
	// the nodes' neighbor advertisements build (G=(V,E)) and forward to the
	// first hop of the shortest path that reaches a capable node. The forward
	// is an ordinary delegation: the receiving node runs its own Route and
	// picks the next hop, so the path is computed hop-by-hop from the same
	// directory view — which is also what keeps it loop-safe (AppendChain
	// refuses revisits along the way).
	if haveSelf {
		if hop := graphFirstHop(selfNode, employees, seen, required, req); hop != "" {
			return Decision{Action: ActionForward, Target: hop}
		}
	}
	if sub, _ := pickBestScored(subs, now, preferred); sub != "" {
		return Decision{Action: ActionForward, Target: sub}
	}
	return Decision{
		Action: ActionDecline,
		Reason: declineReason(required, req),
	}
}

// graphFirstHop BFS-searches the link-state graph from self toward the
// nearest online node that can run the task (ability match + hardware fit),
// returning the first hop of that shortest path — the only hop this node
// needs, since every relay re-runs Route on arrival. Edges are the nodes'
// advertised Neighbors, restricted to rows that are online right now: a
// stale advertisement names a dead link, and routing a task onto it parks it
// in an outbox whose flush never comes.
func graphFirstHop(self ledger.Node, employees []ledger.Node, seen map[string]bool, required []string, req ledger.ResourceProfile) string {
	online := make(map[string]ledger.Node, len(employees))
	for _, n := range employees {
		online[n.ID] = n
	}
	type item struct{ id, first string }
	visited := map[string]bool{self.ID: true}
	var queue []item
	for _, nb := range self.Neighbors {
		if seen[nb] || visited[nb] {
			continue
		}
		if _, ok := online[nb]; !ok {
			continue
		}
		visited[nb] = true
		queue = append(queue, item{id: nb, first: nb})
	}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		n := online[it.id]
		if (len(required) == 0 || n.Matches(required)) && n.Fits(req) {
			return it.first
		}
		for _, nb := range n.Neighbors {
			if seen[nb] || visited[nb] {
				continue
			}
			if _, ok := online[nb]; !ok {
				continue
			}
			visited[nb] = true
			queue = append(queue, item{id: nb, first: it.first})
		}
	}
	return ""
}

// declineReason distinguishes "nobody has this ability" from "nobody has this
// much hardware". The two have different fixes — install a tool versus add a
// node — and the reason string is what the user reads on a declined task.
func declineReason(required []string, req ledger.ResourceProfile) string {
	if req.Declared() {
		return "no node matches required abilities [" + strings.Join(required, ",") +
			"] with the declared resources"
	}
	return "no capability matches required: " + strings.Join(required, ",")
}
