package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
)

// trainScript is what the coding stage writes. Its length is the fact the last
// stage reports, so the assertion is derived from this constant rather than
// hard-coded: the number can only be right if the bytes actually travelled.
const trainScript = "import torch\nprint(1)\n"

// TestPlanThreeStagesThreeNodes is the flagship scenario, reduced to the smallest
// thing that still proves it: one plan, three stages, three machines, and an
// output that could not exist unless each stage's tree reached the next one.
//
// The nodes are the real ones. pi is the entry point and the only node the user
// talks to; it has no compute worth the name and can only report. mac has the
// coding ability, win has the training ability, and neither is wired to the
// other — the only links are pi↔mac and pi↔win. That topology is the point: the
// artifact has to be relayed by the plan node, because the node that will run the
// *next* stage is one the producer has never heard of and could not authorize
// (see adoptStageOutput).
//
// The chain is byte-exact rather than merely green. mac writes train.py; win runs
// wc over the file it must have received and writes the count; pi cats the file it
// must have received in turn. Nothing produces the number 22 unless both hops
// happened, and a stage that ran on an empty tree fails on the spot instead of
// reporting a cheerful nothing.
func TestPlanThreeStagesThreeNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pi := newCoreWithNative(t, "pi", "127.0.0.1:17991", ledger.NativeAbility{
		ID: "sys:report", Command: "sh", Args: []string{"-c", "cat metrics.txt"}, Tier: 1,
	})
	mac := newCoreWithNative(t, "mac", "127.0.0.1:17992", ledger.NativeAbility{
		ID: "dev:code", Command: "sh",
		Args: []string{"-c", "printf 'import torch\\nprint(1)\\n' > train.py"}, Tier: 1,
	})
	win := newCoreWithNative(t, "win", "127.0.0.1:17993", ledger.NativeAbility{
		ID: "gpu:train", Command: "sh",
		Args: []string{"-c", "wc -c < train.py | tr -d ' \t' > metrics.txt"}, Tier: 1,
	})
	// Every node needs a pool of its own (an artifact is bytes on one disk) and a
	// work dir of its own: stageWorkDir is derived from the plan and stage ids, so
	// three nodes sharing os.TempDir() in one test process would collide on the
	// same path and pack each other's files.
	for _, c := range []*Core{pi, mac, win} {
		withArtifactPool(t, c)
		c.SetWorkDir(t.TempDir())
	}

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("startup: %v", err)
		}
	}
	must(pi.Register(ctx))
	must(mac.Register(ctx))
	must(win.Register(ctx))
	go func() { _ = pi.Listen(ctx, "127.0.0.1:17991") }()
	go func() { _ = mac.Listen(ctx, "127.0.0.1:17992") }()
	go func() { _ = win.Listen(ctx, "127.0.0.1:17993") }()
	time.Sleep(200 * time.Millisecond)
	must(pi.DialPeer(ctx, "127.0.0.1:17992"))
	must(pi.DialPeer(ctx, "127.0.0.1:17993"))
	time.Sleep(300 * time.Millisecond)

	pi.StartQueueScheduler(ctx)

	p := plan.Plan{
		Goal: "train a model and report the result",
		Stages: []plan.Stage{
			{ID: "develop", Title: "write the training script", Requires: []string{"dev:code"},
				Intent: "write train.py"},
			{ID: "train", Title: "run the training", Requires: []string{"gpu:train"},
				Needs: []string{"develop"}, Intent: "train the model"},
			{ID: "report", Title: "summarize for the user", Requires: []string{"sys:report"},
				Needs: []string{"train"}, Intent: "report the metrics"},
		},
	}
	planID, err := pi.StartPlan(ctx, p, DefaultQueueSpec())
	if err != nil {
		t.Fatalf("start plan: %v", err)
	}

	stages := waitPlanDone(t, ctx, pi, planID, 3)

	// R4: each stage ran where its ability lives, and no two stages shared a node.
	wantOn := map[string]string{"develop": "mac", "train": "win", "report": "pi"}
	seen := map[string]string{}
	for _, st := range stages {
		target, err := pi.store.DispatchTarget(ctx, st.TaskID)
		if err != nil {
			t.Fatalf("dispatch target of %s: %v", st.StageID, err)
		}
		if want := wantOn[st.StageID]; target != want {
			t.Errorf("stage %s ran on %q, want %q", st.StageID, target, want)
		}
		if prev, dup := seen[target]; dup {
			t.Errorf("stages %s and %s both ran on %s", prev, st.StageID, target)
		}
		seen[target] = st.StageID
		if st.OutputArtifact == "" {
			t.Errorf("stage %s recorded no output artifact", st.StageID)
			continue
		}
		// The plan node is the hub, so it must hold every stage's tree — including
		// the two it never executed.
		if _, held := pi.Artifacts().Has(st.OutputArtifact); !held {
			t.Errorf("plan node does not hold %s's artifact %s", st.StageID, st.OutputArtifact)
		}
	}

	// The data plane, end to end: the byte count in the last stage's stdout was
	// computed by win over a file mac wrote, and read back by pi from a file win
	// wrote. Any break in that chain changes this number or fails the stage.
	report := stageByID(t, stages, "report")
	var res bus.TaskResultPayload
	if err := json.Unmarshal([]byte(report.ResultJSON), &res); err != nil {
		t.Fatalf("decode report result (%q): %v", report.ResultJSON, err)
	}
	if got, want := strings.TrimSpace(res.Stdout), strconv.Itoa(len(trainScript)); got != want {
		t.Errorf("report stdout = %q, want %q (the length of train.py)", got, want)
	}
}

// waitPlanDone polls until every stage of the plan is done, failing with the
// stage that got stuck and why. Polling the rows rather than a completion signal
// is deliberate: the plan is only really finished when the store says so, which
// is also what the board and the CLI read.
func waitPlanDone(t *testing.T, ctx context.Context, c *Core, planID string, want int) []Task {
	t.Helper()
	deadline := time.Now().Add(75 * time.Second)
	var stages []Task
	for time.Now().Before(deadline) {
		var err error
		stages, err = c.store.PlanStages(ctx, planID)
		if err != nil {
			t.Fatalf("plan stages: %v", err)
		}
		done := 0
		for _, st := range stages {
			switch st.State {
			case StateDone:
				done++
			case StateFailed, StateCancelled, StateReview:
				dumpStageEvents(t, c, st.TaskID)
				t.Fatalf("stage %s ended in %s: %s", st.StageID, st.State, stageFailure(st))
			}
		}
		if done == want && len(stages) == want {
			return stages
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("plan %s did not finish: %s", planID, stageStates(stages))
	return nil
}

func stageStates(stages []Task) string {
	parts := make([]string, 0, len(stages))
	for _, st := range stages {
		parts = append(parts, fmt.Sprintf("%s=%s", st.StageID, st.State))
	}
	return strings.Join(parts, " ")
}

// stageFailure digs the executor's stderr out of the recorded result so a failing
// stage says what went wrong instead of only that it went wrong.
func stageFailure(st Task) string {
	var res bus.TaskResultPayload
	if err := json.Unmarshal([]byte(st.ResultJSON), &res); err != nil {
		return st.ResultJSON
	}
	return strings.TrimSpace(res.Stderr + " " + res.Stdout)
}

// dumpStageEvents replays a stage's audit chain into the test log. A plan that
// goes wrong goes wrong somewhere in the routing — released, forwarded, accepted,
// declined — and the chain is the only record that says which hop it was.
func dumpStageEvents(t *testing.T, c *Core, taskID string) {
	t.Helper()
	evs, err := c.store.Events(context.Background(), taskID)
	if err != nil {
		t.Logf("events of %s: %v", taskID, err)
		return
	}
	for _, e := range evs {
		t.Logf("  %s %s", e.Type, e.DataJSON)
	}
}

func stageByID(t *testing.T, stages []Task, id string) Task {
	t.Helper()
	for _, st := range stages {
		if st.StageID == id {
			return st
		}
	}
	t.Fatalf("plan has no stage %q", id)
	return Task{}
}
