// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
)

// sideTaskJSON is a well-formed task directive, the shape a model emits when it
// decides a request needs doing rather than answering.
const sideTaskJSON = `{"kind":"task","task":{"title":"修改配置文件","context_type":"file","requires":{"abilities":["agent:codex"]},"spec":{"scope":"config.yaml","target":"把 config.yaml 里的端口改成 8080","constraints":[],"success_definition":"端口已修改"},"complexity":0.3,"risk":"low","resource_profile":{"cpu":1,"ram_gb":1,"gpu_vram_gb":0,"duration_hint":"short"}}}`

// TestSideAskHasFullToolFace pins the channel's capability: a side ask holds
// the same dispatch and workspace tools as the thread. Isolation is the
// transcript, not a crippled tool set — a channel that can only look cannot
// finish the small piece of work the user asked for.
func TestSideAskHasFullToolFace(t *testing.T) {
	names := make(chan []string, 1)
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		out := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			out = append(out, tool.Name)
		}
		select {
		case names <- out:
		default:
		}
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText("这是一个侧边回答。"))
		_, _ = w.Write(b)
	})

	res, err := e.AskTurnsScoped(context.Background(), nil, "这个项目是做什么的", AskScope{Side: true, WorkDir: t.TempDir()}, false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("side ask: %v", err)
	}
	if res == nil || res.Kind != "answer" {
		t.Fatalf("side ask should answer, got %+v", res)
	}
	seen := map[string]bool{}
	for _, name := range <-names {
		seen[name] = true
	}
	if len(seen) == 0 {
		t.Fatal("the side ask sent no tool list")
	}
	for _, want := range []string{entry.ToolNameTaskSubmit, "task_budget", "workspace_read", "system_status"} {
		if !seen[want] {
			t.Errorf("side ask is missing tool %q; tools seen: %v", want, seen)
		}
	}
}

// TestSideAskDispatchesTaskClassification is the new contract: a side ask that
// lands on a task submits it instead of returning a fixed refusal. The work is
// real, records a task id, and passes the same scheduler/approval path.
func TestSideAskDispatchesTaskClassification(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText(sideTaskJSON))
		_, _ = w.Write(b)
	})

	res, err := e.AskTurnsScoped(context.Background(), nil, "把 config.yaml 的端口改成 8080", AskScope{Side: true}, false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("side ask: %v", err)
	}
	if res == nil {
		t.Fatal("side ask returned no result")
	}
	if res.TaskID == "" {
		t.Fatalf("side task was not dispatched: %+v", res)
	}
	if res.Kind != "task" {
		t.Errorf("side dispatch kind = %q, want task", res.Kind)
	}
}

// TestSideScopeDispatchesAtTheCreationPoint calls submitTask directly — the way
// the round-exhaustion path does after the tool loop gives up — and asserts a
// side scope is no longer vetoed at the single creation point.
func TestSideScopeDispatchesAtTheCreationPoint(t *testing.T) {
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText("ok"))
		_, _ = w.Write(b)
	})
	spec := &entry.TaskSpec{Title: "改配置", Spec: entry.TaskSpecDetail{Target: "把端口改成 8080"}}
	res := e.submitTask(context.Background(), spec, "把端口改成 8080", false,
		AskScope{Side: true}, "", StreamCallbacks{}, false)
	if res == nil {
		t.Fatal("side submitTask returned nothing")
	}
	if res.TaskID == "" {
		t.Errorf("a side scope was refused at the creation point: %+v", res)
	}
}

// TestSideAskPromptMarksHistoryReferenceOnly checks the transcript boundary at
// the wire: the system prompt says the replayed thread is reference material,
// and the transcript carries a boundary marker before the side question.
// Without either, a model reading a long thread resumes the work it finds there.
func TestSideAskPromptMarksHistoryReferenceOnly(t *testing.T) {
	type captured struct {
		system   string
		messages []string
	}
	got := make(chan captured, 1)
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			System   json.RawMessage `json:"system"`
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		c := captured{system: string(req.System)}
		for _, m := range req.Messages {
			c.messages = append(c.messages, string(m.Content))
		}
		select {
		case got <- c:
		default:
		}
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText("好的。"))
		_, _ = w.Write(b)
	})

	history := []entry.Turn{
		{Role: "user", Content: "把服务重构成三层架构"},
		{Role: "assistant", Content: "好的，我先修改 storage 层。"},
	}
	sideTurns := []entry.Turn{
		{Role: "user", Content: "storage 层现在长什么样"},
		{Role: "assistant", Content: "两个文件，一个负责打开连接。"},
	}
	if _, err := e.AskTurnsScoped(context.Background(), history, "这三层分别是什么含义", AskScope{Side: true, SideTurns: sideTurns}, false, StreamCallbacks{}); err != nil {
		t.Fatalf("side ask: %v", err)
	}
	c := <-got
	if !strings.Contains(c.system, "Side Channel (Separate Transcript)") {
		t.Errorf("system prompt is missing the side section:\n%s", c.system)
	}
	boundary := -1
	for i, m := range c.messages {
		if strings.Contains(m, "Side channel boundary") || strings.Contains(m, "侧边对话边界") {
			boundary = i
		}
	}
	if boundary < 0 {
		t.Fatalf("no boundary marker in the replayed transcript: %v", c.messages)
	}
	// Everything above the marker is the main thread; the channel's own turns
	// sit at or below it. (Adjacent same-role turns are folded into one wire
	// message, so the marker and the first side question arrive together.)
	if !strings.Contains(c.messages[0], "三层架构") {
		t.Errorf("the main-thread history is not above the boundary: %v", c.messages)
	}
	var sawSideTurn bool
	for _, m := range c.messages[boundary:] {
		if strings.Contains(m, "storage 层现在长什么样") {
			sawSideTurn = true
		}
	}
	if !sawSideTurn {
		t.Errorf("the channel's own turns are not at or below the boundary: %v", c.messages)
	}
	for _, m := range c.messages[:boundary] {
		if strings.Contains(m, "storage 层现在长什么样") {
			t.Errorf("the channel's own turn leaked above the boundary: %v", c.messages)
		}
	}
}

// TestSideAskNeverTakesFastPath pins the channel's grounding guarantee: a
// conceptual question that the cheap triage would answer with no tools still
// runs the full pipeline, so the model always holds the dispatch and
// observation tools. The fast path's generic prompt has no side boundary and
// no tool roster; taking it would make "did it finish?" unanswerable.
func TestSideAskNeverTakesFastPath(t *testing.T) {
	toolsSeen := make(chan int, 1)
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		select {
		case toolsSeen <- len(req.Tools):
		default:
		}
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText("这个问题可以直接解释。"))
		_, _ = w.Write(b)
	})
	res, err := e.AskTurnsScoped(context.Background(), nil, "什么是依赖注入？", AskScope{Side: true, WorkDir: t.TempDir()}, false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("side ask: %v", err)
	}
	if res == nil || res.Kind != "answer" {
		t.Fatalf("side ask should answer, got %+v", res)
	}
	if n := <-toolsSeen; n == 0 {
		t.Error("a side ask took the no-tools fast path")
	}
}
