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
	"encoding/json"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// directiveStreamProbeRunes is how much held text the guard needs before it
// releases an ordinary answer. A possible JSON protocol prefix stays buffered
// independently of this prose latency threshold.
const directiveStreamProbeRunes = 64

// Unresolved protocol prefixes never become visible merely because they are
// long. Bound their memory and let classification/recovery handle the reply.
const directiveStreamMaxBytes = 16 * 1024

// directiveStreamGuard holds the first deltas of a turn long enough to tell a
// directive from prose, and then either releases them or drops them for the
// rest of the call.
type directiveStreamGuard struct {
	emit       func(string)
	held       strings.Builder
	released   bool
	suppressed bool
	probeAfter int
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

// Suppression must also bind the final answer. A truncated/oversized envelope
// that the classifier could not recognize enters its existing directive repair
// path instead of being saved as an answer after its deltas were withheld.
func guardedStreamClassification(delta func(string), classify func(func(string)) (entry.Output, error)) (entry.Output, error) {
	if delta == nil {
		delta = func(string) {}
	}
	g := newDirectiveStreamGuard(delta)
	out, err := classify(g.Write)
	g.Flush()
	if err == nil && out.Kind == entry.KindAnswer && g.suppressed {
		return entry.Output{}, entry.ErrDirectiveProse
	}
	return out, err
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
	// An unresolved JSON prefix stays private between probes. Parsing every
	// character of a long unfinished string would repeatedly decode its prefix.
	if len(text) < g.probeAfter && len(text) < directiveStreamMaxBytes {
		return
	}
	if strings.TrimSpace(text) == "" {
		g.probeAfter = len(text) + directiveStreamProbeRunes
		if len(text) >= directiveStreamMaxBytes {
			g.suppressed = true
			g.held.Reset()
		}
		return
	}
	pending, directive := protocolStreamPrefix(text)
	if directive || (pending && len(text) >= directiveStreamMaxBytes) {
		// The turn is a dispatch written as text. Drop what was held and stay
		// silent for the rest of the call: the parsed output drives the surface
		// (a task card, or a readable retry), and the envelope never appears.
		g.suppressed = true
		g.held.Reset()
		return
	}
	if pending {
		g.probeAfter = len(text) + directiveStreamProbeRunes
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
	if pending, directive := protocolStreamPrefix(g.held.String()); pending || directive {
		g.suppressed = true
		g.held.Reset()
		return
	}
	g.released = true
	if g.held.Len() > 0 {
		g.emit(g.held.String())
		g.held.Reset()
	}
}

// protocolStreamPrefix distinguishes ordinary prose/code, a possible JSON
// envelope, and a known directive. Decode keys and values so escaped strings,
// late kind fields and arbitrary chunk boundaries have the same meaning.
func protocolStreamPrefix(text string) (pending, directive bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return true, false
	}
	if strings.HasPrefix("```", s) {
		return true, false
	}
	if strings.HasPrefix(s, "```") {
		line, rest, complete := strings.Cut(s[3:], "\n")
		lang := strings.ToLower(strings.TrimSpace(line))
		if !complete {
			return lang == "" || strings.HasPrefix("json", lang), false
		}
		if lang != "" && lang != "json" {
			return false, false
		}
		s = strings.TrimSpace(rest)
		if s == "" {
			return true, false
		}
	}
	if !strings.HasPrefix(s, "{") {
		return false, false
	}
	d := json.NewDecoder(strings.NewReader(s))
	if _, err := d.Token(); err != nil {
		return true, false
	}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return true, false
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return true, false
		}
		if key == "kind" {
			var kind string
			if json.Unmarshal(raw, &kind) == nil {
				switch kind {
				case "task", "plan", "tool_call", "clarify":
					return false, true
				}
			}
		}
	}
	if _, err := d.Token(); err != nil {
		return true, false
	}
	return false, false
}
