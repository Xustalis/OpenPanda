package main

// Command init bootstraps a new node interactively — the first thing a fresh
// user runs after `panda install`. It prompts for the node name, resource
// class, and model provider, then writes config.yaml and a capabilities card,
// so going from "downloaded binary" to "working node" needs no hand-edited
// YAML. Hardware detection (detectCard) and config defaults (config.Default)
// do the heavy lifting.
//
//	panda init                                    # interactive, user-writable config
//	panda init --config ./config.yaml --card ./capabilities.yaml

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"gopkg.in/yaml.v3"
)

func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	configPath := fs.String("config", "", "path to write config.yaml (default: user config dir)")
	cardPath := fs.String("card", "", "path to write capabilities.yaml (default: <config dir>/capabilities.yaml)")
	fs.Parse(args)

	loc := i18n.Detect()
	target := resolveInitConfigPath(*configPath)

	if exists(target) {
		fmt.Println(i18n.Tf(loc, "init.exists", "path", target))
		os.Exit(1)
	}

	in := bufio.NewReader(os.Stdin)
	prompt := func(label, def string) string {
		if def != "" {
			fmt.Printf("%s [%s]: ", label, def)
		} else {
			fmt.Printf("%s: ", label)
		}
		s, _ := in.ReadString('\n')
		s = strings.TrimSpace(s)
		if s == "" {
			return def
		}
		return s
	}
	// promptValid re-asks while the answer fails accept(); empty keeps the default.
	promptValid := func(label, def string, accept func(string) bool) string {
		for {
			s := prompt(label, def)
			if accept(s) {
				return s
			}
			fmt.Println(i18n.T(loc, "init.invalid"))
		}
	}

	// Reuse the hardware scan as sensible defaults for the two node prompts.
	card := detectCard()
	def := config.Default()

	nodeName := prompt(i18n.T(loc, "init.node.name"), card.Device)
	resourceClass := promptValid(i18n.T(loc, "init.node.class"), card.ResourceClass,
		func(s string) bool { return config.ValidResourceClass(s) })
	apiType := promptValid(i18n.T(loc, "init.model.apitype"), config.APITypeAnthropic,
		func(s string) bool { return s == config.APITypeAnthropic || s == config.APITypeOpenAI })
	baseURL := prompt(i18n.T(loc, "init.model.baseurl"), def.Model.BaseURL)
	model := prompt(i18n.T(loc, "init.model.name"), def.Model.Model)
	apiKey := prompt(i18n.T(loc, "init.model.apikey"), "")

	def.Node.Name = orDefault(nodeName, def.Node.Name)
	def.Node.ResourceClass = orDefault(resourceClass, def.Node.ResourceClass)
	def.Model.APIType = orDefault(apiType, config.APITypeAnthropic)
	def.Model.BaseURL = orDefault(baseURL, def.Model.BaseURL)
	def.Model.Model = orDefault(model, def.Model.Model)
	def.Model.APIKey = apiKey

	// Belt and braces: never write a config the node would refuse to load.
	if err := def.Validate(); err != nil {
		fatal("validate config", err)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fatal("create config dir", err)
	}
	data, err := yaml.Marshal(def)
	if err != nil {
		fatal("render config", err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		fatal("write config", err)
	}
	fmt.Println(i18n.Tf(loc, "init.config.written", "path", target))

	// Capability card next to the config unless the user named one.
	cardOut := *cardPath
	if cardOut == "" {
		cardOut = filepath.Join(filepath.Dir(target), "capabilities.yaml")
	}
	cardData, err := yaml.Marshal(card)
	if err != nil {
		fatal("render card", err)
	}
	if err := os.WriteFile(cardOut, cardData, 0o644); err != nil {
		fatal("write card", err)
	}
	fmt.Println(i18n.Tf(loc, "init.card.written", "path", cardOut))

	fmt.Println(i18n.Tf(loc, "init.next", "config", target, "card", cardOut))
}

// resolveInitConfigPath picks where init writes: an explicit flag or
// OPENPANDA_CONFIG_PATH wins, then a user-writable user config dir, then the
// system default. This matches ResolvePath's read order, so a config written
// here is read back automatically by `panda web` and the daemon.
func resolveInitConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("OPENPANDA_CONFIG_PATH"); env != "" {
		return env
	}
	if p, err := config.UserConfigPath(); err == nil {
		return p
	}
	return config.DefaultPath
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}