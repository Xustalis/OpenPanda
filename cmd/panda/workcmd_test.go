// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/taskhub"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func TestWorkCLIMutationsShareProviderCAS(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	var out bytes.Buffer
	env := todoEnv{out: &out, errW: io.Discard, loc: i18n.English, jsonOut: true}
	if err := workMain(ctx, db, env, []string{"add", "--title", "business", "--request", "create"}); err != nil {
		t.Fatal(err)
	}
	var task taskhub.Task
	if err := json.Unmarshal(out.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	key := task.Snapshot.Key.String()
	if err := workMain(ctx, db, env, []string{"edit", key, "--title", "updated", "--request", "edit"}); !errors.Is(err, taskproviders.ErrPrecondition) {
		t.Fatal(err)
	}
	out.Reset()
	args := []string{"edit", key, "--if-match", task.Snapshot.TaskRevision, "--title", "updated", "--request", "edit"}
	if err := workMain(ctx, db, env, args); err != nil {
		t.Fatal(err)
	}
	first := out.String()
	out.Reset()
	if err := workMain(ctx, db, env, args); err != nil || out.String() != first {
		t.Fatalf("replay=%s err=%v", out.String(), err)
	}
	if err := workMain(ctx, db, env, []string{"edit", key, "--if-match", task.Snapshot.TaskRevision, "--title", "stale", "--request", "other"}); !errors.Is(err, taskproviders.ErrConflict) {
		t.Fatal(err)
	}
	if err := workMain(ctx, db, env, []string{"run", key, "--plan", "/no-such-plan.yaml"}); err == nil {
		t.Fatal("invalid Plan accepted")
	}
}
