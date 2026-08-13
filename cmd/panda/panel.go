package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/core"
	"github.com/xenith/panda/internal/ledger"
	"github.com/xenith/panda/internal/storage"
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
	if len(nodes) == 0 {
		fmt.Println("no nodes registered")
		return
	}

	// Stable order: by ID.
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	for _, n := range nodes {
		seen := time.Unix(n.LastSeen, 0).Format(time.RFC3339)
		if n.LastSeen == 0 {
			seen = "never"
		}
		var abilities []string
		for _, a := range n.Native {
			abilities = append(abilities, a.ID)
		}
		for name := range n.Agents {
			abilities = append(abilities, "agent:"+name)
		}
		fmt.Printf("%-16s status=%-8s chip=%-40s last_seen=%s\n", n.ID, n.Status, n.Chip, seen)
		if len(abilities) > 0 {
			fmt.Printf("  abilities: %s\n", strings.Join(abilities, ", "))
		}
	}
}

// runQueue implements `panda queue [--state s]` — the task list, newest first.
func runQueue(args []string) {
	fs := flag.NewFlagSet("queue", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	state := fs.String("state", "", "filter by state (empty = all)")
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
	if len(tasks) == 0 {
		fmt.Println("no tasks")
		return
	}
	for _, t := range tasks {
		fmt.Printf("%-36s %-12s %-6s %s\n", t.TaskID, t.State, t.OwnerNode, t.Title)
	}
}

// runTask implements `panda task <id>` — one task's full row + event timeline.
func runTask(args []string) {
	fs := flag.NewFlagSet("task", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda task [--config PATH] <task-id>")
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
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "panda: no such task: %s\n", id)
			os.Exit(1)
		}
		fatal("get task", err)
	}
	fmt.Printf("id:       %s\n", t.TaskID)
	fmt.Printf("parent:   %s\n", orDash(t.ParentID))
	fmt.Printf("project:  %s\n", orDash(t.Project))
	fmt.Printf("title:    %s\n", t.Title)
	fmt.Printf("state:    %s\n", t.State)
	fmt.Printf("owner:    %s\n", t.OwnerNode)
	fmt.Printf("attempt:  %s\n", t.AttemptID)
	fmt.Printf("chain:    %s\n", strings.Join(t.Chain, " -> "))
	fmt.Printf("created:  %s\n", ts(t.CreatedAt))
	fmt.Printf("updated:  %s\n", ts(t.UpdatedAt))
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

	n, err := store.CancelCascade(context.Background(), id)
	if err != nil {
		fatal("cancel", err)
	}
	fmt.Printf("cancelled %d task(s)\n", n)
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
	if len(events) == 0 {
		fmt.Printf("no events for %s\n", id)
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
