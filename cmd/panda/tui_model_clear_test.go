// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// Deleting the last model has to reach the engine, not just the file. The ask
// path reads the client the engine holds, so clearing `model:` alone left the
// removed credential in the slot and every following turn was still answered by
// the row the operator had just deleted — while the panel and the file both
// said nothing was configured.
func TestPanelDeleteOfTheLastModelStopsTheEngine(t *testing.T) {
	cfg := isolatedUIConfig(t)
	// A loopback endpoint on a dead port: a regression here must fail by
	// dialing something local, never by reaching a real vendor from a test.
	cfg.Model = config.ModelConfig{
		Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "http://127.0.0.1:9/v1", Model: "probe", APIKey: "sk-only",
	}
	cfg.Models = []config.ModelConfig{cfg.Model}

	eng, err := askengine.New(context.Background(), cfg, askengine.Options{CardPath: cfg.Node.CardPath})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	t.Cleanup(eng.Close)

	r := &repl{loc: i18n.English, cfg: cfg, configPath: cliConfigPath}
	r.engine.Store(eng)

	m := newTUIModel(r)
	m.width, m.height = 100, 30
	next, _ := m.openModelPanel()
	m = next
	if len(m.selectionList.Items) != 1 {
		t.Fatalf("rows = %d, want the one configured model", len(m.selectionList.Items))
	}

	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if got := eng.ModelConfig(); got.Model != "" || got.Provider != "" || got.BaseURL != "" {
		t.Fatalf("engine still holds a model after the delete: %+v", got)
	}
	if engHasModel(cfg) {
		t.Fatalf("config still carries a model after the delete: %+v", cfg.Model)
	}
	if _, err := eng.Ask(context.Background(), "hello", false); !errors.Is(err, entry.ErrNoModel) {
		t.Fatalf("ask after deleting the last model = %v, want ErrNoModel", err)
	}
}

// engHasModel reads the shared config the engine aliases, so the assertion is
// on what a later turn would see rather than on the test's own copy.
func engHasModel(cfg *config.Config) bool {
	return cfg.Model.Model != "" || cfg.Model.Provider != "" || cfg.Model.BaseURL != ""
}

// The same contract on the CLI path: `/model remove` of the last row must clear
// the engine's client too, or the command reports "removed" while the next turn
// is still answered by that row.
func TestModelRemoveOfTheLastModelStopsTheEngine(t *testing.T) {
	cfg := isolatedUIConfig(t)
	cfg.Model = config.ModelConfig{
		Name: "only", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "http://127.0.0.1:9/v1", Model: "probe", APIKey: "sk-only",
	}
	cfg.Models = []config.ModelConfig{cfg.Model}

	eng, err := askengine.New(context.Background(), cfg, askengine.Options{CardPath: cfg.Node.CardPath})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	t.Cleanup(eng.Close)

	r := &repl{loc: i18n.English, cfg: cfg, configPath: cliConfigPath}
	r.engine.Store(eng)

	r.modelRemove([]string{"only"})

	if got := eng.ModelConfig(); got.Model != "" || got.Provider != "" || got.BaseURL != "" {
		t.Fatalf("engine still holds a model after /model remove: %+v", got)
	}
	if _, err := eng.Ask(context.Background(), "hello", false); !errors.Is(err, entry.ErrNoModel) {
		t.Fatalf("ask after /model remove of the last model = %v, want ErrNoModel", err)
	}
}
