// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

// Conversation compaction: instead of dropping the oldest exchanges when the
// replay window overflows, fold them into a running digest written by the
// model itself (the Claude Code / pi "auto-compact" pattern). The digest
// travels as a synthetic turn — see cmd/panda/convo.go and the sessions
// Summary field — so the next eviction cycle re-summarizes it together with
// whatever else falls out and the digest stays self-maintaining.

import (
	"context"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// compactTimeout bounds the summarization call: it runs on the conversation
// path (right after a completed ask), so a hung provider must not stall the
// REPL/TUI prompt loop.
const compactTimeout = 60 * time.Second

// SummarizeTurns compresses evicted history into a running digest. evicted
// may begin with the previous digest's synthetic turn; the model is asked
// to fold it forward so the result replaces it, not appends to it. Returns
// ("", nil) when no model client is configured — callers then degrade to a
// hard trim.
func (e *Engine) SummarizeTurns(ctx context.Context, evicted []entry.Turn) (string, error) {
	if len(evicted) == 0 {
		return "", nil
	}
	client, _ := e.healthyClient()
	if client == nil {
		return "", nil
	}
	var transcript strings.Builder
	for _, t := range evicted {
		text := strings.TrimSpace(t.Content)
		if text == "" {
			continue
		}
		role := t.Role
		if role == "" {
			role = "user"
		}
		transcript.WriteString("<turn role=\"")
		transcript.WriteString(role)
		transcript.WriteString("\">\n")
		transcript.WriteString(text)
		transcript.WriteString("\n</turn>\n")
	}
	if transcript.Len() == 0 {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, compactTimeout)
	defer cancel()
	return client.Complete(ctx,
		"You compress conversation history. The transcript may open with an earlier "+
			"digest — merge it forward; do not repeat it verbatim. Write one updated "+
			"digest that lets an assistant continue seamlessly: keep decisions, facts, "+
			"file paths, task/plan ids, and open questions; drop phatic talk. Under 400 "+
			"words, in the transcript's own language. Output only the digest.",
		"<transcript>\n"+transcript.String()+"</transcript>")
}
