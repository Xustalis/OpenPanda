// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserve

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/util"
)

// Compact exposes essential tools plus discovery and a call gateway. Schema
// discovery returns a small page; callers do not need MCP list-change support.
// The gateway invokes the original handler with the same guards/panic boundary.
func Compact(tools []Tool, essential func(string) bool) []Tool {
	byName := make(map[string]Tool)
	out := make([]Tool, 0)
	for _, t := range tools {
		byName[t.Name] = t
		if essential(t.Name) {
			out = append(out, t)
		}
	}
	out = append(out, Tool{Name: "panda_tools", Description: "Discover OpenPanda tools and input schemas by capability/name. Query '*' with offset to browse. Invoke discovered operations with panda_tool_call.",
		InputSchema: SchemaObject(map[string]any{"query": StringProp("keywords or '*'"), "offset": IntProp("catalog page offset; default 0")}, "query"),
		Handle: func(_ context.Context, args map[string]any) (string, error) {
			query := strings.ToLower(Arg(args, "query"))
			offset := 0
			if n, ok := args["offset"].(float64); ok && n >= 0 {
				offset = int(min(n, float64(len(tools))))
			}
			type found struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Schema      json.RawMessage `json:"inputSchema"`
				score       int
			}
			matches := make([]found, 0)
			tokens := util.Tokenize(query)
			for _, t := range tools {
				text := strings.ToLower(t.Name + " " + t.Description)
				score := 0
				if query == "*" || query == "" {
					score = 1
				} else {
					if strings.Contains(text, query) {
						score += 20
					}
					for token := range tokens {
						if !util.IsStopword(token) && strings.Contains(text, token) {
							score++
						}
					}
				}
				if score > 0 {
					matches = append(matches, found{t.Name, t.Description, t.InputSchema, score})
				}
			}
			sort.Slice(matches, func(i, j int) bool {
				if matches[i].score != matches[j].score {
					return matches[i].score > matches[j].score
				}
				return matches[i].Name < matches[j].Name
			})
			offset = min(offset, len(matches))
			end := min(offset+4, len(matches))
			b, err := json.Marshal(map[string]any{"tools": matches[offset:end], "next_offset": end, "more": end < len(matches), "total": len(matches)})
			return string(b), err
		}}, Tool{Name: "panda_tool_call", Description: "Invoke an operation discovered by panda_tools using its exact name and arguments. Existing operation authorization and task limits still apply.",
		InputSchema: SchemaObject(map[string]any{"name": StringProp("discovered tool name"), "arguments": map[string]any{"type": "object"}}, "name", "arguments"),
		Handle: func(ctx context.Context, args map[string]any) (string, error) {
			name := Arg(args, "name")
			t, ok := byName[name]
			if !ok {
				return "", fmt.Errorf("unknown tool %q; discover with panda_tools", name)
			}
			params, ok := args["arguments"].(map[string]any)
			if !ok {
				return "", fmt.Errorf("arguments must be an object")
			}
			return invokeTool(ctx, t, params)
		}})
	return out
}
