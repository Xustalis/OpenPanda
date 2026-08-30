package main

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/i18n"
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
	if !strings.Contains(b.body, "panda task show t-1") {
		t.Fatalf("raw-output pointer missing: %q", b.body)
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
