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
