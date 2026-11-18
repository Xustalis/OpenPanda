// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

// Streaming guard for the entry protocol.
//
// The ask loop streams answer deltas to the surface before the response is
// classified, so text that turns out to be a structured directive has already
// been painted. One observed failure printed a truncated task JSON into the
// chat pane, where it stayed as the assistant's reply for a task that was never
// created. The classifier now refuses such a body (entry.ErrDirectiveProse) and
// repairs a directive that is merely cut short, but neither helps if the raw
// envelope has already been streamed: the fix has to happen before the first
// character reaches the user.

import (
	"strings"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// directiveStreamProbeRunes is how much held text the guard needs before it
// releases an ordinary answer. The marker that identifies a directive
// ("kind":"task") sits within the first few bytes, so a body that has not shown
// one by this point is prose and must stream normally — holding it longer would
// add latency to every reply to protect against a rare one.
const directiveStreamProbeRunes = 64

// directiveStreamGuard holds the first deltas of a turn long enough to tell a
// directive from prose, and then either releases them or drops them for the
// rest of the call.
type directiveStreamGuard struct {
	emit       func(string)
	held       strings.Builder
	released   bool
	suppressed bool
}

// newDirectiveStreamGuard wraps a delta callback. A nil callback yields a guard
// that does nothing, so callers need no branch of their own.
func newDirectiveStreamGuard(emit func(string)) *directiveStreamGuard {
	return &directiveStreamGuard{emit: emit}
}

// guardedDelta adapts a surface's delta callback for one model call. The
// returned flush must run when the call ends, whatever the outcome: it releases
// text the guard is still holding, and is a no-op once the guard has decided.
func guardedDelta(delta func(string)) (write func(string), flush func()) {
	if delta == nil {
		return nil, func() {}
	}
	g := newDirectiveStreamGuard(delta)
	return g.Write, g.Flush
}

// Write is the OnDelta callback handed to the classifier.
func (g *directiveStreamGuard) Write(delta string) {
	if g == nil || g.emit == nil || delta == "" || g.suppressed {
		return
	}
	if g.released {
		g.emit(delta)
		return
	}
	g.held.WriteString(delta)
	text := g.held.String()
	if strings.TrimSpace(text) == "" {
		return
	}
	if entry.DeclaresDirective(text) {
		// The turn is a dispatch written as text. Drop what was held and stay
		// silent for the rest of the call: the parsed output drives the surface
		// (a task card, or a readable retry), and the envelope never appears.
		g.suppressed = true
		g.held.Reset()
		return
	}
	// Not a directive so far. Wait for enough text to be sure before paying the
	// latency cost of holding it; a newline in the lead-in settles it earlier.
	if len([]rune(text)) < directiveStreamProbeRunes && !strings.Contains(text, "\n") {
		return
	}
	g.Flush()
}

// Flush releases held text to the surface. A suppressed guard stays silent.
func (g *directiveStreamGuard) Flush() {
	if g == nil || g.emit == nil || g.released || g.suppressed {
		return
	}
	g.released = true
	if g.held.Len() > 0 {
		g.emit(g.held.String())
		g.held.Reset()
	}
}
