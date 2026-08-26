package plan

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// flagship is the plan the project exists to run: develop the training code on a
// laptop, train on the machine with the GPU, then summarize for the user. It is
// the fixture for the linear-chain behaviour every other case is measured against.
func flagship() Plan {
	return Plan{
		Goal: "train a small classifier and report the result",
		Stages: []Stage{
			{
				ID: "develop", Title: "write the training script",
				Requires: []string{"agent:coding"},
				Intent:   "write train.py for a small image classifier",
			},
			{
				ID: "train", Title: "run the training",
				Requires:  []string{"agent:shell"},
				Needs:     []string{"develop"},
				Resources: entry.ResourceProfile{GPUVRAMGB: 8, DurationHint: "long"},
				Intent:    "run train.py and save the weights",
			},
			{
				ID: "report", Title: "summarize the outcome",
				Needs:  []string{"train"},
				Intent: "summarize the training metrics for the user",
			},
		},
	}
}

func TestValidateAcceptsFlagshipPlan(t *testing.T) {
	if err := Validate(flagship()); err != nil {
		t.Fatalf("the flagship plan must be valid: %v", err)
	}
}

// TestValidateRejectsBrokenPlans covers the failures that would otherwise appear
// only after stages had been dispatched: a stage waiting forever on a dependency
// that does not exist, or a plan where nothing is ever ready and no error is
// raised anywhere.
func TestValidateRejectsBrokenPlans(t *testing.T) {
	cases := []struct {
		name string
		want string
		p    Plan
	}{
		{
			name: "no goal",
			want: "goal",
			p:    Plan{Stages: []Stage{{ID: "a", Intent: "do a"}}},
		},
		{
			name: "no stages",
			want: "no stages",
			p:    Plan{Goal: "g"},
		},
		{
			name: "empty stage id",
			want: "has no id",
			p:    Plan{Goal: "g", Stages: []Stage{{ID: "", Intent: "do it"}}},
		},
		{
			name: "padded stage id",
			want: "whitespace",
			p:    Plan{Goal: "g", Stages: []Stage{{ID: " a ", Intent: "do it"}}},
		},
		{
			name: "duplicate stage id",
			want: "share the id",
			p: Plan{Goal: "g", Stages: []Stage{
				{ID: "a", Intent: "first"},
				{ID: "a", Intent: "second"},
			}},
		},
		{
			name: "stage with no intent",
			want: "no intent",
			p:    Plan{Goal: "g", Stages: []Stage{{ID: "a", Title: "a"}}},
		},
		{
			name: "dangling need",
			want: "not in the plan",
			p: Plan{Goal: "g", Stages: []Stage{
				{ID: "a", Intent: "first", Needs: []string{"ghost"}},
			}},
		},
		{
			name: "self dependency",
			want: "depends on itself",
			p: Plan{Goal: "g", Stages: []Stage{
				{ID: "a", Intent: "first", Needs: []string{"a"}},
			}},
		},
		{
			name: "repeated need",
			want: "twice",
			p: Plan{Goal: "g", Stages: []Stage{
				{ID: "a", Intent: "first"},
				{ID: "b", Intent: "second", Needs: []string{"a", "a"}},
			}},
		},
		{
			name: "two stage cycle",
			want: "cycle",
			p: Plan{Goal: "g", Stages: []Stage{
				{ID: "a", Intent: "first", Needs: []string{"b"}},
				{ID: "b", Intent: "second", Needs: []string{"a"}},
			}},
		},
		{
			name: "three stage cycle",
			want: "cycle",
			p: Plan{Goal: "g", Stages: []Stage{
				{ID: "a", Intent: "first", Needs: []string{"c"}},
				{ID: "b", Intent: "second", Needs: []string{"a"}},
				{ID: "c", Intent: "third", Needs: []string{"b"}},
			}},
		},
		{
			name: "cycle reachable from an acyclic stage",
			want: "cycle",
			p: Plan{Goal: "g", Stages: []Stage{
				{ID: "root", Intent: "entry", Needs: []string{"a"}},
				{ID: "a", Intent: "first", Needs: []string{"b"}},
				{ID: "b", Intent: "second", Needs: []string{"a"}},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.p)
			if err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestValidateRejectsOversizedPlan: every stage is a live task with a lease and a
// delegation, so a model that emits a thousand of them must be stopped at the
// boundary rather than at the scheduler.
func TestValidateRejectsOversizedPlan(t *testing.T) {
	p := Plan{Goal: "too much"}
	for i := 0; i <= MaxStages; i++ {
		p.Stages = append(p.Stages, Stage{ID: string(rune('a'+i%26)) + itoa(i), Intent: "work"})
	}
	err := Validate(p)
	if err == nil || !strings.Contains(err.Error(), "exceeds the limit") {
		t.Fatalf("Validate accepted %d stages: %v", len(p.Stages), err)
	}
}

// itoa avoids pulling strconv in for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestReadyWalksLinearChainOneStageAtATime is the R4 shape: the chain advances
// only as each stage completes, so the training stage is never dispatched before
// the code it trains on exists.
func TestReadyWalksLinearChainOneStageAtATime(t *testing.T) {
	p := flagship()
	done := map[string]bool{}

	first := Ready(p, done)
	if len(first) != 1 || first[0].ID != "develop" {
		t.Fatalf("first ready = %v, want [develop]", ids(first))
	}

	done["develop"] = true
	second := Ready(p, done)
	if len(second) != 1 || second[0].ID != "train" {
		t.Fatalf("after develop, ready = %v, want [train]", ids(second))
	}

	done["train"] = true
	third := Ready(p, done)
	if len(third) != 1 || third[0].ID != "report" {
		t.Fatalf("after train, ready = %v, want [report]", ids(third))
	}

	done["report"] = true
	if rest := Ready(p, done); len(rest) != 0 {
		t.Fatalf("finished plan still offers %v", ids(rest))
	}
	if !Complete(p, done) {
		t.Fatalf("plan with every stage done is not Complete")
	}
}

// TestReadyFansOutInParallel is the other half of the same function and the
// reason it returns a slice: two stages that do not depend on each other are
// offered together, and the queue puts them on two nodes at once. This is the
// load-balancing case from the user's "publish several tasks and split them
// across two devices".
func TestReadyFansOutInParallel(t *testing.T) {
	p := Plan{
		Goal: "process two datasets and merge",
		Stages: []Stage{
			{ID: "prep", Intent: "fetch the data"},
			{ID: "left", Needs: []string{"prep"}, Intent: "process the left half"},
			{ID: "right", Needs: []string{"prep"}, Intent: "process the right half"},
			{ID: "merge", Needs: []string{"left", "right"}, Intent: "merge the halves"},
		},
	}
	if err := Validate(p); err != nil {
		t.Fatalf("validate fan-out: %v", err)
	}

	done := map[string]bool{"prep": true}
	parallel := Ready(p, done)
	if len(parallel) != 2 || parallel[0].ID != "left" || parallel[1].ID != "right" {
		t.Fatalf("ready after prep = %v, want [left right]", ids(parallel))
	}

	// A join waits for *all* of its inputs: one half finishing must not release
	// the merge, or it would run on half the data.
	done["left"] = true
	if got := Ready(p, done); len(got) != 1 || got[0].ID != "right" {
		t.Fatalf("ready with one half done = %v, want [right]", ids(got))
	}
	done["right"] = true
	if got := Ready(p, done); len(got) != 1 || got[0].ID != "merge" {
		t.Fatalf("ready with both halves done = %v, want [merge]", ids(got))
	}
	if Complete(p, done) {
		t.Fatalf("plan reported complete with merge outstanding")
	}
}

// TestReadyIgnoresUnknownDoneKeys: the caller's completion set comes from the
// database and may name stages from another plan.
func TestReadyIgnoresUnknownDoneKeys(t *testing.T) {
	p := flagship()
	got := Ready(p, map[string]bool{"unrelated-stage": true})
	if len(got) != 1 || got[0].ID != "develop" {
		t.Fatalf("ready = %v, want [develop]", ids(got))
	}
}

// TestInputsIsSortedAndScoped pins the artifact wiring: a stage's inputs are
// exactly the stages it needs, in a stable order.
func TestInputsIsSortedAndScoped(t *testing.T) {
	p := Plan{
		Goal: "g",
		Stages: []Stage{
			{ID: "b", Intent: "b"},
			{ID: "a", Intent: "a"},
			{ID: "join", Needs: []string{"b", "a"}, Intent: "join"},
		},
	}
	got := Inputs(p, "join")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("Inputs(join) = %v, want [a b]", got)
	}
	if got := Inputs(p, "a"); len(got) != 0 {
		t.Fatalf("Inputs(a) = %v, want none", got)
	}
	if got := Inputs(p, "ghost"); got != nil {
		t.Fatalf("Inputs of an unknown stage = %v, want nil", got)
	}
	if _, ok := Find(p, "ghost"); ok {
		t.Fatalf("Find invented a stage")
	}
}

func ids(stages []Stage) []string {
	out := make([]string, 0, len(stages))
	for _, s := range stages {
		out = append(out, s.ID)
	}
	return out
}
