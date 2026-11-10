// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ccswitch reads CC Switch's provider store.
//
// CC Switch is a desktop switcher for Claude Code / Codex / Gemini endpoints:
// the person keeps a list of providers there (name, endpoint, token, model
// tiers) and the app rewrites the client's own config to whichever one is
// current. That list is the interesting thing for discovery — it is every
// relay the person already configured, not just the one that happens to be
// live — but it lives in a SQLite file the app owns, so this package only ever
// opens it read-only and only ever answers two questions: "which providers are
// configured" and "what is this provider's token".
//
// Two deliberate limits:
//
//   - Read-only, always. The file is opened with mode=ro and a busy timeout:
//     CC Switch may be writing while discovery runs, and a scan must never be
//     able to corrupt or block the app that owns the store.
//   - Nothing secret leaves the process. Read returns the endpoint, the model
//     ids and *whether* a token exists; ResolveKey is the only function that
//     returns a secret, and its only caller is the model client at call time.
package ccswitch

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// The app types CC Switch stores. Each one keeps its settings in a different
// shape under settings_config, which is why the parser branches on this.
const (
	AppClaude        = "claude"
	AppClaudeDesktop = "claude-desktop"
	AppCodex         = "codex"
	AppGemini        = "gemini"
)

// Provider is one configured endpoint, flattened out of the store's
// settings_config JSON.
type Provider struct {
	// ID is CC Switch's own row id (a UUID). KeyRef names it.
	ID string
	// AppType is "claude", "claude-desktop", "codex" or "gemini".
	AppType string
	// Name is the label the person gave the provider ("Zhipu GLM", "本地").
	// It is the most recognisable thing a discovered row can show.
	Name string
	// BaseURL is the endpoint the provider dials, empty for a vendor-official
	// row that leaves the endpoint to the client's default.
	BaseURL string
	// Models are the model ids the provider pins, in tier order and deduped.
	// Claude Desktop rows usually carry none — the client picks the model.
	Models []string
	// APIType is the wire dialect this row speaks: "anthropic" for the Claude
	// apps, "responses" or "openai" for Codex (its wire_api).
	APIType string
	// HasToken says a credential is stored for this provider. The value never
	// leaves ResolveKey.
	HasToken bool
	// Bearer marks a credential stored as ANTHROPIC_AUTH_TOKEN rather than
	// ANTHROPIC_API_KEY: the two headers are not interchangeable on a relay,
	// so the imported row has to keep the scheme.
	Bearer bool
	// Current marks the provider CC Switch has written into the live config.
	Current bool
	// SortIndex is CC Switch's own ordering; it keeps discovery output stable.
	SortIndex int
}

// Path returns the store's default location under home.
func Path(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".cc-switch", "cc-switch.db")
}

// open opens the store read-only. A missing file is not an error the caller
// has to distinguish: it means CC Switch is not installed here.
func open(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(abs) + "?mode=ro&_pragma=busy_timeout(1500)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: the file is read a handful of rows at a time, and a pool
	// would open the same read-only handle several times for no gain.
	db.SetMaxOpenConns(1)
	return db, nil
}

// Read returns every provider in the store, ordered the way CC Switch orders
// them. A store that does not exist yields (nil, nil): absence is a status,
// not a failure.
func Read(path string) ([]Provider, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	db, err := open(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT id, app_type, name, settings_config, is_current, sort_index
		FROM providers
		ORDER BY app_type, sort_index, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Provider
	for rows.Next() {
		var (
			id, appType, name, cfg string
			current                bool
			sortIndex              sql.NullInt64
		)
		if err := rows.Scan(&id, &appType, &name, &cfg, &current, &sortIndex); err != nil {
			return nil, err
		}
		p := Provider{
			ID:        strings.TrimSpace(id),
			AppType:   strings.TrimSpace(appType),
			Name:      strings.TrimSpace(name),
			Current:   current,
			SortIndex: int(sortIndex.Int64),
		}
		applySettings(&p, cfg)
		out = append(out, p)
	}
	return out, rows.Err()
}

// settingsConfig is the union of the shapes CC Switch stores. Claude rows put
// the settings.json body under "env"; Codex rows put auth.json under "auth"
// and config.toml (as a string) under "config"; Gemini rows keep an object in
// "config" that this package does not read.
type settingsConfig struct {
	Env    map[string]string `json:"env"`
	Auth   map[string]any    `json:"auth"`
	Config any               `json:"config"`
}

// applySettings flattens one settings_config row into the provider.
func applySettings(p *Provider, raw string) {
	var doc settingsConfig
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return
	}
	switch p.AppType {
	case AppClaude, AppClaudeDesktop:
		applyClaude(p, doc.Env)
	case AppCodex:
		applyCodex(p, doc.Auth, doc.Config)
	}
}

// applyClaude reads the Claude Code settings.json body: the keys here are the
// ones Claude Code itself reads.
func applyClaude(p *Provider, env map[string]string) {
	p.BaseURL = strings.TrimRight(strings.TrimSpace(env["ANTHROPIC_BASE_URL"]), "/")
	p.APIType = "anthropic"
	seen := make(map[string]bool, len(claudeTierKeys))
	for _, key := range claudeTierKeys {
		id := strings.TrimSpace(env[key])
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		p.Models = append(p.Models, id)
	}
	p.HasToken = strings.TrimSpace(env["ANTHROPIC_AUTH_TOKEN"]) != "" ||
		strings.TrimSpace(env["ANTHROPIC_API_KEY"]) != ""
	p.Bearer = strings.TrimSpace(env["ANTHROPIC_AUTH_TOKEN"]) != ""
}

// applyCodex reads the Codex pair: auth.json under "auth" and config.toml as a
// string under "config". A row whose auth carries no OPENAI_API_KEY relies on
// the ChatGPT login, which OpenPanda cannot reuse — HasToken stays false and
// the collector skips it.
func applyCodex(p *Provider, auth map[string]any, config any) {
	text, _ := config.(string)
	model, providerID, base, wire := parseCodexConfig(text)
	p.BaseURL = strings.TrimRight(strings.TrimSpace(base), "/")
	if model != "" {
		p.Models = []string{model}
	}
	switch strings.ToLower(strings.TrimSpace(wire)) {
	case "responses":
		p.APIType = "responses"
	default:
		// "chat" is the OpenAI-compatible surface; an empty wire_api is the
		// CLI's default, which is also chat completions.
		p.APIType = "openai"
	}
	_ = providerID
	if key, _ := auth["OPENAI_API_KEY"].(string); strings.TrimSpace(key) != "" {
		p.HasToken = true
	}
}

// parseCodexConfig pulls the four fields an import needs out of a config.toml
// body: the pinned model, the provider id, and that provider's base_url and
// wire_api. It is a line reader, not a TOML parser, because those four keys
// are flat strings and a full parser would be a dependency for nothing.
func parseCodexConfig(text string) (model, providerID, baseURL, wireAPI string) {
	type endpoint struct{ base, wire string }
	providers := make(map[string]endpoint, 4)
	section := ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.Trim(line, "[]"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if id, isProvider := strings.CutPrefix(section, "model_providers."); isProvider {
			if id == "" {
				continue
			}
			cur := providers[id]
			switch key {
			case "base_url":
				cur.base = value
			case "wire_api":
				cur.wire = value
			}
			providers[id] = cur
			continue
		}
		if section != "" {
			continue
		}
		switch key {
		case "model":
			model = value
		case "model_provider":
			providerID = value
		}
	}
	if p, ok := providers[providerID]; ok {
		baseURL, wireAPI = p.base, p.wire
	}
	return model, providerID, baseURL, wireAPI
}

// claudeTierKeys mirrors the env names Claude Code reads a model id from, in
// the same order the settings collector uses. The `_NAME` siblings are display
// aliases, not model ids, and CLAUDE_CODE_SUBAGENT_MODEL names a model the
// endpoint need not serve — both are deliberately absent.
var claudeTierKeys = []string{
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
}

// RefPrefix names a credential that lives in the CC Switch store:
// "ccswitch:<app_type>:<provider id>". It is stored in config as a reference,
// so the secret stays in the file that already holds it and a key rotation in
// CC Switch is picked up without re-importing.
const RefPrefix = "ccswitch:"

// MakeRef builds the reference for one provider.
func MakeRef(appType, id string) string {
	return RefPrefix + appType + ":" + id
}

// ResolveKey returns the token a reference names. It is the only function in
// this package that returns a secret, and the model client is its only caller.
// An unreadable store, an unknown id or a provider without a token all answer
// "" — the client then reports the row as having no key, which is the truth.
func ResolveKey(ref, home string) string {
	appType, id, ok := splitRef(ref)
	if !ok {
		return ""
	}
	path := Path(home)
	if path == "" {
		return ""
	}
	db, err := open(path)
	if err != nil {
		return ""
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow(`SELECT settings_config FROM providers WHERE app_type = ? AND id = ?`, appType, id).Scan(&raw); err != nil {
		return ""
	}
	var doc settingsConfig
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return ""
	}
	if appType == AppCodex {
		if key, _ := doc.Auth["OPENAI_API_KEY"].(string); strings.TrimSpace(key) != "" {
			return strings.TrimSpace(key)
		}
		return ""
	}
	// AUTH_TOKEN wins: it is the Bearer credential a relay expects, and the
	// reference scheme is fixed at import time from the same precedence.
	if v := strings.TrimSpace(doc.Env["ANTHROPIC_AUTH_TOKEN"]); v != "" {
		return v
	}
	return strings.TrimSpace(doc.Env["ANTHROPIC_API_KEY"])
}

func splitRef(ref string) (appType, id string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(ref), RefPrefix)
	if !found {
		return "", "", false
	}
	appType, id, found = strings.Cut(rest, ":")
	if !found || appType == "" || id == "" {
		return "", "", false
	}
	return appType, id, true
}

// Describe is the source's one-line status detail, kept here so the collector
// does not have to duplicate the counting rules. "Importable" means the row has
// an endpoint, a model and a token — the three things a registry row cannot
// invent.
func Describe(providers []Provider) string {
	importable := 0
	for _, p := range providers {
		if p.BaseURL != "" && len(p.Models) > 0 && p.HasToken {
			importable++
		}
	}
	if len(providers) == 0 {
		return "no providers configured"
	}
	return fmt.Sprintf("%d provider(s), %d importable", len(providers), importable)
}
