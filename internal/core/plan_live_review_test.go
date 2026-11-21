// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/plan"
)

// Three distinct adapter calls rendezvous before any can finish. The third
// branch stays live until the second has received the supervisor's correction.
func TestBranchVerdictRedirectsLiveSiblingAndAddsVerification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	c := newSuperviseCore(t, "local", 1)
	withArtifactPool(t, c)
	c.SetSuperviseRounds(3)
	apiStarted, docsStarted, corrected := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var apiRuns, docsRuns, followupRuns atomic.Int32
	c.router.SetAdapterRunner(func(run context.Context, adapter, prompt, dir string) commander.AgentResult {
		switch filepath.Base(dir) {
		case "inspect":
			for _, started := range []chan struct{}{apiStarted, docsStarted} {
				select {
				case <-started:
				case <-run.Done():
					return commander.AgentResult{ExitCode: 1, Stderr: "rendezvous cancelled"}
				}
			}
		case "api":
			if apiRuns.Add(1) == 1 {
				close(apiStarted)
				<-run.Done()
				return commander.AgentResult{ExitCode: 1, Stderr: "interrupted"}
			}
			if !strings.Contains(prompt, "Use the revised schema") {
				t.Error("corrected adapter run did not receive the revised schema instruction")
			}
			if apiRuns.Load() == 2 {
				if err := os.WriteFile(filepath.Join(dir, "response.txt"), []byte("revised schema"), 0644); err != nil {
					t.Error(err)
				}
				close(corrected)
			}
		case "docs":
			if docsRuns.Add(1) == 1 {
				close(docsStarted)
			}
			select {
			case <-corrected:
			case <-run.Done():
				return commander.AgentResult{ExitCode: 1, Stderr: "docs cancelled"}
			}
		case "deliver":
			if followupRuns.Load() != 1 {
				t.Error("delivery started before the new verification branch finished")
			}
			if !strings.Contains(prompt, "[Panda plan audit]") || !strings.Contains(prompt, "read_event_id") {
				t.Error("final synthesis did not receive the decision and its read receipt")
			}
		default:
			if data, err := os.ReadFile(filepath.Join(dir, "response.txt")); err != nil || string(data) != "revised schema" {
				t.Errorf("verifier did not receive revised API artifact: %q, %v", data, err)
			}
			followupRuns.Add(1)
			if !strings.Contains(prompt, "Verify the revised response") {
				t.Error("new adapter session lost its verification instruction")
			}
		}
		return commander.AgentResult{OK: true, Result: "completed " + filepath.Base(dir)}
	})
	c.SetSupervisor(newFakeSupervisor(t, func(call int) string {
		if call == 1 {
			return `{"status":"done","reason":"inspection found a schema mismatch","plan_actions":[{"kind":"adjust","target_stage":"api","instruction":"Use the revised schema","reason":"inspection changed the response contract"},{"kind":"open","needs":["api"],"instruction":"Verify the revised response","reason":"the contract needs independent verification"}]}`
		}
		return `{"status":"done","reason":"checked the branch output"}`
	}))
	if err := c.Register(ctx); err != nil {
		t.Fatal(err)
	}
	c.StartQueueScheduler(ctx)
	stage := func(id string, needs ...string) plan.Stage {
		return plan.Stage{ID: id, Intent: "Execute " + id, Requires: []string{"agent:claude_code"}, Needs: needs}
	}
	graph, err := plan.WithSynthesis(plan.Plan{Goal: "deliver a checked response", Stages: []plan.Stage{stage("inspect"), stage("api"), stage("docs"), stage("deliver", "inspect", "api", "docs")}}, "deliver", "Deliver", "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.StartPlan(ctx, graph, DefaultQueueSpec())
	if err != nil {
		t.Fatal(err)
	}
	rows := waitPlanDone(t, ctx, c, id, 5)
	if apiRuns.Load() != 2 || docsRuns.Load() != 1 || followupRuns.Load() != 1 {
		t.Fatalf("api runs=%d, docs runs=%d, follow-up runs=%d", apiRuns.Load(), docsRuns.Load(), followupRuns.Load())
	}
	for _, stage := range rows {
		if stage.StageID == "inspect" {
			events, err := c.store.Events(ctx, stage.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, e := range events {
				if e.Type == EvPlanIntervention {
					t.Log(e.DataJSON)
					n++
				}
			}
			if n != 2 {
				t.Errorf("source recorded %d decisions", n)
			}
		}
	}
}
