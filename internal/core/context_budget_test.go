// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/skills"
)

func TestSkillIndexAndBodiesShareBoundedBudget(t *testing.T) {
	store := skills.NewStore(filepath.Join(t.TempDir(), "skills"))
	for n := range 50 {
		err := store.Save(&skills.Skill{Name: fmt.Sprintf("playbook-%02d", n), Description: "playbook 部署流程", Scope: skills.ScopeGlobal, Status: skills.StatusActive, Body: strings.Repeat("步骤很长\n", 1000)})
		if err != nil {
			t.Fatal(err)
		}
	}
	c := &Core{logger: testLogger(), skills: store}
	intent := strings.Repeat("保留全部用户要求与验收标准。", 1000)
	for _, budget := range []int{-1, 0, 1, 100, 1000, 12000} {
		got, used := withSkills(c, intent, "", "playbook", budget)
		if !strings.HasSuffix(got, intent) || !utf8.ValidString(got) || len(got)-len(intent) > max(0, budget) {
			t.Fatalf("budget=%d optional=%d", budget, len(got)-len(intent))
		}
		for _, sk := range used {
			if !strings.Contains(got, sk.Name) {
				t.Fatalf("counted invisible skill %s", sk.Name)
			}
		}
	}
	got, _ := buildAgentPrompt(c, intent, "", "playbook", "", 12000)
	if !strings.Contains(got, intent) {
		t.Fatal("mandatory intent truncated")
	}
	got, _ = buildAgentPrompt(c, "run playbook", "", "playbook", "", 12000)
	if len(got) > 12000 {
		t.Fatalf("assembled prompt exceeded budget: %d", len(got))
	}
}

func TestAgentContextBudgetTiers(t *testing.T) {
	for _, tc := range []struct {
		complexity float64
		want       int
	}{{0, 32000}, {0.2, 12000}, {0.5, 32000}, {0.9, 64000}} {
		if got := agentContextBudget(Task{Complexity: tc.complexity}); got != tc.want {
			t.Fatalf("%v: %d != %d", tc.complexity, got, tc.want)
		}
	}
}

func TestOversizedSingleRunNoteIsBounded(t *testing.T) {
	c := &Core{logger: testLogger()}
	note := "FIRST EVIDENCE\n" + strings.Repeat("中文记录", 20000) + "\nLATEST FAILURE"
	got := strings.Join(c.compactRunNotes(context.Background(), []string{note}), "\n\n")
	if len(got) > 12000 || !utf8.ValidString(got) || !strings.Contains(got, "FIRST EVIDENCE") || !strings.Contains(got, "LATEST FAILURE") || !strings.Contains(got, "omitted") {
		t.Fatalf("bad bounded note: %d bytes", len(got))
	}
}
