// SPDX-License-Identifier: AGPL-3.0-or-later

package localmodels

// Local inference servers publish their roster on a fixed port, so discovery
// does not need a config file to find them — it needs a short list of the ports
// the usual suspects listen on and a timeout short enough that a closed port
// costs nothing.
//
// The keys here are placeholders, not secrets: Ollama, LM Studio and friends
// ignore the Authorization header, but several of them reject a request that
// carries none at all, so one is sent. Unsloth is the exception and the reason
// apiKeyEnv exists.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/providers"
)

type localHost struct {
	id    string
	label string
	base  string
	// apiKey is the placeholder bearer most of these servers ignore.
	apiKey string
	// apiKeyEnv names a real token when the server has one (Unsloth Studio).
	apiKeyEnv string
}

var localHosts = []localHost{
	{id: "omlx", label: "oMLX", base: "http://127.0.0.1:8080/v1", apiKey: "omlx"},
	{id: "ollama", label: "Ollama", base: "http://127.0.0.1:11434/v1", apiKey: "ollama"},
	{id: "local_ollama", label: "Ollama", base: "http://127.0.0.1:11434/v1", apiKey: "ollama"},
	{id: "exo", label: "EXO", base: "http://127.0.0.1:52415/v1", apiKey: "exo"},
	{id: "lmstudio", label: "LM Studio", base: "http://127.0.0.1:1234/v1", apiKey: "lm-studio"},
	{id: "unsloth", label: "Unsloth", base: "http://127.0.0.1:8888/v1", apiKeyEnv: "UNSLOTH_STUDIO_AUTH_TOKEN"},
}

// collectLocal probes every distinct loopback endpoint once.
func collectLocal(ctx context.Context, opt Options) Source {
	src := Source{ID: SourceLocal, Label: "Local servers", Status: StatusEmpty}

	// ollama and local_ollama are the same port under two ids; probing twice
	// would double every row.
	seenBase := make(map[string]bool)
	var hosts []localHost
	for _, h := range localHosts {
		if seenBase[h.base] {
			continue
		}
		seenBase[h.base] = true
		hosts = append(hosts, h)
	}

	for _, h := range hosts {
		key := h.apiKey
		keyRef := ""
		placeholder := false
		if h.apiKeyEnv != "" {
			if v := opt.env(h.apiKeyEnv); v != "" {
				key = v
				keyRef = "env:" + h.apiKeyEnv
			}
		}
		if key != "" && keyRef == "" {
			// A built-in placeholder ("lm-studio"): some servers reject a
			// request with no Authorization at all, so the row carries it. It
			// is not a secret, and the console marks it as a placeholder.
			keyRef = "literal:" + key
			placeholder = true
		}
		ids := probeModelIDs(ctx, h.base, key, opt.ProbeTimeout)
		if len(ids) == 0 {
			continue
		}
		loaded, windows := localState(ctx, h, key, opt.ProbeTimeout)

		providerID := "custom"
		if p, ok := providers.Lookup(h.id); ok {
			providerID = p.ID
		}
		for _, id := range ids {
			src.Candidates = append(src.Candidates, Candidate{
				Source:        SourceLocal,
				Provider:      providerID,
				ProviderLabel: h.label,
				Model:         id,
				Label:         id,
				BaseURL:       strings.TrimRight(h.base, "/"),
				APIType:       "openai",
				KeyRef:        keyRef,
				Placeholder:   placeholder,
				NoAuth:        key == "",
				ContextWindow: windows[id],
				Loaded:        loaded[id],
				Suggested:     true,
				Importable:    true,
				Note:          h.label,
			})
		}
	}

	if len(src.Candidates) > 0 {
		src.Status = StatusOK
		src.Detail = strconv.Itoa(len(src.Candidates)) + " model(s) answering on loopback"
	} else {
		src.Detail = "no inference server answering on the usual ports"
	}
	return src
}

// localState reads the server-specific "what is loaded right now" endpoint.
// Ollama's /api/ps is the only one that also reports a context length, which is
// worth having: /v1/models never carries it, and a row without a window falls
// back to the client's conservative default.
//
// Every failure path is silent — the catalogue probe already succeeded, and a
// missing /api/ps only means "no loaded flag", not "no models".
func localState(ctx context.Context, h localHost, key string, timeout time.Duration) (map[string]bool, map[string]int) {
	loaded := make(map[string]bool)
	windows := make(map[string]int)
	origin := strings.TrimSuffix(h.base, "/v1")

	var url string
	switch h.id {
	case "ollama", "local_ollama":
		url = origin + "/api/ps"
	case "lmstudio":
		url = origin + "/api/v0/models"
	case "omlx":
		url = h.base + "/models/status"
	default:
		return loaded, windows
	}
	body, ok := getJSON(ctx, url, key, timeout)
	if !ok {
		return loaded, windows
	}

	var payload struct {
		Models []localRow `json:"models"`
		Data   []localRow `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return loaded, windows
	}
	rows := append(payload.Models, payload.Data...)
	for _, row := range rows {
		id := firstNonEmpty(row.Model, row.Name, row.ID)
		if id == "" {
			continue
		}
		if row.State == "" || strings.EqualFold(row.State, "loaded") || strings.EqualFold(row.State, "running") {
			loaded[id] = true
		}
		window := row.ContextLength
		if window == 0 {
			window = row.MaxContextLength
		}
		if window <= 0 {
			continue
		}
		windows[id] = window
		// `qwen3:14b` and `qwen3` are the same model to a caller that does not
		// pin a tag; keep the smaller window when both appear, because that is
		// the one that will actually fail.
		if base, _, ok := strings.Cut(id, ":"); ok && base != "" {
			if cur, seen := windows[base]; !seen || window < cur {
				windows[base] = window
			}
		}
	}
	return loaded, windows
}

type localRow struct {
	Model            string `json:"model"`
	Name             string `json:"name"`
	ID               string `json:"id"`
	State            string `json:"state"`
	ContextLength    int    `json:"context_length"`
	MaxContextLength int    `json:"max_context_length"`
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
