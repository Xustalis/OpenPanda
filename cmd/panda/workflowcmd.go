// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/taskflow"
)

func isWorkflowVerb(verb string) bool {
	switch verb {
	case "binding", "policy", "runs", "result", "export", "research", "approve", "reject", "accept", "return", "pause":
		return true
	}
	return false
}

// Positional IDs have different domains: an action, a delivery or a request.
// Only research accepts a taskKey. No shortened IDs are resolved implicitly.
func workflowMain(ctx context.Context, s *taskflow.Service, env todoEnv, verb string, args []string) error {
	fs := flag.NewFlagSet("work "+verb, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	connection := fs.String("connection", "", "connection ID")
	space := fs.String("space", "", "space ID")
	key := fs.String("key", "", "task key filter")
	file := fs.String("file", "", "policy JSON file; - for stdin")
	fingerprint := fs.String("fingerprint", "", "exact final action fingerprint")
	revision := fs.Uint64("revision", 0, "exact delivery revision")
	reason := fs.String("reason", "", "decision reason")
	remoteProject := fs.String("remote-project", "", "remote project ID; empty binds the space")
	localProject := fs.String("local-project", "", "existing local project; empty removes the binding")
	id := ""
	if verb != "binding" && verb != "policy" && verb != "runs" {
		if len(args) == 0 {
			return errors.New(i18n.T(env.loc, "cli.work.usage"))
		}
		id, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New(i18n.T(env.loc, "cli.work.usage"))
	}
	switch verb {
	case "binding":
		if *connection == "" || *space == "" {
			return errors.New("--connection and --space required")
		}
		mutate := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "local-project" {
				mutate = true
			}
		})
		if mutate {
			if err := s.SetBinding(ctx, *connection, *space, taskflow.ProjectBinding{RemoteProject: *remoteProject, LocalProject: *localProject}); err != nil {
				return err
			}
		}
		b, err := s.Bindings(ctx, *connection, *space)
		if err != nil {
			return err
		}
		return writeTodoJSON(env, b)
	case "policy":
		if *connection == "" || *space == "" {
			return errors.New("--connection and --space required")
		}
		if *file != "" {
			var r io.Reader = os.Stdin
			if env.in != nil {
				r = env.in
			}
			if *file != "-" {
				f, err := os.Open(*file)
				if err != nil {
					return err
				}
				defer f.Close()
				r = f
			}
			var p taskflow.Policy
			raw, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
			if err != nil {
				return err
			}
			if len(raw) > 1<<20 {
				return errors.New("policy JSON exceeds 1 MiB")
			}
			raw = bytes.TrimSpace(raw)
			if len(raw) == 0 || raw[0] != '{' {
				return errors.New("expected one policy JSON object")
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&p); err != nil {
				return err
			}
			if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				return errors.New("expected one policy JSON object")
			}
			if err := s.SetPolicy(ctx, *connection, *space, p); err != nil {
				return err
			}
		}
		p, err := s.Policy(ctx, *connection, *space)
		if err != nil {
			return err
		}
		return writeTodoJSON(env, p)
	case "runs":
		x, err := s.Runs(ctx, *connection, *key)
		if err != nil {
			return err
		}
		return writeTodoJSON(env, x)
	case "research":
		x, err := s.Research(ctx, *connection, id)
		if err != nil {
			return err
		}
		return writeTodoJSON(env, x)
	case "result", "export":
		x, err := s.Work(ctx, id)
		if err != nil {
			return err
		}
		if verb == "export" {
			env.outf("%s", x.Export())
			return nil
		}
		return writeTodoJSON(env, x)
	case "approve", "reject":
		if *fingerprint == "" {
			return errors.New("--fingerprint required")
		}
		var err error
		if verb == "approve" {
			err = s.ApproveAction(ctx, id, *fingerprint)
		} else {
			err = s.RejectAction(ctx, id, *fingerprint)
		}
		if err != nil {
			return err
		}
	case "accept", "return":
		if *revision == 0 {
			return errors.New("--revision required")
		}
		if err := s.DecideDelivery(ctx, id, *revision, verb, *reason); err != nil {
			return err
		}
	case "pause":
		if err := s.PauseWork(ctx, id); err != nil {
			return err
		}
	}
	return writeTodoJSON(env, map[string]bool{"recorded": true})
}
