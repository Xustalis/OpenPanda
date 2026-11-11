// SPDX-License-Identifier: AGPL-3.0-or-later
package core

import (
	"context"
	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"os"
	"path/filepath"
	"testing"
)

func TestStageInputsPreserveLocalCheckpoint(t *testing.T) {
	c := newCore(t, "checkpoint-files")
	c.artifacts = artifact.NewStore(t.TempDir())
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "app.js"), []byte("upstream"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := c.artifacts.PackDir(source)
	if err != nil {
		t.Fatal(err)
	}
	task := createTask(t, c.store, "", "stage", c.nodeID)
	task.Inputs = []bus.ArtifactRef{{Hash: m.Hash, Source: c.nodeID, Stage: "behavior"}}
	work := t.TempDir()
	if err = c.fetchStageInputs(context.Background(), task, work); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(work, "app.js"), []byte("integrated changes"), 0600); err != nil {
		t.Fatal(err)
	}
	task.AgentSessionID = "checkpoint"
	task.AgentSessionNode = c.nodeID
	if err = c.fetchStageInputs(context.Background(), task, work); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(work, "app.js"))
	if string(got) != "integrated changes" {
		t.Fatalf("checkpoint overwritten: %q", got)
	}
	if err = c.fetchStageInputs(context.Background(), task, t.TempDir()); err == nil {
		t.Fatal("missing checkpoint workspace silently rebuilt")
	}
	task.AgentSessionNode = "foreign-node"
	if err = c.fetchStageInputs(context.Background(), task, work); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(work, "app.js"))
	if string(got) != "upstream" {
		t.Fatalf("foreign checkpoint prevented hydration: %q", got)
	}
}
