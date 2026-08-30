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
	"github.com/Xustalis/OpenPanda/internal/ledger"
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
	Adapter      *string  `json:"adapter"`
	InstallCheck *string  `json:"install_check"`
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
	if eng := h.currentEngine(); eng != nil {
		return eng.CardPath()
	}
	return ""
}

// nodesAddRequest is the body of POST /api/nodes/add.
type nodesAddRequest struct {
	Addr string `json:"addr"`
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
	if _, _, err := net.SplitHostPort(req.Addr); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("addr must be host:port"))
		return
	}
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}

	secret := h.cfg.Network.SharedSecret
	generated := false
	if secret == "" {
		var err error
		secret, err = generatePanelSecret()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, errors.New("generate shared secret failed"))
			return
		}
		generated = true
	}

	added := true
	peers := slices.Clone(h.cfg.Network.Peers)
	if slices.Contains(peers, req.Addr) {
		added = false
	} else {
		peers = append(peers, req.Addr)
	}
	if h.configPath != "" {
		if err := config.UpdateNetworkSection(h.configPath, config.NetworkConfig{
			ListenAddr:   h.cfg.Network.ListenAddr,
			SharedSecret: secret,
			Peers:        peers,
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	// Keep the live config in sync so a follow-up add sees the new peer and
	// the engine holder's next rebuild dials it too.
	h.cfg.Network.Peers = peers
	h.cfg.Network.SharedSecret = secret

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

	listen := h.cfg.Network.ListenAddr
	if host, port, err := net.SplitHostPort(listen); err == nil && host == "" {
		listen = "<this-machine>" + port
	}
	writeJSON(w, nodesAddResult{
		Addr:           req.Addr,
		Added:          added,
		SecretGen:      generated,
		Dialed:         dialed,
		DialError:      dialErr,
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
