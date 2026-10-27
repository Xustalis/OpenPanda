// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda config` — view and edit config.yaml from the CLI with the same
// comment-preserving writers the web settings page uses. Six sections cover
// the policy surface: model, mcp, limits, routing, injection, approval.
// Every set prints a restart hint — the daemon and any running panel cache
// their config at startup.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/mcp"
)

var configSections = []string{"model", "mcp", "limits", "routing", "injection", "approval", "delegation", "self_evolution"}

func isConfigAction(s string) bool {
	switch s {
	case "get", "set", "test":
		return true
	}
	return false
}

func runConfig(args []string) {
	if len(args) == 0 {
		configUsage()
		os.Exit(2)
	}
	section, rest := args[0], args[1:]
	valid := false
	for _, s := range configSections {
		if s == section {
			valid = true
			break
		}
	}
	if !valid && isConfigAction(section) && len(rest) > 0 {
		// `panda config get model` — action/section swapped; accept the order
		// users naturally type.
		for _, s := range configSections {
			if s == rest[0] {
				section, rest = rest[0], append([]string{section}, rest[1:]...)
				valid = true
				break
			}
		}
	}
	if !valid {
		switch section {
		case "help", "-h", "--help":
			configUsage()
			return
		default:
			failUsage(2, "panda: "+i18n.Tf(i18n.Detect(), "cli.unknownNamed", "kind", "config section", "name", section), configUsage)
		}
	}
	if len(rest) == 0 {
		rest = []string{"get"}
	}
	action, rest := rest[0], rest[1:]
	switch action {
	case "get":
		runConfigGet(section, rest)
	case "set":
		runConfigSet(section, rest)
	case "test":
		runConfigTest(section, rest)
	case "help", "-h", "--help":
		configUsage()
	default:
		failUsage(2, "panda: "+i18n.Tf(i18n.Detect(), "cli.unknownNamedOpts", "kind", "config action", "name", action, "opts", "get|set|test"), configUsage)
	}
}

func configUsage() {
	fmt.Fprintln(os.Stderr, "usage: panda config <section> <get|set|test> [args]")
	fmt.Fprintln(os.Stderr, "sections: "+strings.Join(configSections, "|"))
	fmt.Fprintln(os.Stderr, "  model get|test | set [--base-url U] [--api-key K] [--model M] [--max-tokens N]")
	fmt.Fprintln(os.Stderr, "  mcp get|test | set \"<stdio command>\"   (empty command disables MCP)")
	fmt.Fprintln(os.Stderr, "  limits get | set <user|memory|project> <int>")
	fmt.Fprintln(os.Stderr, "  routing get | set preferred_agents <a,b,c>   (empty clears)")
	fmt.Fprintln(os.Stderr, "  delegation get | set <max_children|fanout|max_depth> <int> | set enabled <true|false>")
	fmt.Fprintln(os.Stderr, "  injection get | set <auto|always|never>")
	fmt.Fprintln(os.Stderr, "  approval get | set <always|on-request|never>")
	fmt.Fprintln(os.Stderr, "writes preserve YAML comments; changes apply after the daemon/panel restarts")
}

func configWritePath(flagPath string) string {
	if flagPath != "" && flagPath != config.DefaultPath {
		return flagPath
	}
	if env := os.Getenv("OPENPANDA_CONFIG_PATH"); env != "" {
		return env
	}
	if p, err := config.UserConfigPath(); err == nil {
		return p
	}
	return config.ResolvePath(flagPath)
}

func runConfigGet(section string, args []string) {
	fs := flag.NewFlagSet("config "+section+" get", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	rejectExtraArgs(fs, "usage: panda config "+section+" get")
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}

	switch section {
	case "model":
		if jsonOutput {
			emitJSON(map[string]any{
				"api_type": cfg.Model.NormalizedAPIType(), "base_url": cfg.Model.BaseURL,
				"model": cfg.Model.Model, "max_tokens": cfg.Model.MaxTokens,
				"api_key_set": cfg.Model.APIKey != "", "api_key_hint": maskAPIKey(cfg.Model.APIKey),
			})
			return
		}
		fmt.Printf("api_type:   %s\nbase_url:   %s\nmodel:      %s\nmax_tokens: %d\napi_key:    %s\n",
			cfg.Model.NormalizedAPIType(), cfg.Model.BaseURL, cfg.Model.Model, cfg.Model.MaxTokens, maskAPIKey(cfg.Model.APIKey))
	case "mcp":
		if jsonOutput {
			emitJSON(map[string]string{"command": cfg.MCP.Command})
			return
		}
		if cfg.MCP.Command == "" {
			fmt.Println(i18n.T(i18n.Detect(), "cli.config.mcp.disabled"))
			return
		}
		fmt.Println("command: " + cfg.MCP.Command)
	case "limits":
		if jsonOutput {
			emitJSON(map[string]int{"user": cfg.Memory.Limits.User, "memory": cfg.Memory.Limits.Memory, "project": cfg.Memory.Limits.Project})
			return
		}
		fmt.Printf("user:    %d\nmemory:  %d\nproject: %d\n", cfg.Memory.Limits.User, cfg.Memory.Limits.Memory, cfg.Memory.Limits.Project)
	case "delegation":
		enabled := true
		if cfg.Delegation.Enabled != nil {
			enabled = *cfg.Delegation.Enabled
		}
		if jsonOutput {
			emitJSON(map[string]any{"max_children": cfg.Delegation.MaxChildren, "fanout": cfg.Delegation.FanOut, "max_depth": cfg.Delegation.MaxDepth, "enabled": enabled})
			return
		}
		fmt.Printf("enabled:      %v\nmax_children: %d\nfanout:       %d\nmax_depth:    %d\n", enabled, cfg.Delegation.MaxChildren, cfg.Delegation.FanOut, cfg.Delegation.MaxDepth)
	case "routing":
		if jsonOutput {
			emitJSON(map[string]any{
				"preferred_agents": cfg.Routing.PreferredAgents,
				"agent_models":     cfg.Routing.AgentModels,
			})
			return
		}
		if len(cfg.Routing.PreferredAgents) == 0 && len(cfg.Routing.AgentModels) == 0 {
			fmt.Println(i18n.T(i18n.Detect(), "cli.config.routing.empty"))
			return
		}
		if len(cfg.Routing.PreferredAgents) > 0 {
			fmt.Println("preferred_agents: " + strings.Join(cfg.Routing.PreferredAgents, ", "))
		}
		agents := make([]string, 0, len(cfg.Routing.AgentModels))
		for name := range cfg.Routing.AgentModels {
			agents = append(agents, name)
		}
		sort.Strings(agents)
		for _, name := range agents {
			fmt.Printf("agent_models.%s: %s\n", name,
				strings.Join(cfg.Routing.AgentModels[name], ", "))
		}
	case "injection":
		if jsonOutput {
			emitJSON(map[string]string{"model": cfg.Injection.NormalizedModel()})
			return
		}
		fmt.Println("model: " + cfg.Injection.NormalizedModel())
	case "approval":
		if jsonOutput {
			emitJSON(map[string]string{"mode": cfg.Approval.NormalizedMode()})
			return
		}
		fmt.Println("mode: " + cfg.Approval.NormalizedMode())
	case "self_evolution":
		se := cfg.SelfEvolution
		if jsonOutput {
			emitJSON(map[string]any{
				"enabled": se.Enabled, "repo": se.Repo,
				"producers": se.Producers, "rebase_on_update": se.RebaseOnUpdate,
				"state_dir": cfg.SelfEvolutionDir(),
			})
			return
		}
		fmt.Printf("enabled: %v\nrepo: %s\nproducers: %s\nrebase_on_update: %v\nstate_dir: %s\n",
			se.Enabled, se.Repo, se.Producers, se.RebaseOnUpdate, cfg.SelfEvolutionDir())
	}
}

// maskAPIKey renders a secret the way the web settings page does: only the
// last four characters are visible.
func maskAPIKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 4 {
		return "****"
	}
	return "…" + key[len(key)-4:]
}

func runConfigSet(section string, args []string) {
	loc := i18n.Detect()
	switch section {
	case "model":
		fs := flag.NewFlagSet("config model set", flag.ExitOnError)
		configPath := fs.String("config", cliConfigPath, "path to config.yaml")
		baseURL := fs.String("base-url", "", "model endpoint base URL")
		apiKey := fs.String("api-key", "", "model API key (empty keeps the stored one)")
		model := fs.String("model", "", "model name")
		maxTokens := fs.Int("max-tokens", 0, "max tokens per completion (0 keeps current)")
		authProvider := fs.String("auth", "", "subscription OAuth provider (e.g. anthropic; 'none' clears)")
		fs.Parse(reorderFlags(fs, args))
		rejectExtraArgs(fs, "usage: panda config model set [--base-url U] [--model M] [--api-key K] [--max-tokens N] [--auth P]")
		cfg, err := config.Load(*configPath)
		if err != nil {
			fatal("load config", err)
		}
		mc := cfg.Model
		if v := strings.TrimSpace(*baseURL); v != "" {
			mc.BaseURL = v
		}
		if v := strings.TrimSpace(*model); v != "" {
			mc.Model = v
		}
		if *maxTokens > 0 {
			mc.MaxTokens = *maxTokens
		}
		if v := strings.TrimSpace(*apiKey); v != "" {
			mc.APIKey = v
		}
		if v := strings.TrimSpace(*authProvider); v != "" {
			if strings.EqualFold(v, "none") {
				mc.Auth = ""
			} else {
				mc.Auth = v
			}
		}
		// Build a client first: invalid endpoints fail here without touching
		// the stored config (same guard as the web settings page).
		if _, err := entry.NewClient(mc); err != nil {
			fatal("config model set", err)
		}
		if err := config.UpdateModelSection(configWritePath(*configPath), mc); err != nil {
			fatal("config model set", err)
		}
		configSetDone(loc, "model")
	case "mcp":
		fs := flag.NewFlagSet("config mcp set", flag.ExitOnError)
		configPath := fs.String("config", cliConfigPath, "path to config.yaml")
		fs.Parse(reorderFlags(fs, args))
		command := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if err := config.UpdateMCPSection(configWritePath(*configPath), command); err != nil {
			fatal("config mcp set", err)
		}
		configSetDone(loc, "mcp")
	case "limits":
		fs := flag.NewFlagSet("config limits set", flag.ExitOnError)
		configPath := fs.String("config", cliConfigPath, "path to config.yaml")
		fs.Parse(reorderFlags(fs, args))
		if fs.NArg() != 2 {
			fail(2, "usage: panda config limits set <user|memory|project> <int>")
		}
		key, raw := fs.Arg(0), fs.Arg(1)
		switch key {
		case "user", "memory", "project":
		default:
			fail(2, "panda: "+i18n.Tf(i18n.Detect(), "cli.unknownNamedOpts", "kind", "limits key", "name", key, "opts", "user|memory|project"))
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			failf(2, "panda: limits value must be a positive integer, got %q\n", raw)
		}
		if err := config.UpdateSectionFieldInt(configWritePath(*configPath), []string{"memory", "limits"}, key, value); err != nil {
			fatal("config limits set", err)
		}
		configSetDone(loc, "limits")
	case "delegation":
		fs := flag.NewFlagSet("config delegation set", flag.ExitOnError)
		configPath := fs.String("config", cliConfigPath, "path to config.yaml")
		fs.Parse(reorderFlags(fs, args))
		if fs.NArg() != 2 {
			fail(2, "usage: panda config delegation set <max_children|fanout|max_depth> <int> | enabled <true|false>")
		}
		key, raw := fs.Arg(0), fs.Arg(1)
		if key == "enabled" {
			value, err := strconv.ParseBool(raw)
			if err != nil {
				failf(2, "panda: delegation enabled must be true or false, got %q\n", raw)
			}
			if err := config.UpdateSectionFieldBool(configWritePath(*configPath), []string{"delegation"}, key, value); err != nil {
				fatal("config delegation set", err)
			}
			configSetDone(loc, "delegation")
			return
		}
		switch key {
		case "max_children", "fanout", "max_depth":
		default:
			fail(2, "panda: "+i18n.Tf(i18n.Detect(), "cli.unknownNamedOpts", "kind", "delegation key", "name", key, "opts", "max_children|fanout|max_depth|enabled"))
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			failf(2, "panda: delegation value must be a positive integer, got %q\n", raw)
		}
		if err := config.UpdateSectionFieldInt(configWritePath(*configPath), []string{"delegation"}, key, value); err != nil {
			fatal("config delegation set", err)
		}
		configSetDone(loc, "delegation")
	case "routing":
		fs := flag.NewFlagSet("config routing set", flag.ExitOnError)
		configPath := fs.String("config", cliConfigPath, "path to config.yaml")
		fs.Parse(reorderFlags(fs, args))
		if fs.NArg() < 1 {
			fail(2, "usage: panda config routing set preferred_agents <a,b,c> | agent_models <agent> <m1,m2,...>")
		}
		switch fs.Arg(0) {
		case "agent_models":
			if fs.NArg() < 3 {
				fail(2, "usage: panda config routing set agent_models <agent> <m1,m2,...> (use - to clear)")
			}
			agent := strings.TrimSpace(fs.Arg(1))
			if agent == "" {
				fail(2, "panda: agent name is required")
			}
			var models []string
			for _, name := range strings.Split(fs.Arg(2), ",") {
				if name = strings.TrimSpace(name); name != "" {
					models = append(models, name)
				}
			}
			if len(models) == 1 && (models[0] == "-" || models[0] == "none") {
				models = nil
			}
			if err := config.UpdateSectionList(configWritePath(*configPath),
				[]string{"routing", "agent_models"}, agent, models); err != nil {
				fatal("config routing set", err)
			}
		case "preferred_agents":
			var agents []string
			if fs.NArg() == 2 {
				for _, name := range strings.Split(fs.Arg(1), ",") {
					if name = strings.TrimSpace(name); name != "" {
						agents = append(agents, name)
					}
				}
			}
			if err := config.UpdateSectionList(configWritePath(*configPath),
				[]string{"routing"}, "preferred_agents", agents); err != nil {
				fatal("config routing set", err)
			}
		default:
			fail(2, "usage: panda config routing set preferred_agents <a,b,c> | agent_models <agent> <m1,m2,...>")
		}
		configSetDone(loc, "routing")
	case "injection":
		fs := flag.NewFlagSet("config injection set", flag.ExitOnError)
		configPath := fs.String("config", cliConfigPath, "path to config.yaml")
		fs.Parse(reorderFlags(fs, args))
		value := strings.TrimSpace(fs.Arg(0))
		switch value {
		case config.InjectionModelAuto, config.InjectionModelAlways, config.InjectionModelNever:
		default:
			fail(2, "panda: injection model must be auto, always, or never")
		}
		if err := config.UpdateSectionField(configWritePath(*configPath), []string{"injection"}, "model", value); err != nil {
			fatal("config injection set", err)
		}
		configSetDone(loc, "injection")
	case "approval":
		fs := flag.NewFlagSet("config approval set", flag.ExitOnError)
		configPath := fs.String("config", cliConfigPath, "path to config.yaml")
		fs.Parse(reorderFlags(fs, args))
		value := strings.TrimSpace(fs.Arg(0))
		switch value {
		case config.ApprovalModeAlways, config.ApprovalModeOnRequest, config.ApprovalModeNever:
		default:
			fail(2, "panda: approval mode must be always, on-request, or never")
		}
		if err := config.UpdateSectionField(configWritePath(*configPath), []string{"approval"}, "mode", value); err != nil {
			fatal("config approval set", err)
		}
		configSetDone(loc, "approval")
	case "self_evolution":
		fs := flag.NewFlagSet("config self_evolution set", flag.ExitOnError)
		configPath := fs.String("config", cliConfigPath, "path to config.yaml")
		fs.Parse(reorderFlags(fs, args))
		if fs.NArg() != 2 {
			fail(2, "usage: panda config self_evolution set <enabled|rebase_on_update> <true|false> | <repo> <path> | <producers> <self|mesh>")
		}
		key, raw := fs.Arg(0), fs.Arg(1)
		path := configWritePath(*configPath)
		switch key {
		case "enabled", "rebase_on_update":
			value, err := strconv.ParseBool(raw)
			if err != nil {
				failf(2, "panda: self_evolution %s must be true or false, got %q\n", key, raw)
			}
			if err := config.UpdateSectionFieldBool(path, []string{"self_evolution"}, key, value); err != nil {
				fatal("config self_evolution set", err)
			}
		case "repo", "producers":
			if key == "producers" && raw != "self" && raw != "mesh" {
				failf(2, "panda: producers must be self or mesh, got %q\n", raw)
			}
			if key == "repo" && strings.TrimSpace(raw) == "" {
				fail(2, "panda: repo must name this node's OpenPanda checkout")
			}
			if err := config.UpdateSectionField(path, []string{"self_evolution"}, key, raw); err != nil {
				fatal("config self_evolution set", err)
			}
		default:
			failf(2, "panda: unknown self_evolution key %q (want enabled|repo|producers|rebase_on_update)\n", key)
		}
		// Reload+validate the result: enabling without repo, or a bad enum,
		// must fail here rather than at the next daemon start.
		if cfg, err := config.Load(path); err != nil {
			fatal("config self_evolution set", err)
		} else if err := cfg.SelfEvolution.Validate(); err != nil {
			fatal("config self_evolution set", err)
		}
		configSetDone(loc, "self_evolution")
	}
}

// configSetDone reports a successful write plus the restart hint.
func configSetDone(loc i18n.Locale, section string) {
	if jsonOutput {
		emitJSON(map[string]string{"section": section, "status": "saved"})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.config.saved", "section", section))
	fmt.Println(i18n.T(loc, "cli.config.restart"))
}

// runConfigTest runs a live check: `model test` does a one-word completion,
// `mcp test` spawns the server and lists its tools.
func runConfigTest(section string, args []string) {
	fs := flag.NewFlagSet("config "+section+" test", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	rejectExtraArgs(fs, "usage: panda config "+section+" test")
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	loc := i18n.Detect()

	switch section {
	case "model":
		client, err := entry.NewClient(cfg.Model)
		if err != nil {
			reportTest(loc, "model", false, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		answer, err := client.Complete(ctx, "You are a connectivity test.", "Reply with exactly: OK")
		if err != nil {
			reportTest(loc, "model", false, err.Error())
			return
		}
		reportTest(loc, "model", true, answer)
	case "mcp":
		if cfg.MCP.Command == "" {
			reportTest(loc, "mcp", false, i18n.T(loc, "cli.config.mcp.disabled"))
			return
		}
		if err := testMCPCommand(context.Background(), cfg.MCP.Command); err != nil {
			reportTest(loc, "mcp", false, err.Error())
			return
		}
		reportTest(loc, "mcp", true, i18n.T(loc, "cli.config.mcp.ok"))
	default:
		failf(2, "panda: config test supports model|mcp, not %q\n", section)
	}
}

// testMCPCommand spawns the configured stdio MCP server and lists its tools —
// the same probe the web settings page runs.
func testMCPCommand(ctx context.Context, command string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	parts, err := splitMCPCommand(command)
	if err != nil {
		return err
	}
	client, err := mcp.NewStdioClient(ctx, parts[0], nil, parts[1:]...)
	if err != nil {
		return err
	}
	defer client.Close()
	if _, err := client.ListTools(ctx); err != nil {
		return err
	}
	return nil
}

// splitMCPCommand splits a space-separated argv, honoring double quotes (the
// documented mcp.command format).
func splitMCPCommand(command string) ([]string, error) {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range command {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	if inQuote {
		return nil, fmt.Errorf("unbalanced quotes in %q", command)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return parts, nil
}

func reportTest(loc i18n.Locale, section string, ok bool, detail string) {
	if jsonOutput {
		emitJSON(map[string]any{"section": section, "ok": ok, "detail": detail})
		if !ok {
			os.Exit(1)
		}
		return
	}
	if ok {
		fmt.Println(i18n.Tf(loc, "cli.config.test.ok", "section", section, "detail", detail))
	} else {
		fmt.Println(i18n.Tf(loc, "cli.config.test.fail", "section", section, "detail", detail))
		os.Exit(1)
	}
}
