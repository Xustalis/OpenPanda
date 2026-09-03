package main

// `panda task` grew from a one-shot show into the task verb family: show
// stays the default, and add/priority/move join approve/reject/cancel/logs
// (which keep their bare-command forms too). add is the kernel-form of the
// board's POST /api/tasks — same enqueue + linked-session semantics.

import (
	"context"
	"database/sql"
	"encoding/json"
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

	id = resolveTaskRef(store, id)
	t, err := store.Get(context.Background(), id)
	if err != nil {
		taskStoreFatal(err, id)
	}

	if jsonOutput {
		emitJSON(taskToJSON(t))
		return
	}

	// One field printer for the whole record: the labels line up (they did not —
	// "complexity:" is a column wider than the format string allowed for), and a
	// multi-line value hangs under its own label instead of starting at column
	// zero, where a long intent used to read as the end of the record.
	taskField("id", t.TaskID)
	taskField("parent", orDash(t.ParentID))
	taskField("project", orDash(t.Project))
	taskField("title", t.Title)
	taskField("state", colorState(t.State))
	taskField("priority", priorityName(t.Priority))
	taskField("owner", t.OwnerNode)
	taskField("attempt", t.AttemptID)
	taskField("chain", strings.Join(t.Chain, " "+pal().MarkArrow()+" "))
	taskField("created", ts(t.CreatedAt))
	taskField("updated", ts(t.UpdatedAt))
	if t.SessionID != "" {
		taskField("session", t.SessionID)
	}
	if t.ContextType != "" {
		taskField("context", t.ContextType)
	}
	if t.Intent != "" {
		taskField("intent", t.Intent)
	}
	if t.SpecJSON != "" {
		printTaskSpec(t.SpecJSON)
	}
	if t.Risk != "" {
		taskField("risk", t.Risk)
	}
	if t.Complexity != 0 {
		taskField("complexity", fmt.Sprintf("%.2f", t.Complexity))
	}
	if t.ResultJSON != "" {
		printTaskResult(t.ResultJSON)
	}

	events, err := store.Events(context.Background(), id)
	if err != nil {
		fatal("load events", err)
	}
	if len(events) > 0 {
		fmt.Println(pal().Heading("events:"))
		printEventTimeline(events, "  ")
	}
}

// taskFieldWidth is the label column of the task record, sized to its longest
// label ("complexity:") plus a space.
const taskFieldWidth = 12

// taskField prints one label/value line of the task record, hanging a multi-line
// value under the label so the record stays one readable block.
func taskField(label, value string) {
	head := pal().Muted(cell(label+":", taskFieldWidth))
	lines := strings.Split(strings.TrimRight(value, "\n"), "\n")
	fmt.Println(head + lines[0])
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			fmt.Println() // a blank line in the value stays blank, not 12 spaces
			continue
		}
		fmt.Println(strings.Repeat(" ", taskFieldWidth) + l)
	}
}

// printTaskSpec renders the classifier's structured spec as labelled lines. The
// stored form is a JSON document, and printing it raw put the task's target and
// success criteria — the two things a reader of `panda task` came for — inside
// escaped quotes on one unwrappable line.
func printTaskSpec(raw string) {
	var spec struct {
		Scope       string   `json:"scope"`
		Target      string   `json:"target"`
		Constraints []string `json:"constraints"`
		Success     string   `json:"success_definition"`
	}
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		fmt.Printf("spec:     %s\n", raw) // unknown shape: better raw than dropped
		return
	}
	p := pal()
	field := func(label, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		fmt.Printf("  %s %s\n", p.Muted(cell(label, 9)), value)
	}
	fmt.Println(p.Heading("spec:"))
	field("scope", spec.Scope)
	field("target", spec.Target)
	for i, c := range spec.Constraints {
		label := ""
		if i == 0 {
			label = "limits"
		}
		field(label, p.MarkBullet()+" "+c)
	}
	field("success", spec.Success)
}

// printTaskResult renders the executor's stored result: the outcome first, then
// the output as text. The raw column is a JSON document whose stdout field holds
// the entire agent report with its newlines escaped — the one field a person
// opens `panda task` to read, in the one encoding they cannot read it in.
func printTaskResult(raw string) {
	var res struct {
		ExitCode int    `json:"exit_code"`
		OK       bool   `json:"ok"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		Failed   string `json:"failed"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		fmt.Printf("result:   %s\n", raw)
		return
	}
	p := pal()
	outcome := p.Success(p.MarkOK() + " ok")
	if !res.OK {
		outcome = p.Danger(fmt.Sprintf("%s exit %d", p.MarkFail(), res.ExitCode))
	}
	fmt.Println(p.Heading("result:") + " " + outcome)
	for _, body := range []string{res.Failed, strings.TrimRight(res.Stdout, "\n"), strings.TrimRight(res.Stderr, "\n")} {
		if strings.TrimSpace(body) == "" {
			continue
		}
		for _, line := range strings.Split(body, "\n") {
			fmt.Println("  " + line)
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

// resolveTaskRef turns a user-typed task reference into a full task id, or ends
// the command with a message that says what to type instead. Every listing shows
// a task by its short id, so every command that takes one has to accept it.
func resolveTaskRef(store *core.TaskStore, ref string) string {
	id, err := store.ResolveTaskID(context.Background(), ref)
	if err == nil {
		return id
	}
	var amb *core.AmbiguousTaskIDError
	if errors.As(err, &amb) {
		fmt.Fprintln(os.Stderr, "panda: "+ambiguousTaskMsg(i18n.Detect(), amb))
		os.Exit(2)
	}
	taskStoreFatal(err, ref)
	return ""
}

// ambiguousTaskMsg phrases a prefix collision: how many tasks it hit, then the
// ids themselves — the user's next command is a copy-paste from this list, so
// printing the candidates is the whole point of the message.
func ambiguousTaskMsg(loc i18n.Locale, amb *core.AmbiguousTaskIDError) string {
	msg := i18n.Tf(loc, "cli.task.ambiguous",
		"ref", amb.Ref, "n", strconv.Itoa(len(amb.Candidates)))
	for _, c := range amb.Candidates {
		msg += "\n  " + c
	}
	return msg
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
	requires := fs.String("requires", "coding", "comma-separated ability ids the task needs (routed cross-device)")
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

	requiresList := []string{}
	for _, r := range strings.Split(*requires, ",") {
		if r = strings.TrimSpace(r); r != "" {
			requiresList = append(requiresList, r)
		}
	}
	if len(requiresList) == 0 {
		requiresList = []string{"coding"}
	}
	in := core.TaskInput{
		Title:      *title,
		Project:    *project,
		Intent:     *prompt,
		Requires:   requiresList,
		Authorized: *authorize,
	}
	q := core.DefaultQueueSpec()
	q.Priority = prio
	// Deliberately no WorkDir: engine.WorkPath() is the node-wide default the
	// executor already falls back to, and pinning it re-classifies the task as
	// local-only work — forwardScheduled skips any task with a WorkDir, so
	// `--requires pi.uptime` on a node without that ability never reached the
	// peer that has it (the same trap plan submission avoids).

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
	id = resolveTaskRef(store, id)
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
	id = resolveTaskRef(store, id)
	if err := store.SetSeq(context.Background(), id, seq); err != nil {
		fatal("move task", err)
	}
	if jsonOutput {
		emitJSON(map[string]any{"id": id, "seq": seq})
		return
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.task.move.done", "id", id, "seq", strconv.FormatInt(seq, 10)))
}
