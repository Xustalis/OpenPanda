// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda task artifact <id>` is the user-facing half of the artifact data
// plane: task output lands in this node's pool under a content hash (the
// panel's /api/tasks/{id}/artifact trio is the web twin). A screenshot a
// computer-use task produced, a plan stage's packed tree — anything the
// executor reported as an artifact — lists, previews, and extracts here.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

func runTaskArtifact(args []string) {
	fs := flag.NewFlagSet("task artifact", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	outDir := fs.String("out", "", "extract here (default ./<id>-artifact)")
	list := fs.Bool("list", false, "list archive entries without extracting")
	file := fs.String("file", "", "print one archive entry to stdout (e.g. screenshot.png)")
	fs.Parse(reorderFlags(fs, args))
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		fmt.Fprintln(os.Stderr, "usage: panda task artifact <task-id> [--out DIR | --list | --file PATH]")
		os.Exit(2)
	}
	loc := i18n.Detect()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	id = resolveTaskRef(store, id)
	t, err := store.Get(context.Background(), id)
	if err != nil {
		taskStoreFatal(err, id)
	}
	hash := t.OutputArtifact
	if hash == "" {
		fmt.Fprintln(os.Stderr, i18n.T(loc, "cli.task.artifact.none"))
		os.Exit(1)
	}

	pool := artifact.NewStore(cfg.Storage.ArtifactPath, cfg.Storage.ArtifactExtraPaths...)
	size, held := pool.Has(hash)
	if !held {
		// The row names content this node never received — the bytes stay on
		// the executor until a transfer moves them. Say where instead of a
		// bare "not found" that reads as data loss.
		fmt.Fprintln(os.Stderr, i18n.Tf(loc, "cli.task.artifact.not_held",
			"hash", hash, "executor", orDash(taskExecutor(store, t))))
		os.Exit(1)
	}

	switch {
	case *list:
		entries, err := pool.Entries(hash)
		if err != nil {
			fatal("read artifact", err)
		}
		if jsonOutput {
			emitJSON(map[string]any{"task_id": id, "hash": hash, "size": size, "entries": entries})
			return
		}
		fmt.Println(pal().Heading("artifact:") + " " + pal().Muted(hash))
		for _, e := range entries {
			fmt.Printf("  %10d  %s\n", e.Size, e.Path)
		}
	case *file != "":
		rc, _, err := pool.OpenEntry(hash, *file)
		if err != nil {
			fatal("open entry", err)
		}
		defer rc.Close()
		if _, err := io.Copy(os.Stdout, rc); err != nil {
			fatal("write stdout", err)
		}
	default:
		dst := *outDir
		if dst == "" {
			dst = "./" + id + "-artifact"
		}
		m, err := pool.Extract(hash, dst)
		if err != nil {
			fatal("extract artifact", err)
		}
		if jsonOutput {
			emitJSON(map[string]any{"task_id": id, "hash": hash, "path": dst, "files": len(m.Entries)})
			return
		}
		fmt.Println(i18n.Tf(loc, "cli.task.artifact.wrote", "path", dst, "n", fmt.Sprint(len(m.Entries))))
	}
}

// taskExecutor names the node that ran the task — DispatchTarget wins over
// the owner (a delegated result lives where it was made), "" when unknown.
func taskExecutor(store *core.TaskStore, t core.Task) string {
	target, err := store.DispatchTarget(context.Background(), t.TaskID)
	if err == nil && target != "" {
		return target
	}
	return t.OwnerNode
}
