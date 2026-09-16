package main

// The watch-mode task board: `panda queue --watch` (and /tasks watch in the
// REPL) redraws the queue in place every couple of seconds — the web
// console's live board, in a terminal. Ctrl-C exits the view (the SIGINT
// is intercepted so the process itself keeps running when called from the
// REPL).

import (
	"context"
	"fmt"
	"io"
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
	watchQueueTo(ctx, store, state, project, os.Stdout, true)
}

func watchQueueTo(
	ctx context.Context,
	store *core.TaskStore,
	state, project string,
	out io.Writer,
	trapSignals bool,
) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if out == nil {
		out = io.Discard
	}

	if trapSignals {
		// The standalone board owns terminal signals; embedded callers cancel the
		// request context instead, so they never install a process-global handler.
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
	}

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
				_, _ = fmt.Fprint(out, "\x1b[2J\x1b[H") // full clear on entry
				first = false
			} else {
				_, _ = fmt.Fprint(out, "\x1b[H") // repaint from the top
			}
			p := pal()
			_, _ = fmt.Fprintf(out, "%s  %s  (%s)\r\n",
				p.Bold(i18n.T(loc, "cli.watch.head")), time.Now().Format("15:04:05"),
				p.Muted(i18n.Tf(loc, "cli.watch.hint", "key", "^C")))
			if len(rows) == 0 {
				_, _ = fmt.Fprint(out, "  "+i18n.T(loc, "cli.queue.none")+"\r\n")
			}
			// Same column plan and same row renderer as the one-shot listing, so
			// the two boards stay one board. The indent is the board's own, and
			// it is charged against the width so a row still fits the terminal.
			cols := planTaskTable(loc, rows, listWidth()-2)
			_, _ = fmt.Fprint(out, "  "+taskTableHeader(loc, cols)+"\r\n")
			for _, t := range rows {
				_, _ = fmt.Fprint(out, "  "+taskTableRow(t, cols)+"\r\n")
			}
			_, _ = fmt.Fprint(out, "\x1b[J") // clear stale rows below (shrunk lists)
		}
		select {
		case <-ctx.Done():
			_, _ = fmt.Fprint(out, "\x1b[0m\x1b[H\x1b[J") // leave a clean screen behind
			_, _ = fmt.Fprintln(out, i18n.T(loc, "cli.watch.exited"))
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
