// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
)

// Both sibling shell processes must reach a shared barrier before either
// exits. A sequential scheduler cannot pass this test. The join consumes
// both independently packed output trees, proving graph execution as well
// as overlapping processes (not merely two rows marked running).
func TestPlanForkJoinRunsRealProcessesConcurrently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process barrier")
	}
	barrier := t.TempDir()
	worker := func(name, other string) ledger.NativeAbility {
		script := `printf '%s\n' "$$" > "$1/$2.started"
i=0
while [ ! -f "$1/$3.started" ]; do
  i=$((i+1)); [ "$i" -lt 200 ] || exit 9
  sleep 0.05
done
kill -0 "$(cat "$1/$3.started")" || exit 10
printf '%s\n' "$2" > "$2.txt"
touch "$1/$2.overlap"
while [ ! -f "$1/$3.overlap" ]; do
  i=$((i+1)); [ "$i" -lt 200 ] || exit 11
  sleep 0.05
done
printf '%s\n' "overlap proven: $2"`
		return ledger.NativeAbility{ID: "proof:" + name, Command: "sh", Args: []string{"-c", script, "barrier", barrier, name, other}, Tier: 1}
	}
	card := ledger.Card{Device: "fork-join", ResourceClass: "Standard",
		Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 3},
		Native: []ledger.NativeAbility{worker("left", "right"), worker("right", "left"),
			{ID: "proof:join", Command: "sh", Args: []string{"-c", "test -s left.txt && test -s right.txt && cat left.txt right.txt > joined.txt && cat joined.txt"}, Tier: 1}},
	}
	c := NewCore(openTestDB(t), "fork-join", card, 5, testLogger(), config.ModelConfig{})
	c.SetWorkDir(t.TempDir())
	withArtifactPool(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := c.Register(ctx); err != nil {
		t.Fatal(err)
	}
	c.StartQueueScheduler(ctx)
	p := plan.Plan{Goal: "prove real fork and join", Stages: []plan.Stage{
		{ID: "left", Title: "left branch", Intent: "left", Requires: []string{"proof:left"}},
		{ID: "right", Title: "right branch", Intent: "right", Requires: []string{"proof:right"}},
		{ID: "join", Title: "join branches", Intent: "join", Requires: []string{"proof:join"}, Needs: []string{"left", "right"}},
	}}
	started := time.Now()
	id, err := c.StartPlan(ctx, p, DefaultQueueSpec())
	if err != nil {
		t.Fatal(err)
	}
	stages := waitPlanDone(t, ctx, c, id, 3)
	left, err := os.ReadFile(filepath.Join(barrier, "left.started"))
	if err != nil {
		t.Fatal(err)
	}
	right, err := os.ReadFile(filepath.Join(barrier, "right.started"))
	if err != nil {
		t.Fatal(err)
	}
	if string(left) == string(right) {
		t.Fatal("siblings used the same process")
	}
	join := stageByID(t, stages, "join")
	if !strings.Contains(join.ResultJSON, "left\\nright") {
		t.Fatalf("join did not consume both predecessor outputs: %s", join.ResultJSON)
	}
	t.Logf("METRIC concurrent_processes=2 left_pid=%s right_pid=%s joined_artifacts=2 elapsed_ms=%d", strings.TrimSpace(string(left)), strings.TrimSpace(string(right)), time.Since(started).Milliseconds())
	for _, stage := range stages {
		events, err := c.store.Events(ctx, stage.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Type == EvAccept || event.Type == EvResult {
				t.Logf("stage=%s event=%s data=%s", stage.StageID, event.Type, event.DataJSON)
			}
		}
	}
}
