package scheduler

import (
	"math"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// Scored-ranking weights (design doc §6.3, DCPS soft-penalty mapping):
// resource_efficiency 0.4 + user_priority 0.3 + scheduler_tier 0.2 +
// wait_time 0.1. The sum is the node's raw desirability; the TMB freshness
// factor then discounts it by heartbeat age.
const (
	wResourceEfficiency = 0.4
	wUserPriority       = 0.3
	wSchedulerTier      = 0.2
	wWaitTime           = 0.1
)

// freshnessHalfLife is the λ of the TMB delayed-discount attention mapping
// (design doc §2.2: "心跳的时间衰减权重，5 分钟前比 2 小时前更有参考价值").
// With a 30-minute half-life, a 5-minute-old heartbeat keeps ≈0.89 of its
// weight while a 2-hour-old one keeps ≈0.06 — fresh heartbeats dominate
// without a hard online/offline cutoff ever discarding a recoverable node.
const freshnessHalfLife = 30 * time.Minute

// Freshness returns the TMB delayed-discount weight for a heartbeat seen at
// lastSeen, evaluated at now: exp(−λ·Δt), λ = ln2/halfLife. A zero lastSeen
// means "no freshness data" (e.g. self-registration rows or hand-built test
// nodes) and is neutral (1.0) rather than discarded — the weight discounts
// evidence, it does not invent doubt where no clock exists.
func Freshness(lastSeen, now int64) float64 {
	if lastSeen <= 0 || now <= lastSeen {
		return 1
	}
	age := time.Duration(now-lastSeen) * time.Second
	// exp(−λ·Δt), λ = ln2/halfLife — the classic half-life decay.
	return math.Exp(-math.Ln2 * age.Hours() / freshnessHalfLife.Hours())
}

// resourceEfficiency is the free-capacity ratio of the node: the share of its
// concurrent execution slots currently unoccupied. A node that advertises no
// concurrency limit is neutral (0.5) rather than full marks — absence of a
// limit is absence of evidence, not proof of idle.
func resourceEfficiency(n ledger.Node) float64 {
	if n.Capacity.MaxConcurrent <= 0 {
		return 0.5
	}
	free := n.Capacity.MaxConcurrent - n.Capacity.CurrentTasks
	if free < 0 {
		free = 0
	}
	return float64(free) / float64(n.Capacity.MaxConcurrent)
}

// waitSignal inverts the node's current queue depth: how soon a new task would
// start. Distinct from resourceEfficiency — a wide node half-full and a narrow
// node half-full score the same efficiency, but the narrow node's single queued
// task still blocks longer in absolute terms.
func waitSignal(n ledger.Node) float64 {
	return 1 / (1 + float64(n.Capacity.CurrentTasks))
}

// tierSignal normalizes the scheduler tier (Micro=1, Standard=5, Full=10) to
// [0,1].
func tierSignal(n ledger.Node) float64 {
	t := n.SchedulerTier
	if t < 0 {
		t = 0
	}
	if t > 10 {
		t = 10
	}
	return float64(t) / 10
}

// score returns the node's weighted desirability (design §6.3), discounted by
// heartbeat freshness (TMB). user_priority enters as 1.0 for the user-named
// preferred node; in practice Route honors a matching preferred node before
// scoring runs, so the term stays 0 here and the remaining three weights carry
// the ranking.
func score(n ledger.Node, now int64) float64 {
	raw := wResourceEfficiency*resourceEfficiency(n) +
		wSchedulerTier*tierSignal(n) +
		wWaitTime*waitSignal(n)
	return raw * Freshness(n.LastSeen, now)
}

// pickBest returns the id of the highest-scoring node, breaking ties by lowest
// id so every node in the network ranks the same candidate set identically and
// a forwarded task never loops back through an earlier hop. now is the Unix
// evaluation time for the freshness discount.
func pickBest(nodes []ledger.Node, now int64) string {
	id, _ := pickBestScored(nodes, now)
	return id
}

// pickBestScored is pickBest plus the winning score, which is what lets this
// node enter its own ranking as one candidate among many: comparing "best peer"
// against "myself" needs the number, not just the name. An empty set scores 0,
// so a lone capable local node wins by default.
func pickBestScored(nodes []ledger.Node, now int64) (string, float64) {
	if len(nodes) == 0 {
		return "", 0
	}
	best := nodes[0]
	bestScore := score(best, now)
	for _, n := range nodes[1:] {
		s := score(n, now)
		if s > bestScore || (s == bestScore && n.ID < best.ID) {
			best, bestScore = n, s
		}
	}
	return best.ID, bestScore
}
