// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// The web console's half of the Bluetooth-style pairing ceremony — the
// same flow `panda pair` drives at the CLI:
//
//	GET  /api/pair           — discovered LAN devices, inbound requests
//	                           awaiting this node's answer, and outgoing
//	                           sessions this console started.
//	POST /api/pair/initiate  — dial a device, run the key exchange, return
//	                           the SAS both screens must show; a background
//	                           goroutine then waits out the remote human.
//	POST /api/pair/answer    — confirm or reject an inbound request; the
//	                           daemon's session goroutine does the rest.
//
// The responder side needs nothing web-specific: the pairing request lives
// in the shared pair_sessions table, so answering it from the browser is
// the same row flip the CLI performs. The initiator side holds the live
// conn in memory keyed by session id — the secrets it carries never touch
// the DB or the response body.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
)

// outgoingPair tracks one console-started pairing between the moment the
// code is shown and the moment the remote human answers — the web twin of
// `panda pair <target>`'s blocking wait.
type outgoingPair struct {
	session string
	code    string
	target  string // what the operator picked (name or addr)
	addr    string // resolved host:port
	state   string // waiting | done | rejected | expired | error
	err     string
	started time.Time
}

const pairOutgoingTTL = 10 * time.Minute

// pairView is GET /api/pair's payload.
type pairView struct {
	Requests   []pairRequestJSON `json:"requests"`
	Discovered []discoveredJSON  `json:"discovered"`
	Outgoing   []outgoingJSON    `json:"outgoing"`
}

// pairRequestJSON is one inbound pairing request awaiting this operator.
type pairRequestJSON struct {
	ID        string `json:"id"`
	PeerName  string `json:"peer_name"`
	PeerAddr  string `json:"peer_addr"`
	Code      string `json:"code"`
	State     string `json:"state"`
	ExpiresAt string `json:"expires_at"`
}

// discoveredJSON is one LAN beacon the discovery table heard recently —
// the candidate list the pair button offers.
type discoveredJSON struct {
	ID       string `json:"id"`
	Addr     string `json:"addr"`
	Verified bool   `json:"verified"`
}

// outgoingJSON reports one console-started session's state for polling.
type outgoingJSON struct {
	Session string `json:"session"`
	Target  string `json:"target"`
	Addr    string `json:"addr"`
	Code    string `json:"code"`
	State   string `json:"state"`
	Err     string `json:"err,omitempty"`
}

// pairMu/pairOutgoing hold the live initiator sessions on the handler —
// the conn itself, and the row the polling UI reads.
type pairTracker struct {
	mu       sync.Mutex
	outgoing map[string]*outgoingPair
}

// listPair serves GET /api/pair — the three lists the pairing panel
// renders: who wants to pair with me, who the LAN heard, and what my
// console-initiated sessions are doing.
func (h *handler) listPair(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("no task database"))
		return
	}
	view := pairView{
		Requests:   []pairRequestJSON{},
		Discovered: []discoveredJSON{},
		Outgoing:   []outgoingJSON{},
	}
	if sessions, err := core.ListPairSessions(h.db); err == nil {
		for _, s := range sessions {
			// Only live requests matter to the answer UI — terminal rows
			// (done/rejected/expired) would only be noise here.
			if s.State != core.PairStateReady && s.State != core.PairStateConfirmed {
				continue
			}
			view.Requests = append(view.Requests, pairRequestJSON{
				ID: s.ID, PeerName: s.PeerName, PeerAddr: s.PeerAddr,
				Code: s.SAS, State: s.State, ExpiresAt: s.ExpiresAt.UTC().Format(time.RFC3339),
			})
		}
	}
	if pending, err := ledger.ListPending(h.db, 90*time.Second); err == nil {
		for _, n := range pending {
			view.Discovered = append(view.Discovered, discoveredJSON{
				ID: n.ID, Addr: n.Addr, Verified: n.Verified,
			})
		}
	}
	h.pair.mu.Lock()
	now := time.Now()
	for id, o := range h.pair.outgoing {
		if now.Sub(o.started) > pairOutgoingTTL {
			delete(h.pair.outgoing, id)
			continue
		}
		view.Outgoing = append(view.Outgoing, outgoingJSON{
			Session: o.session, Target: o.target, Addr: o.addr,
			Code: o.code, State: o.state, Err: o.err,
		})
	}
	h.pair.mu.Unlock()
	slices.SortFunc(view.Outgoing, func(a, b outgoingJSON) int {
		return strings.Compare(b.Session, a.Session)
	})
	writeJSON(w, view)
}

// pairInitiateRequest is POST /api/pair/initiate's body: a discovered
// device id/name, or a literal host:port / ws(s):// address.
type pairInitiateRequest struct {
	Target string `json:"target"`
}

func (h *handler) initiatePair(w http.ResponseWriter, r *http.Request) {
	var req pairInitiateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	req.Target = strings.TrimSpace(req.Target)
	if req.Target == "" {
		writeErr(w, http.StatusBadRequest, errors.New("target is required"))
		return
	}
	if h.db == nil || h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("node not ready"))
		return
	}

	addr, name, err := h.resolvePairTarget(req.Target)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}

	var selfID, nodeName, listenAddr string
	h.readCfg(func(c *config.Config) {
		nodeName = c.Node.Name
		listenAddr = c.Network.ListenAddr
		selfID = core.RuntimeNodeID(c.Node.Name, c.Node.Kind, c.Node.EffectiveIdentity())
	})
	pub, _, ok := core.LoadNodeKey(h.db)
	if !ok {
		pub, _, _ = ed25519.GenerateKey(rand.Reader)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	pc, err := core.DialPair(ctx, addr, selfID, nodeName, listenAddr, pub)
	cancel()
	if err != nil {
		var rj *core.PairRejected
		if errors.As(err, &rj) {
			writeErr(w, http.StatusConflict, errors.New("pairing refused: "+rj.Reason))
			return
		}
		writeErr(w, http.StatusBadGateway, errors.New("device unreachable — is `panda daemon` running there?"))
		return
	}

	h.pair.mu.Lock()
	if h.pair.outgoing == nil {
		h.pair.outgoing = make(map[string]*outgoingPair)
	}
	h.pair.outgoing[pc.Session] = &outgoingPair{
		session: pc.Session, code: pc.Code, target: name, addr: addr,
		state: "waiting", started: time.Now(),
	}
	h.pair.mu.Unlock()

	go h.finishPair(pc, addr)
	writeJSON(w, map[string]string{
		"session": pc.Session,
		"code":    pc.Code,
		"target":  name,
		"state":   "waiting",
	})
}

// finishPair waits out the remote human on the pairing conn, then lands
// the delivered secret: config write + in-memory peers + a live dial —
// the web twin of `panda pair`'s adoptPairedSecret. The outcome goes into
// the outgoing map the UI polls.
func (h *handler) finishPair(pc *core.PairClient, addr string) {
	mark := func(state, errMsg string) {
		h.pair.mu.Lock()
		if o, ok := h.pair.outgoing[pc.Session]; ok {
			o.state, o.err = state, errMsg
		}
		h.pair.mu.Unlock()
	}
	defer pc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute+45*time.Second)
	defer cancel()
	secret, err := pc.WaitSecret(ctx)
	if err != nil {
		var rj *core.PairRejected
		switch {
		case errors.As(err, &rj):
			mark(rj.Reason, "")
		case errors.Is(err, context.DeadlineExceeded):
			mark("expired", "")
		default:
			mark("error", err.Error())
		}
		return
	}

	// The delivered secret is the mesh key — persisting it is the whole
	// point of pairing (unlike env-injected secrets, which must stay out
	// of the file; this one has nowhere else to live).
	err = h.mutateCfgErr(func(c *config.Config) error {
		peers := slices.Clone(c.Network.Peers)
		if !slices.Contains(peers, addr) {
			peers = append(peers, addr)
		}
		if h.configPath != "" {
			if err := config.UpdateNetworkSection(h.configPath, config.NetworkConfig{
				SharedSecret: secret,
				Peers:        peers,
			}); err != nil {
				return err
			}
		}
		c.Network.Peers = peers
		c.Network.SharedSecret = secret
		return nil
	})
	if err != nil {
		mark("error", err.Error())
		return
	}
	if eng := h.currentEngine(); eng != nil {
		dctx, dcancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = eng.DialPeer(dctx, addr)
		dcancel()
	}
	mark("done", "")
}

// pairAnswerRequest is POST /api/pair/answer's body.
type pairAnswerRequest struct {
	ID      string `json:"id"`
	Confirm bool   `json:"confirm"`
}

func (h *handler) answerPair(w http.ResponseWriter, r *http.Request) {
	var req pairAnswerRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("id is required"))
		return
	}
	if h.db == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("no task database"))
		return
	}
	state := core.PairStateRejected
	if req.Confirm {
		state = core.PairStateConfirmed
	}
	if err := core.AnswerPairSession(h.db, req.ID, state); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if !req.Confirm {
		writeJSON(w, map[string]string{"state": core.PairStateRejected})
		return
	}
	// The daemon's confirm goroutine resolves within about a second of the
	// flip; poll briefly so the UI reports "joined" rather than "probably".
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		st, err := core.PairSessionState(h.db, req.ID)
		if err != nil {
			break
		}
		if st == core.PairStateDone || st == core.PairStateExpired || st == core.PairStateRejected {
			writeJSON(w, map[string]string{"state": st})
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	writeJSON(w, map[string]string{"state": core.PairStateConfirmed})
}

// resolvePairTarget mirrors the CLI's resolution: a pending-discovery
// id/name/addr first, then a literal dialable address.
func (h *handler) resolvePairTarget(target string) (addr, name string, err error) {
	if pending, perr := ledger.ListPending(h.db, 90*time.Second); perr == nil {
		for _, n := range pending {
			if n.ID == target || scheduler.NodeNamePart(n.ID) == target || n.Addr == target {
				return n.Addr, scheduler.NodeNamePart(n.ID), nil
			}
		}
	}
	if config.ValidatePeerAddr(target) == nil {
		return target, scheduler.NodeNamePart(target), nil
	}
	return "", "", errors.New("no discovered device or valid address matches " + target)
}
