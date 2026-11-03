// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

const clarifyJSON = `{"kind":"clarify","clarify":{"plan":"先确认范围与验证方式。","questions":[{"id":"scope","header":"范围","question":"改哪个目录？","options":[{"label":"internal","description":"核心逻辑"},{"label":"cmd","description":"命令行"}],"recommended":"internal"},{"id":"verify","header":"验证","question":"用哪个命令验证？","options":[{"label":"make test"},{"label":"make gate"}],"recommended":"make test"}]}}`

const clarifyTaskJSON = `{"kind":"task","task":{"title":"改一处代码","context_type":"file","requires":{"abilities":["agent:codex"]},"spec":{"scope":"internal","target":"改一处代码","constraints":[],"success_definition":"改完并通过检查"},"complexity":0.3,"risk":"low","resource_profile":{"cpu":1,"ram_mb":512,"duration_hint":"short"}}}`

func TestClarifyInlineAnswersContinue(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var secondBody string
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		n := calls
		if n == 2 {
			secondBody = string(body)
		}
		mu.Unlock()
		text := clarifyTaskJSON
		if n == 1 {
			text = clarifyJSON
		}
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText(text))
		_, _ = w.Write(b)
	})

	var asked int
	res, err := e.AskTurnsScoped(context.Background(), nil, "把核心逻辑改一下", AskScope{
		WorkDir: t.TempDir(),
	}, false, StreamCallbacks{
		OnQuestion: func(spec entry.ClarifySpec) ([]entry.ClarifyAnswer, bool) {
			asked++
			if len(spec.Questions) != 2 {
				t.Fatalf("questions = %d, want 2", len(spec.Questions))
			}
			return []entry.ClarifyAnswer{
				{ID: "scope", Selected: []string{"internal"}},
				{ID: "verify", Selected: []string{"make test"}},
			}, true
		},
	})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if asked != 1 {
		t.Fatalf("OnQuestion called %d times, want 1", asked)
	}
	if res.Kind != "task" || res.TaskID == "" {
		t.Fatalf("res = %+v, want the task created after the answers", res)
	}
	mu.Lock()
	n := calls
	body := secondBody
	mu.Unlock()
	if n < 2 {
		t.Fatalf("model called %d times, want at least 2 (clarify then task)", n)
	}
	// The answers must be in the second classification's conversation, not
	// just in the UI: that user turn is what makes the model act on them.
	if !strings.Contains(body, "make test") || !strings.Contains(body, "internal") {
		t.Fatal("answers did not reach the second classification request")
	}
}

func TestClarifyWithoutCallbackReturnsQuestion(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText(clarifyJSON))
		_, _ = w.Write(b)
	})
	res, err := e.AskTurnsScoped(context.Background(), nil, "把这个仓库改好", AskScope{
		WorkDir: t.TempDir(),
	}, false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !res.NeedsClarification || res.Clarification == nil {
		t.Fatalf("res = %+v, want NeedsClarification with the question set", res)
	}
	if len(res.Clarification.Questions) != 2 {
		t.Fatalf("questions = %d, want 2", len(res.Clarification.Questions))
	}
	if res.Answer == "" {
		t.Fatal("a headless clarify result must render its questions")
	}
}

const planConfirmTaskJSON = `{"kind":"task","task":{"title":"重构核心","context_type":"file","requires":{"abilities":["agent:codex"]},"spec":{"scope":"internal","target":"重构 internal/core 的错误处理","constraints":[],"success_definition":"make test 通过"},"complexity":0.9,"risk":"high","resource_profile":{"cpu":1,"ram_mb":512,"duration_hint":"long"}}}`

// runPlanConfirm drives one ask over a high-complexity task and answers the
// synthesized plan card with the option at choiceIndex (0 run, 1 read-only,
// 2 cancel).
func runPlanConfirm(t *testing.T, choiceIndex int, perm string) (*Engine, *Result, int, entry.ClarifySpec) {
	t.Helper()
	var asked int
	var seen entry.ClarifySpec
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText(planConfirmTaskJSON))
		_, _ = w.Write(b)
	})
	res, err := e.AskTurnsScoped(context.Background(), nil, "重构一下核心错误处理", AskScope{
		WorkDir:        t.TempDir(),
		PermissionMode: perm,
	}, false, StreamCallbacks{
		OnQuestion: func(spec entry.ClarifySpec) ([]entry.ClarifyAnswer, bool) {
			asked++
			seen = spec
			return []entry.ClarifyAnswer{{
				ID:       "go",
				Selected: []string{spec.Questions[0].Options[choiceIndex].Label},
			}}, true
		},
	})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	return e, res, asked, seen
}

func TestPlanConfirmCancelSkipsExecution(t *testing.T) {
	e, res, asked, seen := runPlanConfirm(t, 2, "")
	if asked != 1 {
		t.Fatalf("confirm card shown %d times, want 1", asked)
	}
	if seen.Plan == "" || len(seen.Questions[0].Options) != 3 {
		t.Fatalf("confirm card = %+v, want a plan and three choices", seen)
	}
	if res.Kind != "answer" || res.TaskID != "" {
		t.Fatalf("res = %+v, want cancellation without a task", res)
	}
	_ = e
}

func TestPlanConfirmReadOnlyPinsPlanMode(t *testing.T) {
	// The thread is in manual mode; the card's read-only choice must still
	// pin plan mode for this task — the user just picked it.
	e, res, _, _ := runPlanConfirm(t, 1, "manual")
	if res.Kind != "task" || res.TaskID == "" {
		t.Fatalf("res = %+v, want the task created", res)
	}
	task, err := core.NewTaskStore(e.db, nil).Get(context.Background(), res.TaskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	var spec entry.TaskSpecDetail
	if err := json.Unmarshal([]byte(task.SpecJSON), &spec); err != nil {
		t.Fatalf("spec json %q: %v", task.SpecJSON, err)
	}
	if spec.PermissionMode != "plan" {
		t.Fatalf("permission_mode = %q, want plan", spec.PermissionMode)
	}
	if len(spec.Constraints) == 0 || !strings.Contains(spec.Constraints[len(spec.Constraints)-1], "read-only") && !strings.Contains(spec.Constraints[len(spec.Constraints)-1], "只读") {
		t.Fatalf("constraints = %v, want the read-only note", spec.Constraints)
	}
}

func TestPlanConfirmExecCreatesTask(t *testing.T) {
	_, res, _, _ := runPlanConfirm(t, 0, "")
	if res.Kind != "task" || res.TaskID == "" {
		t.Fatalf("res = %+v, want the task created after approval", res)
	}
}

func TestClarifyAsksOnlyOnce(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText(clarifyJSON))
		_, _ = w.Write(b)
	})
	var asked int
	res, err := e.AskTurnsScoped(context.Background(), nil, "改一下", AskScope{
		WorkDir: t.TempDir(),
	}, false, StreamCallbacks{
		OnQuestion: func(spec entry.ClarifySpec) ([]entry.ClarifyAnswer, bool) {
			asked++
			return []entry.ClarifyAnswer{{ID: "scope", Selected: []string{"internal"}}}, true
		},
	})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if asked != 1 {
		t.Fatalf("OnQuestion called %d times, want 1 (one clarify per ask)", asked)
	}
	if !res.NeedsClarification {
		t.Fatalf("res = %+v, want the second question handed back", res)
	}
}
