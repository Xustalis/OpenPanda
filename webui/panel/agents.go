package panel

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// agentCLI is one probed agent CLI: its panel/adapter name and the binary
// on PATH. Kept in sync with detect.go's card-generation probe list.
type agentCLI struct {
	Name   string
	Binary string
}

var agentCLIs = []agentCLI{
	{Name: "claude_code", Binary: "claude"},
	{Name: "opencode", Binary: "opencode"},
	{Name: "codex", Binary: "codex"},
}

// agentJSON is the wire form of one probed agent.
type agentJSON struct {
	Name      string `json:"name"`
	Binary    string `json:"binary"`
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
}

// probeAgent resolves one CLI's install path and best-effort version. Some
// CLIs print their version on stderr or exit non-zero for --version, so a
// failed probe still counts as installed when the binary resolves.
func probeAgent(cli agentCLI) agentJSON {
	out := agentJSON{Name: cli.Name, Binary: cli.Binary}
	path, err := exec.LookPath(cli.Binary)
	if err != nil {
		return out
	}
	out.Installed = true
	out.Path = path
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	version, _ := exec.CommandContext(ctx, path, "--version").Output()
	out.Version = strings.TrimSpace(string(version))
	return out
}

// listAgents serves GET /api/agents — the installed agent CLIs (settings page
// visibility, queue redesign): path plus a best-effort version per CLI.
func (h *handler) listAgents(w http.ResponseWriter, r *http.Request) {
	out := make([]agentJSON, 0, len(agentCLIs))
	for _, cli := range agentCLIs {
		out = append(out, probeAgent(cli))
	}
	writeJSON(w, out)
}

// testAgent serves POST /api/agents/{name}/test — a connectivity check for one
// agent: runs `<cli> --version` and reports the outcome.
func (h *handler) testAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	for _, cli := range agentCLIs {
		if cli.Name != name {
			continue
		}
		agent := probeAgent(cli)
		if !agent.Installed {
			writeJSON(w, map[string]any{"name": name, "ok": false, "error": cli.Binary + " not found on PATH"})
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
		writeJSON(w, map[string]any{"name": name, "ok": true, "path": agent.Path, "version": strings.TrimSpace(string(version))})
		return
	}
	writeErr(w, http.StatusNotFound, errors.New("unknown agent"))
}
