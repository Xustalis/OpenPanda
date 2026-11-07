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

func roundSummary(r core.AskRound) string {
	counts := map[string]int{}
	for _, t := range r.Tasks {
		counts[t.State]++
	}
	loc := i18n.Locale(r.Locale)
	text := i18n.Tf(loc, "session.round.summary", "total", strconv.Itoa(len(r.Tasks)), "done", strconv.Itoa(counts[core.StateDone]), "failed", strconv.Itoa(counts[core.StateFailed]), "cancelled", strconv.Itoa(counts[core.StateCancelled]+counts[core.StateExpired]), "review", strconv.Itoa(counts[core.StateReview]))
	if r.Status == "stopped" || r.Status == "interrupted" {
		text += "\n" + i18n.T(loc, "session.round.detached")
	}
	// Keep the whole-round outcome deterministic and cheap. The per-task
	// reports carry each agent's conclusion; these links show the scope of
	// this round rather than suggesting that one task represents the batch.
	for _, t := range r.Tasks {
		text += "\n- [" + t.State + "] " + strings.ReplaceAll(t.Title, "\n", " ") + " (" + t.TaskID + ")"
	}
	return text
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
	for _, r := range rounds {
		settled := len(r.Tasks) > 0
		for _, t := range r.Tasks {
			switch t.State {
			case core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired, core.StateReview:
			default:
				settled = false
			}
		}
		if !settled {
			continue
		} // a retry can race candidate selection
		if _, err := h.sessions.AppendTurn(r.SessionID, sessions.Turn{
			Role: "assistant", Kind: "answer", RoundID: r.ID, Text: roundSummary(r), IdempotencyKey: roundSummaryKey(r),
		}); err != nil {
			log.From(ctx).Warn("round summary append", "round", r.ID, "err", err)
			continue
		}
		if err := h.store.MarkAskRoundReported(ctx, r.ID, r.Version()); err != nil {
			log.From(ctx).Warn("round summary marker", "round", r.ID, "err", err)
		}
	}
}
