// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !lite

package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestChatHistoryRenderedAtMemoizes pins the transcript render memo: identical
// parameters return the memoized slice instead of re-rendering (and
// re-parsing markdown), while any render-parameter change rebuilds.
func TestChatHistoryRenderedAtMemoizes(t *testing.T) {
	th := newTheme(i18n.English)
	h := &chatHistory{blocks: []block{
		{kind: blockUser, body: "hi there"},
		{kind: blockAnswer, body: "**bold** answer"},
	}}
	want := strings.Split(h.blocks[1].render(th, 80, false), "\n")
	got := h.renderedAt(1, th, 80, false)
	if !slices.Equal(got, want) {
		t.Fatalf("renderedAt = %q, want %q", got, want)
	}
	// Same parameters: the memoized slice itself comes back — same backing
	// array, not a fresh render that merely compares equal.
	got2 := h.renderedAt(1, th, 80, false)
	if len(got) > 0 && &got[0] != &got2[0] {
		t.Fatal("identical parameters re-rendered instead of memoizing")
	}
	// A new block appends one render; the earlier memo stays.
	h.blocks = append(h.blocks, block{kind: blockNote, body: "note"})
	_ = h.renderedAt(2, th, 80, false)
	if len(h.renders) != 3 {
		t.Fatalf("renders len = %d, want 3", len(h.renders))
	}
	if &h.renderedAt(1, th, 80, false)[0] != &got[0] {
		t.Fatal("append invalidated earlier block renders")
	}
	// Width change rebuilds the whole memo.
	narrow := h.renderedAt(1, th, 40, false)
	wantNarrow := strings.Split(h.blocks[1].render(th, 40, false), "\n")
	if !slices.Equal(narrow, wantNarrow) {
		t.Fatalf("width-40 render = %q, want %q", narrow, wantNarrow)
	}
	// Truncated blocks cannot outrun their memo.
	h.blocks = h.blocks[:1]
	if got := h.renderedAt(0, th, 40, false); !slices.Equal(got, strings.Split(h.blocks[0].render(th, 40, false), "\n")) {
		t.Fatalf("post-truncation render = %q", got)
	}
}

// TestChatHistoryRenderedAtFoldKeys the thought-fold toggle into the memo key:
// flipping expandThought must show the expansion, not the cached fold.
func TestChatHistoryRenderedAtFold(t *testing.T) {
	th := newTheme(i18n.English)
	h := &chatHistory{blocks: []block{
		{kind: blockThought, thoughtLines: []string{"line one", "line two"}},
	}}
	folded := h.renderedAt(0, th, 80, false)
	expanded := h.renderedAt(0, th, 80, true)
	if slices.Equal(folded, expanded) {
		t.Fatal("fold toggle served the folded memo for the expanded render")
	}
	if !strings.Contains(strings.Join(expanded, "\n"), "line two") {
		t.Fatalf("expanded render missing thought body: %q", expanded)
	}
}

// TestStreamAnswerTextThrottle pins the live-region render memo: identical
// sources reuse the memo, deltas inside the interval serve the previous
// frame, and a resize always re-renders regardless of the interval.
func TestStreamAnswerTextThrottle(t *testing.T) {
	m := tuiModel{th: newTheme(i18n.English), liveRC: &liveRenderCache{}, width: 80}
	a := m.streamAnswerText("hello **world**")
	if a == "" {
		t.Fatal("empty render")
	}
	if b := m.streamAnswerText("hello **world**"); b != a {
		t.Fatal("identical source did not reuse the memo")
	}
	// A delta inside liveRenderMinInterval is throttled: the previous frame
	// comes back rather than paying a full markdown pass per token.
	if c := m.streamAnswerText("hello **world** more"); c != a {
		t.Fatal("in-interval delta re-rendered instead of serving the memo")
	}
	// A resize is a parameter change, not a throttle: fresh render now.
	m.width = 40
	if d := m.streamAnswerText("hello **world** more"); d == a {
		t.Fatal("resize served the stale render")
	}
	// A nil cache (a zero-value model) renders straight through.
	bare := tuiModel{th: newTheme(i18n.English), width: 80}
	if out := bare.streamAnswerText("hi"); out == "" {
		t.Fatal("nil cache must still render")
	}
}

// TestStreamAnswerTextExpiresInterval makes the throttle honest: once the
// interval lapses a changed source renders fresh content.
func TestStreamAnswerTextExpiresInterval(t *testing.T) {
	c := &liveRenderCache{}
	m := tuiModel{th: newTheme(i18n.English), liveRC: c, width: 80}
	a := m.streamAnswerText("first")
	c.at = time.Now().Add(-2 * liveRenderMinInterval)
	b := m.streamAnswerText("first second")
	if b == a {
		t.Fatal("elapsed interval still served the stale frame")
	}
}
