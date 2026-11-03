// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

// Two accounts of one vendor — the "work DeepSeek" and the "personal DeepSeek"
// — share a provider id, a model id and an endpoint. Everything that told rows
// apart used to be derived from exactly those fields, so the second account was
// indistinguishable from the first: saving it rewrote the first, the panel
// painted only one of the two, and deleting either took both. These tests pin
// the rule that replaced it — the alias is the row's address, and every write
// acts on one row's identity rather than on a name that accounts can share.

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// keysOf lists the stored rows as "alias=key" so a failure says which account
// survived rather than only how many did.
func keysOf(ms []config.ModelConfig) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Alias()+"="+m.APIKey)
	}
	return out
}

// wizardAddDeepSeek drives the real add flow — provider pick, key, save — for
// the same provider and model every time, the way a user configuring a second
// account would.
func wizardAddDeepSeek(t *testing.T, m tuiModel, key string) tuiModel {
	t.Helper()
	next, _ := m.startModelWizard()
	m = next
	m = wizardSelect(m, "deepseek")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = formFocusID(m, "api_key")
	m = step(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = formTypes(m, key)
	m = formFocusID(m, "model")
	m = step(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = formTypes(m, "deepseek-chat")
	m = formFocusID(m, "save")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = step(m, wizardTestMsg{})
	if m.mode != modeIdle {
		t.Fatalf("save did not finish: mode=%v formErr=%q testErr=%q", m.mode, m.formErr, m.formTestErr)
	}
	return m
}

// Saving a second account of a vendor already configured must register a row
// beside the first. The old save treated provider+model+endpoint as identity,
// gave the new row the same alias, and the upsert replaced the first account —
// which is why "add the same model again" looked like it opened an editor.
func TestWizardAddsASecondAccountOfTheSameModel(t *testing.T) {
	cfg := isolatedUIConfig(t)
	r := &repl{loc: i18n.English, cfg: cfg, configPath: cliConfigPath}
	closeWizardEngine(t, r)
	m := newTUIModel(r)
	m.width, m.height = 100, 30

	m = wizardAddDeepSeek(t, m, "sk-first")
	m = wizardAddDeepSeek(t, m, "sk-second")

	if len(cfg.Models) != 2 {
		t.Fatalf("rows after two accounts = %d (%v), want 2", len(cfg.Models), keysOf(cfg.Models))
	}
	names := map[string]string{}
	for _, mod := range cfg.Models {
		names[mod.Alias()] = mod.APIKey
	}
	if names["deepseek"] != "sk-first" {
		t.Fatalf("first account = %q, want sk-first (aliases %v)", names["deepseek"], keysOf(cfg.Models))
	}
	if names["deepseek-2"] != "sk-second" {
		t.Fatalf("second account = %q, want sk-second under a suffixed name (%v)", names["deepseek-2"], keysOf(cfg.Models))
	}
}

// A row that is not painted is a row that cannot be edited or deleted. Rows
// sharing a name — a config written before names were kept unique — must all
// appear, and the masked key tail is what tells them apart on screen.
func TestPanelPaintsEveryRowEvenUnderOneName(t *testing.T) {
	cfg := isolatedUIConfig(t)
	cfg.Models = []config.ModelConfig{
		{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"},
		{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"},
	}
	r := &repl{loc: i18n.English, cfg: cfg, configPath: cliConfigPath}
	closeWizardEngine(t, r)
	m := newTUIModel(r)
	m.width, m.height = 100, 30

	next, _ := m.openModelPanel()
	m = next
	if len(m.selectionList.Items) != 2 {
		t.Fatalf("panel rows = %d (%v), want both", len(m.selectionList.Items), itemIDs(m))
	}
	if m.selectionList.Items[0].ID == m.selectionList.Items[1].ID {
		t.Fatalf("two accounts painted identically: %q", m.selectionList.Items[0].ID)
	}
	if !strings.Contains(m.selectionList.Items[0].ID+m.selectionList.Items[1].ID, "irst") {
		t.Fatalf("rows do not name the account: %v", itemIDs(m))
	}
}

func itemIDs(m tuiModel) []string {
	out := make([]string, 0, len(m.selectionList.Items))
	for _, it := range m.selectionList.Items {
		out = append(out, it.ID)
	}
	return out
}

// Deleting one account must leave the other one — with its key. Matching on
// alias-or-model-id dropped every row sharing the model id, so removing the
// work account also removed the personal one.
func TestPanelDeleteDropsOnlyTheAddressedRow(t *testing.T) {
	cfg := isolatedUIConfig(t)
	cfg.Models = []config.ModelConfig{
		{Name: "deepseek-chat", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"},
		{Name: "deepseek-chat-2", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"},
	}
	r := &repl{loc: i18n.English, cfg: cfg, configPath: cliConfigPath}
	closeWizardEngine(t, r)
	m := newTUIModel(r)
	m.width, m.height = 100, 30

	next, _ := m.openModelPanel()
	m = next
	m.selectionList.Cursor = 0
	next, _ = m.deleteSelectedModel()
	m = next
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if len(cfg.Models) != 1 {
		t.Fatalf("rows after deleting one = %d (%v), want 1", len(cfg.Models), keysOf(cfg.Models))
	}
	if cfg.Models[0].APIKey != "sk-second" {
		t.Fatalf("deleted the wrong account: %v", keysOf(cfg.Models))
	}
}

// Editing one account must rewrite that row. Resolving the address by name
// picked the first row answering to it, so editing the second account
// rewrote the first one's key.
func TestPanelEditRewritesTheAddressedRow(t *testing.T) {
	cfg := isolatedUIConfig(t)
	cfg.Models = []config.ModelConfig{
		{Name: "deepseek-chat", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"},
		{Name: "deepseek-chat-2", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"},
	}
	r := &repl{loc: i18n.English, cfg: cfg, configPath: cliConfigPath}
	closeWizardEngine(t, r)
	m := newTUIModel(r)
	m.width, m.height = 100, 30

	next, _ := m.openModelPanel()
	m = next
	// Item 0 is the first row in the registry; pick the second by identity.
	for i, it := range m.selectionList.Items {
		if mc, ok := it.Value.(config.ModelConfig); ok && mc.APIKey == "sk-second" {
			m.selectionList.Cursor = i
		}
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if m.wizardEditOrigin.APIKey != "sk-second" {
		t.Fatalf("edit opened on %q, want the second account", m.wizardEditOrigin.APIKey)
	}
	m = formFocusID(m, "api_key")
	m = step(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = formTypes(m, "sk-second-rotated")
	m = formFocusID(m, "save")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	// The action row opens on "test & save": the probe has to answer before
	// the row is written.
	m = step(m, wizardTestMsg{})

	if len(cfg.Models) != 2 {
		t.Fatalf("rows after an edit = %d (%v), want 2", len(cfg.Models), keysOf(cfg.Models))
	}
	if cfg.Models[0].APIKey != "sk-first" {
		t.Fatalf("the edit touched the other account: %v", keysOf(cfg.Models))
	}
	if cfg.Models[1].APIKey != "sk-second-rotated" {
		t.Fatalf("the edited row = %v, want the rotated key", keysOf(cfg.Models))
	}
}

// The CLI's add derives its own alias, so two accounts without an explicit
// name must not collide on it.
func TestModelAddKeepsASecondAccountOfTheSameProvider(t *testing.T) {
	r := &repl{cfg: &config.Config{}, configPath: filepath.Join(t.TempDir(), "config.yaml")}

	r.modelAdd([]string{"deepseek", "sk-first-000000000000000000000000", "--force"})
	r.modelAdd([]string{"deepseek", "sk-second-00000000000000000000000", "--force"})

	if len(r.cfg.Models) != 2 {
		t.Fatalf("rows = %d (%v), want 2", len(r.cfg.Models), keysOf(r.cfg.Models))
	}
	if r.cfg.Models[0].APIKey != "sk-first-000000000000000000000000" {
		t.Fatalf("first account overwritten: %v", keysOf(r.cfg.Models))
	}
	if r.cfg.Models[1].Alias() != "deepseek-2" {
		t.Fatalf("second alias = %q, want deepseek-2", r.cfg.Models[1].Alias())
	}
}

// An explicit alias stays an address: naming a row that already exists updates
// it, which is the only edit the CLI has.
func TestModelAddWithAnExplicitAliasUpdatesThatRow(t *testing.T) {
	r := &repl{cfg: &config.Config{}, configPath: filepath.Join(t.TempDir(), "config.yaml")}

	r.modelAdd([]string{"deepseek", "sk-first-000000000000000000000000", "work", "--force"})
	r.modelAdd([]string{"deepseek", "sk-rotated-0000000000000000000000", "work", "--force"})

	if len(r.cfg.Models) != 1 {
		t.Fatalf("rows = %d (%v), want the named row updated", len(r.cfg.Models), keysOf(r.cfg.Models))
	}
	if r.cfg.Models[0].APIKey != "sk-rotated-0000000000000000000000" {
		t.Fatalf("key = %q, want the rotated one", r.cfg.Models[0].APIKey)
	}
}

// Removing one account leaves the other. The name matched the alias *or* the
// model id, so removing either account of one model removed both.
func TestModelRemoveDropsOnlyTheNamedRow(t *testing.T) {
	cfg := &config.Config{}
	cfg.Models = []config.ModelConfig{
		{Name: "deepseek-chat", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"},
		{Name: "deepseek-chat-2", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"},
	}
	cfg.Model = cfg.Models[0]
	r := &repl{cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}

	r.modelRemove([]string{"deepseek-chat-2"})

	if len(r.cfg.Models) != 1 || r.cfg.Models[0].APIKey != "sk-first" {
		t.Fatalf("rows = %v, want only the first account", keysOf(r.cfg.Models))
	}
}

// A model id addresses a row only when one row serves it: two accounts share
// the id, and guessing there deletes the wrong one.
func TestModelRemoveRefusesAnAmbiguousModelID(t *testing.T) {
	cfg := &config.Config{}
	cfg.Models = []config.ModelConfig{
		{Name: "work", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"},
		{Name: "home", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"},
	}
	r := &repl{cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}

	r.modelRemove([]string{"deepseek-chat"})

	if len(r.cfg.Models) != 2 {
		t.Fatalf("an ambiguous name removed rows: %v", keysOf(r.cfg.Models))
	}
}

// Deleting the model in use is a normal thing to want, and it used to be
// refused outright. The next row takes over; the last one clears the section
// deliberately instead of leaving model: pointing at a row that is gone.
func TestModelRemovePromotesTheSuccessorOfTheLiveRow(t *testing.T) {
	cfg := &config.Config{}
	cfg.Models = []config.ModelConfig{
		{Name: "work", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"},
		{Name: "home", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"},
	}
	cfg.Model = cfg.Models[0]
	r := &repl{cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}

	r.modelRemove([]string{"work"})
	if len(r.cfg.Models) != 1 || r.cfg.Model.Alias() != "home" {
		t.Fatalf("after removing the live row: models=%v active=%q, want home active", keysOf(r.cfg.Models), r.cfg.Model.Alias())
	}

	r.modelRemove([]string{"home"})
	if len(r.cfg.Models) != 0 || modelRowConfigured(r.cfg.Model) {
		t.Fatalf("after removing the last row: models=%v active=%+v, want both empty", keysOf(r.cfg.Models), r.cfg.Model)
	}
}

// Editing a spare account must leave the conversation on the model it is using.
// The wizard used to end every save with applyModel, so rotating the key of a
// second vendor silently switched the next turn to it — the console and the
// CLI's `/model add <alias>` both rewrite the row and stay put.
func TestPanelEditOfASpareAccountKeepsTheCurrentOne(t *testing.T) {
	cfg := isolatedUIConfig(t)
	cfg.Models = []config.ModelConfig{
		{Name: "work", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"},
		{Name: "home", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"},
	}
	cfg.Model = cfg.Models[0]
	r := &repl{loc: i18n.English, cfg: cfg, configPath: cliConfigPath}
	closeWizardEngine(t, r)
	m := newTUIModel(r)
	m.width, m.height = 100, 30

	next, _ := m.openModelPanel()
	m = next
	for i, it := range m.selectionList.Items {
		if mc, ok := it.Value.(config.ModelConfig); ok && mc.APIKey == "sk-second" {
			m.selectionList.Cursor = i
		}
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = formFocusID(m, "api_key")
	m = step(m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = formTypes(m, "sk-second-rotated")
	m = formFocusID(m, "save")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = step(m, wizardTestMsg{})

	if cfg.Model.APIKey != "sk-first" {
		t.Fatalf("editing a spare account took the live slot: active=%q", cfg.Model.APIKey)
	}
	if len(cfg.Models) != 2 || cfg.Models[1].APIKey != "sk-second-rotated" {
		t.Fatalf("the edit did not land on the spare row: %v", keysOf(cfg.Models))
	}
}

// `/model <name>` and `/model remove <name>` resolve names through one function
// now. Before, only remove refused the ambiguous case, so switching to a name
// two accounts share silently took whichever row came first — acting on the
// account the user did not pick, without saying so.
func TestModelSwitchRefusesToGuessBetweenTwoAccounts(t *testing.T) {
	first := config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"}
	second := config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"}
	cfg := &config.Config{Model: first, Models: []config.ModelConfig{first, second}}
	r := &repl{loc: i18n.English, cfg: cfg}

	r.modelSwitch("ds")

	if cfg.Model.APIKey != "sk-first" {
		t.Fatalf("switch guessed between two accounts: live key = %q", cfg.Model.APIKey)
	}
	if len(cfg.Models) != 2 {
		t.Fatalf("switch touched the registry: %v", keysOf(cfg.Models))
	}

	// A model id both accounts serve is the same dead end, and the same answer.
	if _, found, ambiguous := resolveModelRow(cfg, "deepseek-chat"); found || !ambiguous {
		t.Fatalf("shared model id = found %v ambiguous %v, want ambiguous", found, ambiguous)
	}
	// Once the rows have distinct names, each one resolves to exactly itself —
	// the ambiguity was the shared name, not the shared model.
	if _, _, ambiguous := resolveModelRow(cfg, "ds"); !ambiguous {
		t.Fatal("two rows under one alias must stay ambiguous")
	}
	cfg.Models[1].Name = "ds-home"
	mc, found, ambiguous := resolveModelRow(cfg, "ds-home")
	if !found || ambiguous || mc.APIKey != "sk-second" {
		t.Fatalf("unique alias = found %v ambiguous %v key %q, want the second account", found, ambiguous, mc.APIKey)
	}
}

// Registering a second account is not the same act as choosing it. The wizard
// used to end every save with applyModel, so adding a spare account pulled the
// next turn onto it — while the console and `/model add <alias>` both leave a
// configured model alone. A switch is a choice: Enter in this panel, a click in
// the console, `/model <alias>` in the CLI.
func TestWizardAddOfASpareAccountKeepsTheCurrentOne(t *testing.T) {
	cfg := isolatedUIConfig(t)
	r := &repl{loc: i18n.English, cfg: cfg, configPath: cliConfigPath}
	closeWizardEngine(t, r)
	m := newTUIModel(r)
	m.width, m.height = 100, 30

	// The first model has nothing to displace, so it takes the live slot.
	m = wizardAddDeepSeek(t, m, "sk-first")
	if cfg.Model.APIKey != "sk-first" {
		t.Fatalf("first model = %q, want it active (nothing else was configured)", cfg.Model.APIKey)
	}

	m = wizardAddDeepSeek(t, m, "sk-second")
	if cfg.Model.APIKey != "sk-first" {
		t.Fatalf("adding a second account moved the conversation to it: live=%q", cfg.Model.APIKey)
	}
	if len(cfg.Models) != 2 {
		t.Fatalf("rows = %v, want both accounts", keysOf(cfg.Models))
	}
}

// A config that only has `model:` — the single-model form an older build wrote —
// has no registry row, so the model id is the only name it answers to besides
// its alias. The unified resolver must keep both: dropping the live row from the
// id lookup would leave the one configured model unreachable by the id the
// vendor prints.
func TestResolveModelRowReachesTheLiveModelById(t *testing.T) {
	solo := config.ModelConfig{Name: "solo", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-solo"}
	cfg := &config.Config{Model: solo}

	if mc, found, ambiguous := resolveModelRow(cfg, "deepseek-chat"); !found || ambiguous || mc.APIKey != "sk-solo" {
		t.Fatalf("live-only model by id = found %v ambiguous %v key %q", found, ambiguous, mc.APIKey)
	}
	if mc, found, ambiguous := resolveModelRow(cfg, "solo"); !found || ambiguous || mc.APIKey != "sk-solo" {
		t.Fatalf("live-only model by alias = found %v ambiguous %v key %q", found, ambiguous, mc.APIKey)
	}
}
