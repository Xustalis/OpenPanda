package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseAgentList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"  ", nil},
		{"claude_code", []string{"claude_code"}},
		{"claude_code,codex", []string{"claude_code", "codex"}},
		{" claude_code , codex ,, gemini ", []string{"claude_code", "codex", "gemini"}},
		// "agent:" prefixes are how requirements spell harnesses; accept them.
		{"agent:claude_code,agent:codex", []string{"claude_code", "codex"}},
		// Duplicates collapse but first-seen order survives — it is the
		// serial chain order.
		{"codex,claude_code,codex", []string{"codex", "claude_code"}},
		{"a,a,a", []string{"a"}},
	}
	for _, c := range cases {
		if got := parseAgentList(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseAgentList(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBuildMultiAgentPlanParallel(t *testing.T) {
	p, err := buildMultiAgentPlan([]string{"claude_code", "codex"}, "parallel",
		"Fix the bug", "Fix the bug", []string{"coding"}, false)
	if err != nil {
		t.Fatalf("buildMultiAgentPlan: %v", err)
	}
	if p.Goal != "Fix the bug" {
		t.Fatalf("goal = %q", p.Goal)
	}
	if len(p.Stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(p.Stages))
	}
	for i, st := range p.Stages {
		if len(st.Needs) != 0 {
			t.Errorf("stage %d Needs = %v, want none in parallel", i, st.Needs)
		}
		if st.Intent != "Fix the bug" {
			t.Errorf("stage %d intent = %q", i, st.Intent)
		}
	}
	if got := p.Stages[0].Requires; !reflect.DeepEqual(got, []string{"agent:claude_code"}) {
		t.Errorf("stage 0 requires = %v", got)
	}
	if got := p.Stages[1].Requires; !reflect.DeepEqual(got, []string{"agent:codex"}) {
		t.Errorf("stage 1 requires = %v", got)
	}
}

func TestBuildMultiAgentPlanSerial(t *testing.T) {
	for _, mode := range []string{"serial", "chain", "sequential"} {
		p, err := buildMultiAgentPlan([]string{"claude_code", "codex", "gemini"}, mode,
			"Ship it", "Ship it", []string{"coding"}, false)
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if len(p.Stages) != 3 {
			t.Fatalf("mode %s: stages = %d", mode, len(p.Stages))
		}
		if len(p.Stages[0].Needs) != 0 {
			t.Errorf("mode %s: stage 0 Needs = %v, want none", mode, p.Stages[0].Needs)
		}
		for i := 1; i < len(p.Stages); i++ {
			want := []string{p.Stages[i-1].ID}
			if !reflect.DeepEqual(p.Stages[i].Needs, want) {
				t.Errorf("mode %s: stage %d Needs = %v, want %v", mode, i, p.Stages[i].Needs, want)
			}
		}
	}
}

func TestBuildMultiAgentPlanExplicitRequires(t *testing.T) {
	// An explicit --requires list rides along inside every stage, on top of
	// the agent pin — the default "coding" does not (it is CLI filler).
	p, err := buildMultiAgentPlan([]string{"claude_code"}, "serial",
		"Do it", "Do it", []string{"gpu", "coding"}, true)
	if err != nil {
		t.Fatalf("buildMultiAgentPlan: %v", err)
	}
	want := []string{"agent:claude_code", "gpu", "coding"}
	if !reflect.DeepEqual(p.Stages[0].Requires, want) {
		t.Fatalf("requires = %v, want %v", p.Stages[0].Requires, want)
	}
}

func TestBuildMultiAgentPlanBadMode(t *testing.T) {
	_, err := buildMultiAgentPlan([]string{"a", "b"}, "bogus", "t", "t", nil, false)
	if !errors.Is(err, errBadAgentMode) {
		t.Fatalf("err = %v, want errBadAgentMode", err)
	}
}

func TestStageIDName(t *testing.T) {
	cases := map[string]string{
		"claude_code": "claude_code",
		"Claude Code": "claude-code",
		"codex":       "codex",
		"a/b.c@d":     "a-b-c-d",
	}
	for in, want := range cases {
		if got := stageIDName(in); got != want {
			t.Errorf("stageIDName(%q) = %q, want %q", in, got, want)
		}
	}
}
