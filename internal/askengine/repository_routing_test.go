// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

func TestRepositoryInvestigationRoutingIsNarrow(t *testing.T) {
	for _, prompt := range []string{
		"看一下这个项目，深入分析一下，探索一下代码库",
		"这个项目有什么问题呢？分析一下",
		"不是，我需要你实际调研代码库",
		"Thoroughly investigate this repository",
	} {
		if !needsRepositoryInvestigation(prompt) {
			t.Errorf("should delegate investigation: %q", prompt)
		}
	}
	for _, prompt := range []string{"看一下这个项目", "什么是 goroutine？", "请解释 project management", "这个项目中的函数是什么？"} {
		if needsRepositoryInvestigation(prompt) {
			t.Errorf("should not force a deep harness task: %q", prompt)
		}
	}
	if !repositoryInvestigationReadOnly("深入分析一下这个代码库") || repositoryInvestigationReadOnly("深入分析并修复这个代码库") {
		t.Fatal("analysis-only should be read-only, analysis plus fix should not")
	}
	for _, prompt := range []string{"深入分析代码库实现有哪些问题", "Review this repository implementation", "深入分析代码库，不要修改", "Review this repository, no edits", "深入分析代码库并给出优化方案", "Review this repository build pipeline", "Analyze this repository and recommend how to fix it"} {
		if !repositoryInvestigationReadOnly(prompt) {
			t.Errorf("analysis should remain read-only: %s", prompt)
		}
	}
}

// Four batches of directory listing cannot support a completed code review.
// The first bogus answer gets one dispatch correction; another is surfaced as
// incomplete rather than passed to the user as a deep investigation.
func TestDeepRepositoryAskRejectsShallowConclusion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		n := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n <= 4 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"list-1","type":"function","function":{"name":"workspace_list","arguments":"{\"path\":\".\"}"}}]}}]}`)
			return
		}
		writeAnswer(w, "I fully reviewed the implementation and tests")
	}))
	defer srv.Close()

	e := newGuardTestEngine(t, srv, entry.NewRegistry(), 200000)
	res, err := e.AskTurnsScoped(context.Background(), nil, "看一下这个项目，深入分析一下，探索一下代码库", AskScope{WorkDir: root}, true, StreamCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != "answer" || !strings.Contains(res.Answer, "尚未完成") || strings.Contains(res.Answer, "I fully reviewed") {
		t.Fatalf("shallow review accepted: %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 6 || !strings.Contains(requests[4], "深入调研") || !strings.Contains(requests[5], "请派发 coding harness") {
		t.Fatalf("routing reminder not applied after shallow reads; requests=%d, final=%.500s", len(requests), requests[len(requests)-1])
	}
}

func TestWorkspaceRepositoryQuestionSkipsNoToolsFastPath(t *testing.T) {
	root := t.TempDir()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		writeAnswer(w, "A short, unverified overview")
	}))
	defer srv.Close()
	e := newGuardTestEngine(t, srv, entry.NewRegistry(), 200000)
	_, err := e.AskTurnsScoped(context.Background(), nil, "介绍一下这个项目", AskScope{WorkDir: root}, true, StreamCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "workspace_read") || strings.Contains(body, "智能技术助手") {
		t.Errorf("workspace question used no-tools fast path: %.500s", body)
	}
}

// Both dispatch channels must survive the guard and carry the read-only
// boundary and the user's chosen agent roster into the persisted task.
func TestDeepRepositoryDispatchPreservesReadOnlyAndRoster(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "native"}[native], func(t *testing.T) {
			e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
				var response map[string]any
				if native {
					response = map[string]any{"content": []map[string]any{{
						"type": "tool_use", "id": "investigate", "name": "task_submit",
						"input": map[string]any{"title": "investigate", "target": "Read implementation and tests; report file and line evidence", "abilities": []string{"agent:codex"}},
					}}}
				} else {
					response = anthropicText(`{"kind":"task","task":{"title":"investigate","context_type":"file","requires":{"abilities":["agent:codex"]},"spec":{"target":"Read implementation and tests; report file and line evidence"},"complexity":0.5,"risk":"low"}}`)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(response)
			})
			res, err := e.AskTurnsScoped(context.Background(), nil, "深入分析一下这个代码库", AskScope{WorkDir: t.TempDir(), Agents: []string{"codex"}, PermissionMode: defense.ModeBypass}, true, StreamCallbacks{})
			if err != nil || res == nil || res.TaskID == "" {
				t.Fatalf("investigation did not dispatch: res=%+v err=%v", res, err)
			}
			task, err := core.NewTaskStore(e.db, nil).Get(context.Background(), res.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			var spec entry.TaskSpecDetail
			if err := json.Unmarshal([]byte(task.SpecJSON), &spec); err != nil {
				t.Fatal(err)
			}
			if spec.Kind != entry.TaskInvestigation || spec.PermissionMode != defense.ModePlan || len(spec.AllowAgents) != 1 || spec.AllowAgents[0] != "codex" {
				t.Fatalf("lost permission or roster: %+v", spec)
			}
		})
	}
}

// Structured classification works without the legacy "repository/deep" words.
func TestTypedInvestigationDispatchWithoutRepositoryKeywords(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "native"}[native], func(t *testing.T) {
			e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
				var response map[string]any
				if native {
					response = map[string]any{"content": []map[string]any{{"type": "tool_use", "id": "typed", "name": "task_submit", "input": map[string]any{
						"title": "trace", "target": "trace worker callers", "abilities": []string{"agent:codex"}, "task_kind": "investigation",
					}}}}
				} else {
					response = anthropicText(`{"kind":"task","task":{"title":"trace","context_type":"file","requires":{"abilities":["agent:codex"]},"spec":{"task_kind":"investigation","target":"trace worker callers"},"complexity":0.5,"risk":"low"}}`)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(response)
			})
			res, err := e.AskTurnsScoped(context.Background(), []entry.Turn{{Role: "user", Content: "Focus on worker.go"}}, "Trace worker.go callers", AskScope{WorkDir: t.TempDir(), Agents: []string{"codex"}, PermissionMode: defense.ModeBypass}, true, StreamCallbacks{})
			if err != nil || res == nil || res.TaskID == "" {
				t.Fatalf("dispatch: %+v %v", res, err)
			}
			task, err := core.NewTaskStore(e.db, nil).Get(context.Background(), res.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			var spec entry.TaskSpecDetail
			if err := json.Unmarshal([]byte(task.SpecJSON), &spec); err != nil {
				t.Fatal(err)
			}
			if spec.Kind != entry.TaskInvestigation || spec.PermissionMode != defense.ModePlan {
				t.Fatalf("lost typed contract: %+v", spec)
			}
		})
	}
}

func TestDeepRepositoryRejectedAnswerNeverStreams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeAnswer(w, "unsupported finding") }))
	defer srv.Close()
	e := newGuardTestEngine(t, srv, entry.NewRegistry(), 200000)
	var deltas strings.Builder
	res, err := e.AskTurnsScoped(context.Background(), nil, "深入分析这个代码库", AskScope{WorkDir: t.TempDir()}, true, StreamCallbacks{OnDelta: func(s string) { deltas.WriteString(s) }})
	if err != nil || res == nil || !strings.Contains(res.Answer, "尚未完成") {
		t.Fatalf("result: %+v %v", res, err)
	}
	if strings.Contains(deltas.String(), "unsupported finding") {
		t.Fatalf("unaccepted conclusion streamed: %q", deltas.String())
	}
}

func TestRepositoryRequestResolvesFollowupsAndSymbols(t *testing.T) {
	history := []entry.Turn{{Role: "user", Content: "请分析这个代码库"}, {Role: "assistant", Content: "overview"}}
	for _, tc := range []struct {
		prompt          string
		history         []entry.Turn
		wantQ, wantDeep bool
	}{
		{"解释 FastTriage 如何处理历史记录", nil, true, false},
		{"explain fast_triage behavior", nil, true, false},
		{"检查 worker.go 的调用者", nil, true, false},
		{"不对，要全面深入检查，不要只读目录", history, true, true},
		{"continue the thorough review", history, true, true},
		{"全面深入分析这个代码库，不要修改", nil, true, true},
		{"什么是 mutex？", history, false, false},
		{"解释分布式系统", nil, false, false},
		{"你好", history, false, false},
	} {
		q, deep := repositoryRequest(tc.prompt, tc.history, true)
		if q != tc.wantQ || deep != tc.wantDeep {
			t.Errorf("%q: question=%v deep=%v want=%v/%v", tc.prompt, q, deep, tc.wantQ, tc.wantDeep)
		}
	}
}

func TestRepositorySymbolAnswerRequiresActualSourceRead(t *testing.T) {
	for _, read := range []bool{false, true} {
		t.Run(map[bool]string{false: "no evidence", true: "source evidence"}[read], func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "triage.go"), []byte("package test\nfunc FastTriage() bool { return true }\n"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if read && calls == 1 {
					_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"r1","type":"function","function":{"name":"workspace_read","arguments":"{\"path\":\"triage.go\"}"}}]}}]}`)
					return
				}
				writeAnswer(w, "FastTriage returns true")
			}))
			defer srv.Close()
			e := newGuardTestEngine(t, srv, entry.NewRegistry(), 200000)
			res, err := e.AskTurnsScoped(context.Background(), nil, "解释 FastTriage 的实现", AskScope{WorkDir: root}, false, StreamCallbacks{})
			if err != nil {
				t.Fatal(err)
			}
			if read && res.Answer != "FastTriage returns true" {
				t.Fatalf("read evidence not accepted: %+v", res)
			}
			if !read && (res.Answer == "FastTriage returns true" || !strings.Contains(res.Answer, "尚未读取")) {
				t.Fatalf("shallow answer accepted: %+v", res)
			}
		})
	}
}

func TestDeepRepositorySideAndFollowupRejectShallowConclusion(t *testing.T) {
	for _, side := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeAnswer(w, "shallow audit complete") }))
		e := newGuardTestEngine(t, srv, entry.NewRegistry(), 200000)
		history := []entry.Turn{{Role: "user", Content: "请分析这个代码库"}}
		res, err := e.AskTurnsScoped(context.Background(), history, "不对，要全面深入检查，不要只读目录", AskScope{WorkDir: t.TempDir(), Side: side}, false, StreamCallbacks{})
		srv.Close()
		if err != nil || !strings.Contains(res.Answer, "尚未完成") {
			t.Fatalf("side=%v shallow result=%+v err=%v", side, res, err)
		}
	}
}

// Three real queue receipts plus a refused fourth submission keep lastTask in
// the loop. Neither an ordinary answer nor budget-exhaustion convergence may
// re-label those receipts as a completed repository investigation.
func TestDeepRepositoryQueuedReceiptCannotBecomeCompletedAnswer(t *testing.T) {
	for _, exhaust := range []bool{false, true} {
		t.Run(fmt.Sprintf("exhaust=%v", exhaust), func(t *testing.T) {
			var calls atomic.Int32
			e, _ := newBoundaryTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				var response map[string]any
				if n == 1 {
					var tools []map[string]any
					for i := 0; i < 4; i++ {
						tools = append(tools, map[string]any{"type": "tool_use", "id": fmt.Sprintf("review-%d", i), "name": "task_submit", "input": map[string]any{
							"title": fmt.Sprintf("review-%d", i), "target": fmt.Sprintf("inspect implementation section %d", i), "abilities": []string{"agent:codex"},
						}})
					}
					response = map[string]any{"content": tools}
				} else if exhaust && n <= 6 {
					response = map[string]any{"content": []map[string]any{{"type": "tool_use", "id": fmt.Sprintf("list-%d", n), "name": "workspace_list", "input": map[string]any{"path": "."}}}}
				} else {
					response = anthropicText("fabricated completed repository audit")
				}
				_ = json.NewEncoder(w).Encode(response)
			})
			var deltas strings.Builder
			res, err := e.AskTurnsScoped(context.Background(), nil, "深入分析这个代码库", AskScope{WorkDir: t.TempDir()}, true, StreamCallbacks{OnDelta: func(s string) { deltas.WriteString(s) }})
			if err != nil || res == nil || res.TaskID == "" || res.TaskState != core.StateQueued || res.OK {
				t.Fatalf("lost queued receipt: %+v err=%v", res, err)
			}
			if strings.Contains(res.Answer+res.Report+deltas.String(), "fabricated completed") {
				t.Fatalf("unverified completion escaped: %+v stream=%q", res, deltas.String())
			}
			want := int32(2)
			if exhaust {
				want = 6
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d, want %d; must not summarize a queued investigation", calls.Load(), want)
			}
		})
	}
}

func TestIncompleteRepositoryTaskStates(t *testing.T) {
	for _, state := range []string{core.StateQueued, core.StateFailed, core.StateReview, core.StateDone} {
		for _, ok := range []bool{false, true} {
			task := &Result{TaskState: state, OK: ok}
			want := state != core.StateDone || !ok
			if incompleteRepositoryTask(true, task) != want || incompleteRepositoryTask(false, task) {
				t.Fatalf("state=%s ok=%v", state, ok)
			}
		}
	}
	if incompleteRepositoryTask(true, nil) {
		t.Fatal("nil task has a separate missing-dispatch guard")
	}
}
