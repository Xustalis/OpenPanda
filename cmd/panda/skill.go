package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/skills"
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
			fatalf("%s", i18n.Tf(i18n.Detect(), "cli.skill.needName", "cmd", cmd))
		}
		approveSkill(store, positional[0], cmd == "approve")
	default:
		fmt.Fprintln(os.Stderr, i18n.Tf(i18n.Detect(), "cli.skill.unknown", "cmd", cmd))
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
	if jsonOutput {
		if index == nil {
			emitJSON([]struct{}{})
			return
		}
		emitJSON(index)
		return
	}
	if len(index) == 0 {
		fmt.Println(i18n.T(i18n.Detect(), "cli.skill.none"))
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
				fatalf("%s", i18n.Tf(i18n.Detect(), "cli.skill.duplicate", "name", name))
			}
			entry = &index[i]
		}
	}
	if entry == nil {
		fatalf("%s", i18n.Tf(i18n.Detect(), "cli.skill.notFound", "name", name))
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
	if jsonOutput {
		status := "approved"
		if !approve {
			status = "rejected"
		}
		emitJSON(map[string]string{"name": name, "status": status})
		return
	}
	if approve {
		fmt.Println(i18n.Tf(i18n.Detect(), "cli.skill.approved", "name", name))
	} else {
		fmt.Println(i18n.Tf(i18n.Detect(), "cli.skill.rejected", "name", name))
	}
}

// fatalf reports a usage error and exits, mirroring fatal for non-error paths.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "panda: %s\n", fmt.Sprintf(format, args...))
	os.Exit(1)
}
