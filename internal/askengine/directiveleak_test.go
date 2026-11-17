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
)

// leakedDirective mirrors the 2026-11-17 reply: a task envelope whose tail is
// one brace short and whose spec fields ended up inside objectives. The runtime
// used to return it as the answer — the chat pane showed the JSON, no task
// existed, and the turn looked like a crash.
const leakedDirective = `{"kind":"task","task":{"title":"新建鹈鹕骑车单文件 HTML 插画","context_type":"file","requires":{"abilities":["agent:codex"]},"spec":{"objectives":{"version":1,"revision":1,"dimensions":[{"dimension":"deliverable","weight":1,"priority":"MAX","requirement":"新增一个 HTML 文件","reason":"用户要求产出一个 html","criteria":["文件存在"]}],"task_kind":"edit","scope":"pelican.html"}},"complexity":0.4,"risk":"low","resource_profile":{"cpu":1,"ram_gb":2,"gpu_vram_gb":0,"duration_hint":"short"}}`

const dispatchedDirective = `{"kind":"task","task":{"title":"新建鹈鹕骑车单文件 HTML 插画","context_type":"file","requires":{"abilities":["agent:codex"]},"spec":{"objectives":{"version":1,"revision":1,"dimensions":[{"dimension":"deliverable","weight":1,"priority":"MAX","requirement":"新增一个 HTML 文件","reason":"用户要求产出一个 html","criteria":["文件存在"]}]},"task_kind":"edit","scope":"pelican.html"},"complexity":0.4,"risk":"low","resource_profile":{"cpu":1,"ram_gb":2,"gpu_vram_gb":0,"duration_hint":"short"}}}`

// TestLeakedDirectiveIsRetriedThenDispatched pins the whole recovery chain: a
// directive written into the message body is handed back once with the protocol
// restated, the retried turn dispatches for real, and the envelope never becomes
// the user's answer.
func TestLeakedDirectiveIsRetriedThenDispatched(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	reminded := false
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls++
		if strings.Contains(string(body), "Do not print task, plan or tool_call JSON") {
			reminded = true
		}
		retry := reminded
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		payload := leakedDirective
		if retry {
			payload = dispatchedDirective
		}
		b, _ := json.Marshal(anthropicText(payload))
		_, _ = w.Write(b)
	})

	res, err := e.Ask(context.Background(), "我需要你写一个鹈鹕骑车，在这个文件夹里，一个 html", false)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	mu.Lock()
	gotCalls, gotReminded := calls, reminded
	mu.Unlock()
	if !gotReminded {
		t.Fatalf("the leaked directive was not retried with the protocol reminder")
	}
	if gotCalls < 2 {
		t.Fatalf("calls = %d, want at least the retry", gotCalls)
	}
	if strings.Contains(res.Answer, `"kind":"task"`) {
		t.Fatalf("the envelope became the answer: %q", res.Answer)
	}
	if res.Kind != "task" || res.TaskID == "" {
		t.Fatalf("res = %+v, want a dispatched task", res)
	}
	queued, err := core.NewTaskStore(e.db, nil).ListByState(context.Background(), core.StateQueued)
	if err != nil || len(queued) == 0 {
		t.Fatalf("no queued task in the store: %v (%d)", err, len(queued))
	}
}
