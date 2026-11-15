// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/taskflow"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func TestWorkflowCLIExactDecisionArguments(t *testing.T) {
	s := taskflow.New(testdb.Open(t), "")
	env := todoEnv{out: io.Discard, errW: io.Discard, loc: i18n.English}
	for _, tc := range []struct {
		verb string
		args []string
		want string
	}{
		{"approve", []string{"action"}, "--fingerprint required"},
		{"reject", []string{"action"}, "--fingerprint required"},
		{"accept", []string{"delivery"}, "--revision required"},
		{"return", []string{"delivery", "--revision", "0"}, "--revision required"},
		{"policy", nil, "--connection and --space required"},
		{"binding", nil, "--connection and --space required"},
	} {
		if err := workflowMain(context.Background(), s, env, tc.verb, tc.args); err == nil || err.Error() != tc.want {
			t.Fatalf("%s: %v", tc.verb, err)
		}
	}
}

func TestWorkflowCLIBindingListSetRemove(t *testing.T) {
	db := testdb.Open(t)
	if _, err := db.Exec(`INSERT INTO task_connections(connection_id,provider,account_id,instance_id,auth_epoch,state,spaces_json,scopes_json,updated_at) VALUES('c','tsync','a','i','auth','connected','[{"spaceId":"s","workspaceId":"w","scopes":["tasks:read"]}]','["tasks:read"]',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.NewStore(db).Create("local-context", "", ""); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	env := todoEnv{out: &out, errW: io.Discard, loc: i18n.English}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "[]"},
		{[]string{"--local-project", "local-context"}, `"localProject": "local-context"`},
		{nil, `"localProject": "local-context"`},
		{[]string{"--local-project="}, "[]"},
	} {
		out.Reset()
		args := append([]string{"--connection", "c", "--space", "s"}, tc.args...)
		if err := workflowMain(context.Background(), taskflow.New(db, ""), env, "binding", args); err != nil || !strings.Contains(out.String(), tc.want) {
			t.Fatalf("%v: %s %v", args, out.String(), err)
		}
	}
}

func TestWorkflowCLIRejectsMalformedAndOversizePolicy(t *testing.T) {
	s := taskflow.New(testdb.Open(t), "")
	for _, raw := range []string{"null", "[]", "{} {}", `{"shell":true}`, "{}" + strings.Repeat(" ", 1<<20)} {
		env := todoEnv{in: strings.NewReader(raw), out: io.Discard, errW: io.Discard, loc: i18n.English}
		err := workflowMain(context.Background(), s, env, "policy", []string{"--connection", "c", "--space", "s", "--file", "-"})
		if err == nil || strings.Contains(err.Error(), "no rows") {
			t.Fatalf("invalid input reached policy mutation: %v", err)
		}
	}
}

func TestWorkflowCLIAndREPLShareResultDiscovery(t *testing.T) {
	db := testdb.Open(t)
	var out bytes.Buffer
	env := todoEnv{out: &out, errW: io.Discard, loc: i18n.English}
	if err := workMain(context.Background(), db, env, []string{"runs"}); err != nil || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("runs=%q err=%v", out.String(), err)
	}
	out.Reset()
	r := &repl{db: db, loc: i18n.English, commandOut: &out, commandErrOut: io.Discard}
	r.cmdWork("runs")
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("REPL=%q", out.String())
	}
}
