// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/taskhub"
)

func (e *Engine) registerTeamSearch(reg *entry.Registry, scope AskScope, project string) {
	if !scope.PersonalMemory || e.db == nil {
		return
	}
	reg.Register(entry.Tool{Name: "tsync_task_search", ReadOnly: true, Tier: defense.TierReversible,
		Description: "Search the synchronized TSync team board by title, space or project. Use for 团队空间、TSync、协作任务. Include the returned task links in your reply. For my assignments set mine=true. Project conversations see only explicitly bound team tasks.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":  map[string]any{"type": "string"},
				"mine":   map[string]any{"type": "boolean"},
				"offset": map[string]any{"type": "integer", "minimum": 0},
			},
			"additionalProperties": false,
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			query, validQuery := args["query"].(string)
			if _, provided := args["query"]; provided && !validQuery {
				return "", errors.New("query must be a string")
			}
			mine, validMine := args["mine"].(bool)
			if _, provided := args["mine"]; provided && !validMine {
				return "", errors.New("mine must be a boolean")
			}
			offset := 0
			if v, ok := args["offset"]; ok {
				n, err := strconv.Atoi(fmt.Sprint(v))
				if err != nil || n < 0 {
					return "", errors.New("invalid task offset")
				}
				offset = n
			}
			rows, err := taskhub.New(e.db).SearchTeam(ctx, project, query, mine, offset)
			if err != nil {
				return "", err
			}
			result := struct {
				Tasks []taskhub.TeamTaskSummary `json:"tasks"`
				Next  int                       `json:"nextOffset,omitempty"`
				Board string                    `json:"board"`
			}{Tasks: rows, Board: "#/team"}
			if len(rows) > 20 {
				result.Tasks = rows[:20]
				result.Next = offset + 20
			}
			b, err := json.Marshal(result)
			return string(b), err
		}})
}
