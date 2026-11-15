// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestProjectTurnCannotReadOrWritePersonalMemory(t *testing.T) {
	tool := NewTool(NewHermes(t.TempDir()), NewProjects(t.TempDir()))
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user", "entry": "user prefers concise answers"}); err != nil {
		t.Fatal(err)
	}
	projectCtx := WithScopeProject(context.Background(), "work")
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{ToolRead, nil},
		{ToolRead, map[string]any{"target": "user"}},
		{ToolRead, map[string]any{"target": "memory"}},
		{ToolAdd, map[string]any{"target": "memory", "entry": "leaked"}},
		{ToolReplace, map[string]any{"target": "user", "old": "private", "new": "leaked"}},
		{ToolRemove, map[string]any{"target": "user", "old": "private"}},
	} {
		if out, err := tool.ExecuteCtx(projectCtx, tc.name, tc.args); err == nil {
			t.Errorf("%s %+v returned %q in project context", tc.name, tc.args, out)
		}
	}
	if _, err := tool.ExecuteCtx(projectCtx, ToolAdd, map[string]any{"target": "project", "entry": "项目约定：使用 Go"}); err != nil {
		t.Fatal(err)
	}
	workspaceCtx := WithoutPersonalMemory(WithScopeProject(context.Background(), ""))
	if _, err := tool.ExecuteCtx(workspaceCtx, ToolRead, map[string]any{"target": "user"}); err == nil {
		t.Fatal("unnamed workspace can read personal memory")
	}
	if out, err := tool.ExecuteCtx(WithScopeProject(context.Background(), ""), ToolRead, map[string]any{"target": "user"}); err != nil || !strings.Contains(out, "user prefers concise answers") {
		t.Fatalf("project-free personal read: %q, %v", out, err)
	}
}

func TestToolAddReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	tool := NewTool(NewHermes(root), NewProjects(root))

	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user", "entry": "prefers dark mode"}); err != nil {
		t.Fatalf("add user: %v", err)
	}
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "memory", "entry": "Server is Ubuntu 24.04"}); err != nil {
		t.Fatalf("add memory: %v", err)
	}

	out, err := tool.Execute(ToolRead, nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "prefers dark mode") || !strings.Contains(out, "Server is Ubuntu 24.04") {
		t.Errorf("read should list both layers, got %q", out)
	}
}

func TestToolReplaceRemove(t *testing.T) {
	tool := NewTool(NewHermes(t.TempDir()), nil)
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user", "entry": "prefers dark mode"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := tool.Execute(ToolReplace, map[string]any{"target": "user", "old": "dark mode", "new": "prefers light mode"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if _, err := tool.Execute(ToolRemove, map[string]any{"target": "user", "old": "light mode"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	out, _ := tool.Execute(ToolRead, nil)
	if strings.Contains(out, "mode") {
		t.Errorf("entry should be gone after remove, got %q", out)
	}
}

func TestToolProjectTarget(t *testing.T) {
	tool := NewTool(nil, NewProjects(t.TempDir()))
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "project", "project": "panda", "entry": "项目架构：Go core + Python glue"}); err != nil {
		t.Fatalf("add project: %v", err)
	}
	// A project target without a project name must fail.
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "project", "entry": "x"}); err == nil {
		t.Errorf("project target without project name should error")
	}
	out, err := tool.Execute(ToolRead, map[string]any{"target": "project", "project": "panda"})
	if err != nil || !strings.Contains(out, "Go core") {
		t.Errorf("read project: got %q err=%v", out, err)
	}
}

func TestToolProjectRemoveClearsAttributeCopies(t *testing.T) {
	projects := NewProjects(t.TempDir())
	if err := projects.Save("alpha", MemFile{Entries: []string{
		"[fact:project:test.command] test command: go test ./old",
		"[fact:project:test.command] test command: go test ./...",
		"[fact:project:build.command] build command: go build ./...",
	}}); err != nil {
		t.Fatal(err)
	}
	tool := NewTool(nil, projects)
	if _, err := tool.Execute(ToolRemove, map[string]any{
		"target": "project", "project": "alpha", "old": "go test ./...",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := projects.Load("alpha")
	if err != nil || len(got.Entries) != 1 || !strings.Contains(got.Entries[0], "go build") {
		t.Fatalf("removed attribute resurfaced: %v, %v", got.Entries, err)
	}
}

func TestToolUnknownAndMalformed(t *testing.T) {
	tool := NewTool(NewHermes(t.TempDir()), nil)
	if _, err := tool.Execute("weather.get", map[string]any{}); err == nil {
		t.Errorf("unknown tool should error")
	}
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user"}); err == nil {
		t.Errorf("missing entry should error")
	}
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user", "entry": 42}); err == nil {
		t.Errorf("non-string entry should error")
	}
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "bogus", "entry": "x"}); err == nil {
		t.Errorf("unknown target should error")
	}
}

func TestToolAddOverLimit(t *testing.T) {
	tool := NewTool(NewHermes(t.TempDir()), nil)
	big := "Server is " + strings.Repeat("x", MemoryCharLimit+10)
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "memory", "entry": big}); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("over-limit add: got %v, want ErrOverLimit", err)
	}
}

func TestToolNilStore(t *testing.T) {
	tool := NewTool(nil, nil)
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user", "entry": "x"}); !errors.Is(err, ErrNoStore) {
		t.Fatalf("nil store: got %v, want ErrNoStore", err)
	}
}

// TestToolReadHonorsTarget: a named layer must list that layer alone — the
// old behavior returned both personal layers for target=user|memory, so a
// "what's in MEMORY.md" read silently included the user profile.
func TestToolReadHonorsTarget(t *testing.T) {
	tool := NewTool(NewHermes(t.TempDir()), nil)
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user", "entry": "prefers dark mode"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "memory", "entry": "Server is Ubuntu 24.04"}); err != nil {
		t.Fatal(err)
	}
	out, err := tool.Execute(ToolRead, map[string]any{"target": "user"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "prefers dark mode") || strings.Contains(out, "Server is Ubuntu 24.04") {
		t.Errorf("target=user must list only the user layer, got %q", out)
	}
	out, err = tool.Execute(ToolRead, map[string]any{"target": "memory"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Server is Ubuntu 24.04") || strings.Contains(out, "prefers dark mode") {
		t.Errorf("target=memory must list only the memory layer, got %q", out)
	}
}
