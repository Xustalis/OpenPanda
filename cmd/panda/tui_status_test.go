// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestQuietNoteNamesPreludeThenGap pins the two silences apart: before any
// event the turn is waiting on the model's prefill, after activity it is simply
// quiet. A surface that used one wording for both would blame the provider for
// a normal first-token wait.
func TestQuietNoteNamesPreludeThenGap(t *testing.T) {
	now := time.Now()
	m := tuiModel{loc: i18n.ChineseSimp}
	m.started = now.Add(-10 * time.Second)
	if got := m.quietNote(now); !strings.Contains(got, "等待模型响应") {
		t.Fatalf("prelude note = %q", got)
	}

	m.lastActivity = now.Add(-time.Second)
	if got := m.quietNote(now); got != "" {
		t.Fatalf("fresh activity note = %q, want empty", got)
	}

	m.lastActivity = now.Add(-10 * time.Second)
	m.started = now.Add(-20 * time.Second)
	if got := m.quietNote(now); !strings.Contains(got, "已静默") {
		t.Fatalf("gap note = %q", got)
	}
}

// TestStatusMetaCarriesLiveTokens: the running status row shows consumption as
// soon as the engine reports it, not only in the closing result line.
func TestStatusMetaCarriesLiveTokens(t *testing.T) {
	now := time.Now()
	m := tuiModel{loc: i18n.English}
	m.started = now.Add(-5 * time.Second)
	m.lastActivity = now
	m.liveInputTokens = 900
	m.liveOutputTokens = 1200
	meta := m.statusMeta(now)
	if !strings.Contains(meta, "2.1k tokens") {
		t.Fatalf("meta = %q, want the live total", meta)
	}
}

// TestStatusMetaMarksEstimate: a character-based guess must be distinguishable
// from the provider's count, or the number read as a bill.
func TestStatusMetaMarksEstimate(t *testing.T) {
	now := time.Now()
	m := tuiModel{loc: i18n.English}
	m.started = now.Add(-5 * time.Second)
	m.lastActivity = now
	m.liveOutputTokens = 2100
	m.liveTokensEst = true
	if meta := m.statusMeta(now); !strings.Contains(meta, "~2.1k tokens") {
		t.Fatalf("meta = %q, want a marked estimate", meta)
	}
}

// TestThoughtViewPreviewsRecentLines: the folded chain shows the newest few
// lines, so the turn displays what it is reasoning about rather than only that
// it is reasoning.
func TestThoughtViewPreviewsRecentLines(t *testing.T) {
	m := tuiModel{loc: i18n.English, thought: []string{"one", "two", "three", "four"}}
	got := m.thoughtView()
	for _, want := range []string{"4 lines", "two", "three", "four", "ctrl+o to expand"} {
		if !strings.Contains(got, want) {
			t.Fatalf("thought view = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "one") {
		t.Fatalf("thought view = %q, the oldest line should be folded away", got)
	}
}
