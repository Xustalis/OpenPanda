// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestTaskBudgetDerivesFromRoster pins the arithmetic that made a four-agent
// team run only three: the floor is defaultTaskBudget, and the user's own
// roster raises it to the number of hands they picked. A roster is the one
// deterministic statement of width an ask has.
func TestTaskBudgetDerivesFromRoster(t *testing.T) {
	cases := []struct {
		roster int
		want   int
	}{
		{0, defaultTaskBudget},
		{1, defaultTaskBudget},
		{3, defaultTaskBudget},
		{4, 4},
		{9, 9},
	}
	for _, c := range cases {
		if got := newTaskBudget(c.roster).limit; got != c.want {
			t.Errorf("roster %d: limit = %d, want %d", c.roster, got, c.want)
		}
	}
}

// TestTaskBudgetRequestRaisesOnlyWithinCeiling covers task_budget's contract:
// a declaration raises the limit, never lowers it, and is held to the ceiling
// unless the roster already justified more (sixteen picks are sixteen picks).
func TestTaskBudgetRequestRaisesOnlyWithinCeiling(t *testing.T) {
	b := newTaskBudget(0)
	if got := b.request(6); got != 6 {
		t.Fatalf("request(6) = %d, want 6", got)
	}
	if got := b.request(2); got != 6 {
		t.Fatalf("request(2) after 6 = %d, want 6 (never lowers)", got)
	}
	if got := b.request(99); got != taskCeiling {
		t.Fatalf("request(99) = %d, want ceiling %d", got, taskCeiling)
	}

	big := newTaskBudget(12)
	if got := big.request(99); got != 12 {
		t.Fatalf("roster 12 request(99) = %d, want 12 (ceiling follows the roster)", got)
	}
}

// TestTaskBudgetRefusalTracksDroppedTitles: the advisory is built from titles
// still missing, and a later round that dispatches one clears it — otherwise a
// raised budget would leave a stale "not dispatched" note on a task that ran.
func TestTaskBudgetRefusalTracksDroppedTitles(t *testing.T) {
	b := newTaskBudget(0)
	b.refuse("beta")
	b.refuse("alpha")
	if got := b.dropped(); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("dropped = %v, want [alpha beta] (sorted)", got)
	}
	b.settled("alpha")
	if got := b.dropped(); len(got) != 1 || got[0] != "beta" {
		t.Fatalf("dropped after settle = %v, want [beta]", got)
	}
	if note := budgetWarning(b, i18n.English); !strings.Contains(note, "beta") || strings.Contains(note, "alpha") {
		t.Fatalf("warning = %q, want it to name only beta", note)
	}
	b.settled("beta")
	if note := budgetWarning(b, i18n.English); note != "" {
		t.Fatalf("warning = %q, want empty once nothing is missing", note)
	}
}

// TestBudgetRefusalNoteIsActionable: the model must be able to tell a refusal
// from a dispatch, learn the limit that refused it, and know the way out.
func TestBudgetRefusalNoteIsActionable(t *testing.T) {
	b := newTaskBudget(0)
	b.used = 3
	en := budgetRefusalNote(b, "probe opencode", i18n.English)
	for _, want := range []string{"probe opencode", "NOT dispatched", "task_budget", "3"} {
		if !strings.Contains(en, want) {
			t.Errorf("English refusal missing %q: %s", want, en)
		}
	}
	zh := budgetRefusalNote(b, "测 opencode", i18n.ChineseSimp)
	if !strings.Contains(zh, "没有派发") || !strings.Contains(zh, "task_budget") {
		t.Errorf("Chinese refusal unexpected: %s", zh)
	}
}

// TestTaskBudgetToolReportsLimitInForce: the tool answers with the limit that
// actually applies, because a model that believes it has ten slots when it has
// eight loses two tasks to the refusal this tool exists to prevent.
func TestTaskBudgetToolReportsLimitInForce(t *testing.T) {
	b := newTaskBudget(0)
	tool := (&Engine{}).taskBudgetTool(b, i18n.English)
	out, err := tool.Run(context.Background(), map[string]any{"count": float64(6)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if b.limit != 6 || !strings.Contains(out, "now 6") {
		t.Fatalf("limit = %d, out = %q; want 6 and a confirmation", b.limit, out)
	}
	out, err = tool.Run(context.Background(), map[string]any{"count": "99"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if b.limit != taskCeiling {
		t.Fatalf("limit = %d, want ceiling %d", b.limit, taskCeiling)
	}
	if !strings.Contains(out, fmt.Sprintf("ceiling is %d", taskCeiling)) {
		t.Fatalf("clamp not reported: %q", out)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"count": float64(0)}); err == nil {
		t.Fatal("count 0 accepted; want an error")
	}
}

// rosterBudgetHandler answers the first round with one task_submit per title
// and every later round with plain text, so the ask converges on a receipt.
func rosterBudgetHandler(titles []string, toolsSeen *[]string) http.HandlerFunc {
	var mu sync.Mutex
	round := 0
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		body := json.NewDecoder(r.Body)
		_ = body.Decode(&req)
		mu.Lock()
		round++
		n := round
		if toolsSeen != nil && n == 1 {
			for _, tool := range req.Tools {
				*toolsSeen = append(*toolsSeen, tool.Name)
			}
		}
		mu.Unlock()

		w.Header().Set("content-type", "application/json")
		if n > 1 {
			_ = json.NewEncoder(w).Encode(anthropicText("done"))
			return
		}
		content := make([]map[string]any, 0, len(titles))
		for i, title := range titles {
			content = append(content, map[string]any{
				"type": "tool_use", "id": fmt.Sprintf("toolu_budget_%d", i), "name": "task_submit",
				"input": map[string]any{"title": title, "target": "probe " + title, "abilities": []string{"agent:opencode"}},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": content,
			"usage":   map[string]int{"input_tokens": 8, "output_tokens": 6},
		})
	}
}

// TestRosterRaisesDispatchBudget is the regression for the reported bug: a
// conversation whose team is four agents dispatched three tasks and dropped
// the fourth (opencode) with no card, no error and no mention. One task per
// picked agent has to fit.
func TestRosterRaisesDispatchBudget(t *testing.T) {
	titles := []string{"roster-budget-claude", "roster-budget-codex", "roster-budget-dsh", "roster-budget-opencode"}
	var toolsSeen []string
	e, _ := newBoundaryTestEngine(t, rosterBudgetHandler(titles, &toolsSeen))

	res, err := e.AskTurnsScoped(context.Background(), nil, "测试我选的这几个 agent", AskScope{
		WorkDir: t.TempDir(),
		Agents:  []string{"claude_code", "codex", "deepseek_harness", "opencode"},
	}, false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := countTasksWithTitlePrefix(t, e, "roster-budget-"); got != len(titles) {
		t.Fatalf("dispatched %d tasks, want %d (the roster is the budget)", got, len(titles))
	}
	// The receipt names the last dispatch, so a surface that files one turn
	// from it shows one card over a board of four. Dispatched is what lets the
	// panel write a turn per task instead.
	if len(res.Dispatched) != len(titles) {
		t.Fatalf("Dispatched = %d rows, want %d", len(res.Dispatched), len(titles))
	}
	for i, d := range res.Dispatched {
		if d.TaskTitle != titles[i] {
			t.Fatalf("Dispatched[%d] = %q, want %q (dispatch order)", i, d.TaskTitle, titles[i])
		}
		if d.TaskID == "" {
			t.Fatalf("Dispatched[%d] has no id", i)
		}
	}
	if res.Warning != "" {
		t.Fatalf("warning = %q, want none when every pick was dispatched", res.Warning)
	}
	offered := false
	for _, name := range toolsSeen {
		if name == "task_budget" {
			offered = true
		}
	}
	if !offered {
		t.Fatalf("task_budget not offered; tools = %v", toolsSeen)
	}
}

// TestBudgetRetryDoesNotDuplicate: a refusal teaches the model to raise the
// limit and re-send, and models re-send the whole batch. The tasks that already
// went out must come back as their own ids, not run a second time.
func TestBudgetRetryDoesNotDuplicate(t *testing.T) {
	titles := []string{"retry-a", "retry-b", "retry-c", "retry-d"}
	var mu sync.Mutex
	round := 0
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		round++
		n := round
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		submits := func(names []string) []map[string]any {
			out := make([]map[string]any, 0, len(names))
			for i, name := range names {
				out = append(out, map[string]any{
					"type": "tool_use", "id": fmt.Sprintf("toolu_retry_%d_%d", n, i), "name": "task_submit",
					"input": map[string]any{"title": name, "target": "probe " + name, "abilities": []string{"agent:opencode"}},
				})
			}
			return out
		}
		var content []map[string]any
		switch n {
		case 1:
			content = submits(titles) // 4 into a budget of 3: the fourth is refused
		case 2:
			content = append([]map[string]any{{
				"type": "tool_use", "id": "toolu_retry_budget", "name": "task_budget",
				"input": map[string]any{"count": 4, "reason": "four probes were asked for"},
			}}, submits(titles)...)
		default:
			_ = json.NewEncoder(w).Encode(anthropicText("done"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": content,
			"usage":   map[string]int{"input_tokens": 8, "output_tokens": 6},
		})
	})

	res, err := e.AskTurnsScoped(context.Background(), nil, "测试这几个项目", AskScope{WorkDir: t.TempDir()}, false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := countTasksWithTitlePrefix(t, e, "retry-"); got != len(titles) {
		t.Fatalf("dispatched %d tasks, want %d (re-sent batch must not duplicate)", got, len(titles))
	}
	if res.Warning != "" {
		t.Fatalf("warning = %q, want none: the retry covered the refused task", res.Warning)
	}
}

// TestBudgetTruncationIsVisible: when the round really cannot fit everything,
// the user is told which tasks did not go out. Silence was the bug — three
// cards looked like the whole team had been tested.
func TestBudgetTruncationIsVisible(t *testing.T) {
	titles := []string{"capped-a", "capped-b", "capped-c", "capped-d"}
	e, _ := newBoundaryTestEngine(t, rosterBudgetHandler(titles, nil))

	// No roster: the limit stays at the floor, so the fourth dispatch of a
	// four-way fan-out is the one that has to be reported rather than lost.
	res, err := e.AskTurnsScoped(context.Background(), nil, "测试这几个项目", AskScope{
		WorkDir: t.TempDir(),
	}, false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if got := countTasksWithTitlePrefix(t, e, "capped-"); got != defaultTaskBudget {
		t.Fatalf("dispatched %d tasks, want %d", got, defaultTaskBudget)
	}
	if res.Warning == "" {
		t.Fatal("no warning: a task was dropped without a word")
	}
	if !strings.Contains(res.Warning, "capped-d") {
		t.Errorf("warning %q does not name the dropped task", res.Warning)
	}
	for _, ran := range []string{"capped-a", "capped-b", "capped-c"} {
		if strings.Contains(res.Warning, ran) {
			t.Errorf("warning %q names a task that did run", res.Warning)
		}
	}
}
