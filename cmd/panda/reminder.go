package main

// Command reminder manages scheduled reminders (design P1-28) from the CLI:
//
//	panda reminder list
//	panda reminder add --after 10m "standup"        # relative (30s/10m/2h/1h30m)
//	panda reminder add --at "2026-08-18 15:00" "meeting" # absolute (local tz)
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
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

func runReminder(args []string) {
	if len(args) == 0 {
		reminderUsage()
		os.Exit(2)
	}
	sub, rest := args[0], args[1:]
	// --help/-h is not a real verb: print the usage line and exit 0, the
	// same contract every flag.FlagSet subcommand gives its --help.
	if sub == "--help" || sub == "-h" {
		fmt.Println("usage: panda reminder <list | add --after 10m \"text\" | add --at \"2006-01-02 15:04\" \"text\" | rm <id>>")
		return
	}
	switch sub {
	case "list", "ls":
		reminderList(rest)
	case "add":
		reminderAdd(rest)
	case "rm", "remove", "del":
		reminderRemove(rest)
	default:
		fmt.Fprintln(os.Stderr, i18n.Tf(i18n.Detect(), "cli.reminder.unknown", "cmd", sub))
		reminderUsage()
		os.Exit(2)
	}
}

func reminderUsage() {
	fmt.Fprintln(os.Stderr, "usage: panda reminder <list | add --after 10m \"text\" | add --at \"2006-01-02 15:04\" \"text\" | rm <id>>")
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
	if jsonOutput {
		emitJSON(list)
		return
	}
	if len(list) == 0 {
		fmt.Println(i18n.T(i18n.Detect(), "cli.reminder.none"))
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
	loc := i18n.Detect()
	if message == "" {
		fmt.Fprintln(os.Stderr, i18n.T(loc, "cli.reminder.noMessage"))
		reminderUsage()
		os.Exit(2)
	}
	if (*after == "") == (*at == "") {
		fmt.Fprintln(os.Stderr, i18n.T(loc, "cli.reminder.oneFlag"))
		reminderUsage()
		os.Exit(2)
	}

	var due time.Time
	if *after != "" {
		dur, err := time.ParseDuration(*after)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.Tf(loc, "cli.reminder.badAfter", "value", *after, "err", err.Error()))
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
			fmt.Fprintln(os.Stderr, i18n.Tf(loc, "cli.reminder.badAt", "value", *at))
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
	if jsonOutput {
		emitJSON(r)
		return
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.reminder.added", "id", strconv.FormatInt(r.ID, 10), "due", due.Format("2006-01-02 15:04:05")))
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
		fmt.Fprintln(os.Stderr, i18n.Tf(i18n.Detect(), "cli.reminder.badID", "id", idRaw))
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
		fmt.Fprintln(os.Stderr, i18n.Tf(i18n.Detect(), "cli.reminder.notFound", "id", strconv.FormatInt(id, 10)))
		os.Exit(1)
	}
	if jsonOutput {
		emitJSON(map[string]any{"id": id, "status": "removed"})
		return
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.reminder.removed", "id", strconv.FormatInt(id, 10)))
}
