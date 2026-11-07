// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

// Guard rails for the tool-classification loop.
//
// The loop had a round ceiling (maxRounds) but no input ceiling: every tool
// result was replayed verbatim in every later classification, so a long
// exploration grew the request until the provider rejected it. That rejection
// then took the generic "stopped responding after N tool operations" exit,
// which reported a symptom and hid the cause. These helpers give the loop a
// bounded input, a targeted recovery for context-overflow rejections, visible
// failure reasons, and a last-resort containment for panics escaping a tool
// adapter or a stream callback.

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// maxOverflowRetries bounds the compress-and-retry ladder. Each step halves
// the tool-result budget, so a retried request is strictly smaller than the
// one the provider rejected. Two steps are enough to go from the full window
// budget down to the one-line folded markers.
const maxOverflowRetries = 2

// logWarn/logError keep a nil logger from turning a diagnostic into a second
// crash. Tests construct Engine literals directly; production always supplies
// one, but the guard paths must be safe in both.
func (e *Engine) logWarn(msg string, args ...any) {
	if e.logger != nil {
		e.logger.Warn(msg, args...)
	}
}

func (e *Engine) logError(msg string, args ...any) {
	if e.logger != nil {
		e.logger.Error(msg, args...)
	}
}

// classifyBounded runs one classification over a bounded transcript. Tool
// results are the only input the ask loop grows without limit, so they are
// trimmed to a share of the serving model's advertised context window before
// every call; user and assistant prose is never touched.
//
// A provider that still rejects the request as too large gets further attempts
// on the SAME model with a tighter budget. Failing over to another model would
// reproduce the fault on a possibly smaller window, and taking the generic
// digest exit would hide it. Non-overflow errors pass through untouched for
// the loop's existing fatal/fallback handling. The returned turns carry the
// trimming forward, so a later fallback model reuses the smaller transcript.
func (e *Engine) classifyBounded(ctx context.Context, c *entry.Client, turns []entry.Turn, run func([]entry.Turn) (entry.Output, error)) (entry.Output, []entry.Turn, error) {
	window := 0
	if c != nil {
		window = c.ContextWindow()
	}
	budget := turnBudgetForWindow(window)
	var out entry.Output
	var err error
	for attempt := 0; ; attempt++ {
		var freed int
		turns, freed = fitToolTurns(turns, budget)
		if freed > 0 {
			e.logWarn("askengine: bounded tool results to fit context window",
				"window", window, "budget_runes", budget, "freed_runes", freed,
				"turns", len(turns), "attempt", attempt)
		}
		out, err = run(turns)
		if err == nil || ctx.Err() != nil || !entry.IsContextOverflowError(err) {
			return out, turns, err
		}
		if attempt >= maxOverflowRetries {
			return out, turns, err
		}
		e.logError("askengine: context overflow, compressing tool results and retrying",
			"err", err, "window", window, "tool_result_runes", toolResultRunes(turns),
			"attempt", attempt+1, "next_budget_runes", budget/2)
		budget /= 2
	}
}

// loopFailureReason turns an otherwise swallowed model error into one line the
// user can act on. The digest says what ran; this says why the loop stopped.
// Errors are bounded by runes so a provider that echoes the whole request back
// cannot replace the answer with its own body.
func loopFailureReason(err error, loc i18n.Locale) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	if msg == "" {
		return ""
	}
	if loc == i18n.English {
		return "Reason: " + msg
	}
	return "中断原因：" + msg
}

// loopInterruptionNotice is the short, protocol-free line appended to a tool
// digest when the model call failed. The digest itself must stay readable:
// raw DSML/parse errors belong in Warning and the structured log, not in the
// user-facing summary of what already ran.
func loopInterruptionNotice(loc i18n.Locale) string {
	if loc == i18n.English {
		return "\n\n(The model call failed; the detailed reason is in the log.)"
	}
	return "\n\n（模型调用失败中断，详细原因见日志。）"
}

// emptyAnswerNotice is the last-resort text for a converged answer that came
// back blank. A 200-OK with an empty content field must never look like the
// assistant simply had nothing to say.
func emptyAnswerNotice(loc i18n.Locale) string {
	if loc == i18n.English {
		return "The model returned no text for this turn. Check the model connection and retry."
	}
	return "模型本轮未返回文本内容，请检查模型连接后重试。"
}

// containedPanicResult is the ask pipeline's last line of defence. A panic in
// a tool adapter, a stream callback or a parsed payload must end this one ask
// with a readable error, not take down the CLI command or the daemon. The stack
// goes to the structured log rather than to the user.
func (e *Engine) containedPanicResult(r any) (*Result, error) {
	e.logError("askengine: contained panic in ask pipeline",
		"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
	return nil, fmt.Errorf("askengine: internal error (contained): %v", r)
}
