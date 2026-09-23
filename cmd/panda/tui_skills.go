package main

// The Skills Hub panel ("/skills hub" or "/skills") browses the curated plus
// remote hub catalogue inside the TUI: the list loads asynchronously, "/" opens
// a filter line, Enter or "i" installs the highlighted skill, and "r"
// re-fetches the index. Installation is always an explicit keystroke — the
// panel never auto-installs.

import (
	"context"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/skills"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// skillsHubIndexMsg delivers the fetched catalogue to the Update loop.
type skillsHubIndexMsg struct {
	index *skills.HubIndex
	err   error
}

// skillsHubInstallMsg reports one installation attempt.
type skillsHubInstallMsg struct {
	name string
	err  error
}

// openSkillsHub switches to the hub panel and kicks off the index fetch. The
// fetch is async: the panel shows a loading line until skillsHubIndexMsg
// lands, so a slow hub never freezes the UI.
func (m tuiModel) openSkillsHub() (tuiModel, tea.Cmd) {
	m.mode = modeSkillsHub
	m.hubLoading = true
	m.hubErr = ""
	m.hubQuery = ""
	m.hubSearching = false
	m.hubIndex = nil
	m.selectionList = NewSelectionList(i18n.T(m.loc, "tui.skillsHub.title"), nil)
	// Plain (unboxed) render: the boxed variant hides the Header line and item
	// snippets, both of which carry the search filter and skill descriptions.
	m.selectionList.FooterHints = i18n.T(m.loc, "tui.skillsHub.hints")
	m.selectionList.EmptyText = i18n.T(m.loc, "tui.skillsHub.empty")
	// Builtins are ensured once here, not on every list rebuild — a search
	// keystroke must not touch the disk.
	store := m.hubStore()
	_ = store.EnsureBuiltins()
	m.hubInstalled = hubInstalledSet(store)
	return m, m.fetchHubIndexCmd()
}

// hubInstalledSet snapshots which curated/global skill names are already on
// disk so list rebuilds only read memory.
func hubInstalledSet(store *skills.Store) map[string]bool {
	set := map[string]bool{}
	idx, err := store.Index()
	if err != nil {
		return set
	}
	for _, e := range idx {
		if e.Scope == skills.ScopeGlobal {
			set[e.Name] = true
		}
	}
	return set
}

// hubStore opens the on-disk skills store the hub installs into — the same
// path /skill and the injector use.
func (m tuiModel) hubStore() *skills.Store {
	p := ""
	if m.r != nil && m.r.cfg != nil {
		p = m.r.cfg.Storage.SkillsPath
	}
	return skills.NewStore(p)
}

// hubURL reads the configured hub endpoint; "" means "curated only".
func (m tuiModel) hubURL() string {
	if m.r != nil && m.r.cfg != nil {
		return m.r.cfg.Skills.HubURL
	}
	return ""
}

// fetchHubIndexCmd loads the hub index off the UI goroutine.
func (m tuiModel) fetchHubIndexCmd() tea.Cmd {
	hubURL := m.hubURL()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		idx, err := skills.FetchHubIndex(ctx, hubURL)
		return skillsHubIndexMsg{index: idx, err: err}
	}
}

// hubInstallCmd installs the named skill in the background.
func (m tuiModel) hubInstallCmd(name string) tea.Cmd {
	hubURL := m.hubURL()
	return func() tea.Msg {
		store := m.hubStore()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		opts := skills.ImportOptions{Scope: skills.ScopeGlobal, Status: skills.StatusActive}
		_, err := skills.InstallFromHub(ctx, store, hubURL, name, opts)
		return skillsHubInstallMsg{name: name, err: err}
	}
}

// hubRebuildList refills the selection list from the fetched index, applying
// the search filter and marking already-installed entries.
func (m *tuiModel) hubRebuildList() {
	var matched []skills.HubSkill
	if m.hubIndex != nil {
		matched = skills.SearchHub(m.hubIndex, m.hubQuery)
	}
	var items []SelectionItem
	for i, sk := range matched {
		var badges []string
		if sk.Recommended {
			badges = append(badges, i18n.T(m.loc, "tui.skillsHub.recommended"))
		}
		if m.hubInstalled[sk.Name] {
			badges = append(badges, i18n.T(m.loc, "tui.skillsHub.installed"))
		}
		snippet := sk.Description
		if sk.Author != "" {
			snippet += " · " + sk.Author
		}
		items = append(items, SelectionItem{
			Index:   i + 1,
			ID:      sk.Name,
			Title:   sk.Name,
			Snippet: snippet,
			Badge:   strings.Join(badges, " "),
		})
	}
	m.selectionList.Items = items
	if m.selectionList.Cursor >= len(items) {
		m.selectionList.Cursor = max(0, len(items)-1)
	}
	m.selectionList.Top = min(m.selectionList.Top, m.selectionList.Cursor)
}

// handleSkillsHubKey routes keys in the hub panel: navigation, install,
// search input, refresh.
func (m tuiModel) handleSkillsHubKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.selectionPageRows()

	// The search line owns keystrokes while it is open: runes filter, Enter
	// confirms, Esc clears.
	if m.hubSearching {
		switch msg.Type {
		case tea.KeyEsc:
			m.hubSearching = false
			m.hubQuery = ""
			m.hubRebuildList()
			return m, nil
		case tea.KeyEnter:
			m.hubSearching = false
			return m, nil
		case tea.KeyBackspace, tea.KeyCtrlH:
			runes := []rune(m.hubQuery)
			if len(runes) > 0 {
				m.hubQuery = string(runes[:len(runes)-1])
				m.hubRebuildList()
			}
			return m, nil
		case tea.KeyRunes:
			m.hubQuery += string(msg.Runes)
			m.hubRebuildList()
			return m, nil
		case tea.KeySpace:
			m.hubQuery += " "
			m.hubRebuildList()
			return m, nil
		}
		return m, nil
	}

	switch msg.Type {
	case tea.KeyEsc:
		m.mode = modeIdle
		return m, nil
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
		case "j", "J":
			m.selectionList.MoveDown(rows)
			return m, nil
		case "k", "K":
			m.selectionList.MoveUp()
			return m, nil
		case "q", "Q":
			m.mode = modeIdle
			return m, nil
		case "/", "f", "F":
			m.hubSearching = true
			return m, nil
		case "r", "R":
			m.hubLoading = true
			m.hubErr = ""
			return m, m.fetchHubIndexCmd()
		case "i", "I":
			if item, ok := m.selectionList.Selected(); ok {
				return m, m.hubInstallCmd(item.ID)
			}
			return m, nil
		}
	case tea.KeyEnter:
		if item, ok := m.selectionList.Selected(); ok {
			return m, m.hubInstallCmd(item.ID)
		}
		return m, nil
	}
	return m, nil
}

// onSkillsHubIndex applies a fetched catalogue.
func (m tuiModel) onSkillsHubIndex(msg skillsHubIndexMsg) (tuiModel, tea.Cmd) {
	m.hubLoading = false
	if msg.err != nil {
		m.hubErr = msg.err.Error()
	}
	// FetchHubIndex falls back to the curated catalogue on any remote failure,
	// so a configured hub that answered with the fallback still deserves a
	// quiet "offline" note rather than silently pretending the hub worked.
	if msg.index != nil && m.hubURL() != "" && strings.Contains(msg.index.Name, "Fallback") {
		m.hubErr = i18n.T(m.loc, "tui.skillsHub.offline")
	}
	m.hubIndex = msg.index
	m.hubInstalled = hubInstalledSet(m.hubStore())
	m.hubRebuildList()
	return m, nil
}

// onSkillsHubInstall reports the install outcome and refreshes the badges.
func (m tuiModel) onSkillsHubInstall(msg skillsHubInstallMsg) (tuiModel, tea.Cmd) {
	m.hubInstalled = hubInstalledSet(m.hubStore())
	m.hubRebuildList()
	var b block
	if msg.err != nil {
		b = block{kind: blockError, body: i18n.Tf(m.loc, "tui.skillsHub.installFail", "name", msg.name, "err", msg.err.Error())}
	} else {
		b = block{kind: blockNote, body: i18n.Tf(m.loc, "tui.skillsHub.installedOK", "name", msg.name)}
	}
	return m, m.printBlock(b)
}

// skillsHubView renders the hub panel.
func (m tuiModel) skillsHubView() string {
	w := m.width
	h := m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	if m.hubLoading {
		content := m.th.heading.Render(i18n.T(m.loc, "tui.skillsHub.title")) + "\n\n" +
			m.sp.View() + " " + i18n.T(m.loc, "tui.skillsHub.loading")
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, content)
	}

	// Search line rides above the list while the filter is open or set.
	if m.hubSearching || m.hubQuery != "" {
		m.selectionList.Header = i18n.Tf(m.loc, "tui.skillsHub.search", "query", m.hubQuery)
	} else {
		m.selectionList.Header = ""
	}
	if m.hubErr != "" {
		m.selectionList.Header = strings.TrimSpace(m.selectionList.Header + "  ⚠ " + cliui.Truncate(m.hubErr, max(20, w-20), m.th.unicode))
	}
	return m.selectionList.Render(m.th, w, h)
}
