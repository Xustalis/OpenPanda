//go:build !lite

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/providers"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
	tea "github.com/charmbracelet/bubbletea"
)

// TestTUISplashScreen verifies splash screen layout, centered content, and key interactions.
func TestTUISplashScreen(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkPath: "/test/workspace/path"},
	}
	r := &repl{loc: i18n.ChineseSimp, cfg: cfg, interactive: true}
	m := newTUIModel(r)

	if m.mode != modeSplash {
		t.Fatalf("expected initial modeSplash, got %v", m.mode)
	}

	// Sizing
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(tuiModel)

	view := m.View()
	// Logo figlet check
	figLines := figlet("OpenPanda")
	if !strings.Contains(view, strings.TrimSpace(figLines[0])) {
		t.Fatalf("splash should contain logo figlet: %s", view)
	}
	if !strings.Contains(view, "v"+version) {
		t.Fatalf("splash should contain version: %s", view)
	}
	if !strings.Contains(view, "工作目录") || !strings.Contains(view, "/test/workspace/path") {
		t.Fatalf("splash should contain working directory: %s", view)
	}
	if !strings.Contains(view, "Enter 开始 · Q 退出") {
		t.Fatalf("splash should contain bottom prompt: %s", view)
	}

	// Sizing with narrow screen < 76
	nextNarrow, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	viewNarrow := nextNarrow.(tuiModel).View()
	if !strings.Contains(viewNarrow, "OpenPanda") {
		t.Fatalf("narrow splash should contain OpenPanda text: %s", viewNarrow)
	}

	// Pressing Q quits
	qModel, qCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Q")})
	if !qModel.(tuiModel).quitting || !isQuit(qCmd) {
		t.Fatal("pressing Q in splash should quit")
	}

	// Pressing Enter when not onboarded transitions to onboarding wizard
	entModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := entModel.(tuiModel)
	if m2.mode != modeOnboarding || m2.onboardingStep != onboardingStepLanguage {
		t.Fatalf("expected transition to modeOnboarding, got %v (step %v)", m2.mode, m2.onboardingStep)
	}

	// Pressing Enter when already onboarded but no model configured transitions to model wizard
	cfg.UI.Onboarded = true
	mOnboarded := newTUIModel(r)
	nextO, _ := mOnboarded.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	mOnboarded = nextO.(tuiModel)
	entModelWiz, _ := mOnboarded.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mWiz := entModelWiz.(tuiModel)
	if mWiz.mode != modeModelWizard || mWiz.wizardStep != wizardStepProvider {
		t.Fatalf("expected transition to modeModelWizard, got %v (step %v)", mWiz.mode, mWiz.wizardStep)
	}
	wizardView := mWiz.View()
	if !strings.Contains(wizardView, "未配置模型，请选择提供商开始添加：") {
		t.Fatalf("expected onboarding prompt in view: %s", wizardView)
	}
	// The whole catalogue is offered — including the custom/relay entry — even
	// if the rendered window only shows the first rows.
	var sawOllama, sawCustom, sawDeepSeek bool
	for _, it := range mWiz.selectionList.Items {
		sawOllama = sawOllama || it.ID == "ollama"
		sawCustom = sawCustom || it.ID == "custom"
		sawDeepSeek = sawDeepSeek || it.ID == "deepseek"
	}
	if !sawDeepSeek || !sawOllama || !sawCustom {
		t.Fatalf("wizard provider list missing entries (ds=%v ollama=%v custom=%v)", sawDeepSeek, sawOllama, sawCustom)
	}

	// Pressing Enter when a model is configured transitions to modeIdle
	cfg.Model.BaseURL = "http://localhost:11434"
	m3 := newTUIModel(r)
	next3, _ := m3.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m3 = next3.(tuiModel)
	entModel3, _ := m3.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if entModel3.(tuiModel).mode != modeIdle {
		t.Fatalf("expected transition to modeIdle when model configured, got %v", entModel3.(tuiModel).mode)
	}
}

// TestTUIListCommands verifies /sessions, /projects, and /resume keyboard navigation and human-readable fields.
func TestTUIListCommands(t *testing.T) {
	tempDir := t.TempDir()
	sessStore := sessions.NewStore(tempDir)
	sess, err := sessStore.Create("优化TUI项目会话")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = sessStore.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: "请优化系统TUI交互，包括开屏与模型配置"})
	_, _ = sessStore.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "好的，我们开始。"})

	sessWithTurns, err := sessStore.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}

	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatal(err)
	}
	projStore := projectstore.NewStore(db)
	projWorkDir := filepath.Join(t.TempDir(), "openpanda")
	os.MkdirAll(projWorkDir, 0o755)
	_, _ = projStore.Create("OpenPanda", projWorkDir, "主项目")
	_ = projStore.SetActive("OpenPanda")

	r := &repl{
		loc:        i18n.ChineseSimp,
		cfg:        &config.Config{},
		sessionsSt: sessStore,
		projStore:  projStore,
		activeProj: "OpenPanda",
	}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 100
	m.height = 30

	// 1. Test /sessions
	next, _ := m.submit("/sessions")
	m = next.(tuiModel)

	if m.mode != modeList || m.listKind != listSessions {
		t.Fatalf("expected modeList listSessions, got mode %v listKind %v", m.mode, m.listKind)
	}
	view := m.View()
	// Must contain human-readable fields
	if !strings.Contains(view, "1. ") {
		t.Fatalf("sessions view should contain index 1.: %s", view)
	}
	if !strings.Contains(view, "turns") {
		t.Fatalf("sessions view should contain turns: %s", view)
	}
	if !strings.Contains(view, "请优化系统TUI交互") {
		t.Fatalf("sessions view should contain first message snippet: %s", view)
	}
	if strings.Contains(view, sessWithTurns.ID) {
		// Full hash must NOT be displayed, only truncated hash
		t.Fatalf("sessions view must not show full hash ID: %s", view)
	}
	if !strings.Contains(view, sessWithTurns.ID[:6]+"...") {
		t.Fatalf("sessions view should contain truncated hash: %s", view)
	}
	if !strings.Contains(view, "↑↓ 选择 · Enter 确认 · Esc 返回") {
		t.Fatalf("sessions view should contain persistent hints: %s", view)
	}

	// Esc returns to idle
	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeIdle {
		t.Fatalf("Esc should return to idle, got mode %v", m.mode)
	}

	// 2. Test /projects
	next, _ = m.submit("/projects")
	m = next.(tuiModel)
	if m.mode != modeList || m.listKind != listProjects {
		t.Fatalf("expected modeList listProjects, got %v", m.mode)
	}
	projView := m.View()
	if !strings.Contains(projView, "OpenPanda") || !strings.Contains(projView, "openpanda") {
		t.Fatalf("projects view should contain name and workdir: %s", projView)
	}
	if !strings.Contains(projView, "[当前]") {
		t.Fatalf("projects view should mark active project: %s", projView)
	}

	// KJ navigation and Enter confirmation
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeIdle {
		t.Fatalf("Enter in projects list should switch and return to idle, got %v", m.mode)
	}

	// 3. Test /resume
	next, _ = m.submit("/resume")
	m = next.(tuiModel)
	if m.mode != modeList || m.listKind != listResume {
		t.Fatalf("expected modeList listResume, got %v", m.mode)
	}
	resumeView := m.View()
	if !strings.Contains(resumeView, "请优化系统TUI交互") {
		t.Fatalf("resume view should contain message snippet: %s", resumeView)
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeIdle || r.activeSess != sess.ID {
		t.Fatalf("Enter in resume should activate session %s, got activeSess=%s mode=%v", sess.ID, r.activeSess, m.mode)
	}
}

// TestTUIModelManagement verifies existing model panel with [A] [D] [E] shortcuts and Enter switching.
func TestTUIModelManagement(t *testing.T) {
	cfg := &config.Config{
		Model: config.ModelConfig{Name: "deepseek-v4-flash", Provider: "deepseek", Model: "deepseek-chat"},
		Models: []config.ModelConfig{
			{Name: "deepseek-v4-flash", Provider: "deepseek", Model: "deepseek-chat"},
			{Name: "gpt-4o", Provider: "openai", Model: "gpt-4o"},
			{Name: "claude-3-5-sonnet", Provider: "claude", Model: "claude-3-5-sonnet-20241022"},
		},
	}
	r := &repl{
		loc:        i18n.ChineseSimp,
		cfg:        cfg,
		configPath: filepath.Join(t.TempDir(), "config.yaml"),
	}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 100
	m.height = 30

	// Typing /model opens the Boxed Model Management panel
	next, _ := m.submit("/model")
	m = next.(tuiModel)

	if m.mode != modeModelPanel {
		t.Fatalf("expected modeModelPanel, got %v", m.mode)
	}

	view := m.View()
	if !strings.Contains(view, "模型管理") {
		t.Fatalf("expected title 模型管理 in view: %s", view)
	}
	if !strings.Contains(view, "deepseek-v4-flash") || !strings.Contains(view, "[当前]") {
		t.Fatalf("expected deepseek-v4-flash [当前] in view: %s", view)
	}
	if !strings.Contains(view, "gpt-4o") || !strings.Contains(view, "claude-3-5-sonnet") {
		t.Fatalf("expected other models in view: %s", view)
	}
	if !strings.Contains(view, "a 添加") || !strings.Contains(view, "d 删除") || !strings.Contains(view, "e 编辑") || !strings.Contains(view, "t 测试") {
		t.Fatalf("expected action hints in view: %s", view)
	}
	if !strings.Contains(view, "Enter 切换") {
		t.Fatalf("expected Enter 切换 hint in view: %s", view)
	}

	// Move down to gpt-4o using 'j'
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})

	// Press Enter to switch active model
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeIdle {
		t.Fatalf("expected return to idle after model switch, got %v", m.mode)
	}
	if r.cfg.Model.Alias() != "gpt-4o" && r.cfg.Model.Model != "gpt-4o" {
		t.Fatalf("expected active model to switch to gpt-4o, got %+v", r.cfg.Model)
	}

	// Reopen panel, test 'd' deletion confirmation
	next, _ = m.submit("/model")
	m = next.(tuiModel)
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}) // select gpt-4o or claude
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if !m.confirmDeleteModel {
		t.Fatalf("pressing 'd' should prompt for deletion confirmation")
	}
	delView := m.View()
	if !strings.Contains(delView, "确认删除") {
		t.Fatalf("expected deletion confirmation prompt in view: %s", delView)
	}
	// Cancel deletion with 'n'
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.confirmDeleteModel {
		t.Fatalf("pressing 'n' should cancel deletion prompt")
	}
	// Trigger deletion again and confirm with 'y'
	initialModelCount := len(r.cfg.Models)
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(r.cfg.Models) != initialModelCount-1 {
		t.Fatalf("expected model to be deleted after 'y', remaining count: %d", len(r.cfg.Models))
	}

	// Reopen panel, test 'a' shortcut to enter wizard
	next, _ = m.submit("/model")
	m = next.(tuiModel)
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.mode != modeModelWizard {
		t.Fatalf("pressing 'a' should enter wizard, got %v", m.mode)
	}
}

// wizardSelect moves the provider picker's highlight onto id, so the tests
// stay independent of the catalogue's ordering.
func wizardSelect(m tuiModel, id string) tuiModel {
	for i, it := range m.selectionList.Items {
		if it.ID == id {
			m.selectionList.Cursor = i
			return m
		}
	}
	panic("provider not in wizard list: " + id)
}

// formFocusID moves the form editor's focus onto the named field, so tests do
// not depend on field order.
func formFocusID(m tuiModel, id string) tuiModel {
	for i, f := range m.form {
		if f.id == id {
			m.formFocus = i
			return m
		}
	}
	panic("field not in form: " + id)
}

// formTypes types s into the focused field one rune at a time.
func formTypes(m tuiModel, s string) tuiModel {
	for _, ch := range s {
		m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	return m
}

// TestTUIModelWizardStepFlow tests the single-screen model form for Ollama —
// a NoAuth provider whose form has no key field.
func TestTUIModelWizardStepFlow(t *testing.T) {
	cfg := &config.Config{}
	r := &repl{loc: i18n.ChineseSimp, cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 100
	m.height = 30

	// Launch wizard
	next, _ := m.startModelWizard()
	m = next

	// Choose provider — lands on the form with the catalogue defaults filled.
	m = wizardSelect(m, "ollama")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.wizardStep != wizardStepForm || m.wizardProvider != "ollama" {
		t.Fatalf("expected wizardStepForm for ollama, got step %v prov %v", m.wizardStep, m.wizardProvider)
	}
	p, _ := providers.Lookup("ollama")
	if m.wizardModel != p.DefaultModel {
		t.Fatalf("expected prefilled default model %q, got %q", p.DefaultModel, m.wizardModel)
	}
	// NoAuth provider: no API key row.
	for _, f := range m.form {
		if f.id == "api_key" {
			t.Fatal("ollama form should not carry an api_key field")
		}
	}

	// Focus the action row and confirm "test & save".
	m = formFocusID(m, "save")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.formTesting {
		t.Fatal("Enter on the action row should start the connectivity probe")
	}

	// A successful probe finalizes: model persisted + activated.
	m = step(m, wizardTestMsg{err: nil})
	if m.mode != modeIdle {
		t.Fatalf("expected return to idle after completing wizard, got %v", m.mode)
	}
	if r.cfg.Model.Provider != "ollama" || r.cfg.Model.Model != p.DefaultModel {
		t.Fatalf("expected ollama model configured, got %+v", r.cfg.Model)
	}
}

// TestTUIModelWizardCustomRelay walks the custom/relay branch end to end on
// the single-screen form: URL fixup, dialect cycle, optional key, model,
// thinking, context — then a failed probe stays unsaved until the user picks
// the plain "save" action.
func TestTUIModelWizardCustomRelay(t *testing.T) {
	cfg := &config.Config{}
	r := &repl{loc: i18n.English, cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 100
	m.height = 30

	next, _ := m.startModelWizard()
	m = next

	m = wizardSelect(m, "custom")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.wizardStep != wizardStepForm {
		t.Fatalf("custom should land on the form, got step %v", m.wizardStep)
	}
	// Custom forms carry the dialect choice row.
	hasAPIType := false
	for _, f := range m.form {
		if f.id == "api_type" {
			hasAPIType = true
		}
	}
	if !hasAPIType {
		t.Fatal("custom form must include the api_type choice")
	}

	// URL without scheme gets https:// prepended at validation.
	m = formFocusID(m, "base_url")
	m = formTypes(m, "relay.example.com/v1")

	// Anthropic dialect via the choice row's right-arrow cycle.
	m = formFocusID(m, "api_type")
	m = step(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.wizardAPIType != config.APITypeAnthropic {
		t.Fatalf("expected anthropic dialect, got %q", m.wizardAPIType)
	}

	// Blank key stays allowed for custom; fill model/thinking/context.
	m = formFocusID(m, "model")
	m = formTypes(m, "claude-3-7-sonnet")
	m = formFocusID(m, "thinking")
	m = step(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.wizardThinking != "on" {
		t.Fatalf("expected thinking=on, got %q", m.wizardThinking)
	}
	m = formFocusID(m, "context")
	m = formTypes(m, "180000")

	mc := m.wizardConfig()
	if mc.APIType != config.APITypeAnthropic || mc.Thinking != "on" || mc.ContextWindow != 180000 || !mc.NoAuth {
		t.Fatalf("wizardConfig assembled wrong ModelConfig: %+v", mc)
	}

	// Test & Save on the action row starts the probe.
	m = formFocusID(m, "save")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.formTesting {
		t.Fatal("the action row should start the probe")
	}

	// A failed probe must NOT persist the entry; the form stays up to fix.
	m = step(m, wizardTestMsg{err: fmt.Errorf("dial tcp: connection refused")})
	if m.formTestErr == "" || m.formTesting {
		t.Fatalf("probe failure not surfaced: err=%q testing=%v", m.formTestErr, m.formTesting)
	}
	if len(r.cfg.Models) != 0 {
		t.Fatalf("failed probe must not persist a model, got %d", len(r.cfg.Models))
	}

	// Move the action row to the plain "save" button and commit anyway.
	m = step(m, tea.KeyMsg{Type: tea.KeyRight})
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeIdle || len(r.cfg.Models) != 1 {
		t.Fatalf("save-anyway should persist the entry, mode=%v models=%d", m.mode, len(r.cfg.Models))
	}
	saved := r.cfg.Models[0]
	if saved.BaseURL != "https://relay.example.com/v1" || saved.Model != "claude-3-7-sonnet" {
		t.Fatalf("saved entry carries wrong fields: %+v", saved)
	}
}

// TestTUIModelWizardAPIKeyMasking verifies that API Key input is masked with dots in the view and handled correctly.
func TestTUIModelWizardAPIKeyMasking(t *testing.T) {
	cfg := &config.Config{}
	r := &repl{loc: i18n.ChineseSimp, cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 100
	m.height = 30

	// Launch wizard
	next, _ := m.startModelWizard()
	m = next

	// DeepSeek (index 0) requires auth — the form carries a key field.
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.wizardStep != wizardStepForm {
		t.Fatalf("expected the form for deepseek, got %v", m.wizardStep)
	}
	m = formFocusID(m, "api_key")

	// Type secret API key
	secret := "sk-supersecret123"
	m = formTypes(m, secret)

	if m.wizardKey != secret {
		t.Fatalf("expected wizardKey to hold raw secret, got %q", m.wizardKey)
	}

	// Verify view masks API key and does not leak plaintext
	view := m.View()
	if strings.Contains(view, secret) {
		t.Fatalf("view leaked raw API key plaintext: %s", view)
	}
	if !strings.Contains(view, "•••") {
		t.Fatalf("expected masked bullets in view: %s", view)
	}

	// Backspace removes the character left of the cursor.
	m = step(m, tea.KeyMsg{Type: tea.KeyBackspace})
	if m.wizardKey != "sk-supersecret12" {
		t.Fatalf("expected backspace to remove one rune, got %q", m.wizardKey)
	}

	// Cursor editing: Left then insert lands mid-string.
	m = step(m, tea.KeyMsg{Type: tea.KeyLeft})
	m = step(m, tea.KeyMsg{Type: tea.KeyLeft})
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	if m.wizardKey != "sk-supersecretX12" {
		t.Fatalf("expected mid-string insert, got %q", m.wizardKey)
	}
}

// TestTUIModelPanelDetailAndTest covers the redesigned register: the detail
// card shows the highlighted entry's real config, and 't' runs an inline
// connectivity probe whose result lands on the status row.
func TestTUIModelPanelDetailAndTest(t *testing.T) {
	cfg := &config.Config{
		Model: config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com/anthropic", APIKey: "sk-abcd1234"},
		Models: []config.ModelConfig{
			{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com/anthropic", APIKey: "sk-abcd1234"},
		},
	}
	r := &repl{loc: i18n.English, cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 110
	m.height = 30

	next, _ := m.openModelPanel()
	m = next
	if m.mode != modeModelPanel {
		t.Fatalf("expected modeModelPanel, got %v", m.mode)
	}
	view := m.View()
	for _, want := range []string{"deepseek-chat", "DeepSeek", "api.deepseek.com", "anthropic", "••••1234"} {
		if !strings.Contains(view, want) {
			t.Fatalf("panel view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "sk-abcd1234") {
		t.Fatalf("panel leaked the raw API key: %s", view)
	}

	// 't' starts a probe; the arriving panelTestMsg fills the status line.
	next, _ = m.testSelectedModel()
	m = next
	if !m.panelTesting || m.panelTestName != "ds" {
		t.Fatalf("probe not armed: testing=%v name=%q", m.panelTesting, m.panelTestName)
	}
	m = step(m, panelTestMsg{alias: "ds", err: nil, dur: 800 * time.Millisecond})
	if !m.panelTestOK || m.panelTesting {
		t.Fatalf("probe result not recorded: ok=%v testing=%v", m.panelTestOK, m.panelTesting)
	}
	if v := m.View(); !strings.Contains(v, "connected") {
		t.Fatalf("panel should show the probe outcome: %s", v)
	}
}

// TestTUIModelFormFetchPicker covers ^L pulling the endpoint's catalogue into
// a picker and Enter writing the pick into the model field.
func TestTUIModelFormFetchPicker(t *testing.T) {
	cfg := &config.Config{}
	r := &repl{loc: i18n.English, cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 100
	m.height = 30

	next, _ := m.startModelWizard()
	m = next
	m = wizardSelect(m, "openai")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.wizardStep != wizardStepForm {
		t.Fatalf("expected form after provider pick, got %v", m.wizardStep)
	}

	// The fetch command runs async; inject its result.
	m = step(m, modelListMsg{models: []string{"gpt-4o", "gpt-4o-mini", "o3-mini"}})
	if !m.formPicking || len(m.selectionList.Items) != 3 {
		t.Fatalf("picker not open with fetched models: picking=%v items=%d", m.formPicking, len(m.selectionList.Items))
	}
	// The current model value is pre-highlighted.
	if m.selectionList.Cursor != 1 {
		t.Fatalf("expected cursor on gpt-4o-mini (index 1), got %d", m.selectionList.Cursor)
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyDown}) // -> o3-mini
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.formPicking {
		t.Fatal("Enter should close the picker")
	}
	if m.wizardModel != "o3-mini" {
		t.Fatalf("picked model not written into the form, got %q", m.wizardModel)
	}

	// Fetch errors surface inline instead of opening an empty picker.
	m = step(m, modelListMsg{err: fmt.Errorf("401 unauthorized")})
	if m.formPicking || m.formFetchErr == "" {
		t.Fatalf("fetch error not surfaced: picking=%v err=%q", m.formPicking, m.formFetchErr)
	}
}

// TestTUIModelFormValidation covers required-field and context validation on
// the save path.
func TestTUIModelFormValidation(t *testing.T) {
	cfg := &config.Config{}
	r := &repl{loc: i18n.English, cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 100
	m.height = 30

	next, _ := m.startModelWizard()
	m = next
	m = wizardSelect(m, "deepseek")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})

	// No key: validation refuses the save and focuses the field.
	m = formFocusID(m, "save")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.formErr == "" || m.formTesting {
		t.Fatalf("missing key must block the probe: err=%q testing=%v", m.formErr, m.formTesting)
	}
	if f := m.focusedField(); f == nil || f.id != "api_key" {
		t.Fatalf("validation should focus api_key, focused %v", f)
	}

	// Fill the key, then a non-numeric context is refused the same way.
	m = formFocusID(m, "api_key")
	m = formTypes(m, "sk-test")
	m = formFocusID(m, "context")
	m = step(m, tea.KeyMsg{Type: tea.KeyCtrlU}) // clear prefilled default
	m = formTypes(m, "lots")
	m = formFocusID(m, "save")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.formErr == "" || m.formTesting {
		t.Fatalf("non-numeric context must block the probe: err=%q testing=%v", m.formErr, m.formTesting)
	}
	if f := m.focusedField(); f == nil || f.id != "context" {
		t.Fatalf("validation should focus context, focused %v", f)
	}
}

// TestTUIModelFormEscape covers the form's back navigation: Esc on an add
// returns to the provider picker; Esc on an edit returns to the register.
func TestTUIModelFormEscape(t *testing.T) {
	cfg := &config.Config{
		Model:  config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com/anthropic", APIKey: "sk-x"},
		Models: []config.ModelConfig{{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", BaseURL: "https://api.deepseek.com/anthropic", APIKey: "sk-x"}},
	}
	r := &repl{loc: i18n.English, cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 100
	m.height = 30

	// Edit flow: 'e' on the panel opens the prefilled form; Esc returns to it.
	next, _ := m.openModelPanel()
	m = next
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if m.mode != modeModelWizard || m.wizardStep != wizardStepForm || m.wizardEditAlias != "ds" {
		t.Fatalf("edit did not open the prefilled form: mode=%v step=%v alias=%q", m.mode, m.wizardStep, m.wizardEditAlias)
	}
	if m.wizardKey != "sk-x" || m.wizardModel != "deepseek-chat" {
		t.Fatalf("form not prefilled: key=%q model=%q", m.wizardKey, m.wizardModel)
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeModelPanel {
		t.Fatalf("Esc on an edit should return to the panel, got %v", m.mode)
	}

	// Add flow: 'a' -> provider pick -> Esc on the form returns to the picker.
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = wizardSelect(m, "openai")
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.wizardStep != wizardStepProvider {
		t.Fatalf("Esc on an add form should return to the provider picker, got %v", m.wizardStep)
	}
	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeModelPanel {
		t.Fatalf("Esc on the picker should return to the panel, got %v", m.mode)
	}
}

// TestTUIFirstRunOnboardingFlow tests the full onboarding wizard flow from splash to idle.
func TestTUIFirstRunOnboardingFlow(t *testing.T) {
	// 1. English default flow with Skip Model
	cfg := &config.Config{}
	r := &repl{loc: i18n.Locale("en"), cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	m := newTUIModel(r)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(tuiModel)

	if m.mode != modeSplash {
		t.Fatalf("expected initial modeSplash, got %v", m.mode)
	}

	// Press Enter from Splash -> enters onboardingStepLanguage
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	if m.mode != modeOnboarding || m.onboardingStep != onboardingStepLanguage {
		t.Fatalf("expected modeOnboarding step language, got %v / %v", m.mode, m.onboardingStep)
	}

	langView := m.View()
	if !strings.Contains(langView, "Welcome to OpenPanda") || !strings.Contains(langView, "English") {
		t.Fatalf("expected English language option in view: %s", langView)
	}
	if !strings.Contains(langView, "[Default]") {
		t.Fatalf("expected default badge for English in view: %s", langView)
	}

	// Confirm English (default item 0)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	if m.onboardingStep != onboardingStepTerms {
		t.Fatalf("expected onboardingStepTerms, got %v", m.onboardingStep)
	}
	if m.loc != i18n.English {
		t.Fatalf("expected locale to be English, got %v", m.loc)
	}

	// Verify rich Terms view in English
	termsView := m.View()
	for _, expectedText := range []string{
		"MIT Open Source License",
		"Local-First Architecture & Privacy Autonomy",
		"AI Generation Advisory & Risk Disclaimer",
		"System Execution & Operational Safety",
		"> [Y] Agree and Continue",
		"[N] Decline and Exit",
	} {
		if !strings.Contains(termsView, expectedText) {
			t.Fatalf("terms view missing expected clause '%s':\n%s", expectedText, termsView)
		}
	}

	// Press Y to accept terms -> enters onboardingStepApproval
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(tuiModel)
	if m.onboardingStep != onboardingStepApproval {
		t.Fatalf("expected onboardingStepApproval, got %v", m.onboardingStep)
	}
	if !cfg.UI.TermsAccepted {
		t.Fatalf("expected TermsAccepted to be true")
	}

	// Verify Approval Mode view
	approvalView := m.View()
	if !strings.Contains(approvalView, "Choose Execution Safety & Approval Mode:") {
		t.Fatalf("expected approval mode title in view: %s", approvalView)
	}
	if !strings.Contains(approvalView, "Interactive Approval (Recommended)") {
		t.Fatalf("expected interactive approval option in view: %s", approvalView)
	}

	// Confirm Interactive Approval (item 0) -> enters onboardingStepModelChoice
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	if m.onboardingStep != onboardingStepModelChoice {
		t.Fatalf("expected onboardingStepModelChoice, got %v", m.onboardingStep)
	}
	if cfg.Approval.Mode != "prompt" {
		t.Fatalf("expected approval mode 'prompt', got %v", cfg.Approval.Mode)
	}

	// Verify Model Choice view
	modelChoiceView := m.View()
	if !strings.Contains(modelChoiceView, "Configure Large Language Model") {
		t.Fatalf("expected model choice title in view: %s", modelChoiceView)
	}
	if !strings.Contains(modelChoiceView, "Configure Later (Skip)") {
		t.Fatalf("expected skip option in view: %s", modelChoiceView)
	}

	// Navigate down to "Configure Later (Skip)" using 'j'
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = next.(tuiModel)

	// Press Enter to skip model setup and complete onboarding
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)

	if m.mode != modeIdle {
		t.Fatalf("expected modeIdle after completing onboarding, got %v", m.mode)
	}
	if !cfg.UI.Onboarded {
		t.Fatalf("expected Onboarded to be true")
	}
	if !cfg.UI.TermsAccepted {
		t.Fatalf("expected TermsAccepted to be true")
	}

	// Verify completion batch includes note block
	if cmd == nil {
		t.Fatal("expected non-nil tea.Cmd batch on onboarding finalization")
	}

	// 2. Terms Rejection test: pressing N declines and quits
	mDecline := newTUIModel(&repl{cfg: &config.Config{}, configPath: filepath.Join(t.TempDir(), "config.yaml")})
	mDecline.mode = modeOnboarding
	mDecline.onboardingStep = onboardingStepTerms
	mDecline.termsCursor = 1 // Pointing to Decline [N]
	nextDec, cmdDec := mDecline.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !nextDec.(tuiModel).quitting || !isQuit(cmdDec) {
		t.Fatal("confirming decline on terms should quit")
	}

	// 3. Chinese Selection flow
	cfgZh := &config.Config{}
	rZh := &repl{cfg: cfgZh, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	mZh := newTUIModel(rZh)
	mZh.mode = modeOnboarding
	mZh.onboardingStep = onboardingStepLanguage
	mZh.selectionList = NewSelectionList("Select", buildLanguageItems())

	// Move down to 简体中文
	nextZh, _ := mZh.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	mZh = nextZh.(tuiModel)
	// Confirm 简体中文
	nextZh, _ = mZh.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mZh = nextZh.(tuiModel)
	if mZh.loc != i18n.ChineseSimp {
		t.Fatalf("expected ChineseSimp locale, got %v", mZh.loc)
	}

	// Check Chinese terms
	zhTermsView := mZh.View()
	for _, expectedText := range []string{
		"MIT 开源许可证声明",
		"本地优先与数据自治原则",
		"AI 生成内容免责声明",
		"命令执行与系统安全须知",
		"同意并继续",
	} {
		if !strings.Contains(zhTermsView, expectedText) {
			t.Fatalf("Chinese terms view missing '%s':\n%s", expectedText, zhTermsView)
		}
	}
}

// TestTUISlashCommandExecutionAndOutputPersisted verifies that slash commands
// (such as /help, /status) capture stdout and persist their output in chatHistory
// rather than disappearing and refreshing the view in AltScreen mode.
func TestTUISlashCommandExecutionAndOutputPersisted(t *testing.T) {
	r := &repl{
		loc:         i18n.ChineseSimp,
		cfg:         &config.Config{},
		interactive: true,
	}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 100
	m.height = 30

	// 1. Submit /help
	next, cmd := m.submit("/help")
	m = next.(tuiModel)
	if m.mode != modeExec {
		t.Fatalf("expected modeExec during slash dispatch, got %v", m.mode)
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd for slash execution")
	}

	// Pump progressive output until the execution's matching terminal event.
	var doneMsg execDoneMsg
	for i := 0; i < 1024; i++ {
		msg := cmd()
		switch msg := msg.(type) {
		case execOutputMsg:
			next, cmd = m.Update(msg)
			m = next.(tuiModel)
			if cmd == nil {
				t.Fatal("progressive output should re-arm the command event pump")
			}
		case execDoneMsg:
			doneMsg = msg
			cmd = nil
		}
		if cmd == nil {
			break
		}
	}
	if doneMsg.exec == nil {
		t.Fatal("command did not produce a terminal event")
	}
	if doneMsg.text != "/help" {
		t.Fatalf("expected text '/help', got %q", doneMsg.text)
	}
	if !strings.Contains(doneMsg.output, "/help") || !strings.Contains(doneMsg.output, "/skills") {
		t.Fatalf("expected captured output to contain help info, got %q", doneMsg.output)
	}

	// Update with execDoneMsg.
	next, _ = m.Update(doneMsg)
	m = next.(tuiModel)

	if m.mode != modeIdle {
		t.Fatalf("expected modeIdle after command finish, got %v", m.mode)
	}
	if m.chatHistory == nil || len(m.chatHistory.blocks) < 2 {
		t.Fatalf("expected at least 2 blocks (user + output), got %d", len(m.chatHistory.blocks))
	}
	if m.chatHistory.blocks[0].body != "/help" {
		t.Fatalf("expected first block to be user command /help, got %q", m.chatHistory.blocks[0].body)
	}
	if !strings.Contains(m.chatHistory.blocks[1].body, "/skills") {
		t.Fatalf("expected second block to contain help text, got %q", m.chatHistory.blocks[1].body)
	}

	// Verify View() renders command and output at bottom, and scrolling up reveals earlier items
	view := m.View()
	if !strings.Contains(view, "/quit") {
		t.Fatalf("View() should render the tail of the help output: %s", view)
	}

	// Pressing PgUp scrolls up to reveal /skills from earlier in the help text
	mUp, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	viewUp := mUp.(tuiModel).View()
	if !strings.Contains(viewUp, "/skills") {
		t.Fatalf("Scrolled View() should render /skills from earlier in help output: %s", viewUp)
	}

	// 2. Clear history
	next, _ = m.submit("/clear")
	m = next.(tuiModel)
	if m.chatHistory != nil && len(m.chatHistory.blocks) != 0 {
		t.Fatalf("expected /clear to wipe chatHistory blocks, got %d", len(m.chatHistory.blocks))
	}
}

func TestTUICommandExecutionCancellationAndGenerationIsolation(t *testing.T) {
	m := newTestTUI(t)
	m.mode = modeExec
	m.execGen = 2
	m.exec = newCommandExec(m.execGen)
	current := m.exec
	stale := newCommandExec(1)

	for _, msg := range []tea.Msg{
		execOutputMsg{exec: stale, generation: 1, text: "stale output"},
		execDoneMsg{exec: stale, generation: 1, text: "!stale", output: "stale done"},
	} {
		next, cmd := m.Update(msg)
		got := next.(tuiModel)
		if cmd != nil || got.mode != modeExec || got.exec != current || got.execText.Len() != 0 {
			t.Fatalf("stale %T changed current execution: mode=%v exec=%p text=%q cmd=%v", msg, got.mode, got.exec, got.execText.String(), cmd)
		}
		m = got
	}

	m = step(m, tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case <-current.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Esc did not cancel the current command context")
	}
	if m.mode != modeExec || m.exec != current {
		t.Fatalf("cancel must wait for the matching terminal event: mode=%v exec=%p", m.mode, m.exec)
	}
	m = step(m, execDoneMsg{exec: current, generation: 2, text: "!sleep", err: context.Canceled})
	if m.mode != modeIdle || m.exec != nil {
		t.Fatalf("matching cancellation did not terminalize execution: mode=%v exec=%p", m.mode, m.exec)
	}
}

func TestTUIShellCommandStreamsProgressAndLongOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX printf")
	}
	r := &repl{loc: i18n.Locale("en"), cfg: &config.Config{}, interactive: true}
	r.cfg.Storage.WorkPath = t.TempDir()
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width, m.height = 80, 24
	// The 128 KiB of output is produced by the child, never carried on its
	// command line. Linux caps a single argv string at MAX_ARG_STRLEN (32
	// pages, 128 KiB) and fails execve with E2BIG beyond it: inlining the
	// payload made the `-c` argument 131,125 bytes — 53 over the cap — so the
	// shell never started and Linux runs reported "fork/exec /bin/bash:
	// argument list too long" instead of the payload. macOS allows roughly an
	// order of magnitude more per argument, which is why this only ever broke
	// in CI.
	const payloadBytes = 128 * 1024
	shellCmd := fmt.Sprintf(
		"printf first; sleep 0.05; printf ' second'; head -c %d /dev/zero | tr '\\0' x",
		payloadBytes)
	// Guard the fixture, not just the bug: the output size is the point of
	// this test, so the bytes have to stay off argv however the command is
	// edited later.
	if len(shellCmd) > 4*1024 {
		t.Fatalf("shell fixture command line is %d bytes; have the child generate the bulk instead of passing it as an argument", len(shellCmd))
	}

	next, cmd := m.submit("!" + shellCmd)
	m = next.(tuiModel)
	if m.mode != modeExec || cmd == nil {
		t.Fatal("shell command did not enter progressive execution mode")
	}
	seenProgress := false
	for i := 0; i < 4096; i++ {
		msg := cmd()
		switch msg := msg.(type) {
		case execOutputMsg:
			seenProgress = true
			next, cmd = m.Update(msg)
			m = next.(tuiModel)
		case execDoneMsg:
			next, _ = m.Update(msg)
			m = next.(tuiModel)
			cmd = nil
		}
		if cmd == nil {
			break
		}
	}
	if !seenProgress || m.mode != modeIdle || len(m.chatHistory.blocks) < 2 {
		t.Fatalf("shell execution did not complete progressively: progress=%v mode=%v blocks=%d", seenProgress, m.mode, len(m.chatHistory.blocks))
	}
	out := m.chatHistory.blocks[len(m.chatHistory.blocks)-1].body
	if !strings.HasPrefix(out, "first second") || len(out) < payloadBytes {
		t.Fatalf("long shell output was truncated or reordered: len=%d prefix=%q", len(out), out[:min(20, len(out))])
	}
}

// TestTUIChatHistoryScrolling verifies that PgUp, PgDown, and mouse wheel
// scroll chatHistory and display a scroll indicator when scrolled up.
func TestTUIChatHistoryScrolling(t *testing.T) {
	r := &repl{
		loc:         i18n.ChineseSimp,
		cfg:         &config.Config{},
		interactive: true,
	}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 100
	m.height = 20

	// Add 30 blocks to exceed available height
	for i := 0; i < 30; i++ {
		m.chatHistory.blocks = append(m.chatHistory.blocks, block{
			kind: blockUser,
			body: fmt.Sprintf("Turn user message number %02d", i),
		})
	}

	// Initial view is anchored at bottom (scrollOffset = 0)
	if m.scrollOffset != 0 {
		t.Fatalf("initial scrollOffset should be 0, got %d", m.scrollOffset)
	}
	viewBottom := m.View()
	if !strings.Contains(viewBottom, "Turn user message number 29") {
		t.Fatalf("bottom view should show the latest message: %s", viewBottom)
	}

	// Press PgUp to scroll up
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(tuiModel)
	if m.scrollOffset <= 0 {
		t.Fatalf("expected scrollOffset > 0 after PgUp, got %d", m.scrollOffset)
	}

	viewScrolled := m.View()
	if !strings.Contains(viewScrolled, "浏览历史") && !strings.Contains(viewScrolled, "偏移") {
		t.Fatalf("scrolled view should display scroll indicator: %s", viewScrolled)
	}

	// Mouse wheel up scrolls further up
	prevOffset := m.scrollOffset
	next, _ = m.Update(tea.MouseMsg{Type: tea.MouseWheelUp})
	m = next.(tuiModel)
	if m.scrollOffset != prevOffset+3 {
		t.Fatalf("expected scrollOffset to increase by 3, got %d (was %d)", m.scrollOffset, prevOffset)
	}

	// Mouse wheel down scrolls back down
	next, _ = m.Update(tea.MouseMsg{Type: tea.MouseWheelDown})
	m = next.(tuiModel)
	if m.scrollOffset != prevOffset {
		t.Fatalf("expected scrollOffset to decrease by 3, got %d", m.scrollOffset)
	}

	// Esc returns to bottom
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(tuiModel)
	if m.scrollOffset != 0 {
		t.Fatalf("expected scrollOffset to reset to 0 after Esc, got %d", m.scrollOffset)
	}
}

// TestTUIListModeWheelScrolling verifies that mouse wheel events page the
// selection highlight in the full-screen list modes (they share onMouse now
// that runTUI captures the mouse) while the transcript scrollOffset stays
// untouched.
func TestTUIListModeWheelScrolling(t *testing.T) {
	m := newTestTUI(t)
	m.mode = modeList
	m.listKind = listSessions
	m.width, m.height = 80, 24
	var items []SelectionItem
	for i := 1; i <= 30; i++ {
		items = append(items, SelectionItem{Index: i, Title: fmt.Sprintf("s-%d", i), ID: fmt.Sprintf("id-%d", i)})
	}
	m.selectionList = NewSelectionList("sessions", items)

	next, _ := m.Update(tea.MouseMsg{Type: tea.MouseWheelDown})
	m = next.(tuiModel)
	if m.selectionList.Cursor != 3 {
		t.Fatalf("wheel down should advance the highlight 3 rows, cursor=%d", m.selectionList.Cursor)
	}
	if m.scrollOffset != 0 {
		t.Fatalf("list modes must not touch the transcript scrollOffset, got %d", m.scrollOffset)
	}

	next, _ = m.Update(tea.MouseMsg{Type: tea.MouseWheelUp})
	m = next.(tuiModel)
	if m.selectionList.Cursor != 0 {
		t.Fatalf("wheel up should retreat the highlight 3 rows, cursor=%d", m.selectionList.Cursor)
	}

	// PgDn still works alongside the wheel.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = next.(tuiModel)
	if m.selectionList.Cursor != m.listPageRows() {
		t.Fatalf("PgDn should jump one page (%d), cursor=%d", m.listPageRows(), m.selectionList.Cursor)
	}
}

func TestTUIScrollbarRendering(t *testing.T) {
	r := &repl{
		loc:         i18n.English,
		cfg:         &config.Config{},
		interactive: true,
	}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 80
	m.height = 20

	// Add 40 blocks to easily exceed height 20
	for i := 0; i < 40; i++ {
		m.chatHistory.blocks = append(m.chatHistory.blocks, block{
			kind: blockUser,
			body: fmt.Sprintf("Message %02d", i),
		})
	}

	viewBottom := m.View()
	// Should render vertical scrollbar thumb or track on right margin
	if !strings.Contains(viewBottom, "█") && !strings.Contains(viewBottom, "│") && !strings.Contains(viewBottom, "#") {
		t.Fatalf("expected view with overflowing content to render scrollbar character, got:\n%s", viewBottom)
	}

	// Scroll to top with KeyHome
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = next.(tuiModel)
	if m.scrollOffset <= 0 {
		t.Fatalf("expected scrollOffset > 0 after KeyHome, got %d", m.scrollOffset)
	}
	viewTop := m.View()
	if viewTop == viewBottom {
		t.Fatal("top view and bottom view should differ with scrollbar and position")
	}

	// Return to bottom with KeyEnd
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = next.(tuiModel)
	if m.scrollOffset != 0 {
		t.Fatalf("expected scrollOffset to reset to 0 after KeyEnd, got %d", m.scrollOffset)
	}
}

func TestTUIArrowNavigationWhenScrolled(t *testing.T) {
	r := &repl{
		loc:         i18n.English,
		cfg:         &config.Config{},
		interactive: true,
	}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 80
	m.height = 20

	for i := 0; i < 40; i++ {
		m.chatHistory.blocks = append(m.chatHistory.blocks, block{
			kind: blockUser,
			body: fmt.Sprintf("Message %02d", i),
		})
	}

	// Initial render
	_ = m.View()

	// PgUp to enter history browsing
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(tuiModel)
	scrolled := m.scrollOffset
	if scrolled <= 0 {
		t.Fatalf("expected scrollOffset > 0, got %d", scrolled)
	}

	// Up arrow should scroll further back
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(tuiModel)
	if m.scrollOffset != scrolled+transcriptScrollStep {
		t.Fatalf("KeyUp when scrolled should increase offset by %d, got %d (was %d)",
			transcriptScrollStep, m.scrollOffset, scrolled)
	}

	// Down arrow should scroll forward toward bottom
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(tuiModel)
	if m.scrollOffset != scrolled {
		t.Fatalf("KeyDown when scrolled should decrease offset back to %d, got %d",
			scrolled, m.scrollOffset)
	}

	// Typing a character in textarea should not snap scrollOffset to 0
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = next.(tuiModel)
	if m.scrollOffset != scrolled {
		t.Fatalf("typing should not reset scrollOffset to 0, got %d (wanted %d)", m.scrollOffset, scrolled)
	}

	// Submitting with Enter resets to 0
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	if m.scrollOffset != 0 {
		t.Fatalf("Enter should reset scrollOffset to 0, got %d", m.scrollOffset)
	}
}

// TestNoInvalidEscapeFragmentsInInputBar verifies that leaked escape fragments,
// SGR mouse movements, CPR reports, and Alt-prefixed residues are never printed
// into the user prompt input bar.
func TestNoInvalidEscapeFragmentsInInputBar(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkPath: "/test"},
		Model:   config.ModelConfig{BaseURL: "http://localhost:8080", Model: "test"},
		UI:      config.UIConfig{Onboarded: true, TermsAccepted: true},
	}
	r := &repl{loc: i18n.English, cfg: cfg, interactive: true}
	m := newTUIModel(r)
	// Enter idle mode
	m.mode = modeIdle
	m.width, m.height = 100, 30

	leakedInputs := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'['}, Alt: true},
		{Type: tea.KeyRunes, Runes: []rune{'O'}, Alt: true},
		{Type: tea.KeyRunes, Runes: []rune{']'}, Alt: true},
		{Type: tea.KeyRunes, Runes: []rune{'?'}, Alt: true},
		{Type: tea.KeyRunes, Runes: []rune("<35;12;34M")},
		{Type: tea.KeyRunes, Runes: []rune("<35;12;34m")},
		{Type: tea.KeyRunes, Runes: []rune("[<35;12;34M")},
		{Type: tea.KeyRunes, Runes: []rune(";12;34M")},
		{Type: tea.KeyRunes, Runes: []rune(";34M")},
		{Type: tea.KeyRunes, Runes: []rune("[<")},
		{Type: tea.KeyRunes, Runes: []rune("<35;12")},
		{Type: tea.KeyRunes, Runes: []rune("[24;80R")},
		{Type: tea.KeyRunes, Runes: []rune("24;80R")},
		{Type: tea.KeyRunes, Runes: []rune(";80R")},
		{Type: tea.KeyRunes, Runes: []rune("[?1002h")},
		{Type: tea.KeyRunes, Runes: []rune("?1007h")},
		{Type: tea.KeyRunes, Runes: []rune("[I")},
		{Type: tea.KeyRunes, Runes: []rune("[O")},
		{Type: tea.KeyRunes, Runes: []rune("[200~")},
		{Type: tea.KeyRunes, Runes: []rune("[1;2A")},
		{Type: tea.KeyRunes, Runes: []rune("[3~")},
		{Type: tea.KeyRunes, Runes: []rune("]11;rgb:0000/0000/0000")},
	}

	for _, msg := range leakedInputs {
		next, _ := m.Update(msg)
		m = next.(tuiModel)
		if val := m.ta.Value(); val != "" {
			t.Fatalf("leaked input %v inserted %q into input bar", msg, val)
		}
	}

	// Normal valid characters should still work properly
	validMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")}
	next, _ := m.Update(validMsg)
	m = next.(tuiModel)
	if m.ta.Value() != "hello" {
		t.Fatalf("valid typing failed, got %q, want 'hello'", m.ta.Value())
	}
}
