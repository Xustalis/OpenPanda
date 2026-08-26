package plan

import (
	"strings"
	"testing"
)

// TestParseExampleYAML pins the shipped example: `panda plan example` is the
// first plan most users will run, so it has to parse and validate as-is. A typo
// in the constant would otherwise only surface on someone's first attempt.
func TestParseExampleYAML(t *testing.T) {
	p, err := Parse([]byte(ExampleYAML))
	if err != nil {
		t.Fatalf("the shipped example plan does not parse: %v", err)
	}
	if len(p.Stages) != 3 {
		t.Fatalf("stages = %d, want 3", len(p.Stages))
	}
	order, err := Order(p)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	got := []string{order[0].ID, order[1].ID, order[2].ID}
	want := []string{"develop", "train", "report"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	// The whole point of the middle stage is that it declares VRAM, which is
	// what keeps a training run off the Pi.
	train := order[1]
	if train.Resources.GPUVRAMGB <= 0 {
		t.Errorf("the train stage declares no VRAM (%+v); it would route anywhere",
			train.Resources)
	}
	if train.Resources.DurationHint != "long" {
		t.Errorf("train duration_hint = %q, want long", train.Resources.DurationHint)
	}
}

// TestParseRejectsUnknownField is the reason KnownFields is on: a misspelled key
// silently dropped is a stage that quietly declares nothing, and "requires"
// dropped from a training stage is how a GPU job lands on a Raspberry Pi.
func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte("goal: g\nstages:\n  - id: a\n    intent: do it\n    reqires: [gpu]\n"))
	if err == nil {
		t.Fatal("a misspelled key was accepted")
	}
	if !strings.Contains(err.Error(), "reqires") {
		t.Errorf("error does not name the bad key: %v", err)
	}
}

// TestParseValidates makes sure a plan file cannot reach StartPlan unvalidated —
// the failure mode that check exists to prevent is a dangling dependency
// discovered only after stages have been dispatched to real machines.
func TestParseValidates(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"no goal", "stages:\n  - id: a\n    intent: i\n", "goal must not be empty"},
		{"no stages", "goal: g\n", "no stages"},
		{"dangling need", "goal: g\nstages:\n  - id: a\n    intent: i\n    needs: [b]\n", "not in the plan"},
		{"cycle", "goal: g\nstages:\n  - id: a\n    intent: i\n    needs: [b]\n  - id: b\n    intent: i\n    needs: [a]\n", "cycle"},
		{"no intent", "goal: g\nstages:\n  - id: a\n", "no intent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// TestOrderFansOut checks Order on a diamond: both middle stages must appear
// after the stage they need and before the one that needs them, and the listing
// must not imply they run one after the other — they are one wave, which is what
// puts them on two machines at once.
func TestOrderFansOut(t *testing.T) {
	p := Plan{Goal: "g", Stages: []Stage{
		{ID: "root", Intent: "i"},
		{ID: "left", Intent: "i", Needs: []string{"root"}},
		{ID: "right", Intent: "i", Needs: []string{"root"}},
		{ID: "join", Intent: "i", Needs: []string{"left", "right"}},
	}}
	if err := Validate(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	order, err := Order(p)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	pos := map[string]int{}
	for i, s := range order {
		pos[s.ID] = i
	}
	if len(order) != 4 {
		t.Fatalf("order = %d stages, want 4", len(order))
	}
	if pos["root"] != 0 || pos["join"] != 3 {
		t.Fatalf("root at %d, join at %d; want 0 and 3", pos["root"], pos["join"])
	}
	// The two independent stages are one wave: Ready returns both, so both are
	// released together rather than serialized by the plan.
	ready := Ready(p, map[string]bool{"root": true})
	if len(ready) != 2 {
		t.Errorf("Ready after root = %d stages, want 2 (left and right in parallel)", len(ready))
	}
}
