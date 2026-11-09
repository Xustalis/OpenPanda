// SPDX-License-Identifier: AGPL-3.0-or-later

// Package localmodels answers one question: what can this machine already
// serve?
//
// The registry in config's `models:` block is written by hand — a person types
// an endpoint and a key for every row. But most machines already carry the
// answer somewhere: Codex keeps its providers in ~/.codex/config.toml, Claude
// Code keeps its endpoint and tier aliases in ~/.claude/settings.json, OpenCode
// knows its own catalogue, and a local inference server publishes /v1/models on
// loopback. This package reads those four places and turns them into rows the
// registry could hold.
//
// Three rules, inherited from the console it feeds:
//
//   - Read-only. Nothing here writes a config, a key, or a settings file.
//   - Secrets stay server-side. A candidate names the *place* a credential
//     comes from ("env:DASHSCOPE_API_KEY", "codex:custom"), never its value;
//     ResolveKey turns that name into a secret, and only the importer calls it.
//   - Silence on failure. A missing CLI, a refused port, a malformed TOML file:
//     each collector reports its own status and returns whatever it has.
//     Discovery is a convenience over a hand-written config, never a startup
//     dependency.
package localmodels

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// Source ids. They are stable wire values — the console keys its sections and
// its selection set on them.
const (
	SourceCodex    = "codex"
	SourceClaude   = "claude"
	SourceCCSwitch = "ccswitch"
	SourceOpenCode = "opencode"
	SourceLocal    = "local"
)

// Source status. "missing" and "empty" are different answers: one means the
// tool is not installed, the other means it is installed and has nothing to
// offer — and only the second is worth a "go configure it" hint.
const (
	StatusOK      = "ok"
	StatusEmpty   = "empty"
	StatusMissing = "missing"
	StatusError   = "error"
)

// modelID is the id shape every row must have to be writable. It is
// deliberately generous — vendors ship ids with dots, slashes, colons and
// at-signs (gpt-5.6-sol, claude-opus-5[1M] after the bracket is stripped,
// @cf/meta/llama) — but it still refuses whitespace, quotes and control
// characters, which is what keeps a hand-edited JSON catalogue from injecting
// something a URL parser would read as a different request.
var modelID = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./:+@-]*$`)

// providerID is the shape of a Codex/OpenCode provider key.
var providerID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// Candidate is one model the machine can serve, shaped like a registry row.
//
// Provider is the catalogue id the row would be written under ("custom" for an
// endpoint the catalogue does not know); BaseURL is what the row would dial.
// KeyRef is a *name* for the credential — "env:NAME", "codex:<provider>",
// "claude:settings" — never the secret itself.
type Candidate struct {
	// ID is stable across two runs of the same discovery, so the console can
	// keep a tick set through a refresh.
	ID            string `json:"id"`
	Source        string `json:"source"`
	Provider      string `json:"provider"`
	ProviderLabel string `json:"provider_label,omitempty"`
	Model         string `json:"model"`
	Label         string `json:"label,omitempty"`
	BaseURL       string `json:"base_url"`
	APIType       string `json:"api_type"`
	KeyRef        string `json:"key_ref,omitempty"`
	NoAuth        bool   `json:"no_auth,omitempty"`
	// Auth names a subscription credential from internal/auth instead of a
	// key: the client reads a live token per call. It is how a Codex login
	// reaches the registry — the token stays in the CLI's own file.
	Auth string `json:"auth,omitempty"`
	// Bearer marks a KeyRef whose credential is an OAuth-style bearer token
	// rather than an API key — Claude Code's ANTHROPIC_AUTH_TOKEN. Row puts it
	// in the Authorization header; sending it as x-api-key would 401.
	Bearer        bool `json:"-"`
	ContextWindow int  `json:"context_window,omitempty"`
	// Loaded marks a local model the server says is resident in memory. It is
	// a hint, not a filter: an unloaded model is still importable, it just pays
	// a cold start on the first turn.
	Loaded bool `json:"loaded,omitempty"`
	// Suggested is the console's default tick. Curated sources (the model a
	// CLI is configured to use, the aliases in a settings file) are suggested;
	// a vendor's full built-in catalogue is not, because importing 81 rows to
	// use one is not a favour.
	Suggested bool `json:"suggested,omitempty"`
	// Importable is false for a model we can see but cannot address: no
	// endpoint, or no provider the catalogue can bill through. The console
	// still lists it, greyed, because "OpenCode knows 43 Alibaba models" is
	// information the person may want.
	Importable bool   `json:"importable"`
	Note       string `json:"note,omitempty"`
	// Verified says the endpoint itself answered for this model during the
	// scan. Only loopback rows are checked: a local proxy answers in a second
	// and costs nothing, while probing thirty-six catalogue rows would turn one
	// scan into a minute of network. False therefore means "not checked", not
	// "broken" — and the console says exactly that.
	Verified bool `json:"verified,omitempty"`
	// Check carries the endpoint's own words when a loopback row failed to
	// answer, so the console can explain instead of just greying the row.
	Check string `json:"check,omitempty"`
	// Placeholder marks a credential that is a literal from a config file and
	// does not look like a secret (a local proxy's "PROXY_MANAGED"). The row
	// still sends it — such a proxy rejects a request without the header — but
	// the console must not present it as a real key.
	Placeholder bool `json:"placeholder,omitempty"`
	// Existing is filled in by the panel: a row with this endpoint and model is
	// already registered, so importing it again would only produce a duplicate
	// alias.
	Existing bool `json:"existing,omitempty"`
}

// Source is one collector's report.
type Source struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
	// Detail is the collector's own sentence about what it found or why it
	// found nothing — a file path, a CLI version, a refused port.
	Detail     string      `json:"detail,omitempty"`
	Candidates []Candidate `json:"candidates"`
}

// Options are the seams the tests and the CLI need: a fake environment, a fake
// home, a pinned CLI path, and the two timeouts that decide how long a
// discovery may take.
type Options struct {
	// Getenv reads the environment a collector should believe in. nil means the
	// real one.
	Getenv func(string) string
	// Home overrides the user's home directory (tests, and a `--home` flag).
	Home string
	// OpenCodeCLI pins the OpenCode executable. Empty means "look it up".
	OpenCodeCLI string
	// CodexCLI pins the Codex executable, used for the account roster.
	CodexCLI string
	// Probe turns the loopback HTTP probes off; the CLI does that when the
	// caller only wants the file-based half.
	NoProbe bool
	// ProbeTimeout bounds one HTTP probe. The console shows a spinner while
	// this runs, so it is deliberately short.
	ProbeTimeout time.Duration
	// CLITimeout bounds a subprocess. OpenCode's catalogue dump is the slow
	// one: it loads its whole provider registry before printing.
	CLITimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = 1500 * time.Millisecond
	}
	if o.CLITimeout <= 0 {
		o.CLITimeout = 25 * time.Second
	}
	return o
}

func (o Options) env(key string) string {
	if key == "" || o.Getenv == nil {
		return ""
	}
	return strings.TrimSpace(o.Getenv(key))
}

// Discover runs every collector and returns their reports in a fixed order.
//
// The four run concurrently: the Codex probe is network-bound, OpenCode's
// catalogue is process-bound, and a serial run would make the console wait for
// the sum of them. Order is restored afterwards so the UI is stable.
func Discover(ctx context.Context, opt Options) []Source {
	opt = opt.withDefaults()
	slots := make(chan Source, 4)
	go func() { slots <- safe(SourceCodex, func() Source { return collectCodex(ctx, opt) }) }()
	go func() { slots <- safe(SourceClaude, func() Source { return collectClaude(ctx, opt) }) }()
	go func() { slots <- safe(SourceOpenCode, func() Source { return collectOpenCode(ctx, opt) }) }()
	go func() { slots <- safe(SourceLocal, func() Source { return collectLocal(ctx, opt) }) }()

	out := make([]Source, 0, 4)
	for i := 0; i < 4; i++ {
		out = append(out, <-slots)
	}
	// CC Switch is deliberately absent: it has its own entry point
	// (CCSwitchGroups), because its four app rosters are a different question
	// from "what can this machine serve" and mixing them made one list of
	// thirty rows nobody could scan.
	order := map[string]int{SourceCodex: 0, SourceClaude: 1, SourceOpenCode: 2, SourceLocal: 3}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && order[out[j].ID] < order[out[j-1].ID]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	out = dedupe(out)
	if !opt.NoProbe {
		verifyCandidates(ctx, out, opt)
	}
	return out
}

// verifyCandidates asks each loopback endpoint whether it will serve the model
// it was discovered for.
//
// Discovery otherwise reports what a machine is *configured* to use, and the
// gap between that and what works is exactly where this feature has been wrong
// before. Loopback rows are cheap to check, so they are checked; a remote row
// keeps Verified=false and the console renders that as "未验证" rather than
// letting a green import imply a working call.
func verifyCandidates(ctx context.Context, sources []Source, opt Options) {
	type job struct {
		cand *Candidate
		mc   config.ModelConfig
	}
	jobs := make([]job, 0, 8)
	for si := range sources {
		for ci := range sources[si].Candidates {
			cand := &sources[si].Candidates[ci]
			if !cand.Importable || !IsLoopback(cand.BaseURL) {
				continue
			}
			mc, err := cand.Row(opt)
			if err != nil {
				cand.Check = err.Error()
				continue
			}
			jobs = append(jobs, job{cand, mc})
		}
	}
	if len(jobs) == 0 {
		return
	}

	// Three at a time, against a whole-pass budget.
	//
	// Measured on the proxy this was written for: four concurrent probes all
	// succeed, but each takes 2-4x longer than it does alone (the proxy
	// serialises upstream), so an unbounded fan-out turns a healthy model into a
	// timeout. Three keeps the pass short without starving anything, and a call
	// that hangs is capped by whatever is left of the budget — one slow model
	// must not hold the whole sheet.
	deadline := time.Now().Add(12 * time.Second)
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i := range jobs {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		per := 10 * time.Second
		if remaining < per {
			per = remaining
		}
		go func(j job, timeout time.Duration) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := Answer(ctx, j.mc, timeout); err != nil {
				j.cand.Check = err.Error()
				return
			}
			j.cand.Verified = true
		}(jobs[i], per)
	}
	wg.Wait()
}

// safe turns a collector panic into a status line. A hand-edited config file is
// exactly the input that makes a parser index out of range, and one broken
// source must not take the other three down with it.
func safe(id string, fn func() Source) (s Source) {
	defer func() {
		if r := recover(); r != nil {
			s = Source{ID: id, Status: StatusError, Detail: fmt.Sprint(r)}
		}
	}()
	return fn()
}

// dedupe drops a candidate whose (endpoint, model) pair another source already
// reported — the same proxy reached through both Codex and Claude Code is the
// normal case on a machine with one local gateway.
//
// The first reporter wins, because the sources are ordered by how much they
// know: Codex names a wire dialect and a provider, while a bare loopback probe
// only knows "something answers here". A later duplicate that *has* a key
// upgrades an earlier one that does not, so the merged row is dialable.
func dedupe(sources []Source) []Source {
	// Flatten, merge on the way through, then hand each survivor back to the
	// source that reported it. Merging in place would need pointers into slices
	// that are still growing, which is how a "keep the key" rule silently
	// updates a copy nobody reads.
	type key struct{ base, model string }
	pos := make(map[key]int, len(sources)*4)
	flat := make([]Candidate, 0, len(sources)*4)
	owner := make([]int, 0, len(sources)*4)
	for si := range sources {
		for _, c := range sources[si].Candidates {
			c.ID = candidateID(c)
			k := key{strings.ToLower(strings.TrimRight(c.BaseURL, "/")), c.Model}
			if i, ok := pos[k]; ok {
				first := &flat[i]
				// A loopback probe outranks a config file: it is the only
				// reporter that knows the model is resident, and its window
				// comes from the server rather than from a hand-written alias.
				// This mirrors the reference implementation, which drops an
				// earlier custom entry when a local port now serves the same id.
				if c.Source == SourceLocal && first.Source != SourceLocal {
					*first = c
					owner[i] = si
					continue
				}
				if first.KeyRef == "" && c.KeyRef != "" && first.Importable {
					first.KeyRef = c.KeyRef
					first.NoAuth = c.NoAuth
				}
				if c.ContextWindow > first.ContextWindow {
					first.ContextWindow = c.ContextWindow
				}
				if c.Loaded {
					first.Loaded = true
				}
				continue
			}
			pos[k] = len(flat)
			flat = append(flat, c)
			owner = append(owner, si)
		}
	}
	for si := range sources {
		sources[si].Candidates = []Candidate{}
	}
	for i := range flat {
		si := owner[i]
		sources[si].Candidates = append(sources[si].Candidates, flat[i])
	}
	return sources
}

func candidateID(c Candidate) string {
	return strings.Join([]string{c.Source, c.Provider, c.Model, strings.ToLower(strings.TrimRight(c.BaseURL, "/"))}, "|")
}

// cleanModelID trims the decorations a CLI prints around an id and rejects
// anything that is not a model id at all. Claude Code's `[1M]` context marker
// is the case that matters: it is not part of the model name, and a row
// carrying it would 404 on the first turn.
func cleanModelID(raw string) (id string, contextWindow int) {
	s := strings.TrimSpace(raw)
	if open := strings.LastIndex(s, "["); open > 0 && strings.HasSuffix(s, "]") {
		marker := strings.ToUpper(s[open+1 : len(s)-1])
		if strings.Contains(marker, "1M") {
			contextWindow = 1_000_000
		}
		s = strings.TrimSpace(s[:open])
	}
	if !modelID.MatchString(s) {
		return "", 0
	}
	return s, contextWindow
}

// joinHome is filepath.Join with the "no home directory" case collapsed: a
// collector that cannot name a path should report "not installed", not
// accidentally read "./.codex" from the working directory.
func joinHome(home string, parts ...string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(append([]string{home}, parts...)...)
}

// findCLI locates an agent CLI: an explicit path, then the environment, then
// PATH. On Windows the npm shim is a .cmd, so the extension is probed
// explicitly — exec.LookPath alone can return the extensionless shell script
// that ships beside it, which CreateProcess cannot run.
func findCLI(explicit, envKey string, names ...string) string {
	if explicit != "" {
		return explicit
	}
	if envKey != "" {
		if v := os.Getenv(envKey); v != "" {
			return v
		}
	}
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		if runtime.GOOS == "windows" {
			for _, ext := range []string{".cmd", ".exe", ".bat"} {
				if p, err := exec.LookPath(name + ext); err == nil {
					return p
				}
			}
		}
	}
	return ""
}

// readText is the "this file may not exist" read every collector needs.
func readText(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}
