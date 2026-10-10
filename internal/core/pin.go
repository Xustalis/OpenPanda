// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

// PinnedNode exposes the hard pin a task carries (spec.node), for callers
// outside this package that render or reason about where the task is allowed
// to run. "" means unpinned — normal scored routing applies.
func PinnedNode(t Task) string { return targetNodeOf(t) }

// pinResolution is the outcome of mapping a user-named node reference
// (spec.node, --preferred, task_submit's node, plan-stage node) onto the
// capability directory. The reference is a HARD pin: once the user names a
// destination the task runs there, waits for it, or fails with the reason —
// it never silently reroutes to a higher-scored candidate. Soft scoring
// remains the default only for submissions that name nobody.
type pinResolution struct {
	// targetID is the canonical directory row id the reference resolved to.
	// Empty when found is false.
	targetID string
	// node is the directory row the reference resolved to (capability and
	// fit checks read it), zero-valued when found is false.
	node ledger.Node
	// self marks the reference naming this node — execution stays local.
	self bool
	// found marks at least one directory row matching the reference (by row
	// id or display name, case-insensitive). An offline row still counts:
	// the pin is deliverable later even though the link is down now.
	found bool
	// online marks the resolved row currently advertising online status.
	online bool
	// ambiguous marks the reference matching several DISTINCT online rows —
	// a name collision the resolver refuses to guess at. Offline duplicates
	// resolve to their freshest row instead (ghost entries decay, the real
	// node's row is the one that beat most recently).
	ambiguous bool
}

// resolvePin maps ref to a node in this node's directory view. Resolution
// order:
//
//  1. Self — the routing participant id, the self row's id, and the self
//     row's display name all resolve to local execution. Checking self
//     first keeps a forwarded pin landing on its destination even when the
//     executor's directory holds same-named ghost rows for other machines.
//  2. Online rows — an unambiguous online match wins outright.
//  3. Offline rows — the most recently seen match, so a name shared by
//     stale ghost entries still tracks the node that used it last.
//
// Two or more distinct ONLINE rows answering to one name resolve to
// ambiguous: pinning "the Mac" onto whichever duplicate happened to sort
// first is the silent-misrouting this resolver exists to kill.
func (c *Core) resolvePin(ctx context.Context, ref string) pinResolution {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return pinResolution{}
	}
	// A ref naming this node directly (instance id or stable key) resolves to
	// self without a directory row, so a pin survives a directory that has not
	// recorded this node yet.
	selfRef := strings.EqualFold(ref, c.nodeID) || strings.EqualFold(ref, c.selfStableID())
	// A "k:<pubkey>" ref names a stable identity, not an instance: resolve it
	// to pubkey-hex for row matching so a pin by stable key works on every
	// hop, including the target itself (whose self row id is its instance id).
	keyRef := strings.TrimPrefix(ref, stableIDPrefix)
	isKeyRef := strings.HasPrefix(ref, stableIDPrefix)
	nodes, err := ledger.Query(c.db, "", "")
	if err != nil {
		c.logger.Warn("resolve pin: directory query failed", "ref", ref, "err", err)
		if selfRef {
			// An explicit self-name still resolves on a broken directory: the
			// ref names this node's own live identity, no row needed.
			return pinResolution{self: true, found: true, online: true}
		}
		return pinResolution{}
	}
	var selfRow ledger.Node
	var onlineHit, staleHit ledger.Node
	var onlineCount, staleCount int
	for _, n := range nodes {
		if scheduler.IsSelfRow(n.ID, c.nodeID) {
			selfRow = n
			continue
		}
		matched := strings.EqualFold(n.ID, ref) || strings.EqualFold(n.Name, ref) ||
			strings.EqualFold(scheduler.NodeNamePart(n.ID), ref) ||
			(n.PubKey != "" && strings.EqualFold(n.PubKey, keyRef))
		if isKeyRef {
			matched = n.PubKey != "" && strings.EqualFold(n.PubKey, keyRef)
		}
		if !matched {
			continue
		}
		if n.Status == "online" {
			onlineCount++
			if onlineHit.ID == "" {
				onlineHit = n
			}
			continue
		}
		staleCount++
		if staleHit.ID == "" || n.LastSeen > staleHit.LastSeen {
			staleHit = n
		}
	}
	if selfRef || (selfRow.ID != "" &&
		(strings.EqualFold(ref, selfRow.ID) || strings.EqualFold(ref, selfRow.Name) ||
			strings.EqualFold(ref, scheduler.NodeNamePart(selfRow.ID)) ||
			(selfRow.PubKey != "" && strings.EqualFold(selfRow.PubKey, keyRef) &&
				(isKeyRef || strings.EqualFold(ref, selfRow.PubKey))))) {
		// The self row rides along so callers can run the same resource-fit
		// check on this node that a remote resolution would get; a node the
		// directory has not recorded yet yields a zero row, and Fits treats
		// an undeclared profile as unknown-permissive.
		return pinResolution{self: true, node: selfRow, found: true,
			online: selfRef || selfRow.Status == "online"}
	}
	switch {
	case onlineCount == 1:
		return pinResolution{targetID: onlineHit.ID, node: onlineHit, found: true, online: true}
	case onlineCount > 1:
		return pinResolution{found: true, online: true, ambiguous: true}
	case staleCount > 0:
		return pinResolution{targetID: staleHit.ID, node: staleHit, found: true}
	default:
		return pinResolution{}
	}
}

// routePinned produces the routing decision for a task carrying a hard node
// pin. Unlike scheduler.RouteP it never scores alternatives: the pin resolves
// to self (run locally — when this node can actually serve the task), to a
// directory row (forward — reachability is a delivery concern handled
// downstream, where a dead link parks rather than diverts), to several live
// rows (ambiguous — refuse to guess), or to nothing (decline honestly). A pin
// that names a node already in the chain declines too: re-entering a hop is
// the loop the chain exists to prevent, pin or not.
//
// Capability is part of the pin's honesty contract: a node the user named but
// that cannot serve the task's requires/profile declines up front with the
// missing ability named, instead of accepting and erroring mid-run.
func (c *Core) routePinned(ctx context.Context, ref string, chain []string,
	requires []string, resReq ledger.ResourceProfile) scheduler.Decision {
	res := c.resolvePin(ctx, ref)
	switch {
	case res.self:
		// Local execution is a terminal sink, not a hop: a chain that already
		// contains this node is the normal origin-queued case, not a loop to
		// refuse (revisiting via delegation is rejected upstream by
		// AppendChain before this resolver runs).
		if len(requires) > 0 && !c.localMatch()(requires) {
			return scheduler.Decision{Action: scheduler.ActionDecline,
				Reason: fmt.Sprintf("pinned node %q (this node) cannot satisfy requires %v", ref, requires)}
		}
		// The same honesty a remote pin gets: the user's destination is
		// fixed, but a declared profile this node positively cannot fit is
		// still a decline, not a quiet under-provisioned run.
		if !res.node.Fits(resReq) {
			return scheduler.Decision{Action: scheduler.ActionDecline,
				Reason: fmt.Sprintf("pinned node %q (this node) does not fit the resource profile", ref)}
		}
		return scheduler.Decision{Action: scheduler.ActionLocal, Reason: "pinned to this node"}
	case res.ambiguous:
		return scheduler.Decision{Action: scheduler.ActionDecline,
			Reason: fmt.Sprintf("pinned node %q matches multiple online rows; use the node id", ref)}
	case !res.found:
		return scheduler.Decision{Action: scheduler.ActionDecline,
			Reason: fmt.Sprintf("pinned node %q is not in the directory", ref)}
	case slices.Contains(chain, res.targetID):
		return scheduler.Decision{Action: scheduler.ActionDecline,
			Reason: fmt.Sprintf("pinned node %q is already in the delegation chain", ref)}
	case len(requires) > 0 && !res.node.Matches(requires):
		return scheduler.Decision{Action: scheduler.ActionDecline,
			Reason: fmt.Sprintf("pinned node %q cannot satisfy requires %v", ref, requires)}
	case !res.node.Fits(resReq):
		return scheduler.Decision{Action: scheduler.ActionDecline,
			Reason: fmt.Sprintf("pinned node %q does not fit the resource profile", ref)}
	default:
		return scheduler.Decision{Action: scheduler.ActionForward, Target: res.targetID,
			Reason: fmt.Sprintf("pinned to %s", res.targetID)}
	}
}
