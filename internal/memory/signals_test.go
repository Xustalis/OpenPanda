package memory

import (
	"testing"
	"time"
)

func TestFrequencyAndDiversitySignals(t *testing.T) {
	if frequencySignal(0) != 0 || frequencySignal(3) != 1 || frequencySignal(9) != 1 {
		t.Errorf("frequencySignal: got %v %v %v, want 0 1 1",
			frequencySignal(0), frequencySignal(3), frequencySignal(9))
	}
	if queryDiversitySignal(0) != 0 || queryDiversitySignal(3) != 1 || queryDiversitySignal(5) != 1 {
		t.Errorf("queryDiversitySignal: got %v %v %v, want 0 1 1",
			queryDiversitySignal(0), queryDiversitySignal(3), queryDiversitySignal(5))
	}
}

func TestRecencySignal(t *testing.T) {
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	if recencySignal(now, now) != 1 {
		t.Errorf("recency today = %v, want 1", recencySignal(now, now))
	}
	future := now.Add(24 * time.Hour)
	if recencySignal(future, now) != 1 {
		t.Errorf("recency future = %v, want 1", recencySignal(future, now))
	}
	old := now.AddDate(0, 0, -30)
	if recencySignal(old, now) != 0 {
		t.Errorf("recency 30d = %v, want 0", recencySignal(old, now))
	}
	mid := now.AddDate(0, 0, -15)
	got := recencySignal(mid, now)
	if got < 0.49 || got > 0.51 {
		t.Errorf("recency 15d = %v, want ~0.5", got)
	}
}

func TestConsolidationSignal(t *testing.T) {
	day := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	if consolidationSignal(day, day) != 0 {
		t.Errorf("consolidation same-day = %v, want 0", consolidationSignal(day, day))
	}
	week := day.AddDate(0, 0, 7)
	if consolidationSignal(day, week) != 1 {
		t.Errorf("consolidation 7d = %v, want 1", consolidationSignal(day, week))
	}
	month := day.AddDate(0, 0, 28)
	if consolidationSignal(day, month) != 1 {
		t.Errorf("consolidation 28d = %v, want 1 (clamped)", consolidationSignal(day, month))
	}
	if consolidationSignal(week, day) != 0 {
		t.Errorf("negative span = %v, want 0", consolidationSignal(week, day))
	}
}

func TestRelevanceSignal(t *testing.T) {
	if got := relevanceSignal("anything at all", nil); got != 1 {
		t.Errorf("cold start relevance = %v, want 1", got)
	}
	memory := []string{"the core is written in Go", "deploy to Orange Pi"}
	if got := relevanceSignal("the core is Go", memory); got != 1 {
		t.Errorf("full-overlap relevance = %v, want 1", got)
	}
	if got := relevanceSignal("unrelated topic here", memory); got != 0 {
		t.Errorf("no-overlap relevance = %v, want 0", got)
	}
}

func TestConceptualSignal(t *testing.T) {
	if got := conceptualSignal("use Go and TypeScript for the UI"); got <= 0 {
		t.Errorf("conceptual density = %v, want > 0", got)
	}
	if got := conceptualSignal("a simple and clean design"); got != 0 {
		t.Errorf("no-concept density = %v, want 0", got)
	}
}

func TestTokenize(t *testing.T) {
	words := tokenize("Go core, 部署 TypeScript")
	for _, want := range []string{"go", "core", "typescript"} {
		if _, ok := words[want]; !ok {
			t.Errorf("tokenize missing %q in %v", want, words)
		}
	}
	for _, want := range []string{"部", "署"} {
		if _, ok := words[want]; !ok {
			t.Errorf("tokenize missing CJK %q in %v", want, words)
		}
	}
}

func TestScoreCandidateWeightsSumToOne(t *testing.T) {
	// A full-signal candidate must score exactly 1.0.
	if got := scoreCandidate(1, 1, 1, 1, 1, 1); got != 1 {
		t.Errorf("full-signal score = %v, want 1", got)
	}
}
