// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	tea "github.com/charmbracelet/bubbletea"
)

// TestTaskResultDigest pins the compact digest a follow-up prompt carries:
// outcome first, output tail, changed-files roll-up, raw prefix for payloads
// that do not decode.
func TestTaskResultDigest(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace", "   ", nil},
		{"ok+stdout", `{"ok":true,"exit_code":0,"stdout":"built 3 packages"}`, []string{"ok", "built 3 packages"}},
		{"failed", `{"failed":"declined by node"}`, []string{"failed: declined by node"}},
		{"exit", `{"exit_code":2,"stderr":"boom"}`, []string{"exit 2", "boom"}},
		{"files", `{"ok":true,"files_changed":["a.go","b.go"]}`, []string{"ok", "2 file(s) changed", "a.go, b.go"}},
		{"garbage", `not json at all`, []string{"not json"}},
	}
	for _, c := range cases {
		got := taskResultDigest(c.raw)
		if c.want == nil {
			if got != "" {
				t.Fatalf("%s: digest = %q, want empty", c.name, got)
			}
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Fatalf("%s: digest %q missing %q", c.name, got, w)
			}
		}
	}
	// The digest is bounded — a multi-megabyte stdout must not ride into a
	// prompt. truncate() caps at the rune limit (a trailing … is still runes).
	if n := len([]rune(taskResultDigest(`{"ok":true,"stdout":"` + strings.Repeat("x", 9999) + `"}`))); n > taskResultDigestLimit {
		t.Fatalf("digest = %d runes, cap %d", n, taskResultDigestLimit)
	}
}

// TestTaskFollowupText pins the prompt the model sees: identity, state and a
// result digest (or the no-result marker), with the follow-up instruction.
func TestTaskFollowupText(t *testing.T) {
	task := core.Task{
		TaskID:     "t-123",
		Title:      "build docs",
		State:      core.StateDone,
		ResultJSON: `{"ok":true,"stdout":"shipped"}`,
	}
	out := taskFollowupText(i18n.English, task)
	for _, want := range []string{"t-123", "build docs", "done", "shipped"} {
		if !strings.Contains(out, want) {
			t.Fatalf("followup text missing %q:\n%s", want, out)
		}
	}
	bare := taskFollowupText(i18n.English, core.Task{TaskID: "t-9", State: core.StateCancelled})
	if !strings.Contains(bare, i18n.T(i18n.English, "cli.task.followup.noResult")) {
		t.Fatalf("no-result fallback missing:\n%s", bare)
	}
	// An empty title falls back to the task id so the prompt never reads "Task t-x: ".
	if !strings.Contains(bare, "t-9") {
		t.Fatalf("title fallback missing the id:\n%s", bare)
	}
}

// TestBuildTaskItemsOrdering pins the interactive queue's ordering: live work
// first, queued rows in the scheduler's own execution order (drag seq, then
// priority, then FIFO), terminal rows last.
func TestBuildTaskItemsOrdering(t *testing.T) {
	r, st := newOpsTestRepl(t)
	ctx := context.Background()
	mk := func(title string) core.Task {
		task, err := st.Create(ctx, "", "", title, "", nil)
		if err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		return task
	}
	// Three queued rows deliberately created out of execution order.
	fifo := mk("fifo")
	hiPrio := mk("high prio")
	if err := st.SetPriority(ctx, hiPrio.TaskID, core.PriorityHigh); err != nil {
		t.Fatal(err)
	}
	dragged := mk("dragged")
	if err := st.SetSeq(ctx, dragged.TaskID, 1); err != nil {
		t.Fatal(err)
	}
	fin := mk("finished")
	if err := st.ForceFail(ctx, fin.TaskID, "done testing"); err != nil {
		t.Fatal(err)
	}

	m := tuiModel{r: r, loc: i18n.English}
	items := m.buildTaskItems("")
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	got := []string{items[0].ID, items[1].ID, items[2].ID, items[3].ID}
	want := []string{dragged.TaskID, hiPrio.TaskID, fifo.TaskID, fin.TaskID}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (dragged→priority→fifo→terminal)", got, want)
		}
	}
	// State filter narrows the view like `/tasks <state>`.
	if only := m.buildTaskItems(core.StateFailed); len(only) != 1 || only[0].ID != fin.TaskID {
		t.Fatalf("state filter = %v, want only the failed row", only)
	}
	// Badges carry the state so queue position and status read together.
	if items[0].Badge == "" {
		t.Fatal("badge missing")
	}
}

// TestTasksListEnterDispatch pins the two Enter paths of the queue picker:
// a settled task hands its result to the model (ask path — never modeExec),
// while live work opens the row detail through the exec pump.
func TestTasksListEnterDispatch(t *testing.T) {
	done := core.Task{TaskID: "t-done", Title: "shipped", State: core.StateDone}
	live := core.Task{TaskID: "t-run", Title: "wip", State: core.StateRunning}

	// Settled → followUpTask → askTurn. With no engine the turn cannot start,
	// but the path taken is observably not exec.
	m := tuiModel{
		loc:           i18n.English,
		th:            newTheme(i18n.English),
		mode:          modeList,
		listKind:      listTasks,
		selectionList: NewSelectionList("q", []SelectionItem{{ID: done.TaskID, Value: done}}),
	}
	next := step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if next.mode == modeExec {
		t.Fatal("Enter on a settled task went to exec; want the model follow-up")
	}

	// Live → detail via the exec pump.
	m2 := tuiModel{
		loc:           i18n.English,
		th:            newTheme(i18n.English),
		mode:          modeList,
		listKind:      listTasks,
		selectionList: NewSelectionList("q", []SelectionItem{{ID: live.TaskID, Value: live}}),
	}
	next2 := step(m2, tea.KeyMsg{Type: tea.KeyEnter})
	if next2.mode != modeExec {
		t.Fatalf("Enter on a running task mode = %v, want modeExec (task detail)", next2.mode)
	}
}

// TestTasksSlashOpensList pins `/tasks` routing in the TUI: the plain board
// opens the picker, `/tasks <state>` narrows it, while watch/clear keep
// their exec and confirm paths.
func TestTasksSlashOpensList(t *testing.T) {
	r, _ := newOpsTestRepl(t)
	m := newTUIModel(r)
	m.width = 100
	m.height = 30

	next, _ := m.submit("/tasks")
	m = next.(tuiModel)
	if m.mode != modeList || m.listKind != listTasks {
		t.Fatalf("/tasks mode = %v kind = %v, want modeList/listTasks", m.mode, m.listKind)
	}
	next, _ = m.submit("/tasks failed")
	m = next.(tuiModel)
	if m.mode != modeList || m.listKind != listTasks {
		t.Fatalf("/tasks failed mode = %v, want the filtered list", m.mode)
	}
}
