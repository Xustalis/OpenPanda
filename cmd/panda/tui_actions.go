package main

import (
	"context"
	"fmt"
	"path/filepath"
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
			badge = "[当前]"
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
			badge = "[当前]"
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
			Badge: "[当前]",
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

// buildProviderItems returns the core 4 providers for onboarding/adding.
func buildProviderItems() []SelectionItem {
	return []SelectionItem{
		{ID: "deepseek", Title: "DeepSeek"},
		{ID: "openai", Title: "OpenAI"},
		{ID: "anthropic", Title: "Anthropic"},
		{ID: "ollama", Title: "Ollama (本地)"},
	}
}

// openSessionsList switches to the interactive sessions view.
func (m tuiModel) openSessionsList() (tuiModel, tea.Cmd) {
	items := m.buildSessionItems()
	sl := NewSelectionList("会话列表", items)
	sl.EmptyText = "暂无历史会话。输入消息直接开始新对话。"
	sl.FooterHints = "↑↓ 选择 · Enter 确认 · Esc 返回"

	m.mode = modeList
	m.listKind = listSessions
	m.selectionList = sl
	return m, nil
}

// openProjectsList switches to the interactive projects view.
func (m tuiModel) openProjectsList() (tuiModel, tea.Cmd) {
	items := m.buildProjectItems()
	sl := NewSelectionList("项目列表", items)
	sl.EmptyText = "暂无已保存的项目。可使用 /project <name> 创建新项目。"
	sl.FooterHints = "↑↓ 选择 · Enter 确认 · Esc 返回"

	m.mode = modeList
	m.listKind = listProjects
	m.selectionList = sl
	return m, nil
}

// openResumeList switches to the interactive resume view.
func (m tuiModel) openResumeList() (tuiModel, tea.Cmd) {
	items := m.buildSessionItems()
	sl := NewSelectionList("恢复会话", items)
	sl.EmptyText = "暂无历史会话可供恢复。"
	sl.FooterHints = "↑↓ 选择 · Enter 确认 · Esc 返回"

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
	sl := NewSelectionList("模型管理", items)
	sl.Boxed = true
	sl.ActionHints = "[A] 添加  [D] 删除  [E] 编辑"
	sl.FooterHints = "↑↓ 选择 · Enter 切换 · Esc 返回"

	m.mode = modeModelPanel
	m.selectionList = sl
	return m, nil
}

// startModelWizard starts the step-by-step model setup guide.
func (m tuiModel) startModelWizard() (tuiModel, tea.Cmd) {
	items := buildProviderItems()
	sl := NewSelectionList("未配置模型，请选择提供商开始添加：", items)
	sl.FooterHints = "↑↓ 选择 · Enter 确认 · Esc 返回"

	m.mode = modeModelWizard
	m.wizardStep = wizardStepProvider
	m.wizardProvider = ""
	m.wizardKey = ""
	m.wizardModel = ""
	m.wizardInput = ""
	m.selectionList = sl
	return m, nil
}

// handleListKey routes keys in modeList (sessions, projects, resume).
func (m tuiModel) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.selectionList.MoveUp()
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.selectionList.MoveDown(max(3, m.height-6))
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
			actionLabel := "已切换到会话"
			if m.listKind == listResume {
				actionLabel = "已恢复会话"
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
				body: fmt.Sprintf("已切换到项目: %s", item.Title),
			}
			return m, m.printBlock(note)
		}
	}
	return m, nil
}

// handleModelPanelKey routes keys inside the Boxed Model Management panel.
func (m tuiModel) handleModelPanelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.selectionList.MoveUp()
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.selectionList.MoveDown(8)
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
			body: fmt.Sprintf("已切换到模型: %s (%s)", targetMC.Alias(), effectiveModel(*targetMC)),
		}
		return m, m.printBlock(note)
	}

	m.mode = modeIdle
	return m, nil
}

// deleteSelectedModel removes the highlighted model.
func (m tuiModel) deleteSelectedModel() (tuiModel, tea.Cmd) {
	item, ok := m.selectionList.Selected()
	if !ok || m.r == nil || m.r.cfg == nil {
		return m, nil
	}

	alias := item.ID
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
		prov = "openai"
	}
	m.mode = modeModelWizard
	m.wizardProvider = prov
	m.wizardKey = curMC.APIKey
	m.wizardModel = curMC.Model
	m.wizardInput = curMC.APIKey
	if p, ok := providers.Lookup(prov); ok && p.NoAuth {
		m.wizardStep = wizardStepModelName
		m.wizardInput = curMC.Model
	} else {
		m.wizardStep = wizardStepAPIKey
	}
	return m, nil
}

// handleModelWizardKey handles keystrokes in the onboarding/add guide.
func (m tuiModel) handleModelWizardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.wizardStep {
	case wizardStepProvider:
		switch msg.Type {
		case tea.KeyUp, tea.KeyCtrlP:
			m.selectionList.MoveUp()
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN:
			m.selectionList.MoveDown(4)
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
				m.selectionList.MoveDown(4)
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
			m.wizardProvider = item.ID
			if p, ok := providers.Lookup(item.ID); ok && p.NoAuth {
				m.wizardStep = wizardStepModelName
				m.wizardInput = p.DefaultModel
			} else {
				m.wizardStep = wizardStepAPIKey
				m.wizardInput = ""
			}
			return m, nil
		}

	case wizardStepAPIKey:
		switch msg.Type {
		case tea.KeyEsc:
			return m.startModelWizard()
		case tea.KeyEnter:
			m.wizardKey = strings.TrimSpace(m.wizardInput)
			m.wizardStep = wizardStepModelName
			defModel := ""
			if p, ok := providers.Lookup(m.wizardProvider); ok {
				defModel = p.DefaultModel
			}
			m.wizardInput = defModel
			return m, nil
		case tea.KeyBackspace, tea.KeyCtrlH:
			if len(m.wizardInput) > 0 {
				m.wizardInput = m.wizardInput[:len(m.wizardInput)-1]
			}
			return m, nil
		case tea.KeyRunes:
			m.wizardInput += string(msg.Runes)
			return m, nil
		}

	case wizardStepModelName:
		switch msg.Type {
		case tea.KeyEsc:
			if p, ok := providers.Lookup(m.wizardProvider); ok && p.NoAuth {
				return m.startModelWizard()
			}
			m.wizardStep = wizardStepAPIKey
			m.wizardInput = m.wizardKey
			return m, nil
		case tea.KeyEnter:
			modelName := strings.TrimSpace(m.wizardInput)
			if modelName == "" {
				if p, ok := providers.Lookup(m.wizardProvider); ok {
					modelName = p.DefaultModel
				}
			}
			m.wizardModel = modelName
			return m.finalizeWizard()
		case tea.KeyBackspace, tea.KeyCtrlH:
			if len(m.wizardInput) > 0 {
				m.wizardInput = m.wizardInput[:len(m.wizardInput)-1]
			}
			return m, nil
		case tea.KeyRunes:
			m.wizardInput += string(msg.Runes)
			return m, nil
		}
	}
	return m, nil
}

// finalizeWizard completes adding/updating model, sets as active, and returns to chat.
func (m tuiModel) finalizeWizard() (tuiModel, tea.Cmd) {
	if m.r == nil || m.r.cfg == nil {
		m.mode = modeIdle
		return m, nil
	}

	mc, ok := providers.ModelConfig(m.wizardProvider, m.wizardModel, m.wizardKey)
	if !ok {
		note := block{kind: blockError, body: "未知模型提供商: " + m.wizardProvider}
		m.mode = modeIdle
		return m, m.printBlock(note)
	}

	alias := m.wizardProvider
	if m.wizardModel != "" && m.wizardModel != alias {
		alias = m.wizardModel
	}
	mc.Name = alias

	// Add to cfg.Models
	replaced := false
	for i := range m.r.cfg.Models {
		if m.r.cfg.Models[i].Alias() == alias || m.r.cfg.Models[i].Model == mc.Model {
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
		body: fmt.Sprintf("已成功添加并启用模型: %s (%s) [当前]", alias, mc.Model),
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
	sl.FooterHints = "↑↓ / KJ Move · Enter Confirm · Esc Exit"
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
	if loc == i18n.ChineseSimp {
		return []SelectionItem{
			{
				Index:   1,
				ID:      "prompt",
				Title:   "交互确认模式 (推荐)",
				Snippet: "对文件修改与系统命令提示确认，安全平衡",
				Badge:   "[推荐]",
			},
			{
				Index:   2,
				ID:      "auto",
				Title:   "只读自动放行模式",
				Snippet: "只读检索自动执行，写操作与高危命令需确认",
			},
			{
				Index:   3,
				ID:      "strict",
				Title:   "严格审查模式",
				Snippet: "所有工具调用与执行操作均需手动逐项确认",
			},
		}
	}
	return []SelectionItem{
		{
			Index:   1,
			ID:      "prompt",
			Title:   "Interactive Approval (Recommended)",
			Snippet: "Prompts for confirmation on file edits and shell commands",
			Badge:   "[Recommended]",
		},
		{
			Index:   2,
			ID:      "auto",
			Title:   "Auto-Approve Read-Only",
			Snippet: "Read-only inspection runs automatically; writes prompt",
		},
		{
			Index:   3,
			ID:      "strict",
			Title:   "Strict Approval",
			Snippet: "Prompt and review every single tool call and command",
		},
	}
}

// buildModelChoiceItems returns the model configuration choice options.
func buildModelChoiceItems(loc i18n.Locale) []SelectionItem {
	if loc == i18n.ChineseSimp {
		return []SelectionItem{
			{
				Index:   1,
				ID:      "now",
				Title:   "立即配置大模型",
				Snippet: "设置 DeepSeek / OpenAI / Anthropic / Ollama 等提供商",
				Badge:   "[快速开始]",
			},
			{
				Index:   2,
				ID:      "skip",
				Title:   "稍后再配置 (跳过)",
				Snippet: "先进入主界面，随时可通过输入 /model 进行配置",
			},
		}
	}
	return []SelectionItem{
		{
			Index:   1,
			ID:      "now",
			Title:   "Configure Model Now",
			Snippet: "Set up DeepSeek, OpenAI, Anthropic, Ollama, etc.",
			Badge:   "[Quickstart]",
		},
		{
			Index:   2,
			ID:      "skip",
			Title:   "Skip for Now",
			Snippet: "Proceed to main screen; configure anytime via /model",
		},
	}
}

// advanceFromTerms moves to the approval mode step after accepting terms.
func (m tuiModel) advanceFromTerms() (tuiModel, tea.Cmd) {
	if m.r != nil && m.r.cfg != nil {
		m.r.cfg.UI.TermsAccepted = true
	}
	m.onboardingStep = onboardingStepApproval
	title := "Choose Execution Safety & Approval Mode:"
	hints := "↑↓ / KJ Move · Enter Confirm · Esc Back"
	if m.loc == i18n.ChineseSimp {
		title = "请选择智能体执行安全策略 (Approval Mode):"
		hints = "↑↓ 选择 · Enter 确认 · Esc 返回"
	}
	sl := NewSelectionList(title, buildApprovalModeItems(m.loc))
	sl.Boxed = true
	sl.FooterHints = hints
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
		body: "保存配置至 config.yaml，初始化已完成",
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
			m.selectionList.MoveDown(max(3, m.height-6))
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
			m.selectionList.MoveDown(max(3, m.height-6))
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
			title := "Configure Large Language Model (LLM Setup):"
			hints := "↑↓ / KJ Move · Enter Confirm · Esc Back"
			if m.loc == i18n.ChineseSimp {
				title = "配置大语言模型 (LLM Provider Setup):"
				hints = "↑↓ 选择 · Enter 确认 · Esc 返回"
			}
			sl := NewSelectionList(title, buildModelChoiceItems(m.loc))
			sl.Boxed = true
			sl.FooterHints = hints
			m.selectionList = sl
			return m, nil
		}

	case onboardingStepModelChoice:
		switch msg.Type {
		case tea.KeyUp, tea.KeyCtrlP:
			m.selectionList.MoveUp()
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN:
			m.selectionList.MoveDown(max(3, m.height-6))
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
				title := "Select Model Provider:"
				hints := "↑↓ / KJ Move · Enter Confirm · Esc Back"
				if m.loc == i18n.ChineseSimp {
					title = "选择模型提供商开始添加："
					hints = "↑↓ 选择 · Enter 确认 · Esc 返回"
				}
				sl := NewSelectionList(title, buildProviderItems())
				sl.Boxed = true
				sl.FooterHints = hints
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
		title := "Configure Large Language Model (LLM Setup):"
		hints := "↑↓ / KJ Move · Enter Confirm · Esc Back"
		if m.loc == i18n.ChineseSimp {
			title = "配置大语言模型 (LLM Provider Setup):"
			hints = "↑↓ 选择 · Enter 确认 · Esc 返回"
		}
		sl := NewSelectionList(title, buildModelChoiceItems(m.loc))
		sl.Boxed = true
		sl.FooterHints = hints
		m.selectionList = sl
		return m, nil
	}
	return m.handleModelWizardKey(msg)
}
