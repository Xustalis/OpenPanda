package panel

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"
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
}

// probeAgent resolves one registry entry's install path and best-effort
// version. Some CLIs print their version on stderr or exit non-zero for
// --version, so a failed probe still counts as installed when the binary
// resolves.
func probeAgent(k agents.Known) agentJSON {
	out := agentJSON{
		Name:        k.Name,
		DisplayName: k.DisplayName,
		InstallHint: k.InstallHint,
		InstallURL:  k.InstallURL,
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	version, _ := exec.CommandContext(ctx, out.Path, "--version").Output()
	out.Version = firstLine(string(version))
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

// listAgents serves GET /api/agents — the known agent CLIs (from the
// registry) with install state, path, best-effort version, and install
// guidance for the settings page.
func (h *handler) listAgents(w http.ResponseWriter, r *http.Request) {
	known := agents.Registry()
	out := make([]agentJSON, 0, len(known))
	for _, k := range known {
		out = append(out, probeAgent(k))
	}
	writeJSON(w, out)
}

// testAgent serves POST /api/agents/{name}/test — a connectivity check for one
// agent: runs `<cli> --version` and reports the outcome.
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, agent.Path, "--version")
	version, err := cmd.Output()
	if err != nil {
		writeJSON(w, map[string]any{"name": name, "ok": false, "path": agent.Path, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"name": name, "ok": true, "path": agent.Path, "version": firstLine(string(version))})
}
