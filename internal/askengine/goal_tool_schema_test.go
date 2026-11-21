// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"encoding/json"
	"testing"
)

// A goal tool with no arguments used to publish `"required": null`, because a
// variadic invoked with nothing yields a nil slice. That is not valid JSON
// Schema, and DeepSeek rejects the whole request with
//
//	400 Invalid schema for function 'get_goal': null is not of type "array"
//
// which took the console offline for every model behind that endpoint: one
// malformed tool schema fails the turn, not just the one tool.
func TestGoalToolSchemaAlwaysPublishesRequiredAsArray(t *testing.T) {
	cases := []struct {
		name    string
		schema  map[string]any
		entries int
	}{
		{"no arguments", goalToolSchema(map[string]any{}), 0},
		{"one argument", goalToolSchema(map[string]any{"objective": map[string]any{"type": "string"}}, "objective"), 1},
		{"two arguments", goalToolSchema(map[string]any{"status": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}}, "status", "reason"), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.schema)
			if err != nil {
				t.Fatalf("marshal schema: %v", err)
			}
			// Decode through `any` rather than into a typed struct: null decodes
			// into a nil []string without complaint, so a typed target would miss
			// exactly the failure this test exists to catch.
			var generic map[string]any
			if err := json.Unmarshal(raw, &generic); err != nil {
				t.Fatalf("unmarshal schema %s: %v", raw, err)
			}
			required, ok := generic["required"]
			if !ok {
				t.Fatalf("schema has no required key: %s", raw)
			}
			entries, isArray := required.([]any)
			if !isArray {
				t.Fatalf("required must be a JSON array, got %T (%v): %s", required, required, raw)
			}
			if len(entries) != tc.entries {
				t.Fatalf("required has %d entries, want %d: %s", len(entries), tc.entries, raw)
			}
			if generic["type"] != "object" {
				t.Fatalf("type = %v, want object: %s", generic["type"], raw)
			}
			properties, isObject := generic["properties"].(map[string]any)
			if !isObject {
				t.Fatalf("properties must be an object: %s", raw)
			}
			if len(properties) != tc.entries {
				t.Fatalf("properties has %d entries, want %d: %s", len(properties), tc.entries, raw)
			}
		})
	}
}

// Every name a schema lists as required must also appear in properties, or a
// strict provider rejects the definition even when required is a well-formed
// array.
func TestGoalToolSchemaRequiresOnlyDeclaredProperties(t *testing.T) {
	schema := goalToolSchema(
		map[string]any{"status": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}},
		"status", "reason",
	)
	properties, _ := schema["properties"].(map[string]any)
	for _, name := range schema["required"].([]string) {
		if _, ok := properties[name]; !ok {
			t.Fatalf("required names %q but properties does not declare it", name)
		}
	}
}
