// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/taskflow"
	"github.com/Xustalis/OpenPanda/internal/taskhub"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/todos"
	"github.com/Xustalis/OpenPanda/internal/util"
)

func runWork(args []string) {
	loc := i18n.Detect()
	if todoHelpRequested(args) {
		fmt.Println(i18n.T(loc, "cli.work.usage"))
		return
	}
	cfg, err := config.Load(cliConfigPath)
	if err != nil {
		fatal("load config", err)
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()
	env := todoEnv{out: os.Stdout, errW: os.Stderr, loc: loc, jsonOut: jsonOutput, cfg: cfg, configPath: cliConfigPath, cardPath: cardFlagDefault(), mcp: cliMCP}
	if err := workMain(context.Background(), db, env, args); err != nil {
		fail(2, "panda: "+err.Error())
	}
}

// Business commands share the HTTP service and provider mutation boundaries.
// Local work run requests a new execution; remote work run requests the
// current canonical generation. todo start keeps its compatibility execution.
func workMain(ctx context.Context, db *sql.DB, env todoEnv, args []string) error {
	if todoHelpRequested(args) {
		env.outln(i18n.T(env.loc, "cli.work.usage"))
		return nil
	}
	verb := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		verb, args = args[0], args[1:]
	}
	if isWorkflowVerb(verb) {
		path := ""
		if env.cfg != nil {
			path = env.cfg.Storage.DBPath
		}
		return workflowMain(ctx, taskflow.New(db, path), env, verb, args)
	}
	fs := flag.NewFlagSet("work "+verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	connection := fs.String("connection", "local", "task connection")
	space := fs.String("space", "", "remote space ID")
	remoteProject := fs.String("remote-project", "", "remote project ID")
	remotePriority := fs.Int("remote-priority", 2, "source priority 0..4")
	startAt := fs.String("start-at", "", "typed time JSON")
	dueAt := fs.String("due-at", "", "typed time JSON")
	requestID := fs.String("request", "", "idempotency key")
	revision := fs.String("if-match", "", "task revision")
	title := fs.String("title", "", "title")
	description := fs.String("description", "", "description")
	project := fs.String("project", "", "local project")
	priority := fs.String("priority", "", "low, normal or high")
	status := fs.String("status", "", "business status")
	planFile := fs.String("plan", "", "YAML Plan file")
	mode := fs.String("approval-mode", "before_submit", "before_submit or after_execution")
	var key string
	switch verb {
	case "show", "edit", "run", "history":
		if len(args) == 0 {
			return errors.New(i18n.T(env.loc, "cli.work.usage"))
		}
		key, args = args[0], args[1:]
		if _, err := taskproviders.ParseKey(key); err != nil {
			return err
		}
	case "list", "add":
	default:
		return errors.New(i18n.T(env.loc, "cli.work.usage"))
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New(i18n.T(env.loc, "cli.work.usage"))
	}
	hub := taskhub.New(db)
	switch verb {
	case "list":
		x, err := hub.List(ctx, *connection)
		if err != nil {
			return err
		}
		if env.jsonOut {
			return writeTodoJSON(env, x)
		}
		for _, task := range x {
			env.outf("%s\t%s\t%s\n", task.Snapshot.Key.String(), task.Snapshot.Status, task.Snapshot.Title)
		}
		return nil
	case "show":
		x, err := hub.Get(ctx, *connection, key)
		if err != nil {
			return err
		}
		return writeTodoJSON(env, x)
	case "history":
		x, err := hub.History(ctx, *connection, key)
		if err != nil {
			return err
		}
		return writeTodoJSON(env, x)
	}
	if *requestID == "" {
		var err error
		*requestID, err = util.UUIDv7()
		if err != nil {
			return err
		}
	}
	if *connection != "local" {
		path := ""
		if env.cfg != nil {
			path = env.cfg.Storage.DBPath
		}
		service := taskflow.New(db, path)
		if verb == "run" {
			if *planFile != "" {
				return taskproviders.ErrUnsupported
			}
			k, err := taskproviders.ParseKey(key)
			if err != nil {
				return err
			}
			result, err := service.RequestExecution(ctx, *connection, k, *requestID)
			if err != nil {
				return err
			}
			return writeTodoJSON(env, result)
		}
		if *priority != "" || *project != "" {
			return errors.New("remote tasks use --remote-priority and --remote-project")
		}
		if verb == "add" {
			var start, due *taskproviders.TypedTime
			if *startAt != "" {
				if err := json.Unmarshal([]byte(*startAt), &start); err != nil {
					return err
				}
			}
			if *dueAt != "" {
				if err := json.Unmarshal([]byte(*dueAt), &due); err != nil {
					return err
				}
			}
			result, err := service.CreateTask(ctx, *connection, *space, taskproviders.Snapshot{Title: *title, Description: *description, Status: *status, Priority: *remotePriority, ProjectID: *remoteProject, StartAt: start, DueAt: due}, *requestID)
			if err != nil {
				return err
			}
			return writeTodoJSON(env, taskhub.Task{ConnectionID: *connection, Snapshot: result})
		}
		patch := map[string]any{}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "title":
				patch["title"] = *title
			case "description":
				patch["description"] = *description
			case "status":
				patch["status"] = *status
			case "remote-priority":
				patch["priority"] = *remotePriority
			case "remote-project":
				patch["projectId"] = *remoteProject
			case "start-at":
				patch["startAt"] = json.RawMessage(*startAt)
			case "due-at":
				patch["dueAt"] = json.RawMessage(*dueAt)
			}
		})
		raw, err := json.Marshal(patch)
		if err != nil {
			return err
		}
		k, err := taskproviders.ParseKey(key)
		if err != nil {
			return err
		}
		result, err := service.PatchTask(ctx, *connection, k, *revision, raw, *requestID)
		if err != nil {
			return err
		}
		return writeTodoJSON(env, taskhub.Task{ConnectionID: *connection, Snapshot: result})
	}
	provider := taskproviders.Local{Store: hub.Todos}
	if verb == "add" {
		x, err := provider.CreateLocal(ctx, todos.CreateInput{Title: *title, Description: *description, Project: *project, Priority: *priority, Status: *status}, *requestID)
		if err != nil {
			return err
		}
		return writeTodoJSON(env, taskhub.Task{ConnectionID: "local", AccountID: "local", Snapshot: taskproviders.LocalSnapshot(x), Local: &x})
	}
	k, err := taskproviders.ParseKey(key)
	if err != nil {
		return err
	}
	id, err := taskproviders.LocalID(k)
	if err != nil {
		return taskproviders.ErrUnsupported
	}
	if verb == "edit" {
		var p todos.Patch
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "title":
				p.Title = title
			case "description":
				p.Description = description
			case "project":
				p.Project = project
			case "priority":
				p.Priority = priority
			case "status":
				p.Status = status
			}
		})
		x, err := provider.PatchLocal(ctx, id, *revision, p, *requestID)
		if err != nil {
			return err
		}
		return writeTodoJSON(env, taskhub.Task{ConnectionID: "local", AccountID: "local", Snapshot: taskproviders.LocalSnapshot(x), Local: &x})
	}
	opts := taskhub.RunOptions{RequestID: *requestID, Purpose: "rerun", ApprovalMode: *mode}
	if *planFile != "" {
		raw, err := os.ReadFile(*planFile)
		if err != nil {
			return err
		}
		p, err := plan.Parse(raw)
		if err != nil {
			return err
		}
		opts.Plan = &p
	}
	eng, closeEng, err := env.engineFor(ctx)
	if err != nil {
		return err
	}
	if closeEng {
		defer eng.Close()
	}
	x, err := hub.StartLocal(ctx, eng, id, opts)
	if err != nil {
		return err
	}
	return writeTodoJSON(env, x)
}
