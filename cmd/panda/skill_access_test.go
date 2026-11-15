// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/mcpserve"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

func TestSkillRuntimeReadFromCLIAndMCP(t *testing.T) {
	ctx := context.Background()
	d := selfDeps(t)
	task, err := d.tasks.Create(ctx, "", "one", "use a skill", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PANDA_TASK_ID", task.TaskID)
	for _, project := range []string{"one", "two"} {
		if err := d.skills.Save(&skills.Skill{Name: project + "-guide", Description: "diagnose task", Scope: skills.ScopeProject, Project: project, Status: skills.StatusActive, Body: project + " procedure"}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := d.toolSkillSearch(ctx, map[string]any{"query": "diagnose"})
	if err != nil || !strings.Contains(result, "one-guide") || strings.Contains(result, "two-guide") {
		t.Fatalf("scope search: %s %v", result, err)
	}
	if _, err := d.toolSkillRead(ctx, map[string]any{"name": "two-guide"}); err == nil {
		t.Fatal("MCP leaked another project's skill")
	}
	result, err = d.toolSkillRead(ctx, map[string]any{"name": "one-guide"})
	if err != nil || !strings.Contains(result, "one procedure") {
		t.Fatalf("MCP read: %s %v", result, err)
	}
	access, id, err := runtimeSkillAccess(ctx, d.cfg, d.tasks)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	read, err := skillReadTo(&buf, d.skills, access, []string{"one-guide", "--limit", "4000"})
	if err != nil || !strings.Contains(buf.String(), "one procedure") {
		t.Fatalf("CLI read: %s %v", buf.String(), err)
	}
	recordSkillRead(ctx, d.tasks, d.skills, id, read, "cli")
	events, err := d.tasks.Events(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	loads := 0
	for _, event := range events {
		if event.Type == core.EvSkillLoad {
			loads++
			if !strings.Contains(event.DataJSON, "sha256") || strings.Contains(event.DataJSON, "one procedure") {
				t.Fatal("invalid read evidence")
			}
		}
	}
	if loads != 2 {
		t.Fatalf("expected two delivered skill events, got %d", loads)
	}
	t.Setenv("PANDA_TASK_ID", "missing-task")
	if _, err := d.toolSkillRead(ctx, map[string]any{"name": "one-guide"}); err == nil {
		t.Fatal("unknown task widened access")
	}
}

func TestSelfToolSchemaFootprint(t *testing.T) {
	full := selfTools(selfDeps(t))
	compact := mcpserve.Compact(full, func(name string) bool {
		return name == "panda_skill_search" || name == "panda_skill_read" || name == "panda_subtask_spawn" || name == "panda_subtask_await"
	})
	measure := func(tools []mcpserve.Tool) int {
		rows := make([]map[string]any, 0)
		for _, tool := range tools {
			rows = append(rows, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema})
		}
		b, _ := json.Marshal(rows)
		return len(b)
	}
	f, c := measure(full), measure(compact)
	if c >= f {
		t.Fatalf("compact schemas expanded: %d >= %d", c, f)
	}
	t.Logf("MCP tool schemas: full=%d bytes (%d tools), compact=%d bytes (%d tools)", f, len(full), c, len(compact))
}

func TestSkillRuntimePagingKeepsOffsetsStable(t *testing.T) {
	ctx := context.Background()
	d := selfDeps(t)
	task, err := d.tasks.Create(ctx, "", "", "read procedure", "test-node", []string{"test-node"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PANDA_TASK_ID", task.TaskID)
	sk := &skills.Skill{Name: "long-procedure", Description: "paged procedure", Scope: skills.ScopeGlobal, Status: skills.StatusActive, Body: strings.Repeat("分页 procedure ", 700)}
	if err := d.skills.Save(sk); err != nil {
		t.Fatal(err)
	}
	initial, err := sk.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	offset, hash := 0, ""
	for {
		raw, err := d.toolSkillRead(ctx, map[string]any{"name": sk.Name, "offset": offset, "sha256": hash})
		if err != nil {
			t.Fatal(err)
		}
		var page skills.ReadResult
		if err := json.Unmarshal([]byte(raw), &page); err != nil {
			t.Fatal(err)
		}
		joined.WriteString(page.Content)
		stored, err := d.skills.Load(sk.Scope, "", sk.Name)
		if err != nil {
			t.Fatal(err)
		}
		if !page.Truncated {
			if stored.UseCount != 1 {
				t.Fatalf("completed read use_count=%d", stored.UseCount)
			}
			break
		}
		if stored.UseCount != 0 {
			t.Fatal("read changed SKILL.md before the last page")
		}
		offset, hash = page.NextOffset, page.SHA256
	}
	if joined.String() != string(initial) {
		t.Fatal("paged SKILL.md skipped or duplicated bytes")
	}
}
