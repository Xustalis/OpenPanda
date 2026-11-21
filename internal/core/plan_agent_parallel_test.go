// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
)

// The real Python adapters launch fixture CLIs. A branch can only finish
// after observing the other live process. The join must receive both files.
func TestPlanParallelHarnessSessions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixtures")
	}
	for _, tc := range []struct{ agent, cli string }{{"claude_code", "claude"}, {"codex", "codex"}} {
		t.Run(tc.agent, func(t *testing.T) {
			bin := t.TempDir()
			barrier := t.TempDir()
			script := fmt.Sprintf(`#!/usr/bin/env python3
import json, os, pathlib, sys, time
stage = pathlib.Path.cwd().name
barrier = pathlib.Path(%q)
if stage in ("left", "right"):
    other = "right" if stage == "left" else "left"
    assert pathlib.Path("seed.txt").is_file()
    (barrier / stage).write_text(str(os.getpid()))
    deadline = time.monotonic() + 10
    for marker in [other, other + ".overlap"]:
        while not (barrier / marker).exists():
            assert time.monotonic() < deadline, marker
            time.sleep(0.02)
        if marker == other:
            os.kill(int((barrier / other).read_text()), 0)
            (barrier / (stage + ".overlap")).touch()
elif stage == "join":
    assert all(pathlib.Path(s + ".txt").exists() for s in ("left", "right"))
else:
    assert stage == "seed", stage
pathlib.Path(stage + ".txt").write_text(stage)
sid = "fixture-" + os.environ["PANDA_TASK_ID"]
if pathlib.Path(sys.argv[0]).name == "codex":
    print(json.dumps({"type":"thread.started","thread_id":sid}))
    print(json.dumps({"type":"item.completed","item":{"type":"agent_message","text":"done"}}))
else:
    if "--input-format" in sys.argv:
        json.loads(sys.stdin.readline())
    print(json.dumps({"type":"result","subtype":"success","result":"done","session_id":sid}), flush=True)
`, barrier)
			if err := os.WriteFile(filepath.Join(bin, tc.cli), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("HOME", t.TempDir())
			adapters, err := filepath.Abs("../../adapters")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("OPENPANDA_ADAPTER_DIR", adapters)
			card := ledger.Card{Device: "local", ResourceClass: "Standard", Capacity: ledger.Capacity{CPUCores: 4, RAMGB: 8, MaxConcurrent: 2}, Agents: map[string]ledger.Agent{tc.agent: {Adapter: tc.agent + ".py", Capabilities: []string{"coding"}, Tier: 1}}}
			c := NewCore(openTestDB(t), "local", card, 5, testLogger(), config.ModelConfig{})
			c.SetWorkDir(t.TempDir())
			withArtifactPool(t, c)
			c.router.SetAgentProber(func(string, ledger.Agent) bool { return true })
			c.router.SetEndpointProber(func(commander.ProbeSpec) commander.ProbeVerdict { return commander.ProbeVerdict{OK: true} })
			c.SetSupervisor(newFakeSupervisor(t, func(int) string { return `{"status":"done","reason":"fixture completed"}` }))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.Register(ctx); err != nil {
				t.Fatal(err)
			}
			c.StartQueueScheduler(ctx)
			stage := func(name string, needs ...string) plan.Stage {
				return plan.Stage{ID: name, Intent: name, Requires: []string{"agent:" + tc.agent}, Needs: needs}
			}
			p := plan.Plan{Goal: "fork after seed and join outputs", Stages: []plan.Stage{stage("seed"), stage("left", "seed"), stage("right", "seed"), stage("join", "left", "right")}}
			q := DefaultQueueSpec()
			q.SessionID = "one-conversation"
			id, err := c.StartPlan(ctx, p, q)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range waitPlanDone(t, ctx, c, id, 4) {
				events, err := c.store.Events(ctx, row.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, event := range events {
					if event.Type == "adapter_session_mint" && strings.Contains(event.DataJSON, "fixture-"+row.TaskID) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("missing task-specific session for %s", row.StageID)
				}
			}
			t.Logf("%s concurrent CLI PIDs %s and %s", tc.cli, readFile(t, filepath.Join(barrier, "left")), readFile(t, filepath.Join(barrier, "right")))
		})
	}
}
