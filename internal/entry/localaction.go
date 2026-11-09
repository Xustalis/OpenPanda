// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"regexp"
	"strings"
)

// LocalAction is a whole-message imperative that this node can satisfy with
// one of its own subcommands — no entry-model call, no task row, no agent
// subprocess. It exists because "open the web console" was costing two model
// calls and a delegation to an agent CLI to do something the binary already
// does deterministically.
//
// The set is deliberately tiny. Every entry must be (a) idempotent-ish, (b)
// bounded in time, and (c) answerable from this node alone; anything that
// edits a tree, installs software or talks to a third party stays a task.
type LocalAction struct {
	// ID names the action for tests and telemetry.
	ID string
	// Argv is what to run, excluding the binary itself.
	Argv []string
	// ExitStatusIsReport marks an action whose non-zero exit is content, not
	// failure: `doctor` exits non-zero precisely when it found something, and
	// that finding is the answer. Without this the reply would be discarded
	// and the same question re-sent to a model, which is both slower and less
	// accurate than the diagnosis that was already computed.
	ExitStatusIsReport bool
}

var spacesRE = regexp.MustCompile(`\s+`)

// localActionTable maps a normalized imperative to its argv. Patterns are
// anchored and matched against the normalized message (see
// normalizeActionText), so a longer sentence — "open the console and change
// the port to 9000" — never matches and stays on the normal path.
var localActionTable = []struct {
	action LocalAction
	re     *regexp.Regexp
}{
	// The console. `--daemon` is what makes this bounded: the child detaches
	// and the call returns with the URL instead of the ask blocking on a
	// foreground server that only Ctrl-C ends.
	{
		action: LocalAction{ID: "web", Argv: []string{"web", "--daemon"}},
		re: regexp.MustCompile(`^(?:打开|启动|开启|运行|起)(?:一下)?(?:本机|这个)?(?:的)?\s*(?:web)?\s*(?:控制台|面板|管理界面|界面|网页版)$` +
			`|^(?:open|start|launch|bring up)(?: the)?(?: web)?(?: console| dashboard| panel| ui)$`),
	},
	// "start the web service" / "启动 web 服务" — the phrasing that prompted
	// this path. It is the same console: this node serves it.
	{
		action: LocalAction{ID: "web", Argv: []string{"web", "--daemon"}},
		re: regexp.MustCompile(`^(?:打开|启动|开启|运行|起)(?:一下)?(?:本机|这个)?(?:的)?\s*web\s*(?:服务|服务器)$` +
			`|^(?:open|start|launch|serve)(?: up)?(?: the)?(?: web)?(?: service| server)$`),
	},
	// Self-diagnostics and node status: both read-only, both bounded.
	// Self-diagnostics and node status: both read-only, both bounded, and both
	// report through their exit status — "3 checks failed" is the answer.
	{
		action: LocalAction{ID: "doctor", Argv: []string{"doctor"}, ExitStatusIsReport: true},
		re:     regexp.MustCompile(`^(?:跑一下|执行|运行|run)?(?:doctor|自检|体检|诊断)$`),
	},
	{
		action: LocalAction{ID: "status", Argv: []string{"status"}, ExitStatusIsReport: true},
		re:     regexp.MustCompile(`^(?:看|看看|查看|show|check)?(?:status|状态|运行状态|节点状态)$`),
	},
}

// questionMarkers reject anything that asks rather than instructs. A question
// about the console ("how do I open the web console?") must reach the model —
// answering it by starting a server is the worst possible reply.
var questionMarkers = []string{"?", "？", "怎么", "如何", "为什么", "为啥", "能不能", "可不可以", "how do", "how to", "how can", "what is", "why "}

// politePrefixes are stripped before matching so the same imperative with a
// courtesy wrapper still qualifies.
var politePrefixes = []string{
	"帮我直接", "请帮我", "帮我", "麻烦", "请", "给我", "直接",
	"please", "can you", "could you", "would you", "help me", "just",
}

// politeSuffixes are stripped for the same reason.
var politeSuffixes = []string{"一下", "吧", "谢谢", "thanks", "please"}

// MatchLocalAction reports whether the whole prompt is a bare imperative for
// one of this node's own subcommands. Callers must pass a fresh turn: a
// follow-up like "and the port to 9000" has no meaning on its own, and the
// match is a whole-message test precisely so it cannot hijack one.
func MatchLocalAction(prompt string) (LocalAction, bool) {
	text := normalizeActionText(prompt)
	if text == "" {
		return LocalAction{}, false
	}
	for _, marker := range questionMarkers {
		if strings.Contains(text, marker) {
			return LocalAction{}, false
		}
	}
	for _, row := range localActionTable {
		if row.re.MatchString(text) {
			return row.action, true
		}
	}
	return LocalAction{}, false
}

// normalizeActionText lowers, collapses whitespace and peels the courtesy
// wrappers, leaving the imperative core. Trailing sentence punctuation is
// dropped, but a question mark is not: it was already rejected above.
func normalizeActionText(prompt string) string {
	t := strings.ToLower(strings.TrimSpace(prompt))
	t = strings.TrimRight(t, "。.!！~,，;；")
	t = strings.TrimSpace(spacesRE.ReplaceAllString(t, " "))
	for changed := true; changed; {
		changed = false
		for _, p := range politePrefixes {
			if strings.HasPrefix(t, p) {
				t = strings.TrimSpace(strings.TrimPrefix(t, p))
				changed = true
			}
		}
		for _, s := range politeSuffixes {
			if strings.HasSuffix(t, s) {
				t = strings.TrimSpace(strings.TrimSuffix(t, s))
				t = strings.TrimRight(t, "。.!！~,，;；")
				changed = true
			}
		}
	}
	return strings.TrimSpace(t)
}
