package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/carddetect"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/providers"
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
		if s.ID == m.r.activeSess {
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

// buildModelItems extracts human-readable model entries.
func (m tuiModel) buildModelItems() []SelectionItem {
	if m.r == nil || m.r.cfg == nil {
		return nil
	}
	active := m.r.cfg.Model
	var items []SelectionItem

	seen := make(map[string]bool)
	if active.Alias() != "" || active.Model != "" || active.BaseURL != "" {
		alias := active.Alias()
		if alias == "" {
			alias = effectiveModel(active)
		}
		seen[alias] = true
		items = append(items, SelectionItem{
			ID:    alias,
			Title: alias,
			Badge: i18n.T(m.loc, "tui.badge.current"),
			Value: active,
		})
	}

	for _, mod := range m.r.cfg.Models {
		alias := mod.Alias()
		if alias == "" {
			alias = effectiveModel(mod)
		}
		if seen[alias] {
			continue
		}
		seen[alias] = true
		items = append(items, SelectionItem{
			ID:    alias,
			Title: alias,
			Value: mod,
		})
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
		} else if p.APIType == config.APITypeAnthropic {
			badge = "anthropic"
		} else {
			badge = "openai"
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

// openModelPanel opens the model management box or the onboarding guide.
func (m tuiModel) openModelPanel() (tuiModel, tea.Cmd) {
	hasModels := false
	if m.r != nil && m.r.cfg != nil {
		if m.r.cfg.Model.BaseURL != "" || m.r.cfg.Model.Provider != "" || len(m.r.cfg.Models) > 0 {
			hasModels = true
		}
	}

	if !hasModels {
		return m.startModelWizard()
	}

	items := m.buildModelItems()
	sl := NewSelectionList(i18n.T(m.loc, "tui.model.panelTitle"), items)
	sl.Boxed = true
	sl.ActionHints = i18n.T(m.loc, "tui.model.actionHints")
	sl.FooterHints = i18n.T(m.loc, "tui.model.footerHints")

	m.mode = modeModelPanel
	m.selectionList = sl
	return m, nil
}

// startModelWizard starts the step-by-step model setup guide.
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
	m.wizardInput = ""
	m.wizardTestErr = ""
	m.wizardTesting = false
	m.wizardEditAlias = ""
	m.selectionList = sl
	return m, nil
}

// listPageRows is the page size for the plain full-screen lists: the same
// viewport budget MoveDown and renderPlain agree on.
func (m tuiModel) listPageRows() int {
	return max(3, m.height-6)
}

// selectionPageRows is the viewport budget of whichever selection list is on
// screen, so wheel paging (MovePage) and row stepping (MoveDown) keep the
// highlight inside the rendered window in every list mode.
func (m tuiModel) selectionPageRows() int {
	switch m.mode {
	case modeModelPanel:
		return 8
	case modeModelWizard:
		return m.listPageRows() // the catalogue is a dozen rows — page like a list
	default:
		return m.listPageRows()
	}
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
		m.mode = modeIdle
		return m, nil
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k", "K":
			m.selectionList.MoveUp()
			return m, nil
		case "j", "J":
			m.selectionList.MoveDown(max(3, m.height-6))
			return m, nil
		case "q", "Q":
			m.mode = modeIdle
			return m, nil
		}
	case tea.KeyEnter:
		item, ok := m.selectionList.Selected()
		if !ok {
			m.mode = modeIdle
			return m, nil
		}

		m.mode = modeIdle
		switch m.listKind {
		case listSessions, listResume:
			if m.r != nil {
				m.r.activeSess = item.ID
				if m.r.sessionsSt != nil {
					if sess, err := m.r.sessionsSt.Get(item.ID); err == nil && len(sess.Turns) > 0 {
						// Load turns into convo
						var convo []entry.Turn
						for _, t := range sess.Turns {
							convo = append(convo, entry.Turn{Role: t.Role, Content: t.Text})
						}
						m.r.convo = convo
						if m.chatHistory != nil {
							m.chatHistory.blocks = nil
							for _, t := range sess.Turns {
								switch t.Role {
								case "user":
									m.chatHistory.blocks = append(m.chatHistory.blocks, block{kind: blockUser, body: t.Text})
								case "assistant":
									m.chatHistory.blocks = append(m.chatHistory.blocks, block{kind: blockAnswer, body: t.Text})
								}
							}
						}
					}
				}
			}
			actionLabel := i18n.T(m.loc, "tui.list.switchedSession")
			if m.listKind == listResume {
				actionLabel = i18n.T(m.loc, "tui.list.resumedSession")
			}
			note := block{
				kind: blockNote,
				body: fmt.Sprintf("%s: %s (%s)", actionLabel, shortID(item.ID), item.Snippet),
			}
			return m, m.printBlock(note)

		case listProjects:
			if m.r != nil && m.r.projStore != nil {
				_ = m.r.projStore.SetActive(item.ID)
				m.r.activeProj = item.ID
				m.projName = item.ID
				if pr, ok := item.Value.(projectstore.Project); ok && pr.WorkDir != "" {
					m.r.cfg.Storage.WorkPath = pr.WorkDir
				}
			}
			note := block{
				kind: blockNote,
				body: fmt.Sprintf("%s: %s", i18n.T(m.loc, "tui.list.projectsTitle"), item.Title),
			}
			return m, m.printBlock(note)
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
			m.pendingDeleteModel = ""
			return m.openModelPanel()
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "y", "Y":
				alias := m.pendingDeleteModel
				m.confirmDeleteModel = false
				m.pendingDeleteModel = ""
				return m.executeDeleteModel(alias)
			case "n", "N":
				m.confirmDeleteModel = false
				m.pendingDeleteModel = ""
				return m.openModelPanel()
			}
		}
		return m, nil
	}

	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.selectionList.MoveUp()
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.selectionList.MoveDown(8)
		return m, nil
	case tea.KeyPgUp:
		m.selectionList.MovePage(-8, 8)
		return m, nil
	case tea.KeyPgDown:
		m.selectionList.MovePage(8, 8)
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
			m.selectionList.MoveDown(8)
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
		}
	case tea.KeyEnter:
		return m.switchSelectedModel()
	}
	return m, nil
}

// switchSelectedModel switches active model to the chosen entry.
func (m tuiModel) switchSelectedModel() (tuiModel, tea.Cmd) {
	item, ok := m.selectionList.Selected()
	if !ok || m.r == nil {
		m.mode = modeIdle
		return m, nil
	}

	name := item.ID
	var targetMC *config.ModelConfig
	if m.r.cfg.Model.Alias() == name {
		targetMC = &m.r.cfg.Model
	} else {
		for _, mod := range m.r.cfg.Models {
			if mod.Alias() == name || mod.Model == name {
				targetMC = &mod
				break
			}
		}
	}

	if targetMC != nil {
		_ = m.r.applyModel(*targetMC)
		m.mode = modeIdle
		note := block{
			kind: blockNote,
			body: i18n.Tf(m.loc, "tui.model.switched", "alias", targetMC.Alias(), "model", effectiveModel(*targetMC)),
		}
		return m, m.printBlock(note)
	}

	m.mode = modeIdle
	return m, nil
}

// deleteSelectedModel prompts for confirmation before removing the model.
func (m tuiModel) deleteSelectedModel() (tuiModel, tea.Cmd) {
	item, ok := m.selectionList.Selected()
	if !ok || m.r == nil || m.r.cfg == nil {
		return m, nil
	}

	m.confirmDeleteModel = true
	m.pendingDeleteModel = item.ID
	return m.openModelPanel()
}

// executeDeleteModel removes the highlighted model after confirmation.
func (m tuiModel) executeDeleteModel(alias string) (tuiModel, tea.Cmd) {
	if m.r == nil || m.r.cfg == nil {
		return m, nil
	}

	newModels := make([]config.ModelConfig, 0, len(m.r.cfg.Models))
	for _, mod := range m.r.cfg.Models {
		if mod.Alias() != alias && mod.Model != alias {
			newModels = append(newModels, mod)
		}
	}
	m.r.cfg.Models = newModels
	_ = config.UpdateModelsSection(configWritePath(m.r.configPath), m.r.cfg.Models)

	// If active model was deleted, switch to first remaining or clear
	if m.r.cfg.Model.Alias() == alias || m.r.cfg.Model.Model == alias {
		if len(newModels) > 0 {
			_ = m.r.applyModel(newModels[0])
		} else {
			m.r.cfg.Model = config.ModelConfig{}
			_ = config.UpdateModelSection(configWritePath(m.r.configPath), m.r.cfg.Model)
		}
	}

	return m.openModelPanel()
}

// editSelectedModel starts editing the chosen model.
func (m tuiModel) editSelectedModel() (tuiModel, tea.Cmd) {
	item, ok := m.selectionList.Selected()
	if !ok || m.r == nil {
		return m, nil
	}

	alias := item.ID
	var curMC config.ModelConfig
	found := false
	if m.r.cfg.Model.Alias() == alias || m.r.cfg.Model.Model == alias {
		curMC = m.r.cfg.Model
		found = true
	} else {
		for _, mod := range m.r.cfg.Models {
			if mod.Alias() == alias || mod.Model == alias {
				curMC = mod
				found = true
				break
			}
		}
	}

	if !found {
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
	if m.wizardThinking == "" {
		m.wizardThinking = "auto"
	}
	if curMC.ContextWindow > 0 {
		m.wizardContext = strconv.Itoa(curMC.ContextWindow)
	} else {
		m.wizardContext = ""
	}
	m.wizardTestErr = ""
	m.wizardTesting = false
	m.wizardEditAlias = curMC.Alias()
	if p, ok := providers.Lookup(prov); ok && p.NoAuth {
		m.wizardStep = wizardStepModelName
		m.wizardInput = curMC.Model
	} else {
		m.wizardStep = wizardStepAPIKey
		m.wizardInput = curMC.APIKey
	}
	return m, nil
}

// wizardNext advances the wizard to step s, loading that step's value into
// the shared wizardInput buffer (or building its selection list for the pick
// steps). wizardBack is the Esc path — the previous step in the sequence for
// the chosen provider.
func (m tuiModel) wizardNext(s wizardStep) (tuiModel, tea.Cmd) {
	m.wizardStep = s
	switch s {
	case wizardStepBaseURL:
		m.wizardInput = m.wizardBaseURL
	case wizardStepAPIType:
		items := []SelectionItem{
			{Index: 1, ID: config.APITypeOpenAI, Title: "OpenAI", Snippet: i18n.T(m.loc, "tui.wizard.apiTypeOpenAIHint")},
			{Index: 2, ID: config.APITypeAnthropic, Title: "Anthropic", Snippet: i18n.T(m.loc, "tui.wizard.apiTypeAnthropicHint")},
		}
		sl := NewSelectionList(i18n.T(m.loc, "tui.wizard.apiTypeTitle"), items)
		sl.Boxed = true
		sl.FooterHints = i18n.T(m.loc, "tui.wizard.confirmBack")
		if m.wizardAPIType == config.APITypeAnthropic {
			sl.Cursor = 1
		}
		m.selectionList = sl
	case wizardStepAPIKey:
		m.wizardInput = m.wizardKey
	case wizardStepModelName:
		m.wizardInput = m.wizardModel
	case wizardStepThinking:
		items := []SelectionItem{
			{Index: 1, ID: "auto", Title: i18n.T(m.loc, "tui.wizard.thinkingAuto"), Snippet: i18n.T(m.loc, "tui.wizard.thinkingAutoHint")},
			{Index: 2, ID: "on", Title: i18n.T(m.loc, "tui.wizard.thinkingOn"), Snippet: i18n.T(m.loc, "tui.wizard.thinkingOnHint")},
			{Index: 3, ID: "off", Title: i18n.T(m.loc, "tui.wizard.thinkingOff"), Snippet: i18n.T(m.loc, "tui.wizard.thinkingOffHint")},
		}
		sl := NewSelectionList(i18n.T(m.loc, "tui.wizard.thinkingTitle"), items)
		sl.Boxed = true
		sl.FooterHints = i18n.T(m.loc, "tui.wizard.confirmBack")
		switch m.wizardThinking {
		case "on":
			sl.Cursor = 1
		case "off":
			sl.Cursor = 2
		}
		m.selectionList = sl
	case wizardStepContext:
		m.wizardInput = m.wizardContext
	case wizardStepTest:
		m.wizardTesting = true
		m.wizardTestErr = ""
		return m, m.wizardTestCmd()
	}
	return m, nil
}

// wizardBack walks one step backwards, honouring the branches that the
// forward path skips (custom providers own the URL/dialect steps; no-auth
// providers skip the key step).
func (m tuiModel) wizardBack() (tuiModel, tea.Cmd) {
	switch m.wizardStep {
	case wizardStepBaseURL:
		return m.wizardNext(wizardStepProvider)
	case wizardStepAPIType:
		return m.wizardNext(wizardStepBaseURL)
	case wizardStepAPIKey:
		if m.wizardProvider == "custom" {
			return m.wizardNext(wizardStepAPIType)
		}
		return m.wizardNext(wizardStepProvider)
	case wizardStepModelName:
		if p, ok := providers.Lookup(m.wizardProvider); ok && p.NoAuth {
			if m.wizardProvider == "custom" {
				return m.wizardNext(wizardStepAPIType)
			}
			return m.wizardNext(wizardStepProvider)
		}
		return m.wizardNext(wizardStepAPIKey)
	case wizardStepThinking:
		return m.wizardNext(wizardStepModelName)
	case wizardStepContext:
		return m.wizardNext(wizardStepThinking)
	case wizardStepTest:
		return m.wizardNext(wizardStepContext)
	}
	return m.startModelWizard()
}

// wizardEdit edits one text-input step's buffer with a keystroke — runes
// append, Backspace/Ctrl+H delete. It is shared by every input step.
func (m *tuiModel) wizardEdit(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyBackspace, tea.KeyCtrlH:
		runes := []rune(m.wizardInput)
		if len(runes) > 0 {
			m.wizardInput = string(runes[:len(runes)-1])
		}
	case tea.KeyRunes:
		m.wizardInput += string(msg.Runes)
	case tea.KeySpace:
		m.wizardInput += " "
	case tea.KeyCtrlV, tea.KeyCtrlU:
		// Ctrl+U clears the field; Ctrl+V has no clipboard bridge — ignore.
		if msg.Type == tea.KeyCtrlU {
			m.wizardInput = ""
		}
	}
}

// wizardSelectionKey handles arrows/enter/esc on the pick steps (provider,
// apiType, thinking). It returns the selected item ID when Enter lands.
func (m tuiModel) wizardSelectionKey(msg tea.KeyMsg, rows int) (sel string, next tuiModel, cmd tea.Cmd, done bool) {
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.selectionList.MoveUp()
	case tea.KeyDown, tea.KeyCtrlN:
		m.selectionList.MoveDown(rows)
	case tea.KeyPgUp:
		m.selectionList.MovePage(-rows, rows)
	case tea.KeyPgDown:
		m.selectionList.MovePage(rows, rows)
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k", "K":
			m.selectionList.MoveUp()
		case "j", "J":
			m.selectionList.MoveDown(rows)
		case "q", "Q":
			return "", m, nil, true // caller treats as Esc
		}
	case tea.KeyEnter:
		item, ok := m.selectionList.Selected()
		if !ok {
			return "", m, nil, true
		}
		return item.ID, m, nil, true
	}
	return "", m, nil, false
}

// wizardTestCmd runs the connectivity probe off the UI goroutine: one-word
// completion against the assembled config. The result arrives as
// wizardTestMsg.
func (m tuiModel) wizardTestCmd() tea.Cmd {
	mc := m.wizardConfig()
	return func() tea.Msg {
		client, err := entry.NewClient(mc)
		if err != nil {
			return wizardTestMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, err = client.Complete(ctx, "You are a connectivity test.", "Reply with exactly: OK")
		return wizardTestMsg{err: err}
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
	} else {
		mc, _ = providers.ModelConfig(m.wizardProvider, m.wizardModel, m.wizardKey)
	}
	if m.wizardThinking != "" && m.wizardThinking != "auto" {
		mc.Thinking = m.wizardThinking
	}
	if n, err := strconv.Atoi(strings.TrimSpace(m.wizardContext)); err == nil && n > 0 {
		mc.ContextWindow = n
	}
	return mc
}

// handleModelWizardKey handles keystrokes in the onboarding/add guide.
func (m tuiModel) handleModelWizardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.wizardStep {
	case wizardStepProvider:
		if msg.Type == tea.KeyEsc {
			m.mode = modeIdle
			return m, nil
		}
		sel, next, _, done := m.wizardSelectionKey(msg, m.selectionPageRows())
		m = next
		if !done {
			return m, nil
		}
		if sel == "" { // q/Q or empty list
			m.mode = modeIdle
			return m, nil
		}
		m.wizardProvider = sel
		m.wizardTestErr = ""
		switch {
		case sel == "custom":
			return m.wizardNext(wizardStepBaseURL)
		default:
			p, _ := providers.Lookup(sel)
			if p.NoAuth {
				m.wizardModel = p.DefaultModel
				return m.wizardNext(wizardStepModelName)
			}
			return m.wizardNext(wizardStepAPIKey)
		}

	case wizardStepBaseURL:
		switch msg.Type {
		case tea.KeyEsc:
			return m.wizardBack()
		case tea.KeyEnter:
			base := strings.TrimSpace(m.wizardInput)
			if base == "" {
				return m, nil // the URL is required — no default exists
			}
			if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
				base = "https://" + base
			}
			m.wizardBaseURL = base
			return m.wizardNext(wizardStepAPIType)
		default:
			m.wizardEdit(msg)
			return m, nil
		}

	case wizardStepAPIType:
		if msg.Type == tea.KeyEsc {
			return m.wizardBack()
		}
		sel, next, _, done := m.wizardSelectionKey(msg, m.selectionPageRows())
		m = next
		if !done {
			return m, nil
		}
		if sel == "" {
			return m.wizardBack()
		}
		m.wizardAPIType = sel
		if m.wizardKey == "" && m.wizardProvider == "custom" {
			// Relays usually need a key, but a keyless internal gateway is
			// legitimate — the key step stays optional for custom.
			return m.wizardNext(wizardStepAPIKey)
		}
		return m.wizardNext(wizardStepAPIKey)

	case wizardStepAPIKey:
		switch msg.Type {
		case tea.KeyEsc:
			return m.wizardBack()
		case tea.KeyEnter:
			m.wizardKey = strings.TrimSpace(m.wizardInput)
			if m.wizardKey == "" {
				p, ok := providers.Lookup(m.wizardProvider)
				if ok && !p.NoAuth && m.wizardProvider != "custom" {
					return m, nil // a key is required for auth providers
				}
			}
			if m.wizardModel == "" {
				if p, ok := providers.Lookup(m.wizardProvider); ok {
					m.wizardModel = p.DefaultModel
				}
			}
			return m.wizardNext(wizardStepModelName)
		default:
			m.wizardEdit(msg)
			return m, nil
		}

	case wizardStepModelName:
		switch msg.Type {
		case tea.KeyEsc:
			return m.wizardBack()
		case tea.KeyEnter:
			modelName := strings.TrimSpace(m.wizardInput)
			if modelName == "" {
				if p, ok := providers.Lookup(m.wizardProvider); ok {
					modelName = p.DefaultModel
				}
			}
			if modelName == "" {
				return m, nil // required for custom
			}
			m.wizardModel = modelName
			return m.wizardNext(wizardStepThinking)
		default:
			m.wizardEdit(msg)
			return m, nil
		}

	case wizardStepThinking:
		if msg.Type == tea.KeyEsc {
			return m.wizardBack()
		}
		sel, next, _, done := m.wizardSelectionKey(msg, m.selectionPageRows())
		m = next
		if !done {
			return m, nil
		}
		if sel == "" {
			return m.wizardBack()
		}
		m.wizardThinking = sel
		return m.wizardNext(wizardStepContext)

	case wizardStepContext:
		switch msg.Type {
		case tea.KeyEsc:
			return m.wizardBack()
		case tea.KeyEnter:
			m.wizardContext = strings.TrimSpace(m.wizardInput)
			return m.wizardNext(wizardStepTest)
		default:
			m.wizardEdit(msg)
			return m, nil
		}

	case wizardStepTest:
		if m.wizardTesting {
			if msg.Type == tea.KeyEsc {
				m.wizardTesting = false
				return m.wizardBack()
			}
			return m, nil
		}
		switch msg.Type {
		case tea.KeyEsc:
			return m.wizardBack()
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "r", "R":
				return m.wizardNext(wizardStepTest)
			case "s", "S":
				return m.finalizeWizard()
			}
		case tea.KeyEnter:
			if m.wizardTestErr == "" {
				return m.finalizeWizard()
			}
			return m.wizardNext(wizardStepTest)
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
	if m.wizardEditAlias != "" {
		alias = m.wizardEditAlias // keep the entry's stable name
	} else if m.wizardModel != "" && m.wizardModel != alias {
		alias = m.wizardModel
	}
	// A collision on a different config needs a distinct alias rather than a
	// silent overwrite: two providers can legitimately serve the same model
	// name (e.g. a relay and OpenAI both offering "gpt-4o"), so keep
	// deriving a fresh alias until it no longer clashes.
	if m.wizardEditAlias == "" {
		collides := func(a string) bool {
			for _, existing := range m.r.cfg.Models {
				if existing.Alias() == a &&
					(existing.Model != mc.Model || existing.BaseURL != mc.BaseURL || existing.Provider != mc.Provider) {
					return true
				}
			}
			return false
		}
		for i := 2; collides(alias); i++ {
			alias = fmt.Sprintf("%s-%d", mc.Model, i)
		}
	}
	mc.Name = alias

	// Upsert into cfg.Models. When editing, drop the stale alias first so a
	// renamed endpoint cannot leave two rows behind.
	if m.wizardEditAlias != "" && m.wizardEditAlias != alias {
		kept := m.r.cfg.Models[:0]
		for _, e := range m.r.cfg.Models {
			if e.Alias() != m.wizardEditAlias {
				kept = append(kept, e)
			}
		}
		m.r.cfg.Models = kept
	}
	replaced := false
	for i := range m.r.cfg.Models {
		if m.r.cfg.Models[i].Alias() == alias {
			m.r.cfg.Models[i] = mc
			replaced = true
			break
		}
	}
	if !replaced {
		m.r.cfg.Models = append(m.r.cfg.Models, mc)
	}

	_ = config.UpdateModelsSection(configWritePath(m.r.configPath), m.r.cfg.Models)
	_ = m.r.applyModel(mc)

	// Ensure engine is active
	if m.r.engine == nil {
		eng, err := askengine.New(context.Background(), m.r.cfg, askengine.Options{
			CardPath:   m.r.cardPath,
			ReplyASCII: isLinuxConsole(),
			Locale:     m.r.loc,
			AsyncPeers: true,
		})
		if err == nil {
			m.r.engine = eng
			m.engine = eng
			m.r.bindProject()
		}
	} else {
		m.engine = m.r.engine
	}

	if m.r != nil && m.r.cfg != nil && !m.r.cfg.UI.Onboarded {
		return m.finalizeOnboarding()
	}

	m.mode = modeIdle
	note := block{
		kind: blockNote,
		body: i18n.Tf(m.loc, "tui.model.added", "alias", alias, "model", mc.Model),
	}
	return m, m.printBlock(note)
}

// startOnboarding begins the first-time use onboarding wizard.
func (m tuiModel) startOnboarding() (tuiModel, tea.Cmd) {
	m.mode = modeOnboarding
	m.onboardingStep = onboardingStepLanguage
	m.termsCursor = 0

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

// advanceFromTerms moves to the approval mode step after accepting terms.
func (m tuiModel) advanceFromTerms() (tuiModel, tea.Cmd) {
	if m.r != nil && m.r.cfg != nil {
		m.r.cfg.UI.TermsAccepted = true
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
		m.r.cfg.UI.TermsAccepted = true
		m.r.cfg.UI.Onboarded = true
		m.r.cfg.UI.Locale = string(m.loc)

		cfgPath := configWritePath(m.r.configPath)
		m.r.configPath = cfgPath
		_ = config.UpdateSectionField(cfgPath, []string{"ui"}, "locale", string(m.loc))
		_ = config.UpdateSectionFieldBool(cfgPath, []string{"ui"}, "terms_accepted", true)
		_ = config.UpdateSectionFieldBool(cfgPath, []string{"ui"}, "onboarded", true)
		if m.r.cfg.Approval.Mode != "" {
			_ = config.UpdateSectionField(cfgPath, []string{"approval"}, "mode", m.r.cfg.Approval.Mode)
		}

		// Ensure capability card exists
		cardPath := m.r.cardPath
		if cardPath == "" {
			cardPath = filepath.Join(filepath.Dir(cfgPath), "capabilities.yaml")
		}
		if _, _, err := carddetect.EnsureCard(cardPath); err == nil {
			m.r.cardPath = cardPath
			m.r.hasCard = true
			if m.r.engine != nil {
				_ = m.r.engine.ReloadCard(cardPath)
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
				m.selectionList.MoveDown(max(3, m.height-6))
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
				m.r.loc = loc
				if m.r.cfg != nil {
					m.r.cfg.UI.Locale = string(loc)
				}
			}
			m.applyLocale()
			m.onboardingStep = onboardingStepTerms
			m.termsCursor = 0
			return m, nil
		}

	case onboardingStepTerms:
		switch msg.Type {
		case tea.KeyEsc:
			m.quitting = true
			return m, tea.Quit
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
			return m, nil
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "k", "K":
				m.selectionList.MoveUp()
				return m, nil
			case "j", "J":
				m.selectionList.MoveDown(max(3, m.height-6))
				return m, nil
			}
		case tea.KeyEnter:
			item, ok := m.selectionList.Selected()
			if !ok {
				return m, nil
			}
			if m.r != nil && m.r.cfg != nil {
				m.r.cfg.Approval.Mode = item.ID
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
				m.selectionList.MoveDown(max(3, m.height-6))
				return m, nil
			}
		case tea.KeyEnter:
			item, ok := m.selectionList.Selected()
			if !ok {
				return m, nil
			}
			if item.ID == "now" {
				m.onboardingStep = onboardingStepModelWizard
				m.wizardStep = wizardStepProvider
				m.wizardProvider = ""
				m.wizardKey = ""
				m.wizardModel = ""
				m.wizardInput = ""
				sl := NewSelectionList(i18n.T(m.loc, "tui.onboard.providerTitle"), buildProviderItems(m.loc))
				sl.Boxed = true
				sl.FooterHints = i18n.T(m.loc, "tui.onboard.hintsBack")
				m.selectionList = sl
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
