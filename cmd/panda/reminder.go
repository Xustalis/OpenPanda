package main

// Command reminder manages scheduled reminders (design P1-28) from the CLI:
//
//	panda reminder list
//	panda reminder add --after 10m "提醒内容"      # 相对时间（30s/10m/2h/1h30m）
//	panda reminder add --at "2026-08-18 15:00" "开会" # 绝对时间（本地时区）
//	panda reminder rm 3
//
// Reminders fire wherever a scanner runs — the daemon or the web panel
// (log line + Web Push when configured); ClaimDue's atomic claim keeps the
// two from double-firing.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

func runReminder(args []string) {
	if len(args) == 0 {
		reminderUsage()
		os.Exit(2)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		reminderList(rest)
	case "add":
		reminderAdd(rest)
	case "rm", "remove", "del":
		reminderRemove(rest)
	default:
		fmt.Fprintf(os.Stderr, "panda: unknown reminder subcommand %q\n", sub)
		reminderUsage()
		os.Exit(2)
	}
}

func reminderUsage() {
	fmt.Fprintln(os.Stderr, "usage: panda reminder <list | add --after 10m \"内容\" | add --at \"2006-01-02 15:04\" \"内容\" | rm <id>>")
}

// reminderStore opens the reminder store from cfg's database.
func reminderStore(cfg *config.Config) (*reminders.Store, func(), error) {
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		return nil, nil, err
	}
	if err := storage.Migrate(db); err != nil {
		db.Close()
		return nil, nil, err
	}
	return reminders.NewStore(db), func() { db.Close() }, nil
}

func reminderList(args []string) {
	fs := flag.NewFlagSet("reminder list", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	store, done, err := reminderStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer done()

	list, err := store.List(context.Background(), true)
	if err != nil {
		fatal("list reminders", err)
	}
	if len(list) == 0 {
		fmt.Println("no reminders")
		return
	}
	for _, r := range list {
		status := "pending"
		if r.FiredAt != 0 {
			status = "fired"
		}
		fmt.Printf("#%-4d %-16s %-8s %s\n",
			r.ID, time.Unix(r.DueAt, 0).Format("2006-01-02 15:04"), status, r.Message)
	}
}

func reminderAdd(args []string) {
	fs := flag.NewFlagSet("reminder add", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	after := fs.String("after", "", "relative delay, e.g. 30s / 10m / 2h / 1h30m")
	at := fs.String("at", "", `absolute local time, e.g. "2026-08-18 15:00"`)
	fs.Parse(args)

	message := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if message == "" {
		fmt.Fprintln(os.Stderr, "panda: missing reminder message")
		reminderUsage()
		os.Exit(2)
	}
	if (*after == "") == (*at == "") {
		fmt.Fprintln(os.Stderr, "panda: exactly one of --after / --at is required")
		reminderUsage()
		os.Exit(2)
	}

	var due time.Time
	if *after != "" {
		dur, err := time.ParseDuration(*after)
		if err != nil {
			fmt.Fprintf(os.Stderr, "panda: invalid --after %q: %v\n", *after, err)
			os.Exit(2)
		}
		due = time.Now().Add(dur)
	} else {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
			if t, err := time.ParseInLocation(layout, *at, time.Local); err == nil {
				due = t
				break
			}
		}
		if due.IsZero() {
			fmt.Fprintf(os.Stderr, "panda: invalid --at %q — use \"2006-01-02 15:04\"\n", *at)
			os.Exit(2)
		}
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	store, done, err := reminderStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer done()

	r, err := store.Add(context.Background(), message, due, "cli")
	if err != nil {
		fatal("add reminder", err)
	}
	fmt.Printf("reminder #%d set — fires %s\n", r.ID, due.Format("2006-01-02 15:04:05"))
	fmt.Printf("  %s\n", message)
}

func reminderRemove(args []string) {
	fs := flag.NewFlagSet("reminder rm", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	idRaw := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if idRaw == "" {
		fmt.Fprintln(os.Stderr, "usage: panda reminder rm [--config PATH] <id>")
		os.Exit(2)
	}
	id, err := strconv.ParseInt(idRaw, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "panda: invalid id %q\n", idRaw)
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	store, done, err := reminderStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer done()

	ok, err := store.Delete(context.Background(), id)
	if err != nil {
		fatal("remove reminder", err)
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "panda: no reminder #%d\n", id)
		os.Exit(1)
	}
	fmt.Printf("removed reminder #%d\n", id)
}
