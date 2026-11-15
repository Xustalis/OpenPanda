// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/util"
)

// EnableDiscovery narrows schemas on this request's private registry, while
// leaving execution and permission metadata intact. Discovery is reversible
// and additive: an unfamiliar capability can always be found by catalog page.
func (r *Registry) EnableDiscovery(essential func(string) bool) {
	r.exposed = make(map[string]bool)
	for name := range r.tools {
		if essential(name) {
			r.exposed[name] = true
		}
	}
	r.bindDiscoveryTool()
}

// A copied/filtered registry must bind discovery to its own maps, so a
// request cannot reveal another request's hidden or filtered capabilities.
func (r *Registry) bindDiscoveryTool() {
	r.Register(Tool{
		Name: "tool_search", ReadOnly: true, Tier: defense.TierReversible,
		Description: "Find and load tool schemas by capability or exact name. Use before unavailable operations. Query '*' pages the catalog; selected tools become callable next round.",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"query":  map[string]any{"type": "string"},
			"offset": map[string]any{"type": "integer", "minimum": 0},
		}, "required": []string{"query"}},
		Run: func(_ context.Context, args map[string]any) (string, error) {
			query, _ := args["query"].(string)
			query = strings.ToLower(strings.TrimSpace(query))
			offset := 0
			if n, ok := args["offset"].(float64); ok && n >= 0 {
				offset = int(min(n, float64(len(r.tools))))
			}
			type item struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				score       int
			}
			items := make([]item, 0)
			tokens := util.Tokenize(query)
			for name, t := range r.tools {
				if name == "tool_search" {
					continue
				}
				text := strings.ToLower(name + " " + t.Description)
				score := 0
				if query == "*" || query == "" {
					score = 1
				} else {
					if strings.Contains(text, query) {
						score += 20
					}
					if name == query {
						score += 100
					}
					for token := range tokens {
						if !util.IsStopword(token) && strings.Contains(text, token) {
							score++
						}
					}
				}
				if score > 0 {
					items = append(items, item{Name: name, Description: t.Description, score: score})
				}
			}
			sort.Slice(items, func(i, j int) bool {
				if items[i].score != items[j].score {
					return items[i].score > items[j].score
				}
				return items[i].Name < items[j].Name
			})
			total := len(items)
			offset = min(offset, total)
			end := min(offset+8, total)
			page := items[offset:end]
			for _, i := range page {
				r.exposed[i.Name] = true
			}
			b, err := json.Marshal(map[string]any{"tools": page, "total": total, "next_offset": end, "more": end < total, "hint": "Schemas loaded for next round. If no match, query '*' to browse all capabilities."})
			return string(b), err
		},
	})
	r.exposed["tool_search"] = true
}
