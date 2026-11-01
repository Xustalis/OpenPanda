// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

// The task rail: a side column listing the work this thread has in flight.
//
// The transcript answers "what happened"; during a fan-out it stops answering
// "what is happening right now" — a plan's stages and its delegated children run
// concurrently, and only one of them ever owns the live card. The rail is that
// missing view: every unfinished task of the thread, a line each, refreshed by
// the same two-second watch the transcript already runs.
//
// It is a view, not a second front end. The selected row can be located in the
// transcript, but nothing is started, approved or cancelled from here — those
// are still the card's and the slash command's jobs.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

const (
	// sidebarMinTerminal is the narrowest frame the rail is allowed to take a
	// slice of. Below it the transcript needs every column it has.
	sidebarMinTerminal = 72
	sidebarMaxWidth    = 38
	sidebarMinWidth    = 26
	// sidebarTaskCap bounds the read. The rail is the working set, not the
	// archive; a thread that dispatched two hundred tasks shows the newest.
	sidebarTaskCap = 40
)

// sidebarState is the rail's own state, nil-visible until Ctrl+B.
type sidebarState struct {
	visible bool
	tasks   []core.Task
	sel     int
}

// sidebarWidth is how many columns the rail takes, zero when it is closed or
// the terminal is too narrow to afford it.
func (m tuiModel) sidebarWidth() int {
	if !m.sidebar.visible || m.width < sidebarMinTerminal {
		return 0
	}
	return max(sidebarMinWidth, min(sidebarMaxWidth, m.width/3))
}

// toggleSidebar opens or closes the rail. Opening reads the store once; after
// that the watch poll keeps it current.
func (m *tuiModel) toggleSidebar() (tea.Model, tea.Cmd) {
	m.sidebar.visible = !m.sidebar.visible
	// The composer renders inside the transcript's column, so its width follows
	// textWidth — which just changed. Without this the textarea keeps wrapping
	// at the old, wider measure and lipgloss re-wraps the already-wrapped lines
	// at the new one, splitting words.
	m.ta.SetWidth(max(1, m.textWidth()-4))
	if m.sidebar.visible {
		m.refreshSidebar()
	}
	return *m, nil
}

// refreshSidebar re-reads the thread's tasks. The rail shows what the thread
// dispatched; a bare REPL (no session) shows the node's active work instead,
// which is the same question one level up.
func (m *tuiModel) refreshSidebar() {
	if !m.sidebar.visible || m.r == nil || m.r.store == nil {
		return
	}
	ctx := m.r.commandContext()
	var (
		tasks []core.Task
		err   error
	)
	if id := m.r.sessID(); id != "" {
		tasks, err = m.r.store.ListBySession(ctx, id)
	} else {
		tasks, err = m.r.store.ListActive(ctx)
	}
	if err != nil {
		return // a failed poll leaves the last good list on screen
	}
	m.sidebar.tasks = orderSidebarTasks(tasks)
	if m.sidebar.sel >= len(m.sidebar.tasks) {
		m.sidebar.sel = max(0, len(m.sidebar.tasks)-1)
	}
}

// orderSidebarTasks puts the moving work first: what is running, then what
// waits on a human, then the queue, then everything that has stopped. Within a
// bucket the newest is on top — a fresh dispatch is what the reader is looking
// for, not last hour's.
func orderSidebarTasks(tasks []core.Task) []core.Task {
	rank := func(state string) int {
		switch state {
		case core.StateRunning:
			return 0
		case core.StateDispatched:
			return 1
		case core.StateReview, core.StateWaitingCtx:
			return 2
		case core.StateQueued, core.StateSubmitted:
			return 3
		}
		return 4
	}
	out := make([]core.Task, len(tasks))
	copy(out, tasks)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i].State), rank(out[j].State)
		if ri != rj {
			return ri < rj
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	if len(out) > sidebarTaskCap {
		out = out[:sidebarTaskCap]
	}
	return out
}

// moveSidebarSel walks the selection.
func (m *tuiModel) moveSidebarSel(delta int) {
	if len(m.sidebar.tasks) == 0 {
		return
	}
	sel := m.sidebar.sel + delta
	m.sidebar.sel = max(0, min(len(m.sidebar.tasks)-1, sel))
}

// sidebarLines renders the rail to exactly `height` rows of `width` columns.
// The caller attaches them to the right of the frame, so a short list is padded
// rather than left ragged.
func (m tuiModel) sidebarLines(height, width int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	inner := width - 2 // one column of gutter on each side
	if inner < 8 {
		return nil
	}
	rows := make([]string, 0, height)

	title := m.th.heading.Render(" " + i18n.T(m.loc, "tui.sidebar.title"))
	if inflight := m.sidebarInflight(); inflight > 0 {
		title += m.th.muted.Render(" " + i18n.Tf(m.loc, "tui.sidebar.running", "n", fmt.Sprint(inflight)))
	}
	rows = append(rows, clipRendered(title, width))
	rows = append(rows, m.th.muted.Render(strings.Repeat(m.th.glyph("─", "-"), width)))

	if len(m.sidebar.tasks) == 0 {
		rows = append(rows, m.th.muted.Render(" "+i18n.T(m.loc, "tui.sidebar.empty")))
	}
	for i, t := range m.sidebar.tasks {
		if len(rows) >= height-1 {
			break
		}
		rows = append(rows, m.sidebarTaskRows(t, i == m.sidebar.sel, inner)...)
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	if len(rows) > height {
		rows = rows[:height]
	}
	return rows
}

// sidebarTaskRows draws one task as two rows: what it is, then what it is doing
// and where. Two rows is the trade the rail can afford — one row of a 26-column
// column holds either a readable title or a readable state, never both.
func (m tuiModel) sidebarTaskRows(t core.Task, selected bool, inner int) []string {
	marker, style := m.th.glyph("✓", "+"), m.th.muted
	switch t.State {
	case core.StateRunning:
		marker, style = m.th.glyph("●", "*"), m.th.success
	case core.StateDispatched:
		marker, style = m.th.glyph("◐", "o"), m.th.accent
	case core.StateReview, core.StateWaitingCtx:
		marker, style = m.th.glyph("!", "?"), m.th.warn
	case core.StateQueued, core.StateSubmitted:
		marker, style = m.th.glyph("·", "."), m.th.muted
	case core.StateFailed:
		marker, style = m.th.glyph("✗", "x"), m.th.danger
	}
	if selected {
		marker = m.th.accent.Render(m.th.glyph("▸", ">"))
	}

	titleWidth := max(4, inner-2)
	title := clipRendered(style.Render(t.Title), titleWidth)
	rows := []string{" " + marker + " " + title}

	meta := []string{stateWord(m.loc, t.State)}
	if t.OwnerNode != "" && t.Remote {
		meta = append(meta, clipRendered(t.OwnerNode, 12))
	}
	if age := sidebarAge(t); age != "" {
		meta = append(meta, age)
	}
	line := "   " + clipRendered(strings.Join(meta, " · "), max(4, inner-1))
	rows = append(rows, m.th.muted.Render(line))
	return rows
}

// sidebarAge is the short elapsed time a row shows: how long the task has been
// where it is. A finished row shows how long ago it finished; a running one how
// long it has been going, which is the number a stalled run is spotted by.
func sidebarAge(t core.Task) string {
	ts := t.UpdatedAt
	if t.State != core.StateDone && t.State != core.StateFailed {
		ts = t.CreatedAt
	}
	if ts <= 0 {
		return ""
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// sidebarInflight counts the rows that are moving.
func (m tuiModel) sidebarInflight() int {
	n := 0
	for _, t := range m.sidebar.tasks {
		switch t.State {
		case core.StateRunning, core.StateDispatched:
			n++
		}
	}
	return n
}

// stateWord is the short per-state label a row shows.
func stateWord(loc i18n.Locale, state string) string {
	return i18n.T(loc, "tui.sidebar.state."+state)
}

// sidebarSelected returns the task under the cursor.
func (m tuiModel) sidebarSelected() (core.Task, bool) {
	if m.sidebar.sel < 0 || m.sidebar.sel >= len(m.sidebar.tasks) {
		return core.Task{}, false
	}
	return m.sidebar.tasks[m.sidebar.sel], true
}

// sidebarKey handles the rail's own bindings. Ctrl+B toggles it in every mode
// that has a transcript; Alt+arrows walk the list, which leaves the plain
// arrows to the reader's scrolling.
func (m tuiModel) sidebarKey(msg tea.KeyMsg) (handled bool, _ tea.Model, _ tea.Cmd) {
	if msg.Type == tea.KeyCtrlB {
		next, cmd := m.toggleSidebar()
		return true, next, cmd
	}
	if !m.sidebar.visible {
		return false, m, nil
	}
	switch msg.Type {
	case tea.KeyUp, tea.KeyDown:
		if !msg.Alt {
			return false, m, nil
		}
		if msg.Type == tea.KeyUp {
			m.moveSidebarSel(-1)
		} else {
			m.moveSidebarSel(1)
		}
		return true, m, nil
	case tea.KeyCtrlG:
		next, cmd := m.locateSelectedTask()
		return true, next, cmd
	}
	return false, m, nil
}

// locateSelectedTask scrolls the transcript to the block of the selected task.
func (m *tuiModel) locateSelectedTask() (tea.Model, tea.Cmd) {
	task, ok := m.sidebarSelected()
	if !ok {
		return *m, nil
	}
	idx, found := m.findTaskBlock(task)
	if !found {
		// A task that never produced a transcript block (dispatched from the
		// web console, or a stage whose card is still live) has nothing to
		// scroll to; say so rather than moving the view to nowhere.
		return *m, m.printBlock(block{kind: blockNote, body: i18n.Tf(m.loc, "tui.sidebar.noBlock", "title", task.Title)})
	}
	m.scrollToBlock(idx)
	return *m, nil
}

// findTaskBlock finds the committed block that reports this task. The exact
// match is the row id stamped on the block; the body scan behind it covers a
// transcript rebuilt from stored turns, where the id is only present when the
// text happens to name it.
func (m tuiModel) findTaskBlock(task core.Task) (int, bool) {
	if m.chatHistory == nil {
		return 0, false
	}
	for i, b := range m.chatHistory.blocks {
		if b.kind == blockTask && b.taskRef == task.TaskID {
			return i, true
		}
	}
	for i, b := range m.chatHistory.blocks {
		if b.taskRef == task.TaskID || strings.Contains(b.body, task.TaskID) {
			return i, true
		}
	}
	return 0, false
}

// transcriptLayout is where the last painted frame put each committed block,
// and how tall the content and the viewport were. View fills it; the locate
// action reads it.
type transcriptLayout struct {
	blockStart []int
	content    int
	avail      int
}

// scrollToBlock brings a committed block into view. The block lands a third of
// the way down rather than at the very top: what sits above it is usually the
// prompt that caused it, and reading a result without its question is half a
// result.
func (m *tuiModel) scrollToBlock(i int) {
	if m.layout == nil || i < 0 || i >= len(m.layout.blockStart) {
		return
	}
	total, avail := m.layout.content, m.layout.avail
	if avail <= 0 || total <= avail {
		return // the whole transcript fits; there is nothing to move
	}
	end := m.layout.blockStart[i] - avail/3 + avail
	m.scrollOffset = max(0, min(total-avail, total-end))
}
