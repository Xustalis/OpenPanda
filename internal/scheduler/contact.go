package scheduler

import (
	"math"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// contact.go is the schedule-aware half of DTN routing (whitepaper §8.4 —
// the CGR/SABR-style path). DTNNextHop asks "which online neighbor sits on
// the cheapest advertised path"; ContactNextHop asks "which path delivers
// earliest" over the union of two edge kinds: live links, traversable at the
// moment the bundle arrives at the node holding them, and advertised contact
// windows, traversable only once they open — and only if the bundle's
// transmission fits inside the window. The answer is a first hop plus the
// earliest arrival time, which is what a store-and-forward custody decision
// actually needs: not "who is closest" but "whose custody shortens the wait".

// contactHorizonSec caps how far ahead the search trusts the plan. Contact
// advertisements are claims about the future; a route whose first step opens
// more than a week out is a parked bundle's business, not a routing answer.
const contactHorizonSec = 7 * 24 * 3600

// ContactNextHop runs earliest-arrival Dijkstra over the directory's contact
// plan. Edges out of a node u are its advertised live neighbors (online rows
// only — a live edge that is down now is no edge) and its advertised
// contacts (a windowed edge is usable even when u itself is offline: custody
// parked on a sleeping node is the DTN case, provided the node published a
// plan to wake for). dest is exempt from the online rule, as in DTNNextHop.
// exclude forbids the FIRST hop only — the no-echo rule for the inbound
// peer. sizeBytes feeds each window's fit check; expiresAt prunes any path
// that would arrive after the bundle's lifetime lapses (0 = unbounded).
//
// Returns the first hop and the earliest arrival unix time, or ("", 0) when
// no path can deliver before expiry.
func ContactNextHop(self ledger.Node, nodes []ledger.Node, dest string, exclude map[string]bool, now, sizeBytes, expiresAt int64) (string, int64) {
	if dest == "" || dest == self.ID {
		return "", 0
	}
	byID := make(map[string]ledger.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	// dist is earliest-arrival unix time, not a cost — a windowed edge's
	// weight depends on when you reach its tail, which is exactly the
	// time-expanded-graph property plain Dijkstra still satisfies here
	// because waiting dominates any edge's transit.
	dist := map[string]int64{self.ID: now}
	first := make(map[string]string)
	visited := map[string]bool{}
	for {
		var cur string
		curDist := int64(math.MaxInt64)
		for id, d := range dist {
			if !visited[id] && d < curDist {
				cur, curDist = id, d
			}
		}
		if cur == "" {
			return "", 0
		}
		visited[cur] = true
		if cur == dest {
			return first[dest], curDist
		}
		curNode := self
		if cur != self.ID {
			n, ok := byID[cur]
			if !ok {
				continue
			}
			// An intermediate must be able to hold custody usefully: online
			// now, or scheduled (it published windows it will wake for). An
			// offline node with no plan is a parking lot the sweep cannot
			// leave.
			if n.Status != "online" && len(n.Contacts) == 0 {
				continue
			}
			curNode = n
		}
		for _, e := range contactEdges(curNode) {
			nb := e.peer
			if visited[nb] {
				continue
			}
			if cur == self.ID && exclude[nb] {
				continue // first hop back to the sender is an echo
			}
			if nb != dest {
				nbRow, ok := byID[nb]
				if !ok {
					continue
				}
				// The next hop must exist as a custody holder at all — and
				// for a live edge, must be up right now.
				if e.live && nbRow.Status != "online" {
					continue
				}
			}
			var nd int64
			if e.live {
				nd = curDist // a live edge at the tail is traversable on arrival
			} else {
				start, done, ok := e.contact.SendAt(curDist, sizeBytes)
				if !ok || start > now+contactHorizonSec {
					continue
				}
				nd = done
			}
			if expiresAt > 0 && nd > expiresAt {
				continue // arrives dead — a path that cannot beat the lifetime is no path
			}
			if old, ok := dist[nb]; !ok || nd < old {
				dist[nb] = nd
				if cur == self.ID {
					first[nb] = nb
				} else {
					first[nb] = first[cur]
				}
			}
		}
	}
}

// contactEdge is one traversable edge: either a live adjacency or a
// scheduled contact window.
type contactEdge struct {
	peer    string
	live    bool
	contact ledger.Contact
}

// contactEdges lists u's outgoing edges: every advertised live neighbor, and
// every advertised contact window.
func contactEdges(u ledger.Node) []contactEdge {
	out := make([]contactEdge, 0, len(u.Neighbors)+len(u.Contacts))
	for _, nb := range u.Neighbors {
		out = append(out, contactEdge{peer: nb, live: true})
	}
	for _, ct := range u.Contacts {
		out = append(out, contactEdge{peer: ct.Peer, contact: ct})
	}
	return out
}
