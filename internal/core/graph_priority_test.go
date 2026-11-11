// SPDX-License-Identifier: AGPL-3.0-or-later
package core

import (
	"context"
	"encoding/json"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"testing"
)

func TestPlanPriorityPersistsInReadyProjection(t *testing.T) {
	c := NewCore(openTestDB(t), "rank", ledger.Card{}, 5, testLogger(), config.ModelConfig{})
	p := plan.Plan{Goal: "rank", Stages: []plan.Stage{
		{ID: "short", Intent: "short"}, {ID: "long", Intent: "long", Resources: entry.ResourceProfile{DurationHint: "long"}},
		{ID: "join", Intent: "join", Needs: []string{"long"}},
	}}
	ctx := context.Background()
	id, err := c.StartPlan(ctx, p, DefaultQueueSpec())
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := c.store.PlanStages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"short": 1, "long": 5, "join": 1}
	for _, task := range tasks {
		var v map[string]any
		if err = json.Unmarshal([]byte(task.ResourceJSON), &v); err != nil {
			t.Fatal(err)
		}
		if v["critical_path_rank"] != float64(want[task.StageID]) {
			t.Fatalf("rank missing: %v", v)
		}
	}
	ready, err := c.store.ListReadySummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 2 {
		t.Fatalf("ready=%+v", ready)
	}
	for _, r := range ready {
		if r.PlanID != id || r.CriticalPathRank < 1 {
			t.Fatalf("projection lost rank: %+v", r)
		}
	}
}
func TestLoadedSkillEvidenceOnlyDescribesInjectedBodies(t *testing.T) {
	if len(loadedSkillEvidence(nil)) != 0 {
		t.Fatal("invented skills")
	}
	sk := &skills.Skill{Name: "visual", Scope: skills.ScopeGlobal, Body: "rules"}
	a := loadedSkillEvidence([]*skills.Skill{sk})
	if len(a) != 1 || a[0]["body_bytes"] != 5 || len(a[0]["body_sha256"].(string)) != 64 {
		t.Fatalf("invalid evidence: %v", a)
	}
	sk.Body = "different"
	b := loadedSkillEvidence([]*skills.Skill{sk})
	if a[0]["body_sha256"] == b[0]["body_sha256"] {
		t.Fatal("hash failed to change")
	}
}
