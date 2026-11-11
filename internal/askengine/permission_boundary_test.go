// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

func TestPlanEntryToolsRequireExplicitReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readOnly bool
		want     bool
	}{
		{"unknown_configured_tool", false, false},
		{"declared_read", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := entry.NewRegistry()
			called := false
			reg.Register(entry.Tool{Name: tc.name, Tier: defense.TierReversible, ReadOnly: tc.readOnly,
				Run: func(context.Context, map[string]any) (string, error) { called = true; return "read", nil }})
			_, err := executeToolChecked(withReadOnlyEntryTools(context.Background()), reg, &entry.ToolCall{Tool: tc.name}, true, false, "")
			if err != nil || called != tc.want {
				t.Fatalf("called=%v want=%v err=%v", called, tc.want, err)
			}
		})
	}
	// An ordinary request must not inherit another thread's read-only gate.
	reg := entry.NewRegistry()
	called := false
	reg.Register(entry.Tool{Name: "mutation", Tier: defense.TierReversible, Run: func(context.Context, map[string]any) (string, error) { called = true; return "ok", nil }})
	executeTool(context.Background(), reg, &entry.ToolCall{Tool: "mutation"}, false, false, "")
	if !called {
		t.Fatal("plan policy leaked into ordinary ask")
	}
}

func TestBuiltinPlanMutationToolsNotReadOnly(t *testing.T) {
	e, _, _ := newStageTestEngine(t)
	reg := buildToolRegistry(e, nil, nil, nil)
	for _, name := range []string{"memory_add", "memory_replace", "memory_remove", "skill_discover", "skill_install", "taskq_cancel", "taskq_priority", "project_create"} {
		tool, ok := reg.Lookup(name)
		if !ok || tool.ReadOnly {
			t.Fatalf("%s missing or incorrectly read-only: %+v", name, tool)
		}
	}
	for _, name := range []string{"system_status", "taskq_list", "skill_show", "time_now", "model_list"} {
		tool, ok := reg.Lookup(name)
		if !ok || !tool.ReadOnly {
			t.Fatalf("read tool %s not permitted", name)
		}
	}
}

func TestPlanLocalActionCannotLaunchService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeAnswer(w, "service was not started") }))
	defer srv.Close()
	e := newGuardTestEngine(t, srv, entry.NewRegistry(), 200000)
	var invoked atomic.Bool
	e.SetLocalActionRunner(func(context.Context, []string) (string, error) { invoked.Store(true); return "bad", nil })
	_, err := e.AskTurnsScoped(context.Background(), nil, "启动 web 服务", AskScope{PermissionMode: defense.ModePlan}, true, StreamCallbacks{})
	if err != nil || invoked.Load() {
		t.Fatalf("plan launched service=%v err=%v", invoked.Load(), err)
	}
}

func TestExplicitSpecDoesNotTakeFastPath(t *testing.T) {
	var sawMode atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "Request Mode: SPEC") {
			sawMode.Store(true)
		}
		writeAnswer(w, "specification")
	}))
	defer srv.Close()
	e := newGuardTestEngine(t, srv, entry.NewRegistry(), 200000)
	_, err := e.AskTurnsScoped(context.Background(), nil, "解释分布式系统", AskScope{Mode: "spec"}, false, StreamCallbacks{})
	if err != nil || !sawMode.Load() {
		t.Fatalf("spec mode lost=%v err=%v", !sawMode.Load(), err)
	}
}

func TestClassifiedPlanPreservesScopeAndSource(t *testing.T) {
	e, store, ctx := newStageTestEngine(t)
	e.queueTasks = true
	e.sched.Load().SetArtifactStore(artifact.NewStore(filepath.Join(t.TempDir(), "artifacts")))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	spec := &entry.PlanSpec{Goal: "repository audit", Stages: []entry.PlanStageSpec{
		{ID: "inspect", Intent: "inspect implementation", Kind: entry.TaskEdit},
		{ID: "report", Intent: "report findings", Needs: []string{"inspect"}},
	}}
	scope := AskScope{WorkDir: root, Project: "audit-project", SessionID: "audit-thread", PermissionMode: defense.ModePlan, Agents: []string{"codex"}}
	res, err := e.startClassifiedPlan(ctx, spec, scope, true, StreamCallbacks{})
	if err != nil || res.PlanID == "" || !res.OK {
		t.Fatalf("plan=%+v err=%v", res, err)
	}
	rows, err := store.PlanStages(ctx, res.PlanID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for _, row := range rows {
		var detail entry.TaskSpecDetail
		if err := json.Unmarshal([]byte(row.SpecJSON), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.PermissionMode != defense.ModePlan || detail.Kind != entry.TaskInvestigation || strings.Join(detail.AllowAgents, ",") != "codex" || row.Project != scope.Project || row.SessionID != scope.SessionID || row.WorkDir != "" || len(row.Inputs) != 1 || row.Inputs[0].Hash == "" || row.Authorized {
			t.Fatalf("scope lost or widened: row=%+v detail=%+v", row, detail)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "main.go")); err != nil {
		t.Fatal(err)
	}
}

func TestPlanSkillReadsDoNotSeedFilesystem(t *testing.T) {
	e, _, _ := newStageTestEngine(t)
	root := filepath.Join(t.TempDir(), "uncreated-skills")
	e.cfg.Storage.SkillsPath = root
	_, _ = e.toolSkillList(withReadOnlyEntryTools(context.Background()))
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("read-only skill listing created store: %v", err)
	}
	if e.skills != nil {
		t.Fatal("read-only initialization changed shared engine state")
	}
}

func TestExplicitProjectOutranksAmbientFallback(t *testing.T) {
	e, _, _ := newStageTestEngine(t)
	e.SetProject("ambient", "/ambient")
	project, dir := e.scopeProject(AskScope{Project: "explicit", AmbientProject: true})
	if project != "explicit" || dir != "" {
		t.Fatalf("explicit scope replaced by ambient: %s %s", project, dir)
	}
}
