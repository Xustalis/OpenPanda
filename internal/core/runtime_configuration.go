// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/version"
	"strings"
)

// SetContacts installs this node's advertised contact plan (§8.4). Call it
// after NewCore, before the serving goroutines start — the slice is read
// unsynchronized afterward. The plan gossips out in every heartbeat and
// lands in the local directory row via refreshSelfNeighbors.
func (c *Core) SetContacts(contacts []ledger.Contact) {
	c.contacts = contacts
}

// SetOwnsNodeRow declares whether this process owns the node's directory
// row. Call it after NewCore, before serving starts. See the field comment.
func (c *Core) SetOwnsNodeRow(owns bool) {
	c.ownsNodeRow = owns
}

// OwnsNodeRow reports the same: false marks a borrowed engine whose daemon
// holds the identity lock — such a core must never write liveness or run a
// second work consumer for the identity.
func (c *Core) OwnsNodeRow() bool {
	return c.ownsNodeRow
}

// NodeID returns this core's stable node identity — the employee_cache row
// its heartbeat (when it owns the row) and queue claims are keyed on.
func (c *Core) NodeID() string {
	return c.nodeID
}

// SetEdgeAuthority marks whether this process holds the node identity lock
// for its lifetime (the daemon entry point does; engines only probe-and-
// release it at init, so they must leave this false). The claim rides every
// hello and lets a same-id arbitration prefer the lock-holding process when
// a stray sibling holds a peer edge hostage.
func (c *Core) SetEdgeAuthority(v bool) {
	c.edgeAuthority.Store(v)
}

// wireContacts converts the configured plan to its wire form. The slice is
// non-nil even when empty so the heartbeat emits "contacts":[] — a plan
// deleted from config must clear the directories that cached it, which only
// happens if the field rides the beat explicitly.
func (c *Core) wireContacts() []bus.Contact {
	out := make([]bus.Contact, len(c.contacts))
	for i, ct := range c.contacts {
		out[i] = bus.Contact{Peer: ct.Peer, Start: ct.Start, End: ct.End, RateBps: ct.RateBps, Period: ct.Period}
	}
	return out
}

// SetRouterPolicy applies the configured injection/routing policy to the
// commander router (injection.model and routing.preferred_agents). Call it
// after NewCore with the loaded config sections; a nil router (empty card)
// makes it a no-op. The sections are also remembered so ReloadCard can rebuild
// the router with the same policy.
func (c *Core) SetRouterPolicy(injection config.InjectionConfig, routing config.RoutingConfig) {
	c.cardMu.Lock()
	c.routerInjection, c.routerRouting = injection, routing
	c.rebuildRouterLocked()
	c.cardMu.Unlock()
	c.updateQueueLimit()
}

// SetAgentMCPPassthrough sets the stdio MCP server (mcp.command argv string,
// empty disables) that extended-policy agent runs expose to the delegated
// agent CLI via a work-dir .mcp.json. Remembered like the router policy so a
// ReloadCard rebuild re-applies it.
func (c *Core) SetAgentMCPPassthrough(command string) {
	c.cardMu.Lock()
	c.mcpPassthrough = command
	router := c.router
	c.cardMu.Unlock()
	if router != nil {
		router.SetMCPPassthrough(command)
	}
}

// SetSelfConfigPath forwards the daemon's --config path to the router so the
// .mcp.json it materializes can pass it to `panda mcp`. Remembered like the
// passthrough so a ReloadCard rebuild re-applies it.
func (c *Core) SetSelfConfigPath(path string) {
	c.cardMu.Lock()
	c.selfConfigPath = path
	router := c.router
	c.cardMu.Unlock()
	if router != nil {
		router.SetSelfConfigPath(path)
	}
}

// Card snapshots the current capability card (guarding the swap a reload may
// be performing concurrently).
func (c *Core) Card() ledger.Card {
	c.cardMu.RLock()
	defer c.cardMu.RUnlock()
	return c.card
}

// currentRouter snapshots the router built from the current card.
func (c *Core) currentRouter() *commander.Router {
	c.cardMu.RLock()
	defer c.cardMu.RUnlock()
	return c.router
}

// ReloadCard swaps the capability card at runtime: load + validate the file,
// prune native abilities this host cannot run, swap the card and rebuild the
// router from it, re-register this node in the local directory, then announce
// the change to every connected peer with a card-carrying heartbeat. Until now
// a card edit required a daemon restart before the network saw the new
// abilities; this is the "改卡热生效" half of the equation.
func (c *Core) ReloadCard(ctx context.Context, path string) error {
	card, err := ledger.LoadCard(path)
	if err != nil {
		return fmt.Errorf("reload card: %w", err)
	}
	if dropped := card.PruneUnavailableNative(); len(dropped) > 0 {
		c.logger.Warn("reloaded card: native abilities dropped: command not found on this host", "ids", strings.Join(dropped, ","))
	}
	if dropped := card.PruneUnavailableActuators(); len(dropped) > 0 {
		c.logger.Warn("reloaded card: actuators dropped: command not found on this host", "ids", strings.Join(dropped, ","))
	}
	// Node kind/identity live in config, not the card file; carry them over
	// exactly as the daemon's startup path does, or the re-registered row
	// would silently flip to the defaults.
	c.cardMu.RLock()
	card.NodeKind = c.card.NodeKind
	card.NodeIdentity = c.card.NodeIdentity
	c.cardMu.RUnlock()

	c.cardMu.Lock()
	c.card = card
	c.rebuildRouterLocked()
	c.cardMu.Unlock()

	c.updateQueueLimit()
	c.node.SetCard(card)
	if err := c.node.Register(ctx); err != nil {
		return err
	}
	c.broadcastCard(ctx)
	c.logger.Info("card reloaded", "path", path)
	return nil
}

// broadcastCard announces the current capability summary to every connected
// peer as a heartbeat carrying the card, so peers adopt the new abilities
// without waiting for a reconnect. Send failures are skipped like the
// heartbeat loop's: the next tick's plain heartbeat still refreshes liveness.
func (c *Core) broadcastCard(ctx context.Context) {
	card, err := c.helloCard()
	if err != nil {
		c.logger.Warn("marshal card for broadcast", "err", err)
		return
	}
	capJSON, _ := c.node.capacitySnapshot(ctx)
	c.mu.RLock()
	conns := make(map[string]*bus.Conn, len(c.peers))
	for id, p := range c.peers {
		conns[id] = p.conn
	}
	c.mu.RUnlock()
	msgID, err := newUUID()
	if err != nil {
		c.logger.Warn("mint card-broadcast id", "err", err)
		return
	}
	env, err := bus.NewEnvelope(bus.MsgHeartbeat, c.nodeID, msgID, bus.HeartbeatPayload{
		Status: "online", Load: 0, Capacity: capJSON, Card: card,
		BlockedAgents: c.blockedAgents(),
		Ver:           version.Version, Prov: wireProvenance(),
	})
	if err != nil {
		c.logger.Warn("build card heartbeat", "err", err)
		return
	}
	// One envelope serves every peer: msg_id dedup is per-receiver. Sends go
	// out concurrently — Send blocks until the conn's writer places the frame
	// (bounded by writeWait), so a wedged peer must not serialize its stall
	// onto every later peer's beat.
	for id, conn := range conns {
		e := env
		e.To = id
		c.goGuarded("card heartbeat send "+id, func() {
			if err := conn.Send(e); err != nil {
				c.logger.Debug("card heartbeat send", "peer", id, "err", err)
			}
		})
	}
}

// SetSupervisor attaches the entry model client that judges agent task
// completeness (上级完成度判定). A nil client disables supervision: agent
// tasks then finish in one shot exactly as before, so minimal nodes and tests
// are unaffected.
func (c *Core) SetSupervisor(client *entry.Client) {
	c.cardMu.Lock()
	c.supervisor = client
	c.cardMu.Unlock()
}

func (c *Core) currentSupervisor() *entry.Client {
	c.cardMu.RLock()
	defer c.cardMu.RUnlock()
	return c.supervisor
}

// ModelConfig returns the live default used for agent injection and judging.
func (c *Core) ModelConfig() config.ModelConfig {
	c.cardMu.RLock()
	defer c.cardMu.RUnlock()
	return c.model
}

// executionSnapshot binds an attempt's router, model attribution and judge to
// the same settings revision. In-flight attempts keep their snapshot on a
// hot switch; subsequently started attempts use the new defaults.
func (c *Core) executionSnapshot() (*commander.Router, config.ModelConfig, *entry.Client) {
	c.cardMu.RLock()
	defer c.cardMu.RUnlock()
	return c.router, c.model, c.supervisor
}

// SetModelRefresh installs an optional pre-attempt config observer. The
// callback runs outside cardMu: it may atomically publish a new model.
func (c *Core) SetModelRefresh(fn func(context.Context) error) {
	c.cardMu.Lock()
	defer c.cardMu.Unlock()
	c.modelRefresh = fn
}

func (c *Core) refreshModel(ctx context.Context) {
	c.cardMu.RLock()
	refresh := c.modelRefresh
	c.cardMu.RUnlock()
	if refresh != nil {
		_ = refresh(ctx)
	} // invalid/deleted file keeps last-good defaults
}

// SetModel publishes new execution defaults without changing explicit agent
// overrides or injection policy. Client construction is validated first, so
// failure leaves the previous snapshot intact. An empty model clears both
// injection credentials and the supervisor for future attempts.
func (c *Core) SetModel(model config.ModelConfig) error {
	supervisor, err := c.supervisorForModel(model)
	if err != nil {
		return err
	}
	c.cardMu.Lock()
	defer c.cardMu.Unlock()
	c.model = model
	c.supervisor = supervisor
	c.rebuildRouterLocked()
	return nil
}

// rebuildRouterLocked keeps every rebuild (card or model) wired identically.
// The caller holds cardMu; existing runs keep their old router pointer.
func (c *Core) rebuildRouterLocked() {
	card := c.card
	// The concurrency gate is shared with the commander, so it must follow the
	// live routing config even when this node has no capability card at all
	// (a pure router, or a card that failed to load). Skipping Configure here
	// leaves a zero-valued gate: a later agent acquire then blocks until its
	// context expires instead of running.
	c.agentConcurrency.Configure(card.Capacity.MaxConcurrent, c.routerRouting, c.delegLimits)
	if len(card.Native) == 0 && len(card.Agents) == 0 && len(card.Manual) == 0 && len(card.Actuators) == 0 {
		c.router = nil
		return
	}
	if c.router == nil {
		c.router = commander.NewRouter(card, commander.NewExecutor(), c.model, c.routerInjection, c.routerRouting)
	} else {
		c.router = c.router.WithConfig(card, c.model, c.routerInjection, c.routerRouting)
	}
	c.router.SetConcurrency(c.agentConcurrency)
	c.router.SetAgentBlocked(func(name string) bool { return c.breaker.Blocked("agent:" + name) })
	c.router.SetMCPPassthrough(c.mcpPassthrough)
	c.router.SetSelfConfigPath(c.selfConfigPath)
}

// SetSuperviseRounds bounds the execute → judge → re-delegate loop for agent
// tasks. A value <= 0 resets to defaultSuperviseRounds.
func (c *Core) SetSuperviseRounds(n int) {
	if n < 1 {
		n = defaultSuperviseRounds
	}
	c.superviseRounds = n
}

// SetAcceptanceConfig applies execution.acceptance: the per-check and
// per-phase budgets for the delivery-contract checks this node runs.
func (c *Core) SetAcceptanceConfig(a config.AcceptanceConfig) {
	c.mu.Lock()
	c.acceptance = a
	c.mu.Unlock()
}

// SetMessageBudget bounds sibling-board posts per task episode (§inter-task
// messages). A value <= 0 resets to defaultMsgBudget.
func (c *Core) SetMessageBudget(n int) {
	if n < 1 {
		n = defaultMsgBudget
	}
	c.msgBudgetCap = n
}

// msgBudget returns the per-episode message cap, honoring an unset field.
func (c *Core) msgBudget() int {
	if c.msgBudgetCap < 1 {
		return defaultMsgBudget
	}
	return c.msgBudgetCap
}

// SetDelegationLimits applies the configured sub-agent bounds (the
// delegation: section). It is safe to refresh while tasks are running.
func (c *Core) SetDelegationLimits(d config.DelegationConfig) {
	c.cardMu.Lock()
	c.delegLimits = d.NormalizedDelegation()
	c.agentConcurrency.Configure(c.card.Capacity.MaxConcurrent, c.routerRouting, c.delegLimits)
	c.cardMu.Unlock()
}

// delegateLimits resolves the effective bounds: whatever config installed,
// normalized so unset fields carry the documented defaults.
func (c *Core) delegateLimits() config.DelegationConfig {
	c.cardMu.RLock()
	defer c.cardMu.RUnlock()
	return c.delegLimits
}

// effectiveSpawnCap resolves the max_depth bound applying to a task: the
// tightest value the delegation path already carried (SpawnCap) clamped
// against this node's own config, so a forwarded subtree keeps the most
// conservative bound any hop's operator set.
func (c *Core) effectiveSpawnCap(t Task) int {
	local := c.delegateLimits().MaxDepth
	if t.SpawnCap > 0 && t.SpawnCap < local {
		return t.SpawnCap
	}
	return local
}

// delegationEnabled reports whether this node permits agent-driven
// sub-agent spawning at all (delegation.enabled). A nil flag is on —
// configs predating the toggle must not silently disable orchestration.
func (c *Core) delegationEnabled() bool {
	d := c.delegateLimits()
	return d.Enabled == nil || *d.Enabled
}

// delegateActiveCount reports how many delegate children are in flight on
// this node right now (spawned and still awaited) — the fan-out bound's
// live counter, surfaced for tests and the metrics path.
func (c *Core) delegateActiveCount() int64 {
	return c.delegateActive.Load()
}

// AttachSupervisor builds a supervisor client from the model config and
// attaches it — but only when the model is actually configured (an API key is
// present), so a model-less node keeps single-shot agent execution instead of
// burning a wasted judge call per task. A build failure is non-fatal: agent
// tasks then finish in one shot exactly as before. The supervisor also gets
// the disk cache over the node database: identical (intent, result) pairs
// reuse the previous verdict without an LLM call.
func (c *Core) AttachSupervisor(model config.ModelConfig) {
	if client, err := c.supervisorForModel(model); err == nil {
		c.SetSupervisor(client)
	}
}

func (c *Core) supervisorForModel(model config.ModelConfig) (*entry.Client, error) {
	if strings.TrimSpace(model.APIKey) == "" {
		return nil, nil
	}
	client, err := entry.NewClient(entry.BriefModelConfig(model))
	if err != nil {
		return nil, err
	}
	client.SetDiskCache(entry.NewDiskCache(c.db))
	return client, nil
}
