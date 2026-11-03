// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/carddetect"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/providers"
	"github.com/Xustalis/OpenPanda/internal/security"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
	tea "github.com/charmbracelet/bubbletea"
)

// buildSessionItems extracts human-readable session entries.
func (m tuiModel) buildSessionItems() []SelectionItem {
	if m.r == nil || m.r.sessionsSt == nil {
		return nil
	}
	list, err := m.r.sessionsSt.List()
	if err != nil || len(list) == 0 {
		return nil
	}

	var items []SelectionItem
	for i, s := range list {
		snippet := s.Title
		for _, t := range s.Turns {
			if t.Role == "user" && strings.TrimSpace(t.Text) != "" {
				snippet = strings.TrimSpace(t.Text)
				break
			}
		}
		if snippet == "" {
			snippet = i18n.T(m.loc, "repl.sessions.none")
		}

		badge := ""
		if s.ID == m.r.sessID() {
			badge = i18n.T(m.loc, "tui.badge.current")
		}

		timeStr := s.UpdatedAt.Format("2006-01-02 15:04")
		items = append(items, SelectionItem{
			ID:      s.ID,
			Index:   i + 1,
			Title:   s.Title,
			Time:    timeStr,
			Turns:   len(s.Turns),
			Snippet: snippet,
			Badge:   badge,
			Value:   s,
		})
	}
	return items
}

// buildProjectItems extracts human-readable project entries.
func (m tuiModel) buildProjectItems() []SelectionItem {
	if m.r == nil || m.r.projStore == nil {
		return nil
	}
	list, err := m.r.projStore.List()
	if err != nil {
		return nil
	}
	if m.r.projects != nil {
		list = withAdoptedProjects(m.r.projStore, m.r.projects, list)
	}
	active, _ := m.r.projStore.Active()

	var items []SelectionItem
	for i, pr := range list {
		badge := ""
		if pr.Name == active {
			badge = i18n.T(m.loc, "tui.badge.current")
		}
		items = append(items, SelectionItem{
			ID:      pr.Name,
			Index:   i + 1,
			Title:   pr.Name,
			WorkDir: pr.WorkDir,
			Badge:   badge,
			Value:   pr,
		})
	}
	return items
}

// modelRowName is the name a row is painted and addressed by: its alias, or
// the model id when a config written by hand left the name out.
func modelRowName(m config.ModelConfig) string {
	if a := m.Alias(); a != "" {
		return a
	}
	return effectiveModel(m)
}

// buildModelItems renders one row per configured model, each carrying the row
// itself as its value. The panel's switch, edit and delete act on that value —
// never on a name, because a name is exactly what two accounts of one vendor
// can share.
//
// The registry is what the panel can edit and delete, so it is painted first;
// the live model is appended only when it was configured outside the registry
// (an older single-model config), and then it is the row a delete clears
// model: for. Rows are never collapsed by name: a row that is not painted is a
// row that cannot be edited or deleted, which is how a second account sharing
// an alias became invisible and unremovable.
func (m tuiModel) buildModelItems() []SelectionItem {
	if m.r == nil || m.r.cfg == nil {
		return nil
	}
	var active config.ModelConfig
	var models []config.ModelConfig
	m.r.readConfig(func(c *config.Config) {
		active = c.Model
		models = slices.Clone(c.Models)
	})

	rows := make([]config.ModelConfig, 0, len(models)+1)
	activeListed := false
	for _, mod := range models {
		if !modelRowConfigured(mod) {
			continue
		}
		if sameModelRow(mod, active) {
			activeListed = true
		}
		rows = append(rows, mod)
	}
	if !activeListed && modelRowConfigured(active) {
		rows = append(rows, active)
	}

	// Two rows can still share a name (a config written by an older build, or
	// by hand). The masked key tail is the one field that tells them apart, so
	// the rows that repeat say which account they are.
	names := make([]string, len(rows))
	repeats := make(map[string]int, len(rows))
	for i, mod := range rows {
		names[i] = modelRowName(mod)
		repeats[names[i]]++
	}

	current := i18n.T(m.loc, "tui.badge.current")
	items := make([]SelectionItem, 0, len(rows))
	for i, mod := range rows {
		label := names[i]
		if repeats[label] > 1 {
			if tail := maskedKeyTail(mod); tail != "" {
				label += " " + tail
			}
		}
		item := SelectionItem{ID: label, Title: label, Value: mod}
		if sameModelRow(mod, active) {
			item.Badge = current
		}
		items = append(items, item)
	}
	return items
}

// buildProviderItems returns the core 4 providers for onboarding/adding. Only
// Ollama carries a gloss — the other three are the vendors' own names — and it
// is localized, like every other label in this list.
// buildProviderItems renders the wizard's provider picker from the built-in
// catalogue (internal/providers), so the TUI and `/model add` can never
// diverge: every registered vendor — OpenAI, Anthropic, DeepSeek, Qwen, Kimi,
// 火山引擎, relays (custom) — appears with its endpoint and auth requirement.
func buildProviderItems(loc i18n.Locale) []SelectionItem {
	var items []SelectionItem
	for i, p := range providers.All() {
		snippet := p.BaseURL
		if snippet == "" {
			snippet = i18n.T(loc, "tui.wizard.customURLHint")
		}
		badge := ""
		if p.NoAuth {
			badge = i18n.T(loc, "tui.wizard.noKeyBadge")
		} else {
			badge = p.APIType
		}
		items = append(items, SelectionItem{
			Index:   i + 1,
			ID:      p.ID,
			Title:   p.Label,
			Snippet: snippet,
			Badge:   badge,
		})
	}
	return items
}

// openSessionsList switches to the interactive sessions view.
func (m tuiModel) openSessionsList() (tuiModel, tea.Cmd) {
	items := m.buildSessionItems()
	sl := NewSelectionList(i18n.T(m.loc, "tui.list.sessionsTitle"), items)
	sl.EmptyText = i18n.T(m.loc, "tui.list.sessionsEmpty")
	sl.FooterHints = i18n.T(m.loc, "tui.list.footerHints")

	m.mode = modeList
	m.listKind = listSessions
	m.selectionList = sl
	return m, nil
}

// openProjectsList switches to the interactive projects view.
func (m tuiModel) openProjectsList() (tuiModel, tea.Cmd) {
	items := m.buildProjectItems()
	sl := NewSelectionList(i18n.T(m.loc, "tui.list.projectsTitle"), items)
	sl.EmptyText = i18n.T(m.loc, "tui.list.projectsEmpty")
	sl.FooterHints = i18n.T(m.loc, "tui.list.footerHints")

	m.mode = modeList
	m.listKind = listProjects
	m.selectionList = sl
	return m, nil
}

// openResumeList switches to the interactive resume view.
func (m tuiModel) openResumeList() (tuiModel, tea.Cmd) {
	items := m.buildSessionItems()
	sl := NewSelectionList(i18n.T(m.loc, "tui.list.resumeTitle"), items)
	sl.EmptyText = i18n.T(m.loc, "tui.list.resumeEmpty")
	sl.FooterHints = i18n.T(m.loc, "tui.list.footerHints")

	m.mode = modeList
	m.listKind = listResume
	m.selectionList = sl
	return m, nil
}

// taskListCapTUI bounds the interactive queue view — the picker's job is the
// working set, not the archive, same cap the `/tasks` table prints.
const taskListCapTUI = 200

// taskSettled reports whether a task state is final — the rows where Enter
// hands the recorded result to the model instead of opening the row detail.
func taskSettled(state string) bool {
	switch state {
	case core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired:
		return true
	}
	return false
}

// buildTaskItems renders the interactive queue. The ordering mirrors what the
// local scheduler will actually do — running work first, queued rows in
// execution order (drag seq → priority → FIFO), parked rows, then the recent
// terminal tail — so "the model works through them in order" is visible in
// the list, not just documented. A state argument narrows the view like the
// `/tasks <state>` table does.
func (m tuiModel) buildTaskItems(state string) []SelectionItem {
	if m.r == nil || m.r.store == nil {
		return nil
	}
	tasks, err := m.r.store.ListByStateLimit(m.r.commandContext(), state, taskListCapTUI)
	if err != nil || len(tasks) == 0 {
		return nil
	}
	var inflight, queued, parked, terminal []core.Task
	for _, t := range tasks {
		switch t.State {
		case core.StateRunning, core.StateDispatched:
			inflight = append(inflight, t)
		case core.StateQueued, core.StateSubmitted:
			queued = append(queued, t)
		case core.StateDone, core.StateFailed, core.StateCancelled, core.StateExpired:
			terminal = append(terminal, t)
		default:
			parked = append(parked, t) // review, waiting_context — human-gated
		}
	}
	// Same comparator the queue scheduler's sortReady applies: dragged rows
	// (seq>0) first, then priority, then FIFO. A seq that is not positive
	// means "not dragged" — it is never compared as a number.
	sort.SliceStable(queued, func(i, j int) bool {
		a, b := queued[i], queued[j]
		if (a.Seq > 0) != (b.Seq > 0) {
			return a.Seq > 0
		}
		if a.Seq > 0 && b.Seq > 0 && a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.CreatedAt < b.CreatedAt
	})
	ordered := append(append(append(inflight, queued...), parked...), terminal...)

	// Unread user notes, counted per task in one grouped read. This is the
	// "your message is still waiting" signal the list owed the user: a note
	// silently sits on the board until the next round folds it, and a picker
	// that shows only states made that wait invisible.
	pending, _ := m.r.store.PendingNoteCounts(m.r.commandContext())

	items := make([]SelectionItem, 0, len(ordered))
	for i, t := range ordered {
		title := t.Title
		if title == "" {
			title = t.TaskID
		}
		snippet := t.Project
		if t.OwnerNode != "" && t.OwnerNode != "local" {
			if snippet != "" {
				snippet += " · "
			}
			snippet += t.OwnerNode
		}
		if n := pending[t.TaskID]; n > 0 {
			tag := i18n.Tf(m.loc, "tui.task.pendingN", "n", strconv.Itoa(n))
			if snippet != "" {
				snippet = tag + " · " + snippet
			} else {
				snippet = tag
			}
		}
		items = append(items, SelectionItem{
			Index:   i + 1,
			ID:      t.TaskID,
			Title:   title,
			Time:    time.Unix(t.UpdatedAt, 0).Format("01-02 15:04"),
			Badge:   t.State,
			Snippet: snippet,
			Value:   t,
		})
	}
	return items
}

// openTasksList switches to the interactive queue view (TUI twin of `/tasks`):
// browse what is queued, running and recently finished. Enter on a settled
// task feeds its recorded result back to the model for a follow-up; Enter on
// live work opens the task detail instead.
func (m tuiModel) openTasksList(state string) (tuiModel, tea.Cmd) {
	items := m.buildTaskItems(state)
	title := i18n.T(m.loc, "tui.list.tasksTitle")
	if state != "" {
		title += " · " + state
	}
	sl := NewSelectionList(title, items)
	sl.EmptyText = i18n.T(m.loc, "tui.list.tasksEmpty")
	sl.FooterHints = i18n.T(m.loc, "tui.list.tasksFooter")

	m.mode = modeList
	m.listKind = listTasks
	m.taskListState = state
	m.selectionList = sl
	return m, nil
}

// refreshTasksList rebuilds the open queue picker after a watch event — the
// states under the cursor just moved, and a static snapshot is exactly the
// staleness this list exists to expose. The cursor stays on the same task
// when it is still listed.
func (m *tuiModel) refreshTasksList() {
	prevID := ""
	if it, ok := m.selectionList.Selected(); ok {
		prevID = it.ID
	}
	items := m.buildTaskItems(m.taskListState)
	m.selectionList.SetItems(items)
	for i, it := range items {
		if it.ID == prevID {
			m.selectionList.Cursor = i
			break
		}
	}
	// Keep the cursor inside the rendered window after the rebuild — and the
	// window full: a shrunken list can otherwise leave Top past the last full
	// page, showing one row where a whole screen could render.
	rows := m.selectionPageRows()
	if m.selectionList.Top > max(0, len(items)-rows) {
		m.selectionList.Top = max(0, len(items)-rows)
	}
	if m.selectionList.Cursor < m.selectionList.Top {
		m.selectionList.Top = m.selectionList.Cursor
	} else if m.selectionList.Cursor >= m.selectionList.Top+rows {
		m.selectionList.Top = m.selectionList.Cursor - rows + 1
	}
}

// followUpTask hands a settled task's recorded result back to the model as
// the next ask — the "press Enter on a finished task to prompt the model"
// gesture. The task's own row is echoed first so the transcript shows which
// result the answer refers to.
func (m tuiModel) followUpTask(t core.Task) (tea.Model, tea.Cmd) {
	return m.askTurn(taskFollowupText(m.loc, t), "")
}

// noteTarget resolves which task a /queue or /inject line addresses, and
// returns the note text with that prefix stripped. An explicit leading id wins
// when it names a real row; otherwise the whole argument is the text and the
// task is the one this turn is running, falling back to the one last opened
// from the picker.
//
// The running row outranks the picker on purpose: it is the work on screen, and
// it is what "the thing I am watching" means. The picker fallback is what makes
// the bare form usable at all — "open the task, type /queue fix it" is the
// gesture, and demanding a 36-character id mid-conversation is not.
func (m tuiModel) noteTarget(arg string) (id, text string) {
	arg = strings.TrimSpace(arg)
	first, rest, hasRest := strings.Cut(arg, " ")
	rest = strings.TrimSpace(rest)
	if hasRest && rest != "" && m.taskExists(first) {
		return first, rest
	}
	if m.streamTask != "" && m.taskExists(m.streamTask) {
		return m.streamTask, arg
	}
	return m.focusTask, arg
}

// taskExists reports whether the store knows this id — the guard that keeps a
// note starting with a word like "this" from being read as a task id.
func (m tuiModel) taskExists(id string) bool {
	if id == "" || m.r == nil || m.r.store == nil {
		return false
	}
	_, err := m.r.store.Get(m.r.commandContext(), id)
	return err == nil
}

// sendTaskNote delivers one user note into a task: /queue (the agent reads it at
// its next round, nothing is interrupted) or /inject (the same note, plus
// aborting the in-flight round when THIS process runs it).
//
// It runs inline rather than through the exec pump. There is no `panda queue`
// or `panda inject` CLI verb to dispatch, and the write is a couple of rows —
// backgrounding it would spawn a subprocess and freeze the input box for no
// work. Inline also means it works mid-turn, which is exactly when a user wants
// to steer.
func (m tuiModel) sendTaskNote(arg string, inject bool) (tea.Model, tea.Cmd) {
	engine := m.engine
	if engine == nil && m.r != nil {
		engine = m.r.engine.Load()
	}
	if engine == nil {
		return m, m.printBlock(block{kind: blockError, body: i18n.T(m.loc, "tui.error.noModel")})
	}
	id, text := m.noteTarget(arg)
	if id == "" {
		return m, m.printBlock(block{kind: blockNote, body: i18n.T(m.loc, "tui.task.note.noTask")})
	}
	if text == "" {
		return m, m.printBlock(block{kind: blockError, body: i18n.T(m.loc, "tui.task.note.usage")})
	}
	status, warn, err := engine.SendTaskMessage(context.Background(), id, text, inject)
	if err != nil {
		return m, m.printBlock(block{kind: blockError, body: err.Error()})
	}
	m.focusTask = id
	// Report the outcome, not a blanket "sent": "interrupted" and "queued" are
	// different promises, and the warning (no consumer, or an inject that could
	// not reach the running process) is the whole reason this returns one.
	key := "cli.task.send.queued"
	if status == "interrupted" {
		key = "cli.task.send.interrupted"
	}
	body := i18n.Tf(m.loc, key, "id", shortID(id))
	if warn != "" {
		body += " — " + warn
	}
	return m, m.printBlock(block{kind: blockNote, body: body})
}

// Mid-turn text is not routed from here on purpose.
//
// There are two queue surfaces in the TUI and they must not be confused:
//
//	/queue <id> text, /inject <id> text
//	    Address a TASK, through the durable board (sendTaskNote above). The
//	    note survives this process, is visible in /tasks, and is read by
//	    whichever round owns the row — possibly another node's executor.
//
//	Enter, Ctrl+X (enqueuePending / injectQueued in tui_update.go)
//	    Address the CONVERSATION. The line becomes the user's next turn and
//	    lives only in this front end's pending queue.
//
// What used to live here — stageNote and its stagedNote bookkeeping — blurred
// the two: a plain Enter mid-turn wrote the board *and* appended
// "[Steering idea]: ..." to the prompt already streaming, so the model read
// one sentence twice and the persisted transcript was rewritten to say the
// user had typed something they had not. Pressing Enter twice on a changed
// draft rewrote the same board slot instead of queueing a second message, so
// three notes typed in one turn collapsed to the last one. Both are gone: a
// queued message is the user's NEXT input, never a late edit to their last.
// followUpTaskRef is the typed-command path (/task followup <id>): resolve
// the id then take the same turn the list's Enter produces. Failures land as
// transcript notes, not exec output — this runs on the Update goroutine.
func (m tuiModel) followUpTaskRef(ref string) (tea.Model, tea.Cmd) {
	note := func(s string) (tea.Model, tea.Cmd) {
		return m, m.printBlock(block{kind: blockNote, body: s})
	}
	if m.r == nil || m.r.store == nil {
		return note(i18n.T(m.loc, "repl.tasks.none"))
	}
	if strings.TrimSpace(ref) == "" {
		return note("usage: /task followup <id>")
	}
	id, err := m.r.store.ResolveTaskID(m.r.commandContext(), ref)
	if err != nil {
		var amb *core.AmbiguousTaskIDError
		if errors.As(err, &amb) {
			return note(ambiguousTaskMsg(m.loc, amb))
		}
		return note(i18n.Tf(m.loc, "repl.task.none", "id", ref))
	}
	t, err := m.r.store.Get(m.r.commandContext(), id)
	if err != nil {
		return note(i18n.Tf(m.loc, "repl.task.none", "id", ref))
	}
	return m.followUpTask(t)
}

// openModelPanel opens the model register or, with nothing configured, the
// setup guide that puts the first entry in.
func (m tuiModel) openModelPanel() (tuiModel, tea.Cmd) {
	hasModels := false
	if m.r != nil && m.r.cfg != nil {
		m.r.readConfig(func(c *config.Config) {
			if c.Model.BaseURL != "" || c.Model.Provider != "" || len(c.Models) > 0 {
				hasModels = true
			}
		})
	}

	if !hasModels {
		return m.startModelWizard()
	}

	items := m.buildModelItems()
	title := i18n.T(m.loc, "tui.model.panelTitle")
	if m.panelNotice != "" {
		title += " — " + m.panelNotice
		m.panelNotice = ""
	}
	sl := NewSelectionList(title, items)
	// Reopens (after add/edit/delete) keep the highlight where it was instead
	// of snapping back to the first row.
	if prev := m.selectionList.Cursor; len(items) > 0 {
		sl.Cursor = min(prev, len(items)-1)
		if rows := m.panelListRows(); sl.Cursor >= sl.Top+rows {
			sl.Top = sl.Cursor - rows + 1
		}
	}

	m.mode = modeModelPanel
	m.selectionList = sl
	return m, nil
}

// startModelWizard opens the provider picker; choosing one lands on the
// single-screen form (buildModelForm).
func (m tuiModel) startModelWizard() (tuiModel, tea.Cmd) {
	items := buildProviderItems(m.loc)
	sl := NewSelectionList(i18n.T(m.loc, "tui.wizard.noModelPrompt"), items)
	sl.Boxed = true
	sl.FooterHints = i18n.T(m.loc, "tui.wizard.confirmBack")

	m.mode = modeModelWizard
	m.wizardStep = wizardStepProvider
	m.wizardProvider = ""
	m.wizardKey = ""
	m.wizardModel = ""
	m.wizardBaseURL = ""
	m.wizardAPIType = ""
	m.wizardThinking = ""
	m.wizardContext = ""
	m.wizardCtxManual = false
	m.wizardMaxTok = ""
	m.wizardBudget = ""
	m.wizardRetries = ""
	m.wizardParams = ""
	m.wizardHeaders = ""
	m.wizardAlias = ""
	m.wizardEditAlias = ""
	m.wizardEditOrigin = config.ModelConfig{}
	m.form = nil
	m.formFocus = 0
	m.formErr = ""
	m.formTesting = false
	m.formTestErr = ""
	m.formFetching = false
	m.formFetchErr = ""
	m.formPicking = false
	m.selectionList = sl
	return m, nil
}

// listPageRows is the page size for the plain full-screen lists: the same
// viewport budget renderPlain draws, via the list's own VisibleRows.
func (m tuiModel) listPageRows() int {
	return m.selectionList.VisibleRows(m.height)
}

// selectionPageRows is the viewport budget of whichever selection list is on
// screen, so wheel paging (MovePage) and row stepping (MoveDown) keep the
// highlight inside the rendered window in every list mode. It asks the list
// itself — a boxed picker shows far fewer rows than a plain full-screen one,
// and the two must never disagree or the cursor scrolls off the window.
func (m tuiModel) selectionPageRows() int {
	if m.mode == modeModelPanel {
		return m.panelListRows() // the panel has its own row budget, not Render's
	}
	return m.selectionList.VisibleRows(m.height)
}

// handleListKey routes keys in modeList (sessions, projects, resume).
func (m tuiModel) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.selectionList.MoveUp()
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.selectionList.MoveDown(m.listPageRows())
		return m, nil
	case tea.KeyPgUp:
		m.selectionList.MovePage(-m.listPageRows(), m.listPageRows())
		return m, nil
	case tea.KeyPgDown:
		m.selectionList.MovePage(m.listPageRows(), m.listPageRows())
		return m, nil
	case tea.KeyEsc:
		if m.listKind == listTeam {
			m.teamMarks = nil
			m.teamExtras = nil
		}
		m.mode = modeIdle
		return m, nil
	case tea.KeySpace:
		if m.listKind == listTeam {
			m.toggleTeamMark()
			return m, nil
		}
	case tea.KeyRunes:
		// Some terminals deliver space as a rune rather than KeySpace.
		if m.listKind == listTeam && string(msg.Runes) == " " {
			m.toggleTeamMark()
			return m, nil
		}
		switch string(msg.Runes) {
		case "k", "K":
			m.selectionList.MoveUp()
			return m, nil
		case "j", "J":
			m.selectionList.MoveDown(m.listPageRows())
			return m, nil
		case "q", "Q":
			if m.listKind == listTeam {
				m.teamMarks = nil
				m.teamExtras = nil
			}
			m.mode = modeIdle
			return m, nil
		}
	case tea.KeyEnter:
		if m.listKind == listTeam {
			return m.applyTeamPicker()
		}
		item, ok := m.selectionList.Selected()
		if !ok {
			m.mode = modeIdle
			return m, nil
		}

		m.mode = modeIdle
		switch m.listKind {
		case listSessions, listResume:
			if m.r != nil && m.r.sessionsSt != nil {
				if sess, err := m.r.sessionsSt.Get(item.ID); err == nil {
					return m.attachSession(sess)
				}
			}
			return m, nil

		case listProjects:
			return m.enterProject(item.ID)

		case listTasks:
			t, ok := item.Value.(core.Task)
			if !ok {
				return m, nil
			}
			// Remember the choice: /queue and /inject default to the task the
			// user just opened, which is the only way those commands can work
			// without a 36-character id in the middle of a conversation.
			m.focusTask = t.TaskID
			if taskSettled(t.State) {
				return m.followUpTask(t)
			}
			return m.startExec("/task " + t.TaskID)

		case listRewind:
			turn, ok := item.Value.(int)
			if !ok {
				return m, nil
			}
			// A rewind deletes work; it gets a card, not a bare Enter.
			return m.confirmRewind(turn)
		}
	}
	return m, nil
}

// handleModelPanelKey routes keys inside the Boxed Model Management panel.
func (m tuiModel) handleModelPanelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirmDeleteModel {
		switch msg.Type {
		case tea.KeyEsc:
			m.confirmDeleteModel = false
			m.pendingDeleteModel = config.ModelConfig{}
			return m.openModelPanel()
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "y", "Y":
				target := m.pendingDeleteModel
				m.confirmDeleteModel = false
				m.pendingDeleteModel = config.ModelConfig{}
				return m.executeDeleteModel(target)
			case "n", "N":
				m.confirmDeleteModel = false
				m.pendingDeleteModel = config.ModelConfig{}
				return m.openModelPanel()
			}
		}
		return m, nil
	}

	rows := m.panelListRows()
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.selectionList.MoveUp()
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.selectionList.MoveDown(rows)
		return m, nil
	case tea.KeyPgUp:
		m.selectionList.MovePage(-rows, rows)
		return m, nil
	case tea.KeyPgDown:
		m.selectionList.MovePage(rows, rows)
		return m, nil
	case tea.KeyEsc:
		m.mode = modeIdle
		return m, nil
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k", "K":
			m.selectionList.MoveUp()
			return m, nil
		case "j", "J":
			m.selectionList.MoveDown(rows)
			return m, nil
		case "q", "Q":
			m.mode = modeIdle
			return m, nil
		case "a", "A":
			return m.startModelWizard()
		case "d", "D":
			return m.deleteSelectedModel()
		case "e", "E":
			return m.editSelectedModel()
		case "t", "T":
			return m.testSelectedModel()
		}
	case tea.KeyEnter:
		return m.switchSelectedModel()
	}
	return m, nil
}

// switchSelectedModel switches the active model to the highlighted row. The
// row is taken from the list item itself: re-resolving it by name picks the
// first row that answers to the name, which for two accounts of one vendor is
// the wrong account.
func (m tuiModel) switchSelectedModel() (tuiModel, tea.Cmd) {
	item, ok := m.selectionList.Selected()
	if !ok || m.r == nil {
		m.mode = modeIdle
		return m, nil
	}
	target, ok := item.Value.(config.ModelConfig)
	if !ok || !modelRowConfigured(target) {
		m.mode = modeIdle
		return m, nil
	}

	_ = m.r.applyModel(target)
	m.mode = modeIdle
	note := block{
		kind: blockNote,
		body: i18n.Tf(m.loc, "tui.model.switched", "alias", target.Alias(), "model", effectiveModel(target)),
	}
	return m, m.printBlock(note)
}

// deleteSelectedModel prompts for confirmation before removing the model.
func (m tuiModel) deleteSelectedModel() (tuiModel, tea.Cmd) {
	item, ok := m.selectionList.Selected()
	if !ok || m.r == nil || m.r.cfg == nil {
		return m, nil
	}
	target, ok := item.Value.(config.ModelConfig)
	if !ok || !modelRowConfigured(target) {
		return m, nil
	}

	m.confirmDeleteModel = true
	m.pendingDeleteModel = target
	return m.openModelPanel()
}

// executeDeleteModel removes the confirmed row — that row, by identity, and
// nothing else. Matching on alias-or-model-id dropped every account that
// shared the model id, so deleting one of two DeepSeek entries deleted both.
func (m tuiModel) executeDeleteModel(target config.ModelConfig) (tuiModel, tea.Cmd) {
	if m.r == nil || m.r.cfg == nil {
		return m, nil
	}

	// No capacity hint from m.r.cfg: the registry is read under the config
	// lock only, and a bare len() outside it is a race with a concurrent save.
	kept := make([]config.ModelConfig, 0)
	var activeHit bool
	_ = m.r.mutateConfigErr(func(c *config.Config) error {
		for _, mod := range c.Models {
			if sameModelRow(mod, target) {
				continue
			}
			kept = append(kept, mod)
		}
		c.Models = kept
		activeHit = sameModelRow(c.Model, target)
		return config.UpdateModelsSection(configWritePath(m.r.configPath), kept)
	})

	// If active model was deleted, switch to the first remaining one, or clear
	// the section deliberately when the registry is now empty. That clear is
	// the one write the downgrade guard must let through — the operator asked
	// for it, it is not a stale document — so it goes through
	// ClearModelSection. A failure leaves the in-memory model untouched (file
	// and engine stay in agreement) and is shown on the panel instead of being
	// dropped.
	if activeHit {
		if len(kept) > 0 {
			// A refusal here is the panel's one chance to explain itself: the
			// row is already gone from the file, so a silent failure leaves
			// model: naming a model the list no longer shows.
			if err := m.r.applyModel(kept[0]); err != nil {
				m.panelNotice = err.Error()
			}
		} else if err := m.r.clearActiveModel(); err != nil {
			// Same contract as the promote branch: the file is already in the
			// state the operator asked for, so the failure goes on the panel
			// rather than disappearing.
			m.panelNotice = err.Error()
		}
	}

	return m.openModelPanel()
}

// editSelectedModel opens the form prefilled with the highlighted entry.
func (m tuiModel) editSelectedModel() (tuiModel, tea.Cmd) {
	item, ok := m.selectionList.Selected()
	if !ok || m.r == nil {
		return m, nil
	}

	curMC, ok := item.Value.(config.ModelConfig)
	if !ok || !modelRowConfigured(curMC) {
		return m, nil
	}

	prov := effectiveProvider(curMC)
	if prov == "-" {
		if curMC.BaseURL != "" {
			prov = "custom"
		} else {
			prov = "openai"
		}
	}
	m.mode = modeModelWizard
	m.wizardProvider = prov
	m.wizardKey = curMC.APIKey
	m.wizardModel = curMC.Model
	m.wizardBaseURL = curMC.BaseURL
	m.wizardAPIType = curMC.NormalizedAPIType()
	m.wizardThinking = curMC.Thinking
	if curMC.ContextWindow > 0 {
		m.wizardContext = strconv.Itoa(curMC.ContextWindow)
		// A saved value is the operator's own — model changes in the editor
		// must not silently re-suggest over it.
		m.wizardCtxManual = true
	} else {
		m.wizardContext = ""
	}
	if curMC.MaxTokens > 0 {
		m.wizardMaxTok = strconv.Itoa(curMC.MaxTokens)
	} else {
		m.wizardMaxTok = ""
	}
	if curMC.ThinkingBudget > 0 {
		m.wizardBudget = strconv.Itoa(curMC.ThinkingBudget)
	} else {
		m.wizardBudget = ""
	}
	if curMC.MaxRetries != 0 {
		m.wizardRetries = strconv.Itoa(curMC.MaxRetries)
	} else {
		m.wizardRetries = ""
	}
	if len(curMC.Params) > 0 {
		if b, err := json.Marshal(curMC.Params); err == nil {
			m.wizardParams = string(b)
		}
	} else {
		m.wizardParams = ""
	}
	if len(curMC.Headers) > 0 {
		if b, err := json.Marshal(curMC.Headers); err == nil {
			m.wizardHeaders = string(b)
		}
	} else {
		m.wizardHeaders = ""
	}
	m.wizardAlias = curMC.Alias()
	m.wizardEditAlias = curMC.Alias()
	// The row this edit opened on — its identity, not its name, is what the
	// save replaces. A config can hold two rows under one name, and a save
	// that rewrote "the first row named ds" would edit the other account.
	m.wizardEditOrigin = curMC
	m.wizardStep = wizardStepForm
	m.buildModelForm()
	return m, nil
}

// wizardTestCmd runs the connectivity probe off the UI goroutine: one-word
// completion against the assembled config. The result arrives as
// wizardTestMsg.
func (m tuiModel) wizardTestCmd() tea.Cmd {
	mc := m.wizardConfig()
	return func() tea.Msg {
		return wizardTestMsg{err: probeModel(mc)}
	}
}

// wizardConfig assembles the ModelConfig the current wizard state describes —
// the single place every wizard step's value becomes a config field.
func (m tuiModel) wizardConfig() config.ModelConfig {
	var mc config.ModelConfig
	if m.wizardProvider == "custom" {
		mc = config.ModelConfig{
			Provider: "custom",
			APIType:  m.wizardAPIType,
			BaseURL:  m.wizardBaseURL,
			APIKey:   m.wizardKey,
			Model:    m.wizardModel,
			NoAuth:   m.wizardKey == "",
		}
		if mc.APIType == "" {
			mc.APIType = config.APITypeOpenAI
		}
		// A custom endpoint behind a known vendor inherits its catalogue
		// tuning: the saved row carries the resolved context window and
		// auto-loaded output budget just like a builtin provider's would.
		if p, ok := providers.Detect(mc.BaseURL, mc.Model); ok {
			mc.ContextWindow = p.ContextFor(mc.Model)
			mc.MaxTokens = p.AutoMaxTokens(mc.Model)
		}
	} else {
		mc, _ = providers.ModelConfig(m.wizardProvider, m.wizardModel, m.wizardKey)
		// The base URL field is editable for builtins too: a relay standing
		// in for the vendor's endpoint overrides the catalogue URL here. A
		// value that is still one of the provider's own endpoints — the
		// prefilled row URL, say — is rebased onto the surface this model is
		// served on (an aggregator routes Claude away from its chat row).
		if m.wizardBaseURL != "" {
			if p, ok := providers.Lookup(m.wizardProvider); ok && p.OwnsEndpoint(m.wizardBaseURL) {
				if base, apiType := p.EndpointFor(mc.Model); base != "" {
					mc.BaseURL, mc.APIType = base, apiType
				}
			} else {
				mc.BaseURL = m.wizardBaseURL
			}
		}
	}
	if m.wizardThinking != "" && m.wizardThinking != "auto" {
		mc.Thinking = m.wizardThinking
	}
	if n, err := strconv.Atoi(strings.TrimSpace(m.wizardContext)); err == nil && n > 0 {
		mc.ContextWindow = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(m.wizardMaxTok)); err == nil && n > 0 {
		mc.MaxTokens = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(m.wizardBudget)); err == nil && n > 0 {
		mc.ThinkingBudget = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(m.wizardRetries)); err == nil && n != 0 {
		mc.MaxRetries = n
	}
	if v := strings.TrimSpace(m.wizardParams); v != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(v), &obj); err == nil {
			mc.Params = obj
		}
	}
	if v := strings.TrimSpace(m.wizardHeaders); v != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(v), &obj); err == nil {
			h := make(map[string]string, len(obj))
			for k, val := range obj {
				if s, ok := val.(string); ok {
					h[k] = s
				}
			}
			mc.Headers = h
		}
	}
	return mc
}

// handleModelWizardKey routes keys in the add/edit flow: the provider picker
// on the first step, the single-screen form on the second.
func (m tuiModel) handleModelWizardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.wizardStep {
	case wizardStepForm:
		return m.handleFormKey(msg)
	case wizardStepProvider:
		if msg.Type == tea.KeyEsc {
			return m.wizardBack()
		}
		rows := m.selectionPageRows()
		switch msg.Type {
		case tea.KeyUp, tea.KeyCtrlP:
			m.selectionList.MoveUp()
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN:
			m.selectionList.MoveDown(rows)
			return m, nil
		case tea.KeyPgUp:
			m.selectionList.MovePage(-rows, rows)
			return m, nil
		case tea.KeyPgDown:
			m.selectionList.MovePage(rows, rows)
			return m, nil
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "k", "K":
				m.selectionList.MoveUp()
				return m, nil
			case "j", "J":
				m.selectionList.MoveDown(rows)
				return m, nil
			case "q", "Q":
				return m.wizardBack()
			}
			return m, nil
		case tea.KeyEnter:
			item, ok := m.selectionList.Selected()
			if !ok {
				return m, nil
			}
			m.wizardProvider = item.ID
			m.wizardEditAlias = ""
			m.buildModelForm()
			m.wizardStep = wizardStepForm
			return m, nil
		}
	}
	return m, nil
}

// finalizeWizard completes adding/updating model, sets as active, and returns to chat.
// finalizeWizard persists the assembled model, switches the active model to
// it, and returns to chat. When the wizard ran an edit (wizardEditAlias set),
// the edited entry is replaced in place — the registry never grows a stale
// duplicate under the old alias.
func (m tuiModel) finalizeWizard() (tuiModel, tea.Cmd) {
	if m.r == nil || m.r.cfg == nil {
		m.mode = modeIdle
		return m, nil
	}

	mc := m.wizardConfig()
	if mc.Provider == "" {
		note := block{kind: blockError, body: i18n.Tf(m.loc, "tui.wizard.unknownProvider", "provider", m.wizardProvider)}
		m.mode = modeIdle
		return m, m.printBlock(note)
	}

	alias := mc.Provider
	switch {
	case m.wizardEditAlias != "" && m.wizardAlias == m.wizardEditAlias:
		alias = m.wizardEditAlias // unchanged name: in-place edit
	case m.wizardAlias != "":
		alias = m.wizardAlias // the form's alias field wins
	case m.wizardModel != "" && m.wizardModel != alias:
		alias = m.wizardModel
	}
	mc.Name = alias

	// What this save replaces: the row the edit opened on, and nothing else.
	// The old test skipped rows matching on provider+model+endpoint because
	// they looked like "the same entry" — which is exactly what a second
	// account of one vendor looks like, so adding the same model again
	// rewrote the first account instead of registering a second one.
	origin := m.wizardEditOrigin
	editing := origin.Alias() != ""
	// An edit only takes the live slot when it was already there. Editing a
	// spare account used to activate it: rotating a second vendor's key pulled
	// the next turn off the model the user was talking to, which the console
	// and `/model add <alias>` both leave alone.
	wasLive := false
	var models []config.ModelConfig
	stored := mc // overwritten with the allocated name below; a failed write keeps it usable
	_ = m.r.mutateConfigErr(func(c *config.Config) error {
		kept := make([]config.ModelConfig, 0, len(c.Models)+1)
		for _, e := range c.Models {
			if editing && sameModelRow(e, origin) {
				continue
			}
			kept = append(kept, e)
		}
		// A name is the row's address, so the save must land on a free one.
		// An edit that keeps its name is not blocked by itself: the row it
		// replaces is already out of the list above.
		mc.Name = freeModelAlias(c, alias, origin)
		stored = mc
		c.Models = append(kept, mc)
		models = slices.Clone(c.Models)
		// A save takes the live slot when there is none to keep, or when the row
		// it rewrote was the live one. An *add* used to activate unconditionally,
		// so registering a second account pulled the next turn onto it — the
		// console and `/model add <alias>` both leave a configured model alone,
		// and choosing one is a click (or Enter in this panel), not a side effect.
		wasLive = !modelRowConfigured(c.Model) || (editing && sameModelRow(c.Model, origin))
		return config.UpdateModelsSection(configWritePath(m.r.configPath), models)
	})
	mc = stored
	if wasLive {
		_ = m.r.applyModel(mc)
	}

	// Ensure engine is active. Construction reads cfg fields (fallback model
	// list, card path), so it runs under the config read lock — a concurrent
	// mutateConfig writer would otherwise tear the build. bindProject stays
	// outside: it takes the write lock.
	if m.r.engine.Load() == nil {
		var eng *askengine.Engine
		loc := m.r.locale()
		m.r.readConfig(func(c *config.Config) {
			built, err := askengine.New(context.Background(), c, askengine.Options{
				CardPath:   m.r.cardPathNow(),
				ReplyASCII: isLinuxConsole(),
				Locale:     loc,
				ConfigPath: m.r.configPath,
				AsyncPeers: true,
				// Tasks go through the scheduler queue and the ask follows
				// them to settle: same-worktree work stays serialized,
				// independent projects run in parallel, and the turn still
				// reports the outcome like an inline execution.
				QueueTasks:  true,
				AwaitQueued: true,
			})
			if err == nil {
				eng = built
			}
		})
		if eng != nil {
			m.r.engine.Store(eng)
			m.engine = eng
			m.r.bindProject()
		}
	} else {
		m.engine = m.r.engine.Load()
	}

	onboarded := true
	if m.r != nil && m.r.cfg != nil {
		m.r.readConfig(func(c *config.Config) { onboarded = c.UI.Onboarded })
	}
	if !onboarded {
		return m.finalizeOnboarding()
	}

	m.mode = modeIdle
	// An edit is reported as an edit. Both used to say "added", which is how a
	// save that rewrote an existing account read on screen as a new one. A
	// spare account's edit says nothing about activation, because it did not
	// touch the live model.
	key := "tui.model.added"
	switch {
	case editing && wasLive:
		key = "tui.model.saved"
	case editing:
		key = "tui.model.updated"
	}
	note := block{
		kind: blockNote,
		body: i18n.Tf(m.loc, key, "alias", mc.Alias(), "model", mc.Model),
	}
	return m, m.printBlock(note)
}

// startOnboarding begins the first-time use onboarding wizard.
func (m tuiModel) startOnboarding() (tuiModel, tea.Cmd) {
	m.mode = modeOnboarding
	m.onboardingStep = onboardingStepLanguage
	m.termsCursor = 0
	m.termsScroll = 0
	m.termsReconsent = false

	title := "Welcome to OpenPanda · Please select your language / 请选择语言:"
	sl := NewSelectionList(title, buildLanguageItems())
	sl.Boxed = true
	sl.FooterHints = i18n.T(m.loc, "tui.onboard.hintsExit")
	sl.Cursor = 0 // Default English
	m.selectionList = sl
	return m, nil
}

// buildLanguageItems returns the supported language list with English as default.
func buildLanguageItems() []SelectionItem {
	return []SelectionItem{
		{
			Index:   1,
			ID:      "en",
			Title:   "English",
			Snippet: "Default / System Language",
			Badge:   "[Default]",
		},
		{
			Index:   2,
			ID:      "zh-CN",
			Title:   "简体中文",
			Snippet: "Simplified Chinese",
		},
		{
			Index:   3,
			ID:      "ja",
			Title:   "日本語",
			Snippet: "Japanese",
		},
		{
			Index:   4,
			ID:      "es",
			Title:   "Español",
			Snippet: "Spanish",
		},
		{
			Index:   5,
			ID:      "de",
			Title:   "Deutsch",
			Snippet: "German",
		},
	}
}

// buildApprovalModeItems returns execution approval safety options.
func buildApprovalModeItems(loc i18n.Locale) []SelectionItem {
	return []SelectionItem{
		{
			Index:   1,
			ID:      "prompt",
			Title:   i18n.T(loc, "tui.onboard.approve.prompt.title"),
			Snippet: i18n.T(loc, "tui.onboard.approve.prompt.snippet"),
			Badge:   i18n.T(loc, "tui.onboard.approve.prompt.badge"),
		},
		{
			Index:   2,
			ID:      "auto",
			Title:   i18n.T(loc, "tui.onboard.approve.auto.title"),
			Snippet: i18n.T(loc, "tui.onboard.approve.auto.snippet"),
		},
		{
			Index:   3,
			ID:      "strict",
			Title:   i18n.T(loc, "tui.onboard.approve.strict.title"),
			Snippet: i18n.T(loc, "tui.onboard.approve.strict.snippet"),
		},
	}
}

// buildModelChoiceItems returns the model configuration choice options.
func buildModelChoiceItems(loc i18n.Locale) []SelectionItem {
	return []SelectionItem{
		{
			Index:   1,
			ID:      "now",
			Title:   i18n.T(loc, "tui.onboard.modelNow.title"),
			Snippet: i18n.T(loc, "tui.onboard.modelNow.snippet"),
			Badge:   i18n.T(loc, "tui.onboard.modelNow.badge"),
		},
		{
			Index:   2,
			ID:      "skip",
			Title:   i18n.T(loc, "tui.onboard.modelSkip.title"),
			Snippet: i18n.T(loc, "tui.onboard.modelSkip.snippet"),
		},
	}
}

// startTermsReconsent re-shows only the license step when the accepted terms
// predate TermsVersionCurrent (the MIT→AGPL change). Accepting re-records the
// version and drops straight into the session; declining exits, so the next
// launch asks again.
func (m tuiModel) startTermsReconsent() (tuiModel, tea.Cmd) {
	m.mode = modeOnboarding
	m.onboardingStep = onboardingStepTerms
	m.termsCursor = 0
	m.termsScroll = 0
	m.termsReconsent = true
	return m, nil
}

// advanceFromTerms moves to the approval mode step after accepting terms —
// or, on the re-consent path, persists the new version and ends the wizard.
func (m tuiModel) advanceFromTerms() (tuiModel, tea.Cmd) {
	ts := time.Now().UTC().Format(time.RFC3339)
	who := "local"
	if m.r != nil && m.r.cfg != nil {
		m.r.mutateConfig(func(c *config.Config) {
			c.UI.TermsAccepted = true
			c.UI.TermsVersion = config.TermsVersionCurrent
			c.UI.TermsAcceptedAt = ts
			if strings.TrimSpace(c.Node.Name) != "" {
				who = c.Node.Name
			}
		})
	}
	if m.termsReconsent {
		m.termsReconsent = false
		// Re-consent users never reach finalizeOnboarding — persist now.
		cfgPath := configWritePath(m.r.configPath)
		_ = config.UpdateSectionFieldBool(cfgPath, []string{"ui"}, "terms_accepted", true)
		_ = config.UpdateSectionFieldInt(cfgPath, []string{"ui"}, "terms_version", config.TermsVersionCurrent)
		_ = config.UpdateSectionField(cfgPath, []string{"ui"}, "terms_accepted_at", ts)
		m.recordTermsAudit(who)
		var configured bool
		m.r.readConfig(func(c *config.Config) { configured = modelConfigured(c) })
		if !configured {
			return m.startModelWizard()
		}
		m.mode = modeIdle
		return m, m.printBlock(block{kind: blockNote, body: i18n.T(m.loc, "tui.onboard.termsAccepted")})
	}
	m.onboardingStep = onboardingStepApproval
	sl := NewSelectionList(i18n.T(m.loc, "tui.onboard.approvalTitle"), buildApprovalModeItems(m.loc))
	sl.Boxed = true
	sl.FooterHints = i18n.T(m.loc, "tui.onboard.hintsBack")
	m.selectionList = sl
	return m, nil
}

// finalizeOnboarding persists onboarding configuration and enters idle chat.
func (m tuiModel) finalizeOnboarding() (tuiModel, tea.Cmd) {
	if m.r != nil && m.r.cfg != nil {
		loc := m.loc
		ts := time.Now().UTC().Format(time.RFC3339)
		who := "local"

		cfgPath := configWritePath(m.r.configPath)
		// One critical section for the whole onboarding commit: each
		// UpdateSectionField rewrites the document, and an unlocked write
		// between fields would let a shared-lock peer lose the rest.
		// r.configPath joins the mutation too — /config set and /model save
		// read it inside this same cfgMu critical section.
		_ = m.r.mutateConfigErr(func(c *config.Config) error {
			m.r.configPath = cfgPath
			var firstErr error
			persist := func(err error) {
				if firstErr == nil {
					firstErr = err
				}
			}
			persist(config.UpdateSectionField(cfgPath, []string{"ui"}, "locale", string(loc)))
			persist(config.UpdateSectionFieldBool(cfgPath, []string{"ui"}, "terms_accepted", true))
			persist(config.UpdateSectionFieldInt(cfgPath, []string{"ui"}, "terms_version", config.TermsVersionCurrent))
			persist(config.UpdateSectionField(cfgPath, []string{"ui"}, "terms_accepted_at", ts))
			persist(config.UpdateSectionFieldBool(cfgPath, []string{"ui"}, "onboarded", true))
			c.UI.TermsAccepted = true
			c.UI.TermsVersion = config.TermsVersionCurrent
			c.UI.TermsAcceptedAt = ts
			c.UI.Onboarded = true
			c.UI.Locale = string(loc)
			if strings.TrimSpace(c.Node.Name) != "" {
				who = c.Node.Name
			}
			if c.Approval.Mode != "" {
				persist(config.UpdateSectionField(cfgPath, []string{"approval"}, "mode", c.Approval.Mode))
			}
			return firstErr
		})
		m.recordTermsAudit(who)

		// Ensure capability card exists
		cardPath := m.r.cardPathNow()
		if cardPath == "" {
			cardPath = filepath.Join(filepath.Dir(cfgPath), "capabilities.yaml")
		}
		if _, _, err := carddetect.EnsureCard(cardPath); err == nil {
			m.r.setCard(cardPath, true)
			if eng := m.r.engine.Load(); eng != nil {
				_ = eng.ReloadCard(cardPath)
			}
		}
	}

	m.mode = modeIdle
	m.scrollOffset = 0
	note := block{
		kind: blockNote,
		body: i18n.T(m.loc, "tui.onboard.done"),
	}
	return m, m.printBlock(note)
}

// recordTermsAudit leaves tamper-evident consent evidence: one row on the
// signed audit chain carrying the terms revision, the accepting node, the
// channel (TUI), and the build — verifiable later via `panda audit verify`.
// Best effort: with no open store there is nothing to sign into.
func (m tuiModel) recordTermsAudit(who string) {
	if m.r == nil || m.r.db == nil {
		return
	}
	_ = core.NewSignedAudit(m.r.db).Record(context.Background(), security.Entry{
		Who:    who,
		What:   "terms:accept",
		Target: fmt.Sprintf("terms_version:%d", config.TermsVersionCurrent),
		Result: "ok",
		Detail: "via=tui build=" + versionpkg.Version,
	})
}

// handleOnboardingKey routes key messages across onboarding steps.
func (m tuiModel) handleOnboardingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.onboardingStep {
	case onboardingStepLanguage:
		switch msg.Type {
		case tea.KeyUp, tea.KeyCtrlP:
			m.selectionList.MoveUp()
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN:
			m.selectionList.MoveDown(m.listPageRows())
			return m, nil
		case tea.KeyPgUp:
			m.selectionList.MovePage(-m.listPageRows(), m.listPageRows())
			return m, nil
		case tea.KeyPgDown:
			m.selectionList.MovePage(m.listPageRows(), m.listPageRows())
			return m, nil
		case tea.KeyEsc:
			m.quitting = true
			return m, tea.Quit
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "k", "K":
				m.selectionList.MoveUp()
				return m, nil
			case "j", "J":
				m.selectionList.MoveDown(m.listPageRows())
				return m, nil
			case "q", "Q":
				m.quitting = true
				return m, tea.Quit
			}
		case tea.KeyEnter:
			item, ok := m.selectionList.Selected()
			if !ok {
				return m, nil
			}
			loc := i18n.Locale(item.ID)
			m.loc = loc
			if m.r != nil {
				// applyLocale covers the raw writes this used to do: r.loc
				// under watchMu (the watcher reads it every poll), the engine
				// locale, and the ui.locale persist.
				_ = m.r.applyLocale(loc)
			}
			m.applyLocale()
			m.onboardingStep = onboardingStepTerms
			m.termsCursor = 0
			m.termsScroll = 0
			return m, nil
		}

	case onboardingStepTerms:
		switch msg.Type {
		case tea.KeyEsc:
			m.quitting = true
			return m, tea.Quit
		case tea.KeyPgUp:
			m.scrollTerms(-m.termsPageRows())
			return m, nil
		case tea.KeyPgDown:
			m.scrollTerms(m.termsPageRows())
			return m, nil
		case tea.KeyHome:
			m.termsScroll = 0
			return m, nil
		case tea.KeyEnd:
			m.scrollTerms(1 << 20) // clamped to the document
			return m, nil
		case tea.KeyUp, tea.KeyLeft:
			m.termsCursor = 0
			return m, nil
		case tea.KeyDown, tea.KeyRight:
			m.termsCursor = 1
			return m, nil
		case tea.KeyTab:
			m.termsCursor = 1 - m.termsCursor
			return m, nil
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "y", "Y":
				return m.advanceFromTerms()
			case "n", "N", "q", "Q":
				m.quitting = true
				return m, tea.Quit
			case "k", "K", "h", "H":
				m.termsCursor = 0
				return m, nil
			case "j", "J", "l", "L":
				m.termsCursor = 1
				return m, nil
			}
		case tea.KeyEnter:
			if m.termsCursor == 0 {
				return m.advanceFromTerms()
			}
			m.quitting = true
			return m, tea.Quit
		}

	case onboardingStepApproval:
		switch msg.Type {
		case tea.KeyUp, tea.KeyCtrlP:
			m.selectionList.MoveUp()
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN:
			m.selectionList.MoveDown(m.listPageRows())
			return m, nil
		case tea.KeyPgUp:
			m.selectionList.MovePage(-m.listPageRows(), m.listPageRows())
			return m, nil
		case tea.KeyPgDown:
			m.selectionList.MovePage(m.listPageRows(), m.listPageRows())
			return m, nil
		case tea.KeyEsc:
			m.onboardingStep = onboardingStepTerms
			m.termsCursor = 0
			m.termsScroll = 0
			return m, nil
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "k", "K":
				m.selectionList.MoveUp()
				return m, nil
			case "j", "J":
				m.selectionList.MoveDown(m.listPageRows())
				return m, nil
			}
		case tea.KeyEnter:
			item, ok := m.selectionList.Selected()
			if !ok {
				return m, nil
			}
			if m.r != nil && m.r.cfg != nil {
				mode := item.ID
				m.r.mutateConfig(func(c *config.Config) { c.Approval.Mode = mode })
			}
			m.onboardingStep = onboardingStepModelChoice
			sl := NewSelectionList(i18n.T(m.loc, "tui.onboard.modelChoiceTitle"), buildModelChoiceItems(m.loc))
			sl.Boxed = true
			sl.FooterHints = i18n.T(m.loc, "tui.onboard.hintsBack")
			m.selectionList = sl
			return m, nil
		}

	case onboardingStepModelChoice:
		switch msg.Type {
		case tea.KeyUp, tea.KeyCtrlP:
			m.selectionList.MoveUp()
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN:
			m.selectionList.MoveDown(m.listPageRows())
			return m, nil
		case tea.KeyPgUp:
			m.selectionList.MovePage(-m.listPageRows(), m.listPageRows())
			return m, nil
		case tea.KeyPgDown:
			m.selectionList.MovePage(m.listPageRows(), m.listPageRows())
			return m, nil
		case tea.KeyEsc:
			return m.advanceFromTerms()
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "k", "K":
				m.selectionList.MoveUp()
				return m, nil
			case "j", "J":
				m.selectionList.MoveDown(m.listPageRows())
				return m, nil
			}
		case tea.KeyEnter:
			item, ok := m.selectionList.Selected()
			if !ok {
				return m, nil
			}
			if item.ID == "now" {
				// Reuse the wizard's reset path, then swap back into the
				// onboarding step that hosts it.
				next, _ := m.startModelWizard()
				m = next
				m.mode = modeOnboarding
				m.onboardingStep = onboardingStepModelWizard
				m.selectionList.Title = i18n.T(m.loc, "tui.onboard.providerTitle")
				m.selectionList.FooterHints = i18n.T(m.loc, "tui.onboard.hintsBack")
				return m, nil
			}
			return m.finalizeOnboarding()
		}

	case onboardingStepModelWizard:
		return m.handleOnboardingModelWizardKey(msg)
	}
	return m, nil
}

// handleOnboardingModelWizardKey handles keys in the embedded onboarding model wizard.
func (m tuiModel) handleOnboardingModelWizardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.wizardStep == wizardStepProvider && (msg.Type == tea.KeyEsc || (msg.Type == tea.KeyRunes && (string(msg.Runes) == "q" || string(msg.Runes) == "Q"))) {
		m.onboardingStep = onboardingStepModelChoice
		sl := NewSelectionList(i18n.T(m.loc, "tui.onboard.modelChoiceTitle"), buildModelChoiceItems(m.loc))
		sl.Boxed = true
		sl.FooterHints = i18n.T(m.loc, "tui.onboard.hintsBack")
		m.selectionList = sl
		return m, nil
	}
	return m.handleModelWizardKey(msg)
}

// confirmTasksClear opens the yes/no card for "/tasks clear". The task count
// is read inline so the card can say exactly what is about to be wiped — an
// empty board short-circuits to the same "nothing to do" note the classic
// handler prints.
func (m tuiModel) confirmTasksClear() (tea.Model, tea.Cmd) {
	n := 0
	if m.r != nil && m.r.store != nil {
		if cnt, err := m.r.store.CountByState(context.Background(), ""); err == nil {
			n = cnt
		}
	}
	if n == 0 {
		note := block{kind: blockNote, body: i18n.T(m.loc, "cli.queue.clear.empty")}
		return m, m.printBlock(note)
	}
	m.mode = modeConfirm
	m.confirmText = strings.TrimSpace(i18n.Tf(m.loc, "cli.queue.clear.confirm", "n", strconv.Itoa(n)))
	m.confirmYes = "/tasks clear --yes"
	return m, nil
}

// onConfirmKey answers the yes/no card: y runs the held slash line through the
// exec pump (where its output lands in the transcript like any command);
// anything else — n, Esc, Enter — declines and says so. A destructive action
// never fires on an ambiguous key.
func (m tuiModel) onConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	run := m.confirmRun
	yes := m.confirmYes
	if msg.Type == tea.KeyRunes && strings.EqualFold(string(msg.Runes), "y") && (run != nil || yes != "") {
		m.confirmText, m.confirmYes, m.confirmRun = "", "", nil
		if run != nil {
			// Leave the card BEFORE running: the callback is an arbitrary
			// action whose failure paths do not know about modeConfirm, and a
			// card left standing with an emptied message swallows the next
			// keystroke as a decline.
			m.mode = modeIdle
			return run(&m)
		}
		return m.startExec(yes)
	}
	m.confirmText, m.confirmYes, m.confirmRun = "", "", nil
	m.mode = modeIdle
	note := block{kind: blockNote, body: i18n.T(m.loc, "tui.confirm.cancelled")}
	return m, m.printBlock(note)
}
