package main

// `panda session` — the kernel-form of the web console's conversation model:
// one chat thread per session, each backed by a git worktree when the work
// path is a repository (the codex/claude-code working model). Semantics mirror
// webui/panel/sessions.go so CLI and web never disagree.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// sessionStoreRoot is where session JSON files live: alongside the SQLite
// data dir (same layout web.go uses).
func sessionStoreRoot(cfg *config.Config) string {
	return filepath.Join(filepath.Dir(cfg.Storage.DBPath), "sessions")
}

// runSession dispatches `panda session <verb>`: list | new | show | rm |
// ask | diff | merge.
func runSession(args []string) {
	if len(args) == 0 {
		sessionUsage()
		os.Exit(2)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list", "ls":
		runSessionList(rest)
	case "new":
		runSessionNew(rest)
	case "show":
		runSessionShow(rest)
	case "mv", "move":
		runSessionMove(rest)
	case "rm", "delete":
		runSessionRm(rest)
	case "ask":
		runSessionAsk(rest)
	case "diff":
		runSessionDiff(rest)
	case "merge":
		runSessionMerge(rest)
	case "help", "-h", "--help":
		sessionUsage()
	default:
		fmt.Fprintf(os.Stderr, "panda: unknown session verb %q\n", verb)
		sessionUsage()
		os.Exit(2)
	}
}

func sessionUsage() {
	fmt.Fprintln(os.Stderr, "usage: panda session <verb>")
	fmt.Fprintln(os.Stderr, "  list [--project P]            list sessions, newest first")
	fmt.Fprintln(os.Stderr, "  new [--title T] [--project P] create a session (carves a worktree in a repo)")
	fmt.Fprintln(os.Stderr, "  show <id>                     show one session and its turns")
	fmt.Fprintln(os.Stderr, "  mv <id> --project P           move session to project (empty to disassociate)")
	fmt.Fprintln(os.Stderr, "  rm <id>                       remove the session and its worktree")
	fmt.Fprintln(os.Stderr, "  ask <id> <prompt> [--authorize] [--card PATH]   continue a session")
	fmt.Fprintln(os.Stderr, "  diff <id>                     show the session's worktree changes")
	fmt.Fprintln(os.Stderr, "  merge <id> [--message M]      merge the session branch into HEAD")
}

func runSessionList(args []string) {
	fs := flag.NewFlagSet("session list", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	projectName := fs.String("project", "", "filter sessions by project")
	fs.Parse(args)
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	store := sessions.NewStore(sessionStoreRoot(cfg))
	var list []*sessions.Session
	if *projectName != "" {
		list, err = store.ListByProject(*projectName)
	} else {
		list, err = store.List()
	}
	if err != nil {
		fatal("list sessions", err)
	}
	loc := i18n.Detect()
	if jsonOutput {
		if list == nil {
			list = []*sessions.Session{}
		}
		emitJSON(list)
		return
	}
	if len(list) == 0 {
		fmt.Println(i18n.T(loc, "cli.session.none"))
		return
	}
	// Same column discipline as the task board: sized to the data, clipped
	// rather than wrapped, and a dim header so the columns name themselves.
	idW := cliui.DisplayWidth(i18n.T(loc, "cli.col.id"))
	branchW := cliui.DisplayWidth(i18n.T(loc, "cli.col.branch"))
	projW := cliui.DisplayWidth(i18n.T(loc, "cli.col.project"))
	for _, s := range list {
		idW = max(idW, cliui.DisplayWidth(s.ID))
		branchW = max(branchW, cliui.DisplayWidth(orDash(s.Branch)))
		projW = max(projW, cliui.DisplayWidth(orDash(s.Project)))
	}
	idW, branchW = min(idW, 18), min(branchW, 22)
	projW = min(projW, 16)
	const whenW, turnsW = 16, 6
	titleW := max(20, listWidth()-(idW+whenW+branchW+projW+turnsW+5))

	fmt.Println(listHeader(
		cell(i18n.T(loc, "cli.col.id"), idW),
		cell(i18n.T(loc, "cli.col.when"), whenW),
		cell(i18n.T(loc, "cli.col.project"), projW),
		cell(i18n.T(loc, "cli.col.branch"), branchW),
		cell(i18n.T(loc, "cli.col.turns"), turnsW),
		i18n.T(loc, "cli.col.title"),
	))
	for _, s := range list {
		fmt.Println(row(
			cell(s.ID, idW),
			cell(s.UpdatedAt.Format("2006-01-02 15:04"), whenW),
			cell(orDash(s.Project), projW),
			cell(orDash(s.Branch), branchW),
			cell(strconv.Itoa(len(s.Turns)), turnsW),
			cell(s.Title, titleW),
		))
	}
}

func runSessionNew(args []string) {
	fs := flag.NewFlagSet("session new", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	title := fs.String("title", "", "session title (default: derived from the first ask)")
	projectName := fs.String("project", "", "project name (default: active project)")
	fs.Parse(args)
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	proj := strings.TrimSpace(*projectName)
	if proj == "" {
		proj, _ = activeProject(cfg)
	}
	store := sessions.NewStore(sessionStoreRoot(cfg))
	sess, err := store.Create(strings.TrimSpace(*title), proj)
	if err != nil {
		fatal("create session", err)
	}
	wt := openWorktreesBestEffort(cfg.Storage.WorkPath)
	if wt != nil {
		if path, err := wt.Ensure(context.Background(), sess.ID); err == nil {
			_ = store.SetWorktree(sess.ID, path, sessions.Branch(sess.ID))
			sess, _ = store.Get(sess.ID)
		}
	}
	if jsonOutput {
		emitJSON(sess)
		return
	}
	fmt.Printf("%s  %s\n", sess.ID, i18n.T(i18n.Detect(), "cli.session.created"))
	if sess.Project != "" {
		fmt.Printf("project:  %s\n", sess.Project)
	}
	if sess.Worktree != "" {
		fmt.Printf("worktree: %s  branch: %s\n", sess.Worktree, sess.Branch)
	}
}

func runSessionShow(args []string) {
	fs := flag.NewFlagSet("session show", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	id := strings.TrimSpace(fs.Arg(0))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda session show <id>")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	sess, err := sessions.NewStore(sessionStoreRoot(cfg)).Get(id)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "panda: no such session: %s\n", id)
			os.Exit(1)
		}
		fatal("load session", err)
	}
	if jsonOutput {
		emitJSON(sess)
		return
	}
	fmt.Printf("id:       %s\n", sess.ID)
	fmt.Printf("title:    %s\n", orDash(sess.Title))
	if sess.Project != "" {
		fmt.Printf("project:  %s\n", sess.Project)
	}
	fmt.Printf("created:  %s\n", sess.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("updated:  %s\n", sess.UpdatedAt.Format("2006-01-02 15:04:05"))
	if sess.Branch != "" {
		fmt.Printf("branch:   %s\n", sess.Branch)
	}
	if sess.Worktree != "" {
		fmt.Printf("worktree: %s\n", sess.Worktree)
	}
	if len(sess.Turns) == 0 {
		return
	}
	fmt.Println("turns:")
	for i, t := range sess.Turns {
		text := strings.ReplaceAll(t.Text, "\n", " ")
		if len([]rune(text)) > 120 {
			text = string([]rune(text)[:120]) + "…"
		}
		ref := ""
		if t.Ref != "" {
			ref = "  [" + t.Ref + "]"
		}
		fmt.Printf("  %2d %-9s %s%s\n", i+1, t.Role, text, ref)
	}
}

func runSessionMove(args []string) {
	fs := flag.NewFlagSet("session mv", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	projectName := fs.String("project", "", "target project name (empty to disassociate)")
	fs.Parse(args)
	id := strings.TrimSpace(fs.Arg(0))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda session mv <id> --project <name>")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	store := sessions.NewStore(sessionStoreRoot(cfg))
	targetProj := strings.TrimSpace(*projectName)
	if err := store.SetProject(id, targetProj); err != nil {
		fatal("move session", err)
	}
	sess, _ := store.Get(id)
	if jsonOutput {
		emitJSON(sess)
		return
	}
	loc := i18n.Detect()
	if sess != nil && sess.Project != "" {
		fmt.Println(i18n.Tf(loc, "cli.session.moved", "id", id, "project", sess.Project))
	} else {
		fmt.Println(i18n.Tf(loc, "cli.session.unassociated", "id", id))
	}
}

func runSessionRm(args []string) {
	fs := flag.NewFlagSet("session rm", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	id := strings.TrimSpace(fs.Arg(0))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda session rm <id>")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	if wt := openWorktreesBestEffort(cfg.Storage.WorkPath); wt != nil {
		_ = wt.Remove(context.Background(), id)
	}
	if err := sessions.NewStore(sessionStoreRoot(cfg)).Delete(id); err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "panda: no such session: %s\n", id)
			os.Exit(1)
		}
		fatal("delete session", err)
	}
	fmt.Printf("%s deleted\n", id)
}

// runSessionAsk continues a session from the terminal: same flow as the web
// console's POST /api/sessions/{id}/ask (persist the user turn, run with the
// full history in the session's worktree, bind a spawned task back to the
// session, store the assistant turn) — but the stream goes straight to the
// terminal instead of SSE.
func runSessionAsk(args []string) {
	fs := flag.NewFlagSet("session ask", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	cardPath := fs.String("card", defaultCardPath(), "path to capabilities.yaml (default: discovered; required to execute tasks)")
	mcpCmd := fs.String("mcp", "", "MCP server command (space-separated)")
	authorize := fs.Bool("authorize", false, "authorize tier-2 (irreversible) commands")
	fs.Parse(args)
	id := strings.TrimSpace(fs.Arg(0))
	prompt := strings.TrimSpace(strings.Join(fs.Args()[1:], " "))
	if id == "" || prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: panda session ask <id> <prompt> [--authorize] [--card PATH]")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	loc := i18n.Detect()
	engine, err := askengine.New(context.Background(), cfg, askengine.Options{
		CardPath:   *cardPath,
		MCPCommand: *mcpCmd,
		// An interactive session over a chat thread: peers dial in the
		// background rather than gating the first prompt (same as the REPL).
		AsyncPeers: true,
	})
	if err != nil {
		fatal("ask engine", err)
	}
	defer engine.Close()

	store := sessions.NewStore(sessionStoreRoot(cfg))
	sess, err := store.Get(id)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "panda: no such session: %s\n", id)
			os.Exit(1)
		}
		fatal("load session", err)
	}

	// History is the thread as it stands; the fresh turn is persisted after
	// building it because AskTurns carries the prompt itself — replaying the
	// persisted copy too would send two consecutive user messages (400).
	var history []entry.Turn
	for _, t := range sess.Turns {
		history = append(history, entry.Turn{Role: t.Role, Content: t.Text})
	}
	if _, err := store.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: prompt}); err != nil {
		fatal("save turn", err)
	}

	// Repo sessions run in their worktree, non-repo ones in the project work dir
	// or shared work path (memory wall §17.2: personal memory never enters a session prompt).
	workDir := sess.Worktree
	if workDir == "" && sess.Project != "" {
		pStore, _, _, closeDB := projectStores(*configPath)
		if p, err := pStore.Get(sess.Project); err == nil && p.WorkDir != "" {
			workDir = p.WorkDir
		}
		closeDB()
	}
	if workDir == "" {
		workDir = engine.WorkPath()
	}
	if sess.Project != "" {
		engine.SetProject(sess.Project, workDir)
	}

	out, err := askSessionTurns(engine, history, prompt, workDir, *authorize)
	if err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
		_, _ = store.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "⚠ " + err.Error(), Kind: "error"})
		os.Exit(1)
	}

	if sess.Title == sess.ID || sess.Title == "" {
		_ = store.SetTitle(sess.ID, prompt)
	}

	if out.Kind == "task" && out.TaskID != "" {
		db, store2, derr := panelStore(cfg)
		if derr == nil {
			_ = store2.SetSessionID(context.Background(), out.TaskID, sess.ID)
			db.Close()
		}
	}

	turn := sessions.Turn{Role: "assistant", Kind: out.Kind}
	if out.Kind == "task" {
		turn.Text = out.TaskID
		turn.Ref = out.TaskID
	} else if out.Kind == "plan" {
		turn.Text = out.PlanID
		turn.Ref = out.PlanID
	} else {
		turn.Text = out.Answer
	}
	_, _ = store.AppendTurn(sess.ID, turn)

	switch out.Kind {
	case "task":
		fmt.Println(i18n.Tf(loc, "cli.session.task", "id", out.TaskID, "state", out.TaskState))
		if out.OK {
			fmt.Print(renderCliMd(out.Stdout))
			if s := strings.TrimRight(out.Stdout, "\n"); s != "" && !strings.HasSuffix(out.Stdout, "\n") {
				fmt.Println()
			}
		} else {
			fmt.Fprintf(os.Stderr, "exit %d: %s\n", out.ExitCode, out.Stderr)
			os.Exit(1)
		}
	case "plan":
		printAskPlan(loc, out)
	}
}

// askSessionTurns runs one full-history ask with live streaming on an
// interactive terminal (same UX as askStreaming, but session-aware).
func askSessionTurns(engine *askengine.Engine, history []entry.Turn, prompt, workDir string, authorize bool) (*askengine.Result, error) {
	if !stdoutIsTTY() {
		return engine.AskTurns(context.Background(), history, prompt, workDir, authorize, askengine.StreamCallbacks{})
	}
	lr := newStreamLineRenderer()
	cb := askengine.StreamCallbacks{
		OnDelta: func(chunk string) { lr.delta(chunk) },
		OnProgress: func(p askengine.Progress) {
			if !lr.printed {
				fmt.Printf("%s %s\n", pal().MarkBullet(), progressNote(i18n.Detect(), p))
			}
		},
	}
	out, err := engine.AskTurns(context.Background(), history, prompt, workDir, authorize, cb)
	lr.flush()
	return out, err
}

func runSessionDiff(args []string) {
	fs := flag.NewFlagSet("session diff", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	id := strings.TrimSpace(fs.Arg(0))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda session diff <id>")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	wt := openWorktreesBestEffort(cfg.Storage.WorkPath)
	if wt == nil {
		fatal("session diff", errors.New("work path is not a git repository"))
	}
	changes, err := wt.Status(context.Background(), id)
	if err != nil {
		fatal("session status", err)
	}
	patch, err := wt.Diff(context.Background(), id)
	if err != nil {
		fatal("session diff", err)
	}
	if jsonOutput {
		emitJSON(map[string]any{
			"id":      id,
			"branch":  sessions.Branch(id),
			"changes": changes,
			"patch":   patch,
		})
		return
	}
	fmt.Printf("branch: %s\n", sessions.Branch(id))
	if len(changes) == 0 {
		fmt.Println(i18n.T(i18n.Detect(), "cli.session.diff.clean"))
		return
	}
	for _, c := range changes {
		fmt.Printf("  %-2s %s\n", c.Status, c.Path)
	}
	if patch != "" {
		fmt.Println()
		fmt.Print(patch)
	}
}

func runSessionMerge(args []string) {
	fs := flag.NewFlagSet("session merge", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	message := fs.String("message", "", "merge commit message (default: generated)")
	fs.Parse(args)
	id := strings.TrimSpace(fs.Arg(0))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda session merge <id> [--message M]")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	wt := openWorktreesBestEffort(cfg.Storage.WorkPath)
	if wt == nil {
		fatal("session merge", errors.New("work path is not a git repository"))
	}
	subject, err := wt.Merge(context.Background(), id, *message)
	if err != nil {
		if errors.Is(err, sessions.ErrMergeConflict) {
			fmt.Fprintf(os.Stderr, "panda: %v\n", err)
			os.Exit(1)
		}
		fatal("session merge", err)
	}
	fmt.Printf("merged %s: %s\n", sessions.Branch(id), subject)
}
