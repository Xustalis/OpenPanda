// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/core"
)

// TestOrderSidebarPutsMovingWorkFirst is the rail's ordering: what is running,
// then what waits on a human, then the queue, then the stopped rows.
func TestOrderSidebarPutsMovingWorkFirst(t *testing.T) {
	got := orderSidebarTasks([]core.Task{
		{TaskID: "done", State: core.StateDone, CreatedAt: 400},
		{TaskID: "queued", State: core.StateQueued, CreatedAt: 300},
		{TaskID: "review", State: core.StateReview, CreatedAt: 200},
		{TaskID: "running", State: core.StateRunning, CreatedAt: 100},
	})
	want := []string{"running", "review", "queued", "done"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows", len(got))
	}
	for i, id := range want {
		if got[i].TaskID != id {
			t.Errorf("row %d = %s, want %s", i, got[i].TaskID, id)
		}
	}
}

// TestOrderSidebarNewestFirstWithinABucket keeps a burst of dispatches readable:
// the newest is the one the reader is watching.
func TestOrderSidebarNewestFirstWithinABucket(t *testing.T) {
	got := orderSidebarTasks([]core.Task{
		{TaskID: "old", State: core.StateRunning, CreatedAt: 100},
		{TaskID: "new", State: core.StateRunning, CreatedAt: 300},
	})
	if got[0].TaskID != "new" {
		t.Errorf("first row = %s, want the newest", got[0].TaskID)
	}
}

// TestOrderSidebarCapsTheList: the rail is the working set, not the archive.
func TestOrderSidebarCapsTheList(t *testing.T) {
	var tasks []core.Task
	for i := 0; i < sidebarTaskCap+15; i++ {
		tasks = append(tasks, core.Task{TaskID: "t", State: core.StateDone, CreatedAt: int64(i)})
	}
	if got := orderSidebarTasks(tasks); len(got) != sidebarTaskCap {
		t.Errorf("rows = %d, want the cap %d", len(got), sidebarTaskCap)
	}
}

// TestSidebarLinesRender checks the rail draws a title, a row per task, and the
// state word — the three things a row exists to say.
func TestSidebarLinesRender(t *testing.T) {
	m := newIsolatedTUI(t)
	m.sidebar.visible = true
	m.sidebar.tasks = []core.Task{
		{TaskID: "t1", Title: "生成构建报告", State: core.StateRunning},
		{TaskID: "t2", Title: "跑一遍测试", State: core.StateReview},
	}
	lines := m.sidebarLines(12, 30)
	if len(lines) != 12 {
		t.Fatalf("rows = %d, want the frame height", len(lines))
	}
	body := strings.Join(lines, "\n")
	for _, want := range []string{"Tasks", "生成构建报告", "跑一遍测试", "running", "needs you"} {
		if !strings.Contains(body, want) {
			t.Errorf("rail is missing %q:\n%s", want, body)
		}
	}
}

// TestSidebarLinesMarkTheSelection: the cursor has to be visible on a column
// that is otherwise a wall of grey.
func TestSidebarLinesMarkTheSelection(t *testing.T) {
	m := newIsolatedTUI(t)
	m.sidebar.visible = true
	m.sidebar.sel = 1
	m.sidebar.tasks = []core.Task{
		{TaskID: "t1", Title: "第一条", State: core.StateRunning},
		{TaskID: "t2", Title: "第二条", State: core.StateRunning},
	}
	lines := m.sidebarLines(12, 30)
	picked := 0
	for _, l := range lines {
		if strings.Contains(l, "▸") || strings.Contains(l, ">") {
			picked++
			if !strings.Contains(l, "第二条") {
				t.Errorf("the marker is on the wrong row: %q", l)
			}
		}
	}
	if picked != 1 {
		t.Errorf("selection markers = %d, want 1", picked)
	}
}

// TestSidebarEmptyState says something rather than showing a blank column.
func TestSidebarEmptyState(t *testing.T) {
	m := newIsolatedTUI(t)
	m.sidebar.visible = true
	body := strings.Join(m.sidebarLines(8, 30), "\n")
	if !strings.Contains(body, "no tasks yet") {
		t.Errorf("empty rail has no message:\n%s", body)
	}
}

// TestSidebarToggle opens and closes on Ctrl+B, in a busy mode as well as idle.
func TestSidebarToggle(t *testing.T) {
	m := newIsolatedTUI(t)
	next, _ := m.onKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(tuiModel)
	if !m.sidebar.visible {
		t.Fatal("Ctrl+B did not open the rail")
	}
	// It works while a turn is streaming — that is when a fan-out is worth
	// watching.
	m.mode = modeAsking
	next, _ = m.onKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	m = next.(tuiModel)
	if m.sidebar.visible {
		t.Error("Ctrl+B did not close the rail")
	}
}

// TestSidebarWidthFollowsTheTerminal: a narrow frame keeps every column for the
// transcript.
func TestSidebarWidthFollowsTheTerminal(t *testing.T) {
	m := newIsolatedTUI(t)
	m.sidebar.visible = true

	m.width = 60
	if got := m.sidebarWidth(); got != 0 {
		t.Errorf("width %d on a 60-column frame: the rail should stand down", got)
	}
	m.width = 120
	got := m.sidebarWidth()
	if got < sidebarMinWidth || got > sidebarMaxWidth {
		t.Errorf("rail width = %d, outside [%d,%d]", got, sidebarMinWidth, sidebarMaxWidth)
	}
	if tw := m.textWidth(); tw != 120-got-1 {
		t.Errorf("content width = %d, want %d", tw, 120-got-1)
	}
}

// TestScrollToBlockLandsTheBlockInView: the arithmetic that decides which
// content line ends up at the top of the viewport.
func TestScrollToBlockLandsTheBlockInView(t *testing.T) {
	m := newIsolatedTUI(t)
	m.layout = &transcriptLayout{
		blockStart: []int{0, 20, 60},
		content:    100,
		avail:      20,
	}
	m.scrollToBlock(2)
	// end = 60 - 20/3 + 20 = 74; offset = content - end = 26.
	if m.scrollOffset != 26 {
		t.Errorf("offset = %d, want 26", m.scrollOffset)
	}
	// A block at the top of the content clamps to the far end of the scroll.
	m.scrollToBlock(0)
	if m.scrollOffset < 0 || m.scrollOffset > 100-20 {
		t.Errorf("offset = %d, outside the scroll range", m.scrollOffset)
	}
}

// TestScrollToBlockIgnoresShortTranscripts: when everything fits there is
// nothing to do, and a non-zero offset would scroll a full screen away.
func TestScrollToBlockIgnoresShortTranscripts(t *testing.T) {
	m := newIsolatedTUI(t)
	m.layout = &transcriptLayout{blockStart: []int{0, 3}, content: 10, avail: 20}
	m.scrollToBlock(1)
	if m.scrollOffset != 0 {
		t.Errorf("offset = %d, want 0", m.scrollOffset)
	}
}

// TestSidebarLocateSaysWhenThereIsNoBlock: locating a task the transcript never
// reported must say so instead of moving the view to nowhere.
func TestSidebarLocateSaysWhenThereIsNoBlock(t *testing.T) {
	m := newIsolatedTUI(t)
	m.sidebar.visible = true
	m.sidebar.tasks = []core.Task{{TaskID: "t1", Title: "没有记录的任务", State: core.StateRunning}}

	next, cmd := m.locateSelectedTask()
	m = next.(tuiModel)
	if cmd == nil {
		t.Fatal("locate answered with no command")
	}
	if last := lastBlock(m); last.kind != blockNote || !strings.Contains(last.body, "没有记录的任务") {
		t.Errorf("expected an explanatory note, got %+v", last)
	}
	if m.scrollOffset != 0 {
		t.Errorf("the view moved to nowhere: offset = %d", m.scrollOffset)
	}
}

// TestLocateFindsTheTaskBlock is the positive path the previous matcher never
// had: it keyed on a block field nothing ever set, so Ctrl+G only ever found
// tasks whose id happened to appear in the body text.
func TestLocateFindsTheTaskBlock(t *testing.T) {
	m := newIsolatedTUI(t)
	m.sidebar.visible = true
	m.sidebar.tasks = []core.Task{{TaskID: "task-42", Title: "生成报告", State: core.StateDone}}
	m.chatHistory.blocks = []block{
		{kind: blockUser, body: "生成报告"},
		{kind: blockTask, body: "报告已生成", taskRef: "task-42"},
	}
	m.layout = &transcriptLayout{blockStart: []int{0, 5}, content: 60, avail: 20}

	next, cmd := m.locateSelectedTask()
	m = next.(tuiModel)
	if cmd != nil {
		t.Fatalf("locate reported a failure for a block that exists: %v", cmd)
	}
	if m.scrollOffset == 0 {
		t.Error("the viewport did not move to the block")
	}
}
