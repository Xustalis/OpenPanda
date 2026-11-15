// SPDX-License-Identifier: AGPL-3.0-or-later

package plan

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

func TestSynthesisClosesAllBranchesAndPreservesPins(t *testing.T) {
	p := Plan{Goal: "integrated goal", Stages: []Stage{
		{ID: "a", Intent: "collect readings", Node: "raspberry-pi"},
		{ID: "b", Intent: "build UI", Node: "windows"},
		{ID: "c", Intent: "test UI", Node: "linux", Needs: []string{"b"}},
	}}
	closed, err := WithSynthesis(p, "", "Deliver", "macbook")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Stages) != 3 || len(closed.Stages) != 4 {
		t.Fatal("source graph mutated or closure missing")
	}
	final := closed.Stages[3]
	if strings.Join(final.Needs, ",") != "a,c" || final.Node != "macbook" || closed.Stages[0].Node != "raspberry-pi" {
		t.Fatalf("lost graph/pins: %+v", closed)
	}
	if ready := Ready(closed, map[string]bool{"a": true, "b": true}); len(ready) != 1 || ready[0].ID != "c" {
		t.Fatalf("final released early: %+v", ready)
	}
	var detail entry.TaskSpecDetail
	if err := json.Unmarshal([]byte(final.SpecJSON), &detail); err != nil || !detail.PlanFinal || !strings.Contains(final.Intent, p.Goal) {
		t.Fatalf("missing delivery contract: %+v %v", final, err)
	}
}

func TestExplicitSynthesisMustCoverEveryStage(t *testing.T) {
	p := Plan{Goal: "g", Stages: []Stage{{ID: "a", Intent: "a"}, {ID: "b", Intent: "b"}, {ID: "final", Intent: "integrate", Needs: []string{"a"}}}}
	if _, err := WithSynthesis(p, "final", "", ""); err == nil {
		t.Fatal("orphan branch accepted")
	}
	p.Stages[2].Needs = []string{"a", "b"}
	p.Stages[2].SpecJSON = `{"tools_policy":"extended","max_turns":75,"success_definition":"verified delivery"}`
	closed, err := WithSynthesis(p, "final", "", "")
	if err != nil || len(closed.Stages) != 3 {
		t.Fatalf("unnecessary extra stage: %+v %v", closed, err)
	}
	var detail entry.TaskSpecDetail
	_ = json.Unmarshal([]byte(closed.Stages[2].SpecJSON), &detail)
	if !detail.PlanFinal || detail.MaxTurns != 75 || detail.ToolsPolicy != "extended" || detail.SuccessDefinition != "verified delivery" {
		t.Fatalf("lost existing contract: %+v", detail)
	}
}

func TestSynthesisReservesBoundedStageAndAvoidsCollision(t *testing.T) {
	p := Plan{Goal: "g"}
	for i := 0; i < MaxStages; i++ {
		p.Stages = append(p.Stages, Stage{ID: fmt.Sprintf("s%d", i), Intent: "work"})
	}
	if _, err := WithSynthesis(p, "", "", ""); err == nil {
		t.Fatal("unbounded extra stage")
	}
	p.Stages = []Stage{{ID: "panda-final", Intent: "a"}, {ID: "b", Intent: "b"}}
	closed, err := WithSynthesis(p, "", "", "")
	if err != nil || closed.Stages[2].ID != "panda-final-2" {
		t.Fatalf("collision: %+v %v", closed, err)
	}
}
