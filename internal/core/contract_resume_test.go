// SPDX-License-Identifier: AGPL-3.0-or-later
package core

import (
	"context"
	"encoding/json"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestContractOnlyTurnCapResumesCheckpointOnce(t *testing.T) {
	for _, finish := range []bool{true, false} {
		t.Run(map[bool]string{true: "finishes", false: "bounded"}[finish], func(t *testing.T) {
			c := newSuperviseCore(t, "contract-resume", 1)
			c.SetSupervisor(nil)
			c.SetSuperviseRounds(2)
			c.SetWorkDir(t.TempDir())
			var calls atomic.Int32
			c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
				n := calls.Add(1)
				if n > 1 && commander.ResumeID(ctx) != "real-checkpoint" {
					t.Errorf("checkpoint lost: %q", commander.ResumeID(ctx))
				}
				if n == 2 && finish {
					if err := os.WriteFile(filepath.Join(cwd, "result.txt"), []byte("verified"), 0600); err != nil {
						t.Error(err)
					}
					return commander.AgentResult{OK: true, ExitCode: 0, Result: "wrote result.txt", SessionID: "real-checkpoint"}
				}
				return commander.AgentResult{OK: false, ExitCode: 1, Result: "Agent turn limit reached; task is incomplete.", SessionID: "real-checkpoint"}
			})
			spec, _ := json.Marshal(entry.TaskSpecDetail{Acceptance: []contract.Check{{ID: "output", Exists: []string{"result.txt"}}}})
			task, _, err := c.SubmitLocal(context.Background(), TaskInput{Title: "contract continuation", Intent: "write result.txt", Requires: []string{"code:modify"}, SpecJSON: string(spec)})
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatalf("calls=%d state=%s", calls.Load(), task.State)
			}
			if finish && task.State != StateDone {
				t.Fatalf("state=%s result=%s", task.State, task.ResultJSON)
			}
			if !finish && task.State == StateDone {
				t.Fatal("unfinished task completed")
			}
			if !finish && !task.NeedsFollowup() {
				t.Fatalf("lost incomplete evidence: %s", task.ResultJSON)
			}
		})
	}
}
