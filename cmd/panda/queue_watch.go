// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"strings"
	"syscall"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// watchInterval is the board's refresh cadence.
const watchInterval = 2 * time.Second

// watchBoardCap bounds the rows one poll reads — several times the 50-row
// display so project-filtered views still find their rows near the top of the
// recent-activity window.
const watchBoardCap = 200

// watchQueue renders the task board in place until ctx ends or SIGINT.
// state/project filter as in the one-shot listing.
func watchQueue(ctx context.Context, cfg *config.Config, store *core.TaskStore, state, project string) {
	watchQueueTo(ctx, cfg, store, state, project, i18n.Detect(), os.Stdout, true)
}

func watchQueueTo(
	ctx context.Context,
	cfg *config.Config,
	store *core.TaskStore,
	state, project string,
	loc i18n.Locale,
	out io.Writer,
	trapSignals bool,
) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if out == nil {
		out = io.Discard
	}
	if loc == "" {
		loc = i18n.Detect()
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

	first := true
	for {
		// Read only the recent-activity slice: ordering and the cap happen in
		// SQL, so a long task history is never walked just to paint 50 rows.
		tasks, err := store.ListRecentByState(ctx, state, watchBoardCap)
		if err == nil {
			var rows []core.Task
			for _, t := range tasks {
				if project == "" || t.Project == project {
					rows = append(rows, t)
				}
			}
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
			cols := planTaskTableRefs(loc, rows, listWidth()-2, taskRefsFor(ctx, store, rows))
			_, _ = fmt.Fprint(out, "  "+taskTableHeader(loc, cols)+"\r\n")
			for _, t := range rows {
				_, _ = fmt.Fprint(out, "  "+taskTableRowState(t, cols, dispState(ctx, store, loc, t))+"\r\n")
			}
			// The board's own silent stall: queued rows with no consumer
			// behind them. The probe reruns each repaint so a daemon that
			// just came up clears the line on the next tick.
			for _, t := range rows {
				if t.State == core.StateQueued || t.State == core.StateSubmitted {
					if !queueConsumerAlive(cfg) {
						_, _ = fmt.Fprint(out, "  "+pal().Muted(i18n.T(loc, "cli.queue.noConsumer"))+"\r\n")
					}
					break
				}
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
