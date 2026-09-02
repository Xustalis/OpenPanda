package main

// `panda agents` — probe the agent CLIs this node can delegate to, driven by
// the agent registry (internal/agents) rather than a per-command hardcoded
// list. The same registry feeds `panda detect`, the web settings API, and the
// commander's availability probe, so adding an agent is a single-entry change.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// agentStatus is the wire form of one probed agent.
type agentStatus struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name,omitempty"`
	Binary      string   `json:"binary"`
	Installed   bool     `json:"installed"`
	Path        string   `json:"path,omitempty"`
	Version     string   `json:"version,omitempty"`
	InstallHint string   `json:"install_hint,omitempty"`
	InstallURL  string   `json:"install_url,omitempty"`
	InitHint    string   `json:"init_hint,omitempty"`
	Caps        []string `json:"capabilities,omitempty"`
}

// probeAgentCLI resolves one registry entry to an install status: any of its
// probe binaries resolving on PATH counts as installed (a failed --version
// probe still counts — some CLIs print version on stderr or exit non-zero).
func probeAgentCLI(k agents.Known) agentStatus {
	out := agentStatus{
		Name:        k.Name,
		DisplayName: k.DisplayName,
		InstallHint: k.InstallHint,
		InstallURL:  k.InstallURL,
		InitHint:    k.InitHint,
		Caps:        capabilityTags(k.Capabilities),
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
	if version, err := exec.CommandContext(ctx, out.Path, "--version").Output(); err == nil {
		out.Version = firstLine(string(version))
	}
	return out
}

// probeAgentStatuses returns the probed status for every registry entry.
func probeAgentStatuses() []agentStatus {
	known := agents.Registry()
	out := make([]agentStatus, 0, len(known))
	for _, k := range known {
		out = append(out, probeAgentCLI(k))
	}
	return out
}

// runAgents implements `panda agents [test <name>|install <name>]`.
func runAgents(args []string) {
	fs := flag.NewFlagSet("agents", flag.ExitOnError)
	fs.Parse(args)
	loc := i18n.Detect()
	sub := fs.Arg(0)

	switch sub {
	case "test":
		name := strings.TrimSpace(fs.Arg(1))
		if name == "" {
			fmt.Fprintln(os.Stderr, "usage: panda agents test <name>")
			os.Exit(2)
		}
		runAgentTest(loc, name)
		return
	case "install", "update":
		name := strings.TrimSpace(fs.Arg(1))
		if name == "" {
			fmt.Fprintln(os.Stderr, "usage: panda agents install <name>")
			os.Exit(2)
		}
		runAgentInstall(loc, name)
		return
	}

	statuses := probeAgentStatuses()
	if jsonOutput {
		emitJSON(statuses)
		return
	}
	installed := 0
	p := pal()
	nameW, binW := 16, 8
	for _, a := range statuses {
		nameW = max(nameW, cliui.DisplayWidth(a.Name))
		binW = max(binW, cliui.DisplayWidth(a.Binary))
	}
	for _, a := range statuses {
		// An installed agent is marked and readable; a missing one is dimmed
		// whole, so the list answers "what can this node run" at a glance instead
		// of making the reader compare a column of asterisks.
		mark, tint := p.Muted(p.MarkBullet()), p.Muted
		if a.Installed {
			mark, tint = p.Success(p.MarkOK()), func(s string) string { return s }
			installed++
		}
		version := ""
		if a.Version != "" {
			version = "  " + a.Version
		}
		fmt.Println("  " + mark + " " + tint(row(
			cell(a.Name, nameW), cell(a.Binary, binW), orDash(a.Path)+version)))
	}
	if installed == 0 {
		fmt.Println(i18n.T(loc, "cli.agents.none"))
	}
	if missing := printAgentInstallHelp(statuses); missing > 0 {
		fmt.Println()
		fmt.Println(i18n.Tf(loc, "cli.agents.installHint", "count", fmt.Sprintf("%d", missing)))
	}
}

// runAgentTest implements `panda agents test <name>`.
func runAgentTest(loc i18n.Locale, name string) {
	k, ok := agents.ByName(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "panda: %s\n", i18n.Tf(loc, "cli.agents.unknown", "name", name))
		os.Exit(2)
	}
	agent := probeAgentCLI(k)
	if !agent.Installed {
		reportAgentTest(loc, agentStatus{Name: name}, false, i18n.Tf(loc, "cli.agents.notFound", "binary", agent.Binary))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, agent.Path, "--version").Output()
	if err != nil {
		reportAgentTest(loc, agent, false, err.Error())
		return
	}
	agent.Version = firstLine(string(version))
	reportAgentTest(loc, agent, true, agent.Version)
}

// runAgentInstall implements `panda agents install <name>`: it prints the
// install/update command and documentation link for one registry entry. It
// never executes the installer itself — copy the printed command.
func runAgentInstall(loc i18n.Locale, name string) {
	k, ok := agents.ByName(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "panda: %s\n", i18n.Tf(loc, "cli.agents.unknown", "name", name))
		os.Exit(2)
	}
	if jsonOutput {
		emitJSON(map[string]any{
			"name":         k.Name,
			"display_name": k.DisplayName,
			"install_hint": k.InstallHint,
			"install_url":  k.InstallURL,
			"init_hint":    k.InitHint,
		})
		return
	}
	if k.InstallHint == "" && k.InstallURL == "" {
		fmt.Println(i18n.Tf(loc, "cli.agents.noInstaller", "name", k.DisplayName))
		return
	}
	if k.InstallHint != "" {
		fmt.Println(i18n.Tf(loc, "cli.agents.install.cmd", "name", k.DisplayName, "hint", k.InstallHint))
	}
	if k.InstallURL != "" {
		fmt.Println(i18n.Tf(loc, "cli.agents.install.url", "url", k.InstallURL))
	}
	if k.InitHint != "" {
		fmt.Printf("  After install, initialize: %s\n", k.InitHint)
	}
}

// printAgentInstallHelp prints one install-hint + docs line per missing agent
// and returns the number of missing agents (zero if all installed).
func printAgentInstallHelp(statuses []agentStatus) int {
	missing := 0
	for _, a := range statuses {
		if a.Installed || (a.InstallHint == "" && a.InstallURL == "") {
			continue
		}
		missing++
		// The command and the docs URL are two different things to do, and
		// joining them with a space produced one unusable line ("npm install -g
		// @deepseek-ai/dsh https://github.com/…" reads as one argv). The command
		// goes on its own line, ready to copy; the URL is dimmed underneath.
		p := pal()
		fmt.Printf("  %s %s\n", p.Bold(a.Name), p.Muted("("+orDash(a.Binary)+")"))
		if a.InstallHint != "" {
			fmt.Println("    " + p.Command(a.InstallHint))
		}
		if a.InstallURL != "" {
			fmt.Println("    " + p.Muted(a.InstallURL))
		}
	}
	return missing
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

// capabilityTags translates the registry's boolean capability flags into
// short human-readable tags for the CLI listing and JSON output.
func capabilityTags(c agents.Capabilities) []string {
	var tags []string
	if c.SupportsSkills {
		tags = append(tags, "skills")
	}
	if c.SupportsMCP {
		tags = append(tags, "mcp")
	}
	if c.SupportsSubagents {
		tags = append(tags, "subagents")
	}
	return tags
}
