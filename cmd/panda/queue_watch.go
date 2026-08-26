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
	"strings"
	"syscall"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
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
			p := pal()
			fmt.Printf("%s  %s  (%s)\r\n",
				p.Bold(i18n.T(loc, "cli.watch.head")), time.Now().Format("15:04:05"),
				p.Muted(i18n.Tf(loc, "cli.watch.hint", "key", "^C")))
			if len(rows) == 0 {
				fmt.Println("  " + i18n.T(loc, "cli.queue.none"))
			}
			for _, t := range rows {
				fmt.Printf("  %-10s %s %-8s %-12s %s\r\n",
					shortID(t.TaskID), stateCell(t.State, 12), priorityName(t.Priority), orDash(t.OwnerNode), clipRunes(t.Title, 44))
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
	p := pal()
	switch s {
	case core.StateDone:
		return p.Success(s)
	case core.StateFailed, core.StateCancelled, core.StateExpired:
		return p.Danger(s)
	case core.StateRunning, core.StateReview, core.StateDispatched:
		return p.Warn(s)
	case core.StateQueued, core.StateSubmitted:
		return p.Info(s)
	}
	return p.Muted(s)
}

// stateCell is colorState padded to n columns. The padding has to be computed
// here rather than with %-12s: a tinted word carries escape bytes, and the
// verb-width padding fmt applies would count those, knocking every column after
// it out of alignment on a colour terminal.
func stateCell(s string, n int) string {
	pad := n - cliui.DisplayWidth(s)
	if pad < 0 {
		pad = 0
	}
	return colorState(s) + strings.Repeat(" ", pad)
}

// clipRunes truncates s to at most n runes with an ellipsis marker.
func clipRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n-1]) + "…"
}
