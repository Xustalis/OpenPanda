package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
	tea "github.com/charmbracelet/bubbletea"
)

// TestTUISplashScreen verifies splash screen layout, centered content, and key interactions.
func TestTUISplashScreen(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkPath: "/test/workspace/path"},
	}
	r := &repl{loc: i18n.Locale("zh"), cfg: cfg, interactive: true}
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
	if !strings.Contains(wizardView, "DeepSeek") || !strings.Contains(wizardView, "Ollama") {
		t.Fatalf("expected providers in wizard view: %s", wizardView)
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
	_, _ = projStore.Create("OpenPanda", "/path/to/openpanda", "主项目")
	_ = projStore.SetActive("OpenPanda")

	r := &repl{
		loc:        i18n.Locale("zh"),
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
	if !strings.Contains(projView, "OpenPanda") || !strings.Contains(projView, "/path/to/openpanda") {
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
		Model: config.ModelConfig{Name: "deepseek-v4-flash", Model: "deepseek-chat"},
		Models: []config.ModelConfig{
			{Name: "deepseek-v4-flash", Model: "deepseek-chat"},
			{Name: "gpt-4o", Model: "gpt-4o"},
			{Name: "claude-3-5-sonnet", Model: "claude-3-5-sonnet-20241022"},
		},
	}
	r := &repl{
		loc:        i18n.Locale("zh"),
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
	if !strings.Contains(view, "[A] 添加") || !strings.Contains(view, "[D] 删除") || !strings.Contains(view, "[E] 编辑") {
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

	// Reopen panel, test 'a' shortcut to enter wizard
	next, _ = m.submit("/model")
	m = next.(tuiModel)
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.mode != modeModelWizard {
		t.Fatalf("pressing 'a' should enter wizard, got %v", m.mode)
	}
}

// TestTUIModelWizardStepFlow tests the multi-step model creation wizard for Ollama.
func TestTUIModelWizardStepFlow(t *testing.T) {
	cfg := &config.Config{}
	r := &repl{loc: i18n.Locale("zh"), cfg: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	m := newTUIModel(r)
	m.mode = modeIdle
	m.width = 100
	m.height = 30

	// Launch wizard
	next, _ := m.startModelWizard()
	m = next

	// Step 0: Choose provider (select Ollama with 'j' 3 times)
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = step(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	// Press Enter on Ollama -> Ollama has NoAuth, skips directly to model name
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.wizardStep != wizardStepModelName || m.wizardProvider != "ollama" {
		t.Fatalf("expected wizardStepModelName for ollama, got step %v prov %v", m.wizardStep, m.wizardProvider)
	}

	// Press Enter to accept default model name (llama3)
	m = step(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.mode != modeIdle {
		t.Fatalf("expected return to idle after completing wizard, got %v", m.mode)
	}
	if r.cfg.Model.Provider != "ollama" && r.cfg.Model.Model != "llama3" {
		t.Fatalf("expected ollama model configured, got %+v", r.cfg.Model)
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
	if !strings.Contains(modelChoiceView, "Skip for Now") {
		t.Fatalf("expected skip option in view: %s", modelChoiceView)
	}

	// Navigate down to "Skip for Now" using 'j'
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

	// Run the tea.Cmd to simulate background execution and capture
	msg := cmd()
	doneMsg, ok := msg.(execDoneMsg)
	if !ok {
		t.Fatalf("expected execDoneMsg, got %T", msg)
	}
	if doneMsg.text != "/help" {
		t.Fatalf("expected text '/help', got %q", doneMsg.text)
	}
	if !strings.Contains(doneMsg.output, "/help") || !strings.Contains(doneMsg.output, "/skills") {
		t.Fatalf("expected captured output to contain help info, got %q", doneMsg.output)
	}

	// Update with execDoneMsg
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
	if !strings.Contains(view, "/doctor") {
		t.Fatalf("View() should render /doctor from help output: %s", view)
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
