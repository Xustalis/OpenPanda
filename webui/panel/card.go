// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/cardmut"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/trust"
)

// The card API — the web console's read/edit path for this node's
// capabilities.yaml, mirroring `panda card` and the REPL's /card. Reads go
// through GET /api/card (the parsed card plus the raw YAML, so the console
// can offer both a structured editor and a raw one); writes are the same
// cardmut mutations the CLI performs, followed by a live ReloadCard on the
// ask engine so the edit is in force for the next routed task without a
// restart. Every cardmut call validates the candidate document through
// ledger.LoadCard and keeps a .bak before installing, so a bad edit is
// refused here rather than taking the node down at its next start.

// cardJSON is the wire form of GET /api/card: the parsed card (snake_case,
// mirroring ledger's own JSON tags) plus the raw YAML text and the path it
// was read from. Raw is what the raw-editor textarea edits; card is what the
// structured editor renders.
type cardJSON struct {
	Path string      `json:"path"`
	Raw  string      `json:"raw"`
	Card ledger.Card `json:"card"`
}

// getCard serves GET /api/card — the local capability card.
func (h *handler) getCard(w http.ResponseWriter, r *http.Request) {
	path := h.cardPath()
	if path == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("capability card not configured"))
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, errors.New("no capability card — run `panda card rescan --write` first"))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	card, err := ledger.LoadCard(path)
	if err != nil {
		// The file exists but does not parse: surface the error with the raw
		// text so the raw editor can fix it, rather than hiding the card.
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, cardJSON{Path: path, Raw: string(raw), Card: card})
}

// putCardRaw serves PUT /api/card — the whole-file replacement (the raw
// editor's save). Validation and the .bak are cardmut.WriteRaw's contract;
// the engine reloads the card when one is live.
func (h *handler) putCardRaw(w http.ResponseWriter, r *http.Request) {
	path := h.cardPath()
	if path == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("capability card not configured"))
		return
	}
	var req struct {
		YAML string `json:"yaml"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if strings.TrimSpace(req.YAML) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("yaml must not be empty"))
		return
	}
	if err := cardmut.WriteRaw(path, []byte(req.YAML)); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	h.reloadCardLive(w, r)
}

// nativeRequest is the body of POST /api/card/native (add) — the fields
// `panda card native add` takes as flags.
type nativeRequest struct {
	ID          string   `json:"id"`
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	Tier        int      `json:"tier"`
	Description string   `json:"description"`
}

// addNative serves POST /api/card/native — add one native ability.
func (h *handler) addNative(w http.ResponseWriter, r *http.Request) {
	path := h.cardPath()
	if path == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("capability card not configured"))
		return
	}
	var req nativeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" || req.Command == "" {
		writeErr(w, http.StatusBadRequest, errors.New("id and command are required"))
		return
	}
	if req.Tier == 0 {
		req.Tier = 1
	}
	if err := cardmut.NativeAdd(path, ledger.NativeAbility{
		ID:          req.ID,
		Command:     req.Command,
		Args:        req.Args,
		Tier:        req.Tier,
		Description: req.Description,
	}); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	h.reloadCardLive(w, r)
}

// removeNative serves DELETE /api/card/native/{id}.
func (h *handler) removeNative(w http.ResponseWriter, r *http.Request) {
	path := h.cardPath()
	if path == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("capability card not configured"))
		return
	}
	if err := cardmut.NativeRemove(path, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	h.reloadCardLive(w, r)
}

// agentRequest is the body of POST /api/card/agents/{name} and the partial
// fields of PATCH. A nil/absent field means "leave the card's value alone"
// (the cardmut.AgentUpdate contract), except on POST where adapter is
// required — an agent registration without an adapter script cannot run.
type agentRequest struct {
	Adapter      *string `json:"adapter"`
	InstallCheck *string `json:"install_check"`
	// Command is the argv template generic.py expands — set it when the
	// adapter is generic.py (e.g. "zcode --prompt {prompt}"; {stdin}, {cwd},
	// {resume} and {max_turns} placeholders are also understood).
	Command      *string  `json:"command"`
	Capabilities []string `json:"capabilities"`
	BestAt       []string `json:"best_at"`
	NotFor       []string `json:"not_for"`
	CostTier     *string  `json:"cost_tier"`
	Tier         *int     `json:"tier"`
}

// agentPatch is the wire form of the PATCH body: explicit pointers so an
// omitted field ("capabilities": null/absent) is distinguishable from an
// explicit empty array ("capabilities": []) — the first leaves the card
// alone, the second clears the list.
type agentPatch struct {
	Adapter      *string   `json:"adapter"`
	InstallCheck *string   `json:"install_check"`
	Command      *string   `json:"command"`
	Capabilities *[]string `json:"capabilities"`
	BestAt       *[]string `json:"best_at"`
	NotFor       *[]string `json:"not_for"`
	CostTier     *string   `json:"cost_tier"`
	Tier         *int      `json:"tier"`
}

// addAgent serves POST /api/card/agents/{name} — register one agent CLI.
func (h *handler) addAgent(w http.ResponseWriter, r *http.Request) {
	path := h.cardPath()
	if path == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("capability card not configured"))
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("agent name is required"))
		return
	}
	var req agentRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if req.Adapter == nil || strings.TrimSpace(*req.Adapter) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("adapter is required"))
		return
	}
	tier := 2 // agents run arbitrary shell: the safe default is tier-2 (P1-15)
	if req.Tier != nil {
		tier = *req.Tier
	}
	ag := ledger.Agent{
		Adapter:      strings.TrimSpace(*req.Adapter),
		InstallCheck: derefString(req.InstallCheck),
		Command:      derefString(req.Command),
		Capabilities: req.Capabilities,
		BestAt:       req.BestAt,
		NotFor:       req.NotFor,
		CostTier:     derefString(req.CostTier),
		Tier:         tier,
	}
	if err := cardmut.AgentAdd(path, name, ag); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	h.reloadCardLive(w, r)
}

// patchAgent serves PATCH /api/card/agents/{name} — partial update of one
// agent (tier toggle, capabilities edit, …). Only the fields present in
// the body are rewritten; the rest of the agent's entry — and its comments
// — survive byte-for-byte.
func (h *handler) patchAgent(w http.ResponseWriter, r *http.Request) {
	path := h.cardPath()
	if path == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("capability card not configured"))
		return
	}
	name := r.PathValue("name")
	var req agentPatch
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	upd := cardmut.AgentUpdate{
		Adapter:      req.Adapter,
		InstallCheck: req.InstallCheck,
		Command:      req.Command,
		Capabilities: req.Capabilities,
		BestAt:       req.BestAt,
		NotFor:       req.NotFor,
		CostTier:     req.CostTier,
		Tier:         req.Tier,
	}
	if err := cardmut.AgentSet(path, name, upd); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	h.reloadCardLive(w, r)
}

// removeAgent serves DELETE /api/card/agents/{name}.
func (h *handler) removeAgent(w http.ResponseWriter, r *http.Request) {
	path := h.cardPath()
	if path == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("capability card not configured"))
		return
	}
	if err := cardmut.AgentRemove(path, r.PathValue("name")); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	h.reloadCardLive(w, r)
}

// manualRequest is the body of POST /api/card/manual.
type manualRequest struct {
	ID     string `json:"id"`
	Notify string `json:"notify"`
}

// addManual serves POST /api/card/manual — add one human-performed ability.
func (h *handler) addManual(w http.ResponseWriter, r *http.Request) {
	path := h.cardPath()
	if path == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("capability card not configured"))
		return
	}
	var req manualRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" || req.Notify == "" {
		writeErr(w, http.StatusBadRequest, errors.New("id and notify are required"))
		return
	}
	if err := cardmut.ManualAdd(path, ledger.ManualAbility{ID: req.ID, Notify: req.Notify}); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	h.reloadCardLive(w, r)
}

// removeManual serves DELETE /api/card/manual/{id}.
func (h *handler) removeManual(w http.ResponseWriter, r *http.Request) {
	path := h.cardPath()
	if path == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("capability card not configured"))
		return
	}
	if err := cardmut.ManualRemove(path, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	h.reloadCardLive(w, r)
}

// reloadCardLive applies a card edit to the running engine — the web twin of
// the REPL's reloadCardLive. A live engine hot-swaps (scheduler re-reads the
// file, re-registers, tells peers); without one the write is still durable,
// the daemon picks it up at its next start (or via SIGHUP).
func (h *handler) reloadCardLive(w http.ResponseWriter, r *http.Request) {
	reloaded := false
	if eng := h.currentEngine(); eng != nil {
		if err := eng.ReloadCard(h.cardPath()); err != nil {
			writeErr(w, http.StatusInternalServerError, errors.New("card saved but engine reload failed: "+err.Error()))
			return
		}
		reloaded = true
	}
	writeJSON(w, map[string]any{"status": "saved", "live": reloaded})
}

// cardPath resolves the card the panel edits: the configured CardPath, then
// the live engine's own card (the holder may have built the engine from a
// card the static Deps did not know).
func (h *handler) cardPath() string {
	if h.cardFilePath != "" {
		return h.cardFilePath
	}
	if eng := h.currentEngine(); eng != nil && eng.CardPath() != "" {
		return eng.CardPath()
	}
	path := ""
	h.readCfg(func(c *config.Config) { path = c.EffectiveCardPath() })
	return path
}

// nodesAddRequest is the body of POST /api/nodes/add.
type nodesAddRequest struct {
	Addr  string `json:"addr"`
	Trust string `json:"trust,omitempty"`
}

// nodesAddResult reports what `panda nodes add` prints at the CLI: whether
// the secret was generated (the other machine needs it copied across),
// whether the peer was already configured, and the join guide's three
// steps plus the values they reference — the console renders the same
// copy-paste instructions instead of inventing its own.
type nodesAddResult struct {
	Addr           string   `json:"addr"`
	Added          bool     `json:"added"`
	SecretGen      bool     `json:"secret_generated"`
	Dialed         bool     `json:"dialed"`
	DialError      string   `json:"dial_error,omitempty"`
	CleartextHint  bool     `json:"cleartext_hint,omitempty"`
	ConfigPath     string   `json:"config_path"`
	ListenAddr     string   `json:"listen_addr"`
	InviteSteps    []string `json:"invite_steps"`
	InstallCommand string   `json:"install_command"`
}

// addNode serves POST /api/nodes/add — the web console's join-a-device
// path, the same flow as `panda nodes add`: validate the address, ensure a
// shared secret exists (generating one when missing), append the peer to
// config.yaml, then dial the peer live when an engine is running so the
// session picks it up without a restart. The response carries the join
// guide for the other machine (never the secret itself — only the file it
// lives in, so browser history and server logs never hold it).
func (h *handler) addNode(w http.ResponseWriter, r *http.Request) {
	var req nodesAddRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	req.Addr = strings.TrimSpace(req.Addr)
	if req.Addr == "" {
		writeErr(w, http.StatusBadRequest, errors.New("addr is required"))
		return
	}
	if err := config.ValidatePeerAddr(req.Addr); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("addr must be host:port, punch:<node-id>, or a ws(s):// URL"))
		return
	}
	lv, err := trust.ParseLevel(req.Trust)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Mesh is the absence of a zone: an entry added without a tier (or with
	// an explicit "mesh") keeps the pre-feature scalar form in the file.
	storedTrust := trust.StoredLevel(lv)
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}

	// Read-check, mutate, and persist ride one critical section: the peers
	// contains-check outside the lock was check-then-act, and
	// UpdateNetworkSection rewrites the whole section — a concurrent save
	// between the two calls used to lose one side.
	var secret, listenAddr string
	var generated, allowCleartext bool
	var allowFor []string
	added := true
	err = h.mutateCfgErr(func(c *config.Config) error {
		listenAddr = c.Network.ListenAddr
		secret = c.Network.SharedSecret
		allowCleartext = c.Network.AllowCleartext
		allowFor = c.Network.AllowCleartextFor
		if secret == "" {
			var err error
			secret, err = generatePanelSecret()
			if err != nil {
				return errors.New("generate shared secret failed")
			}
			generated = true
		}
		peers := slices.Clone(c.Network.Peers)
		if slices.ContainsFunc(peers, func(e config.PeerEntry) bool { return e.Addr == req.Addr }) {
			added = false
		} else {
			peers = append(peers, config.PeerEntry{Addr: req.Addr, Trust: storedTrust})
		}
		if h.configPath != "" {
			// Only a secret this call generated belongs on disk — one
			// injected via OPENPANDA_SHARED_SECRET must stay env-provided.
			persistSecret := ""
			if generated {
				persistSecret = secret
			}
			if err := config.UpdateNetworkSection(h.configPath, config.NetworkConfig{
				ListenAddr:   c.Network.ListenAddr,
				SharedSecret: persistSecret,
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
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Live dial when an engine is up — the web twin of /nodes add's
	// dial-on-add. A failed dial is not a failed add: the peer is
	// configured, the next start retries it, and the UI says exactly that.
	dialed := false
	dialErr := ""
	if eng := h.currentEngine(); eng != nil {
		if err := eng.DialPeer(r.Context(), req.Addr); err != nil {
			dialErr = err.Error()
		} else {
			dialed = true
		}
	}

	listen := listenAddr
	if host, port, err := net.SplitHostPort(listen); err == nil && host == "" {
		listen = "<this-machine>" + port
	}
	writeJSON(w, nodesAddResult{
		Addr:      req.Addr,
		Added:     added,
		SecretGen: generated,
		Dialed:    dialed,
		DialError: dialErr,
		// The write succeeded but the daemon's cleartext gate may still
		// refuse every dial — surface that next to the join guide, the same
		// advisory the CLI prints, so the refusal doesn't only live in
		// daemon logs.
		CleartextHint:  core.CleartextDialError(req.Addr, allowCleartext, allowFor) != nil,
		ConfigPath:     h.configPath,
		ListenAddr:     listen,
		InstallCommand: "curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh",
		InviteSteps: []string{
			"install OpenPanda on the other machine",
			"copy network.shared_secret from " + h.configPath + " to the other machine's config.yaml (over your own channel — it must not travel in plaintext)",
			"on the other machine: panda pair --secret <secret> --peer " + listen,
		},
	})
}

// nodeTrustRequest is the body of POST /api/nodes/{id}/trust.
type nodeTrustRequest struct {
	Trust string `json:"trust"`
}

// setNodeTrust serves POST /api/nodes/{id}/trust — the console twin of
// `panda nodes trust <id> <level>`: pin the tier on the directory row (live
// — the daemon reads it on every admission/exposure decision) and keep the
// configured peer entry in step so a config reload cannot resurrect a
// cleared tier. A "k:"-prefixed id pins every sibling row sharing that
// Ed25519 identity, mirroring the CLI's stable-identity form.
func (h *handler) setNodeTrust(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req nodeTrustRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	lv, err := trust.ParseLevel(req.Trust)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Mesh is the absence of a zone: store "" so a row (and the config entry
	// synced below) that names mesh keeps the pre-feature scalar form.
	stored := trust.StoredLevel(lv)
	if h.db == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("store not available"))
		return
	}
	// The local row is not a peer: admission skips self, so a pin on it is
	// meaningless at best and a directory footgun at worst. Reject it — also
	// in the k: form when the self row carries that key.
	if self := localNodeID(h.cfg); self != "" {
		isSelf := id == self
		if !isSelf && strings.HasPrefix(id, "k:") {
			var n int
			_ = h.db.QueryRow(`SELECT COUNT(*) FROM employee_cache WHERE pub_key=? AND id=?`,
				strings.TrimPrefix(id, "k:"), self).Scan(&n)
			isSelf = n > 0
		}
		if isSelf {
			writeErr(w, http.StatusBadRequest, errors.New("cannot pin a trust tier on the local node"))
			return
		}
	}

	if strings.HasPrefix(id, "k:") {
		n, serr := ledger.SetPeerTrustByKey(h.db, strings.TrimPrefix(id, "k:"), stored)
		if serr != nil {
			writeErr(w, http.StatusInternalServerError, serr)
			return
		}
		if n == 0 {
			writeErr(w, http.StatusNotFound, errors.New("no directory rows carry that key"))
			return
		}
	} else if err := ledger.SetPeerTrust(h.db, id, stored); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}

	// Keep the configured peer entry in step when the pinned identity maps
	// to a dial address the config already knows: the addr hint and the row
	// pin are two writes of the same assertion, and a stale hint would
	// resurrect the tier on the next ApplyNetworkConfig.
	if bindings, berr := ledger.PeerAddrBindings(h.db); berr == nil && h.configPath != "" {
		wanted := map[string]bool{id: true}
		if strings.HasPrefix(id, "k:") {
			if rows, qerr := h.db.Query(`SELECT id FROM employee_cache WHERE pub_key=?`, strings.TrimPrefix(id, "k:")); qerr == nil {
				for rows.Next() {
					var sid string
					if rows.Scan(&sid) == nil {
						wanted[sid] = true
					}
				}
				rows.Close()
			}
		}
		for addr, boundID := range bindings {
			if !wanted[boundID] {
				continue
			}
			_ = h.mutateCfgErr(func(c *config.Config) error {
				peers := slices.Clone(c.Network.Peers)
				for i, e := range peers {
					if core.NormalizeDialAddr(e.Addr) == addr && e.Trust != stored {
						peers[i].Trust = stored
					}
				}
				c.Network.Peers = peers
				return config.UpdateNetworkSection(h.configPath, config.NetworkConfig{Peers: peers})
			})
		}
	}
	writeJSON(w, map[string]string{"id": id, "trust": stored})
}

// generatePanelSecret mints the HMAC material node hellos sign with — the
// same 128-bit random hex `panda nodes add` generates at the CLI.
func generatePanelSecret() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
