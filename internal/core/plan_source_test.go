// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/projects"
)

func TestPlanSourceSnapshotSurvivesRemoteHandoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	origin := newCoreWithNative(t, "source-origin", ledger.NativeAbility{
		ID: "dev:first", Command: "sh", Args: []string{"-c", "test -f app/main.go && printf first > first.txt"}, Tier: 1,
	})
	peer := newCoreWithNative(t, "source-peer", ledger.NativeAbility{
		ID: "dev:second", Command: "sh", Args: []string{"-c", "test -f app/main.go && test -f first.txt && printf second > second.txt"}, Tier: 1,
	})
	for _, c := range []*Core{origin, peer} {
		withArtifactPool(t, c)
		c.SetWorkDir(t.TempDir())
		c.SetProjectStores(projects.NewStore(c.db), t.TempDir())
		if err := c.Register(ctx); err != nil {
			t.Fatal(err)
		}
	}
	startListen(t, ctx, origin, "127.0.0.1:0")
	addr := startListen(t, ctx, peer, "127.0.0.1:0")
	if err := origin.DialPeer(ctx, addr); err != nil {
		t.Fatal(err)
	}
	waitPeer(t, origin, "source-peer")
	waitPeer(t, peer, "source-origin")
	origin.StartQueueScheduler(ctx)
	root := writeRepoTree(t)
	q := DefaultQueueSpec()
	q.WorkDir = root
	q.Project = "scoped-source"
	memoryDir := filepath.Join(origin.projectsRoot, q.Project)
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORY.md"), []byte("PROJECT-FACT: preserve stage source inputs"), 0o644); err != nil {
		t.Fatal(err)
	}
	planID, err := origin.StartPlan(ctx, plan.Plan{Goal: "check actual source", Stages: []plan.Stage{
		{ID: "first", Requires: []string{"dev:first"}, Intent: "read source and produce first", SpecJSON: `{"permission_mode":"auto","scope":"app/**"}`},
		{ID: "second", Requires: []string{"dev:second"}, Needs: []string{"first"}, Intent: "read source and predecessor", SpecJSON: `{"permission_mode":"auto","scope":"app/**"}`},
	}}, q)
	if err != nil {
		t.Fatal(err)
	}
	rows := waitPlanDone(t, ctx, origin, planID, 2)
	for _, row := range rows {
		if row.Project != q.Project || row.SpecJSON == "" || row.WorkDir != "" {
			t.Fatalf("stage lost source contract: %+v", row)
		}
		if len(row.Inputs) == 0 || row.Inputs[0].Stage != worktreeArtifactStage {
			t.Fatalf("source input missing: %+v", row)
		}
		if row.StageID == "second" {
			if len(row.Inputs) != 2 {
				t.Fatalf("source/dependency not both kept: %+v", row.Inputs)
			}
			remote, err := peer.store.Get(ctx, row.TaskID)
			if err != nil || remote.Project != q.Project || remote.SpecJSON != row.SpecJSON || remote.WorkDir != "" {
				t.Fatalf("remote scope=%+v err=%v", remote, err)
			}
			target, _ := origin.store.DispatchTarget(ctx, row.TaskID)
			if target != "source-peer" {
				t.Fatalf("target=%q", target)
			}
		}
	}
	if body, err := os.ReadFile(filepath.Join(peer.projectsRoot, q.Project, "MEMORY.md")); err != nil || string(body) != "PROJECT-FACT: preserve stage source inputs" {
		t.Fatalf("remote plan lost project memory: %q err=%v", body, err)
	}
	for _, name := range []string{"first.txt", "second.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("stage wrote into original source: %s (%v)", name, err)
		}
	}
}

func TestPlanSourceFailsClosedWithoutArtifactPool(t *testing.T) {
	c := newCoreWithNative(t, "no-pool", ledger.NativeAbility{ID: "dev:check", Command: "true", Tier: 1})
	q := DefaultQueueSpec()
	q.WorkDir = writeRepoTree(t)
	id, err := c.StartPlan(context.Background(), plan.Plan{Goal: "inspect", Stages: []plan.Stage{{ID: "inspect", Intent: "inspect source"}}}, q)
	if err == nil || id != "" {
		t.Fatalf("missing source pool accepted: id=%s err=%v", id, err)
	}
	var count int
	if err := c.store.db.QueryRow("SELECT count(*) FROM tasks").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial plan rows=%d err=%v", count, err)
	}
}
