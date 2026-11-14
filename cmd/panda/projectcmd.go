// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"path/filepath"
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
	case "approval":
		runProjectApproval(rest)
	case "help", "-h", "--help":
		projectUsage(os.Stdout)
	default:
		p := palFor(os.Stderr)
		failUsagef(2, func() { projectUsage(os.Stderr) }, "panda: %s %s\n", i18n.T(i18n.Detect(), "cli.unknownSub"), p.Command("project "+verb))
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
	line("  approval <name> [mode … | scope … | clear]  show or set the project's approval policy")
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	rejectExtraArgs(fs, "usage: panda project list")
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
		// A refused work dir is shown tinted rather than as an ordinary path:
		// the row is the reason tasks under this project ignore the tree, and
		// the list is where an operator notices it.
		dirCell := func() string {
			if pr.WorkDir == "" {
				return cell(orDash(""), dirW)
			}
			if pr.UsableWorkDir() == "" {
				// The marker leads, so tail-truncation of a long path cannot eat
				// it: the row's point is that this tree is not used, and a mark
				// that scrolls off with the path would leave a plausible path.
				return cell(p.Warn("! "+cliui.TruncateTail(pr.WorkDir, dirW-2, p.Unicode())), dirW)
			}
			return cell(cliui.TruncateTail(pr.WorkDir, dirW, p.Unicode()), dirW)
		}()
		fmt.Println(mark + row(
			styledCell(pr.Name, nameW, tint),
			dirCell,
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	dir := fs.String("dir", "", "work directory for this project's tasks")
	desc := fs.String("desc", "", "one-line description")
	enter := fs.Bool("enter", true, "make the new project current")
	fs.Parse(reorderFlags(fs, args))
	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		fail(2, "usage: panda project new <name> [--dir PATH] [--desc S] [--enter=false]")
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
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
	// A work dir the breadth rule refuses still belongs on screen — with the
	// reason. Printing it bare sends the user to a tree their tasks never use;
	// printing "—" claims the project has none, which is a different fact.
	if wd := pr.UsableWorkDir(); wd != "" {
		field(i18n.T(loc, "cli.col.workdir"), wd)
	} else if pr.WorkDir != "" {
		field(i18n.T(loc, "cli.col.workdir"),
			p.Warn(i18n.Tf(loc, "cli.project.workdirTooBroad", "dir", pr.WorkDir)))
	} else {
		field(i18n.T(loc, "cli.col.workdir"), orDash(pr.WorkDir))
	}
	if pr.Description != "" {
		field(i18n.T(loc, "cli.col.description"), pr.Description)
	}
	field(i18n.T(loc, "cli.col.created"), ts(pr.CreatedAt.Unix()))
	field(i18n.T(loc, "cli.col.updated"), ts(pr.UpdatedAt.Unix()))
	approval := pr.ApprovalMode
	if approval == "" {
		approval = "inherit"
	}
	approval += " · scope " + pr.NormalizedScope()
	if pr.ApprovalDecision != "" {
		approval += " · remembered " + pr.ApprovalDecision
	}
	field(i18n.T(loc, "cli.project.approval"), approval)
	field(i18n.T(loc, "cli.project.memory"),
		i18n.Tf(loc, "cli.project.memorySize", "entries", fmt.Sprint(entries), "chars", fmt.Sprint(chars)))
	printProjectTasks(loc, tasks, pr.Name)
	printProjectSessions(loc, *configPath, pr.Name)
}

// printProjectTasks lists the project's tasks under its record. A project without
// its tasks is a name and a path; the tasks are what makes `show` answer "what is
// happening here".
func printProjectTasks(loc i18n.Locale, store *core.TaskStore, name string) {
	mine, err := store.ListByProject(context.Background(), name)
	if err != nil {
		return
	}
	if len(mine) == 0 {
		fmt.Println(pal().Muted(i18n.T(loc, "cli.project.noTasks")))
		return
	}
	fmt.Println()
	printTaskTable(loc, mine, taskRefsFor(context.Background(), store, mine), store)
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

// runProjectApproval manages a project's tier-2 approval policy: the mode
// override, the default remember scope, and the remembered decision. It is
// the CLI half of "approval logic set per project" — the session half lives
// inside each engine process and clears from inside a session (/approval
// clear, or the console's session scope).
func runProjectApproval(args []string) {
	fs := flag.NewFlagSet("project approval", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	rest := fs.Args()
	loc := i18n.Detect()
	usage := func() {
		fail(2, "usage: panda project approval <name> [mode inherit|never|on-request|always | scope once|session|project | clear]")
	}
	if len(rest) == 0 {
		usage()
	}
	name := rest[0]
	store, _, _, closeDB := projectStores(*configPath)
	defer closeDB()
	pr, err := store.Get(name)
	if err != nil {
		fatal("get project", err)
	}
	if len(rest) == 1 {
		mode := pr.ApprovalMode
		if mode == "" {
			mode = "inherit"
		}
		fmt.Printf("mode:     %s\nscope:    %s\ndecision: %s\n", mode, pr.NormalizedScope(), orDash(pr.ApprovalDecision))
		return
	}
	switch rest[1] {
	case "mode":
		if len(rest) < 3 {
			usage()
		}
		mode := rest[2]
		if mode == "inherit" {
			mode = ""
		}
		if err := projects.ValidateApprovalMode(mode); err != nil {
			fatal("approval mode", err)
		}
		if err := store.SetApprovalPolicy(name, mode, pr.ApprovalScope); err != nil {
			fatal("set approval mode", err)
		}
	case "scope":
		if len(rest) < 3 {
			usage()
		}
		if err := projects.ValidateApprovalScope(rest[2]); err != nil {
			fatal("approval scope", err)
		}
		if err := store.SetApprovalPolicy(name, pr.ApprovalMode, rest[2]); err != nil {
			fatal("set approval scope", err)
		}
	case "clear":
		if err := store.SetApprovalDecision(name, ""); err != nil {
			fatal("clear approval", err)
		}
	default:
		usage()
	}
	fmt.Println(i18n.T(loc, "repl.approval.saved"))
}

func runProjectEnter(args []string) {
	fs := flag.NewFlagSet("project enter", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	name := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if name == "" {
		fail(2, "usage: panda project enter <name>")
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	rejectExtraArgs(fs, "usage: panda project exit")
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	if fs.NArg() != 2 {
		fail(2, "usage: panda project rename <old> <new>")
	}
	oldName, newName := fs.Arg(0), fs.Arg(1)
	if err := projects.ValidateName(newName); err != nil {
		fatalMsg(err.Error())
	}
	store, mem, tasks, closeDB := projectStores(*configPath)
	defer closeDB()

	var pr projects.Project
	err := mem.RenameWith(oldName, newName, func() error {
		var err error
		pr, err = store.Rename(oldName, newName)
		return err
	})
	if err != nil {
		fatal("rename project", err)
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	keepMemory := fs.Bool("keep-memory", false, "leave the project's memory file in place")
	deleteSessions := fs.Bool("delete-sessions", false, "delete all sessions belonging to this project")
	fs.Parse(reorderFlags(fs, args))
	name := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if name == "" {
		fail(2, "usage: panda project rm <name> [--keep-memory] [--delete-sessions]")
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
				for _, s := range list {
					if wt := openWorktreesForSession(cfg, s); wt != nil {
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

// projectWorkDir returns the project's usable work directory, or "" when the
// project is unknown or its directory no longer validates. Session worktrees
// use this as their base: a thread attached to a project belongs in that
// project's repository, not in the node's storage.work_path.
func projectWorkDir(cfg *config.Config, name string) string {
	if name == "" {
		return ""
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		return ""
	}
	defer db.Close()
	p, err := projects.NewStore(db).Get(name)
	if err != nil {
		return ""
	}
	return p.UsableWorkDir()
}

// activeProject reports the project this machine is currently in, and its work
// dir. Every entry point that submits work calls it, so "which project am I in"
// is answered in one place; a missing table (a database from before the projects
// migration) reads as "no project" rather than as a failure.
func activeProject(cfg *config.Config) (name, workDir string) {
	cwd, _ := os.Getwd()
	db, _, err := panelStore(cfg)
	if err != nil {
		return "", cwd
	}
	defer db.Close()
	store := projects.NewStore(db)

	// Prefer the project owning the current working directory — exact match
	// or the deepest ancestor workdir, so running from a subdirectory of a
	// project lands in that project's space instead of the global one.
	if name, dir := projectForDir(store, cwd); name != "" {
		return name, dir
	}

	name, err = store.Active()
	if err != nil || name == "" {
		return "", cwd
	}
	pr, err := store.Get(name)
	if err != nil {
		return name, cwd
	}
	// A row whose tree the breadth rule refuses contributes no work dir: the
	// project stays bound (its approval policy and memory still apply) but the
	// task falls back to the node default, exactly like a project with no tree
	// of its own. Without this the active-project branch handed every task the
	// refused path and createTask failed them all — the regression this rule
	// caused on any machine that had adopted $HOME as a project before it
	// existed.
	usable := pr.UsableWorkDir()
	// If the ambient project's directory is completely different from current working directory,
	// do NOT hijack the user's cwd unless cwd is within pr.WorkDir.
	if cwd != "" && usable != "" {
		cleanCwd := filepath.Clean(cwd)
		cleanPrDir := filepath.Clean(usable)
		if cleanCwd != cleanPrDir && !strings.HasPrefix(cleanCwd, cleanPrDir+string(filepath.Separator)) {
			return "", cwd
		}
	}
	return pr.Name, usable
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
		// "在哪里启动哪里就是项目空间": an unnamed ask from a directory that
		// looks like a workspace adopts it as its project, so the harness
		// runs inside the project tree instead of the bare cwd — even when
		// the directory was never `panda project enter`ed.
		name, dir := ambientProject(cfg)
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
	engine.SetProject(pr.Name, pr.UsableWorkDir())
}
