//go:build !lite

package main

// The redesigned model configuration surface.
//
// Two screens replace the old alias-only box and the six-step linear wizard:
//
//   - modelPanelView: a full-width register of every configured model —
//     alias, model id, provider, context window and endpoint on one row —
//     with a detail card for the highlighted entry and an inline
//     connectivity probe (t). Nothing is hidden behind an alias anymore.
//
//   - modelFormView: a single-screen editor reached after the provider pick
//     (add) or via e (edit). Every field is on screen at once and can be
//     fixed in any order — a typo'd key no longer means Esc-walking back
//     through four prompts. Text fields have real cursor editing (arrows,
//     Home/End, Delete, ^W/^U), choice fields cycle with ←→, ^L pulls the
//     endpoint's model catalogue into a picker, and the action row offers
//     "test & save" next to a plain "save".

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/providers"
)

// mfieldKind selects the control a form row renders.
type mfieldKind int

const (
	mfText   mfieldKind = iota // free text with a movable cursor
	mfSecret                   // text rendered as bullets; same editing
	mfChoice                   // ←→ / Space cycles a fixed option set
	mfAction                   // button row: test & save / save
)

// mfieldOption is one choice in a mfChoice field, or one button in mfAction.
type mfieldOption struct {
	id    string
	label string
}

// mfield is one row of the model form. Text kinds keep their cursor offset
// here; the value itself lives in the wizard* fields so wizardConfig stays
// the single assembly point.
type mfield struct {
	id       string
	kind     mfieldKind
	label    string
	hint     string
	required bool
	options  []mfieldOption
	optIdx   int // mfAction only: which button is armed
	cursor   int // rune offset into the field value (text kinds)
}

// mfieldGet/mfieldSet map a field id onto the wizard's canonical value slots.
func (m *tuiModel) mfieldGet(id string) string {
	switch id {
	case "base_url":
		return m.wizardBaseURL
	case "api_type":
		return m.wizardAPIType
	case "api_key":
		return m.wizardKey
	case "model":
		return m.wizardModel
	case "thinking":
		return m.wizardThinking
	case "context":
		return m.wizardContext
	case "alias":
		return m.wizardAlias
	}
	return ""
}

func (m *tuiModel) mfieldSet(id, v string) {
	switch id {
	case "base_url":
		m.wizardBaseURL = v
	case "api_type":
		m.wizardAPIType = v
	case "api_key":
		m.wizardKey = v
	case "model":
		m.wizardModel = v
	case "thinking":
		m.wizardThinking = v
	case "context":
		m.wizardContext = v
	case "alias":
		m.wizardAlias = v
	}
}

// buildModelForm lays out the editor for the picked provider. The base URL is
// editable for every provider — prefilled from the catalogue for builtins, so
// rebasing onto a relay is one field edit rather than a different provider.
// The key row is skipped for no-auth providers (Ollama); for custom it stays
// but optional, since keyless gateways are legitimate.
func (m *tuiModel) buildModelForm() {
	prov := m.wizardProvider
	p, ok := providers.Lookup(prov)
	noAuth := ok && p.NoAuth

	if m.wizardThinking == "" {
		m.wizardThinking = "auto"
	}
	if m.wizardAPIType == "" {
		m.wizardAPIType = config.APITypeOpenAI
	}
	if m.wizardBaseURL == "" && ok {
		m.wizardBaseURL = p.BaseURL
	}
	if m.wizardModel == "" && ok {
		m.wizardModel = p.DefaultModel
	}
	if m.wizardContext == "" && ok && p.ContextWindow > 0 {
		m.wizardContext = strconv.Itoa(p.ContextWindow)
	}
	if m.wizardAlias == "" {
		m.wizardAlias = prov
	}
	if m.wizardKey == "" && m.r != nil {
		// A second entry on the same provider almost always wants the same
		// key — prefill it instead of making the user retype a secret.
		m.wizardKey = m.r.findProviderKey(prov)
	}

	fields := []mfield{{
		id:       "base_url",
		label:    i18n.T(m.loc, "tui.mform.f.baseURL"),
		required: prov == "custom",
		hint:     i18n.T(m.loc, "tui.mform.hint.baseURL"),
	}}
	if prov == "custom" {
		fields = append(fields, mfield{
			id:    "api_type",
			kind:  mfChoice,
			label: i18n.T(m.loc, "tui.mform.f.apiType"),
			hint:  i18n.T(m.loc, "tui.mform.hint.apiType"),
			options: []mfieldOption{
				{id: config.APITypeOpenAI, label: "OpenAI"},
				{id: config.APITypeAnthropic, label: "Anthropic"},
			},
		})
	}
	if !noAuth {
		fields = append(fields, mfield{
			id:       "api_key",
			kind:     mfSecret,
			label:    i18n.T(m.loc, "tui.mform.f.apiKey"),
			required: prov != "custom",
			hint:     i18n.T(m.loc, "tui.mform.hint.apiKey"),
		})
	}
	ctxHint := i18n.T(m.loc, "tui.mform.hint.ctx")
	if ok && p.ContextWindow > 0 {
		ctxHint = i18n.Tf(m.loc, "tui.mform.hint.ctxDef", "def", strconv.Itoa(p.ContextWindow))
	}
	fields = append(fields,
		mfield{
			id:       "model",
			label:    i18n.T(m.loc, "tui.mform.f.model"),
			required: true,
			hint:     i18n.T(m.loc, "tui.mform.hint.fetch"),
		},
		mfield{
			id:    "thinking",
			kind:  mfChoice,
			label: i18n.T(m.loc, "tui.mform.f.thinking"),
			options: []mfieldOption{
				{id: "auto", label: i18n.T(m.loc, "tui.wizard.thinkingAuto")},
				{id: "on", label: i18n.T(m.loc, "tui.wizard.thinkingOn")},
				{id: "off", label: i18n.T(m.loc, "tui.wizard.thinkingOff")},
			},
		},
		mfield{
			id:    "context",
			label: i18n.T(m.loc, "tui.mform.f.context"),
			hint:  ctxHint,
		},
		mfield{
			id:    "alias",
			label: i18n.T(m.loc, "tui.mform.f.alias"),
			hint:  i18n.T(m.loc, "tui.mform.hint.alias"),
		},
		mfield{
			id:   "save",
			kind: mfAction,
			options: []mfieldOption{
				{id: "test", label: i18n.T(m.loc, "tui.mform.act.test")},
				{id: "save", label: i18n.T(m.loc, "tui.mform.act.save")},
			},
		},
	)
	for i := range fields {
		fields[i].cursor = len([]rune(m.mfieldGet(fields[i].id)))
	}
	m.form = fields
	m.formFocus = 0
	m.formErr = ""
	m.formTesting = false
	m.formTestErr = ""
	m.formFetching = false
	m.formFetchErr = ""
	m.formPicking = false
}

// focusedField returns the row the editor currently owns, or nil.
func (m *tuiModel) focusedField() *mfield {
	if m.formFocus < 0 || m.formFocus >= len(m.form) {
		return nil
	}
	return &m.form[m.formFocus]
}

// formMove shifts focus by delta rows, wrapping at both ends so Tab from the
// action row returns to the first field.
func (m *tuiModel) formMove(delta int) {
	if len(m.form) == 0 {
		return
	}
	m.formFocus = (m.formFocus + delta + len(m.form)) % len(m.form)
}

// formCycle turns a mfChoice field one step in direction d (+1/−1) and stores
// the picked option id back into the wizard slot.
func (m *tuiModel) formCycle(d int) {
	f := m.focusedField()
	if f == nil || len(f.options) == 0 {
		return
	}
	cur := 0
	for i, o := range f.options {
		if o.id == m.mfieldGet(f.id) {
			cur = i
			break
		}
	}
	if f.kind == mfAction {
		cur = f.optIdx
	}
	cur = (cur + d + len(f.options)) % len(f.options)
	if f.kind == mfAction {
		f.optIdx = cur
		return
	}
	m.mfieldSet(f.id, f.options[cur].id)
}

// formEdit applies one editing keystroke to the focused text/secret field.
// Unlike the old wizardEdit — append + backspace only — this is a real line
// editor: cursor movement, forward delete, word erase and field clear.
func (m *tuiModel) formEdit(msg tea.KeyMsg) {
	f := m.focusedField()
	if f == nil || (f.kind != mfText && f.kind != mfSecret) {
		return
	}
	runes := []rune(m.mfieldGet(f.id))
	cur := min(f.cursor, len(runes))
	switch msg.Type {
	case tea.KeyLeft, tea.KeyCtrlB:
		if cur > 0 {
			cur--
		}
	case tea.KeyRight, tea.KeyCtrlF:
		if cur < len(runes) {
			cur++
		}
	case tea.KeyHome, tea.KeyCtrlA:
		cur = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		cur = len(runes)
	case tea.KeyBackspace, tea.KeyCtrlH:
		if cur > 0 {
			runes = append(runes[:cur-1], runes[cur:]...)
			cur--
		}
	case tea.KeyDelete, tea.KeyCtrlD:
		if cur < len(runes) {
			runes = append(runes[:cur], runes[cur+1:]...)
		}
	case tea.KeyCtrlW:
		start := cur
		for start > 0 && runes[start-1] == ' ' {
			start--
		}
		for start > 0 && runes[start-1] != ' ' {
			start--
		}
		runes = append(runes[:start], runes[cur:]...)
		cur = start
	case tea.KeyCtrlU:
		runes = nil
		cur = 0
	case tea.KeySpace:
		runes = append(runes[:cur], append([]rune{' '}, runes[cur:]...)...)
		cur++
	case tea.KeyRunes:
		ins := msg.Runes
		tail := append([]rune{}, runes[cur:]...)
		runes = append(runes[:cur], append(ins, tail...)...)
		cur += len(ins)
	}
	f.cursor = cur
	m.mfieldSet(f.id, string(runes))
}

// formValidate checks the assembled values before a save or probe and reports
// the first offending field id plus a localized message. Fixups that are safe
// (scheme-less base URLs, a blank model on a provider with a default) are
// applied in place rather than complained about.
func (m *tuiModel) formValidate() (string, string) {
	for _, f := range m.form {
		v := strings.TrimSpace(m.mfieldGet(f.id))
		m.mfieldSet(f.id, v)
		switch f.id {
		case "base_url":
			if v == "" {
				if f.required {
					return f.id, i18n.Tf(m.loc, "tui.mform.err.required", "field", f.label)
				}
				continue
			}
			if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
				m.mfieldSet(f.id, "https://"+v)
			}
		case "api_key", "model":
			if f.required && v == "" {
				return f.id, i18n.Tf(m.loc, "tui.mform.err.required", "field", f.label)
			}
		case "context":
			if v != "" {
				if n, err := strconv.Atoi(v); err != nil || n <= 0 {
					return f.id, i18n.T(m.loc, "tui.mform.err.context")
				}
			}
		}
	}
	// A blank model on a catalogue provider takes the provider default.
	if m.wizardModel == "" {
		if p, ok := providers.Lookup(m.wizardProvider); ok && p.DefaultModel != "" {
			m.wizardModel = p.DefaultModel
		}
	}
	return "", ""
}

// formSave is the action row's commit path. test=true probes the endpoint
// first and persists only on a successful answer; test=false saves straight
// away — the user's explicit choice on the second button.
func (m tuiModel) formSave(test bool) (tuiModel, tea.Cmd) {
	badID, msg := m.formValidate()
	if msg != "" {
		m.formErr = msg
		for i, f := range m.form {
			if f.id == badID {
				m.formFocus = i
				break
			}
		}
		return m, nil
	}
	m.formErr = ""
	m.formTestErr = ""
	if !test {
		return m.finalizeWizard()
	}
	m.formTesting = true
	return m, tea.Batch(m.wizardTestCmd(), m.sp.Tick)
}

// startFetchModels pulls the endpoint's /models catalogue so the user picks a
// real model id instead of guessing one. Runs against whatever the form
// currently says — editing the URL and refetching is the point.
func (m tuiModel) startFetchModels() (tea.Model, tea.Cmd) {
	if m.formFetching {
		return m, nil
	}
	// The fetch needs a well-formed endpoint; run the same cheap fixups the
	// save path does first.
	badID, msg := m.formValidate()
	if msg != "" && badID == "base_url" {
		m.formErr = msg
		for i, f := range m.form {
			if f.id == badID {
				m.formFocus = i
			}
		}
		return m, nil
	}
	m.formErr = ""
	m.formFetchErr = ""
	m.formFetching = true
	mc := m.wizardConfig()
	return m, tea.Batch(func() tea.Msg {
		client, err := entry.NewClient(mc)
		if err != nil {
			return modelListMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		models, err := client.ListModels(ctx)
		if err != nil {
			return modelListMsg{err: err}
		}
		ids := make([]string, 0, len(models))
		for _, mi := range models {
			ids = append(ids, mi.ID)
		}
		return modelListMsg{models: ids}
	}, m.sp.Tick)
}

// handleFormKey routes keystrokes while the model form has the screen.
func (m tuiModel) handleFormKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The fetched-models picker is an overlay on the form: arrows pick,
	// Enter fills the model field, Esc drops back to the editor.
	if m.formPicking {
		rows := m.listPageRows()
		switch msg.Type {
		case tea.KeyEsc:
			m.formPicking = false
			return m, nil
		case tea.KeyUp, tea.KeyCtrlP:
			m.selectionList.MoveUp()
		case tea.KeyDown, tea.KeyCtrlN:
			m.selectionList.MoveDown(rows)
		case tea.KeyPgUp:
			m.selectionList.MovePage(-rows, rows)
		case tea.KeyPgDown:
			m.selectionList.MovePage(rows, rows)
		case tea.KeyEnter:
			if it, ok := m.selectionList.Selected(); ok {
				m.wizardModel = it.ID
				for i := range m.form {
					if m.form[i].id == "model" {
						m.form[i].cursor = len([]rune(it.ID))
					}
				}
			}
			m.formPicking = false
			return m, nil
		case tea.KeyRunes:
			switch string(msg.Runes) {
			case "k", "K":
				m.selectionList.MoveUp()
			case "j", "J":
				m.selectionList.MoveDown(rows)
			case "q", "Q":
				m.formPicking = false
			}
		}
		return m, nil
	}

	// A probe in flight ignores editing keys; Esc stops waiting on it (the
	// HTTP request itself has its own timeout and its late result is dropped
	// by the Update guard).
	if m.formTesting {
		if msg.Type == tea.KeyEsc {
			m.formTesting = false
		}
		return m, nil
	}

	f := m.focusedField()
	switch msg.Type {
	case tea.KeyEsc:
		return m.wizardBack()
	case tea.KeyTab:
		m.formMove(1)
		return m, nil
	case tea.KeyShiftTab:
		m.formMove(-1)
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		m.formMove(-1)
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		m.formMove(1)
		return m, nil
	case tea.KeyCtrlL:
		return m.startFetchModels()
	case tea.KeyEnter:
		if f == nil {
			return m, nil
		}
		if f.kind == mfAction {
			test := f.optIdx == 0
			return m.formSave(test)
		}
		m.formMove(1)
		return m, nil
	case tea.KeyLeft:
		if f != nil && (f.kind == mfChoice || f.kind == mfAction) {
			m.formCycle(-1)
			return m, nil
		}
	case tea.KeyRight, tea.KeySpace:
		if f != nil && (f.kind == mfChoice || f.kind == mfAction) {
			m.formCycle(1)
			return m, nil
		}
	case tea.KeyRunes:
		// Letters on a choice or action row are not text — j/k keep their
		// navigator meaning there so a form never traps vim fingers.
		if f != nil && f.kind != mfText && f.kind != mfSecret {
			switch string(msg.Runes) {
			case "h", "H":
				m.formCycle(-1)
			case "l", "L":
				m.formCycle(1)
			case "j", "J":
				m.formMove(1)
			case "k", "K":
				m.formMove(-1)
			case "q", "Q":
				return m.wizardBack()
			}
			return m, nil
		}
	}
	m.formEdit(msg)
	return m, nil
}

// wizardBack is the form's Esc: the provider picker when adding, the model
// panel when editing an existing entry.
func (m tuiModel) wizardBack() (tuiModel, tea.Cmd) {
	if m.wizardStep == wizardStepForm {
		if m.wizardEditAlias != "" {
			return m.openModelPanel()
		}
		m.wizardStep = wizardStepProvider
		m.form = nil
		m.formPicking = false
		sl := NewSelectionList(i18n.T(m.loc, "tui.wizard.noModelPrompt"), buildProviderItems(m.loc))
		sl.Boxed = true
		sl.FooterHints = i18n.T(m.loc, "tui.wizard.confirmBack")
		m.selectionList = sl
		return m, nil
	}
	// Provider step Esc: back to the panel when a register exists, otherwise
	// the wizard was the only way forward and Esc means "not now".
	hasModels := m.r != nil && m.r.cfg != nil &&
		(m.r.cfg.Model.BaseURL != "" || m.r.cfg.Model.Provider != "" || len(m.r.cfg.Models) > 0)
	if hasModels {
		return m.openModelPanel()
	}
	m.mode = modeIdle
	return m, nil
}

// probeModel runs the connectivity check every save path shares: build a
// client, then a one-word completion. A bare custom endpoint with no model id
// picks the first advertised model so the probe still exercises a real call.
func probeModel(mc config.ModelConfig) error {
	client, err := entry.NewClient(mc)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if mc.Model == "" {
		models, lerr := client.ListModels(ctx)
		if lerr != nil || len(models) == 0 {
			return fmt.Errorf("no model configured")
		}
		mc.Model = models[0].ID
		if client, err = entry.NewClient(mc); err != nil {
			return err
		}
	}
	_, err = client.Complete(ctx, "You are a connectivity test.", "Reply with exactly: OK")
	return err
}

// panelTestCmd probes the highlighted registry entry off the UI goroutine.
func (m tuiModel) panelTestCmd(mc config.ModelConfig, alias string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		err := probeModel(mc)
		return panelTestMsg{alias: alias, err: err, dur: time.Since(start)}
	}
}

// testSelectedModel starts a connectivity probe on the highlighted entry.
func (m tuiModel) testSelectedModel() (tuiModel, tea.Cmd) {
	item, ok := m.selectionList.Selected()
	if !ok || m.panelTesting {
		return m, nil
	}
	mc, ok := item.Value.(config.ModelConfig)
	if !ok {
		return m, nil
	}
	m.panelTesting = true
	m.panelTestName = item.ID
	m.panelTestErr = ""
	m.panelTestOK = false
	return m, tea.Batch(m.panelTestCmd(mc, item.ID), m.sp.Tick)
}

// panelListRows is how many register rows the panel can show: the screen minus
// title, column header, detail card and hints.
func (m tuiModel) panelListRows() int {
	return max(3, m.height-13)
}

// modelPanelView renders the register: a columned row per model plus a detail
// card for the highlighted entry. It is deliberately unboxed — endpoints and
// model ids need the full width the old 64-column box could not give.
func (m tuiModel) modelPanelView() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	var lines []string
	lines = append(lines, "  "+m.th.heading.Render(i18n.T(m.loc, "tui.model.panelTitle")))
	lines = append(lines, "")

	items := m.selectionList.Items
	if len(items) == 0 {
		lines = append(lines, "  "+m.th.muted.Render(i18n.T(m.loc, "tui.mpanel.empty")))
		lines = append(lines, "")
		lines = append(lines, "  "+m.th.muted.Render(i18n.T(m.loc, "tui.mpanel.hints")))
		return strings.Join(lines, "\n")
	}

	// Column widths from real content, capped so the endpoint keeps room.
	aliasW, modelW, provW := 5, 5, 8
	for _, it := range items {
		mc, _ := it.Value.(config.ModelConfig)
		aliasW = max(aliasW, cliui.DisplayWidth(it.ID))
		modelW = max(modelW, cliui.DisplayWidth(effectiveModel(mc)))
		pl := effectiveProvider(mc)
		if p, ok := providers.Lookup(pl); ok {
			pl = p.Label
		}
		provW = max(provW, cliui.DisplayWidth(pl))
	}
	aliasW = min(aliasW, 18)
	modelW = min(modelW, 30)
	provW = min(provW, 22)
	ctxW := 6

	header := "      " + strings.Join([]string{
		cell(i18n.T(m.loc, "tui.mpanel.col.alias"), aliasW),
		cell(i18n.T(m.loc, "tui.mpanel.col.model"), modelW),
		cell(i18n.T(m.loc, "tui.mpanel.col.provider"), provW),
		cell(i18n.T(m.loc, "tui.mpanel.col.ctx"), ctxW),
		i18n.T(m.loc, "tui.mpanel.col.endpoint"),
	}, "  ")
	lines = append(lines, m.th.muted.Render(header))

	rows := m.panelListRows()
	top := m.selectionList.Top
	if top < 0 {
		top = 0
	}
	end := min(len(items), top+rows)
	epW := max(10, w-aliasW-modelW-provW-ctxW-16)
	for i := top; i < end; i++ {
		it := items[i]
		mc, _ := it.Value.(config.ModelConfig)
		marker := "  "
		if it.Badge != "" {
			marker = m.th.success.Render(m.th.glyph("●", "*")) + " "
		}
		pl := effectiveProvider(mc)
		if p, ok := providers.Lookup(pl); ok {
			pl = p.Label
		}
		ctxStr := "-"
		if cw := effectiveContextWindow(mc); cw > 0 {
			ctxStr = fmt.Sprintf("%dk", cw/1000)
		}
		line := marker +
			m.th.accent.Render(cell(it.ID, aliasW)) + "  " +
			cell(effectiveModel(mc), modelW) + "  " +
			m.th.muted.Render(cell(pl, provW)) + "  " +
			m.th.muted.Render(cell(ctxStr, ctxW)) + "  " +
			m.th.muted.Render(cliui.Truncate(effectiveBaseURL(mc), epW, m.th.unicode))
		if it.Badge != "" {
			line += "  " + m.th.accent.Render(it.Badge)
		}
		if i == m.selectionList.Cursor {
			lines = append(lines, m.th.accent.Render("  > ")+line)
		} else {
			lines = append(lines, "    "+line)
		}
	}

	// Detail card for the highlighted entry — or the delete confirmation that
	// replaces it while a removal is being confirmed.
	lines = append(lines, "")
	lines = append(lines, "  "+m.th.muted.Render(strings.Repeat(m.th.glyph("─", "-"), min(w-4, 72))))
	if m.confirmDeleteModel && m.pendingDeleteModel != "" {
		lines = append(lines, "  "+m.th.warn.Bold(true).Render(
			m.th.glyph("⚠", "!")+" "+i18n.Tf(m.loc, "tui.model.deleteConfirm", "alias", m.pendingDeleteModel)))
		lines = append(lines, "  "+m.th.muted.Render(i18n.T(m.loc, "tui.mpanel.confirmHints")))
	} else if it, ok := m.selectionList.Selected(); ok {
		if mc, ok2 := it.Value.(config.ModelConfig); ok2 {
			lines = append(lines, m.panelDetailLines(mc, it.ID, w)...)
		}
	}
	lines = append(lines, "")

	// Pad so the hint row sits at the bottom edge like the other list screens.
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	lines = append(lines, "  "+m.th.muted.Render(i18n.T(m.loc, "tui.mpanel.hints")))
	return strings.Join(lines, "\n")
}

// panelDetailLines is the highlighted model's full card: endpoint, masked key,
// dialect, thinking mode, context window — and the probe result once one ran.
func (m tuiModel) panelDetailLines(mc config.ModelConfig, id string, w int) []string {
	labelW := 10
	kv := func(k, v string) string {
		return "  " + m.th.muted.Render(cell(k, labelW)) + v
	}
	key := i18n.T(m.loc, "tui.mpanel.key.unset")
	noAuth := mc.NoAuth
	if p, ok := providers.Lookup(mc.Provider); ok && p.NoAuth {
		noAuth = true
	}
	switch {
	case noAuth:
		key = i18n.T(m.loc, "tui.mpanel.key.none")
	case mc.APIKey != "":
		r := []rune(mc.APIKey)
		tail := string(r)
		if len(r) > 4 {
			tail = string(r[len(r)-4:])
		}
		key = "••••" + tail
	}
	thinking := mc.Thinking
	if thinking == "" {
		thinking = "auto"
	}
	ctxStr := "-"
	if cw := effectiveContextWindow(mc); cw > 0 {
		ctxStr = fmt.Sprintf("%d", cw)
	}
	out := []string{
		kv(i18n.T(m.loc, "tui.mpanel.d.endpoint"), cliui.Truncate(effectiveBaseURL(mc), w-labelW-6, m.th.unicode)),
		kv(i18n.T(m.loc, "tui.mpanel.d.key"), key) +
			"   " + m.th.muted.Render(i18n.T(m.loc, "tui.mpanel.d.dialect")) + "  " + mc.NormalizedAPIType() +
			"   " + m.th.muted.Render(i18n.T(m.loc, "tui.mpanel.d.thinking")) + "  " + thinking +
			"   " + m.th.muted.Render(i18n.T(m.loc, "tui.mpanel.d.context")) + "  " + ctxStr,
	}
	switch {
	case m.panelTesting && m.panelTestName == id:
		out = append(out, kv(i18n.T(m.loc, "tui.mpanel.d.status"),
			m.sp.View()+" "+m.th.muted.Render(i18n.T(m.loc, "tui.mpanel.testing"))))
	case m.panelTestName == id && m.panelTestErr != "":
		out = append(out, kv(i18n.T(m.loc, "tui.mpanel.d.status"),
			m.th.warn.Render(m.th.glyph("✗", "x")+" "+cliui.Truncate(m.panelTestErr, w-labelW-10, m.th.unicode))))
	case m.panelTestName == id && m.panelTestOK:
		out = append(out, kv(i18n.T(m.loc, "tui.mpanel.d.status"),
			m.th.success.Render(m.th.glyph("✓", "*")+" "+i18n.Tf(m.loc, "tui.mpanel.testOK", "dur", elapsed(m.panelTestDur)))))
	}
	return out
}

// modelFormView renders the single-screen editor: provider summary on top,
// every field as a labelled row, a status line for probes/fetches/validation,
// then the action row and the key legend.
func (m tuiModel) modelFormView() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	if m.formPicking {
		return m.selectionList.Render(m.th, w, h)
	}

	var lines []string
	title := i18n.Tf(m.loc, "tui.mform.titleAdd", "provider", m.providerLabel())
	if m.wizardEditAlias != "" {
		title = i18n.Tf(m.loc, "tui.mform.titleEdit", "alias", m.wizardEditAlias)
	}
	lines = append(lines, "  "+m.th.heading.Render(title))
	lines = append(lines, "")

	labelW := 0
	for _, f := range m.form {
		if f.kind != mfAction {
			labelW = max(labelW, cliui.DisplayWidth(f.label))
		}
	}
	for i, f := range m.form {
		lines = append(lines, m.renderFormField(i, f, labelW, w))
	}
	lines = append(lines, "")

	// Status line: validation error > probe result > fetch state > nothing.
	switch {
	case m.formErr != "":
		lines = append(lines, "  "+m.th.warn.Render(m.th.glyph("✗", "x")+" "+m.formErr))
	case m.formTesting:
		lines = append(lines, "  "+m.sp.View()+" "+m.th.muted.Render(i18n.T(m.loc, "tui.mform.testing")))
	case m.formTestErr != "":
		lines = append(lines, "  "+m.th.warn.Render(m.th.glyph("✗", "x")+" "+i18n.Tf(m.loc, "tui.mform.testFail", "err", cliui.Truncate(m.formTestErr, w-8, m.th.unicode))))
		lines = append(lines, "  "+m.th.muted.Render(i18n.T(m.loc, "tui.mform.testFailHints")))
	case m.formFetching:
		lines = append(lines, "  "+m.sp.View()+" "+m.th.muted.Render(i18n.T(m.loc, "tui.mform.fetching")))
	case m.formFetchErr != "":
		lines = append(lines, "  "+m.th.warn.Render(m.th.glyph("✗", "x")+" "+i18n.Tf(m.loc, "tui.mform.fetchFail", "err", cliui.Truncate(m.formFetchErr, w-8, m.th.unicode))))
	}
	lines = append(lines, "")
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	lines = append(lines, "  "+m.th.muted.Render(i18n.T(m.loc, "tui.mform.hints")))
	return strings.Join(lines, "\n")
}

// providerLabel renders the wizard's provider for titles: the catalogue label
// when known, the raw id otherwise.
func (m tuiModel) providerLabel() string {
	if p, ok := providers.Lookup(m.wizardProvider); ok {
		return p.Label
	}
	return m.wizardProvider
}

// renderFormField draws one form row: focus marker, label, control, hint.
func (m tuiModel) renderFormField(idx int, f mfield, labelW, w int) string {
	focused := idx == m.formFocus
	marker := "  "
	if focused {
		marker = m.th.accent.Render("▸ ")
	}
	if f.kind == mfAction {
		var btns []string
		for i, o := range f.options {
			label := "[ " + o.label + " ]"
			switch {
			case focused && i == f.optIdx:
				btns = append(btns, m.th.accent.Bold(true).Render(label))
			default:
				btns = append(btns, m.th.muted.Render(label))
			}
		}
		return "  " + marker + strings.Join(btns, "  ")
	}

	label := cell(f.label, labelW)
	if focused {
		label = m.th.accent.Render(label)
	} else {
		label = m.th.muted.Render(label)
	}
	// The control column gets a fixed width so every field's hint starts at
	// the same column; the hint's own width leaves room on the right.
	controlW := max(16, w-labelW-8-cliui.DisplayWidth(f.hint)-12)
	var control string
	switch f.kind {
	case mfChoice:
		control = m.renderChoice(f)
		if dw := ansi.StringWidth(control); dw < controlW {
			control += strings.Repeat(" ", controlW-dw)
		}
	default:
		control = m.renderTextValue(f, focused, controlW)
	}
	hint := ""
	if f.hint != "" {
		hint = "   " + m.th.muted.Render(f.hint)
	}
	if f.required {
		hint += " " + m.th.muted.Render(i18n.T(m.loc, "tui.mform.required"))
	}
	return "  " + marker + label + "  " + control + hint
}

// renderTextValue draws the field's text padded to w so hints align. Empty
// values show faint underscores (a "type here" affordance); a focused field
// shows a reverse-video cursor at the rune offset and scrolls horizontally so
// the cursor stays visible in overlong values. Secrets mask every rune.
func (m tuiModel) renderTextValue(f mfield, focused bool, w int) string {
	if w < 4 {
		w = 4
	}
	faint := lipgloss.NewStyle().Faint(true)
	pad := func(s string) string {
		return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
	}
	runes := []rune(m.mfieldGet(f.id))
	if f.kind == mfSecret {
		for i := range runes {
			runes[i] = '•'
		}
	}
	if len(runes) == 0 {
		blank := faint.Render(strings.Repeat("_", min(16, w-1)))
		if focused {
			return pad(lipgloss.NewStyle().Reverse(true).Render(" ") + blank)
		}
		return pad(blank)
	}
	if !focused {
		return pad(faint.Render(cliui.Truncate(string(runes), w, m.th.unicode)))
	}
	cur := min(f.cursor, len(runes))
	head := runes[:cur]
	// Scroll: drop leading runes until the head fits, keeping the cursor cell
	// inside the field. A ‹ marker replaces the first visible cell when text
	// was cut so the user knows the value continues off-screen.
	scrolled := false
	for len(head) > 0 && cliui.DisplayWidth(string(head)) >= w-1 {
		head = head[1:]
		scrolled = true
	}
	avail := w - cliui.DisplayWidth(string(head)) - 1
	rest := runes[cur:]
	if cur < len(runes) {
		rest = runes[cur+1:] // the cursor cell itself renders runes[cur]
	}
	tail := ""
	if avail > 0 {
		tail = cliui.Truncate(string(rest), avail, m.th.unicode)
	}
	cursor := lipgloss.NewStyle().Reverse(true)
	var b strings.Builder
	if scrolled && len(head) > 0 {
		b.WriteString(faint.Render("‹" + string(head[1:])))
	} else {
		b.WriteString(faint.Render(string(head)))
	}
	if cur < len(runes) {
		b.WriteString(cursor.Render(string(runes[cur])))
	} else {
		b.WriteString(cursor.Render(" "))
	}
	b.WriteString(faint.Render(tail))
	return pad(b.String())
}

// renderChoice draws a choice field's options with the current one bracketed.
func (m tuiModel) renderChoice(f mfield) string {
	cur := m.mfieldGet(f.id)
	var parts []string
	l, r := m.th.glyph("⟨", "["), m.th.glyph("⟩", "]")
	for _, o := range f.options {
		if o.id == cur {
			parts = append(parts, m.th.accent.Bold(true).Render(l+o.label+r))
		} else {
			parts = append(parts, m.th.muted.Render(o.label))
		}
	}
	return strings.Join(parts, "  ")
}
