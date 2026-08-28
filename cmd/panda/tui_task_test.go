package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestTaskProgressAdvanceDedups checks the trail records each distinct stage once
// — a chatty executor repeating the same milestone must not pad the card.
func TestTaskProgressAdvanceDedups(t *testing.T) {
	t0 := time.Now()
	tp := newTaskProgress("Explain PPO", t0)
	if tp.title != "Explain PPO" {
		t.Fatalf("title = %q", tp.title)
	}
	tp.advance("routing to node-a", t0.Add(1*time.Second))
	tp.advance("routing to node-a", t0.Add(2*time.Second)) // duplicate, dropped
	tp.advance("", t0.Add(3*time.Second))                  // empty, dropped
	tp.advance("running claude_code", t0.Add(4*time.Second))
	if len(tp.stages) != 2 {
		t.Fatalf("expected 2 stages, got %d: %+v", len(tp.stages), tp.stages)
	}
	if tp.stages[1].label != "running claude_code" {
		t.Fatalf("stage[1] = %q", tp.stages[1].label)
	}
}

// TestTaskProgressTrail verifies each committed stage line carries the time that
// stage took: the gap to the next milestone, or the remaining total for the last.
func TestTaskProgressTrail(t *testing.T) {
	t0 := time.Now()
	tp := newTaskProgress("build", t0)
	tp.advance("routed", t0.Add(2*time.Second))
	tp.advance("running", t0.Add(5*time.Second))

	lines := tp.trail(20 * time.Second)
	if len(lines) != 2 {
		t.Fatalf("expected 2 trail lines, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "routed") || !strings.Contains(lines[0], "3s") {
		t.Errorf("first line should be routed for 3s, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "running") || !strings.Contains(lines[1], "15s") {
		t.Errorf("second line should be running for 15s, got %q", lines[1])
	}

	// A nil card and an unstarted card both render an empty trail.
	var nilTP *taskProgress
	if got := nilTP.trail(time.Second); got != nil {
		t.Errorf("nil trail = %v, want nil", got)
	}
	if got := newTaskProgress("x", t0).trail(time.Second); got != nil {
		t.Errorf("empty trail = %v, want nil", got)
	}
}

// TestTaskProgressRenderLive confirms the live card names the task, lists its
// stages, and always shows something moving even before the first milestone.
func TestTaskProgressRenderLive(t *testing.T) {
	th := newTheme(i18n.Locale("en"))
	t0 := time.Now()
	tp := newTaskProgress("Explain PPO", t0)

	// Before any milestone: the spinner sits on a "submitting" arm, never inert.
	out := tp.renderLive(th, "en", "SPIN", t0.Add(time.Second))
	if !strings.Contains(out, "Explain PPO") {
		t.Errorf("card should name the task: %q", out)
	}
	if !strings.Contains(out, "SPIN") {
		t.Errorf("card should carry the spinner before the first stage: %q", out)
	}

	// With stages: earlier ones are ticked, the last one holds the spinner.
	tp.advance("routed to node-a", t0.Add(1*time.Second))
	tp.advance("running claude_code", t0.Add(3*time.Second))
	out = tp.renderLive(th, "en", "SPIN", t0.Add(10*time.Second))
	if !strings.Contains(out, "routed to node-a") || !strings.Contains(out, "running claude_code") {
		t.Errorf("card should list both stages: %q", out)
	}
	if strings.Count(out, "SPIN") != 1 {
		t.Errorf("exactly the current stage should spin: %q", out)
	}
}

// TestOnProgressOpensAndAdvancesCard drives the engine-event path: a task event
// opens the card, the lifecycle milestones that follow extend its trail, and a
// plain answer turn never opens one.
func TestOnProgressOpensAndAdvancesCard(t *testing.T) {
	m := newTestTUI(t)

	// A tool event on a turn with no task must not fabricate a card.
	next, _ := m.onProgress(askengine.Progress{Kind: askengine.ProgressTool, Name: "grep"})
	m = next.(tuiModel)
	if m.liveTask != nil {
		t.Fatal("a tool event alone should not open a task card")
	}

	next, _ = m.onProgress(askengine.Progress{Kind: askengine.ProgressTask, Name: "Explain PPO"})
	m = next.(tuiModel)
	if m.liveTask == nil {
		t.Fatal("a task event should open the card")
	}
	if m.liveTask.title != "Explain PPO" {
		t.Fatalf("card title = %q", m.liveTask.title)
	}

	next, _ = m.onProgress(askengine.Progress{Kind: askengine.ProgressRoute, Name: "node-a"})
	m = next.(tuiModel)
	next, _ = m.onProgress(askengine.Progress{Kind: askengine.ProgressExec, Name: "claude_code"})
	m = next.(tuiModel)
	if len(m.liveTask.stages) != 2 {
		t.Fatalf("route+exec should add 2 stages, got %d", len(m.liveTask.stages))
	}
	if m.note == "" {
		t.Error("the note should still track the latest event for the status line")
	}
}

// TestCommitAttachesTaskTrail verifies a delegated turn's committed block keeps
// the card's title and stage trail, so scrollback records the whole run.
func TestCommitAttachesTaskTrail(t *testing.T) {
	m := newTestTUI(t)
	next, _ := m.onProgress(askengine.Progress{Kind: askengine.ProgressTask, Name: "build docs"})
	m = next.(tuiModel)
	next, _ = m.onProgress(askengine.Progress{Kind: askengine.ProgressExec, Name: "claude_code"})
	m = next.(tuiModel)

	// Drop the repl so commit skips conversation persistence: this test is about
	// the block it renders, not about writing the user's convo file.
	m.r = nil

	out := &askengine.Result{Kind: "task", OK: true, Stdout: "done\n"}
	next, cmd := m.commit(out)
	nm := next.(tuiModel)
	if nm.liveTask != nil {
		t.Error("commit should clear the live card")
	}
	if cmd == nil {
		t.Fatal("commit should print the task block")
	}
	printed := printedText(cmd)
	if !strings.Contains(printed, "build docs") {
		t.Errorf("committed block should name the task: %q", printed)
	}
	if !strings.Contains(printed, "claude_code") {
		t.Errorf("committed block should keep the stage trail: %q", printed)
	}
	if !strings.Contains(printed, "done") {
		t.Errorf("committed block should keep the output: %q", printed)
	}
}

// printedText runs a tea.Println command and returns the text it would print.
// tea.Println wraps the body in an unexported message type, so the text is read
// back through its default format — enough for these assertions.
func printedText(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", cmd()))
}
