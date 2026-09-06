package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

// runSkill implements `panda skill list|approve|reject|import|hub|install|add` —
// management of procedural workflows (design §8.2, §8.3): auto-generated skills,
// local and remote imported skills, and curated/community Skills Hub packages.
func runSkill(args []string) {
	configPath, positional := splitConfig(args)

	cmd := "list"
	if len(positional) > 0 {
		cmd = positional[0]
		positional = positional[1:]
	}

	if cmd == "--help" || cmd == "-h" {
		printSkillUsage()
		return
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config", err)
	}
	store := skills.NewStore(cfg.Storage.SkillsPath)
	_ = store.EnsureBuiltins()

	switch cmd {
	case "list":
		skillList(store)
	case "approve", "reject":
		if len(positional) != 1 {
			fatalf("%s", i18n.Tf(i18n.Detect(), "cli.skill.needName", "cmd", cmd))
		}
		approveSkill(store, positional[0], cmd == "approve")
	case "reset":
		skillReset(store, positional)
	case "import":
		skillImport(store, positional)
	case "hub":
		skillHub(cfg, store, positional)
	case "install", "add":
		skillInstall(cfg, store, positional)
	default:
		fmt.Fprintln(os.Stderr, i18n.Tf(i18n.Detect(), "cli.skill.unknown", "cmd", cmd))
		printSkillUsage()
		os.Exit(2)
	}
}

func printSkillUsage() {
	fmt.Println("usage: panda skill [--config PATH] <list | approve <name> | reject <name> | reset <name|all> | import <path|url> | hub <list|search|install|info> | install/add <target>>")
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
		tag := ""
		if e.Builtin {
			tag = " [builtin]"
		}
		fmt.Printf("%-24s %-14s %-9s used=%d  %s%s\n", e.Name, scope, e.Status, e.UseCount, e.Description, tag)
	}
}

// skillReset restores one or all built-in skills to factory defaults.
func skillReset(store *skills.Store, args []string) {
	if len(args) == 0 {
		fatalf("panda skill reset: need skill name or 'all'")
	}
	target := args[0]
	if strings.EqualFold(target, "all") {
		for _, b := range skills.BuiltinSkills() {
			if _, err := store.ResetBuiltin(b.Name); err != nil {
				fatalf("reset %s: %v", b.Name, err)
			}
		}
		if jsonOutput {
			emitJSON(map[string]any{"ok": true, "reset": "all"})
			return
		}
		fmt.Println("所有内置技能已重置为出厂默认设置 (All built-in skills reset to factory defaults).")
		return
	}
	sk, err := store.ResetBuiltin(target)
	if err != nil {
		fatalf("%s", err)
	}
	if jsonOutput {
		emitJSON(map[string]any{"ok": true, "name": sk.Name})
		return
	}
	fmt.Printf("已将内置技能 %q 重置为默认版本 (Reset built-in skill %q to factory default).\n", sk.Name, sk.Name)
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

// separateFlags partitions args into flags and positional arguments so that
// flags like --force or --pending are recognized regardless of whether they appear
// before or after positional subcommands/arguments.
func separateFlags(args []string) (flags []string, pos []string) {
	valFlags := map[string]bool{
		"scope": true, "project": true, "device": true, "name": true, "hub": true,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			clean := strings.TrimLeft(arg, "-")
			if idx := strings.Index(clean, "="); idx != -1 {
				continue
			}
			if valFlags[clean] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, arg)
		}
	}
	return flags, pos
}

// skillImport handles `panda skill import <path|url> [--scope S] [--project P] [--device D] [--name N] [--pending] [--force]`.
func skillImport(store *skills.Store, args []string) {
	fs := flag.NewFlagSet("skill import", flag.ContinueOnError)
	scope := fs.String("scope", "", "skill scope (global|project|device)")
	project := fs.String("project", "", "project name for project scope")
	device := fs.String("device", "", "device name for device scope")
	name := fs.String("name", "", "override skill name")
	pending := fs.Bool("pending", false, "import with pending status awaiting approval")
	force := fs.Bool("force", false, "overwrite existing skill")

	flagArgs, posArgs := separateFlags(args)
	if err := fs.Parse(flagArgs); err != nil {
		os.Exit(2)
	}

	if len(posArgs) == 0 {
		fatalf("%s", i18n.T(i18n.Detect(), "cli.skill.needSource"))
	}
	source := posArgs[0]

	status := skills.StatusActive
	if *pending {
		status = skills.StatusPending
	}

	opts := skills.ImportOptions{
		Scope:   skills.Scope(*scope),
		Project: *project,
		Device:  *device,
		Name:    *name,
		Status:  status,
		Force:   *force,
	}

	imported, err := store.ImportSource(context.Background(), source, opts)
	if err != nil {
		fatalf("%s", i18n.Tf(i18n.Detect(), "cli.skill.importFailed", "err", err.Error()))
	}

	var names []string
	for _, sk := range imported {
		names = append(names, sk.Name)
	}

	if jsonOutput {
		emitJSON(map[string]any{
			"count":  len(imported),
			"skills": imported,
		})
		return
	}

	fmt.Println(i18n.Tf(i18n.Detect(), "cli.skill.imported", "count", strconv.Itoa(len(imported)), "names", strings.Join(names, ", ")))
}

// skillHub handles `panda skill hub [list|search <q>|install <name>|info <name>]`.
func skillHub(cfg *config.Config, store *skills.Store, args []string) {
	fs := flag.NewFlagSet("skill hub", flag.ContinueOnError)
	hubFlag := fs.String("hub", cfg.Skills.HubURL, "custom Skills Hub registry URL")
	scope := fs.String("scope", "", "skill scope override")
	project := fs.String("project", "", "project name for project scope")
	device := fs.String("device", "", "device name for device scope")
	force := fs.Bool("force", false, "overwrite existing skill")
	pending := fs.Bool("pending", false, "install with pending status awaiting approval")

	flagArgs, posArgs := separateFlags(args)
	if err := fs.Parse(flagArgs); err != nil {
		os.Exit(2)
	}

	action := "list"
	var target string
	if len(posArgs) > 0 {
		action = posArgs[0]
		if len(posArgs) > 1 {
			target = strings.Join(posArgs[1:], " ")
		}
	}

	ctx := context.Background()
	hubURL := *hubFlag

	switch action {
	case "list", "search":
		query := target
		idx, err := skills.FetchHubIndex(ctx, hubURL)
		if err != nil {
			fatal("fetch hub index", err)
		}
		results := skills.SearchHub(idx, query)
		if jsonOutput {
			emitJSON(results)
			return
		}
		if len(results) == 0 {
			fmt.Println(i18n.T(i18n.Detect(), "cli.skill.hubNone"))
			return
		}
		fmt.Printf("%-22s %-8s %-10s %-20s %s\n", "NAME", "VERSION", "SCOPE", "TAGS", "DESCRIPTION")
		for _, s := range results {
			sc := string(s.Scope)
			if sc == "" {
				sc = "global"
			}
			tags := strings.Join(s.Tags, ",")
			if len(tags) > 19 {
				tags = tags[:18] + "…"
			}
			desc := s.Description
			if len(desc) > 50 {
				desc = desc[:49] + "…"
			}
			fmt.Printf("%-22s %-8s %-10s %-20s %s\n", s.Name, s.Version, sc, tags, desc)
		}

	case "install":
		if target == "" {
			fatalf("%s", i18n.Tf(i18n.Detect(), "cli.skill.needName", "cmd", "hub install"))
		}
		status := skills.StatusActive
		if *pending {
			status = skills.StatusPending
		}
		opts := skills.ImportOptions{
			Scope:   skills.Scope(*scope),
			Project: *project,
			Device:  *device,
			Status:  status,
			Force:   *force,
		}
		sk, err := skills.InstallFromHub(ctx, store, hubURL, target, opts)
		if err != nil {
			fatalf("%s", err)
		}
		if jsonOutput {
			emitJSON(sk)
			return
		}
		fmt.Println(i18n.Tf(i18n.Detect(), "cli.skill.hubInstalled", "name", sk.Name))

	case "info":
		if target == "" {
			fatalf("%s", i18n.Tf(i18n.Detect(), "cli.skill.needName", "cmd", "hub info"))
		}
		idx, err := skills.FetchHubIndex(ctx, hubURL)
		if err != nil {
			fatal("fetch hub index", err)
		}
		var found *skills.HubSkill
		for i := range idx.Skills {
			if strings.EqualFold(idx.Skills[i].Name, target) {
				found = &idx.Skills[i]
				break
			}
		}
		if found == nil {
			fatalf("%s", i18n.Tf(i18n.Detect(), "cli.skill.hubNotFound", "name", target))
		}
		if jsonOutput {
			emitJSON(found)
			return
		}
		fmt.Printf("Name:        %s\n", found.Name)
		fmt.Printf("Description: %s\n", found.Description)
		if found.Author != "" {
			fmt.Printf("Author:      %s\n", found.Author)
		}
		if found.Version != "" {
			fmt.Printf("Version:     %s\n", found.Version)
		}
		if len(found.Tags) > 0 {
			fmt.Printf("Tags:        %s\n", strings.Join(found.Tags, ", "))
		}
		if found.URL != "" {
			fmt.Printf("URL:         %s\n", found.URL)
		}
		if found.DocURL != "" {
			fmt.Printf("Doc:         %s\n", found.DocURL)
		}
		if found.Content != "" {
			fmt.Println("\n--- Content Preview ---")
			lines := strings.Split(found.Content, "\n")
			limit := 20
			if len(lines) < limit {
				limit = len(lines)
			}
			for i := 0; i < limit; i++ {
				fmt.Println(lines[i])
			}
			if len(lines) > limit {
				fmt.Printf("... (%d more lines)\n", len(lines)-limit)
			}
		}

	default:
		fatalf("%s", i18n.Tf(i18n.Detect(), "cli.skill.unknown", "cmd", "hub "+action))
	}
}

// skillInstall is the shorthand smart installer.
// If target is omitted, it prints usage guidance.
// If target is a built-in skill, it informs the user that it is already active.
// If target is a URL or local file, it imports it; otherwise it installs from Hub.
func skillInstall(cfg *config.Config, store *skills.Store, args []string) {
	flagArgs, posArgs := separateFlags(args)
	if len(posArgs) == 0 {
		fmt.Println("内置标准技能已全部就绪生效 (All built-in skills are active by default).")
		fmt.Println()
		fmt.Println("用法 (Usage):")
		fmt.Println("  panda skill add <url | file | archive>   从链接或本地文件导入自定义技能")
		fmt.Println("  panda skill hub search <query>           从技能集市搜索社区扩展技能")
		fmt.Println("  panda skill hub install <name>           从技能集市安装扩展技能")
		fmt.Println("  panda skill reset <name | all>           将内置技能恢复为出厂默认设置")
		return
	}
	target := posArgs[0]
	if strings.EqualFold(target, "all") {
		fmt.Println("所有内置标准技能已默认全部就绪生效 (All built-in skills are active by default).")
		fmt.Println("如需将所有内置技能恢复为出厂设置，请运行: panda skill reset all")
		return
	}
	if skills.IsBuiltinSkill(target) {
		sk, _ := store.Load(skills.ScopeGlobal, "", target)
		if sk == nil {
			for _, b := range skills.BuiltinSkills() {
				if strings.EqualFold(b.Alias, target) {
					sk, _ = store.Load(skills.ScopeGlobal, "", b.Name)
					break
				}
			}
		}
		if sk == nil {
			sk, _ = store.ResetBuiltin(target)
		}
		if sk != nil {
			fmt.Printf("技能 %q 是内置标准技能且已默认激活生效 (Skill %q is a built-in skill and is already active).\n如需恢复默认定义，可运行: panda skill reset %s\n", target, target, target)
			return
		}
	}

	// Check if URL or local file
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		skillImport(store, append(flagArgs, posArgs...))
		return
	}
	if _, err := os.Stat(target); err == nil {
		skillImport(store, append(flagArgs, posArgs...))
		return
	}
	// Fall back to hub install
	skillHub(cfg, store, append(append([]string{"install"}, flagArgs...), posArgs...))
}

// fatalf reports a usage error and exits, mirroring fatal for non-error paths.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "panda: %s\n", fmt.Sprintf(format, args...))
	os.Exit(1)
}
