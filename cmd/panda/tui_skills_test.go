//go:build !lite

package main

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/skills"
	tea "github.com/charmbracelet/bubbletea"
)

// TestSkillsHubOpenAndIndex covers the panel lifecycle: "/skills" opens the
// panel in loading state, the injected index populates the list, and Esc
// returns to idle.
func TestSkillsHubOpenAndIndex(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.r.cfg.Storage.SkillsPath = t.TempDir()

	next, cmd := m.submit("/skills")
	m = next.(tuiModel)
	if m.mode != modeSkillsHub {
		t.Fatalf("/skills should open the hub panel, got mode %d", m.mode)
	}
	if !m.hubLoading {
		t.Fatal("hub should start in the loading state")
	}
	if cmd == nil {
		t.Fatal("opening the hub should issue the fetch command")
	}

	m = step(m, skillsHubIndexMsg{index: &skills.HubIndex{
		Version: "1",
		Skills: []skills.HubSkill{
			{Name: "web-research", Description: "Research the web", Author: "panda", Recommended: true},
			{Name: "git-flow", Description: "Git workflow helper"},
		},
	}})
	if m.hubLoading {
		t.Fatal("index message should clear the loading flag")
	}
	if len(m.selectionList.Items) != 2 {
		t.Fatalf("expected 2 hub items, got %d", len(m.selectionList.Items))
	}
	if m.selectionList.Items[0].Badge == "" {
		t.Fatal("recommended skill should carry a badge")
	}
	if view := m.View(); !strings.Contains(view, "web-research") {
		t.Fatalf("hub view should list the skill, got:\n%s", view)
	}

	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeIdle {
		t.Fatalf("Esc should return to idle, got mode %d", m.mode)
	}
}

// TestSkillsHubSearch drives the "/" filter line: runes narrow the list,
// Backspace edits, Esc clears.
func TestSkillsHubSearch(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.r.cfg.Storage.SkillsPath = t.TempDir()
	next, _ := m.submit("/skills")
	m = next.(tuiModel)
	m = step(m, skillsHubIndexMsg{index: &skills.HubIndex{
		Skills: []skills.HubSkill{
			{Name: "web-research", Description: "Research the web"},
			{Name: "git-flow", Description: "Git workflow helper"},
		},
	}})

	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !m.hubSearching {
		t.Fatal("/ should open the filter line")
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("git")})
	if len(m.selectionList.Items) != 1 || m.selectionList.Items[0].ID != "git-flow" {
		t.Fatalf("filter 'git' should leave only git-flow, got %+v", m.selectionList.Items)
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyBackspace})
	m = step(m, tea.KeyMsg{Type: tea.KeyBackspace})
	m = step(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if len(m.selectionList.Items) != 2 {
		t.Fatalf("clearing the filter should restore both items, got %d", len(m.selectionList.Items))
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.hubSearching || m.hubQuery != "" {
		t.Fatal("Esc in the filter should close and clear it")
	}
	if m.mode != modeSkillsHub {
		t.Fatalf("Esc in the filter must not leave the panel, got mode %d", m.mode)
	}
}

// TestSkillsHubInstall covers the Enter-to-install path: a successful install
// refreshes the badges; the toast commits via printBlock.
func TestSkillsHubInstall(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.r.cfg.Storage.SkillsPath = t.TempDir()
	next, _ := m.submit("/skills")
	m = next.(tuiModel)
	m = step(m, skillsHubIndexMsg{index: &skills.HubIndex{
		Skills: []skills.HubSkill{{Name: "web-research", Description: "Research the web"}},
	}})

	// Enter fires the install command; inject the success message directly —
	// the network path is exercised by internal/skills tests.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on a skill should issue the install command")
	}
	m = step(m, skillsHubInstallMsg{name: "web-research"})
	if m.mode != modeSkillsHub {
		t.Fatalf("install should not leave the panel, got mode %d", m.mode)
	}
}

// TestSkillsHubEscWhileLoading: leaving the panel before the fetch lands must
// make the index message a no-op rather than resurrecting the panel state.
func TestSkillsHubEscWhileLoading(t *testing.T) {
	m := newTestTUI(t)
	m = step(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.r.cfg.Storage.SkillsPath = t.TempDir()
	next, _ := m.submit("/skills")
	m = next.(tuiModel)
	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = step(m, skillsHubIndexMsg{index: &skills.HubIndex{
		Skills: []skills.HubSkill{{Name: "x"}},
	}})
	if m.mode != modeIdle {
		t.Fatalf("stale index must not reopen the panel, got mode %d", m.mode)
	}
}
