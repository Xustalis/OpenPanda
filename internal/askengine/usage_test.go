// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// TestUsageTrackerFoldsProviderSnapshots: snapshots are cumulative within a
// call, so the tracker must add their differences, not their absolute values.
func TestUsageTrackerFoldsProviderSnapshots(t *testing.T) {
	var got []Progress
	tr := newUsageTracker(func(p Progress) { got = append(got, p) }, entry.Usage{})
	tr.observe(entry.Usage{InputTokens: 100, OutputTokens: 2})
	tr.observe(entry.Usage{InputTokens: 100, OutputTokens: 9})
	tr.final(100, 9)
	if len(got) == 0 {
		t.Fatal("tracker never reported")
	}
	last := got[len(got)-1]
	if last.Kind != ProgressUsage {
		t.Fatalf("kind = %q, want usage", last.Kind)
	}
	if last.InputTokens != 100 || last.OutputTokens != 9 {
		t.Fatalf("last = %d/%d, want 100/9", last.InputTokens, last.OutputTokens)
	}
}

// TestUsageTrackerSurvivesClientFallback: a fallback client restarts its own
// counters, so a snapshot below the previous one is a new baseline, not
// negative usage. Counts earned before the switch must stay.
func TestUsageTrackerSurvivesClientFallback(t *testing.T) {
	var got []Progress
	tr := newUsageTracker(func(p Progress) { got = append(got, p) }, entry.Usage{})
	tr.observe(entry.Usage{InputTokens: 100, OutputTokens: 10})
	tr.observe(entry.Usage{InputTokens: 5, OutputTokens: 5})  // fallback client
	tr.observe(entry.Usage{InputTokens: 15, OutputTokens: 8}) // +10/+3 on it
	tr.final(0, 0)
	last := got[len(got)-1]
	if last.InputTokens != 110 || last.OutputTokens != 13 {
		t.Fatalf("last = %d/%d, want 110/13", last.InputTokens, last.OutputTokens)
	}
}

// TestUsageTrackerEstimatesBeforeRealUsage: an endpoint that reports usage only
// at the end still gets a number that moves, from streamed bytes.
func TestUsageTrackerEstimatesBeforeRealUsage(t *testing.T) {
	var got []Progress
	tr := newUsageTracker(func(p Progress) { got = append(got, p) }, entry.Usage{})
	tr.addOutput(300) // roughly 100 tokens at the tracker's 3-bytes-per-token
	tr.final(0, 0)
	last := got[len(got)-1]
	if last.OutputTokens != 100 {
		t.Fatalf("estimated output = %d, want 100", last.OutputTokens)
	}
	if !last.Estimated {
		t.Fatal("an estimate must be marked estimated")
	}
}

// TestUsageTrackerFinalReplacesEstimate: the billed total wins even when the
// estimate is higher, so the closing figure is the provider's own.
func TestUsageTrackerFinalReplacesEstimate(t *testing.T) {
	var got []Progress
	tr := newUsageTracker(func(p Progress) { got = append(got, p) }, entry.Usage{})
	tr.addOutput(3000)
	tr.final(120, 40)
	last := got[len(got)-1]
	if last.InputTokens != 120 || last.OutputTokens != 40 {
		t.Fatalf("last = %d/%d, want the billed 120/40", last.InputTokens, last.OutputTokens)
	}
	if last.Estimated {
		t.Fatal("the billed total must not be marked estimated")
	}
}

// TestUsageTrackerStartsFromTheAskBaseline: a long-lived client has already
// billed earlier asks, and the first snapshot of this ask carries that history.
// Only the step past the ask's starting point belongs to this turn.
func TestUsageTrackerStartsFromTheAskBaseline(t *testing.T) {
	var got []Progress
	before := entry.Usage{InputTokens: 40_000, OutputTokens: 12_000}
	tr := newUsageTracker(func(p Progress) { got = append(got, p) }, before)
	tr.observe(entry.Usage{InputTokens: 40_100, OutputTokens: 12_030})
	tr.observe(entry.Usage{InputTokens: 40_100, OutputTokens: 12_045})
	tr.final(100, 45)
	for _, p := range got {
		if p.InputTokens > 100 || p.OutputTokens > 45 {
			t.Fatalf("reported %d/%d, which includes usage from before the ask",
				p.InputTokens, p.OutputTokens)
		}
	}
	last := got[len(got)-1]
	if last.InputTokens != 100 || last.OutputTokens != 45 {
		t.Fatalf("last = %d/%d, want 100/45", last.InputTokens, last.OutputTokens)
	}
}
