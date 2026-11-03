// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// The configured dial list, as a first-class console surface.
//
// `nodes add` answers "what do I do next on the other machine", which is the
// right response to a fresh add but the wrong one for managing a list that is
// already there. The device card needs the list itself — address, trust tier,
// and the directory row the address currently resolves to — plus a way to
// rename or remove an entry. Reads never expose network.shared_secret; the
// join guide already says which file it lives in.

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

var (
	errPeerNotFound = errors.New("peer not found in the dial list")
	errPeerConflict = errors.New("another dial-list entry already uses that address")
)

// peerJSON is one configured network.peers entry, joined with the capability
// directory row its address currently resolves to (when the daemon has seen
// the peer).
type peerJSON struct {
	Addr     string `json:"addr"`
	Trust    string `json:"trust"`
	NodeID   string `json:"node_id,omitempty"`
	Name     string `json:"name,omitempty"`
	Status   string `json:"status,omitempty"`
	Online   bool   `json:"online"`
	LastSeen string `json:"last_seen,omitempty"`
}

// peersView is GET /api/peers. HasSecret tells the card whether adding a peer
// will mint a shared secret (and therefore whether the join guide matters).
type peersView struct {
	Peers      []peerJSON `json:"peers"`
	ListenAddr string     `json:"listen_addr,omitempty"`
	HasSecret  bool       `json:"has_secret"`
}

// listPeers serves GET /api/peers — the dial list, not the task directory.
// The nodes page already has /api/nodes for rows; this answers the different
// question the add-device card asks: what will this node dial on startup?
func (h *handler) listPeers(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	var peers []config.PeerEntry
	var listenAddr string
	var hasSecret bool
	h.readCfg(func(c *config.Config) {
		peers = append([]config.PeerEntry(nil), c.Network.Peers...)
		listenAddr = c.Network.ListenAddr
		hasSecret = c.Network.SharedSecret != ""
	})

	bindings := map[string]string{}
	nodes := map[string]ledger.Node{}
	if h.db != nil {
		if b, err := ledger.PeerAddrBindings(h.db); err == nil {
			bindings = b
		}
		if rows, err := ledger.Query(h.db, "", ""); err == nil {
			for _, n := range rows {
				nodes[n.ID] = n
			}
		}
	}

	out := make([]peerJSON, 0, len(peers))
	for _, p := range peers {
		row := peerJSON{Addr: p.Addr, Trust: p.Trust}
		if row.Trust == "" {
			row.Trust = "mesh"
		}
		if id := bindings[core.NormalizeDialAddr(p.Addr)]; id != "" {
			row.NodeID = id
			if n, ok := nodes[id]; ok {
				row.Name = scheduler.NodeNamePart(n.ID)
				if row.Name == "" {
					row.Name = n.Name
				}
				row.Status = n.Status
				row.Online = n.Status == "online"
				if n.LastSeen > 0 {
					row.LastSeen = time.Unix(n.LastSeen, 0).UTC().Format(time.RFC3339)
				}
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].Addr, out[j].Addr) < 0 })
	writeJSON(w, peersView{Peers: out, ListenAddr: listenAddr, HasSecret: hasSecret})
}

// removePeer serves DELETE /api/peers?addr=… — drop one entry from the dial
// list. The address is matched canonically, so removing the bare host:port
// also removes the equivalent ws:// spelling. The running engine's SyncPeers
// then closes the keepalive loop; the peer itself is untouched.
func (h *handler) removePeer(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimSpace(r.URL.Query().Get("addr"))
	if addr == "" {
		writeErr(w, http.StatusBadRequest, errors.New("addr is required"))
		return
	}
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	want := canonicalPeerAddr(addr)
	removed := false
	err := h.mutateCfgErr(func(c *config.Config) error {
		remaining := make([]config.PeerEntry, 0, len(c.Network.Peers))
		for _, e := range c.Network.Peers {
			if canonicalPeerAddr(e.Addr) == want {
				removed = true
				continue
			}
			remaining = append(remaining, e)
		}
		if !removed {
			return nil
		}
		if h.configPath != "" {
			if err := config.UpdateNetworkSection(h.configPath, config.NetworkConfig{Peers: remaining}); err != nil {
				return err
			}
		}
		c.Network.Peers = remaining
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !removed {
		writeErr(w, http.StatusNotFound, errPeerNotFound)
		return
	}
	writeJSON(w, map[string]any{"addr": addr, "removed": true})
}

// canonicalPeerAddr is the dial list's equality key. It keeps the transport
// scheme (ws and wss are not interchangeable), lowercases the host, drops a
// trailing slash, and treats a bare host:port as its implicit ws:// form.
// core.NormalizeDialAddr is deliberately not used here: it strips the scheme
// for the addr→node binding, where ws:// and wss:// must compare equal.
func canonicalPeerAddr(addr string) string {
	s := strings.TrimSpace(addr)
	if strings.HasPrefix(s, "punch:") {
		return "punch:" + strings.ToLower(strings.TrimPrefix(s, "punch:"))
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			scheme := strings.ToLower(u.Scheme)
			host := strings.ToLower(u.Host)
			path := strings.TrimRight(u.EscapedPath(), "/")
			return scheme + "://" + host + path
		}
		return strings.ToLower(strings.TrimRight(s, "/"))
	}
	if host, port, err := net.SplitHostPort(s); err == nil {
		return "ws://" + strings.ToLower(host) + ":" + port
	}
	return strings.ToLower(strings.TrimRight(s, "/"))
}
