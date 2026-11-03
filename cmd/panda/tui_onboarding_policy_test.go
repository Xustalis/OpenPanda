// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// The onboarding approval card writes the selected item ID straight into
// approval.mode. These IDs must be the canonical values, and the effective
// policy after NormalizedMode must match what the label promises. The old
// "auto" item normalized to "never" (blanket auto-consent) while its label
// said writes still prompt.
func TestApprovalModeItemsUseCanonicalValues(t *testing.T) {
	items := buildApprovalModeItems(i18n.English)
	if len(items) != 3 {
		t.Fatalf("approval options = %d, want 3", len(items))
	}
	want := []struct {
		id   string
		mode string
	}{
		{config.ApprovalModeOnRequest, config.ApprovalModeOnRequest},
		{config.ApprovalModeAlways, config.ApprovalModeAlways},
		{config.ApprovalModeNever, config.ApprovalModeNever},
	}
	for i, w := range want {
		if items[i].ID != w.id {
			t.Fatalf("option %d id = %q, want %q", i, items[i].ID, w.id)
		}
		got := (config.ApprovalConfig{Mode: items[i].ID}).NormalizedMode()
		if got != w.mode {
			t.Fatalf("option %q normalizes to %q, want %q", items[i].ID, got, w.mode)
		}
		if items[i].Title == "" || items[i].Snippet == "" {
			t.Fatalf("option %q has an empty title or snippet", items[i].ID)
		}
	}
}

// The language picker used to hard-code cursor 0 (English) even when the
// process had already detected zh-CN and was rendering a Chinese splash.
func TestStartOnboardingPreselectsDetectedLocale(t *testing.T) {
	for loc, want := range map[i18n.Locale]string{
		i18n.ChineseSimp: "zh-CN",
		i18n.Japanese:    "ja",
		i18n.English:     "en",
	} {
		m := tuiModel{loc: loc, r: &repl{loc: loc, cfg: &config.Config{}}}
		m, _ = m.startOnboarding()
		if m.selectionList.Cursor >= len(m.selectionList.Items) {
			t.Fatalf("%s: cursor out of range", loc)
		}
		if got := m.selectionList.Items[m.selectionList.Cursor].ID; got != want {
			t.Fatalf("%s: preselected %q, want %q", loc, got, want)
		}
	}
}

// "Configure Later (Skip)" is an answer: the next launch must not open the
// provider picker again. A genuinely fresh install still gets the wizard.
func TestSkippedModelIsRemembered(t *testing.T) {
	newModel := func(onboarded bool) tuiModel {
		cfg := &config.Config{}
		cfg.UI.Onboarded = onboarded
		cfg.UI.TermsAccepted = true
		cfg.UI.TermsVersion = config.TermsVersionCurrent
		cfg.UI.Locale = string(i18n.English)
		r := &repl{loc: i18n.English, cfg: cfg}
		m := newTUIModel(r)
		m.mode = modeSplash
		return m
	}

	next, _ := newModel(true).startFromSplash()
	if got := next.(tuiModel); got.mode != modeIdle {
		t.Fatalf("skipped model setup reopened mode %v, want modeIdle", got.mode)
	}
	next, _ = newModel(false).startFromSplash()
	if got := next.(tuiModel); got.mode != modeModelWizard {
		t.Fatalf("fresh install mode = %v, want modeModelWizard", got.mode)
	}
}
