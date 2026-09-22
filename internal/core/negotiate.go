package core

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/defense"
)

// negotiate.go implements the substance behind the §5 peer-negotiation wire
// protocol: a scope lock table with weight arbitration (§5.1/§5.2) and the
// §6.3 distributed wait-for graph whose cycles are broken by preempting the
// weakest holder.
//
// Model: each node keeps a table of scope locks keyed by repo|file|symbol.
// A task with a declared scope negotiates before executing — locally against
// its own table, and with every live peer — so two agents on different nodes
// that would edit the same file arbitrate instead of colliding. The loser is
// told to yield; the denier records a wait-for edge and, when the edges close
// a cycle, the lowest-weight holder is preempted to break the deadlock.

// negoLease bounds how long a scope grant lives before it must be renewed or
// released. Generous enough to cover an agent round, short enough that a
// crashed holder frees the mesh quickly.
const negoLease = 5 * time.Minute

// negoLock is one granted scope lock.
type negoLock struct {
	scopeKey  string
	holder    string // "node|agent"
	weight    int
	expiresAt time.Time
	// holderTask is the local task holding the lock, when the holder is this
	// node — what a yield must actually interrupt. Empty for remote holders.
	holderTask string
}

// negoHolder builds the principal id a lock is attributed to: node|agent.
// The agent is scoped to its node so the same CLI name on two machines is two
// principals, and two local tasks driving the same agent name share one
// principal — within a node the scope-drift layer serializes them.
func negoHolder(node, agent string) string { return node + "|" + agent }

func negoScopeKey(s bus.TargetScope) string {
	return s.Repo + "|" + s.File + "|" + s.Symbol
}

// negoWeightOf derives the arbitration weight from queue priority: the mesh
// treats a high-priority task's claim on a scope as worth more than a
// background one, exactly the ordering the local scheduler already applies.
func negoWeightOf(priority int) int {
	w := (PriorityLow - priority + 1) * 100
	if w <= 0 {
		w = 100
	}
	return w
}

// negoDecision is the outcome of arbitrating one negotiate request.
type negoDecision struct {
	granted   bool
	leaseMS   int64
	reason    string
	preempted []string // holder ids whose locks were broken (preempt / deadlock)
}

// negoDecide arbitrates one request against the lock table, applying the
// §5.2 three-strategy rule in the only order that converges: uncontended →
// grant; same principal → renew; strictly higher weight → preempt and yield;
// otherwise deny and record the wait-for edge — breaking the edge cycle, when
// one forms, by yielding the weakest holder in it (§6.3).
func (c *Core) negoDecide(now time.Time, p bus.AgentNegotiatePayload, holderTask string) negoDecision {
	key := negoScopeKey(p.TargetScope)
	requester := negoHolder(p.FromNode, p.FromAgent)
	c.negoMu.Lock()
	defer c.negoMu.Unlock()
	if c.nego == nil {
		c.nego = make(map[string]*negoLock)
	}
	if c.negoWait == nil {
		c.negoWait = make(map[string]map[string]bool)
	}
	// Lazy lease sweep: expired locks drop out with their wait-for edges, so a
	// holder that died mid-lease frees the mesh without a dedicated sweeper.
	for k, l := range c.nego {
		if now.After(l.expiresAt) {
			delete(c.nego, k)
			c.negoDropHolderLocked(l.holder)
		}
	}
	l, held := c.nego[key]
	if !held {
		c.nego[key] = &negoLock{scopeKey: key, holder: requester, weight: p.Weight,
			expiresAt: now.Add(negoLease), holderTask: holderTask}
		return negoDecision{granted: true, leaseMS: int64(negoLease / time.Millisecond)}
	}
	if l.holder == requester {
		l.expiresAt = now.Add(negoLease)
		if p.Weight > l.weight {
			l.weight = p.Weight
		}
		return negoDecision{granted: true, leaseMS: int64(negoLease / time.Millisecond)}
	}
	if p.Weight > l.weight {
		preempted := []string{l.holder}
		c.negoDropHolderLocked(l.holder)
		l.holder, l.weight, l.expiresAt, l.holderTask = requester, p.Weight, now.Add(negoLease), holderTask
		return negoDecision{granted: true, leaseMS: int64(negoLease / time.Millisecond), preempted: preempted}
	}
	// Denied: wait-for edge requester -> holder (§6.3).
	if c.negoWait[requester] == nil {
		c.negoWait[requester] = make(map[string]bool)
	}
	c.negoWait[requester][l.holder] = true
	if cyc := c.negoCycleLocked(requester); len(cyc) > 0 {
		victim, vw := "", math.MaxInt
		for _, h := range cyc {
			for _, lk := range c.nego {
				if lk.holder == h && lk.weight < vw {
					victim, vw = h, lk.weight
				}
			}
		}
		preempted := []string{}
		if victim != "" {
			preempted = append(preempted, victim)
			for k, lk := range c.nego {
				if lk.holder == victim {
					delete(c.nego, k)
				}
			}
			c.negoDropHolderLocked(victim)
		}
		if _, stillHeld := c.nego[key]; !stillHeld {
			c.nego[key] = &negoLock{scopeKey: key, holder: requester, weight: p.Weight,
				expiresAt: now.Add(negoLease), holderTask: holderTask}
			delete(c.negoWait, requester)
			return negoDecision{granted: true, leaseMS: int64(negoLease / time.Millisecond),
				preempted: preempted, reason: "deadlock broken by yielding " + victim}
		}
		return negoDecision{preempted: preempted,
			reason: "deadlock detected; still waiting on " + l.holder}
	}
	return negoDecision{reason: "scope locked by " + l.holder}
}

// negoCycleLocked reports the wait-for cycle through start, if one exists.
// Edges run waiter -> holder; a path that reaches start again is a deadlock.
// Runs under negoMu; bounded by the graph's own size.
func (c *Core) negoCycleLocked(start string) []string {
	var path []string
	seen := map[string]bool{start: true}
	var dfs func(n string) bool
	dfs = func(n string) bool {
		for next := range c.negoWait[n] {
			if next == start {
				path = append(path, n)
				return true
			}
			if seen[next] {
				continue
			}
			seen[next] = true
			if dfs(next) {
				path = append(path, n)
				return true
			}
		}
		return false
	}
	if dfs(start) {
		for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
			path[i], path[j] = path[j], path[i]
		}
		return path
	}
	return nil
}

// negoDropHolderLocked removes every wait-for edge that names holder, on
// either side. Runs under negoMu.
func (c *Core) negoDropHolderLocked(holder string) {
	delete(c.negoWait, holder)
	for _, set := range c.negoWait {
		delete(set, holder)
	}
}

// negoRelease drops all locks and wait-for edges held by holder — called when
// a local task finishes (its agent cedes every scope it took) or when a yield
// lands from the network.
func (c *Core) negoRelease(holder string) {
	c.negoMu.Lock()
	defer c.negoMu.Unlock()
	for k, l := range c.nego {
		if l.holder == holder {
			delete(c.nego, k)
		}
	}
	c.negoDropHolderLocked(holder)
}

// negoYieldTo informs a preempted holder that its grant was revoked. A local
// holder is interrupted directly: the task that holds the lock is cancelled,
// which is the closest executable equivalent of "halt at the next AST
// checkpoint" the current adapters support. A remote holder gets the wire
// yield; its node performs the same local interrupt on receipt.
func (c *Core) negoYieldTo(ctx context.Context, holder string, scope bus.TargetScope, requester string) {
	node, _, _ := strings.Cut(holder, "|")
	if node == c.nodeID || node == "" {
		c.EvTrace(ctx, "", "agent_yield_local", map[string]any{
			"holder": holder, "scope": scope.File, "to": requester,
		})
		c.negoInterruptLocal(holder, negoScopeKey(scope))
		return
	}
	p := bus.AgentYieldPayload{FromAgent: requester, Scope: scope, Reason: "preempted by higher weight"}
	msgID, err := newUUID()
	if err != nil {
		return
	}
	env, err := bus.NewEnvelope(bus.MsgAgentYield, c.nodeID, msgID, p)
	if err != nil {
		return
	}
	env.To = node
	_ = c.sendTo(node, env) // best-effort: the lease expiry frees the lock anyway
}

// negoInterruptLocal cancels the local task holding a lock for scope. Yield
// enforcement depth ends at task cancellation; finer "halt at checkpoint"
// semantics need adapter-level breakpoints and are future work.
func (c *Core) negoInterruptLocal(holder, scopeFile string) {
	c.negoMu.Lock()
	var taskID string
	for k, l := range c.nego {
		if l.holder == holder && (scopeFile == "" || l.scopeKey == scopeFile) {
			taskID = l.holderTask
			delete(c.nego, k)
		}
	}
	c.negoMu.Unlock()
	if taskID != "" {
		if cancel, ok := c.running.Load(taskID); ok {
			cancel.(context.CancelFunc)()
		}
	}
	c.negoRelease(holder)
}

// negotiateTaskScope runs §5.1 arbitration for every declared scope root —
// locally, then with each live peer — and returns the first denial. A denial
// fails the run so the normal retry/backoff loop retries later, which is the
// honest "wait" semantic: the holder's lease expires or its yield lands, and
// the retried negotiation then wins.
func (c *Core) negotiateTaskScope(ctx context.Context, task Task, scope *defense.Scope, agent string) error {
	weight := negoWeightOf(task.Priority)
	holder := negoHolder(c.nodeID, agent)
	for _, root := range scope.Roots() {
		ts := bus.TargetScope{Repo: task.Project, File: root}
		dec := c.negoDecide(time.Now(), bus.AgentNegotiatePayload{
			FromNode: c.nodeID, FromAgent: agent, Timestamp: time.Now().UnixMilli(),
			Weight: weight, TargetScope: ts, Intent: task.Intent,
		}, task.TaskID)
		for _, h := range dec.preempted {
			c.negoYieldTo(ctx, h, ts, holder)
		}
		if !dec.granted {
			return fmt.Errorf("scope %s: %s", root, dec.reason)
		}
		for _, peer := range c.livePeerIDs() {
			grant, err := c.Negotiate(ctx, peer, bus.AgentNegotiatePayload{
				FromNode: c.nodeID, FromAgent: agent, Timestamp: time.Now().UnixMilli(),
				Weight: weight, TargetScope: ts, Intent: task.Intent,
			})
			if err != nil {
				// An unreachable peer cannot hold a lock it could enforce — its
				// grants ride the same leases we do. Skip, don't block the mesh.
				c.logger.Debug("negotiate skipped", "peer", peer, "err", err)
				continue
			}
			if grant.Denied {
				return fmt.Errorf("scope %s: denied by %s: %s", root, peer, grant.Reason)
			}
		}
	}
	return nil
}

// livePeerIDs lists peers with a live connection — the set that can answer a
// negotiation inside its timeout.
func (c *Core) livePeerIDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.peers))
	for id, p := range c.peers {
		if p.conn != nil {
			out = append(out, id)
		}
	}
	return out
}

// Negotiate sends one scope request to a peer and waits synchronously for its
// grant/deny verdict — the caller-side half of §5.1 that turns the wire
// messages into an arbitration primitive.
func (c *Core) Negotiate(ctx context.Context, target string, p bus.AgentNegotiatePayload) (bus.AgentGrantPayload, error) {
	msgID, err := newUUID()
	if err != nil {
		return bus.AgentGrantPayload{}, err
	}
	ch := make(chan bus.AgentGrantPayload, 1)
	c.negoMu.Lock()
	if c.negoWaiters == nil {
		c.negoWaiters = make(map[string]chan bus.AgentGrantPayload)
	}
	c.negoWaiters[msgID] = ch
	c.negoMu.Unlock()
	defer func() {
		c.negoMu.Lock()
		delete(c.negoWaiters, msgID)
		c.negoMu.Unlock()
	}()

	env, err := bus.NewEnvelope(bus.MsgAgentNegotiate, c.nodeID, msgID, p)
	if err != nil {
		return bus.AgentGrantPayload{}, err
	}
	env.To = target
	if err := c.sendTo(target, env); err != nil {
		return bus.AgentGrantPayload{}, err
	}
	select {
	case g := <-ch:
		return g, nil
	case <-ctx.Done():
		return bus.AgentGrantPayload{}, ctx.Err()
	case <-time.After(4 * time.Second):
		return bus.AgentGrantPayload{}, fmt.Errorf("negotiation timeout")
	}
}
