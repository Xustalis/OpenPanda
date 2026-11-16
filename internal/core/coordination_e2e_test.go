// SPDX-License-Identifier: AGPL-3.0-or-later
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/objectives"
)

// Three independent kernels, stores, artifact pools and authenticated WS links.
// Native POSIX fixtures stand in for agent/model/GPU work; this is deliberately
// not a claim of real Windows training or Orange Pi hardware acceptance.
func TestImmediateCoordinationReverseRepairThreeNodes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX deterministic execution fixtures")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	control := t.TempDir()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	wait := func(name string) string {
		return "while [ ! -f " + quote(filepath.Join(control, name)) + " ]; do sleep 0.02; done; "
	}
	touch := func(name string) string { return "touch " + quote(filepath.Join(control, name)) + "; " }
	mac := newCoreWithNative(t, "mac", ledger.NativeAbility{ID: "test:repair", Command: "sh", Args: []string{"-c", "set -eu; grep -q broken train.txt; printf 'fixed\\n' > train.txt; " + touch("repair-ready") + wait("repair-release") + "grep -q verified evidence.txt; printf 'repair accepted\\n'"}, Tier: 1})
	win := newCoreWithNative(t, "win", ledger.NativeAbility{ID: "test:train", Command: "sh", Args: []string{"-c", "set -eu; grep -q broken train.txt; " + touch("train-ready") + wait("train-release") + "grep -q fixed train.txt; grep -q verified evidence.txt; printf 'training accepted\\n' > metrics.txt; cat metrics.txt"}, Tier: 1})
	pi := newCoreWithNative(t, "orange-pi", ledger.NativeAbility{ID: "test:verify", Command: "sh", Args: []string{"-c", "set -eu; grep -q fixed train.txt; printf 'verified fixed snapshot\\n' > evidence.txt; cat evidence.txt"}, Tier: 1})
	nodes := []*Core{mac, win, pi}
	for _, c := range nodes {
		withArtifactPool(t, c)
		c.card.Capacity.MaxConcurrent = 1
		if err := c.Register(ctx); err != nil {
			t.Fatal(err)
		}
	}
	macAddr := startListen(t, ctx, mac, "127.0.0.1:0")
	winAddr := startListen(t, ctx, win, "127.0.0.1:0")
	piAddr := startListen(t, ctx, pi, "127.0.0.1:0")
	for _, link := range []struct {
		c    *Core
		addr string
	}{{mac, winAddr}, {mac, piAddr}, {win, piAddr}} {
		if err := link.c.DialPeer(ctx, link.addr); err != nil {
			t.Fatal(err)
		}
	}
	_ = macAddr
	for _, c := range nodes {
		for _, peer := range nodes {
			if c != peer {
				waitPeer(t, c, peer.nodeID)
			}
		}
		c.StartQueueScheduler(ctx)
	}
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "train.txt"), "broken\n")
	root := runToRunning(t, mac, "coordination-root", "train and verify")
	goals := objectiveFixturePolicy()
	goals.Dimensions[0].Priority = objectives.Normal
	policy, _ := json.Marshal(entry.TaskSpecDetail{Objectives: goals})
	if err := mac.store.SetDetail(ctx, root.TaskID, TaskDetail{SpecJSON: string(policy), Requires: []string{"test:train", "test:repair", "test:verify"}}); err != nil {
		t.Fatal(err)
	}
	if err := mac.store.SetExecWorkDir(ctx, root.TaskID, source); err != nil {
		t.Fatal(err)
	}
	if err := mac.store.SetAuthorized(ctx, root.TaskID, true); err != nil {
		t.Fatal(err)
	}
	if err := mac.store.SetDelegationBudget(ctx, root.TaskID, 30); err != nil {
		t.Fatal(err)
	}
	if err := mac.store.SetTokenBudget(ctx, root.TaskID, 12000); err != nil {
		t.Fatal(err)
	}
	root, _ = mac.store.Get(ctx, root.TaskID)
	rpc := func(c *Core, parent Task) func(ChildRPCRequest) map[string]any {
		t.Helper()
		token := "test-attempt-capability"
		endpoint := httptest.NewServer(&childRPCServer{c: c, ctx: ctx, parent: parent, token: token})
		t.Cleanup(endpoint.Close)
		return func(req ChildRPCRequest) map[string]any {
			t.Helper()
			req.AttemptID = parent.AttemptID
			raw, _ := json.Marshal(req)
			call, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL+"/children", bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			call.Header.Set("Authorization", "Bearer "+token)
			response, err := http.DefaultClient.Do(call)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("coordination %s on %s: status=%d body=%s err=%v", req.Operation, c.nodeID, response.StatusCode, data, err)
			}
			var out map[string]any
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	// Exercise the live HTTP handler with the exact task/attempt capability.
	rootRPC := rpc(mac, root)
	train := rootRPC(ChildRPCRequest{Operation: "spawn", RequestID: "train", Title: "train", Requires: []string{"test:train"}, Node: "win", TokenBudget: 8000, DelegationBudget: 20})
	trainID := train["task_id"].(string)
	rootAwait := make(chan map[string]any, 1)
	go func() { rootAwait <- rootRPC(ChildRPCRequest{Operation: "await", TaskID: trainID, TimeoutS: 50}) }()
	waitRunning := func(c *Core, id string) Task {
		t.Helper()
		for {
			task, err := c.store.Get(ctx, id)
			if err == nil && task.State == StateRunning && task.ExecWorkDir != "" {
				return task
			}
			if err == nil && (Terminal(task.State) || task.State == StateReview) {
				dumpStageEvents(t, c, id)
				t.Fatalf("task stopped: %s %s", task.State, stageFailure(task))
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	waitFile := func(name string) {
		t.Helper()
		for {
			if _, err := os.Stat(filepath.Join(control, name)); err == nil {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	training := waitRunning(win, trainID)
	waitFile("train-ready")
	trainRPC := rpc(win, training)
	repair := trainRPC(ChildRPCRequest{Operation: "spawn", RequestID: "repair", Title: "repair", Requires: []string{"test:repair"}, Node: "mac", TokenBudget: 4000, DelegationBudget: 10})
	repairID := repair["task_id"].(string)
	trainingAwait := make(chan map[string]any, 1)
	go func() { trainingAwait <- trainRPC(ChildRPCRequest{Operation: "await", TaskID: repairID, TimeoutS: 40}) }()
	repairing := waitRunning(mac, repairID)
	waitFile("repair-ready")
	if len(repairing.Chain) != 2 || repairing.Chain[0] != "win" || repairing.Chain[1] != "mac" {
		t.Fatalf("causal ancestry poisoned fresh route: %v", repairing.Chain)
	}
	repairRPC := rpc(mac, repairing)
	verification := repairRPC(ChildRPCRequest{Operation: "spawn", RequestID: "verify", Title: "verify", Requires: []string{"test:verify"}, Node: "orange-pi", TokenBudget: 1000, DelegationBudget: 4})
	verified := repairRPC(ChildRPCRequest{Operation: "await", TaskID: verification["task_id"].(string), TimeoutS: 20})
	if verified["state"] != StateDone || verified["output_artifact"] == "" || verified["input_version"] == "" {
		t.Fatalf("verification not delivered: %+v", verified)
	}
	if data, err := os.ReadFile(filepath.Join(repairing.ExecWorkDir, "evidence.txt")); err != nil || !strings.Contains(string(data), "verified") {
		t.Fatalf("Mac resumed without Pi evidence: %s %v", data, err)
	}
	writeFile(t, filepath.Join(control, "repair-release"), "go")
	select {
	case result := <-trainingAwait:
		if result["state"] != StateDone || result["input_version"] == "" {
			t.Fatalf("repair not adopted: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if data, err := os.ReadFile(filepath.Join(training.ExecWorkDir, "train.txt")); err != nil || strings.TrimSpace(string(data)) != "fixed" {
		t.Fatalf("Windows resumed on wrong version: %s %v", data, err)
	}
	writeFile(t, filepath.Join(control, "train-release"), "go")
	select {
	case result := <-rootAwait:
		if result["state"] != StateDone {
			t.Fatalf("training failed: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, name := range []string{"train.txt", "evidence.txt", "metrics.txt"} {
		if _, err := os.Stat(filepath.Join(source, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range []struct {
		c            *Core
		id, executor string
	}{{mac, trainID, "win"}, {win, repairID, "mac"}, {mac, verification["task_id"].(string), "orange-pi"}} {
		stored, err := pair.c.store.Get(ctx, pair.id)
		if err != nil {
			t.Fatal(err)
		}
		var result bus.TaskResultPayload
		if err := json.Unmarshal([]byte(stored.ResultJSON), &result); err != nil {
			t.Fatal(err)
		}
		if result.Executor != pair.executor || stored.OutputArtifact == "" || !strings.Contains(stored.SpecJSON, "Regression passes") || !stored.Authorized {
			t.Fatalf("lost executor/artifact/objectives/grant: %+v result=%+v", stored, result)
		}
		if _, held := pair.c.artifacts.Has(stored.OutputArtifact); !held {
			t.Fatalf("missing artifact %s", stored.OutputArtifact)
		}
	}
	t.Logf("simulated reverse repair passed: %s -> win -> mac -> orange-pi; file bytes, hashes, objectives and resumed versions checked", fmt.Sprint(root.TaskID))
	for _, c := range nodes {
		waitArtifactWrites(t, c)
	}
}
