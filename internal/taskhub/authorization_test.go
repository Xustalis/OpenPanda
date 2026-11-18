// SPDX-License-Identifier: AGPL-3.0-or-later

package taskhub

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func TestBusinessReadsRequireCurrentSpaceAndAccount(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE task_connections SET spaces_json='[]'`,
		`UPDATE task_connections SET scopes_json='[]'`,
		`UPDATE task_connections SET account_id='other'`,
		`UPDATE task_connections SET auth_epoch='new'`,
		`UPDATE task_connections SET instance_id='other'`,
		`UPDATE task_connections SET state='revoked'`,
		`UPDATE task_connections SET spaces_json=json_set(spaces_json,'$[0].workspaceId','other')`,
		`UPDATE task_connections SET spaces_json=json_set(spaces_json,'$[0].scopes',json('[]'))`,
		`UPDATE task_snapshots SET snapshot_json=json_set(snapshot_json,'$.permissions.read',json('false'))`,
		`UPDATE task_snapshots SET snapshot_json=json_set(snapshot_json,'$.permissions.authEpoch','stale')`,
	} {
		t.Run(mutation, func(t *testing.T) {
			db := testdb.Open(t)
			s := New(db)
			ctx := context.Background()
			snapshot, _ := json.Marshal(taskproviders.Snapshot{Key: taskproviders.Key{InstanceID: "i", WorkspaceID: "w", SpaceID: "s", TaskID: "t"}, Title: "Private business goal", Permissions: taskproviders.Permissions{AuthEpoch: "auth", Read: true}})
			if _, err := db.Exec(`INSERT INTO task_connections(connection_id,provider,account_id,instance_id,auth_epoch,state,spaces_json,scopes_json,updated_at) VALUES('c','tsync','a','i','auth','connected','[{"spaceId":"s","workspaceId":"w","scopes":["tasks:read"]}]','["tasks:read"]',1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO task_snapshots(connection_id,account_id,task_key,snapshot_json,auth_epoch,updated_at) VALUES('c','a','i/w/s/t',?,'auth',1)`, string(snapshot)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO execution_requests(request_id,connection_id,account_id,task_key,trigger_key,purpose,created_at,updated_at) VALUES('request','c','a','i/w/s/t','trigger','manual',1,1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO execution_links(request_id,task_id,role,created_at) VALUES('request','execution','primary',1)`); err != nil {
				t.Fatal(err)
			}
			if list, err := s.List(ctx, "c"); err != nil || len(list) != 1 {
				t.Fatalf("initial=%+v %v", list, err)
			}
			if sources, err := s.Sources(ctx, []string{"execution"}); err != nil || len(sources) != 1 {
				t.Fatalf("initial sources=%+v %v", sources, err)
			}
			if _, err := db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if list, err := s.List(ctx, "c"); err != nil || len(list) != 0 {
				t.Fatalf("revoked=%+v %v", list, err)
			}
			if _, err := s.Get(ctx, "c", "i/w/s/t"); err == nil {
				t.Fatal("read stale task")
			}
			if _, err := s.History(ctx, "c", "i/w/s/t"); err == nil {
				t.Fatal("read stale history")
			}
			if sources, err := s.Sources(ctx, []string{"execution"}); err != nil || len(sources) != 0 {
				t.Fatalf("revoked sources=%+v %v", sources, err)
			}
		})
	}
}
