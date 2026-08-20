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
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/mcp"
)

var configSections = []string{"model", "mcp", "limits", "routing", "injection", "approval"}

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
	if !valid {
		switch section {
		case "help", "-h", "--help":
			configUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "panda: unknown config section %q\n", section)
			configUsage()
			os.Exit(2)
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
		fmt.Fprintf(os.Stderr, "panda: unknown config action %q (get|set|test)\n", action)
		configUsage()
		os.Exit(2)
	}
}

func configUsage() {
	fmt.Fprintln(os.Stderr, "usage: panda config <section> <get|set|test> [args]")
	fmt.Fprintln(os.Stderr, "sections: "+strings.Join(configSections, "|"))
	fmt.Fprintln(os.Stderr, "  model get|test | set [--base-url U] [--api-key K] [--model M] [--max-tokens N]")
	fmt.Fprintln(os.Stderr, "  mcp get|test | set \"<stdio command>\"   (empty command disables MCP)")
	fmt.Fprintln(os.Stderr, "  limits get | set <user|memory|project> <int>")
	fmt.Fprintln(os.Stderr, "  routing get | set preferred_agents <a,b,c>   (empty clears)")
	fmt.Fprintln(os.Stderr, "  injection get | set <auto|always|never>")
	fmt.Fprintln(os.Stderr, "  approval get | set <always|on-request|never>")
	fmt.Fprintln(os.Stderr, "writes preserve YAML comments; changes apply after the daemon/panel restarts")
}

func configWritePath(flagPath string) string { return config.ResolvePath(flagPath) }

func runConfigGet(section string, args []string) {
	fs := flag.NewFlagSet("config "+section+" get", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
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
	case "routing":
		if jsonOutput {
			emitJSON(map[string]any{"preferred_agents": cfg.Routing.PreferredAgents})
			return
		}
		if len(cfg.Routing.PreferredAgents) == 0 {
			fmt.Println(i18n.T(i18n.Detect(), "cli.config.routing.empty"))
			return
		}
		fmt.Println("preferred_agents: " + strings.Join(cfg.Routing.PreferredAgents, ", "))
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
		configPath := fs.String("config", "", "path to config.yaml")
		baseURL := fs.String("base-url", "", "model endpoint base URL")
		apiKey := fs.String("api-key", "", "model API key (empty keeps the stored one)")
		model := fs.String("model", "", "model name")
		maxTokens := fs.Int("max-tokens", 0, "max tokens per completion (0 keeps current)")
		fs.Parse(args)
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
		configPath := fs.String("config", "", "path to config.yaml")
		fs.Parse(args)
		command := strings.TrimSpace(strings.Join(fs.Args(), " "))
		if err := config.UpdateMCPSection(configWritePath(*configPath), command); err != nil {
			fatal("config mcp set", err)
		}
		configSetDone(loc, "mcp")
	case "limits":
		fs := flag.NewFlagSet("config limits set", flag.ExitOnError)
		configPath := fs.String("config", "", "path to config.yaml")
		fs.Parse(args)
		if fs.NArg() != 2 {
			fmt.Fprintln(os.Stderr, "usage: panda config limits set <user|memory|project> <int>")
			os.Exit(2)
		}
		key, raw := fs.Arg(0), fs.Arg(1)
		switch key {
		case "user", "memory", "project":
		default:
			fmt.Fprintf(os.Stderr, "panda: unknown limits key %q (user|memory|project)\n", key)
			os.Exit(2)
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			fmt.Fprintf(os.Stderr, "panda: limits value must be a positive integer, got %q\n", raw)
			os.Exit(2)
		}
		if err := config.UpdateSectionFieldInt(configWritePath(*configPath), []string{"memory", "limits"}, key, value); err != nil {
			fatal("config limits set", err)
		}
		configSetDone(loc, "limits")
	case "routing":
		fs := flag.NewFlagSet("config routing set", flag.ExitOnError)
		configPath := fs.String("config", "", "path to config.yaml")
		fs.Parse(args)
		if fs.NArg() < 1 || fs.Arg(0) != "preferred_agents" {
			fmt.Fprintln(os.Stderr, "usage: panda config routing set preferred_agents <a,b,c>")
			os.Exit(2)
		}
		var agents []string
		if fs.NArg() == 2 {
			for _, name := range strings.Split(fs.Arg(1), ",") {
				if name = strings.TrimSpace(name); name != "" {
					agents = append(agents, name)
				}
			}
		}
		if err := config.UpdateSectionList(configWritePath(*configPath), []string{"routing"}, "preferred_agents", agents); err != nil {
			fatal("config routing set", err)
		}
		configSetDone(loc, "routing")
	case "injection":
		fs := flag.NewFlagSet("config injection set", flag.ExitOnError)
		configPath := fs.String("config", "", "path to config.yaml")
		fs.Parse(args)
		value := strings.TrimSpace(fs.Arg(0))
		switch value {
		case config.InjectionModelAuto, config.InjectionModelAlways, config.InjectionModelNever:
		default:
			fmt.Fprintln(os.Stderr, "panda: injection model must be auto, always, or never")
			os.Exit(2)
		}
		if err := config.UpdateSectionField(configWritePath(*configPath), []string{"injection"}, "model", value); err != nil {
			fatal("config injection set", err)
		}
		configSetDone(loc, "injection")
	case "approval":
		fs := flag.NewFlagSet("config approval set", flag.ExitOnError)
		configPath := fs.String("config", "", "path to config.yaml")
		fs.Parse(args)
		value := strings.TrimSpace(fs.Arg(0))
		switch value {
		case config.ApprovalModeAlways, config.ApprovalModeOnRequest, config.ApprovalModeNever:
		default:
			fmt.Fprintln(os.Stderr, "panda: approval mode must be always, on-request, or never")
			os.Exit(2)
		}
		if err := config.UpdateSectionField(configWritePath(*configPath), []string{"approval"}, "mode", value); err != nil {
			fatal("config approval set", err)
		}
		configSetDone(loc, "approval")
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
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
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
		fmt.Fprintf(os.Stderr, "panda: config test supports model|mcp, not %q\n", section)
		os.Exit(2)
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
		return
	}
	if ok {
		fmt.Println(i18n.Tf(loc, "cli.config.test.ok", "section", section, "detail", detail))
	} else {
		fmt.Println(i18n.Tf(loc, "cli.config.test.fail", "section", section, "detail", detail))
		os.Exit(1)
	}
}
