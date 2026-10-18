// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestProgressNoteLocalRoute: a task that runs on this machine routes with the
// scheduler's "local" action, which is engine jargon — the progress line must
// phrase it in the user's language ("routing to this node") instead of leaking
// the raw action string, and a real peer's name must still ride through.
func TestProgressNoteLocalRoute(t *testing.T) {
	for _, loc := range []i18n.Locale{i18n.English, i18n.ChineseSimp} {
		got := progressNote(loc, askengine.Progress{Kind: askengine.ProgressRoute, Name: "local"})
		if strings.Contains(got, "local") {
			t.Fatalf("%s: raw scheduler action leaked into the route line: %q", loc, got)
		}
		if strings.Contains(got, "{node}") || strings.TrimSpace(got) == "" {
			t.Fatalf("%s: route line lost its target: %q", loc, got)
		}
	}
	if got := progressNote(i18n.English, askengine.Progress{Kind: askengine.ProgressRoute, Name: "pi-3b"}); !strings.Contains(got, "pi-3b") {
		t.Fatalf("peer node name dropped from the route line: %q", got)
	}
}

// TestElapsedSubSecond: a stage that finishes in milliseconds (a routing
// decision) used to print "0s", which reads as a broken timer. Sub-second
// durations say so explicitly instead.
func TestElapsedSubSecond(t *testing.T) {
	if got := elapsed(400 * time.Millisecond); got != "<1s" {
		t.Fatalf("sub-second: got %q, want <1s", got)
	}
	if got := elapsed(0); got != "<1s" {
		t.Fatalf("zero: got %q, want <1s", got)
	}
	if got := elapsed(3 * time.Second); got != "3s" {
		t.Fatalf("seconds: got %q, want 3s", got)
	}
	if got := elapsed(95 * time.Second); got != "1m35s" {
		t.Fatalf("minutes: got %q, want 1m35s", got)
	}
}

// TestProgressNoteRoundSuffix: a multi-round supervision run re-executes the
// agent per round, and the trail must tell the rounds apart — but a
// single-round task gains nothing from a "round 1/1" decoration.
func TestProgressNoteRoundSuffix(t *testing.T) {
	multi := progressNote(i18n.ChineseSimp, askengine.Progress{Kind: askengine.ProgressExec, Name: "claude_code", Round: 2, Budget: 5})
	if !strings.Contains(multi, "2/5") {
		t.Fatalf("zh multi-round exec lost its round marker: %q", multi)
	}
	multiEn := progressNote(i18n.English, askengine.Progress{Kind: askengine.ProgressJudge, Name: "done", Round: 3, Budget: 5})
	if !strings.Contains(multiEn, "3/5") {
		t.Fatalf("en multi-round judge lost its round marker: %q", multiEn)
	}
	single := progressNote(i18n.ChineseSimp, askengine.Progress{Kind: askengine.ProgressExec, Name: "claude_code", Round: 1, Budget: 1})
	if strings.Contains(single, fmt.Sprintf("%d/%d", 1, 1)) {
		t.Fatalf("single-round run must stay unadorned: %q", single)
	}
}

// TestProgressNoteExecModel verifies that ProgressExec displays the active model
// when provided so users can track token billing.
func TestProgressNoteExecModel(t *testing.T) {
	gotZh := progressNote(i18n.ChineseSimp, askengine.Progress{
		Kind:  askengine.ProgressExec,
		Name:  "claude_code",
		Model: "deepseek-v4-flash",
	})
	if !strings.Contains(gotZh, "claude_code (deepseek-v4-flash)") {
		t.Fatalf("zh exec line should format agent with model: %q", gotZh)
	}

	gotEn := progressNote(i18n.English, askengine.Progress{
		Kind:  askengine.ProgressExec,
		Name:  "claude_code",
		Model: "deepseek-v4-flash",
	})
	if !strings.Contains(gotEn, "claude_code (deepseek-v4-flash)") {
		t.Fatalf("en exec line should format agent with model: %q", gotEn)
	}
}

// TestProgressNoteRetry: a scheduled model retry must read as a localized
// status ("重试中 in Ns · attempt/max") — the whole point of the retry
// milestone is that a 429/5xx stops looking like a frozen spinner.
func TestProgressNoteRetry(t *testing.T) {
	for _, loc := range []i18n.Locale{i18n.English, i18n.ChineseSimp, i18n.Japanese} {
		got := progressNote(loc, askengine.Progress{
			Kind: askengine.ProgressRetry, Status: 429,
			Attempt: 2, MaxAttempt: 6, Delay: 4 * time.Second,
		})
		for _, want := range []string{"429", "2/6", "4"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: retry note %q missing %q", loc, got, want)
			}
		}
	}
	// No HTTP answer (transport drop) reads as a connection failure, not "HTTP 0".
	got := progressNote(i18n.English, askengine.Progress{Kind: askengine.ProgressRetry, Attempt: 1, MaxAttempt: 3, Delay: time.Second})
	if strings.Contains(got, "HTTP 0") || !strings.Contains(got, "connection") {
		t.Fatalf("transport retry note: %q", got)
	}
}
