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
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(args, nil))
	loc := i18n.Detect()
	sub := fs.Arg(0)

	switch sub {
	case "test":
		name := strings.TrimSpace(fs.Arg(1))
		if name == "" {
			fmt.Fprintln(os.Stderr, "usage: panda agents test <name>")
			os.Exit(2)
		}
		runAgentTest(loc, name, *configPath)
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

// runAgentTest implements `panda agents test <name>`: beyond `--version` it
// runs the same dispatch-readiness check routing applies — credentials/model
// source and a live endpoint probe — so "installed but cannot actually run"
// is visible before a task hangs on it.
func runAgentTest(loc i18n.Locale, name, configPath string) {
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
	if version, err := exec.CommandContext(ctx, agent.Path, "--version").Output(); err == nil {
		agent.Version = firstLine(string(version))
	}

	// The dispatch verdict needs the node's model/injection config; a missing
	// or unparseable config degrades to defaults rather than refusing the
	// diagnostic itself.
	cfg, cfgErr := config.Load(configPath)
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, "panda: "+cfgErr.Error())
	}
	if cfg == nil {
		cfg = config.Default()
	}
	ag := ledger.Agent{Adapter: k.Adapter}
	card := ledger.Card{Agents: map[string]ledger.Agent{k.Name: ag}}
	router := commander.NewRouter(card, commander.NewExecutor(), cfg.Model, cfg.Injection, cfg.Routing)
	chk := router.CheckAgent(k.Name, ag)

	// Re-probe the endpoint fresh so the printed latency is what the network
	// looks like now; ProbeAgentFresh writes its verdict back to the cache, so
	// a second CheckAgent reflects it if the cached one was stale.
	var latency time.Duration
	if chk.Endpoint != "" {
		fresh, lat := router.ProbeAgentFresh(ag)
		latency = lat
		if fresh.OK != chk.Reachable || fresh.Detail != chk.Detail {
			chk = router.CheckAgent(k.Name, ag)
		}
	}

	detail := agent.Version
	if !chk.Usable {
		detail = chk.Reason
		if detail == "" {
			detail = "not usable"
		}
	}
	reportAgentTestDetail(loc, agent, chk, detail, latency)
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

// reportAgentTestDetail prints the full dispatch-readiness report: the
// pass/fail headline plus subdued detail lines naming the model source and
// the endpoint verdict — the two facts that decide whether a task dispatched
// to this harness would run or hang.
func reportAgentTestDetail(loc i18n.Locale, agent agentStatus, chk commander.AgentCheck, detail string, latency time.Duration) {
	if jsonOutput {
		emitJSON(map[string]any{
			"name": agent.Name, "ok": chk.Usable, "path": agent.Path,
			"version": agent.Version, "detail": detail,
			"model_source": chk.ModelSrc, "model": chk.Model,
			"endpoint": chk.Endpoint, "reachable": chk.Reachable,
			"endpoint_status": chk.Detail, "credentials_rejected": chk.Rejected,
		})
		if !chk.Usable {
			os.Exit(1)
		}
		return
	}
	p := pal()
	if chk.Usable {
		fmt.Println(i18n.Tf(loc, "cli.agents.test.ok", "name", agent.Name, "detail", detail))
	} else {
		fmt.Println(i18n.Tf(loc, "cli.agents.test.fail", "name", agent.Name, "detail", detail))
	}
	var src string
	switch chk.ModelSrc {
	case "injected":
		src = i18n.Tf(loc, "cli.agents.src.injected", "model", chk.Model)
	case "self":
		src = i18n.T(loc, "cli.agents.src.self")
	case "own":
		if chk.Model != "" {
			src = i18n.Tf(loc, "cli.agents.src.own.model", "model", chk.Model)
		} else {
			src = i18n.T(loc, "cli.agents.src.own")
		}
	default:
		src = i18n.T(loc, "cli.agents.src.none")
	}
	fmt.Println("    " + p.Muted(i18n.Tf(loc, "cli.agents.test.model", "source", src)))
	if chk.Endpoint != "" {
		status := i18n.T(loc, "cli.agents.test.unreachable")
		if chk.Reachable {
			status = i18n.Tf(loc, "cli.agents.test.reachable", "ms", fmt.Sprintf("%d", latency.Milliseconds()))
		}
		// The probe's HTTP status (or transport error) says why the verdict
		// came out this way — "http 401" on reachable means the service is up
		// but the key is rejected, "http 503" on unreachable means the
		// provider is broken right now.
		if chk.Detail != "" {
			status += " · " + chk.Detail
		}
		fmt.Println("    " + p.Muted(i18n.Tf(loc, "cli.agents.test.endpoint", "url", chk.Endpoint, "status", status)))
	}
	if !chk.Usable {
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
