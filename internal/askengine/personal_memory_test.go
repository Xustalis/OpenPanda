// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/memory"
)

func TestPersonalMemoryCapabilityNeverSerialized(t *testing.T) {
	data, err := json.Marshal(AskScope{PersonalMemory: true, Project: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PersonalMemory") || strings.Contains(string(data), "personal_memory") {
		t.Fatalf("owner capability serialized: %s", data)
	}
	var scope AskScope
	if err := json.Unmarshal([]byte("{\"PersonalMemory\":true,\"personal_memory\":true}"), &scope); err != nil {
		t.Fatal(err)
	}
	if scope.PersonalMemory {
		t.Fatal("wire input granted owner capability")
	}
}

// The ask loop carries the trusted grant to tools, including Side, without
// letting it override Plan mode or leak to the next non-owner ask.
func TestPersonalMemoryOwnerConversationToolLoop(t *testing.T) {
	var mu sync.Mutex
	var calls int
	var tool string
	var args map[string]any
	var resultBody string
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		calls++
		w.Header().Set("content-type", "application/json")
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content":     []map[string]any{{"type": "tool_use", "id": "memory-call", "name": tool, "input": args}},
				"stop_reason": "tool_use",
			})
			return
		}
		resultBody = string(body)
		_ = json.NewEncoder(w).Encode(anthropicText("Memory operation complete."))
	})
	root := t.TempDir()
	hermes := memory.NewHermes(root)
	projects := memory.NewProjects(t.TempDir())
	e.injector = memory.NewInjector(hermes, projects)
	e.registry = buildToolRegistry(e, hermes, projects, nil)

	owner := func(project string) AskScope {
		return AskScope{PersonalMemory: true, Project: project, WorkDir: filepath.Join(root, project)}
	}
	side := owner("beta")
	side.Side = true
	plan := owner("gamma")
	plan.PermissionMode = defense.ModePlan
	for _, tc := range []struct {
		name  string
		scope AskScope
		tool  string
		args  map[string]any
		want  string
	}{
		{"owner writes in project", owner("alpha"), memory.ToolAdd, map[string]any{"target": "user", "entry": "prefers dark mode", "category": "preference", "key": "preference.theme"}, "dark mode"},
		{"side reads across projects", side, memory.ToolRead, map[string]any{"target": "user"}, "dark mode"},
		{"side writes personal memory", side, memory.ToolAdd, map[string]any{"target": "memory", "entry": "Server is Ubuntu 24.04", "category": "environment", "key": "environment.server.os"}, "Ubuntu 24.04"},
		{"owner reads Side memory", owner("gamma"), memory.ToolRead, map[string]any{"target": "memory"}, "Ubuntu 24.04"},
		{"named worker denied", AskScope{Project: "alpha", WorkDir: filepath.Join(root, "alpha")}, memory.ToolRead, map[string]any{"target": "user"}, "restricted to owner conversations"},
		{"project-free worker denied", AskScope{}, memory.ToolRead, map[string]any{"target": "memory"}, "restricted to owner conversations"},
		{"plan owner write denied", plan, memory.ToolAdd, map[string]any{"target": "user", "entry": "prefers light mode", "category": "preference", "key": "preference.theme"}, "plan"},
		{"plan owner read allowed", plan, memory.ToolRead, map[string]any{"target": "user"}, "dark mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			calls, tool, args, resultBody = 0, tc.tool, tc.args, ""
			mu.Unlock()
			if _, err := e.AskTurnsScoped(context.Background(), nil, "请使用记忆工具处理我的长期偏好", tc.scope, true, StreamCallbacks{}); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			got, n := resultBody, calls
			mu.Unlock()
			if n != 2 || !strings.Contains(got, tc.want) {
				t.Fatalf("tool loop requests=%d, want 2; missing result %q in %s", n, tc.want, got)
			}
		})
	}
	profile, err := hermes.LoadUser()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(profile.Entries, "\n"); !strings.Contains(got, "dark mode") || strings.Contains(got, "light mode") {
		t.Fatalf("Plan mutated personal profile: %q", got)
	}
}
