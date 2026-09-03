package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/nodeidentity"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// The panel subcommands (status/queue/task/cancel/logs) are read-mostly views
// over the same SQLite store the daemon writes. They share a quiet logger and
// a one-shot DB handle; none of them starts the daemon loop.

// panelStore opens the DB, applies migrations, and returns a ready store.
// Storage startup is shared with runDaemon via openStore (see store.go), so
// the REPL, web server, and panel commands all see the same directory list —
// nothing fails because a user data directory was missing on first launch
// from an arbitrary cwd.
func panelStore(cfg *config.Config) (*sql.DB, *core.TaskStore, error) {
	db, err := openStore(cfg)
	if err != nil {
		return nil, nil, err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return db, core.NewTaskStore(db, logger), nil
}

// runStatus implements `panda status` — this node's identity and the local
// capability directory (Phase 0 employees are all local).
func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	runningOnly := fs.Bool("running", false, "show only currently running nodes")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	nodes, err := ledger.Query(db, "", "")
	if err != nil {
		fatal("query employees", err)
	}
	views := make([]nodeStatusView, 0, len(nodes))
	for _, n := range nodes {
		local := n.ID == core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())
		running := false
		if local {
			held, lockErr := nodeidentity.Held(cfg.Node.Kind, cfg.Node.EffectiveIdentity())
			running = lockErr == nil && held && n.Status == "online"
		} else {
			running = n.Status == "online" && n.LastSeen > 0 && time.Now().Unix()-n.LastSeen <= 45
		}
		if *runningOnly && !running {
			continue
		}
		views = append(views, nodeStatusView{Node: n, Local: local, Running: running})
	}
	if jsonOutput {
		emitJSON(views)
		return
	}
	loc := i18n.Detect()
	if len(views) == 0 {
		fmt.Println(i18n.T(loc, "cli.status.none"))
		return
	}

	// Stable order: by ID.
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })

	// Column widths follow the data, capped: a fleet of short hostnames should
	// not pay for the one long one, and the long one is clipped rather than
	// allowed to wrap (a wrapped row reads as two machines).
	p := pal()
	nameW := cliui.DisplayWidth(i18n.T(loc, "cli.col.node"))
	kindW := cliui.DisplayWidth(i18n.T(loc, "cli.col.kind"))
	chipW := cliui.DisplayWidth(i18n.T(loc, "cli.col.chip"))
	for _, v := range views {
		nameW = max(nameW, cliui.DisplayWidth(v.ID))
		kindW = max(kindW, cliui.DisplayWidth(v.NodeKind))
		chipW = max(chipW, cliui.DisplayWidth(v.Chip))
	}
	nameW, kindW, chipW = min(nameW, 30), min(kindW, 10), min(chipW, 22)
	const whereW, stateW = 6, 8

	fmt.Println(listHeader(
		cell(i18n.T(loc, "cli.col.node"), nameW),
		cell(i18n.T(loc, "cli.col.kind"), kindW),
		cell(i18n.T(loc, "cli.col.where"), whereW),
		cell(i18n.T(loc, "cli.col.state"), stateW),
		cell(i18n.T(loc, "cli.col.chip"), chipW),
		i18n.T(loc, "cli.col.seen"),
	))
	online := 0
	for _, view := range views {
		n := view.Node
		if view.Running {
			online++
		}
		where := "remote"
		if view.Local {
			where = "local"
		}
		fmt.Println(row(
			cell(n.ID, nameW),
			cell(n.NodeKind, kindW),
			cell(where, whereW),
			styledCell(nodeStateWord(view), stateW, nodeStateTint(view)),
			cell(n.Chip, chipW),
			humanAge(loc, n.LastSeen),
		))
		if abilities := n.Abilities(); len(abilities) > 0 {
			// Clipped, not padded: the ability list is a detail row under its
			// node, and padding it would trail invisible whitespace into every
			// copy-paste of a listing.
			fmt.Println("  " + p.Muted(cliui.Truncate(strings.Join(abilities, ", "), listWidth()-2, p.Unicode())))
		}
	}
	fmt.Println(p.Muted(i18n.Tf(loc, "cli.status.summary",
		"total", strconv.Itoa(len(views)), "online", strconv.Itoa(online))))
}

// nodeStateWord collapses the directory's two overlapping liveness fields into
// the one word a reader actually wants. `status` records what the node last
// claimed; `Running` is whether that claim is still fresh (a held identity lock
// locally, a heartbeat inside the 45s window remotely). Printing both invited
// the question the old "stopped status=online" row raised on every listing —
// which of these do I believe? — so the fresh case is "running", a stale claim
// is named "stale" instead of repeating "online", and everything else is
// "offline".
func nodeStateWord(v nodeStatusView) string {
	switch {
	case v.Running:
		return "running"
	case v.Status == "online":
		return "stale"
	default:
		return "offline"
	}
}

// nodeStateTint is the colour for nodeStateWord: green for a live node, yellow
// for a stale claim (the state a user has to act on), dim for a node that is
// honestly gone.
func nodeStateTint(v nodeStatusView) func(string) string {
	p := pal()
	switch {
	case v.Running:
		return p.Success
	case v.Status == "online":
		return p.Warn
	default:
		return p.Muted
	}
}

// runNodeRemove implements `panda nodes remove <id>` — drops a stale row
// from the capability directory: a renamed machine, a peer whose identity
// changed (the pre-identity-fix rows with random suffixes), a decommissioned
// node. The self row and online nodes are refused for the same reason the
// panel refuses them: both re-register themselves, so "removing" them would
// be a no-op wearing a success message.
func runNodeRemove(args []string) {
	fs := flag.NewFlagSet("nodes remove", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 1 {
		fatal("usage", fmt.Errorf("panda nodes remove <id>"))
	}
	id := rest[0]

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	loc := i18n.Detect()
	if id == core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity()) {
		fatal("remove node", fmt.Errorf("%s", i18n.T(loc, "cli.nodes.self")))
	}
	nodes, err := ledger.Query(db, "", "")
	if err != nil {
		fatal("query employees", err)
	}
	for _, n := range nodes {
		if n.ID != id {
			continue
		}
		if n.Status == "online" {
			fatal("remove node", fmt.Errorf("%s", i18n.Tf(loc, "cli.nodes.online", "id", id)))
		}
		if _, err := ledger.Remove(db, id); err != nil {
			fatal("remove node", err)
		}
		fmt.Println(i18n.Tf(loc, "cli.nodes.removed", "id", id))
		return
	}
	fatal("remove node", fmt.Errorf("%s", i18n.Tf(loc, "cli.nodes.none", "id", id)))
}

type nodeStatusView struct {
	ledger.Node
	Local   bool `json:"local"`
	Running bool `json:"running"`
}

// runQueue implements `panda queue [--state s] [--project p] [--watch]` —
// the task board, newest activity first (the web listTasks semantics);
// --watch switches to the in-place refreshing live board.
func runQueue(args []string) {
	fs := flag.NewFlagSet("queue", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	state := fs.String("state", "", "filter by state (empty = all)")
	project := fs.String("project", "", "filter by project (empty = all)")
	watch := fs.Bool("watch", false, "live view: redraw in place until Ctrl-C")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	// A mistyped state used to come back as "queue is empty", which reads as
	// "you have no tasks" rather than "that is not a state" — the one wrong
	// answer a filter can give. Name the vocabulary instead.
	if *state != "" && !core.IsTaskState(*state) {
		fatalMsg(i18n.Tf(i18n.Detect(), "cli.queue.badState",
			"state", *state, "valid", strings.Join(core.TaskStates(), ", ")))
	}

	if *watch {
		watchQueue(context.Background(), store, *state, *project)
		return
	}

	tasks, err := store.ListByState(context.Background(), *state)
	if err != nil {
		fatal("list tasks", err)
	}
	var filtered []core.Task
	for _, t := range tasks {
		if *project == "" || t.Project == *project {
			filtered = append(filtered, t)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].UpdatedAt > filtered[j].UpdatedAt })

	if jsonOutput {
		out := make([]taskJSON, 0, len(filtered))
		for _, t := range filtered {
			out = append(out, taskToJSON(t))
		}
		emitJSON(out)
		return
	}
	loc := i18n.Detect()
	if len(filtered) == 0 {
		fmt.Println(i18n.T(loc, "cli.queue.none"))
		return
	}
	printTaskTable(loc, filtered)
}

// queueListLimit caps the one-shot listing. A queue accumulates: this store had
// several hundred rows, and printing all of them scrolls the ones a user came to
// see off the top of the terminal. The newest page plus a line saying what was
// held back is the answer to "what is my queue doing"; --state / --project (or
// --json) is the answer to "show me everything".
const queueListLimit = 25

// printTaskTable renders the task board as an aligned table: a short id (the
// prefix `panda task`, `approve` and `cancel` all accept), a tinted state, the
// priority, the owning node and as much title as the terminal has room for.
// `panda queue --watch` renders the same rows through the same helpers, so the
// live board and the one-shot listing cannot drift apart.
func printTaskTable(loc i18n.Locale, tasks []core.Task) {
	p := pal()
	shown := tasks
	if len(shown) > queueListLimit {
		shown = shown[:queueListLimit]
	}
	cols := planTaskTable(loc, shown, listWidth())

	fmt.Println(taskTableHeader(loc, cols))
	for _, t := range shown {
		fmt.Println(taskTableRow(t, cols))
	}
	if hidden := len(tasks) - len(shown); hidden > 0 {
		fmt.Println(p.Muted(i18n.Tf(loc, "cli.queue.more", "n", strconv.Itoa(hidden))))
	}
	fmt.Println(p.Muted(i18n.Tf(loc, "cli.queue.summary",
		"shown", strconv.Itoa(len(shown)), "total", strconv.Itoa(len(tasks)))) +
		p.Separator() + stateTally(tasks))
}

// taskTableCols is the column plan for a task listing: fixed budgets for the
// id, state and priority (their vocabularies are bounded), a node column sized
// to the widest owner actually present, and the title taking whatever the
// terminal has left. Shared by the one-shot listing and the watch board.
type taskTableCols struct{ id, state, prio, node, title int }

func planTaskTable(loc i18n.Locale, tasks []core.Task, width int) taskTableCols {
	c := taskTableCols{id: 10, state: 10, prio: 8}
	c.node = cliui.DisplayWidth(i18n.T(loc, "cli.col.node"))
	for _, t := range tasks {
		c.node = max(c.node, cliui.DisplayWidth(shortNode(t.OwnerNode)))
	}
	c.node = min(c.node, 26)
	c.title = max(20, width-(c.id+c.state+c.prio+c.node+4))
	return c
}

// taskTableHeader is the dimmed column-name row.
func taskTableHeader(loc i18n.Locale, c taskTableCols) string {
	return listHeader(
		cell(i18n.T(loc, "cli.col.id"), c.id),
		cell(i18n.T(loc, "cli.col.state"), c.state),
		cell(i18n.T(loc, "cli.col.priority"), c.prio),
		cell(i18n.T(loc, "cli.col.node"), c.node),
		i18n.T(loc, "cli.col.title"),
	)
}

// taskTableRow is one task as a row of sized cells.
func taskTableRow(t core.Task, c taskTableCols) string {
	return row(
		cell(shortID(t.TaskID), c.id),
		stateCell(t.State, c.state),
		cell(priorityName(t.Priority), c.prio),
		cell(shortNode(t.OwnerNode), c.node),
		cell(t.Title, c.title),
	)
}

// stateTally counts the listing by state, busiest first, so the summary line
// answers "how much of this is still moving" without the reader tallying rows.
func stateTally(tasks []core.Task) string {
	counts := map[string]int{}
	for _, t := range tasks {
		counts[t.State]++
	}
	parts := make([]string, 0, len(counts))
	for _, st := range core.TaskStates() { // lifecycle order, not map order
		if n := counts[st]; n > 0 {
			parts = append(parts, colorState(st)+" "+strconv.Itoa(n))
		}
	}
	return strings.Join(parts, "  ")
}

// shortNode trims the runtime suffix off an owner id ("mac-2a08d48f" → "mac").
// The suffix distinguishes two daemons on one host, which matters to the router
// and to nobody reading a queue; it cost 9 columns of every row.
func shortNode(id string) string {
	if id == "" {
		return "-"
	}
	if i := strings.LastIndexByte(id, '-'); i > 0 && len(id)-i == 9 {
		if _, err := strconv.ParseUint(id[i+1:], 16, 64); err == nil {
			return id[:i]
		}
	}
	return id
}

// runCancel implements `panda cancel <id>` — cancels a task and its subtree.
func runCancel(args []string) {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda cancel [--config PATH] <task-id>")
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	// The cancel must travel through the scheduler core: a delegated task's
	// executor only stops when a task_cancel reaches it over the bus, and the
	// store alone never sends one. The engine dials the configured peers (the
	// executor among them) and CancelTree fans the cancel out through them.
	// The card is what makes the engine build that core at all.
	engine, err := askengine.New(context.Background(), cfg, askengine.Options{
		CardPath: defaultCardPath(),
	})
	if err != nil {
		fatal("ask engine", err)
	}
	defer engine.Close()

	id = resolveTaskRef(engine.TaskStore(), id)
	ids, err := engine.CancelTask(context.Background(), id)
	if err != nil {
		fatal("cancel", err)
	}
	if jsonOutput {
		emitJSON(map[string]any{"cancelled": ids})
		return
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.cancel.done", "n", fmt.Sprint(len(ids))))
}

// runApprove implements `panda approve <id>` — approves a reviewed task
// (review -> done). Kernel-form replacement for the web panel's approve action.
func runApprove(args []string) {
	fs := flag.NewFlagSet("approve", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda approve [--config PATH] <task-id>")
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
	if err := store.Approve(context.Background(), id); err != nil {
		fatal("approve", err)
	}
	if jsonOutput {
		emitJSON(map[string]string{"id": id, "status": "approved"})
		return
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.approve.done", "id", id))
}

// runReject implements `panda reject <id> [--reason s]` — rejects a reviewed
// task (review -> failed). Kernel-form replacement for the web panel's reject.
func runReject(args []string) {
	fs := flag.NewFlagSet("reject", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	reason := fs.String("reason", "", "rejection reason")
	fs.Parse(reorderFlags(args, map[string]bool{"--config": true, "--reason": true}))
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda reject [--config PATH] [--reason s] <task-id>")
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
	if err := store.Reject(context.Background(), id, *reason); err != nil {
		fatal("reject", err)
	}
	if jsonOutput {
		emitJSON(map[string]string{"id": id, "status": "rejected"})
		return
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.reject.done", "id", id))
}

// runLogs implements `panda logs <id>` — the event timeline only.
func runLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda logs [--config PATH] <task-id>")
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
	events, err := store.Events(context.Background(), id)
	if err != nil {
		fatal("load events", err)
	}
	if jsonOutput {
		emitJSON(events)
		return
	}
	if len(events) == 0 {
		fmt.Println(i18n.Tf(i18n.Detect(), "cli.logs.none", "id", id))
		return
	}
	printEventTimeline(events, "")
}

// orDash renders an empty string as "-" for aligned fields.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ts renders a Unix-seconds timestamp as RFC3339, or "-" if zero.
func ts(unix int64) string {
	if unix == 0 {
		return "-"
	}
	return time.Unix(unix, 0).Format(time.RFC3339)
}

// runAudit implements `panda audit [entries|verify]` — the hash-chained
// trail. Bare `panda audit` keeps its historical verify semantics; `entries`
// prints the rows (optionally one task's event timeline), `verify` checks the
// chain of either the global audit_log or one task's events.
func runAudit(args []string) {
	verb := "verify"
	if len(args) > 0 && (args[0] == "entries" || args[0] == "verify") {
		verb, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	taskID := fs.String("task", "", "scope the operation to one task (entries: its events; verify: its event chain)")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	if verb == "entries" {
		runAuditEntries(db, store, *taskID)
		return
	}

	if *taskID != "" {
		if err := store.VerifyTaskEventChain(context.Background(), *taskID); err != nil {
			fmt.Fprintf(os.Stderr, "panda: task event chain broken: %v\n", err)
			os.Exit(1)
		}
		if jsonOutput {
			emitJSON(map[string]string{"scope": "task", "id": *taskID, "chain": "ok"})
			return
		}
		fmt.Printf("task %s event chain: OK\n", *taskID)
		return
	}

	audit := security.NewAudit(db)
	if err := audit.VerifyChain(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "panda: audit chain broken: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		emitJSON(map[string]string{"scope": "global", "chain": "ok"})
		return
	}
	fmt.Println("audit chain: OK")
}

// runAuditEntries prints audit trail rows — the global audit_log, or one
// task's event timeline with --task.
func runAuditEntries(db *sql.DB, store *core.TaskStore, taskID string) {
	if taskID != "" {
		events, err := store.Events(context.Background(), taskID)
		if err != nil {
			fatal("load events", err)
		}
		if jsonOutput {
			emitJSON(events)
			return
		}
		if len(events) == 0 {
			fmt.Println(i18n.Tf(i18n.Detect(), "cli.logs.none", "id", taskID))
			return
		}
		for _, e := range events {
			fmt.Printf("%s  %-10s %s\n", ts(e.TS), e.Type, e.DataJSON)
		}
		return
	}

	rows, err := security.NewAudit(db).Entries(context.Background())
	if err != nil {
		fatal("load audit entries", err)
	}
	if jsonOutput {
		emitJSON(rows)
		return
	}
	if len(rows) == 0 {
		fmt.Println(i18n.T(i18n.Detect(), "cli.audit.none"))
		return
	}
	for _, r := range rows {
		fmt.Printf("%-20s %-24s %-16s %-12s %-8s %s\n", ts(r.TS), r.Who, r.What, r.Target, r.Result, r.Detail)
	}
}

// runMetrics implements `panda metrics [--csv]` — export delegation metrics.
func runMetrics(args []string) {
	fs := flag.NewFlagSet("metrics", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	// Opt-in, as `panda help` has always documented it ("metrics [--csv]").
	// The default was true, which made the human table below dead code — plain
	// `panda metrics` answered "how is delegation going" with a spreadsheet.
	asCSV := fs.Bool("csv", false, "output the full history as CSV")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	metrics, err := store.ListDelegationMetrics(context.Background())
	if err != nil {
		fatal("list metrics", err)
	}
	if len(metrics) == 0 {
		if jsonOutput {
			emitJSON([]struct{}{})
			return
		}
		fmt.Println(i18n.T(i18n.Detect(), "cli.metrics.none"))
		return
	}

	if jsonOutput {
		emitJSON(metrics)
		return
	}

	if *asCSV {
		w := csv.NewWriter(os.Stdout)
		_ = w.Write([]string{"id", "task_id", "delegator", "executor", "abilities", "success", "latency_ms", "tokens", "created_at"})
		for _, m := range metrics {
			abilities := ""
			if m.AbilitiesJSON != "" {
				var parsed []string
				if err := json.Unmarshal([]byte(m.AbilitiesJSON), &parsed); err == nil {
					abilities = strings.Join(parsed, ";")
				} else {
					abilities = m.AbilitiesJSON
				}
			}
			tokens := ""
			if m.Tokens.Valid {
				tokens = strconv.FormatInt(m.Tokens.Int64, 10)
			}
			_ = w.Write([]string{
				strconv.FormatInt(m.ID, 10),
				m.TaskID,
				m.Delegator,
				m.Executor,
				abilities,
				strconv.FormatBool(m.Success),
				strconv.FormatInt(m.LatencyMs, 10),
				tokens,
				ts(m.CreatedAt),
			})
		}
		w.Flush()
		if err := w.Error(); err != nil {
			fatal("write csv", err)
		}
		return
	}

	printMetricsTable(i18n.Detect(), metrics)
}

// metricsListLimit caps the human listing at one screen of recent delegations.
// The summary line above it is computed over every row, so capping the table
// costs detail, not truth; --csv still prints the whole history.
const metricsListLimit = 20

// printMetricsTable renders delegation metrics as a summary plus the most recent
// rows. The summary is the part that answers the question the command is asked —
// is delegation working, and how fast — so it leads, and the rows are the
// evidence under it.
func printMetricsTable(loc i18n.Locale, metrics []core.DelegationMetric) {
	p := pal()
	fmt.Println(p.Muted(metricsSummary(loc, metrics)))

	shown := metrics
	if len(shown) > metricsListLimit {
		shown = shown[:metricsListLimit]
	}
	routeW := cliui.DisplayWidth(i18n.T(loc, "cli.col.route"))
	for _, m := range shown {
		routeW = max(routeW, cliui.DisplayWidth(metricRoute(m)))
	}
	routeW = min(routeW, max(24, listWidth()-42))
	const whenW, okW, latW, tokW = 16, 4, 9, 8

	fmt.Println(listHeader(
		cell(i18n.T(loc, "cli.col.when"), whenW),
		cell(i18n.T(loc, "cli.col.route"), routeW),
		cell(i18n.T(loc, "cli.col.ok"), okW),
		cell(i18n.T(loc, "cli.col.latency"), latW),
		i18n.T(loc, "cli.col.tokens"),
	))
	for _, m := range shown {
		mark, tint := p.MarkOK(), p.Success
		if !m.Success {
			mark, tint = p.MarkFail(), p.Danger
		}
		tokens := "-"
		if m.Tokens.Valid {
			tokens = cliui.HumanCount(m.Tokens.Int64)
		}
		fmt.Println(row(
			cell(time.Unix(m.CreatedAt, 0).Format("01-02 15:04:05"), whenW),
			cell(metricRoute(m), routeW),
			styledCell(mark, okW, tint),
			cell(cliui.HumanDuration(time.Duration(m.LatencyMs)*time.Millisecond), latW),
			tokens,
		))
	}
	if hidden := len(metrics) - len(shown); hidden > 0 {
		fmt.Println(p.Muted(i18n.Tf(loc, "cli.metrics.more", "n", strconv.Itoa(hidden))))
	}
}

// metricRoute is the "who asked → who ran" cell, with the runtime suffixes
// trimmed the same way the queue trims them.
func metricRoute(m core.DelegationMetric) string {
	return shortNode(m.Delegator) + " " + pal().MarkArrow() + " " + shortNode(m.Executor)
}

// metricsSummary reduces the whole history to the line a user actually reads:
// how many delegations, how many succeeded, the median and tail latency, and the
// tokens they cost. p50/p95 rather than a mean — one 20-second agent run drags a
// mean far away from what a typical delegation feels like.
func metricsSummary(loc i18n.Locale, metrics []core.DelegationMetric) string {
	lat := make([]int64, 0, len(metrics))
	var ok int
	var tokens int64
	for _, m := range metrics {
		if m.Success {
			ok++
		}
		if m.Tokens.Valid {
			tokens += m.Tokens.Int64
		}
		lat = append(lat, m.LatencyMs)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	ms := func(v int64) string { return cliui.HumanDuration(time.Duration(v) * time.Millisecond) }
	return i18n.Tf(loc, "cli.metrics.summary",
		"n", strconv.Itoa(len(metrics)),
		"ok", strconv.Itoa(ok),
		"p50", ms(percentile(lat, 50)),
		"p95", ms(percentile(lat, 95)),
		"tokens", cliui.HumanCount(tokens))
}

// percentile returns the p-th percentile of a sorted slice (nearest-rank), 0 for
// an empty one.
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := (p * len(sorted)) / 100
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}
