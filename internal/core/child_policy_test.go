// SPDX-License-Identifier: AGPL-3.0-or-later
package core

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/trust"
)

func TestChildRPCDurableRetryAcrossEndpointRecreation(t *testing.T) {
	c, parent, first := runningRPCParent(t)
	req := ChildRPCRequest{Operation: "spawn", RequestID: "repair", Title: "repair", Requires: []string{"sys:info"}}
	a, err := first.spawn(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second := &childRPCServer{c: c, ctx: context.Background(), parent: parent}
	b, err := second.spawn(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a.(map[string]any)["task_id"] != b.(map[string]any)["task_id"] {
		t.Fatal("endpoint restart duplicated child")
	}
	req.Title = "changed"
	if _, err := second.spawn(context.Background(), req); err == nil {
		t.Fatal("conflicting retry after restart accepted")
	}
}

func TestChildAllocationsAndRequestAtomicity(t *testing.T) {
	c, parent, _ := runningRPCParent(t)
	ctx := context.Background()
	if err := c.store.SetTokenBudget(ctx, parent.TaskID, 1000); err != nil {
		t.Fatal(err)
	}
	if err := c.store.SetDelegationBudget(ctx, parent.TaskID, 10); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			_, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "worker", TokenAllocation: 300, DelegationAllocation: 2})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	p, _ := c.store.Get(ctx, parent.TaskID)
	children, _ := c.store.Children(ctx, parent.TaskID)
	total := p.TokenBudget
	for _, ch := range children {
		total += ch.TokenBudget
	}
	if total != 1000 || p.TokenBudget != 400 || p.DelegationBudget != 6 {
		t.Fatalf("quota duplicated: total=%d parent=%+v", total, p)
	}
	if _, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "excess", TokenAllocation: 400}); err == nil {
		t.Fatal("parent resume reserve spent")
	}
	// Fail the final ledger insert. Child and both quota debits must roll back.
	if _, err := c.store.db.Exec(`CREATE TRIGGER fail_child_request BEFORE INSERT ON task_child_requests BEGIN SELECT RAISE(ABORT,'injected ledger failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "atomic", TokenAllocation: 50, childRequestID: "once", childRequestHash: "hash"})
	if err == nil {
		t.Fatal("injected failure ignored")
	}
	after, _ := c.store.Get(ctx, parent.TaskID)
	latest, _ := c.store.Children(ctx, parent.TaskID)
	if len(latest) != len(children) || after.TokenBudget != p.TokenBudget || after.DelegationBudget != p.DelegationBudget {
		t.Fatal("partial child or allocation survived rollback")
	}
}

func TestChildScopeAndBoundedExecutionGrant(t *testing.T) {
	c, parent, _ := runningRPCParent(t)
	ctx := context.Background()
	_ = c.store.SetScopePaths(ctx, parent.TaskID, []string{"src"})
	_ = c.store.SetAuthorized(ctx, parent.TaskID, true)
	_ = c.store.SetDetail(ctx, parent.TaskID, TaskDetail{Requires: []string{"coding"}})
	ch, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "safe", Requires: []string{"coding"}, Paths: []string{"src/api"}, inheritExecutionGrant: true})
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Grant ChildGrant `json:"child_grant"`
	}
	_ = json.Unmarshal([]byte(ch.SpecJSON), &spec)
	if !ch.Authorized || spec.Grant.SourceAttempt != parent.AttemptID || len(spec.Grant.Paths) != 1 {
		t.Fatalf("missing scoped grant: %+v", ch)
	}
	if taskScope(ch.SpecJSON) != "src/api" {
		t.Fatal("execution scope widened the child path grant")
	}
	for _, paths := range [][]string{{"outside"}, {"../src"}, {"/src"}} {
		if _, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "escape", Paths: paths}); err == nil {
			t.Fatalf("scope escaped: %v", paths)
		}
	}
	ch, err = c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "new ability", Requires: []string{"hardware:screen"}, inheritExecutionGrant: true})
	if err != nil {
		t.Fatal(err)
	}
	if ch.Authorized {
		t.Fatal("blanket authorization inherited across capabilities")
	}
}

func TestChildGrantEnforcesLegacyModelPathScope(t *testing.T) {
	c, parent, _ := runningRPCParent(t)
	ctx := context.Background()
	if err := c.store.SetDetail(ctx, parent.TaskID, TaskDetail{SpecJSON: "{\"scope\":\"src,tests\"}"}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []TaskInput{{Title: "outside", Paths: []string{"secrets"}}, {Title: "other tree", WorkDir: t.TempDir()}} {
		if _, err := c.SpawnChildTask(ctx, parent.TaskID, in); err == nil {
			t.Fatal("model path scope was not enforced")
		}
	}
	child, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "within scope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(child.ScopePaths) != 2 || child.ScopePaths[0] != "src" || child.ScopePaths[1] != "tests" {
		t.Fatalf("legacy model scope did not become a bounded child grant: %v", child.ScopePaths)
	}
}

func TestExecutionProtocolCapability(t *testing.T) {
	c := newCore(t, "coordinator")
	if c.summary().Capacity.ExecutionProtocol != bus.ExecutionProtocolVersion {
		t.Fatal("protocol not advertised")
	}
	for _, cap := range []string{bus.CapObjectives, bus.CapActivities, bus.CapCoordination} {
		found := false
		for _, v := range c.helloCaps() {
			found = found || v == cap
		}
		if !found {
			t.Fatal(cap)
		}
	}
	nodes := []ledger.Node{{ID: "old"}, {ID: "new", Capacity: ledger.Capacity{ExecutionProtocol: 1}}}
	eligible := c.eligibleForSend(context.Background(), trust.Needs{MinExecutionProtocol: 1}, nodes)
	if len(eligible) != 1 || eligible[0].ID != "new" {
		t.Fatal("legacy route retained")
	}
	if err := ledger.UpsertRemote(c.db, "old", ledger.CapabilitySummary{Device: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := c.requireArtifactProtocol(context.Background(), "old", `{"coordination":"IMMEDIATE"}`); err == nil {
		t.Fatal("legacy pin accepted")
	}
}

func TestChildCannotResetExhaustedRemoteBudgetOrWidenPolicy(t *testing.T) {
	c, parent, _ := runningRPCParent(t)
	ctx := context.Background()
	if _, err := c.store.db.Exec("UPDATE tasks SET delegation_budget=0 WHERE task_id=?", parent.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "launder quota"}); err == nil {
		t.Fatal("remote exhausted quota reset")
	}
	_ = c.store.SetDelegationBudget(ctx, parent.TaskID, 20)
	_ = c.store.SetDetail(ctx, parent.TaskID, TaskDetail{SpecJSON: "{\"scope\":\"src\",\"permission_mode\":\"plan\",\"tools_policy\":\"minimal\",\"constraints\":[\"do not publish\"],\"allow_agents\":[\"claude_code\"],\"max_turns\":12}"})
	child, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "bounded", SpecJSON: "{\"scope\":\"outside\",\"permission_mode\":\"auto\",\"tools_policy\":\"extended\",\"max_turns\":100}"})
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Scope          string
		PermissionMode string `json:"permission_mode"`
		ToolsPolicy    string `json:"tools_policy"`
		Constraints    []string
		AllowAgents    []string `json:"allow_agents"`
		MaxTurns       int      `json:"max_turns"`
	}
	if err := json.Unmarshal([]byte(child.SpecJSON), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Scope != "src" || spec.PermissionMode != "plan" || spec.ToolsPolicy != "minimal" || spec.MaxTurns != 12 || len(spec.Constraints) != 1 || len(spec.AllowAgents) != 1 {
		t.Fatalf("parent policy weakened: %+v", spec)
	}
	if _, err := c.SpawnChildTask(ctx, parent.TaskID, TaskInput{Title: "wider agent", SpecJSON: "{\"allow_agents\":[\"other\"]}"}); err == nil {
		t.Fatal("agent ceiling widened")
	}
}
