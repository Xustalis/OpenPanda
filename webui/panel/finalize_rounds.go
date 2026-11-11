// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/log"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func roundSummaryKey(r core.AskRound) string {
	sum := sha256.Sum256([]byte(r.Version()))
	return "round_summary:" + r.ID + ":" + hex.EncodeToString(sum[:])
}

// roundStateWord names a task state the way the thread talks about it. An
// unknown state falls back to the wire token: a new lifecycle state should
// print something readable rather than a raw i18n key.
func roundStateWord(loc i18n.Locale, state string) string {
	switch state {
	case core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired, core.StateReview:
		return i18n.T(loc, "session.round.state."+state)
	}
	return state
}

// roundSummary is the panda closing a fan-out: how many jobs went out, and
// how each one ended. The header carries the count and the bullets carry the
// outcomes, which is the whole tally — the old line spelled out every bucket
// including the empty ones, appended a uuid a person cannot read, and put the
// raw state token in brackets, so it read like a log file dropped into the
// conversation.
//
// The per-task reports above already carry each agent's own conclusion; these
// bullets only establish the scope of the batch, so a reader never mistakes
// one task's report for the round's outcome.
func roundSummary(r core.AskRound, loc i18n.Locale) string {
	var b strings.Builder
	b.WriteString(i18n.Tf(loc, "session.round.head", "total", strconv.Itoa(len(r.Tasks))))
	if r.Status == "stopped" || r.Status == "interrupted" {
		b.WriteString("\n\n")
		b.WriteString(i18n.T(loc, "session.round.detached"))
	}
	// A blank line before the list: a list that follows a paragraph line with
	// no gap is a lazy continuation in CommonMark, and the console renders the
	// text as Markdown.
	var bullets []string
	for _, t := range r.Tasks {
		line := "- " + roundStateWord(loc, t.State)
		if title := strings.TrimSpace(strings.ReplaceAll(t.Title, "\n", " ")); title != "" {
			line += " · " + title
		}
		bullets = append(bullets, line)
	}
	if len(bullets) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(bullets, "\n"))
	}
	return b.String()
}

func (h *handler) finalizeAskRounds(ctx context.Context) {
	if h.store == nil || h.sessions == nil {
		return
	}
	rounds, err := h.store.SettledAskRounds(ctx)
	if err != nil {
		log.From(ctx).Warn("round summary scan", "err", err)
		return
	}
	// A round records the console's language when it is dispatched; the
	// persisted ui.locale is the fallback for rounds that predate that field.
	// The engine locale is never consulted here — only the CLI sets it, and
	// borrowing it is how a Chinese console printed English round results.
	fallback := h.locale()
	for _, r := range rounds {
		settled := len(r.Tasks) > 0
		for _, t := range r.Tasks {
			switch t.State {
			case core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired:
			default:
				settled = false
			}
		}
		if !settled {
			continue
		} // a retry can race candidate selection
		// A single job needs no closing line: its own report turn landed in
		// the same thread a moment earlier and already names the job and its
		// outcome, so a "round result" would be a second voice saying the same
		// thing. Mark it reported anyway so the sweep stops re-reading it.
		if len(r.Tasks) < 2 {
			if err := h.store.MarkAskRoundReported(ctx, r.ID, r.Version()); err != nil {
				log.From(ctx).Warn("round summary marker", "round", r.ID, "err", err)
			}
			continue
		}
		lang := i18n.Locale(r.Locale)
		if lang == "" {
			lang = fallback
		}
		if _, err := h.sessions.AppendTurn(r.SessionID, sessions.Turn{
			Role: "assistant", Kind: "answer", RoundID: r.ID, Text: roundSummary(r, lang), IdempotencyKey: roundSummaryKey(r),
		}); err != nil {
			log.From(ctx).Warn("round summary append", "round", r.ID, "err", err)
			continue
		}
		if err := h.store.MarkAskRoundReported(ctx, r.ID, r.Version()); err != nil {
			log.From(ctx).Warn("round summary marker", "round", r.ID, "err", err)
		}
	}
}
