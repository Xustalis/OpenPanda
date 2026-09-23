package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

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

// openModelPanel opens the model register or, with nothing configured, the
// setup guide that puts the first entry in.
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
	m.wizardAlias = ""
	m.wizardEditAlias = ""
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
		m.mode = modeIdle
		return m, nil
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k", "K":
			m.selectionList.MoveUp()
			return m, nil
		case "j", "J":
			m.selectionList.MoveDown(m.listPageRows())
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

// editSelectedModel opens the form prefilled with the highlighted entry.
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
	if curMC.ContextWindow > 0 {
		m.wizardContext = strconv.Itoa(curMC.ContextWindow)
	} else {
		m.wizardContext = ""
	}
	m.wizardAlias = curMC.Alias()
	m.wizardEditAlias = curMC.Alias()
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
	} else {
		mc, _ = providers.ModelConfig(m.wizardProvider, m.wizardModel, m.wizardKey)
		// The base URL field is editable for builtins too: a relay standing
		// in for the vendor's endpoint overrides the catalogue URL here.
		if m.wizardBaseURL != "" {
			mc.BaseURL = m.wizardBaseURL
		}
	}
	if m.wizardThinking != "" && m.wizardThinking != "auto" {
		mc.Thinking = m.wizardThinking
	}
	if n, err := strconv.Atoi(strings.TrimSpace(m.wizardContext)); err == nil && n > 0 {
		mc.ContextWindow = n
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
	// A collision on a different config needs a distinct alias rather than a
	// silent overwrite: two providers can legitimately serve the same model
	// name (e.g. a relay and OpenAI both offering "gpt-4o"), so keep
	// deriving a fresh alias until it no longer clashes. An edit that renamed
	// the entry dedupes too — only the untouched original alias replaces in
	// place.
	if m.wizardEditAlias == "" || alias != m.wizardEditAlias {
		collides := func(a string) bool {
			for _, existing := range m.r.cfg.Models {
				if existing.Alias() == a && existing.Alias() != m.wizardEditAlias &&
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
				m.selectionList.MoveDown(m.listPageRows())
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
