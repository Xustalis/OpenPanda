// SPDX-License-Identifier: AGPL-3.0-or-later

package localmodels

// Claude Code carries its endpoint in the environment, not in a model list: a
// settings.json `env` block (or the shell that launches it) holds
// ANTHROPIC_BASE_URL, a token, and up to five tier aliases. The aliases are the
// interesting part — pointing ANTHROPIC_DEFAULT_SONNET_MODEL at a third-party
// relay is how a Claude Code install is aimed at something other than Anthropic,
// and that relay is exactly the endpoint this registry wants.
//
// The tier values are also where the `[1M]` marker comes from: Claude Code
// spells a 1M-context request as `claude-opus-5[1M]`. The reference
// implementation keeps that suffix in the id (its CLAUDE_MODEL_ID allows it) and
// strips it only while comparing against the official list, so it is kept here
// too — the suffix is what selects the 1M window, and dropping it would quietly
// downgrade the row. The window is *also* recorded in context_window, because
// the registry budgets against that number.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// claudeTierKeys are the env names Claude Code reads a *user-selectable* model
// from. Two neighbouring names are deliberately absent:
//
//   - the `_NAME` siblings ("racc", "workbuddy") are display aliases for a
//     route, not model ids;
//   - CLAUDE_CODE_SUBAGENT_MODEL is what the CLI hands its own subagents. It
//     need not be a model the endpoint serves under that id at all — on this
//     machine it read `glm-5[1M]`, and the relay answered 404 model_not_found.
//
// FABLE is an extension: the reference implementation only walks OPUS, SONNET
// and HAIKU. Claude Code does read ANTHROPIC_DEFAULT_FABLE_MODEL, and a row for
// it is worth having, so it is collected here — but it is the one tier a stock
// Codex-era reader would not know about.
var claudeTierKeys = []string{
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
}

// claudeModelID is modelID plus the optional `[1M]` suffix Claude Code uses to
// ask for the 1M window. The suffix stays in the id; only the window is
// derived from it.
var claudeModelID = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./:+@-]*(?:\[[0-9A-Za-z]+\])?$`)

// parseClaudeModel splits a tier value into the id to store and the window it
// announced. Unlike cleanModelID it keeps the suffix.
func parseClaudeModel(raw string) (id string, contextWindow int) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", 0
	}
	if open := strings.LastIndex(s, "["); open > 0 && strings.HasSuffix(s, "]") {
		if marker := strings.ToUpper(s[open+1 : len(s)-1]); strings.Contains(marker, "1M") {
			contextWindow = 1_000_000
		}
	}
	if !claudeModelID.MatchString(s) {
		return "", 0
	}
	return s, contextWindow
}

// collectClaude reads ~/.claude/settings.json, merges it under the process
// environment, and turns the result into rows.
func collectClaude(ctx context.Context, opt Options) Source {
	src := Source{ID: SourceClaude, Label: "Claude Code", Status: StatusMissing}
	settingsPath := joinHome(opt.Home, ".claude", "settings.json")
	fileEnv := map[string]string{}
	settingsText, hasSettings := readText(settingsPath)
	if hasSettings {
		var doc struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal([]byte(settingsText), &doc); err == nil {
			for k, v := range doc.Env {
				if strings.TrimSpace(v) != "" {
					fileEnv[k] = strings.TrimSpace(v)
				}
			}
		}
	}

	// The file is the install's own answer; the process environment overrides
	// it, because that is the precedence the CLI itself applies.
	lookup := func(key string) string {
		if v := opt.env(key); v != "" {
			return v
		}
		return fileEnv[key]
	}

	base := lookup("ANTHROPIC_BASE_URL")
	type tier struct {
		id     string
		window int
	}
	tiers := make([]tier, 0, len(claudeTierKeys))
	for _, key := range claudeTierKeys {
		if id, window := parseClaudeModel(lookup(key)); id != "" {
			tiers = append(tiers, tier{id, window})
		}
	}

	if !hasSettings && len(tiers) == 0 && base == "" {
		src.Detail = settingsPath + " not found and no ANTHROPIC_* in the environment"
		return src
	}

	keyRef := ""
	bearer := false
	switch {
	case fileEnv["ANTHROPIC_AUTH_TOKEN"] != "" || fileEnv["ANTHROPIC_API_KEY"] != "":
		keyRef = "claude:settings"
		// ResolveKey prefers AUTH_TOKEN when both are present, and that one is
		// a Bearer credential.
		bearer = fileEnv["ANTHROPIC_AUTH_TOKEN"] != ""
	case opt.env("ANTHROPIC_AUTH_TOKEN") != "":
		keyRef = "env:ANTHROPIC_AUTH_TOKEN"
		bearer = true
	case opt.env("ANTHROPIC_API_KEY") != "":
		keyRef = "env:ANTHROPIC_API_KEY"
	}

	// Anthropic's own endpoint is a catalogue row; anything else is a relay and
	// has to carry its address.
	provider, rowBase := "custom", strings.TrimRight(strings.TrimSpace(base), "/")
	if rowBase == "" || strings.Contains(rowBase, "api.anthropic.com") {
		provider, rowBase = "claude", ""
	}

	seen := make(map[string]bool, len(tiers)+8)
	add := func(id string, window int, suggested bool) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		src.Candidates = append(src.Candidates, Candidate{
			Source:        SourceClaude,
			Provider:      provider,
			Model:         id,
			Label:         id,
			BaseURL:       rowBase,
			APIType:       "anthropic",
			KeyRef:        keyRef,
			Bearer:        bearer,
			ContextWindow: window,
			Suggested:     suggested,
			Importable:    true,
		})
	}
	for _, t := range tiers {
		add(t.id, t.window, true)
	}

	// A relay that answers a model-list path knows models the tier aliases never
	// mention. Anthropic's own endpoint is skipped: without a key the probe is a
	// 401, and with one it would list a catalogue the person did not ask to
	// import.
	if !opt.NoProbe && rowBase != "" && keyRef != "" {
		for _, id := range probeAnthropicModels(ctx, rowBase, ResolveKey(keyRef, opt), opt.ProbeTimeout) {
			add(id, 0, false)
		}
	}

	switch {
	case len(src.Candidates) > 0:
		src.Status = StatusOK
		src.Detail = settingsPath
		if !hasSettings {
			src.Detail = "environment"
		}
		// The environment wins over the file — that is what the CLI does — but a
		// stale export then silently changes which endpoint every row points at.
		// On this machine a shell-level ANTHROPIC_BASE_URL ending in
		// "/claude-desktop" turned four working rows into 401s, and nothing in
		// the console said where the address came from. Say it here.
		if envBase := opt.env("ANTHROPIC_BASE_URL"); envBase != "" &&
			fileEnv["ANTHROPIC_BASE_URL"] != "" && !sameURL(envBase, fileEnv["ANTHROPIC_BASE_URL"]) {
			src.Detail += " · ANTHROPIC_BASE_URL from the environment (" + envBase +
				") overrides settings.json (" + fileEnv["ANTHROPIC_BASE_URL"] + ")"
		}
	case hasSettings:
		src.Status = StatusEmpty
		src.Detail = settingsPath + " has no model alias and no endpoint"
	default:
		src.Detail = settingsPath + " not found"
	}
	return src
}

// probeAnthropicModels asks an Anthropic-compatible relay for its roster. The
// Messages API has no model list of its own; relays that have one hang it off
// either the OpenAI-compatible path or a /v1/models route, so both spellings
// are tried and the answers merged.
func probeAnthropicModels(ctx context.Context, base, key string, timeout time.Duration) []string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, url := range []string{base + "/v1/models", base + "/models"} {
		body, ok := getJSON(ctx, url, key, timeout)
		if !ok {
			continue
		}
		for _, rec := range decodeModelRecords(body) {
			id, _ := parseClaudeModel(rec.id)
			if id == "" || isEmbeddingModel(id) || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ResolveKey turns a KeyRef into the secret it names. It is the only function
// in this package that returns a credential, and the only caller is the
// importer — the discovery JSON the console receives never carries one.
func ResolveKey(ref string, opt Options) string {
	opt = opt.withDefaults()
	kind, name, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || name == "" {
		return ""
	}
	switch kind {
	case "env":
		return opt.env(name)
	case "literal":
		// A local server's placeholder credential ("lm-studio"): not a secret,
		// but several of them reject a request that carries no Authorization.
		return name
	case "codex":
		home := codexHome(opt)
		if home == "" {
			// A relative CODEX_HOME names no file this process can trust; an
			// empty home must not turn into "./config.toml" under the daemon's
			// working directory.
			return ""
		}
		text, ok := readText(filepath.Join(home, "config.toml"))
		if !ok {
			return ""
		}
		for _, p := range parseCodexToml(text).Providers {
			if p.ID == name {
				value, _ := p.key(opt)
				return value
			}
		}
		return ""
	case "claude":
		text, ok := readText(joinHome(opt.Home, ".claude", "settings.json"))
		if !ok {
			return ""
		}
		var doc struct {
			Env map[string]string `json:"env"`
		}
		if json.Unmarshal([]byte(text), &doc) != nil {
			return ""
		}
		if v := strings.TrimSpace(doc.Env["ANTHROPIC_AUTH_TOKEN"]); v != "" {
			return v
		}
		return strings.TrimSpace(doc.Env["ANTHROPIC_API_KEY"])
	}
	return ""
}
