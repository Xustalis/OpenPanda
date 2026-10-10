// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Queue-consumer liveness probe. The check itself lives in internal/core
// (QueueConsumerAlive) so the askengine's classified-plan path can run the
// same probe the CLI does; this file keeps the CLI's call sites and the
// advisory rendering.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

func queueConsumerAlive(cfg *config.Config) bool { return core.QueueConsumerAlive(cfg) }

// panelReachable keeps the CLI call spelling for core.PanelReachable.
func panelReachable(addr string) bool { return core.PanelReachable(addr) }

// warnNoConsumerTo prints the one-line advisory: queued work only runs once a
// daemon or web console is up. Writer-scoped so the REPL routes it through
// commandOutput (the TUI transcript) while CLI callers pass os.Stderr —
// keeping --json stdout clean.
func warnNoConsumerTo(w io.Writer, loc i18n.Locale) {
	_, _ = fmt.Fprintln(w, pal().Muted(i18n.T(loc, "cli.queue.noConsumer")))
}

// warnNoConsumerStderr is the CLI shorthand: hint on stderr, never stdout.
func warnNoConsumerStderr(loc i18n.Locale) {
	warnNoConsumerTo(os.Stderr, loc)
}
