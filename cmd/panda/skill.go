package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/skills"
)

// runSkill implements `panda skill list|approve|reject` — the user-facing
// approval flow for auto-generated skills (design §8.2). Generated skills are
// pending until approved here; only active skills are loaded into agent
// execution context, so nothing auto-generated takes effect without a human
// sign-off.
func runSkill(args []string) {
	// A bare `panda skill` lists skills (the most common action); subcommands
	// approve/reject drive the pending-skill approval flow. --config may appear
	// anywhere, so strip it first and treat the first remaining arg as the
	// subcommand (mirroring the flag.FlagSet behavior of the other subcommands).
	configPath, positional := splitConfig(args)

	cmd := "list"
	if len(positional) > 0 {
		cmd = positional[0]
		positional = positional[1:]
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config", err)
	}
	store := skills.NewStore(cfg.Storage.SkillsPath)

	switch cmd {
	case "list":
		skillList(store)
	case "approve", "reject":
		if len(positional) != 1 {
			fatalf("skill %s needs exactly one skill name", cmd)
		}
		approveSkill(store, positional[0], cmd == "approve")
	default:
		fmt.Fprintf(os.Stderr, "unknown skill command %q\n", cmd)
		os.Exit(2)
	}
}

// splitConfig pulls an optional --config PATH (or --config=PATH) out of args in
// any position, returning the path and the remaining positional arguments.
func splitConfig(args []string) (string, []string) {
	configPath := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config" && i+1 < len(args):
			configPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			configPath = strings.TrimPrefix(args[i], "--config=")
		default:
			rest = append(rest, args[i])
		}
	}
	return configPath, rest
}

// skillList prints every skill (name, scope, status, usage, description).
func skillList(store *skills.Store) {
	index, err := store.Index()
	if err != nil {
		fatal("index skills", err)
	}
	if len(index) == 0 {
		fmt.Println("（无技能）")
		return
	}
	sort.Slice(index, func(i, j int) bool { return index[i].Name < index[j].Name })
	for _, e := range index {
		scope := string(e.Scope)
		if e.Key != "" {
			scope += ":" + e.Key
		}
		fmt.Printf("%-24s %-14s %-9s used=%d  %s\n", e.Name, scope, e.Status, e.UseCount, e.Description)
	}
}

// approveSkill approves or rejects a pending skill, resolved by its unique name.
func approveSkill(store *skills.Store, name string, approve bool) {
	index, err := store.Index()
	if err != nil {
		fatal("index skills", err)
	}
	var entry *skills.IndexEntry
	for i := range index {
		if index[i].Name == name {
			if entry != nil {
				fatalf("multiple skills named %q; rename to make it unique", name)
			}
			entry = &index[i]
		}
	}
	if entry == nil {
		fatalf("skill %q not found", name)
	}

	var serr error
	if approve {
		serr = store.Approve(entry.Scope, entry.Key, entry.Name)
	} else {
		serr = store.Reject(entry.Scope, entry.Key, entry.Name)
	}
	if serr != nil {
		fatal("update skill", serr)
	}
	action := "已批准"
	if !approve {
		action = "已拒绝"
	}
	fmt.Printf("%s：%s\n", action, name)
}

// fatalf reports a usage error and exits, mirroring fatal for non-error paths.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "panda: %s\n", fmt.Sprintf(format, args...))
	os.Exit(1)
}
