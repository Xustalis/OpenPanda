// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/trust"
)

type causalFixture struct {
	t    *testing.T
	c    *Core
	base bus.ArtifactRef
}

func TestCausalPermissionsSurviveExtractionMask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	f := newCausalFixture(t)
	source := t.TempDir()
	path := filepath.Join(source, "shared.txt")
	writeFile(t, path, "same bytes")
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	m, err := f.c.artifacts.PackDir(source)
	if err != nil {
		t.Fatal(err)
	}
	base := bus.ArtifactRef{Hash: m.Hash, Source: f.c.nodeID}
	unchanged := f.stage("unchanged-mode", []bus.ArtifactRef{base}, func(string) {})
	stage := f.stage("restricted", []bus.ArtifactRef{base}, func(dir string) {
		if err := os.Chmod(filepath.Join(dir, "shared.txt"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	dir := t.TempDir()
	if err := f.hydrate([]bus.ArtifactRef{base, stage}, dir); err != nil {
		t.Fatalf("umask became a synthetic conflict: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "shared.txt")); got != "same bytes" {
		t.Fatal(got)
	}
	for _, test := range []struct {
		ref    bus.ArtifactRef
		mode   uint32
		source bool
	}{{unchanged, 0666, true}, {stage, 0600, false}} {
		dir := t.TempDir()
		m, err := f.c.artifacts.Extract(test.ref.Hash, dir)
		if err != nil {
			t.Fatal(err)
		}
		state, _, err := readCausalTree(dir, test.ref.Hash, "causal-plan", m.Entries)
		if err != nil {
			t.Fatal(err)
		}
		rev := state.Files["shared.txt"][0]
		if rev.Mode != test.mode || (rev.Writer == "source:"+base.Hash) != test.source {
			t.Fatalf("permission intent lost or synthetic writer introduced: %+v", rev)
		}
	}
}

func newCausalFixture(t *testing.T) causalFixture {
	t.Helper()
	c := newCore(t, "causal-node")
	withArtifactPool(t, c)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "a0")
	writeFile(t, filepath.Join(dir, "b.txt"), "b0")
	m, err := c.artifacts.PackDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return causalFixture{t: t, c: c, base: bus.ArtifactRef{Stage: worktreeArtifactStage, Hash: m.Hash, Source: c.nodeID}}
}
func (f causalFixture) stage(name string, inputs []bus.ArtifactRef, edit func(string)) bus.ArtifactRef {
	f.t.Helper()
	task, _, _, err := f.c.createTask(context.Background(), TaskInput{Title: name})
	if err != nil {
		f.t.Fatal(err)
	}
	task.PlanID, task.StageID, task.Inputs = "causal-plan", name, inputs
	dir := f.t.TempDir()
	if err := f.c.fetchStageInputs(context.Background(), task, dir); err != nil {
		f.t.Fatal(err)
	}
	edit(dir)
	h, err := f.c.packStageOutput(context.Background(), task, dir)
	if err != nil {
		f.t.Fatal(err)
	}
	return bus.ArtifactRef{Stage: name, Hash: h, Source: f.c.nodeID}
}
func (f causalFixture) hydrate(inputs []bus.ArtifactRef, dir string) error {
	return f.c.fetchStageInputs(context.Background(), Task{PlanID: "causal-plan", StageID: "join", Inputs: inputs}, dir)
}

func TestCausalMultiLevelJoin(t *testing.T) {
	f := newCausalFixture(t)
	left := f.stage("left", []bus.ArtifactRef{f.base}, func(d string) { writeFile(t, filepath.Join(d, "a.txt"), "a1") })
	right := f.stage("right", []bus.ArtifactRef{f.base}, func(d string) { writeFile(t, filepath.Join(d, "b.txt"), "b1") })
	child := f.stage("descendant", []bus.ArtifactRef{f.base, left}, func(d string) { writeFile(t, filepath.Join(d, "a.txt"), "a2") })
	joined := f.stage("joined", []bus.ArtifactRef{f.base, left, child, right}, func(d string) {
		if readFile(t, filepath.Join(d, "a.txt")) != "a2" || readFile(t, filepath.Join(d, "b.txt")) != "b1" {
			t.Fatal("ancestor reset a descendant")
		}
		if err := os.Remove(filepath.Join(d, "b.txt")); err != nil {
			t.Fatal(err)
		}
	})
	for _, inputs := range [][]bus.ArtifactRef{{f.base, joined, right, left}, {left, right, joined, f.base}} {
		dir := t.TempDir()
		if err := f.hydrate(inputs, dir); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(dir, "a.txt")); got != "a2" {
			t.Fatal(got)
		}
		if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
			t.Fatal("deletion lost", err)
		}
	}
}

func TestCausalConcurrentWriters(t *testing.T) {
	for _, kind := range []string{"different", "same", "delete-modify", "namespace"} {
		t.Run(kind, func(t *testing.T) {
			f := newCausalFixture(t)
			left := f.stage("left", []bus.ArtifactRef{f.base}, func(d string) {
				if kind == "delete-modify" {
					if err := os.Remove(filepath.Join(d, "a.txt")); err != nil {
						t.Fatal(err)
					}
				} else if kind == "namespace" {
					writeFile(t, filepath.Join(d, "new"), "file")
				} else {
					writeFile(t, filepath.Join(d, "a.txt"), "left")
				}
			})
			right := f.stage("right", []bus.ArtifactRef{f.base}, func(d string) {
				content := "right"
				if kind == "same" {
					content = "left"
				}
				if kind == "namespace" {
					writeFile(t, filepath.Join(d, "new", "file"), "child")
				} else {
					writeFile(t, filepath.Join(d, "a.txt"), content)
				}
			})
			for _, refs := range [][]bus.ArtifactRef{{f.base, left, right}, {right, left, f.base}} {
				dir := t.TempDir()
				writeFile(t, filepath.Join(dir, "sentinel"), "unchanged")
				err := f.hydrate(refs, dir)
				if kind == "same" {
					if err != nil {
						t.Fatal(err)
					}
					if got := readFile(t, filepath.Join(dir, "a.txt")); got != "left" {
						t.Fatal(got)
					}
				} else {
					var blocked *artifactMergeError
					if !errors.As(err, &blocked) {
						t.Fatalf("expected blocked conflict, got %v", err)
					}
					if got := readFile(t, filepath.Join(dir, "sentinel")); got != "unchanged" {
						t.Fatal("partial overwrite")
					}
				}
			}
		})
	}
}

func TestCausalMetadataMismatchAndLegacy(t *testing.T) {
	f := newCausalFixture(t)
	left := f.stage("left", []bus.ArtifactRef{f.base}, func(d string) { writeFile(t, filepath.Join(d, "a.txt"), "a1") })
	unpack := t.TempDir()
	if _, err := f.c.artifacts.Extract(left.Hash, unpack); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(unpack, "a.txt"), "tampered")
	bad, err := f.c.artifacts.PackDir(unpack)
	if err != nil {
		t.Fatal(err)
	}
	err = f.hydrate([]bus.ArtifactRef{{Hash: bad.Hash, Source: f.c.nodeID}}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatal(err)
	}
	raw := t.TempDir()
	writeFile(t, filepath.Join(raw, "legacy.txt"), "legacy")
	m, err := f.c.artifacts.PackDir(raw)
	if err != nil {
		t.Fatal(err)
	}
	legacy := bus.ArtifactRef{Hash: m.Hash, Source: f.c.nodeID}
	if err := f.hydrate([]bus.ArtifactRef{legacy}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := f.hydrate([]bus.ArtifactRef{f.base, legacy}, t.TempDir()); err == nil {
		t.Fatal("unsafe legacy overlay accepted")
	}
	if err := f.hydrate([]bus.ArtifactRef{left, legacy}, t.TempDir()); err == nil {
		t.Fatal("unrelated legacy and causal baselines accepted")
	}
	safe := f.stage("reserved", []bus.ArtifactRef{f.base}, func(d string) { writeFile(t, filepath.Join(d, artifact.KernelMetadataFile), "agent-authored") })
	if err := f.hydrate([]bus.ArtifactRef{safe}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestCausalConflictParksBeforeLaunchingJoin(t *testing.T) {
	c := newCoreWithNative(t, "causal-block", ledger.NativeAbility{ID: "test:write", Command: "sh", Args: []string{"-c", "pwd > shared.txt"}, Tier: 1})
	withArtifactPool(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Register(ctx); err != nil {
		t.Fatal(err)
	}
	c.StartQueueScheduler(ctx)
	id, err := c.StartPlan(ctx, plan.Plan{Goal: "kernel regression", Stages: []plan.Stage{{ID: "left", Intent: "left", Requires: []string{"test:write"}}, {ID: "right", Intent: "right", Requires: []string{"test:write"}}, {ID: "join", Intent: "must-not-run", Needs: []string{"left", "right"}, Requires: []string{"test:write"}}}}, DefaultQueueSpec())
	if err != nil {
		t.Fatal(err)
	}
	var join Task
	for ctx.Err() == nil {
		rows, err := c.store.PlanStages(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		join = stageByID(t, rows, "join")
		if join.State == StateReview {
			break
		}
		if join.State == StateDone || join.State == StateFailed {
			t.Fatalf("conflict state=%s result=%s", join.State, join.ResultJSON)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if join.State != StateReview || join.ApprovalDisposition != ApprovalNeedsChangedInput {
		t.Fatalf("join did not block: %+v", join)
	}
	dir, err := c.stageWorkDir(id, "join")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared.txt")); !os.IsNotExist(err) {
		t.Fatalf("join launched or partially hydrated: %v", err)
	}
	if err := c.store.Approve(context.Background(), join.TaskID); err == nil {
		t.Fatal("approval certified an unresolved conflict")
	}
	waitArtifactWrites(t, c)
}

func TestCausalProtocolCapability(t *testing.T) {
	c := newCore(t, "protocol-node")
	if got := c.summary().Capacity.ArtifactMergeVersion; got != artifactMergeVersion {
		t.Fatal(got)
	}
	nodes := []ledger.Node{{ID: "old", Capacity: ledger.Capacity{}}, {ID: "new", Capacity: ledger.Capacity{ArtifactMergeVersion: 1}}}
	got := c.eligibleForSend(context.Background(), trust.Needs{MinArtifactMergeVersion: 1}, nodes)
	if len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("incompatible candidate retained: %+v", got)
	}
	if err := ledger.UpsertRemote(c.db, "old", ledger.CapabilitySummary{Device: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := c.requireArtifactProtocol(context.Background(), "old", "{\"artifact_merge_version\":1}"); err == nil {
		t.Fatal("new task sent to old node")
	}
}
