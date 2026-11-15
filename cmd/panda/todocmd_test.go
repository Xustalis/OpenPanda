// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/testdb"
	"github.com/Xustalis/OpenPanda/internal/todos"
)

func TestTodoCRUD(t *testing.T) {
	db := testdb.Open(t)
	out, errW := &bytes.Buffer{}, &bytes.Buffer{}
	env := todoEnv{out: out, errW: errW, loc: i18n.English}
	ctx := context.Background()

	if err := todoMain(ctx, db, env, []string{"add", "write the weekly report", "--priority", "high", "--due", "2026-11-02", "--tags", "work,writing"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out.String(), "todo #1 added") {
		t.Fatalf("add output = %q", out.String())
	}

	// The default board hides completed lanes; --all shows them.
	out.Reset()
	if err := todoMain(ctx, db, env, []string{"list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "write the weekly report") {
		t.Fatalf("list output = %q", out.String())
	}

	if err := todoMain(ctx, db, env, []string{"edit", "1", "--desc", "cover the sprint", "--priority", "low"}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := todoMain(ctx, db, env, []string{"done", "1"}); err != nil {
		t.Fatalf("done: %v", err)
	}
	out.Reset()
	if err := todoMain(ctx, db, env, []string{"list"}); err != nil {
		t.Fatalf("list after done: %v", err)
	}
	if strings.Contains(out.String(), "write the weekly report") {
		t.Fatalf("completed row shown without --all: %q", out.String())
	}
	out.Reset()
	if err := todoMain(ctx, db, env, []string{"list", "--all"}); err != nil {
		t.Fatalf("list --all: %v", err)
	}
	if !strings.Contains(out.String(), "write the weekly report") {
		t.Fatalf("--all dropped the completed row: %q", out.String())
	}

	// JSON is a machine channel: one complete row, not a formatted table.
	env.jsonOut = true
	out.Reset()
	if err := todoMain(ctx, db, env, []string{"show", "1"}); err != nil {
		t.Fatalf("show: %v", err)
	}
	var got todos.Todo
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("show JSON: %v\n%s", err, out.String())
	}
	if got.ID != 1 || got.Status != todos.StatusDone || got.Description != "cover the sprint" {
		t.Fatalf("show JSON = %+v", got)
	}
	env.jsonOut = false

	if err := todoMain(ctx, db, env, []string{"rm", "1"}); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if err := todoMain(ctx, db, env, []string{"show", "1"}); err == nil {
		t.Fatal("showing a deleted todo must fail")
	}
}

// TestTodoValidation: bad ids, lanes, priorities and due dates are refused
// before they can reach the store.
func TestTodoValidation(t *testing.T) {
	db := testdb.Open(t)
	env := todoEnv{out: &bytes.Buffer{}, errW: &bytes.Buffer{}, loc: i18n.English}
	ctx := context.Background()

	for _, args := range [][]string{
		{"add", ""},
		{"add", "x", "--status", "someday"},
		{"add", "x", "--priority", "urgent"},
		{"add", "x", "--due", "tomorrow"},
		{"edit", "abc", "--title", "x"},
		{"edit", "1"},
		{"nonsense"},
	} {
		if err := todoMain(ctx, db, env, args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

// TestTodoStartWithoutEngine: 开始 needs a model; the refusal names the way
// out instead of creating a task nobody can run.
func TestTodoStartWithoutEngine(t *testing.T) {
	db := testdb.Open(t)
	out := &bytes.Buffer{}
	env := todoEnv{out: out, errW: &bytes.Buffer{}, loc: i18n.English}
	ctx := context.Background()
	if err := todoMain(ctx, db, env, []string{"add", "ship the release"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	err := todoMain(ctx, db, env, []string{"start", "1"})
	if err == nil {
		t.Fatal("start without a model must fail")
	}
	if !strings.Contains(err.Error(), "configured model") {
		t.Fatalf("refusal = %q, want the model hint", err.Error())
	}
}

// TestTodoHelpFlags: help is answered wherever the user puts it. A help flag
// used to reach a subcommand's FlagSet — created with io.Discard — which came
// back as a bare `flag: help requested`: `panda todo --help` exited 2 instead
// of printing usage.
func TestTodoHelpFlags(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()

	for _, args := range [][]string{
		{"--help"},
		{"-h"},
		{"help"},
		{"list", "--help"},
		{"add", "anything", "--help"},
	} {
		out := &bytes.Buffer{}
		env := todoEnv{out: out, errW: &bytes.Buffer{}, loc: i18n.English}
		if err := todoMain(ctx, db, env, args); err != nil {
			t.Errorf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "usage: panda todo") {
			t.Errorf("%v output = %q, want usage", args, out.String())
		}
	}

	// A title that happens to read "help" is still a title, not a help flag.
	out := &bytes.Buffer{}
	env := todoEnv{out: out, errW: &bytes.Buffer{}, loc: i18n.English}
	if err := todoMain(ctx, db, env, []string{"add", "help"}); err != nil {
		t.Fatalf("add help: %v", err)
	}
	if strings.Contains(out.String(), "usage: panda todo") {
		t.Fatalf("`add help` printed usage instead of adding: %q", out.String())
	}
}

// TestTodoStartJSONCarriesWarning: the dead-queue advisory has to reach a
// machine reader. state alone says the row exists, not that any process will
// ever drain it — the panel's board path returns the same warning.
func TestTodoStartJSONCarriesWarning(t *testing.T) {
	payload := todoStartJSON(
		core.Task{TaskID: "task-1", State: "queued"}, "sess-1", "no consumer will drain this store",
		todos.Todo{ID: 7}, nil,
	)
	if payload["warning"] != "no consumer will drain this store" {
		t.Fatalf("warning = %v, want the advisory", payload["warning"])
	}
	if _, ok := payload["todo"]; !ok {
		t.Fatal("payload dropped the linked to-do")
	}
	if _, ok := payload["link_error"]; ok {
		t.Fatal("successful link reported link_error")
	}

	linked := todoStartJSON(core.Task{TaskID: "task-2", State: "queued"}, "", "", todos.Todo{}, errors.New("boom"))
	if linked["link_error"] != "boom" {
		t.Fatalf("link_error = %v", linked["link_error"])
	}
	if _, ok := linked["todo"]; ok {
		t.Fatal("failed link reported a to-do")
	}
}
