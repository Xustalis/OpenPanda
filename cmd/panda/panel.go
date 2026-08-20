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

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// The panel subcommands (status/queue/task/cancel/logs) are read-mostly views
// over the same SQLite store the daemon writes. They share a quiet logger and
// a one-shot DB handle; none of them starts the daemon loop.

// panelStore opens the DB, applies migrations, and returns a ready store.
func panelStore(cfg *config.Config) (*sql.DB, *core.TaskStore, error) {
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	if err := storage.Migrate(db); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("migrate database: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return db, core.NewTaskStore(db, logger), nil
}

// runStatus implements `panda status` — this node's identity and the local
// capability directory (Phase 0 employees are all local).
func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
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
	if jsonOutput {
		emitJSON(nodes)
		return
	}
	loc := i18n.Detect()
	if len(nodes) == 0 {
		fmt.Println(i18n.T(loc, "cli.status.none"))
		return
	}

	// Stable order: by ID.
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	for _, n := range nodes {
		seen := time.Unix(n.LastSeen, 0).Format(time.RFC3339)
		if n.LastSeen == 0 {
			seen = "never"
		}
		abilities := n.Abilities()
		fmt.Printf("%-16s status=%-8s chip=%-40s last_seen=%s\n", n.ID, n.Status, n.Chip, seen)
		if len(abilities) > 0 {
			fmt.Printf("  abilities: %s\n", strings.Join(abilities, ", "))
		}
	}
}

// runQueue implements `panda queue [--state s] [--project p]` — the task
// board, newest activity first (the web listTasks semantics).
func runQueue(args []string) {
	fs := flag.NewFlagSet("queue", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	state := fs.String("state", "", "filter by state (empty = all)")
	project := fs.String("project", "", "filter by project (empty = all)")
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
	if len(filtered) == 0 {
		fmt.Println(i18n.T(i18n.Detect(), "cli.queue.none"))
		return
	}
	for _, t := range filtered {
		fmt.Printf("%-36s %-12s %-8s %-8s %s\n", t.TaskID, t.State, priorityName(t.Priority), orDash(t.OwnerNode), t.Title)
	}
}

// runCancel implements `panda cancel <id>` — cancels a task and its subtree.
func runCancel(args []string) {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda cancel [--config PATH] <task-id>")
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

	ids, err := store.CancelCascade(context.Background(), id)
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
	fs.Parse(args)
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
	fs.Parse(args)
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
	for _, e := range events {
		fmt.Printf("%s  %-10s %s\n", ts(e.TS), e.Type, e.DataJSON)
	}
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
	asCSV := fs.Bool("csv", true, "output as CSV")
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

	for _, m := range metrics {
		abilities := ""
		if m.AbilitiesJSON != "" {
			var parsed []string
			if err := json.Unmarshal([]byte(m.AbilitiesJSON), &parsed); err == nil {
				abilities = strings.Join(parsed, ", ")
			}
		}
		tokens := "-"
		if m.Tokens.Valid {
			tokens = strconv.FormatInt(m.Tokens.Int64, 10)
		}
		fmt.Printf("%d  %s  %s→%s  ok=%v  latency=%dms  tokens=%s  abilities=%s\n",
			m.ID, ts(m.CreatedAt), m.Delegator, m.Executor, m.Success, m.LatencyMs, tokens, abilities)
	}
}
