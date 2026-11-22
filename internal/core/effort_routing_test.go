// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"slices"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// The effort bands are the router's only statement about difficulty, so their
// edges are worth pinning: below the light threshold the task must read as
// quick work, at or above the deep threshold as reasoning work, and the middle
// must stay silent so ordinary tasks keep trading cost against capability.
func TestEffortTraitsBands(t *testing.T) {
	for _, tc := range []struct {
		name       string
		complexity float64
		want       []string
	}{
		{"unknown", 0, nil},
		{"clearly light", 0.2, []string{"quick_edits", "fast_scripts", "code_search", "code_explanation"}},
		{"just under the light edge", 0.34, []string{"quick_edits", "fast_scripts", "code_search", "code_explanation"}},
		{"the light edge itself is standard", complexityLight, nil},
		{"ordinary", 0.5, nil},
		{"just under the deep edge", 0.74, nil},
		{"the deep edge itself is deep", complexityDeep, []string{"complex_reasoning", "multi_file_edits", "refactoring"}},
		{"clearly deep", 0.95, []string{"complex_reasoning", "multi_file_edits", "refactoring"}},
	} {
		got := effortTraits(tc.complexity)
		if !slices.Equal(got, tc.want) {
			t.Fatalf("%s (%.2f): traits = %v, want %v", tc.name, tc.complexity, got, tc.want)
		}
	}
}

// The route must actually receive the difficulty. This is the wiring test: a
// deep task whose title and intent mention nothing technical still has to
// carry the reasoning vocabulary, because that is what makes a card declaring
// best_at: complex_reasoning win the ranking.
func TestRoutingTraitsCarryEffort(t *testing.T) {
	spec := entry.TaskSpecDetail{Kind: entry.TaskEdit, Target: "fix it"}
	deep := routingTraits(Task{Title: "clean up", Intent: "make it right", Complexity: 0.9}, spec)
	if !slices.Contains(deep.Effort, "complex_reasoning") {
		t.Fatalf("deep task routed without its reasoning trait: %v", deep)
	}
	light := routingTraits(Task{Title: "look around", Intent: "find it", Complexity: 0.1}, spec)
	if !slices.Contains(light.Effort, "quick_edits") {
		t.Fatalf("light task routed without its quick-edit trait: %v", light)
	}
	// Standard effort must not smuggle either vocabulary in.
	standard := routingTraits(Task{Title: "clean up", Intent: "make it right", Complexity: 0.5}, spec)
	for _, unwanted := range []string{"complex_reasoning", "quick_edits"} {
		if slices.Contains(standard.Effort, unwanted) {
			t.Fatalf("standard task carries the %q trait: %v", unwanted, standard)
		}
	}
	// The task's own words still matter: they are appended alongside.
	if !slices.Contains(deep.Traits, "clean up") {
		t.Fatalf("title dropped from routing traits: %v", deep)
	}
}
