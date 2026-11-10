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
	"log/slog"
	"os"
	"runtime/debug"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// stderrLogger is the last-resort sink for guard-rail diagnostics. Engines
// built by tests (and the borrowed web engine, which has no logger wired to
// its structured log) otherwise drop every "model call failed" record, while
// the reply tells the user to "check the log". The web process redirects
// stderr into web.log, so writing JSON here keeps that promise true.
var stderrLogger = slog.New(slog.NewJSONHandler(os.Stderr, nil))

// maxOverflowRetries bounds the compress-and-retry ladder. Each step halves
// the tool-result budget, so a retried request is strictly smaller than the
// one the provider rejected. Two steps are enough to go from the full window
// budget down to the one-line folded markers.
const maxOverflowRetries = 2

// logWarn/logError keep a nil logger from turning a diagnostic into a second
// crash. Tests construct Engine literals directly and some production paths
// (notably a borrowed web engine) never wire e.logger, so they fall back to
// stderr rather than silently losing the one line that explains an aborted
// turn. A test engine stays safe: the fallback never panics and never blocks.
func (e *Engine) guardLogger() *slog.Logger {
	if e.logger != nil {
		return e.logger
	}
	return stderrLogger
}

func (e *Engine) logWarn(msg string, args ...any) {
	e.guardLogger().Warn(msg, args...)
}

func (e *Engine) logError(msg string, args ...any) {
	e.guardLogger().Error(msg, args...)
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
		return "\n\n(The model call failed, so this reply is incomplete; the reason is recorded in the run log and in this turn's warning. Please retry.)"
	}
	return "\n\n（模型调用失败中断，本次回答未完成；具体原因已记入运行日志和本轮警告信息，请重试。）"
}

// convergenceNudge reports the approaching budget boundary, not evidence
// sufficiency. Broad investigations need a harness handoff; other requests
// should report only what their bounded observations can substantiate.
func convergenceNudge(loc i18n.Locale, investigation bool) string {
	if investigation {
		return investigationRouteReminder(loc)
	}
	if loc == i18n.English {
		return "The tool-round limit is near; stop broad exploration. Answer only what the evidence actually supports, and clearly mark any unverified or incomplete claim."
	}
	return "工具轮次接近上限，请停止宽泛探查。只根据已核实的证据作答，明确标注未验证或未完成的部分，不要假定已经收集到足够上下文。"
}

const (
	// compactConvergence keeps only a bounded slice of each tool result: the
	// point is to leave the model enough evidence to write an answer, not to
	// replay the whole exploration that just overflowed the provider.
	compactPerToolRunes = 500
	compactTotalRunes   = 6000
	compactMaxTools     = 16
)

// compactConvergenceTranscript rebuilds a minimal tool-free transcript for a
// last-chance answer. The full history (every tool call and its full output)
// is what failed the final convergence call; re-sending it unchanged can only
// reproduce the failure. The user's question plus an excerpted inventory of
// what actually ran fits comfortably inside any serving window.
func compactConvergenceTranscript(prompt string, toolDigest []string, loc i18n.Locale) []entry.Turn {
	var b strings.Builder
	if loc == i18n.English {
		b.WriteString("Now answer the question above in prose. Do not call tools and do not ask to read more files; note any unverified detail briefly and give the best answer the evidence supports.\n\nObservations already collected (excerpted):\n")
	} else {
		b.WriteString("现在请直接回答上面的问题，用正文输出：不要调用任何工具、不要再要求读取更多文件。未核实的细节简要说明，基于已有证据给出尽可能完整的答案。\n\n已执行的探查与结果节选：\n")
	}
	used, shown := 0, 0
	for _, line := range toolDigest {
		if shown >= compactMaxTools || used >= compactTotalRunes {
			break
		}
		take := compactPerToolRunes
		if used+take > compactTotalRunes {
			take = compactTotalRunes - used
		}
		runes := []rune(strings.TrimSpace(line))
		if len(runes) > take {
			runes = append(runes[:take], '…')
		}
		b.WriteString("- ")
		b.WriteString(string(runes))
		b.WriteByte('\n')
		used += take
		shown++
	}
	if len(toolDigest) > shown {
		if loc == i18n.English {
			fmt.Fprintf(&b, "- ... and %d more tool result(s) omitted.\n", len(toolDigest)-shown)
		} else {
			fmt.Fprintf(&b, "- ……另有 %d 项结果未展开。\n", len(toolDigest)-shown)
		}
	}
	return []entry.Turn{
		{Role: "user", Content: prompt},
		{Role: "user", Content: b.String()},
	}
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
