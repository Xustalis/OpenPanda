// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
)

// agentJSON is the wire form of one probed agent. It carries the install
// guidance from the registry so the settings page can render a download /
// install link for agents that are not yet on PATH.
type agentJSON struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Binary      string `json:"binary"`
	Installed   bool   `json:"installed"`
	Path        string `json:"path,omitempty"`
	Version     string `json:"version,omitempty"`
	InstallHint string `json:"install_hint,omitempty"`
	InstallURL  string `json:"install_url,omitempty"`
	// BestAt/CostTier are the registry's own description of the agent — what
	// it is good at and what it costs. The composer's team picker reads them
	// to say what each "employee" does instead of only naming a CLI.
	BestAt   []string `json:"best_at,omitempty"`
	CostTier string   `json:"cost_tier,omitempty"`
}

// probeAgent resolves one registry entry's install path and best-effort
// version. Some CLIs print their version on stderr or exit non-zero for
// --version, so a failed probe still counts as installed when the binary
// resolves.
//
// The version half is cached: resolving the path stays live (a LookPath is a
// handful of stats) while spawning the CLI is what costs, and a Node-based
// agent spends hundreds of milliseconds starting just to print one line. The
// card is fetched by the settings page, the workspace, the node list and the
// team picker, so without a cache one open of the console re-spawns every
// installed agent several times over.
func probeAgent(k agents.Known) agentJSON {
	out := agentJSON{
		Name:        k.Name,
		DisplayName: k.DisplayName,
		InstallHint: k.InstallHint,
		InstallURL:  k.InstallURL,
		BestAt:      k.DefaultBestAt,
		CostTier:    k.DefaultCostTier,
	}
	for _, bin := range k.Binaries {
		if path, err := exec.LookPath(bin); err == nil {
			out.Installed = true
			out.Binary = bin
			out.Path = path
			break
		}
	}
	if !out.Installed {
		out.Binary = k.PrimaryBinary()
		return out
	}
	out.Version, _ = probeVersion(out.Path, false)
	return out
}

// firstLine collapses a CLI's --version output to its opening line so a
// multi-line banner (e.g. Hermes) does not pollute the one-line table.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// Both sides of the merge keep their own cache layer, because they sit at
// different granularities: the list cache (local) makes GET /api/agents cost
// nothing while views poll it, and the version cache (upstream) stops one
// CLI's --version from being re-run by every view that renders a roster.
// listAgents below reads through the former; probeVersion backs the roster's
// version column and the test button.
//
// agentListCacheTTL keeps the expensive CLI probes out of the panel's polling
// path while still noticing an install or removal promptly. A settings-page
// refresh bypasses the TTL explicitly.
const agentListCacheTTL = time.Minute

// agentListCache serializes the slow probe and shares its result with every
// concurrent reader. Holding the lock through the probe is intentional: it is
// also the single-flight mechanism, so a cold cache cannot start one CLI queue
// per open view. Entries are immutable after publication.
type agentListCache struct {
	mu      sync.Mutex
	entries []agentJSON
	expires time.Time
}

func (c *agentListCache) get(force bool, probe func() []agentJSON) []agentJSON {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.entries != nil && time.Now().Before(c.expires) {
		return c.entries
	}
	entries := probe()
	c.entries = entries
	c.expires = time.Now().Add(agentListCacheTTL)
	return entries
}

// versionTTL bounds how stale a cached version string may be. Long enough that
// opening the console repeatedly costs one probe, short enough that an upgrade
// shows up without a restart — and the explicit test button always bypasses it.
const versionTTL = 5 * time.Minute

// versionCache is keyed by resolved executable path, not by agent name: an
// agent that was missing from PATH and has since been installed resolves to a
// path it did not have before, which is a miss by construction. Failures are
// cached too (empty version plus the error) — a CLI that hangs or exits
// non-zero on --version is the one most worth not asking again.
var (
	versionCacheMu sync.Mutex
	versionCache   = map[string]versionProbe{}
)

type versionProbe struct {
	version string
	err     error
	at      time.Time
}

// probeVersion returns the CLI's --version first line, from cache when fresh.
// force skips the read cache and always writes the result back, which is how
// the explicit connectivity test refreshes the roster for the next open.
//
// The 5s ceiling is the reason for caching at all: a probe that hits it has
// cost the caller five seconds to learn "this CLI does not answer", and the
// roster is fetched by four views.
func probeVersion(path string, force bool) (string, error) {
	if !force {
		versionCacheMu.Lock()
		cached, ok := versionCache[path]
		versionCacheMu.Unlock()
		if ok && time.Since(cached.at) < versionTTL {
			return cached.version, cached.err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A --version banner goes to stderr for some CLIs, so both streams count as
	// output; the error still decides whether the probe is reported as failed.
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	line := firstLine(string(out))
	var exit *exec.ExitError
	if line == "" && errors.As(err, &exit) {
		// Some CLIs print the banner on stderr and exit non-zero; the roster
		// still wants the line, and the error is kept for the test endpoint.
		line = firstLine(string(exit.Stderr))
	}

	versionCacheMu.Lock()
	versionCache[path] = versionProbe{version: line, err: err, at: time.Now()}
	versionCacheMu.Unlock()
	return line, err
}

// listAgents serves GET /api/agents — the known agent CLIs (from the
// registry) with install state, path, best-effort version, and install
// guidance for the settings page.
//
// The probes stay serial on purpose. They were parallelised once to shorten
// the roster card's wait and measured *worse* (2.5s → 10s on a Windows box):
// each probe spawns a Node-based CLI, and eleven of them starting at once
// saturate the disk and blow through the 5s per-probe timeout. One at a time
// is the faster queue here, and the card opens on skeletons meanwhile.
func (h *handler) listAgents(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") == "1"
	out := h.agentList.get(force, func() []agentJSON {
		known := agents.Registry()
		entries := make([]agentJSON, 0, len(known))
		for _, k := range known {
			entries = append(entries, probeAgent(k))
		}
		return entries
	})
	writeJSON(w, out)
}

// testAgent serves POST /api/agents/{name}/test — a connectivity check for one
// agent: runs `<cli> --version` and reports the outcome.
//
// This one forces a fresh probe: the button exists to ask the CLI itself, and
// answering from a cache would make a click that just fixed the agent still
// report the old failure. The result is written back, so the roster reflects it
// on the next open.
func (h *handler) testAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	k, ok := agents.ByName(name)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("unknown agent"))
		return
	}
	agent := probeAgent(k)
	if !agent.Installed {
		writeJSON(w, map[string]any{"name": name, "ok": false, "error": agent.Binary + " not found on PATH"})
		return
	}
	version, err := probeVersion(agent.Path, true)
	if err != nil {
		writeJSON(w, map[string]any{"name": name, "ok": false, "path": agent.Path, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"name": name, "ok": true, "path": agent.Path, "version": version})
}
