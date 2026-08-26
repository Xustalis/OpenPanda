package entry

import (
	"strings"
	"testing"
)

// TestParseOutputPlan is the model-generated half of the plan entry point. The
// flagship scenario is one sentence spoken at the Pi becoming develop → train →
// report across three machines; before KindPlan existed the model could only emit
// a single task, so that request collapsed into one machine doing everything.
func TestParseOutputPlan(t *testing.T) {
	raw := `{"kind":"plan","plan":{"goal":"训练一个图像分类模型并总结结果","stages":[
		{"id":"develop","title":"写训练脚本","intent":"写 train.py","requires":["agent:claude_code"],
		 "resource_profile":{"cpu":2,"ram_gb":4,"duration_hint":"short"}},
		{"id":"train","title":"跑训练","intent":"运行 train.py","requires":["agent:codex"],"needs":["develop"],
		 "resource_profile":{"cpu":8,"ram_gb":16,"gpu_vram_gb":8,"duration_hint":"long"}},
		{"id":"report","title":"总结","intent":"读 result.txt 写 summary.md","requires":["agent:claude_code"],"needs":["train"],
		 "resource_profile":{"cpu":1,"ram_gb":1,"duration_hint":"short"}}]}}`

	out, err := ParseOutput(raw)
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	if out.Kind != KindPlan {
		t.Fatalf("kind = %q, want %q", out.Kind, KindPlan)
	}
	if out.Plan == nil {
		t.Fatal("plan payload is nil")
	}
	if len(out.Plan.Stages) != 3 {
		t.Fatalf("stages = %d, want 3", len(out.Plan.Stages))
	}
	// The middle stage's VRAM is the whole reason this is a plan and not a task:
	// it is what keeps the training run off the Pi and the Pi's summary stage off
	// the GPU box.
	train := out.Plan.Stages[1]
	if train.Resources.GPUVRAMGB != 8 {
		t.Errorf("train gpu_vram_gb = %v, want 8", train.Resources.GPUVRAMGB)
	}
	if train.Resources.DurationHint != "long" {
		t.Errorf("train duration_hint = %q, want long", train.Resources.DurationHint)
	}
	if len(train.Needs) != 1 || train.Needs[0] != "develop" {
		t.Errorf("train needs = %v, want [develop]", train.Needs)
	}
	if out.Plan.Stages[0].Resources.GPUVRAMGB != 0 {
		t.Errorf("the develop stage asked for VRAM (%v); it would be routed to the GPU box for nothing",
			out.Plan.Stages[0].Resources.GPUVRAMGB)
	}
}

// TestParseOutputPlanMissingPayload pins the one shape that must surface as an
// error rather than degrade to prose: the model committed to kind "plan" and then
// sent no plan. Silently answering would tell the user their pipeline started.
func TestParseOutputPlanMissingPayload(t *testing.T) {
	if _, err := ParseOutput(`{"kind":"plan"}`); err == nil {
		t.Fatal("a plan directive with no plan object was accepted")
	}
}

// TestValidatePlanSpec covers the two mistakes a model actually makes, both of
// which make Needs meaningless: a stage with no id (nothing can depend on it) and
// two stages sharing one (a Needs reference is ambiguous).
func TestValidatePlanSpec(t *testing.T) {
	stage := func(id string) PlanStageSpec {
		return PlanStageSpec{ID: id, Intent: "do it"}
	}
	for _, tc := range []struct {
		name string
		spec PlanSpec
		want string
	}{
		{"no goal", PlanSpec{Stages: []PlanStageSpec{stage("a")}}, "goal"},
		{"no stages", PlanSpec{Goal: "g"}, "stages"},
		{"empty id", PlanSpec{Goal: "g", Stages: []PlanStageSpec{{Intent: "i"}}}, "id is required"},
		{"duplicate id", PlanSpec{Goal: "g", Stages: []PlanStageSpec{stage("a"), stage("a")}}, "two stages named"},
		{"no intent", PlanSpec{Goal: "g", Stages: []PlanStageSpec{{ID: "a"}}}, "no intent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePlanSpec(&tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
	ok := PlanSpec{Goal: "g", Stages: []PlanStageSpec{stage("a"), stage("b")}}
	if err := ValidatePlanSpec(&ok); err != nil {
		t.Fatalf("a valid plan was rejected: %v", err)
	}
}

// TestPlanTurnKeepsTaskLayer checks that a started plan keeps the verbose task
// layer attached on the next call. A plan's stages *are* tasks, so the follow-up
// ("跑到哪了", "把训练那段改成 20 epoch") is a task-mode turn; dropping the layer
// would make the model re-derive the schema from the compact skeleton alone.
func TestPlanTurnKeepsTaskLayer(t *testing.T) {
	layers := ChooseLayers([]Turn{
		{Role: "user", Content: "训练一个模型"},
		{Role: "assistant", Content: "[计划0f3a2b 已启动] 训练一个模型（阶段：develop queued → train submitted）"},
	})
	if !layers.TaskExample {
		t.Error("a started plan did not attach the task layer")
	}
}
