// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserve

import (
	"context"
	"strings"
	"testing"
)

func TestCompactDiscoveryPreservesHandlersAndLimits(t *testing.T) {
	calls := 0
	tools := Compact([]Tool{{Name: "private_submit", Description: "submit a test task", InputSchema: SchemaObject(map[string]any{"title": StringProp("title")}, "title"), Handle: func(_ context.Context, args map[string]any) (string, error) {
		calls++
		if calls > 1 {
			return "", context.DeadlineExceeded
		}
		return Arg(args, "title"), nil
	}}}, func(string) bool { return false })
	if len(tools) != 2 {
		t.Fatalf("eager tool surface: %d", len(tools))
	}
	result, err := tools[0].Handle(context.Background(), map[string]any{"query": "submit"})
	if err != nil || !strings.Contains(result, "inputSchema") || !strings.Contains(result, "private_submit") {
		t.Fatalf("missing discovered schema: %s %v", result, err)
	}
	args := map[string]any{"name": "private_submit", "arguments": map[string]any{"title": "run"}}
	if result, err := tools[1].Handle(context.Background(), args); err != nil || result != "run" {
		t.Fatalf("invoke: %s %v", result, err)
	}
	if _, err := tools[1].Handle(context.Background(), args); err == nil {
		t.Fatal("gateway bypassed original handler limit")
	}
	if _, err := tools[1].Handle(context.Background(), map[string]any{"name": "unknown", "arguments": map[string]any{}}); err == nil {
		t.Fatal("unknown tool accepted")
	}
}
