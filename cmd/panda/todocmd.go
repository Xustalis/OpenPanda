// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda todo` and `/todo` — the terminal's to-do board.
//
// The console's 待办 page (webui/app/src/views/todos.tsx) is the reference for
// the data model: a todo is a human intention, not a task, and it only becomes
// work when its owner says so ("开始"). This file gives the terminal the same
// verbs over the same internal/todos store: list, add, edit, done/reopen,
// delete, show, and start-as-task.
//
// One implementation serves both front ends: the CLI supplies os.Stdout; the
// REPL/TUI supplies the captured command writer, so a slash command lands in
// the transcript instead of tearing through the alternate screen.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/taskhub"
	"github.com/Xustalis/OpenPanda/internal/todos"
)

// todoEnv carries what one todo command needs from its host front end.
type todoEnv struct {
	out, errW  io.Writer
	loc        i18n.Locale
	jsonOut    bool
	cfg        *config.Config
	configPath string
	cardPath   string
	mcp        string
	engine     *askengine.Engine // nil: build a LocalOnly engine for `start`
}

func (e todoEnv) errf(format string, args ...any) {
	_, _ = fmt.Fprintf(e.errW, format, args...)
}

func (e todoEnv) outln(args ...any) {
	_, _ = fmt.Fprintln(e.out, args...)
}

func (e todoEnv) outf(format string, args ...any) {
	_, _ = fmt.Fprintf(e.out, format, args...)
}

// runTodo is the CLI entry point: open the store, apply the verb, map an error
// to an exit code.
func runTodo(args []string) {
	loc := i18n.Detect()
	// Usage answers before the store opens: help has to work on a machine that
	// has never run `panda init`, where loading the config would fail first.
	if todoHelpRequested(args) {
		fmt.Println(i18n.T(loc, "cli.todo.usage"))
		return
	}
	cfg, err := config.Load(cliConfigPath)
	if err != nil {
		fatal("load config", err)
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	env := todoEnv{
		out:        os.Stdout,
		errW:       os.Stderr,
		loc:        loc,
		jsonOut:    jsonOutput,
		cfg:        cfg,
		configPath: cliConfigPath,
		cardPath:   cardFlagDefault(),
		mcp:        cliMCP,
	}
	if err := todoMain(context.Background(), db, env, args); err != nil {
		fail(2, "panda: "+err.Error())
	}
}

// cmdTodo is the REPL/TUI slash handler. It shares todoMain's semantics and
// writes into the front end's captured command writer.
func (r *repl) cmdTodo(arg string) {
	db := r.db
	if db == nil {
		r.outln(i18n.T(r.loc, "cli.todo.noStore"))
		return
	}
	env := todoEnv{
		out:        r.commandOutput(),
		errW:       r.commandError(),
		loc:        r.loc,
		jsonOut:    false, // a transcript is not a machine channel
		cfg:        r.cfg,
		configPath: r.configPath,
		cardPath:   r.cardPathNow(),
		engine:     r.engine.Load(),
	}
	if err := todoMain(context.Background(), db, env, splitSlashArgs(arg)); err != nil {
		r.outln(pal().Warn(err.Error()))
	}
}

// splitSlashArgs splits a slash argument into argv tokens, honouring double quotes
// so `/todo add "call the bank" --due 2026-11-01` keeps a multi-word title.
func splitSlashArgs(arg string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range arg {
		switch {
		case r == '"':
			quoted = !quoted
		case (r == ' ' || r == '\t' || r == '\n') && !quoted:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// todoMain dispatches one todo verb. An unknown verb tells the user what is
// accepted instead of silently listing (a typo'd mutation that lists looks
// like it worked).
func todoMain(ctx context.Context, db *sql.DB, env todoEnv, args []string) error {
	store := todos.NewStore(db)
	// Help wins over verb parsing: `panda todo --help` starts with a dash, so
	// the verb stayed "list" and the flag reached todoList's FlagSet, which
	// returned a bare `flag: help requested` instead of usage.
	if todoHelpRequested(args) {
		env.outln(i18n.T(env.loc, "cli.todo.usage"))
		return nil
	}
	verb := "list"
	rest := args
	if len(args) > 0 {
		// `panda todo --all` is the natural spelling of list-with-flags; only a
		// leading word selects a verb.
		if strings.HasPrefix(args[0], "-") {
			rest = args
		} else {
			verb = strings.ToLower(strings.TrimSpace(args[0]))
			rest = args[1:]
		}
	}
	switch verb {
	case "list", "ls", "board":
		return todoList(ctx, store, env, rest)
	case "add", "new":
		return todoAdd(ctx, store, env, rest)
	case "edit", "set", "update":
		return todoEdit(ctx, store, env, rest)
	case "show", "get":
		return todoShow(ctx, store, env, rest)
	case "done", "complete", "tick":
		return todoSetStatus(ctx, store, env, rest, todos.StatusDone)
	case "reopen", "pending", "undone":
		return todoSetStatus(ctx, store, env, rest, todos.StatusPending)
	case "pause":
		return todoSetStatus(ctx, store, env, rest, todos.StatusPaused)
	case "running", "resume":
		return todoSetStatus(ctx, store, env, rest, todos.StatusRunning)
	case "rm", "remove", "delete", "del":
		return todoDelete(ctx, store, env, rest)
	case "move":
		return todoMove(ctx, store, env, rest)
	case "start", "work", "run":
		return todoStart(ctx, store, env, rest)
	default:
		return fmt.Errorf("%s\n%s", i18n.Tf(env.loc, "cli.todo.unknown", "verb", verb), i18n.T(env.loc, "cli.todo.usage"))
	}
}

// todoHelpRequested reports whether the line asks for usage rather than for a
// verb. Only a leading "help" or an explicit help flag counts: `panda todo add
// help` is a title, not a help request. The check has to run before the
// subcommand FlagSets, because they are created with io.Discard output — a
// help flag that reaches one comes back as a bare `flag: help requested`
// error rather than as usage.
func todoHelpRequested(args []string) bool {
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "help") {
		return true
	}
	for _, a := range args {
		if a == "--" {
			break
		}
		switch a {
		case "-h", "-help", "--help":
			return true
		}
	}
	return false
}

// todoList prints the board. Completed rows are hidden unless --all is given:
// the working set is what a terminal user wants at a glance, and the console's
// done lane is a separate visual area there.
func todoList(ctx context.Context, store *todos.Store, env todoEnv, args []string) error {
	fs := flag.NewFlagSet("todo list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	status := fs.String("status", "", "only this lane (pending|running|paused|done|cancelled)")
	project := fs.String("project", "", "only this project")
	query := fs.String("q", "", "case-insensitive search over title, description and tags")
	all := fs.Bool("all", false, "include done and cancelled rows")
	fs.BoolVar(all, "a", false, "include done and cancelled rows (shorthand)")
	if err := fs.Parse(reorderFlags(fs, args)); err != nil {
		return err
	}
	if *status != "" {
		lane, err := validTodoStatus(env.loc, *status)
		if err != nil {
			return err
		}
		*status = lane
	}
	list, err := store.List(ctx, todos.Filter{Status: *status, Project: *project, Query: *query})
	if err != nil {
		return err
	}
	list = filterTodoVisibility(list, *status != "" || *all)
	if env.jsonOut {
		if list == nil {
			list = []todos.Todo{}
		}
		return writeTodoJSON(env, list)
	}
	if len(list) == 0 {
		env.outln(i18n.T(env.loc, "cli.todo.none"))
		return nil
	}
	env.outln(i18n.T(env.loc, "cli.todo.head"))
	for _, t := range list {
		env.outln("  " + todoLine(env.loc, t))
	}
	return nil
}

// filterTodoVisibility drops terminal lanes unless the caller asked for them.
func filterTodoVisibility(list []todos.Todo, all bool) []todos.Todo {
	if all {
		return list
	}
	out := list[:0:0]
	for _, t := range list {
		switch t.Status {
		case todos.StatusDone, todos.StatusCancelled:
			continue
		}
		out = append(out, t)
	}
	return out
}

// todoLine is one board row: marker, id, lane, priority, due, project, title.
// A bare Linux console has no CJK/box glyphs, so it gets ASCII markers.
func todoLine(loc i18n.Locale, t todos.Todo) string {
	marker, arrow := "○", "→"
	switch t.Status {
	case todos.StatusRunning:
		marker = "●"
	case todos.StatusPaused:
		marker = "◐"
	case todos.StatusDone:
		marker = "✓"
	case todos.StatusCancelled:
		marker = "✗"
	}
	if isLinuxConsole() {
		marker, arrow = "o", "->"
		switch t.Status {
		case todos.StatusRunning:
			marker = "*"
		case todos.StatusPaused:
			marker = "~"
		case todos.StatusDone:
			marker = "x"
		case todos.StatusCancelled:
			marker = "-"
		}
	}
	parts := []string{fmt.Sprintf("%s #%d", marker, t.ID), "[" + t.Status + "]"}
	if t.Priority != "" && t.Priority != todos.PriorityNormal {
		parts = append(parts, t.Priority)
	}
	if t.DueAt > 0 {
		parts = append(parts, i18n.Tf(loc, "cli.todo.due", "date", time.Unix(t.DueAt, 0).Format("2006-01-02")))
	}
	if t.Project != "" {
		parts = append(parts, t.Project)
	}
	line := strings.Join(parts, "  ") + "  " + t.Title
	if len(t.Tags) > 0 {
		line += "  " + strings.Join(t.Tags, ",")
	}
	if t.TaskID != "" {
		line += "  " + arrow + " " + shortID(t.TaskID)
	}
	return line
}

func todoAdd(ctx context.Context, store *todos.Store, env todoEnv, args []string) error {
	fs := flag.NewFlagSet("todo add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	desc := fs.String("desc", "", "longer description (used as the task prompt when started)")
	status := fs.String("status", todos.StatusPending, "lane: pending|running|paused|done|cancelled")
	priority := fs.String("priority", todos.PriorityNormal, "priority: high|normal|low")
	due := fs.String("due", "", "deadline: YYYY-MM-DD or RFC3339")
	tags := fs.String("tags", "", "comma-separated tags")
	project := fs.String("project", "", "project this todo belongs to")
	if err := fs.Parse(reorderFlags(fs, args)); err != nil {
		return err
	}
	title := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if title == "" {
		return errors.New(i18n.T(env.loc, "cli.todo.add.noTitle"))
	}
	dueAt, err := parseTodoDue(env.loc, *due)
	if err != nil {
		return err
	}
	prio, err := validTodoPriority(env.loc, *priority)
	if err != nil {
		return err
	}
	lane, err := validTodoStatus(env.loc, *status)
	if err != nil {
		return err
	}
	t, err := store.Create(ctx, todos.CreateInput{
		Title: title, Description: *desc, Status: lane, Priority: prio,
		DueAt: dueAt, Tags: splitTeamNames(*tags), Project: *project,
	})
	if err != nil {
		return err
	}
	if env.jsonOut {
		return writeTodoJSON(env, t)
	}
	env.outln(i18n.Tf(env.loc, "cli.todo.added", "id", strconv.FormatInt(t.ID, 10)))
	return nil
}

func todoEdit(ctx context.Context, store *todos.Store, env todoEnv, args []string) error {
	fs := flag.NewFlagSet("todo edit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	title := fs.String("title", "", "new title")
	desc := fs.String("desc", "", "new description")
	status := fs.String("status", "", "new lane: pending|running|paused|done|cancelled")
	priority := fs.String("priority", "", "new priority: high|normal|low")
	due := fs.String("due", "", "new deadline (empty string clears it)")
	tags := fs.String("tags", "", "new comma-separated tag list (empty clears)")
	project := fs.String("project", "", "new project (empty clears)")
	if err := fs.Parse(reorderFlags(fs, args)); err != nil {
		return err
	}
	id, err := todoArgID(env.loc, fs.Arg(0))
	if err != nil {
		return err
	}
	var p todos.Patch
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if visited["title"] {
		p.Title = title
	}
	if visited["desc"] {
		p.Description = desc
	}
	if visited["status"] {
		lane, err := validTodoStatus(env.loc, *status)
		if err != nil {
			return err
		}
		p.Status = &lane
	}
	if visited["priority"] {
		prio, err := validTodoPriority(env.loc, *priority)
		if err != nil {
			return err
		}
		p.Priority = &prio
	}
	if visited["due"] {
		dueAt, err := parseTodoDue(env.loc, *due)
		if err != nil {
			return err
		}
		p.DueAt = &dueAt
	}
	if visited["tags"] {
		list := splitTeamNames(*tags)
		p.Tags = &list
	}
	if visited["project"] {
		p.Project = project
	}
	if len(visited) == 0 {
		return errors.New(i18n.T(env.loc, "cli.todo.edit.noFields"))
	}
	t, err := store.Patch(ctx, id, p)
	if err != nil {
		return todoErr(env.loc, err, id)
	}
	if env.jsonOut {
		return writeTodoJSON(env, t)
	}
	env.outln(i18n.Tf(env.loc, "cli.todo.updated", "id", strconv.FormatInt(id, 10)))
	return nil
}

func todoShow(ctx context.Context, store *todos.Store, env todoEnv, args []string) error {
	id, err := todoArgID(env.loc, firstArg(args))
	if err != nil {
		return err
	}
	t, err := store.Get(ctx, id)
	if err != nil {
		return todoErr(env.loc, err, id)
	}
	if env.jsonOut {
		return writeTodoJSON(env, t)
	}
	field := func(label, value string) {
		if value != "" {
			env.outf("  %-10s %s\n", label+":", value)
		}
	}
	field("id", strconv.FormatInt(t.ID, 10))
	field("title", t.Title)
	field("status", t.Status)
	field("priority", t.Priority)
	if t.DueAt > 0 {
		field("due", time.Unix(t.DueAt, 0).Format("2006-01-02 15:04"))
	}
	field("project", t.Project)
	field("tags", strings.Join(t.Tags, ", "))
	field("task", t.TaskID)
	field("created", time.Unix(t.CreatedAt, 0).Format("2006-01-02 15:04"))
	field("updated", time.Unix(t.UpdatedAt, 0).Format("2006-01-02 15:04"))
	field("desc", t.Description)
	return nil
}

func todoSetStatus(ctx context.Context, store *todos.Store, env todoEnv, args []string, status string) error {
	id, err := todoArgID(env.loc, firstArg(args))
	if err != nil {
		return err
	}
	t, err := store.Patch(ctx, id, todos.Patch{Status: &status})
	if err != nil {
		return todoErr(env.loc, err, id)
	}
	if env.jsonOut {
		return writeTodoJSON(env, t)
	}
	env.outln(i18n.Tf(env.loc, "cli.todo.updated", "id", strconv.FormatInt(id, 10)))
	return nil
}

func todoDelete(ctx context.Context, store *todos.Store, env todoEnv, args []string) error {
	id, err := todoArgID(env.loc, firstArg(args))
	if err != nil {
		return err
	}
	ok, err := store.Delete(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New(i18n.Tf(env.loc, "cli.todo.notFound", "id", strconv.FormatInt(id, 10)))
	}
	if env.jsonOut {
		return writeTodoJSON(env, map[string]any{"deleted": id})
	}
	env.outln(i18n.Tf(env.loc, "cli.todo.deleted", "id", strconv.FormatInt(id, 10)))
	return nil
}

// todoMove rewrites one row's manual board order — the keyboard counterpart of
// the console's drag. Seq 0 means "untouched", so a positive sequence is the
// only useful value; the store keeps the rest of the lane as it was.
func todoMove(ctx context.Context, store *todos.Store, env todoEnv, args []string) error {
	if len(args) < 2 {
		return errors.New(i18n.T(env.loc, "cli.todo.move.usage"))
	}
	id, err := todoArgID(env.loc, args[0])
	if err != nil {
		return err
	}
	seq, err := strconv.ParseInt(strings.TrimSpace(args[1]), 10, 64)
	if err != nil || seq <= 0 {
		return errors.New(i18n.T(env.loc, "cli.todo.move.usage"))
	}
	t, err := store.Patch(ctx, id, todos.Patch{Seq: &seq})
	if err != nil {
		return todoErr(env.loc, err, id)
	}
	if env.jsonOut {
		return writeTodoJSON(env, t)
	}
	env.outln(i18n.Tf(env.loc, "cli.todo.moved", "id", strconv.FormatInt(id, 10), "seq", strconv.FormatInt(seq, 10)))
	return nil
}

// todoStart enqueues a task from a todo and links the two rows — the terminal
// twin of the console's "交给熊猫" button. The todo's description is the task
// intent; a description-less todo still carries its title.
func todoStart(ctx context.Context, store *todos.Store, env todoEnv, args []string) error {
	id, err := todoArgID(env.loc, firstArg(args))
	if err != nil {
		return err
	}
	eng, closeEng, err := env.engineFor(ctx)
	if err != nil {
		return err
	}
	if closeEng {
		defer eng.Close()
	}
	result, err := taskhub.New(store.DB()).StartLocal(ctx, eng, id, taskhub.RunOptions{})
	if err != nil {
		return todoErr(env.loc, err, id)
	}
	sessionID := result.Task.SessionID
	if !result.AlreadyStarted && env.cfg != nil && result.Todo != nil {
		t := result.Todo
		prompt := strings.TrimSpace(t.Description)
		if prompt == "" {
			prompt = t.Title
		}
		sessionID = linkTaskSessionIn(env.cfg, result.Task.TaskID, t.Title, prompt, t.Project)
	}
	if result.Warning != "" {
		env.errf("⚠ %s\n", result.Warning)
	}
	if env.jsonOut {
		payload := todoStartJSON(result.Task, sessionID, result.Warning, *result.Todo, nil)
		payload["alreadyStarted"] = result.AlreadyStarted
		payload["requestId"] = result.RequestID
		return writeTodoJSON(env, payload)
	}
	key := "cli.todo.started"
	if result.AlreadyStarted {
		key = "cli.todo.alreadyStarted"
	}
	env.outln(i18n.Tf(env.loc, key, "id", strconv.FormatInt(id, 10), "task", result.Task.TaskID, "state", result.Task.State))
	if sessionID != "" {
		env.outln(i18n.Tf(env.loc, "cli.task.add.session", "id", sessionID))
	}
	return nil
}

// todoStartJSON is the machine-readable result of starting a to-do. warning
// carries askengine's dead-queue advisory — the row is durable but nothing
// drains this store, so a script that reads only state would take an
// unclaimable task as one on its way. link_error is the separate, later
// failure to write the back-reference onto the to-do.
func todoStartJSON(task core.Task, sessionID, warn string, updated todos.Todo, linkErr error) map[string]any {
	payload := map[string]any{
		"task_id":    task.TaskID,
		"session_id": sessionID,
		"state":      task.State,
		"warning":    warn,
	}
	if linkErr != nil {
		payload["link_error"] = linkErr.Error()
	} else {
		payload["todo"] = updated
	}
	return payload
}

// engineFor returns the host's engine or builds a short-lived LocalOnly one.
func (e todoEnv) engineFor(ctx context.Context) (*askengine.Engine, bool, error) {
	if e.engine != nil {
		return e.engine, false, nil
	}
	if e.cfg == nil {
		return nil, false, errors.New(i18n.T(e.loc, "cli.todo.noEngine"))
	}
	eng, err := askengine.New(ctx, e.cfg, askengine.Options{
		CardPath:   e.cardPath,
		MCPCommand: e.mcp,
		ConfigPath: e.configPath,
		// A write-and-exit command: enqueue the row and go.
		LocalOnly: true,
	})
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", i18n.T(e.loc, "cli.todo.noEngine"), err)
	}
	return eng, true, nil
}

// ---- small shared helpers ----

func firstArg(args []string) string {
	for _, a := range args {
		if a = strings.TrimSpace(a); a != "" && !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

func todoArgID(loc i18n.Locale, raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New(i18n.Tf(loc, "cli.todo.badID", "id", raw))
	}
	return id, nil
}

// parseTodoDue accepts "" (none), YYYY-MM-DD (end of that local day, matching
// the console) or RFC3339.
func parseTodoDue(loc i18n.Locale, raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "none") || strings.EqualFold(raw, "clear") {
		return 0, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Unix(), nil
	}
	if d, err := time.ParseInLocation("2006-01-02", raw, time.Local); err == nil {
		return d.Add(24*time.Hour - time.Second).Unix(), nil
	}
	return 0, errors.New(i18n.Tf(loc, "cli.todo.badDue", "value", raw))
}

func validTodoStatus(loc i18n.Locale, raw string) (string, error) {
	switch s := strings.ToLower(strings.TrimSpace(raw)); s {
	case todos.StatusPending, todos.StatusRunning, todos.StatusPaused,
		todos.StatusDone, todos.StatusCancelled:
		return s, nil
	default:
		return "", errors.New(i18n.Tf(loc, "cli.todo.badStatus", "value", raw))
	}
}

func validTodoPriority(loc i18n.Locale, raw string) (string, error) {
	switch p := strings.ToLower(strings.TrimSpace(raw)); p {
	case todos.PriorityHigh, todos.PriorityNormal, todos.PriorityLow:
		return p, nil
	default:
		return "", errors.New(i18n.Tf(loc, "cli.todo.badPriority", "value", raw))
	}
}

// todoErr localizes the store's not-found error; anything else passes through.
func todoErr(loc i18n.Locale, err error, id int64) error {
	if errors.Is(err, todos.ErrNotFound) {
		return errors.New(i18n.Tf(loc, "cli.todo.notFound", "id", strconv.FormatInt(id, 10)))
	}
	return err
}

func writeTodoJSON(env todoEnv, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(env.out, string(data))
	return err
}
