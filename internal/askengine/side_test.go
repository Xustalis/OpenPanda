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

// TestSideAskHasNoDispatchTool pins the tool face of a side ask: the entry
// model must never see task_submit or task_budget, and every tool it does see
// is on the read-only allow-list. A dispatch tool that slipped through would
// make /side a silent way to start work the user believed was parked.
func TestSideAskHasNoDispatchTool(t *testing.T) {
	var mu = make(chan []string, 1)
	e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		names := make([]string, 0, len(req.Tools))
		for _, tool := range req.Tools {
			names = append(names, tool.Name)
		}
		select {
		case mu <- names:
		default:
		}
		w.Header().Set("content-type", "application/json")
		b, _ := json.Marshal(anthropicText("这是一个只读的侧边回答。"))
		_, _ = w.Write(b)
	})

	res, err := e.AskTurnsScoped(context.Background(), nil, "这个项目是做什么的", AskScope{Side: true}, false, StreamCallbacks{})
	if err != nil {
		t.Fatalf("side ask: %v", err)
	}
	if res == nil || res.Kind != "answer" {
		t.Fatalf("side ask should answer, got %+v", res)
	}
	names := <-mu
	if len(names) == 0 {
		t.Fatal("the side ask sent no tool list; the assertion below would be vacuous")
	}
	for _, name := range names {
		if name == entry.ToolNameTaskSubmit || name == "task_budget" {
			t.Errorf("side ask exposed dispatch tool %q", name)
		}
		if !sideToolAllowed(name) {
			t.Errorf("side ask exposed %q, which is not on the read-only allow-list", name)
		}
	}
	// The allow-list is what makes the check above meaningful: a side registry
	// that ended up empty would pass it while answering nothing.
	var sawListTool bool
	for _, name := range names {
		if name == "system_status" || name == "taskq_list" {
			sawListTool = true
		}
	}
	if !sawListTool {
		t.Errorf("side ask kept no observation tool; tools seen: %v", names)
	}
}

// TestSideAskRefusesTaskClassification is the second boundary: the prompt asked
// the model not to dispatch and the tool face has no dispatch tool, but
// classification is the model's call. A side ask that still lands on a task
// must come back as the fixed refusal, not as a running task.
func TestSideAskRefusesTaskClassification(t *testing.T) {
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
	if res.Kind != "answer" {
		t.Fatalf("a side ask classified as a task must answer, got kind %q", res.Kind)
	}
	if res.TaskID != "" {
		t.Errorf("side ask dispatched task %q", res.TaskID)
	}
	if !strings.Contains(res.Answer, "侧边") && !strings.Contains(res.Answer, "side channel") {
		t.Errorf("side refusal should name the channel, got %q", res.Answer)
	}
}

// TestSideAskPromptMarksHistoryReferenceOnly checks the third boundary at the
// wire: the system prompt says the replayed thread is reference material, and
// the transcript carries a boundary marker before the side question. Without
// either, a model reading a long thread resumes the work it finds there.
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
	if !strings.Contains(c.system, "Side Channel (Reference Only)") {
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
	// Swapping the two would replay the channel's history as reference material
	// and the thread's as this channel's own — neither would then be continued
	// correctly.
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

// TestSideToolAllowList is the allow-list itself: observation stays, every
// mutation and every dispatch path is out.
func TestSideToolAllowList(t *testing.T) {
	allowed := []string{
		"system_status", "card_list", "card_show", "taskq_list", "taskq_show",
		"project_list", "memory_read", "skill_list", "skill_show",
		"skill_search", "time_now", "weather_get",
		"web_fetch", "web_search", "reminder_list",
	}
	for _, name := range allowed {
		if !sideToolAllowed(name) {
			t.Errorf("read-only tool %q should be allowed", name)
		}
	}
	denied := []string{
		entry.ToolNameTaskSubmit, "task_budget",
		"memory_add", "memory_replace", "memory_remove",
		"taskq_cancel", "taskq_priority", "taskq_move", "taskq_approve",
		"taskq_reject", "taskq_clear",
		"card_native_add", "card_native_remove", "card_agent_add",
		"card_agent_remove", "card_agent_set", "card_manual_add",
		"card_manual_remove", "card_rescan",
		"project_create", "project_enter", "project_exit", "node_remove",
		"reminder_set", "reminder_delete", "skill_install", "skill_discover",
	}
	for _, name := range denied {
		if sideToolAllowed(name) {
			t.Errorf("mutating tool %q must not be on the side allow-list", name)
		}
	}
}

// TestSideScopeIsRefusedAtTheCreationPoint calls submitTask directly — the way
// the round-exhaustion path does after the tool loop gives up — and asserts the
// guard lives at the creation point rather than only in the per-round
// classification branch. The per-round branch covered asks that converge on a
// task; an ask that spends every round on tools reached submitTask unchecked.
func TestSideScopeIsRefusedAtTheCreationPoint(t *testing.T) {
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
	if res.TaskID != "" {
		t.Errorf("a side scope dispatched task %q", res.TaskID)
	}
	if res.Kind != "answer" || !strings.Contains(res.Answer, "侧边") {
		t.Errorf("result = %+v, want the fixed refusal", res)
	}
}
