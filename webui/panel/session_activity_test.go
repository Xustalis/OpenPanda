// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/activity"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func TestSessionActivityAndThoughtNeverReplay(t *testing.T) {
	for _, side := range []bool{false, true} {
		t.Run(fmt.Sprint(side), func(t *testing.T) {
			h, store := newSideHandler(t)
			sess, _ := store.Create("replay")
			records := []activity.Record{{ID: "tool", Name: "workspace_read", Status: "success", Output: "DISPLAY_ONLY_TOOL_OUTPUT"}}
			_, _ = store.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "MAIN_REPLAY_TEXT", Thought: "DISPLAY_ONLY_MAIN_THOUGHT", Activities: records})
			_, _ = store.AppendSideTurn(sess.ID, sessions.SideTurn{Role: "assistant", Text: "SIDE_REPLAY_TEXT", Thought: "DISPLAY_ONLY_SIDE_THOUGHT", Activities: records})
			var request string
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				request = string(body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer model.Close()
			if err := h.engine.SetModel(config.ModelConfig{APIType: config.APITypeOpenAI, BaseURL: model.URL, Model: "test", NoAuth: true}); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fmt.Sprintf("{\"prompt\":\"继续回答\",\"side\":%v}", side)))
			req.SetPathValue("id", sess.ID)
			h.sessionAsk(httptest.NewRecorder(), req)
			if !strings.Contains(request, "MAIN_REPLAY_TEXT") || strings.Contains(request, "DISPLAY_ONLY_") {
				t.Fatalf("display fields leaked or conversation lost: %s", request)
			}
			if side && !strings.Contains(request, "SIDE_REPLAY_TEXT") {
				t.Fatal("side replay text missing")
			}
		})
	}
}

func TestSessionActivityLifecyclePersists(t *testing.T) {
	for _, side := range []bool{false, true} {
		for _, outcome := range []string{"success", "failure", "cancelled"} {
			t.Run(fmt.Sprintf("side=%v/%s", side, outcome), func(t *testing.T) {
				h, store := newSideHandler(t)
				sess, _ := store.Create("lifecycle")
				var calls atomic.Int32
				waiting := make(chan struct{})
				model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if calls.Add(1) == 1 {
						chunk := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
							"reasoning_content": "display reasoning",
							"tool_calls":        []any{map[string]any{"index": 0, "id": "clock", "type": "function", "function": map[string]any{"name": "time_now", "arguments": "{}"}}},
						}, "finish_reason": "tool_calls"}}}
						data, _ := json.Marshal(chunk)
						_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
						return
					}
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}]}\n\n")
					w.(http.Flusher).Flush()
					switch outcome {
					case "failure":
						_, _ = io.WriteString(w, "data: {\"error\":{\"type\":\"invalid_api_key\",\"message\":\"credential rejected\"}}\n\n")
					case "cancelled":
						close(waiting)
						<-r.Context().Done()
					default:
						_, _ = io.WriteString(w, "data: [DONE]\n\n")
					}
				}))
				defer model.Close()
				if err := h.engine.SetModel(config.ModelConfig{APIType: config.APITypeOpenAI, BaseURL: model.URL, Model: "test", NoAuth: true}); err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fmt.Sprintf("{\"prompt\":\"请使用工具查询当前时间\",\"side\":%v}", side)))
				req.SetPathValue("id", sess.ID)
				done := make(chan struct{})
				go func() { defer close(done); h.sessionAsk(httptest.NewRecorder(), req) }()
				if outcome == "cancelled" {
					select {
					case <-waiting:
					case <-time.After(5 * time.Second):
						t.Fatal("provider did not reach cancellable answer")
					}
					// Wait for the periodic live checkpoint, then cancel by identity.
					deadline := time.Now().Add(4 * time.Second)
					checkpointed := false
					for time.Now().Before(deadline) {
						current, _ := store.Get(sess.ID)
						op := current.Operation
						if side {
							op = current.ActiveSideChat().Operation
						}
						if len(op.Activities) > 0 && op.Thought == "display reasoning" {
							checkpointed = true
							break
						}
						time.Sleep(20 * time.Millisecond)
					}
					if !checkpointed {
						t.Error("periodic operation checkpoint lost activity or thought")
					}
					key := sess.ID
					if side {
						key += sideOperationSuffix
					}
					h.askMu.Lock()
					op := h.activeAsks[key]
					h.askMu.Unlock()
					if !h.cancelSessionAsk(key, op.operationID) {
						t.Fatal("cannot stop active ask")
					}
				}
				select {
				case <-done:
				case <-time.After(8 * time.Second):
					t.Fatal("ask did not settle")
				}
				current, _ := store.Get(sess.ID)
				op := current.Operation
				var turn sessions.Turn
				if side {
					chat := current.ActiveSideChat()
					op = chat.Operation
					last := chat.Turns[len(chat.Turns)-1]
					turn = sessions.Turn{Thought: last.Thought, Activities: last.Activities}
				} else {
					turn = current.Turns[len(current.Turns)-1]
				}
				if op.Status == "running" || len(op.Activities) == 0 || op.Thought != "display reasoning" || len(turn.Activities) == 0 || turn.Thought != op.Thought {
					t.Fatalf("terminal evidence missing: operation=%+v turn=%+v", op, turn)
				}
				for _, record := range op.Activities {
					if record.Status == "running" {
						t.Fatal("terminal operation retains running activity")
					}
				}
			})
		}
	}
}
