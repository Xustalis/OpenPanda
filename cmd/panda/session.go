// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"time"

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
	case "fork":
		runSessionFork(rest)
	case "tree":
		runSessionTree(rest)
	case "ask":
		runSessionAsk(rest)
	case "diff":
		runSessionDiff(rest)
	case "merge":
		runSessionMerge(rest)
	case "help", "-h", "--help":
		sessionUsage()
	default:
		fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(i18n.Detect(), "cli.unknownNamed", "kind", "session verb", "name", verb))
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
	fmt.Fprintln(os.Stderr, "  rm <id> [id …]                remove session(s) and their worktrees (ids may be unique prefixes)")
	fmt.Fprintln(os.Stderr, "  rm --project P|--older-than D|--all [--yes]   batch-remove by filter (asks first)")
	fmt.Fprintln(os.Stderr, "  fork <id> [--at N]            fork the session at turn N into a new thread")
	fmt.Fprintln(os.Stderr, "  tree [id]                     show the conversation tree (or one session's family)")
	fmt.Fprintln(os.Stderr, "  ask <id> <prompt> [--authorize] [--card PATH]   continue a session")
	fmt.Fprintln(os.Stderr, "  diff <id>                     show the session's worktree changes")
	fmt.Fprintln(os.Stderr, "  merge <id> [--message M]      merge the session branch into HEAD")
}

func runSessionList(args []string) {
	fs := flag.NewFlagSet("session list", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
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
		title := s.Title
		if s.ParentID != "" {
			title = "↳ " + title // forked thread — `session tree` shows the family
		}
		fmt.Println(row(
			cell(s.ID, idW),
			cell(s.UpdatedAt.Format("2006-01-02 15:04"), whenW),
			cell(orDash(s.Project), projW),
			cell(orDash(s.Branch), branchW),
			cell(strconv.Itoa(len(s.Turns)), turnsW),
			cell(title, titleW),
		))
	}
}

func runSessionNew(args []string) {
	fs := flag.NewFlagSet("session new", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
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
			fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(i18n.Detect(), "cli.noSuch.session", "id", id))
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
	if sess.ParentID != "" {
		fmt.Printf("forked:   %s @ turn %d\n", sess.ParentID, sess.ForkIndex)
	}
	if sess.Summary != "" {
		fmt.Printf("summary:  %d chars (compacted)\n", len(sess.Summary))
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	projectName := fs.String("project", "", "target project name (empty to disassociate)")
	fs.Parse(reorderFlags(args, map[string]bool{"config": true, "project": true}))
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

// runSessionRm removes sessions. Two forms:
//
//	panda session rm <id> [id …]                              explicit ids — like rm(1), no prompt
//	panda session rm --project P | --older-than D | --all [--yes]
//	                                                          filtered — confirms first
//
// Ids accept unique prefixes (same rule as task refs): the listing column
// shows the full 16-char id, but typing the first few is enough when it names
// one session. Filtered deletion without --yes on a non-TTY exits 2 — the
// same rule `queue clear` follows, because a script that cannot answer the
// prompt must say so rather than wipe silently.
func runSessionRm(args []string) {
	fs := flag.NewFlagSet("session rm", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	yes := fs.Bool("yes", false, "skip the confirmation prompt (required for filtered deletes off a TTY)")
	projectName := fs.String("project", "", "delete every session of this project")
	olderThan := fs.String("older-than", "", "delete sessions idle longer than this (e.g. 30d, 2w, 12h)")
	all := fs.Bool("all", false, "delete every session")
	fs.Parse(reorderFlags(args, map[string]bool{"config": true, "project": true, "older-than": true}))
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	store := sessions.NewStore(sessionStoreRoot(cfg))
	loc := i18n.Detect()
	ctx := context.Background()
	wt := openWorktreesBestEffort(cfg.Storage.WorkPath)

	filtered := *all || *projectName != "" || *olderThan != ""
	if !filtered {
		refs := fs.Args()
		if len(refs) == 0 {
			fmt.Fprintln(os.Stderr, "usage: panda session rm <id> [id …] | --project P | --older-than D | --all [--yes]")
			os.Exit(2)
		}
		deleted, failed := 0, 0
		for _, ref := range refs {
			id, err := rmOneSession(ctx, store, wt, ref)
			if err != nil {
				failed++
				printSessionRmErr(loc, ref, err)
				continue
			}
			deleted++
			fmt.Println(i18n.Tf(loc, "cli.session.rm.one", "id", id))
		}
		if jsonOutput {
			emitJSON(map[string]any{"deleted": deleted, "failed": failed})
		}
		if failed > 0 {
			os.Exit(1)
		}
		return
	}

	// Filtered form: gather candidates first so the confirmation names a
	// number, and "nothing matched" is a quiet no-op rather than a prompt.
	list, err := store.List()
	if err != nil {
		fatal("list sessions", err)
	}
	var cutoff time.Time
	if *olderThan != "" {
		d, err := parseOlderThan(*olderThan)
		if err != nil {
			fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(loc, "cli.session.rm.olderBad", "val", *olderThan))
			os.Exit(2)
		}
		cutoff = time.Now().Add(-d)
	}
	var candidates []*sessions.Session
	for _, s := range list {
		if *projectName != "" && s.Project != *projectName {
			continue
		}
		if !cutoff.IsZero() && !s.UpdatedAt.Before(cutoff) {
			continue
		}
		candidates = append(candidates, s)
	}
	if len(candidates) == 0 {
		fmt.Println(i18n.T(loc, "cli.session.rm.none"))
		return
	}
	if !*yes {
		if !stdinIsTTY() {
			fmt.Fprintln(os.Stderr, "panda session rm: "+i18n.T(loc, "cli.session.rm.nonTTY"))
			os.Exit(2)
		}
		fmt.Print(i18n.Tf(loc, "cli.session.rm.confirm", "n", strconv.Itoa(len(candidates))))
		var ans string
		if _, err := fmt.Scanln(&ans); err != nil && ans == "" {
			return // empty line = the default "no"
		}
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			return
		}
	}

	deleted, failed := 0, 0
	for _, s := range candidates {
		if _, err := rmOneSession(ctx, store, wt, s.ID); err != nil {
			failed++
			printSessionRmErr(loc, s.ID, err)
			continue
		}
		deleted++
	}
	if jsonOutput {
		emitJSON(map[string]any{"deleted": deleted, "failed": failed})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.session.rm.done", "n", strconv.Itoa(deleted)))
	if failed > 0 {
		os.Exit(1)
	}
}

// rmOneSession resolves ref (exact id or unique prefix), removes its worktree
// best-effort, then deletes the session file. The returned string is the
// resolved full id for reporting.
func rmOneSession(ctx context.Context, store *sessions.Store, wt *sessions.Worktrees, ref string) (string, error) {
	id, err := resolveSessionRef(store, ref)
	if err != nil {
		return "", err
	}
	if wt != nil {
		_ = wt.Remove(ctx, id)
	}
	if err := store.Delete(id); err != nil {
		return id, err
	}
	return id, nil
}

// resolveSessionRef maps a user-typed ref to a session id: exact match wins,
// then unique prefix. Zero matches is ErrNotFound; several is
// errSessionAmbiguous carrying the candidate list for the message.
var errSessionAmbiguous = errors.New("sessions: ambiguous ref")

type sessionAmbiguousError struct{ candidates []string }

func (e sessionAmbiguousError) Error() string { return errSessionAmbiguous.Error() }

func resolveSessionRef(store *sessions.Store, ref string) (string, error) {
	if _, err := store.Get(ref); err == nil {
		return ref, nil
	}
	list, err := store.List()
	if err != nil {
		return "", err
	}
	var matches []string
	for _, s := range list {
		if strings.HasPrefix(s.ID, ref) {
			matches = append(matches, s.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", sessions.ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return "", sessionAmbiguousError{candidates: matches}
	}
}

// printSessionRmErr reports one failed ref in a batch — the row names what
// was typed so a mixed batch keeps its failures attributable.
func printSessionRmErr(loc i18n.Locale, ref string, err error) {
	var amb sessionAmbiguousError
	switch {
	case errors.As(err, &amb):
		fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(loc, "cli.session.rm.ambiguous",
			"id", ref, "n", strconv.Itoa(len(amb.candidates))))
	case errors.Is(err, sessions.ErrNotFound):
		fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(loc, "cli.noSuch.session", "id", ref))
	default:
		fmt.Fprintf(os.Stderr, "panda: %s: %v\n", ref, err)
	}
}

// parseOlderThan accepts what time.ParseDuration does plus day/week suffixes
// ("30d", "2w") — the units people actually type for "how old".
func parseOlderThan(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	for _, su := range []struct {
		suffix string
		mult   time.Duration
	}{{"d", 24 * time.Hour}, {"w", 7 * 24 * time.Hour}} {
		if strings.HasSuffix(s, su.suffix) {
			n, err := strconv.Atoi(strings.TrimSuffix(s, su.suffix))
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid duration %q", s)
			}
			return time.Duration(n) * su.mult, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}

// runSessionFork splits a session into two threads at a turn boundary. The
// child inherits the parent's turns up to the boundary and its project; in a
// repository its worktree branches off the parent's branch (so the child
// sees the code state the parent produced). The parent keeps its full
// thread — a fork never rewrites history.
func runSessionFork(args []string) {
	fs := flag.NewFlagSet("session fork", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	at := fs.Int("at", 0, "number of turns to copy into the fork (default: all)")
	fs.Parse(reorderFlags(args, map[string]bool{"config": true, "at": true}))
	id := strings.TrimSpace(fs.Arg(0))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda session fork <id> [--at N]")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	store := sessions.NewStore(sessionStoreRoot(cfg))
	parent, err := store.Get(id)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(i18n.Detect(), "cli.noSuch.session", "id", id))
			os.Exit(1)
		}
		fatal("load session", err)
	}
	child, err := store.Fork(id, *at)
	if err != nil {
		fatal("fork session", err)
	}
	// Repo sessions get a worktree rooted at the parent's branch when it has
	// one (inherit the code state), else at HEAD like `session new`.
	if wt := openWorktreesBestEffort(cfg.Storage.WorkPath); wt != nil {
		base := "HEAD"
		if parent.Branch != "" {
			base = parent.Branch
		}
		if path, err := wt.EnsureFrom(context.Background(), child.ID, base); err == nil {
			_ = store.SetWorktree(child.ID, path, sessions.Branch(child.ID))
			child, _ = store.Get(child.ID)
		}
	}
	if jsonOutput {
		emitJSON(child)
		return
	}
	loc := i18n.Detect()
	fmt.Println(i18n.Tf(loc, "cli.session.forked", "parent", id, "id", child.ID, "n", fmt.Sprint(child.ForkIndex)))
	if child.Worktree != "" {
		fmt.Printf("worktree: %s  branch: %s\n", child.Worktree, child.Branch)
	}
}

// runSessionTree renders the conversation tree: root threads with their
// forks nested beneath, so a session family's branching is visible at a
// glance. `tree <id>` scopes to the family containing id.
func runSessionTree(args []string) {
	fs := flag.NewFlagSet("session tree", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	store := sessions.NewStore(sessionStoreRoot(cfg))
	list, err := store.List()
	if err != nil {
		fatal("list sessions", err)
	}
	loc := i18n.Detect()
	if len(list) == 0 {
		fmt.Println(i18n.T(loc, "cli.session.none"))
		return
	}
	byID := make(map[string]*sessions.Session, len(list))
	kids := make(map[string][]*sessions.Session)
	var roots []*sessions.Session
	for _, s := range list {
		byID[s.ID] = s
	}
	for _, s := range list {
		if s.ParentID != "" && byID[s.ParentID] != nil {
			kids[s.ParentID] = append(kids[s.ParentID], s)
		} else {
			roots = append(roots, s)
		}
	}
	for _, k := range kids {
		sortSessionsByCreated(k)
	}
	// `tree <id>` climbs to the family root; orphan-parented children (their
	// parent was deleted) already surface as roots.
	scope := strings.TrimSpace(fs.Arg(0))
	if scope != "" {
		s := byID[scope]
		if s == nil {
			fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(i18n.Detect(), "cli.noSuch.session", "id", scope))
			os.Exit(1)
		}
		for s.ParentID != "" && byID[s.ParentID] != nil {
			s = byID[s.ParentID]
		}
		roots = []*sessions.Session{s}
	}
	var walk func(s *sessions.Session, depth int, last bool, prefix string)
	walk = func(s *sessions.Session, depth int, last bool, prefix string) {
		connector := ""
		childPrefix := prefix
		if depth > 0 {
			if last {
				connector = "└─ "
				childPrefix += "   "
			} else {
				connector = "├─ "
				childPrefix += "│  "
			}
		}
		title := s.Title
		if title == "" {
			title = i18n.T(loc, "cli.session.untitled")
		}
		fmt.Printf("%s%s%s  %s  %s\n", prefix, connector, s.ID,
			i18n.Tf(loc, "cli.session.tree.turns", "n", fmt.Sprint(len(s.Turns))), title)
		children := kids[s.ID]
		for i, c := range children {
			walk(c, depth+1, i == len(children)-1, childPrefix)
		}
	}
	for i, r := range roots {
		walk(r, 0, i == len(roots)-1, "")
	}
}

func sortSessionsByCreated(list []*sessions.Session) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].CreatedAt.Before(list[j-1].CreatedAt); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// runSessionAsk continues a session from the terminal: same flow as the web
// console's POST /api/sessions/{id}/ask (persist the user turn, run with the
// full history in the session's worktree, bind a spawned task back to the
// session, store the assistant turn) — but the stream goes straight to the
// terminal instead of SSE.
func runSessionAsk(args []string) {
	fs := flag.NewFlagSet("session ask", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	cardPath := fs.String("card", cardFlagDefault(), "path to capabilities.yaml (default: discovered; required to execute tasks)")
	mcpCmd := fs.String("mcp", cliMCP, "MCP server command (space-separated)")
	authorize := fs.Bool("authorize", false, "authorize tier-2 (irreversible) commands")
	fs.Parse(reorderFlags(args, commonValueFlags))
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
		ConfigPath: *configPath,
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
			fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(i18n.Detect(), "cli.noSuch.session", "id", id))
			os.Exit(1)
		}
		fatal("load session", err)
	}

	// History is the thread as it stands (auto-compacted when it overflows
	// the replay budget); the fresh turn is persisted after building it
	// because AskTurns carries the prompt itself — replaying the persisted
	// copy too would send two consecutive user messages (400).
	var history []entry.Turn
	sess, history = sessionHistory(context.Background(), store, sess, engine)
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

	streamed := stdoutIsTTY()
	out, err := askSessionTurns(engine, history, prompt, workDir, sess.ID, *authorize)
	if err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
		_, _ = store.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "⚠ " + err.Error(), Kind: "error"})
		os.Exit(1)
	}
	// A tier-2 task with no standing consent parks in review; on an
	// interactive terminal prompt for it here so the thread records the
	// resolved outcome, not the transient park. Session-scoped remembers bind
	// to this session id.
	// A plan may park more than once: each approval re-enters the stage watch
	// and the next parked stage comes back as another NeedsApproval. The loop
	// ends on a denial or when the same stage still parks after its resume —
	// re-prompting that card forever would deadlock.
	for out.NeedsApproval && out.Approval != nil {
		next := confirmApprovalCLI(engine, out, loc, sess.ID)
		if next == out || (next.NeedsApproval && next.Approval != nil && next.Approval.TaskID == out.Approval.TaskID) {
			out = next
			break
		}
		out = next
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
	case "answer":
		// A TTY already streamed these lines live; a pipe saw nothing, and
		// without this branch `panda session ask <id> q | …` exits 0 with
		// empty stdout while the answer sits in the store.
		if !streamed {
			fmt.Println(renderCliMd(out.Answer))
		}
	case "task":
		fmt.Println(i18n.Tf(loc, "cli.session.task", "id", out.TaskID, "state", out.TaskState))
		if out.OK {
			fmt.Print(renderCliMd(out.Stdout))
			if s := strings.TrimRight(out.Stdout, "\n"); s != "" && !strings.HasSuffix(out.Stdout, "\n") {
				fmt.Println()
			}
		} else {
			// Failure evidence only: a parked or cancelled row carries
			// OK=false with no stderr, and "exit 0: " there would report a
			// failure that never happened.
			if out.ExitCode != 0 || strings.TrimSpace(out.Stderr) != "" {
				fmt.Fprintf(os.Stderr, "exit %d: %s\n", out.ExitCode, out.Stderr)
			}
			os.Exit(1)
		}
	case "plan":
		printAskPlan(loc, out)
	}
}

// askSessionTurns runs one full-history ask with live streaming on an
// interactive terminal (same UX as askStreaming, but session-aware). sessionID
// scopes any remembered approval this turn produces to this session.
func askSessionTurns(engine *askengine.Engine, history []entry.Turn, prompt, workDir, sessionID string, authorize bool) (*askengine.Result, error) {
	if !stdoutIsTTY() {
		return engine.AskTurnsSession(context.Background(), history, prompt, workDir, "", sessionID, authorize, askengine.StreamCallbacks{})
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
	out, err := engine.AskTurnsSession(context.Background(), history, prompt, workDir, "", sessionID, authorize, cb)
	lr.flush()
	return out, err
}

func runSessionDiff(args []string) {
	fs := flag.NewFlagSet("session diff", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	message := fs.String("message", "", "merge commit message (default: generated)")
	fs.Parse(reorderFlags(args, map[string]bool{"config": true, "message": true}))
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
