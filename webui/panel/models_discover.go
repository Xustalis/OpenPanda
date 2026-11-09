// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// The registry's "what can this machine already serve" pair: one read that
// asks the four collectors, one write that turns the ticked candidates into
// rows. Both are thin — the collection lives in internal/localmodels, the row
// shape in config — so this file is only the boundary where a candidate meets
// the live registry: what is already there, what alias is free, and what the
// entry client refuses to dial.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/localmodels"
)

// discoverTimeout bounds the whole scan. The OpenCode CLI alone can take ten
// seconds on a cold cache, and the console shows a spinner for the duration, so
// the ceiling is generous but finite.
const discoverTimeout = 45 * time.Second

// importVerifyBudget bounds the whole loopback gate of one import. Individual
// checks are still capped at 12s, but they run concurrently: twenty ticked
// local rows must finish inside a browser's patience, not twenty times it.
const importVerifyBudget = 20 * time.Second

// discoverModels serves GET /api/models/discover.
func (h *handler) discoverModels(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), discoverTimeout)
	defer cancel()
	sources := localmodels.Discover(ctx, localmodels.Options{})
	h.markRegistered(sources)
	writeJSON(w, map[string]any{"sources": sources})
}

// ccswitchModels serves GET /api/models/ccswitch: CC Switch's four app rosters
// as independent groups. It is a separate endpoint from discovery because the
// console shows it behind its own button and one group at a time — a single
// mixed list of thirty rows is what this split exists to avoid.
func (h *handler) ccswitchModels(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), discoverTimeout)
	defer cancel()
	groups := localmodels.CCSwitchGroups(ctx, localmodels.Options{})
	h.markRegistered(groups)
	writeJSON(w, map[string]any{"groups": groups})
}

// markRegistered fills Candidate.Existing.
//
// "Already registered" is a property of this config, not of the machine: the
// same endpoint+model pair can be a fresh find on another node. Marking it here
// keeps the console from offering a duplicate the write would only suffix into
// `-2`.
func (h *handler) markRegistered(sources []localmodels.Source) {
	h.readCfg(func(c *config.Config) {
		registered := make(map[string]bool, len(c.Models)+1)
		note := func(m config.ModelConfig) {
			if m.Model != "" {
				registered[discoverRowKey(m.BaseURL, m.Model)] = true
			}
		}
		for _, m := range c.Models {
			note(m)
		}
		note(c.Model)
		for si := range sources {
			for ci := range sources[si].Candidates {
				cand := &sources[si].Candidates[ci]
				cand.Existing = registered[discoverRowKey(cand.BaseURL, cand.Model)]
			}
		}
	})
}

// modelIdentity is the triple entry.Client keys a row on: two rows that agree on
// it are one row, whatever their aliases say.
func modelIdentity(mc config.ModelConfig) string {
	return strings.ToLower(mc.NormalizedAPIType()) + "|" +
		strings.ToLower(strings.TrimRight(strings.TrimSpace(mc.BaseURL), "/")) + "|" +
		strings.TrimSpace(mc.Model)
}

// discoverRowKey is the identity of a row for duplicate purposes: the endpoint
// it dials and the model it asks for, both normalized the way the entry client
// normalizes them.
func discoverRowKey(base, model string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(base), "/")) + "\x00" + strings.TrimSpace(model)
}

// modelImportItem is one candidate the console ticked. It carries the
// credential *reference* the discovery JSON reported, never a secret: the
// server resolves it while writing, so a panel session cannot be replayed to
// read a key.
type modelImportItem struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	BaseURL       string `json:"base_url,omitempty"`
	APIType       string `json:"api_type,omitempty"`
	KeyRef        string `json:"key_ref,omitempty"`
	NoAuth        bool   `json:"no_auth,omitempty"`
	Auth          string `json:"auth,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	Alias         string `json:"alias,omitempty"`
}

// importModels serves POST /api/models/import — the batch form of POST
// /api/models, used by the discover sheet.
//
// One config write for the whole batch: the sheet can tick twenty rows, and
// twenty rewrites of the models block would leave twenty snapshots behind for
// one user action.
func (h *handler) importModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []modelImportItem `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return
	}
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("config not loaded"))
		return
	}
	if len(req.Items) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("no models to import"))
		return
	}
	if len(req.Items) > 500 {
		writeErr(w, http.StatusBadRequest, errors.New("too many models in one import"))
		return
	}

	opt := localmodels.Options{}
	// KeyRef is a capability — "env:ANY_VAR", "claude:settings",
	// "codex:<provider>" — not a label the client gets to invent. The import
	// body is client-supplied, so accept only references that discovery
	// actually reported for the same host; otherwise a panel session could
	// name OPENPANDA_SHARED_SECRET and pair it with an attacker endpoint.
	refCtx, refCancel := context.WithTimeout(r.Context(), discoverTimeout)
	allowedRefs := discoveredKeyRefs(localmodels.Discover(refCtx, opt))
	refCancel()
	var added []config.ModelConfig
	var skipped []string
	var pending []struct {
		mc   config.ModelConfig
		item modelImportItem
	}
	for _, item := range req.Items {
		if err := checkImportKeyRef(item, allowedRefs); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", item.Model, err))
			continue
		}
		mc, err := h.rowFromImport(item, opt)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", item.Model, err))
			continue
		}
		pending = append(pending, struct {
			mc   config.ModelConfig
			item modelImportItem
		}{mc, item})
	}
	// A loopback row is cheap to check and expensive to get wrong: the proxy in
	// front of it is the thing that decides which model ids exist, and "the
	// config names it" is not the same answer. Remote rows are not checked —
	// fifty catalogue entries would make one import a minute of network — and
	// the loopback checks run together under one budget, because a server that
	// hangs must not spend twelve seconds per ticked row.
	//
	// A cancelled request imports nothing it could not confirm: the browser
	// going away is not a verdict, and the user would otherwise see "import
	// failed" after every row was written.
	checkCtx, cancel := context.WithTimeout(r.Context(), importVerifyBudget)
	defer cancel()
	verdicts := make([]error, len(pending))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range pending {
		if !localmodels.IsLoopback(pending[i].mc.BaseURL) {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			verdicts[i] = localmodels.Verify(checkCtx, pending[i].mc, 12*time.Second)
		}(i)
	}
	wg.Wait()
	for i, p := range pending {
		if err := verdicts[i]; err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", p.item.Model, err))
			continue
		}
		added = append(added, p.mc)
	}
	if len(added) == 0 {
		writeJSON(w, map[string]any{"added": 0, "aliases": []string{}, "skipped": skipped})
		return
	}

	var stored []config.ModelConfig
	relabeled := 0
	err := h.mutateCfgErr(func(c *config.Config) error {
		stored = stored[:0]
		relabeled = 0
		// Identity, not alias: an import that lands on an endpoint+dialect+model
		// the registry already holds is the same row, and suffixing its alias
		// with -2 would create a duplicate that the health check cannot tell
		// apart (entry.Client keys on exactly this triple).
		seen := make(map[string]int, len(c.Models)+len(added))
		for i, m := range c.Models {
			seen[modelIdentity(m)] = i
		}
		for _, mc := range added {
			id := modelIdentity(mc)
			if idx, ok := seen[id]; ok {
				// The row is already here, but this is the only moment the
				// machine tells us where it came from: backfill the provenance
				// so a connection imported before the field existed is still
				// named after the tool that reported it. Not a duplicate —
				// nothing is added, only labelled.
				if mc.Source != "" && c.Models[idx].Source == "" {
					c.Models[idx].Source = mc.Source
					relabeled++
				}
				skipped = append(skipped, fmt.Sprintf("%s: already registered", mc.Model))
				continue
			}
			seen[id] = len(c.Models)
			mc.Name = freeAliasIn(c, defaultAlias(mc), config.ModelConfig{})
			// Append inside the loop: freeAliasIn reads the config, so the next
			// row in this batch has to be visible to it or two candidates with
			// one model id would both claim the same alias.
			c.Models = append(c.Models, mc)
			stored = append(stored, mc)
		}
		// Nothing new is not a reason to rewrite (and snapshot) the file — but
		// a backfilled label is a change, and it is the only one this pass made.
		if (len(stored) > 0 || relabeled > 0) && h.configPath != "" {
			return config.UpdateModelsSection(h.configPath, append([]config.ModelConfig(nil), c.Models...))
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// No active model at all → make the first import live, the same rule the
	// single add follows: a fresh install should be able to ask immediately.
	var noActive bool
	h.readCfg(func(c *config.Config) {
		noActive = c.Model.BaseURL == "" && c.Model.Provider == "" && c.Model.Model == ""
	})
	if noActive && len(stored) > 0 {
		if err := h.applyModelConfig(stored[0]); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}

	aliases := make([]string, 0, len(stored))
	for _, m := range stored {
		aliases = append(aliases, m.Alias())
	}
	writeJSON(w, map[string]any{"added": len(stored), "labeled": relabeled, "aliases": aliases, "skipped": skipped})
}

// discoveredKeyRefs indexes the credential references discovery reported,
// keyed by (reference, host). The host half is what keeps a legitimate
// reference from being paired with an endpoint the caller chose.
func discoveredKeyRefs(sources []localmodels.Source) map[string]bool {
	out := make(map[string]bool)
	for _, s := range sources {
		for _, c := range s.Candidates {
			if c.KeyRef == "" {
				continue
			}
			out[strings.ToLower(c.KeyRef)+"\x00"+urlHost(c.BaseURL)] = true
		}
	}
	return out
}

// checkImportKeyRef refuses a credential reference the client invented, or one
// paired with a host other than the one discovery reported it for. A row with
// no reference reads no secret, and a row with no base_url dials its provider's
// catalogue endpoint, so neither needs the host half.
func checkImportKeyRef(item modelImportItem, allowed map[string]bool) error {
	ref := strings.ToLower(strings.TrimSpace(item.KeyRef))
	if ref == "" || strings.TrimSpace(item.BaseURL) == "" {
		return nil
	}
	host := urlHost(item.BaseURL)
	if !allowed[ref+"\x00"+host] {
		return fmt.Errorf("credential reference %q was not discovered for %s", item.KeyRef, host)
	}
	return nil
}

func urlHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// rowFromImport validates one item and builds the config row.
//
// The address check is the same one the add sheet's custom endpoint goes
// through: the import body names a URL the daemon will dial later, so a caller
// who holds the panel token must not be able to point it at a metadata service.
func (h *handler) rowFromImport(item modelImportItem, opt localmodels.Options) (config.ModelConfig, error) {
	model := strings.TrimSpace(item.Model)
	if model == "" {
		return config.ModelConfig{}, errors.New("no model id")
	}
	cand := localmodels.Candidate{
		Provider:      strings.TrimSpace(item.Provider),
		Model:         model,
		BaseURL:       strings.TrimSpace(item.BaseURL),
		APIType:       normalizeAPIType(strings.TrimSpace(item.APIType)),
		KeyRef:        strings.TrimSpace(item.KeyRef),
		NoAuth:        item.NoAuth,
		Auth:          strings.TrimSpace(item.Auth),
		ContextWindow: item.ContextWindow,
		Importable:    true,
	}
	mc, err := cand.Row(opt)
	if err != nil {
		return config.ModelConfig{}, err
	}
	if mc.Provider == "custom" || cand.BaseURL != "" {
		if err := checkCustomBaseURL(mc.BaseURL); err != nil {
			return config.ModelConfig{}, err
		}
	}
	// The client constructor is the last word on whether this row can exist: it
	// resolves the dialect, the key and the endpoint guard, so a candidate that
	// would fail on its first turn is refused here instead of being written.
	if _, err := entry.NewClient(mc); err != nil {
		return config.ModelConfig{}, err
	}
	if item.Alias != "" {
		mc.Name = strings.TrimSpace(item.Alias)
	}
	return mc, nil
}
