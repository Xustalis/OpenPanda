// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/todos"
)

func TestMCPWorkProjectAndUnknownAmbientIsolation(t *testing.T) {
	d := selfDeps(t)
	ctx := context.Background()
	task, err := d.tasks.Create(ctx, "", "alpha", "project agent", "node", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PANDA_TASK_ID", task.TaskID)
	for _, project := range []string{"alpha", "beta", ""} {
		if _, err := todos.NewStore(d.db).Create(ctx, todos.CreateInput{Title: "goal-" + project, Project: project}); err != nil {
			t.Fatal(err)
		}
	}
	output, err := d.toolWorkList(ctx, nil)
	if err != nil || !strings.Contains(output, "goal-alpha") || strings.Contains(output, "goal-beta") || strings.Contains(output, "\"title\":\"goal-\"") {
		t.Fatalf("list=%s err=%v", output, err)
	}
	if _, err = d.toolWorkShow(ctx, map[string]any{"connection": "local", "key": "local/local/local/2"}); err == nil {
		t.Fatal("foreign project task was visible")
	}
	if _, err = d.toolWorkList(ctx, map[string]any{"project": "beta"}); err == nil {
		t.Fatal("caller enlarged project scope")
	}
	t.Setenv("PANDA_TASK_ID", "missing")
	if _, err = d.toolWorkList(ctx, nil); err == nil {
		t.Fatal("unresolved task became personal scope")
	}
}

func mcpRemoteWork(t *testing.T, d *selfToolsDeps) {
	t.Helper()
	snapshot, err := json.Marshal(taskproviders.Snapshot{Key: taskproviders.Key{InstanceID: "i", WorkspaceID: "w", SpaceID: "s", TaskID: "t"}, ProjectID: "remote", Title: "remote goal", InputRevision: "input", Permissions: taskproviders.Permissions{Read: true, AuthEpoch: "auth"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO task_connections(connection_id,provider,account_id,instance_id,auth_epoch,state,spaces_json,scopes_json,updated_at) VALUES('c','tsync','a','i','auth','connected','[{\"spaceId\":\"s\",\"workspaceId\":\"w\",\"scopes\":[\"tasks:read\"]}]','[\"tasks:read\"]',1)",
		"INSERT INTO task_flow_runs(request_id,connection_id,account_id,task_key,trigger_key,auth_epoch,connection_generation,input_revision,assignment_revision,automation_generation,snapshot_json,policy_json,claim_json,claim_id,prepared_json,created_at,updated_at) VALUES('request','c','a','i/w/s/t','trigger','auth',1,'input','assignment','generation','{}','{\"mode\":\"before_submit\"}','{}','claim','private prepared result',1,1)",
	} {
		if _, err := d.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = d.db.Exec("INSERT INTO task_snapshots(connection_id,account_id,task_key,snapshot_json,auth_epoch,updated_at) VALUES('c','a','i/w/s/t',?,'auth',1)", string(snapshot)); err != nil {
		t.Fatal(err)
	}
}

func TestMCPWorkRemoteBindingAndResultProvenance(t *testing.T) {
	d := selfDeps(t)
	ctx := context.Background()
	mcpRemoteWork(t, d)
	task, err := d.tasks.Create(ctx, "", "alpha", "project agent", "node", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PANDA_TASK_ID", task.TaskID)
	args := map[string]any{"connection": "c", "key": "i/w/s/t"}
	if _, err = d.toolWorkShow(ctx, args); err == nil {
		t.Fatal("remote project matched by name")
	}
	for _, q := range []string{
		"INSERT INTO task_project_bindings VALUES('c','a','w','s','','alpha')",
		"INSERT INTO task_project_bindings VALUES('c','a','w','s','remote','beta')",
	} {
		if _, err := d.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = d.toolWorkShow(ctx, args); err == nil {
		t.Fatal("space fallback overrode explicit project binding")
	}
	if _, err = d.db.Exec("UPDATE task_project_bindings SET local_project='alpha' WHERE remote_project_id='remote'"); err != nil {
		t.Fatal(err)
	}
	if output, err := d.toolWorkShow(ctx, args); err != nil || !strings.Contains(output, "remote goal") {
		t.Fatalf("bound=%s err=%v", output, err)
	}
	// A current binding cannot expose outputs produced in a different project.
	prior, err := d.tasks.Create(ctx, "", "beta", "prior execution", "node", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.db.Exec("INSERT INTO execution_requests(request_id,connection_id,account_id,task_key,trigger_key,purpose,created_at,updated_at) VALUES('request','c','a','i/w/s/t','trigger','execute',1,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err = d.db.Exec("INSERT INTO execution_links(request_id,task_id,role,created_at) VALUES('request',?,'root',1)", prior.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err = d.toolWorkResult(ctx, map[string]any{"request_id": "request"}); err == nil {
		t.Fatal("rebound project exposed old private context")
	}
	t.Setenv("PANDA_TASK_ID", "")
	if output, err := d.toolWorkResult(ctx, map[string]any{"request_id": "request"}); err != nil || !strings.Contains(output, "private prepared result") {
		t.Fatalf("personal=%s err=%v", output, err)
	}
	if _, err = d.db.Exec("UPDATE task_connections SET spaces_json='[]'"); err != nil {
		t.Fatal(err)
	}
	if _, err = d.toolWorkResult(ctx, map[string]any{"request_id": "request"}); err == nil {
		t.Fatal("revoked task result readable")
	}
}

func TestMCPWorkResultPagingAndRequestGuards(t *testing.T) {
	d := selfDeps(t)
	ctx := context.Background()
	t.Setenv("PANDA_TASK_ID", "")
	mcpRemoteWork(t, d)
	long := strings.Repeat("成果🙂", 2000)
	if _, err := d.db.Exec("UPDATE task_flow_runs SET prepared_json=?", long); err != nil {
		t.Fatal(err)
	}
	first, err := d.toolWorkResult(ctx, map[string]any{"request_id": "request"})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Content    string
		NextOffset int
		SHA256     string
	}
	if err = json.Unmarshal([]byte(first), &page); err != nil || page.NextOffset == 0 || len(first) > maxSelfToolOut {
		t.Fatalf("page=%s err=%v", first, err)
	}
	second, err := d.toolWorkResult(ctx, map[string]any{"request_id": "request", "offset": float64(page.NextOffset), "sha256": page.SHA256})
	if err != nil || !json.Valid([]byte(second)) {
		t.Fatalf("second=%s err=%v", second, err)
	}
	if _, err = d.db.Exec("UPDATE task_flow_runs SET prepared_json='changed'"); err != nil {
		t.Fatal(err)
	}
	if _, err = d.toolWorkResult(ctx, map[string]any{"request_id": "request", "sha256": page.SHA256}); err == nil {
		t.Fatal("changed result reused page hash")
	}
	for _, arg := range []string{"authorized", "mode", "project", "approve"} {
		if _, err = d.toolWorkRequest(ctx, map[string]any{"connection": "c", "key": "i/w/s/t", "request_id": "019c0000-0000-7000-8000-000000000001", arg: true}); err == nil {
			t.Fatal("accepted authority argument", arg)
		}
	}
	if _, err = d.toolWorkRequest(ctx, map[string]any{"connection": "c", "key": "i/w/s/t", "request_id": "not-a-uuid"}); err == nil {
		t.Fatal("unstable request ID")
	}
	expected := map[string]bool{"panda_work_list": true, "panda_work_show": true, "panda_work_history": true, "panda_work_request": true, "panda_work_result": true}
	for _, tool := range selfTools(d) {
		if strings.HasPrefix(tool.Name, "panda_work_") {
			if !expected[tool.Name] {
				t.Fatal("unexpected business authority", tool.Name)
			}
			delete(expected, tool.Name)
		}
	}
	if len(expected) != 0 {
		t.Fatal(fmt.Sprint(expected))
	}
}

func TestMCPWorkListPagesVisibleTasks(t *testing.T) {
	d := selfDeps(t)
	ctx := context.Background()
	t.Setenv("PANDA_TASK_ID", "")
	for i := range 25 {
		if _, err := todos.NewStore(d.db).Create(ctx, todos.CreateInput{Title: fmt.Sprintf("goal-%02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, offset := range []int{0, 20} {
		output, err := d.toolWorkList(ctx, map[string]any{"offset": float64(offset)})
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Tasks             []json.RawMessage
			Total, NextOffset int
		}
		if err := json.Unmarshal([]byte(output), &page); err != nil {
			t.Fatal(err)
		}
		wantCount, wantNext := 20, 20
		if offset == 20 {
			wantCount, wantNext = 5, 0
		}
		if len(page.Tasks) != wantCount || page.Total != 25 || page.NextOffset != wantNext {
			t.Fatalf("page=%s", output)
		}
	}
	if _, err := d.toolWorkList(ctx, map[string]any{"offset": 0.5}); err == nil {
		t.Fatal("fractional offset accepted")
	}
}
