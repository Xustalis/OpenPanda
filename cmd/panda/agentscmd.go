package main

// `panda agents` — probe the agent CLIs this node can delegate to (the same
// directory the web settings page shows). The probe list mirrors detect.go's
// card-generation list; the kernel deliberately does not import webui/panel
// for this, so the three-CLI table is kept here in step with detect.go.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// agentProbe is one agent CLI: its adapter name and the binary on PATH.
type agentProbe struct {
	Name   string
	Binary string
}

var agentProbes = []agentProbe{
	{Name: "claude_code", Binary: "claude"},
	{Name: "opencode", Binary: "opencode"},
	{Name: "codex", Binary: "codex"},
}

// agentStatus is the wire form of one probed agent.
type agentStatus struct {
	Name      string `json:"name"`
	Binary    string `json:"binary"`
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
}

// probeAgentCLI resolves one CLI's install path and best-effort version (a
// failed --version probe still counts as installed when the binary resolves).
func probeAgentCLI(p agentProbe) agentStatus {
	out := agentStatus{Name: p.Name, Binary: p.Binary}
	path, err := exec.LookPath(p.Binary)
	if err != nil {
		return out
	}
	out.Installed = true
	out.Path = path
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if version, err := exec.CommandContext(ctx, path, "--version").Output(); err == nil {
		out.Version = strings.TrimSpace(string(version))
	}
	return out
}

// runAgents implements `panda agents [test <name>]`.
func runAgents(args []string) {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	fs.Parse(args)
	loc := i18n.Detect()

	if fs.Arg(0) == "test" {
		name := strings.TrimSpace(fs.Arg(1))
		if name == "" {
			fmt.Fprintln(os.Stderr, "usage: panda agents test <name>")
			os.Exit(2)
		}
		for _, p := range agentProbes {
			if p.Name != name {
				continue
			}
			agent := probeAgentCLI(p)
			if !agent.Installed {
				reportAgentTest(loc, agentStatus{Name: name}, false, i18n.Tf(loc, "cli.agents.notFound", "binary", p.Binary))
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			version, err := exec.CommandContext(ctx, agent.Path, "--version").Output()
			if err != nil {
				reportAgentTest(loc, agent, false, err.Error())
				return
			}
			agent.Version = strings.TrimSpace(string(version))
			reportAgentTest(loc, agent, true, agent.Version)
			return
		}
		fmt.Fprintf(os.Stderr, "panda: unknown agent %q\n", name)
		os.Exit(2)
	}

	statuses := make([]agentStatus, 0, len(agentProbes))
	for _, p := range agentProbes {
		statuses = append(statuses, probeAgentCLI(p))
	}
	if jsonOutput {
		emitJSON(statuses)
		return
	}
	installed := 0
	for _, a := range statuses {
		mark := " "
		if a.Installed {
			mark = "*"
			installed++
		}
		version := ""
		if a.Version != "" {
			version = "  " + a.Version
		}
		fmt.Printf("  %s %-12s %-8s %s%s\n", mark, a.Name, a.Binary, orDash(a.Path), version)
	}
	if installed == 0 {
		fmt.Println(i18n.T(loc, "cli.agents.none"))
	}
}

func reportAgentTest(loc i18n.Locale, agent agentStatus, ok bool, detail string) {
	if jsonOutput {
		emitJSON(map[string]any{"name": agent.Name, "ok": ok, "path": agent.Path, "detail": detail})
		return
	}
	if ok {
		fmt.Println(i18n.Tf(loc, "cli.agents.test.ok", "name", agent.Name, "detail", detail))
	} else {
		fmt.Println(i18n.Tf(loc, "cli.agents.test.fail", "name", agent.Name, "detail", detail))
		os.Exit(1)
	}
}
