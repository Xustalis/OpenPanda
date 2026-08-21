package main

// The watch-mode task board: `panda queue --watch` (and /tasks watch in the
// REPL) redraws the queue in place every couple of seconds — the web
// console's live board, in a terminal. Ctrl-C exits the view (the SIGINT
// is intercepted so the process itself keeps running when called from the
// REPL).

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// watchInterval is the board's refresh cadence.
const watchInterval = 2 * time.Second

// watchQueue renders the task board in place until ctx ends or SIGINT.
// state/project filter as in the one-shot listing.
func watchQueue(ctx context.Context, store *core.TaskStore, state, project string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Intercept Ctrl-C: exiting the board is not exiting the REPL.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
		}
	}()

	loc := i18n.Detect()
	first := true
	for {
		tasks, err := store.ListByState(ctx, "")
		if err == nil {
			var rows []core.Task
			for _, t := range tasks {
				if (state == "" || t.State == state) && (project == "" || t.Project == project) {
					rows = append(rows, t)
				}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].UpdatedAt > rows[j].UpdatedAt })
			if n := len(rows); n > 50 {
				rows = rows[:50] // the board shows activity, not the archive
			}
			if first {
				fmt.Print("\x1b[2J\x1b[H") // full clear on entry
				first = false
			} else {
				fmt.Print("\x1b[H") // repaint from the top
			}
			fmt.Printf("\x1b[1m%s\x1b[0m  %s  (\x1b[2m%s\x1b[0m)\r\n",
				i18n.T(loc, "cli.watch.head"), time.Now().Format("15:04:05"), i18n.Tf(loc, "cli.watch.hint", "key", "^C"))
			if len(rows) == 0 {
				fmt.Println("  " + i18n.T(loc, "cli.queue.none"))
			}
			for _, t := range rows {
				fmt.Printf("  %-10s %-12s %-8s %-12s %s\r\n",
					shortID(t.TaskID), colorState(t.State), priorityName(t.Priority), orDash(t.OwnerNode), clipRunes(t.Title, 44))
			}
			fmt.Print("\x1b[J") // clear stale rows below (shrunk lists)
		}
		select {
		case <-ctx.Done():
			fmt.Print("\x1b[0m\x1b[H\x1b[J") // leave a clean screen behind
			fmt.Println(i18n.T(loc, "cli.watch.exited"))
			return
		case <-time.After(watchInterval):
		}
	}
}

// colorState tints a state word on TTYs — green done, red failed, yellow
// running/review, dim otherwise — so the board scans like the web console.
func colorState(s string) string {
	if !stdoutIsTTY() {
		return s
	}
	code := "2" // dim
	switch s {
	case core.StateDone:
		code = "32"
	case core.StateFailed, core.StateCancelled, core.StateExpired:
		code = "31"
	case core.StateRunning, core.StateReview, core.StateDispatched:
		code = "33"
	case core.StateQueued, core.StateSubmitted:
		code = "36"
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// clipRunes truncates s to at most n runes with an ellipsis marker.
func clipRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n-1]) + "…"
}
