// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/providers"
)

// agentContextSample is one task's newest request-scoped context reading.
// `agent_context` events are written once per model request while the
// adapter runs (see core.EvAgentContext), so the newest row is the freshest
// answer to "how full is the agent's window right now".
type agentContextSample struct {
	tokens  int64
	model   string
	adapter string
}

// attachAgentContext stamps every task with the newest context sample its
// agent reported, plus the window the catalogue resolves for the model that
// served it. One batched query for the whole list, mirroring
// attachLastEvents: the session's task list is polled on every change frame
// and a per-row read would put one query per task on the page that polls
// fastest.
//
// A task that never ran an adapter (queued, routed away, cancelled) has no
// sample and stays zero — the console then falls back to the entry model's
// own turn rather than inventing an agent number.
func (h *handler) attachAgentContext(ctx context.Context, tasks []taskJSON) {
	if h.db == nil || len(tasks) == 0 {
		return
	}
	samples := make(map[string]agentContextSample, len(tasks))
	const batch = 400
	for start := 0; start < len(tasks); start += batch {
		h.agentContextBatch(ctx, tasks[start:min(start+batch, len(tasks))], samples)
	}
	for i := range tasks {
		sample, ok := samples[tasks[i].ID]
		if !ok {
			continue
		}
		tasks[i].ContextTokens = sample.tokens
		tasks[i].ContextModel = sample.model
		tasks[i].ContextWindow = contextWindowForModel(sample.model)
		// The row's Agent column is minted from the adapter's session
		// checkpoint, and an adapter that never mints one leaves it empty.
		// The sample names the adapter that actually produced it, so it can
		// fill the gap — the console must not drop a reading just because
		// the row never latched a session.
		if tasks[i].Agent == "" && sample.adapter != "" {
			tasks[i].Agent = strings.TrimSuffix(sample.adapter, ".py")
		}
	}
}

// agentContextBatch fills `out` with one batch's newest context sample per
// task.
func (h *handler) agentContextBatch(ctx context.Context, tasks []taskJSON, out map[string]agentContextSample) {
	marks := make([]string, len(tasks))
	args := make([]any, len(tasks))
	for i, t := range tasks {
		marks[i] = "?"
		args[i] = t.ID
	}
	rows, err := h.db.QueryContext(ctx,
		`SELECT task_id, COALESCE(data_json, '{}')
		   FROM task_events
		  WHERE id IN (SELECT MAX(id) FROM task_events
		                WHERE task_id IN (`+strings.Join(marks, ",")+`)
		                  AND type = ?
		                GROUP BY task_id)`, append(args, core.EvAgentContext)...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var (
			taskID  string
			raw     string
			payload struct {
				ContextTokens int64  `json:"context_tokens"`
				Model         string `json:"model"`
				Adapter       string `json:"adapter"`
			}
		)
		if err := rows.Scan(&taskID, &raw); err != nil {
			return
		}
		if json.Unmarshal([]byte(raw), &payload) != nil || payload.ContextTokens <= 0 {
			continue
		}
		out[taskID] = agentContextSample{
			tokens: payload.ContextTokens, model: payload.Model, adapter: payload.Adapter,
		}
	}
}

// newestAgentContext reads one task's newest sample for the single-row
// brief projection (the chat bubble's read), the indexed counterpart of
// attachAgentContext's batched query.
func (h *handler) newestAgentContext(ctx context.Context, taskID string) (agentContextSample, bool) {
	ev := h.newestEvent(ctx, taskID, core.EvAgentContext)
	if ev == nil || ev.Data == nil {
		return agentContextSample{}, false
	}
	fields, ok := ev.Data.(map[string]any)
	if !ok {
		return agentContextSample{}, false
	}
	tokens := int64(0)
	switch v := fields["context_tokens"].(type) {
	case float64:
		tokens = int64(v)
	case int64:
		tokens = v
	case int:
		tokens = int64(v)
	}
	if tokens <= 0 {
		return agentContextSample{}, false
	}
	model, _ := fields["model"].(string)
	adapter, _ := fields["adapter"].(string)
	return agentContextSample{tokens: tokens, model: model, adapter: adapter}, true
}

// contextWindowForModel resolves the advertised context window for a model
// id an agent reported. The provider catalogue is the only source that can
// answer without inventing a number: a name it does not know returns 0, and
// the console prints the count alone (the same rule the entry model
// follows when its row carries no window).
func contextWindowForModel(model string) int {
	m := strings.TrimSpace(model)
	if m == "" {
		return 0
	}
	// Claude Code marks the long-window beta in the id itself
	// ("claude-sonnet-4-5[1m]"); the catalogue only knows the base id, so the
	// marker wins (same rule internal/localmodels applies when it imports a
	// Claude tier alias).
	if open := strings.LastIndex(m, "["); open > 0 && strings.HasSuffix(m, "]") {
		if strings.Contains(strings.ToUpper(m[open+1:len(m)-1]), "1M") {
			return 1_000_000
		}
	}
	p, ok := providers.Detect("", m)
	if !ok {
		return 0
	}
	return p.ContextFor(m)
}
