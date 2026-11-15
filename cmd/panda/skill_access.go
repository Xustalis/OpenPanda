// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

func runtimeSkillAccess(ctx context.Context, cfg *config.Config, tasks *core.TaskStore) (skills.Access, string, error) {
	a := skills.Access{}
	if cfg != nil {
		id := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())
		a.Devices = []string{id, cfg.Node.Name}
		if cardPath := cfg.EffectiveCardPath(); cardPath != "" {
			if card, err := ledger.LoadCard(cardPath); err == nil {
				a.Devices = append(a.Devices, card.Device)
			}
		}
		if i := strings.Index(id, "@vm"); i > 0 {
			a.Devices = append(a.Devices, id[:i])
		}
	}
	taskID := strings.TrimSpace(os.Getenv("PANDA_TASK_ID"))
	if taskID != "" {
		if tasks == nil {
			return a, taskID, fmt.Errorf("skills: ambient task store unavailable")
		}
		task, err := tasks.Get(ctx, taskID)
		if err != nil {
			return a, taskID, fmt.Errorf("skills: resolve task scope: %w", err)
		}
		a.Project = task.Project
	}
	return a, taskID, nil
}

// Both MCP and CLI attach the same evidence to the executing task. Reading a
// procedure is a use, not evidence that its instructions succeeded.
func recordSkillRead(ctx context.Context, tasks *core.TaskStore, store *skills.Store, taskID string, r *skills.ReadResult, via string) {
	if taskID == "" || tasks == nil {
		return
	}
	_ = tasks.RecordEvent(ctx, taskID, core.EvSkillLoad, map[string]any{
		"name": r.Name, "scope": r.Scope, "key": r.Key, "path": r.Path, "sha256": r.SHA256,
		"bytes": len(r.Content), "total_bytes": r.TotalBytes, "offset": r.Offset, "truncated": r.Truncated, "via": via,
	})
	// Updating SKILL.md counters before the last page would move subsequent
	// byte offsets while the caller is still reading the same procedure.
	if r.Path == "SKILL.md" && !r.Truncated {
		_ = store.RecordUse(r.Scope, r.Key, r.Name, false, time.Now())
	}
}

func skillReadTo(w io.Writer, store *skills.Store, access skills.Access, args []string) (*skills.ReadResult, error) {
	fs := flag.NewFlagSet("skill show", flag.ContinueOnError)
	fs.SetOutput(w)
	scope := fs.String("scope", "", "global|project|device")
	key := fs.String("key", "", "project/device scope key")
	resource := fs.String("path", "SKILL.md", "relative resource path")
	offset := fs.Int("offset", 0, "byte offset from next_offset")
	limit := fs.Int("limit", 6000, "maximum content bytes")
	hash := fs.String("sha256", "", "previous page hash; reject changed resources")
	if err := fs.Parse(reorderFlags(fs, args)); err != nil {
		return nil, err
	}
	if fs.NArg() != 1 {
		return nil, fmt.Errorf("usage: panda skill show <name> [--scope S --key K] [--path references/file.md] [--offset N] [--limit N]")
	}
	result, err := store.Read(access, skills.ReadRequest{Name: fs.Arg(0), Scope: skills.Scope(*scope), Key: *key, Path: *resource, Offset: *offset, Limit: *limit, SHA256: *hash})
	if err != nil {
		return nil, err
	}
	// Structured output retains paging/hash/resource metadata for every harness.
	err = json.NewEncoder(w).Encode(result)
	return result, err
}

func skillRead(cfg *config.Config, store *skills.Store, args []string, search bool) {
	ctx := context.Background()
	var tasks *core.TaskStore
	if os.Getenv("PANDA_TASK_ID") != "" {
		db, err := openStore(cfg)
		if err != nil {
			fatal("open task scope", err)
		}
		defer db.Close()
		tasks = core.NewSigningTaskStore(db, nil)
	}
	access, taskID, err := runtimeSkillAccess(ctx, cfg, tasks)
	if err != nil {
		fatal("skill scope", err)
	}
	if taskID == "" {
		access.All = true
		access.Inactive = !search
	}
	if search {
		results, err := store.Search(access, strings.Join(args, " "), 20)
		if err != nil {
			fatal("search skills", err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(results)
		return
	}
	result, err := skillReadTo(os.Stdout, store, access, args)
	if err != nil {
		fatal("read skill", err)
	}
	recordSkillRead(ctx, tasks, store, taskID, result, "cli")
}
