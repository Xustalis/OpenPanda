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

// userPriority is the DCPS user-named-node signal (design §6.3): 1.0 when the
// node is the one the user (or the entry model) named — matched on id (the
// routing key) or display name (what the device summary shows) — 0 otherwise.
// Route still short-circuits a *capable* preferred node before ranking, so in
// practice the term mostly matters in the orbit's score breakdown and when the
// named node is on the chain or missing the ability: there it still loses to
// nodes that can actually run the task, which is the intended fall-through.
func userPriority(n ledger.Node, preferred string) float64 {
	if preferred != "" && (preferred == n.ID || preferred == n.Name) {
		return 1
	}
	return 0
}

// score returns the node's weighted desirability (design §6.3), discounted by
// heartbeat freshness (TMB). user_priority enters as 1.0 for the user-named
// preferred node; in practice Route honors a matching preferred node before
// scoring runs, so the term mostly shows up in the breakdown the orbit renders
// — but it is part of the §6.3 sum, so it belongs in the score.
func score(n ledger.Node, now int64, preferred string) float64 {
	raw := wResourceEfficiency*resourceEfficiency(n) +
		wUserPriority*userPriority(n, preferred) +
		wSchedulerTier*tierSignal(n) +
		wWaitTime*waitSignal(n)
	return raw * Freshness(n.LastSeen, now)
}

// pickBest returns the id of the highest-scoring node, breaking ties by lowest
// id so every node in the network ranks the same candidate set identically and
// a forwarded task never loops back through an earlier hop. now is the Unix
// evaluation time for the freshness discount.
func pickBest(nodes []ledger.Node, now int64, preferred string) string {
	id, _ := pickBestScored(nodes, now, preferred)
	return id
}

// pickBestScored is pickBest plus the winning score, which is what lets this
// node enter its own ranking as one candidate among many: comparing "best peer"
// against "myself" needs the number, not just the name. An empty set scores 0,
// so a lone capable local node wins by default.
func pickBestScored(nodes []ledger.Node, now int64, preferred string) (string, float64) {
	if len(nodes) == 0 {
		return "", 0
	}
	best := nodes[0]
	bestScore := score(best, now, preferred)
	for _, n := range nodes[1:] {
		s := score(n, now, preferred)
		if s > bestScore || (s == bestScore && n.ID < best.ID) {
			best, bestScore = n, s
		}
	}
	return best.ID, bestScore
}

// ScoreBreakdown exposes the inner terms of the DCPS weighted ranking so the
// decision orbit can render "why this node scored that way" instead of a
// black-box float. All terms are in the (0,1] range they occupy in scoring;
// LocalBonus is non-zero only for the entry node (see localBias in route.go).
type ScoreBreakdown struct {
	ResourceEfficiency float64 `json:"resource_efficiency"`
	// UserPriority is the user-named-node signal (design §6.3, weight 0.3):
	// 1 when this node is the one the user named, 0 otherwise. Route honors a
	// capable preferred node before ranking, so on the winning path this
	// mostly explains "why" in the orbit rather than flipping the ranking.
	UserPriority  float64 `json:"user_priority"`
	SchedulerTier float64 `json:"scheduler_tier"`
	WaitTime      float64 `json:"wait_time"`
	// HeartbeatFreshness is the TMB decay weight in (0,1] — 1.0 = brand-new
	// heartbeat, ≈0 = stale past the half-life. The wire shape mirrors the
	// design doc §3.1.1 "heartbeat_age" (0..∞ seconds, lower is better) so
	// orbit Step-2 consumers can render age text directly. Both fields
	// coexist: HeartbeatAge is what the wire contract demands, freshness is
	// kept for internal callers that want the weight (e.g. route decisions).
	HeartbeatAge       float64 `json:"heartbeat_age"`
	HeartbeatFreshness float64 `json:"heartbeat_freshness,omitempty"`
	LocalBonus         float64 `json:"local_bonus,omitempty"`
	Total              float64 `json:"total"`
}

// ScoredCandidate is one routing candidate with its identity and score detail.
// Returned ordered by total score desc so consumers can slice the top-N.
// Both total_score (flat, §3.1.1 wire contract) and breakdown.total (nested
// copy) are populated — the orbit renders the short path, the route detail
// drawer reads the full breakdown.
type ScoredCandidate struct {
	NodeID     string         `json:"node_id"`
	NodeName   string         `json:"node_name,omitempty"`
	TotalScore float64        `json:"total_score"`
	Breakdown  ScoreBreakdown `json:"score_breakdown"`
}

// scoreBreakdown computes the per-term breakdown for a single node. total =
// (weighted raw sum) * freshness. localBonus is applied by the caller (it is
// not a term inside score()).
func scoreBreakdown(n ledger.Node, now int64, preferred string) ScoreBreakdown {
	re := resourceEfficiency(n)
	up := userPriority(n, preferred)
	ti := tierSignal(n)
	wt := waitSignal(n)
	var ageSec float64
	if n.LastSeen > 0 && now > n.LastSeen {
		ageSec = float64(now - n.LastSeen)
	}
	fresh := Freshness(n.LastSeen, now)
	raw := wResourceEfficiency*re + wUserPriority*up + wSchedulerTier*ti + wWaitTime*wt
	return ScoreBreakdown{
		ResourceEfficiency: re,
		UserPriority:       up,
		SchedulerTier:      ti,
		WaitTime:           wt,
		HeartbeatAge:       ageSec, // design doc §3.1.1 wire contract
		HeartbeatFreshness: fresh,  // internal weight, still useful
		Total:              raw * fresh,
	}
}

// ScoreAllCandidates ranks every node in candidates (already filtered to
// online/capable) and returns them with full score breakdown. selfID (when
// non-empty) receives the localBias add-on on top of its baseline score;
// preferred (when non-empty) is the user-named node, whose user_priority term
// reflects the §6.3 weight. Callers should filter candidates (online,
// hardware fit, not on chain) before passing them in — this function scores,
// it does not gate.
func ScoreAllCandidates(candidates []ledger.Node, selfID, preferred string, now int64) []ScoredCandidate {
	out := make([]ScoredCandidate, 0, len(candidates))
	for _, n := range candidates {
		bd := scoreBreakdown(n, now, preferred)
		if selfID != "" && n.ID == selfID {
			bd.LocalBonus = localBias
			bd.Total += localBias
		}
		name := n.Name
		if name == "" {
			name = n.ID
		}
		out = append(out, ScoredCandidate{
			NodeID:     n.ID,
			NodeName:   name,
			TotalScore: bd.Total,
			Breakdown:  bd,
		})
	}
	// desc by total, asc by node_id for tie-break consistency
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].TotalScore > out[i].TotalScore ||
				(out[j].TotalScore == out[i].TotalScore && out[j].NodeID < out[i].NodeID) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
