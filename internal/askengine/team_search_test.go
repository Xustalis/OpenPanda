// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func TestEmbeddedTeamSearch(t *testing.T) {
	db := testdb.Open(t)
	e := &Engine{db: db}
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO task_connections(connection_id,provider,account_id,instance_id,auth_epoch,state,spaces_json,scopes_json,updated_at) VALUES('c','tsync','a','i','auth','connected','[{"spaceId":"s","workspaceId":"w","scopes":["tasks:read"]}]','["tasks:read"]',1)`,
		`INSERT INTO task_snapshots(connection_id,account_id,task_key,snapshot_json,auth_epoch,updated_at) VALUES('c','a','i/w/s/t','{"title":"Review Ada","spaceName":"设计","status":"open","projectId":"p","assigneeAccountIds":["a"],"permissions":{"read":true,"authEpoch":"auth"},"key":{"instanceId":"i","workspaceId":"w","spaceId":"s"}}','auth',1)`,
		`INSERT INTO task_project_bindings VALUES('c','a','w','s','','other'),('c','a','w','s','p','design')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	reg := entry.NewRegistry()
	e.registerTeamSearch(reg, AskScope{}, "")
	if _, ok := reg.Lookup("tsync_task_search"); ok {
		t.Fatal("non-owner got team search")
	}
	check := func(project, query string, want bool) {
		t.Helper()
		e.registerTeamSearch(reg, AskScope{PersonalMemory: true}, project)
		tool, _ := reg.Lookup("tsync_task_search")
		out, err := tool.Run(ctx, map[string]any{"mine": true, "query": query})
		if err != nil || strings.Contains(out, "#/work/tasks/i%2Fw%2Fs%2Ft?connection=c") != want {
			t.Fatalf("project=%q query=%q result=%s err=%v", project, query, out, err)
		}
	}
	check("", "ada", true)
	check("design", "设计", true)
	check("other", "ada", false)
	check("", "absent", false)
	if _, err := db.Exec("UPDATE task_connections SET state='reconnect'"); err != nil {
		t.Fatal(err)
	}
	check("", "ada", false)
}
