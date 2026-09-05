package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/charmbracelet/lipgloss"
)

// loc is the locale the block tests render in. It only matters for keys with
// interpolations, but passing it keeps these call sites honest about the real
// signature.
const loc = i18n.Locale("en")

// TestAppendReasoning pins the fold from streamed reasoning chunks to whole
// lines: a chunk without a newline extends the current line (chain-of-thought
// arrives token by token), a chunk with newlines opens new lines. This is what
// makes the live thought preview and the collapsible block read as prose rather
// than a jitter of fragments.
func TestAppendReasoning(t *testing.T) {
	var lines []string
	lines = appendReasoning(lines, "Let me ")
	lines = appendReasoning(lines, "think.")
	if got := strings.Join(lines, "|"); got != "Let me think." {
		t.Fatalf("mid-line join: got %q", got)
	}
	lines = appendReasoning(lines, "\nStep two")
	lines = appendReasoning(lines, " continues")
	if got := strings.Join(lines, "|"); got != "Let me think.|Step two continues" {
		t.Fatalf("newline split: got %q", got)
	}
	// An empty chunk is a no-op, not a spurious blank line.
	if got := appendReasoning(lines, ""); len(got) != 2 {
		t.Fatalf("empty chunk changed line count: %d", len(got))
	}
}

// TestResultBlockAnswer verifies an answer block prefers the streamed text
// (what the user actually saw) and falls back to Result.Answer, and that an
// incidental Note is kept above the answer.
func TestResultBlockAnswer(t *testing.T) {
	b := resultBlock(&askengine.Result{Kind: "answer", Answer: "fallback"}, "streamed", loc)
	if b.kind != blockAnswer || b.body != "streamed" {
		t.Fatalf("streamed preferred: kind=%v body=%q", b.kind, b.body)
	}
	b = resultBlock(&askengine.Result{Kind: "answer", Answer: "fallback"}, "   ", loc)
	if b.body != "fallback" {
		t.Fatalf("empty stream should fall back: body=%q", b.body)
	}
	b = resultBlock(&askengine.Result{Kind: "answer", Answer: "a", Note: "heads up"}, "", loc)
	if !strings.HasPrefix(b.body, "heads up\n") {
		t.Fatalf("note should lead the body: %q", b.body)
	}
}

func TestResultBlockCostMeta(t *testing.T) {
	res := &askengine.Result{
		Kind:         "answer",
		Answer:       "hello",
		InputTokens:  1000,
		OutputTokens: 200,
		Latency:      1500 * time.Millisecond,
		Cost:         0.0035,
	}
	b := resultBlock(res, "", loc)
	if !strings.Contains(b.meta, "1.5s") || !strings.Contains(b.meta, "1.2k tokens") || !strings.Contains(b.meta, "($0.0035)") {
		t.Fatalf("expected b.meta to contain latency, tokens and cost, got %q", b.meta)
	}
}

// TestResultBlockTask distinguishes a succeeded task (its stdout, success tint)
// from a failed one (exit code + stderr).
func TestResultBlockTask(t *testing.T) {
	ok := resultBlock(&askengine.Result{Kind: "task", OK: true, Stdout: "done\n"}, "", loc)
	if ok.kind != blockTask || !ok.ok || ok.body != "done" {
		t.Fatalf("ok task: %+v", ok)
	}
	bad := resultBlock(&askengine.Result{Kind: "task", OK: false, ExitCode: 2, Stderr: "boom"}, "", loc)
	if bad.kind != blockTask || bad.ok || !strings.Contains(bad.body, "exit 2") || !strings.Contains(bad.body, "boom") {
		t.Fatalf("bad task: %+v", bad)
	}
}

// TestResultBlockTaskReport pins the sub-agent round's committed body: the
// converged report is the reply, and the raw agent output is demoted to a
// pointer line that names the task — never the body itself.
func TestResultBlockTaskReport(t *testing.T) {
	out := &askengine.Result{
		Kind: "task", OK: true, TaskID: "t-1", TaskState: "done",
		Answer: "构建通过，两处测试已补", Stdout: "wall of agent log",
	}
	b := resultBlock(out, "", loc)
	if b.kind != blockTask || !b.ok {
		t.Fatalf("kind/ok: %+v", b)
	}
	if !strings.HasPrefix(b.body, "构建通过") {
		t.Fatalf("report should lead the body: %q", b.body)
	}
	if strings.Contains(b.body, "wall of agent log") {
		t.Fatalf("raw output must not be the body: %q", b.body)
	}
	if !strings.Contains(b.meta, "panda task show t-1") {
		t.Fatalf("raw-output pointer missing from meta: %q", b.meta)
	}

	// A failed round keeps its exit evidence alongside the report.
	out.OK = false
	out.ExitCode = 2
	out.Stderr = "boom"
	b = resultBlock(out, "", loc)
	if !strings.Contains(b.body, "构建通过") || !strings.Contains(b.body, "exit 2") {
		t.Fatalf("failed round body: %q", b.body)
	}
}

// TestResultBlockExecutionAttribution verifies that a completed task block
// retains and renders its executor agent, model, and injection status.
func TestResultBlockExecutionAttribution(t *testing.T) {
	out := &askengine.Result{
		Kind:      "task",
		OK:        true,
		TaskID:    "t-attr",
		TaskState: "done",
		Agent:     "claude_code",
		Model:     "deepseek-v4-flash",
		Injected:  true,
		Answer:    "已抓取到最新要闻",
	}
	b := resultBlock(out, "", i18n.ChineseSimp)
	if b.agent != "claude_code" || b.model != "deepseek-v4-flash" || !b.injected {
		t.Fatalf("resultBlock did not capture execution attribution: %+v", b)
	}
	rendered := b.render(newTheme(i18n.ChineseSimp), 80, false)
	if !strings.Contains(rendered, "claude_code (deepseek-v4-flash)") {
		t.Fatalf("rendered block should contain agent and model: %q", rendered)
	}
	if !strings.Contains(rendered, "模型能力已注入") {
		t.Fatalf("rendered block should indicate model injection: %q", rendered)
	}
}

// TestResultBlockPlanFailed pins the failure path: a plan that never started
// carries an empty id and no stages, so rendering it through the success line
// would announce "plan  · 0 stages" — a failure dressed as a success. It has to
// come back as an error block instead.
func TestResultBlockPlanFailed(t *testing.T) {
	bad := resultBlock(&askengine.Result{Kind: "plan", Stderr: "no card", ExitCode: 1}, "", loc)
	if bad.kind != blockError {
		t.Fatalf("kind: got %v want blockError", bad.kind)
	}
	if !strings.Contains(bad.body, "no card") {
		t.Fatalf("the reason belongs in the block: %q", bad.body)
	}
	// The success path still summarises the board that was actually queued.
	good := resultBlock(&askengine.Result{Kind: "plan", OK: true, PlanID: "p1", PlanGoal: "ship it"}, "", loc)
	if good.kind != blockInfo || !strings.Contains(good.body, "p1") {
		t.Fatalf("ok plan: kind=%v body=%q", good.kind, good.body)
	}
}

// TestThoughtFoldDoesNotPromiseAnUnavailableKey pins the inline-mode constraint.
// A committed block has already been written into the terminal's scrollback, so
// it can never be redrawn — advertising ctrl+o on it would point at a key that
// cannot touch it. The fold reports how much is hidden instead, which is a fact
// about this block rather than a promise about a future one.
func TestThoughtFoldDoesNotPromiseAnUnavailableKey(t *testing.T) {
	b := block{kind: blockThought, thoughtLines: []string{"first", "second", "third"}}

	folded := b.render(theme{loc: loc}, 80, false)
	if strings.Contains(folded, "ctrl+o") {
		t.Fatalf("the fold must not point at a key that cannot redraw it: %q", folded)
	}
	if !strings.Contains(folded, "3 lines") {
		t.Fatalf("the fold should say how much is hidden: %q", folded)
	}
	if !strings.Contains(folded, "first") {
		t.Fatalf("the fold should keep a teaser of the reasoning: %q", folded)
	}

	expanded := b.render(theme{loc: loc}, 80, true)
	if strings.Contains(expanded, "3 lines") {
		t.Fatalf("an expanded thought should not report a fold: %q", expanded)
	}
	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded thought missing %q: %q", want, expanded)
		}
	}
}

// TestTruncate checks the rune-aware clip used by the folded thought summary.
func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Fatalf("no clip: %q", got)
	}
	if got := truncate("hello world", 8); got != "hello w…" {
		t.Fatalf("clip: %q", got)
	}
}

// TestIndentLines confirms the subtree indentation: the first line gets the arm
// glyph prefix, continuations align under it.
func TestIndentLines(t *testing.T) {
	got := indentLines("one\ntwo", ">> ", "   ")
	if got != ">> one\n   two" {
		t.Fatalf("indent: %q", got)
	}
}

// TestUserTextVisualBlockAndContrast pins the user prompt UX fix: user input
// must hang continuation lines under the ❯ marker, hold high contrast (bold/white,
// never dim/faint), and render inside a dedicated user block band when colored.
func TestUserTextVisualBlockAndContrast(t *testing.T) {
	th := newTheme(loc)
	out := userText(th, "first line\nsecond line", 40)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out)
	}
	marker := th.glyph("❯", ">")
	if !strings.HasPrefix(lines[0], marker) {
		t.Errorf("first line should start with marker %q: %q", marker, lines[0])
	}
	if !strings.HasPrefix(lines[1], "  ") {
		t.Errorf("second line should hang with 2 spaces indent: %q", lines[1])
	}

	// Test wrapping
	wrapped := userText(th, "alpha beta gamma delta epsilon zeta eta theta iota kappa", 20)
	wlines := strings.Split(wrapped, "\n")
	if len(wlines) < 2 {
		t.Fatalf("expected long line to wrap: %q", wrapped)
	}
	for i, l := range wlines {
		if i == 0 && !strings.HasPrefix(l, marker) {
			t.Errorf("wrapped line 0 should start with marker: %q", l)
		}
		if i > 0 && !strings.HasPrefix(l, "  ") {
			t.Errorf("wrapped continuation line %d should hang under marker: %q", i, l)
		}
	}

	// In color mode, user block is rendered inside userPanel with padding and background
	thColor := newTheme(loc)
	thColor.color = true
	thColor.userPrompt = thColor.userPrompt.Foreground(lipgloss.Color("#FFFFFF"))
	thColor.userMarker = thColor.userMarker.Foreground(lipgloss.Color("6"))
	thColor.userPanel = thColor.userPanel.Background(lipgloss.Color("236")).Padding(0, 1)
	colorOut := userText(thColor, "color prompt", 30)
	if !strings.Contains(colorOut, "color prompt") {
		t.Fatalf("color output missing prompt text: %q", colorOut)
	}
}
