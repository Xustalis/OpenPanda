// SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/taskflow"
)

func runConnection(args []string) {
	loc := i18n.Detect()
	if todoHelpRequested(args) {
		fmt.Println(i18n.T(loc, "cli.connection.usage"))
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
	env := todoEnv{out: os.Stdout, errW: os.Stderr, loc: loc, jsonOut: jsonOutput, cfg: cfg}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err = connectionMain(ctx, taskflow.New(db, cfg.Storage.DBPath), env, args); err != nil {
		fail(2, "panda: "+err.Error())
	}
}
func connectionMain(ctx context.Context, s *taskflow.Service, env todoEnv, args []string) error {
	verb := "list"
	if len(args) > 0 {
		verb, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("connection "+verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "service root URL")
	id := fs.String("id", "", "connection ID")
	name := fs.String("name", "Panda", "device name")
	scopes := fs.String("scopes", "tasks:read,members:read", "comma-separated scopes")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		*id, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New(i18n.T(env.loc, "cli.connection.usage"))
	}
	switch verb {
	case "list":
		v, e := s.List(ctx)
		if e != nil {
			return e
		}
		return writeTodoJSON(env, v)
	case "connect":
		v, e := s.Connect(ctx, taskflow.ConnectOptions{ID: *id, Root: *root, Name: *name, Scopes: strings.FieldsFunc(*scopes, func(r rune) bool { return r == ',' || r == ' ' })})
		if e != nil {
			return e
		}
		return writeTodoJSON(env, v)
	case "status":
		v, e := s.Get(ctx, *id)
		if e != nil {
			return e
		}
		return writeTodoJSON(env, v)
	case "sync":
		if err := s.RequestSync(ctx, *id); err != nil {
			return err
		}
		err := s.SyncNow(ctx, *id)
		if errors.Is(err, taskflow.ErrRuntimeHeld) {
			return nil
		}
		return err
	case "disconnect":
		return s.Disconnect(ctx, *id)
	default:
		return errors.New(i18n.T(env.loc, "cli.connection.usage"))
	}
}
