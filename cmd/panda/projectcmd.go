package main

// `panda project` — project memories from the CLI: list what exists and
// create new ones (idempotent empty seed, same as the panel's POST
// /api/projects and the REPL's /project).

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

func runProject(args []string) {
	if len(args) == 0 {
		args = []string{"list"}
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list", "ls":
		runProjectList(rest)
	case "create", "new":
		runProjectCreate(rest)
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stderr, "usage: panda project <list|create>")
		fmt.Fprintln(os.Stderr, "  list          list project memories")
		fmt.Fprintln(os.Stderr, "  create <name> create a project memory (idempotent)")
	default:
		fmt.Fprintf(os.Stderr, "panda: unknown project verb %q\n", verb)
		os.Exit(2)
	}
}

func runProjectList(args []string) {
	fs := flag.NewFlagSet("project list", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	projects := memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, memoryLimits(cfg))
	names, err := projects.List()
	if err != nil {
		fatal("list projects", err)
	}
	sort.Strings(names)
	loc := i18n.Detect()
	if jsonOutput {
		emitJSON(names)
		return
	}
	if len(names) == 0 {
		fmt.Println(i18n.T(loc, "cli.project.none"))
		return
	}
	for _, n := range names {
		m, err := projects.Load(n)
		if err != nil || len(m.Entries) == 0 {
			fmt.Printf("  %-20s (empty)\n", n)
			continue
		}
		fmt.Printf("  %-20s entries=%d chars=%d\n", n, len(m.Entries), m.Chars())
	}
}

func runProjectCreate(args []string) {
	fs := flag.NewFlagSet("project create", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		fmt.Fprintln(os.Stderr, "usage: panda project create <name>")
		os.Exit(2)
	}
	if err := memory.ValidateName(name); err != nil {
		fmt.Fprintf(os.Stderr, "panda: %v\n", err)
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	projects := memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, memoryLimits(cfg))
	if err := projects.Save(name, memory.MemFile{Limit: projects.Limit()}); err != nil {
		fatal("create project", err)
	}
	if jsonOutput {
		emitJSON(map[string]string{"name": name, "status": "created"})
		return
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.project.created", "name", name))
}
