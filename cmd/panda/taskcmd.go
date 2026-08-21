package main

// `panda task` grew from a one-shot show into the task verb family: show
// stays the default, and add/priority/move join approve/reject/cancel/logs
// (which keep their bare-command forms too). add is the kernel-form of the
// board's POST /api/tasks — same enqueue + linked-session semantics.

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// runTask dispatches `panda task`: a known verb routes to its handler,
// anything else is the legacy `panda task <id>` show form.
func runTask(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "add", "new", "create":
			runTaskAdd(args[1:])
			return
		case "priority":
			runTaskPriority(args[1:])
			return
		case "move":
			runTaskMove(args[1:])
			return
		case "approve":
			runApprove(args[1:])
			return
		case "reject":
			runReject(args[1:])
			return
		case "cancel":
			runCancel(args[1:])
			return
		case "logs":
			runLogs(args[1:])
			return
		case "show":
			runTaskShow(args[1:])
			return
		case "help", "-h", "--help":
			taskUsage()
			return
		}
	}
	runTaskShow(args)
}

func taskUsage() {
	fmt.Fprintln(os.Stderr, "usage: panda task <verb|task-id>")
	fmt.Fprintln(os.Stderr, "  <id>                                    show one task and its timeline")
	fmt.Fprintln(os.Stderr, "  add --title T [--prompt P] [--priority "+cliPriorities+"]")
	fmt.Fprintln(os.Stderr, "      [--project p] [--authorize] [--card PATH]   enqueue a task")
	fmt.Fprintln(os.Stderr, "  priority <id> <level>                   change a task's priority")
	fmt.Fprintln(os.Stderr, "  move <id> <seq>                         reorder the drag-sort queue")
	fmt.Fprintln(os.Stderr, "  approve|reject|cancel|logs <id>         same as the bare commands")
}

// runTaskShow is the legacy `panda task <id>` — one task's full row plus
// event timeline.
func runTaskShow(args []string) {
	fs := flag.NewFlagSet("task", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		taskUsage()
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	t, err := store.Get(context.Background(), id)
	if err != nil {
		taskStoreFatal(err, id)
	}

	if jsonOutput {
		emitJSON(taskToJSON(t))
		return
	}

	fmt.Printf("id:       %s\n", t.TaskID)
	fmt.Printf("parent:   %s\n", orDash(t.ParentID))
	fmt.Printf("project:  %s\n", orDash(t.Project))
	fmt.Printf("title:    %s\n", t.Title)
	fmt.Printf("state:    %s\n", t.State)
	fmt.Printf("priority: %s\n", priorityName(t.Priority))
	fmt.Printf("owner:    %s\n", t.OwnerNode)
	fmt.Printf("attempt:  %s\n", t.AttemptID)
	fmt.Printf("chain:    %s\n", strings.Join(t.Chain, " -> "))
	fmt.Printf("created:  %s\n", ts(t.CreatedAt))
	fmt.Printf("updated:  %s\n", ts(t.UpdatedAt))
	if t.SessionID != "" {
		fmt.Printf("session:  %s\n", t.SessionID)
	}
	if t.ContextType != "" {
		fmt.Printf("context:  %s\n", t.ContextType)
	}
	if t.Intent != "" {
		fmt.Printf("intent:   %s\n", t.Intent)
	}
	if t.SpecJSON != "" {
		fmt.Printf("spec:     %s\n", t.SpecJSON)
	}
	if t.Risk != "" {
		fmt.Printf("risk:     %s\n", t.Risk)
	}
	if t.Complexity != 0 {
		fmt.Printf("complexity: %.2f\n", t.Complexity)
	}
	if t.ResultJSON != "" {
		fmt.Printf("result:   %s\n", t.ResultJSON)
	}

	events, err := store.Events(context.Background(), id)
	if err != nil {
		fatal("load events", err)
	}
	if len(events) > 0 {
		fmt.Println("events:")
		for _, e := range events {
			fmt.Printf("  %s  %-10s %s\n", ts(e.TS), e.Type, e.DataJSON)
		}
	}
}

// taskJSON is the --json wire form of one task (fields mirror the web API's
// taskJSON closely enough for scripts).
type taskJSON struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Project  string `json:"project,omitempty"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Priority string `json:"priority"`
	Owner    string `json:"owner,omitempty"`
	Session  string `json:"session_id,omitempty"`
	Intent   string `json:"intent,omitempty"`
	Created  string `json:"created_at"`
	Updated  string `json:"updated_at"`
}

func taskToJSON(t core.Task) taskJSON {
	return taskJSON{
		ID:       t.TaskID,
		ParentID: t.ParentID,
		Project:  t.Project,
		Title:    t.Title,
		State:    t.State,
		Priority: priorityName(t.Priority),
		Owner:    t.OwnerNode,
		Session:  t.SessionID,
		Intent:   t.Intent,
		Created:  ts(t.CreatedAt),
		Updated:  ts(t.UpdatedAt),
	}
}

func taskStoreFatal(err error, id string) {
	if errors.Is(err, sql.ErrNoRows) {
		fmt.Fprintf(os.Stderr, "panda: no such task: %s\n", id)
		os.Exit(1)
	}
	fatal("get task", err)
}

// runTaskAdd enqueues a user task through the ask engine's scheduler core —
// the kernel-form of the board's "new task" (needs a capability card, exactly
// like POST /api/tasks). A linked session is created so the task's progress
// streams into `panda session show`.
func runTaskAdd(args []string) {
	fs := flag.NewFlagSet("task add", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	cardPath := fs.String("card", defaultCardPath(), "path to capabilities.yaml (default: discovered)")
	mcpCmd := fs.String("mcp", "", "MCP server command (space-separated)")
	title := fs.String("title", "", "task title (required)")
	prompt := fs.String("prompt", "", "task prompt (defaults to the title)")
	priority := fs.String("priority", "normal", "priority: "+cliPriorities)
	project := fs.String("project", "", "project to attach the task to")
	authorize := fs.Bool("authorize", false, "authorize tier-2 (irreversible) commands")
	fs.Parse(args)

	loc := i18n.Detect()
	*title = strings.TrimSpace(*title)
	if *title == "" {
		fmt.Fprintln(os.Stderr, i18n.T(loc, "cli.task.add.noTitle"))
		os.Exit(2)
	}
	*prompt = strings.TrimSpace(*prompt)
	if *prompt == "" {
		*prompt = *title
	}
	prio, ok := parseCLIPriority(*priority)
	if !ok {
		fmt.Fprintln(os.Stderr, i18n.Tf(loc, "cli.task.add.badPriority", "level", *priority, "list", cliPriorities))
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	engine, err := askengine.New(context.Background(), cfg, askengine.Options{
		CardPath:   *cardPath,
		MCPCommand: *mcpCmd,
	})
	if err != nil {
		fatal("ask engine", err)
	}
	defer engine.Close()

	in := core.TaskInput{
		Title:      *title,
		Project:    *project,
		Intent:     *prompt,
		Requires:   []string{"coding"},
		Authorized: *authorize,
	}
	q := core.DefaultQueueSpec()
	q.Priority = prio
	q.WorkDir = engine.WorkPath()

	task, err := engine.EnqueueTask(context.Background(), in, q)
	if err != nil {
		fatal("enqueue task", err)
	}

	// Linked session (same contract as the board): created after a successful
	// enqueue so a failure leaves no orphan behind.
	sessionID := ""
	sessStore := sessions.NewStore(sessionStoreRoot(cfg))
	if sess, err := sessStore.Create(*title); err == nil {
		sessionID = sess.ID
		_, _ = sessStore.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: *prompt})
		db, store, derr := panelStore(cfg)
		if derr == nil {
			_ = store.SetSessionID(context.Background(), task.TaskID, sess.ID)
			db.Close()
		}
	}

	if jsonOutput {
		emitJSON(map[string]string{"task_id": task.TaskID, "session_id": sessionID, "state": task.State})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.task.add.done", "id", task.TaskID, "state", task.State, "priority", priorityName(prio)))
	if sessionID != "" {
		fmt.Println(i18n.Tf(loc, "cli.task.add.session", "id", sessionID))
	}
}

// runTaskPriority implements `panda task priority <id> <level>`.
func runTaskPriority(args []string) {
	fs := flag.NewFlagSet("task priority", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	loc := i18n.Detect()
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: panda task priority <task-id> <"+cliPriorities+">")
		os.Exit(2)
	}
	id, level := fs.Arg(0), fs.Arg(1)
	prio, ok := parseCLIPriority(level)
	if !ok {
		fmt.Fprintln(os.Stderr, i18n.Tf(loc, "cli.task.add.badPriority", "level", level, "list", cliPriorities))
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()
	if err := store.SetPriority(context.Background(), id, prio); err != nil {
		fatal("set priority", err)
	}
	if jsonOutput {
		emitJSON(map[string]string{"id": id, "priority": priorityName(prio)})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.task.priority.done", "id", id, "priority", priorityName(prio)))
}

// runTaskMove implements `panda task move <id> <seq>` — the drag-sort order
// the queue scheduler honors before priority.
func runTaskMove(args []string) {
	fs := flag.NewFlagSet("task move", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: panda task move <task-id> <seq>")
		os.Exit(2)
	}
	id := fs.Arg(0)
	seq, err := strconv.ParseInt(fs.Arg(1), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "panda: seq must be an integer, got %q\n", fs.Arg(1))
		os.Exit(2)
	}
	cfg, cerr := config.Load(*configPath)
	if cerr != nil {
		fatal("load config", cerr)
	}
	db, store, oerr := panelStore(cfg)
	if oerr != nil {
		fatal("open store", oerr)
	}
	defer db.Close()
	if err := store.SetSeq(context.Background(), id, seq); err != nil {
		fatal("move task", err)
	}
	if jsonOutput {
		emitJSON(map[string]any{"id": id, "seq": seq})
		return
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.task.move.done", "id", id, "seq", strconv.FormatInt(seq, 10)))
}
