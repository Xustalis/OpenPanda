package memory

import (
	"errors"
	"strings"
	"testing"
)

func TestToolAddReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	tool := NewTool(NewHermes(root), NewProjects(root))

	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user", "entry": "prefers dark mode"}); err != nil {
		t.Fatalf("add user: %v", err)
	}
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "memory", "entry": "core is Go"}); err != nil {
		t.Fatalf("add memory: %v", err)
	}

	out, err := tool.Execute(ToolRead, nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "prefers dark mode") || !strings.Contains(out, "core is Go") {
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
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "project", "project": "panda", "entry": "Go core + Python glue"}); err != nil {
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
	big := strings.Repeat("x", MemoryCharLimit+10)
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
