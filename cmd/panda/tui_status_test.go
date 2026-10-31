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
