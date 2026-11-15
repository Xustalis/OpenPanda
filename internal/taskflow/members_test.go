// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestMembersAreBoundToCurrentAuthorization(t *testing.T) {
	for _, change := range []string{"account", "epoch", "space", "scope", "state"} {
		t.Run(change, func(t *testing.T) {
			s, p := newSyncTest(t)
			syncOK(t, s)
			members, err := s.Members(context.Background(), "c", "s")
			if err != nil || len(members) != 1 {
				t.Fatalf("members: %+v %v", members, err)
			}
			switch change {
			case "account":
				_, err = s.db.Exec(`UPDATE task_connections SET account_id='different'`)
			case "epoch":
				_, err = s.db.Exec(`UPDATE task_connections SET auth_epoch='different'`)
			case "state":
				_, err = s.db.Exec(`UPDATE task_connections SET state='disconnected'`)
			case "space":
				p.spaces = nil
				syncOK(t, s)
			case "scope":
				p.spaces[0].Scopes = []string{"tasks:read"}
				syncOK(t, s)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Members(context.Background(), "c", "s"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("stale directory exposed: %v", err)
			}
		})
	}
}
