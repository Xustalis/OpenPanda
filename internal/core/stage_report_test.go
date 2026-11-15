// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

func TestStageReportIsBoundedAndCannotEscapeWorkspace(t *testing.T) {
	root := t.TempDir()
	task := Task{TaskID: "t", PlanID: "p", StageID: "sensor", SpecJSON: `{"stage_reports":true}`}
	result := commander.Result{OK: true, Stdout: "API_KEY=hidden " + strings.Repeat("读数", 6000)}
	if err := writeStageReport(task, root, "pi", result, entry.SuperviseVerdict{Status: "done"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".panda-stage-reports", "sensor.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 14000 || !json.Valid(data) || strings.Contains(string(data), "hidden") || !strings.Contains(string(data), "truncated") {
		t.Fatalf("invalid report: %d bytes", len(data))
	}
	// A recovered attempt replaces its own previous report, so descendants
	// cannot mistake a stale candidate for the final branch output.
	result.Stdout = "recovered output"
	if err := writeStageReport(task, root, "pi", result, entry.SuperviseVerdict{Status: "done"}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(root, ".panda-stage-reports", "sensor.json"))
	if err != nil || !strings.Contains(string(data), "recovered output") || strings.Contains(string(data), "truncated") {
		t.Fatalf("stale stage report after recovery: %s %v", data, err)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(root, ".panda-stage-reports")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".panda-stage-reports")); err != nil {
		t.Skip(err)
	}
	if err := writeStageReport(task, root, "pi", result, entry.SuperviseVerdict{}); err == nil {
		t.Fatal("followed external report directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "sensor.json")); !os.IsNotExist(err) {
		t.Fatal("wrote outside workspace")
	}
}

func TestStageReportsFollowBothCausalBranches(t *testing.T) {
	c := newCore(t, "reports-join")
	withArtifactPool(t, c)
	ctx := context.Background()
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "source.txt"), "shared baseline")
	manifest, err := c.artifacts.PackDir(source)
	if err != nil {
		t.Fatal(err)
	}
	base := bus.ArtifactRef{Stage: worktreeArtifactStage, Hash: manifest.Hash, Source: c.nodeID}
	inputs := []bus.ArtifactRef{base}
	for _, name := range []string{"sensor", "analysis"} {
		task, _, _, err := c.createTask(ctx, TaskInput{Title: name})
		if err != nil {
			t.Fatal(err)
		}
		task.PlanID, task.StageID, task.Inputs = "report-plan", name, []bus.ArtifactRef{base}
		task.SpecJSON = `{"stage_reports":true}`
		dir := t.TempDir()
		if err := c.fetchStageInputs(ctx, task, dir); err != nil {
			t.Fatal(err)
		}
		if err := writeStageReport(task, dir, name+"-node", commander.Result{OK: true, Stdout: name + " evidence"}, entry.SuperviseVerdict{}); err != nil {
			t.Fatal(err)
		}
		hash, err := c.packStageOutput(ctx, task, dir)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, bus.ArtifactRef{Stage: name, Hash: hash, Source: c.nodeID})
	}
	joined := t.TempDir()
	if err := c.fetchStageInputs(ctx, Task{PlanID: "report-plan", StageID: "final", Inputs: inputs}, joined); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sensor", "analysis"} {
		data, err := os.ReadFile(filepath.Join(joined, ".panda-stage-reports", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			Stdout string `json:"stdout"`
			Node   string `json:"node"`
		}
		if err := json.Unmarshal(data, &report); err != nil || report.Stdout != name+" evidence" || report.Node != name+"-node" {
			t.Fatalf("lost branch text or attribution: %s %v", data, err)
		}
	}
}
