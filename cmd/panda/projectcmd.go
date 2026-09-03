package main

// `panda project` — the project surface: create one, enter it, look at it,
// rename it, leave it, remove it.
//
// A project used to be a Markdown file and two verbs (list/create), which meant
// the only thing you could do with a project was name it again on every command.
// The metadata now lives in the projects table (internal/projects) and the memory
// file stays where it was (internal/memory.Projects) — this file is what joins
// the two for a human: every verb that touches a project touches both halves, so
// there is no state where the row and the memory file disagree.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func runProject(args []string) {
	if len(args) == 0 {
		args = []string{"list"}
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list", "ls":
		runProjectList(rest)
	case "new", "create":
		runProjectNew(rest)
	case "show":
		runProjectShow(rest)
	case "enter", "use", "cd":
		runProjectEnter(rest)
	case "exit", "leave":
		runProjectExit(rest)
	case "rename", "mv":
		runProjectRename(rest)
	case "rm", "remove", "delete":
		runProjectRemove(rest)
	case "help", "-h", "--help":
		projectUsage(os.Stdout)
	default:
		p := palFor(os.Stderr)
		fmt.Fprintf(os.Stderr, "panda: %s %s\n", i18n.T(i18n.Detect(), "cli.unknownSub"), p.Command("project "+verb))
		projectUsage(os.Stderr)
		os.Exit(2)
	}
}

func projectUsage(w *os.File) {
	p := palFor(w)
	line := func(s string) { fmt.Fprintln(w, styleHelpLine(p, s)) }
	line("usage: panda project <verb>")
	line("  list                                    every project, most recent first")
	line("  new <name> [--dir PATH] [--desc S]      create a project (and its memory file)")
	line("  show [name]                             one project: work dir, tasks, memory size")
	line("  enter <name>                            make it the current project")
	line("  exit                                    leave the current project")
	line("  rename <old> <new>                      rename the project, its memory and its tasks")
	line("  rm <name> [--keep-memory]               remove the project (never its work dir)")
}

// projectStores opens the three things a project verb touches: its metadata row,
// its memory directory, and the task table its name appears in. Opening them in
// one place is what keeps a half-written project from being possible.
func projectStores(configPath string) (*projects.Store, *memory.Projects, *core.TaskStore, func()) {
	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, tasks, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	return projects.NewStore(db),
		memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, memoryLimits(cfg)),
		tasks,
		func() { _ = db.Close() }
}

func runProjectList(args []string) {
	fs := flag.NewFlagSet("project list", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	store, mem, _, closeDB := projectStores(*configPath)
	defer closeDB()

	list, err := store.List()
	if err != nil {
		fatal("list projects", err)
	}
	// Projects that only ever existed as a memory file still have to appear, or
	// the list would silently disagree with `panda memory list`.
	list = withAdoptedProjects(store, mem, list)

	active, _ := store.Active()
	loc := i18n.Detect()
	if jsonOutput {
		emitJSON(map[string]any{"active": active, "projects": list})
		return
	}
	if len(list) == 0 {
		fmt.Println(i18n.T(loc, "cli.project.none"))
		return
	}

	p := pal()
	nameW := cliui.DisplayWidth(i18n.T(loc, "cli.col.name"))
	dirW := cliui.DisplayWidth(i18n.T(loc, "cli.col.workdir"))
	for _, pr := range list {
		nameW = max(nameW, cliui.DisplayWidth(pr.Name))
		dirW = max(dirW, cliui.DisplayWidth(pr.WorkDir))
	}
	nameW = min(nameW, 26)
	dirW = min(dirW, max(20, listWidth()-nameW-32))
	const seenW = 12

	fmt.Println(listHeader(
		cell("", 2),
		cell(i18n.T(loc, "cli.col.name"), nameW),
		cell(i18n.T(loc, "cli.col.workdir"), dirW),
		cell(i18n.T(loc, "cli.col.seen"), seenW),
		i18n.T(loc, "cli.col.description"),
	))
	for _, pr := range list {
		// The current project is marked rather than only listed: "which one am I
		// in" is the question a list of projects is usually asked.
		mark := "  "
		tint := func(s string) string { return s }
		if pr.Name == active {
			mark = p.Accent(p.Glyph("▸", ">")) + " "
			tint = p.Accent
		}
		fmt.Println(mark + row(
			styledCell(pr.Name, nameW, tint),
			cell(orDash(cliui.TruncateTail(pr.WorkDir, dirW, p.Unicode())), dirW),
			cell(humanAge(loc, pr.UpdatedAt.Unix()), seenW),
			pr.Description,
		))
	}
	if active == "" {
		fmt.Println(p.Muted(i18n.T(loc, "cli.project.noActive")))
	}
}

// withAdoptedProjects folds in projects that exist only as a memory file. They
// predate the projects table, and a listing that showed the table alone would
// report that work the user can still see in projects/ does not exist.
func withAdoptedProjects(store *projects.Store, mem *memory.Projects, list []projects.Project) []projects.Project {
	names, err := mem.List()
	if err != nil {
		return list
	}
	known := make(map[string]bool, len(list))
	for _, p := range list {
		known[p.Name] = true
	}
	for _, n := range names {
		if known[n] {
			continue
		}
		if p, err := store.EnsureFromName(n); err == nil {
			list = append(list, p)
		}
	}
	return list
}

func runProjectNew(args []string) {
	fs := flag.NewFlagSet("project new", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	dir := fs.String("dir", "", "work directory for this project's tasks")
	desc := fs.String("desc", "", "one-line description")
	enter := fs.Bool("enter", true, "make the new project current")
	fs.Parse(reorderFlags(args, map[string]bool{"--config": true, "--dir": true, "--desc": true}))
	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		fmt.Fprintln(os.Stderr, "usage: panda project new <name> [--dir PATH] [--desc S] [--enter=false]")
		os.Exit(2)
	}
	if err := projects.ValidateName(name); err != nil {
		fatalMsg(err.Error())
	}
	store, mem, _, closeDB := projectStores(*configPath)
	defer closeDB()

	pr, err := store.Create(name, *dir, *desc)
	if err != nil {
		fatal("create project", err)
	}
	// The memory file is seeded here rather than lazily, so `panda memory get
	// project:<name>` works the moment the project exists.
	if err := mem.Save(name, memory.MemFile{Limit: mem.Limit()}); err != nil {
		fatal("create project memory", err)
	}
	if *enter {
		if err := store.SetActive(name); err != nil {
			fatal("enter project", err)
		}
	}
	loc := i18n.Detect()
	if jsonOutput {
		emitJSON(map[string]any{"project": pr, "active": *enter})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.project.created", "name", name))
	if pr.WorkDir != "" {
		fmt.Println(pal().Muted("  " + i18n.Tf(loc, "cli.project.workdir", "dir", pr.WorkDir)))
	}
	if *enter {
		fmt.Println(pal().Muted("  " + i18n.Tf(loc, "cli.project.entered", "name", name)))
	}
}

func runProjectShow(args []string) {
	fs := flag.NewFlagSet("project show", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
	store, mem, tasks, closeDB := projectStores(*configPath)
	defer closeDB()

	loc := i18n.Detect()
	name := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if name == "" {
		// No argument means "the one I am in" — the default every project-shaped
		// CLI uses, and the reason `enter` exists at all.
		active, err := store.Active()
		if err != nil {
			fatal("read active project", err)
		}
		if active == "" {
			fatalMsg(i18n.T(loc, "cli.project.noActive"))
		}
		name = active
	}
	pr, err := store.Get(name)
	if err != nil {
		fatal("get project", err)
	}
	active, _ := store.Active()

	var entries, chars int
	if m, err := mem.Load(name); err == nil {
		entries, chars = len(m.Entries), m.Chars()
	}
	if jsonOutput {
		emitJSON(map[string]any{
			"project": pr, "active": pr.Name == active,
			"memory": map[string]int{"entries": entries, "chars": chars},
		})
		return
	}

	p := pal()
	field := func(label, value string) {
		fmt.Println(p.Muted(cell(label+":", 14)) + value)
	}
	field(i18n.T(loc, "cli.col.name"), pr.Name)
	if pr.Name == active {
		field(i18n.T(loc, "cli.col.state"), p.Success(i18n.T(loc, "cli.project.isActive")))
	}
	field(i18n.T(loc, "cli.col.workdir"), orDash(pr.WorkDir))
	if pr.Description != "" {
		field(i18n.T(loc, "cli.col.description"), pr.Description)
	}
	field(i18n.T(loc, "cli.col.created"), ts(pr.CreatedAt.Unix()))
	field(i18n.T(loc, "cli.col.updated"), ts(pr.UpdatedAt.Unix()))
	field(i18n.T(loc, "cli.project.memory"),
		i18n.Tf(loc, "cli.project.memorySize", "entries", fmt.Sprint(entries), "chars", fmt.Sprint(chars)))
	printProjectTasks(loc, tasks, pr.Name)
	printProjectSessions(loc, *configPath, pr.Name)
}

// printProjectTasks lists the project's tasks under its record. A project without
// its tasks is a name and a path; the tasks are what makes `show` answer "what is
// happening here".
func printProjectTasks(loc i18n.Locale, store *core.TaskStore, name string) {
	all, err := store.ListByState(context.Background(), "")
	if err != nil {
		return
	}
	var mine []core.Task
	for _, t := range all {
		if t.Project == name {
			mine = append(mine, t)
		}
	}
	if len(mine) == 0 {
		fmt.Println(pal().Muted(i18n.T(loc, "cli.project.noTasks")))
		return
	}
	fmt.Println()
	printTaskTable(loc, mine)
}

// printProjectSessions lists the project's sessions under its record.
func printProjectSessions(loc i18n.Locale, configPath, name string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return
	}
	store := sessions.NewStore(sessionStoreRoot(cfg))
	list, err := store.ListByProject(name)
	if err != nil || len(list) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(pal().Heading(i18n.Tf(loc, "cli.project.sessionsHead", "n", fmt.Sprint(len(list)))))
	for _, s := range list {
		title := orDash(s.Title)
		if len([]rune(title)) > 40 {
			title = string([]rune(title)[:40]) + "…"
		}
		fmt.Printf("  %-16s  %s  %s\n", s.ID, s.UpdatedAt.Format("2006-01-02 15:04"), title)
	}
}

func runProjectEnter(args []string) {
	fs := flag.NewFlagSet("project enter", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
	name := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if name == "" {
		fmt.Fprintln(os.Stderr, "usage: panda project enter <name>")
		os.Exit(2)
	}
	store, mem, _, closeDB := projectStores(*configPath)
	defer closeDB()

	// A project that exists only as a memory file is adopted on entry rather than
	// refused: the user can see it in projects/, so being unable to enter it would
	// read as a bug rather than as a policy.
	if _, err := store.Get(name); err != nil {
		if names, lerr := mem.List(); lerr == nil {
			for _, n := range names {
				if n == name {
					if _, cerr := store.EnsureFromName(name); cerr != nil {
						fatal("adopt project", cerr)
					}
				}
			}
		}
	}
	if err := store.SetActive(name); err != nil {
		fatal("enter project", err)
	}
	loc := i18n.Detect()
	pr, _ := store.Get(name)
	if jsonOutput {
		emitJSON(map[string]any{"active": name, "project": pr})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.project.entered", "name", name))
	if pr.WorkDir != "" {
		fmt.Println(pal().Muted("  " + i18n.Tf(loc, "cli.project.workdir", "dir", pr.WorkDir)))
	}
}

func runProjectExit(args []string) {
	fs := flag.NewFlagSet("project exit", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	store, _, _, closeDB := projectStores(*configPath)
	defer closeDB()

	was, _ := store.Active()
	if err := store.ClearActive(); err != nil {
		fatal("exit project", err)
	}
	loc := i18n.Detect()
	if jsonOutput {
		emitJSON(map[string]string{"left": was})
		return
	}
	if was == "" {
		fmt.Println(i18n.T(loc, "cli.project.noActive"))
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.project.left", "name", was))
}

func runProjectRename(args []string) {
	fs := flag.NewFlagSet("project rename", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: panda project rename <old> <new>")
		os.Exit(2)
	}
	oldName, newName := fs.Arg(0), fs.Arg(1)
	if err := projects.ValidateName(newName); err != nil {
		fatalMsg(err.Error())
	}
	store, mem, tasks, closeDB := projectStores(*configPath)
	defer closeDB()

	pr, err := store.Rename(oldName, newName)
	if err != nil {
		fatal("rename project", err)
	}
	// The memory file carries the name too. Copy-then-drop rather than move, so a
	// failure leaves the old file intact instead of losing the memory entirely.
	if m, lerr := mem.Load(oldName); lerr == nil {
		if serr := mem.Save(newName, m); serr != nil {
			fatal("move project memory", serr)
		}
		if derr := mem.Delete(oldName); derr != nil {
			fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(i18n.Detect(), "cli.project.memoryLeft", "name", oldName))
		}
	}
	renamed, rerr := tasks.RenameProject(context.Background(), oldName, newName)
	if rerr != nil {
		fatal("rename project tasks", rerr)
	}
	var sessRenamed int
	if cfg, cerr := config.Load(*configPath); cerr == nil {
		sessStore := sessions.NewStore(sessionStoreRoot(cfg))
		sessRenamed, _ = sessStore.RenameProject(oldName, newName)
	}
	renameConvo(oldName, newName)

	loc := i18n.Detect()
	if jsonOutput {
		emitJSON(map[string]any{"project": pr, "tasks_updated": renamed, "sessions_updated": sessRenamed})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.project.renamed", "old", oldName, "new", newName))
	if renamed > 0 {
		fmt.Println(pal().Muted("  " + i18n.Tf(loc, "cli.project.tasksMoved", "n", fmt.Sprint(renamed))))
	}
	if sessRenamed > 0 {
		fmt.Println(pal().Muted("  " + i18n.Tf(loc, "cli.project.sessionsMoved", "n", fmt.Sprint(sessRenamed))))
	}
}

func runProjectRemove(args []string) {
	fs := flag.NewFlagSet("project rm", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	keepMemory := fs.Bool("keep-memory", false, "leave the project's memory file in place")
	deleteSessions := fs.Bool("delete-sessions", false, "delete all sessions belonging to this project")
	fs.Parse(reorderFlags(args, commonValueFlags))
	name := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if name == "" {
		fmt.Fprintln(os.Stderr, "usage: panda project rm <name> [--keep-memory] [--delete-sessions]")
		os.Exit(2)
	}
	store, mem, _, closeDB := projectStores(*configPath)
	defer closeDB()

	pr, err := store.Get(name)
	if err != nil {
		fatal("get project", err)
	}
	if err := store.Delete(name); err != nil {
		fatal("remove project", err)
	}
	if !*keepMemory {
		if derr := mem.Delete(name); derr != nil {
			fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(i18n.Detect(), "cli.project.memoryLeft", "name", name))
		}
	}

	cfg, cfgErr := config.Load(*configPath)
	var sessionsKept, sessionsDeleted int
	if cfgErr == nil {
		sessStore := sessions.NewStore(sessionStoreRoot(cfg))
		if list, err := sessStore.ListByProject(name); err == nil && len(list) > 0 {
			if *deleteSessions {
				sessionsDeleted = len(list)
				wt := openWorktreesBestEffort(cfg.Storage.WorkPath)
				for _, s := range list {
					if wt != nil {
						_ = wt.Remove(context.Background(), s.ID)
					}
					_ = sessStore.Delete(s.ID)
				}
			} else {
				sessionsKept = len(list)
				for _, s := range list {
					_ = sessStore.SetProject(s.ID, "")
				}
			}
		}
	}

	deleteConvo(name)

	loc := i18n.Detect()
	if jsonOutput {
		emitJSON(map[string]any{
			"removed":          name,
			"work_dir":         pr.WorkDir,
			"memory_kept":      *keepMemory,
			"sessions_kept":    sessionsKept,
			"sessions_deleted": sessionsDeleted,
		})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.project.removed", "name", name))
	// Say what was left behind. The work dir is the user's own tree and this
	// command never touches it, which is worth stating rather than implying.
	if pr.WorkDir != "" {
		fmt.Println(pal().Muted("  " + i18n.Tf(loc, "cli.project.workdirKept", "dir", pr.WorkDir)))
	}
	if *keepMemory {
		fmt.Println(pal().Muted("  " + i18n.Tf(loc, "cli.project.memoryKept", "name", name)))
	}
	if sessionsDeleted > 0 {
		fmt.Println(pal().Muted("  " + i18n.Tf(loc, "cli.project.sessionsDeleted", "n", fmt.Sprint(sessionsDeleted))))
	}
	if sessionsKept > 0 {
		fmt.Println(pal().Muted("  " + i18n.Tf(loc, "cli.project.sessionsKept", "n", fmt.Sprint(sessionsKept))))
	}
}

// activeProject reports the project this machine is currently in, and its work
// dir. Every entry point that submits work calls it, so "which project am I in"
// is answered in one place; a missing table (a database from before the projects
// migration) reads as "no project" rather than as a failure.
func activeProject(cfg *config.Config) (name, workDir string) {
	db, _, err := panelStore(cfg)
	if err != nil {
		return "", ""
	}
	defer db.Close()
	store := projects.NewStore(db)
	name, err = store.Active()
	if err != nil || name == "" {
		return "", ""
	}
	pr, err := store.Get(name)
	if err != nil {
		return name, ""
	}
	return pr.Name, pr.WorkDir
}

// bindAskProject gives an engine its ambient project: the explicit --project when
// one was named, otherwise whatever `panda project enter` last pointed at. A named
// project that does not exist ends the command — the flag exists to put work
// somewhere findable, and silently running outside it would defeat that.
func bindAskProject(engine *askengine.Engine, cfg *config.Config, named string) {
	if engine == nil {
		return
	}
	named = strings.TrimSpace(named)
	if named == "" {
		name, dir := activeProject(cfg)
		engine.SetProject(name, dir)
		return
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()
	pr, err := projects.NewStore(db).Get(named)
	if err != nil {
		fatal("get project", err)
	}
	engine.SetProject(pr.Name, pr.WorkDir)
}
