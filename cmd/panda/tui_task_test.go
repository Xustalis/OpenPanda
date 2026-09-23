//go:build !lite

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
	tp := newTaskProgress("Explain PPO", t0, i18n.English)
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
	tp := newTaskProgress("build", t0, i18n.English)
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
	if got := newTaskProgress("x", t0, i18n.English).trail(time.Second); got != nil {
		t.Errorf("empty trail = %v, want nil", got)
	}
}

// TestTaskProgressRenderLive confirms the live card names the task, lists its
// stages, and always shows something moving even before the first milestone.
func TestTaskProgressRenderLive(t *testing.T) {
	th := newTheme(i18n.Locale("en"))
	t0 := time.Now()
	tp := newTaskProgress("Explain PPO", t0, i18n.English)

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

	stream := newTestStream(new(bool))
	m.stream = stream

	// A tool event on a turn with no task must not fabricate a card.
	next, _ := m.onProgress(progressMsg{stream: stream, progress: askengine.Progress{Kind: askengine.ProgressTool, Name: "grep"}})
	m = next.(tuiModel)
	if m.liveTask != nil {
		t.Fatal("a tool event alone should not open a task card")
	}

	next, _ = m.onProgress(progressMsg{stream: stream, progress: askengine.Progress{Kind: askengine.ProgressTask, Name: "Explain PPO"}})
	m = next.(tuiModel)
	if m.liveTask == nil {
		t.Fatal("a task event should open the card")
	}
	if m.liveTask.title != "Explain PPO" {
		t.Fatalf("card title = %q", m.liveTask.title)
	}

	next, _ = m.onProgress(progressMsg{stream: stream, progress: askengine.Progress{Kind: askengine.ProgressRoute, Name: "node-a"}})
	m = next.(tuiModel)
	next, _ = m.onProgress(progressMsg{stream: stream, progress: askengine.Progress{Kind: askengine.ProgressExec, Name: "claude_code"}})
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
	stream := newTestStream(new(bool))
	m.stream = stream
	next, _ := m.onProgress(progressMsg{stream: stream, progress: askengine.Progress{Kind: askengine.ProgressTask, Name: "build docs"}})
	m = next.(tuiModel)
	next, _ = m.onProgress(progressMsg{stream: stream, progress: askengine.Progress{Kind: askengine.ProgressExec, Name: "claude_code"}})
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
	// When no summary was generated, the cleaned agent output reaches the committed block.
	if !strings.Contains(printed, "done") {
		t.Errorf("cleaned output should reach the committed block: %q", printed)
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

func TestAnswerTextMarkdown(t *testing.T) {
	th := newTheme(i18n.Locale("en"))
	th.color = true
	in := "# Heading\n\nSome **bold** prose.\n\n| ColA | ColB |\n|---|---|\n| 1 | 2 |"
	out := answerText(th, in, 80)
	if !strings.Contains(out, "Heading") || !strings.Contains(out, "bold") || !strings.Contains(out, "ColA") {
		t.Fatalf("answerText did not render markdown: %q", out)
	}
	if !strings.HasPrefix(out, th.accent.Render(th.glyph("⏺", "*"))+" ") {
		t.Fatalf("answerText lost its marker: %q", out)
	}
}

func TestRenderTaskMarkdown(t *testing.T) {
	th := newTheme(i18n.Locale("en"))
	th.color = true
	blk := block{
		kind:  blockTask,
		ok:    true,
		title: "test task",
		body:  "## Summary\n\n| Item | Count |\n|---|---|\n| Files | 42 |",
	}
	out := blk.render(th, 80, false)
	if !strings.Contains(out, "Summary") || !strings.Contains(out, "Files") || !strings.Contains(out, "42") {
		t.Fatalf("renderTask did not render markdown table: %q", out)
	}
}

// TestTaskProgressCompressesTools confirms that micro-tool steps do not bloat
// stages list into dozens of lines, but are aggregated into an operation count
// while showing the single active tool dynamically.
func TestTaskProgressCompressesTools(t *testing.T) {
	th := newTheme(i18n.Locale("zh-CN"))
	t0 := time.Now()
	tp := newTaskProgress("Deep Analysis", t0, i18n.ChineseSimp)

	// Advance milestone 1: route
	tp.advance("路由至 本地节点", t0.Add(1*time.Second))
	// Advance milestone 2: exec agent
	tp.advance("正在运行 claude_code...", t0.Add(2*time.Second))

	if len(tp.stages) != 2 {
		t.Fatalf("expected 2 milestone stages, got %d", len(tp.stages))
	}

	// Receive 15 micro tool events
	for i := 1; i <= 15; i++ {
		tp.recordTool(fmt.Sprintf("Bash: grep -rn item_%d", i), t0.Add(time.Duration(2+i)*time.Second))
	}

	// Stages count must STAY at 2! Micro tools do not create separate stages!
	if len(tp.stages) != 2 {
		t.Fatalf("micro tools must not inflate stages slice, got %d", len(tp.stages))
	}
	if tp.curTools != 15 {
		t.Fatalf("expected 15 curTools, got %d", tp.curTools)
	}

	// Live rendering should show operation count and current active tool
	live := tp.renderLive(th, "zh-CN", "SPIN", t0.Add(20*time.Second))
	if !strings.Contains(live, "15 项操作") {
		t.Errorf("live render should show (15 ops): %q", live)
	}
	if !strings.Contains(live, "当前操作: Bash: grep -rn item_15") {
		t.Errorf("live render should show active tool: %q", live)
	}

	// Advance to judge
	tp.advance("正在评审执行结果...", t0.Add(21*time.Second))
	if len(tp.stages) != 3 {
		t.Fatalf("expected 3 milestone stages after judge, got %d", len(tp.stages))
	}
	// Stage 1 (exec) must have sealed toolCount=15
	if tp.stages[1].toolCount != 15 {
		t.Fatalf("exec stage should have sealed 15 tools, got %d", tp.stages[1].toolCount)
	}

	// Trail should show compact summary with (15 ops)
	trail := tp.trail(25 * time.Second)
	if len(trail) != 3 {
		t.Fatalf("trail should contain exactly 3 lines, got %d: %v", len(trail), trail)
	}
	if !strings.Contains(trail[1], "15 项操作") {
		t.Errorf("trail exec stage should carry (15 ops): %q", trail[1])
	}
}

// TestIsLeakedEscapeFragment verifies detection of leaked terminal SGR mouse and
// escape codes without false-positive matching on regular text.
func TestIsLeakedEscapeFragment(t *testing.T) {
	leakedCases := []string{
		";20M",
		"[<65;123;20M",
		"<65;123;20M",
		"65;123;20M",
		"[<65;123;20M[<65;123;20M",
		";123;20M",
		";20m",
		"[<35;10;5m",
		"[<35;10",
		"[<",
		"<35;10;5m<35;10;5m",
		"<35;10",
		"<35;",
		"[24;80R",
		"24;80R",
		";80R",
		"[?1002h",
		"[?1006l",
		"?1002h",
		"?1007h",
		"?1;2c",
		"[I",
		"[O",
		"[200~",
		"[201~",
		"[1;2A",
		"[1;5C",
		"[1~",
		"[3~",
		"[8;24;80t",
		"]11;rgb:0000/0000/0000",
	}
	for _, c := range leakedCases {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(c)}
		if !isLeakedEscapeFragment(msg) {
			t.Errorf("isLeakedEscapeFragment(%q) = false, want true", c)
		}
	}

	altLeakedRunes := []rune{'[', 'O', ']', '?', '<', ';', '~', '\x1b'}
	for _, r := range altLeakedRunes {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
		if !isLeakedEscapeFragment(msg) {
			t.Errorf("isLeakedEscapeFragment(Alt+%q) = false, want true", string(r))
		}
	}

	validCases := []string{
		"a",
		"hello",
		"10m",
		"5MB",
		"500M",
		"git commit -m \"fix\"",
		"你好世界",
		"cd /Users/xenith",
		"make build",
		"if x < 10 { y = 20; }",
		"cat < file.txt",
		"<tag>hello</tag>",
	}
	for _, c := range validCases {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(c)}
		if isLeakedEscapeFragment(msg) {
			t.Errorf("isLeakedEscapeFragment(%q) = true, want false (false positive)", c)
		}
	}
}
