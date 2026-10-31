// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
)

// TestClarificationParkAndAnswerResume pins the single-node Q4 loop: an agent
// that ends its turn with a PANDA_QUESTION line parks the task in review, and
// an approve carrying the user's answer re-runs it with the reply folded into
// the intent — the second run sees the answer verbatim.
func TestClarificationParkAndAnswerResume(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "clarify-local", 1)
	c.SetWorkDir(t.TempDir())

	var mu sync.Mutex
	var prompts []string
	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			return commander.AgentResult{
				OK: true, ExitCode: 0,
				Result: "我已经搭好了骨架，但有个关键问题。\nPANDA_QUESTION: 要修改的配置文件是哪一个？",
			}
		}
		return commander.AgentResult{OK: true, ExitCode: 0, Result: "done with the answer"}
	})

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title: "edit config", ContextType: "command",
		Intent: "update the config file", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateReview {
		t.Fatalf("state = %s, want review (question parked)", task.State)
	}
	if result.Question != "要修改的配置文件是哪一个？" {
		t.Fatalf("question = %q", result.Question)
	}
	if result.ApprovalDisposition != string(ApprovalResumeExecution) {
		t.Fatalf("disposition = %q, want resume_execution", result.ApprovalDisposition)
	}
	// The protocol line is stripped from what the user sees.
	if strings.Contains(result.Stdout, questionMarker) {
		t.Fatalf("marker leaked into stdout: %q", result.Stdout)
	}
	stored, err := c.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if !strings.Contains(stored.ResultJSON, "要修改的配置文件是哪一个") {
		t.Fatalf("stored result missing question: %q", stored.ResultJSON)
	}

	final, res, err := c.ResumeApproved(ctx, task.TaskID, "config/app.yaml")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if final.State != StateDone {
		t.Fatalf("post-answer state = %s, want done", final.State)
	}
	if !res.OK {
		t.Fatalf("resumed result not ok: %q", res.Stderr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("agent ran %d times, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], "config/app.yaml") {
		t.Fatalf("answer missing from resumed prompt: %q", prompts[1])
	}
	if !strings.Contains(prompts[1], "clarification") {
		t.Fatalf("resumed prompt lacks clarification suffix: %q", prompts[1])
	}
}

// TestClarificationCrossDevice drives the full wire loop: the executor's agent
// parks on PANDA_QUESTION, the review propagates to the delegator with the
// question, and the delegator's approve-with-answer rides task_resume.answer
// back to the executor, which re-runs with the reply folded in.
func TestClarificationCrossDevice(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entry := newCore(t, "entry-clarify")
	worker := newSuperviseCore(t, "worker-clarify", 1)
	worker.SetWorkDir(t.TempDir())
	var mu sync.Mutex
	var prompts []string
	worker.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			return commander.AgentResult{
				OK: true, ExitCode: 0,
				Result: "partial work done\nPANDA_QUESTION: deploy to staging or production?",
			}
		}
		return commander.AgentResult{OK: true, ExitCode: 0, Result: "deployed to staging"}
	})
	startPair(t, ctx, entry, worker)

	task, result, err := entry.Submit(ctx, TaskInput{
		Title: "deploy app", ContextType: "command",
		Intent: "deploy the app", Requires: []string{"code:modify"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateReview {
		t.Fatalf("entry state = %s, want review", task.State)
	}
	if result.Question != "deploy to staging or production?" {
		t.Fatalf("origin question = %q", result.Question)
	}

	final, res, err := entry.ResumeApproved(ctx, task.TaskID, "staging")
	if err != nil {
		t.Fatalf("resume approved: %v", err)
	}
	if final.State != StateDone {
		t.Fatalf("post-answer entry state = %s, want done", final.State)
	}
	if !res.OK {
		t.Fatalf("resumed result not ok: %q", res.Stderr)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("agent ran %d times, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], "staging") {
		t.Fatalf("answer missing from resumed prompt: %q", prompts[1])
	}

	workerTask, err := worker.store.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("load worker copy: %v", err)
	}
	if workerTask.State != StateDone {
		t.Fatalf("post-answer worker state = %s, want done", workerTask.State)
	}
}

// TestAcceptanceEnvelopeReachesPrompt pins Q2's acceptance side: the spec's
// success_definition and constraints must reach the agent prompt, not die
// inside spec_json — the same intent text also feeds the supervise judge.
func TestAcceptanceEnvelopeReachesPrompt(t *testing.T) {
	ctx := context.Background()
	c := newSuperviseCore(t, "accept-env", 1)
	c.SetWorkDir(t.TempDir())

	var gotPrompt string
	c.router.SetAdapterRunner(func(ctx context.Context, adapter, prompt, cwd string) commander.AgentResult {
		gotPrompt = prompt
		return commander.AgentResult{OK: true, ExitCode: 0, Result: "done"}
	})

	_, _, err := c.SubmitLocal(ctx, TaskInput{
		Title: "bounded edit", ContextType: "command",
		Intent:   "change the color",
		Requires: []string{"code:modify"},
		SpecJSON: `{"scope":"ui","success_definition":"all tests pass","constraints":["do not touch api/","keep diff under 50 lines"]}`,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !strings.Contains(gotPrompt, "all tests pass") {
		t.Fatalf("success definition missing from prompt: %q", gotPrompt)
	}
	if !strings.Contains(gotPrompt, "do not touch api/") || !strings.Contains(gotPrompt, "keep diff under 50 lines") {
		t.Fatalf("constraints missing from prompt: %q", gotPrompt)
	}
	if !strings.Contains(gotPrompt, "acceptance criteria") {
		t.Fatalf("envelope header missing: %q", gotPrompt)
	}
}

// TestParseQuestionRequest covers the marker extraction: last non-empty
// marker wins, empty payloads are left in place, and protocol lines are
// stripped from the visible result.
func TestParseQuestionRequest(t *testing.T) {
	q, cleaned, ok := parseQuestionRequest("did part\nPANDA_QUESTION: which env?\ntrailing")
	if !ok || q != "which env?" {
		t.Fatalf("parse = %q ok=%v", q, ok)
	}
	if strings.Contains(cleaned, questionMarker) {
		t.Fatalf("marker not stripped: %q", cleaned)
	}
	if !strings.Contains(cleaned, "did part") || !strings.Contains(cleaned, "trailing") {
		t.Fatalf("content lost: %q", cleaned)
	}

	// Empty marker payload is not a question — the line survives verbatim.
	_, cleaned2, ok2 := parseQuestionRequest("all done\nPANDA_QUESTION:\n")
	if ok2 {
		t.Fatal("empty marker must not count as a question")
	}
	if !strings.Contains(cleaned2, questionMarker) {
		t.Fatal("malformed marker line was eaten")
	}
	if _, _, ok3 := parseQuestionRequest("no markers here"); ok3 {
		t.Fatal("clean output reported a question")
	}

	// The marker must end at a word boundary: agent prose that merely starts
	// with the marker text is not the protocol and must survive verbatim.
	_, cleaned4, ok4 := parseQuestionRequest("PANDA_QUESTIONABLE: design choice\nreal work")
	if ok4 {
		t.Fatal("PANDA_QUESTIONABLE line eaten as a protocol question")
	}
	if !strings.Contains(cleaned4, "PANDA_QUESTIONABLE: design choice") {
		t.Fatalf("lookalike line mangled: %q", cleaned4)
	}

	// A full-width colon is the separator a Chinese IME produces without
	// being asked; it must park the task exactly like the ASCII one.
	q5, cleaned5, ok5 := parseQuestionRequest("做完了\nPANDA_QUESTION：要改哪个文件？")
	if !ok5 || q5 != "要改哪个文件？" {
		t.Fatalf("full-width colon parse = %q ok=%v", q5, ok5)
	}
	if strings.Contains(cleaned5, questionMarker) {
		t.Fatalf("full-width marker not stripped: %q", cleaned5)
	}
	// The ideographic space (U+3000) is a separator too.
	if q6, _, ok6 := parseQuestionRequest("PANDA_QUESTION　哪个环境？"); !ok6 || q6 != "哪个环境？" {
		t.Fatalf("ideographic space parse = %q ok=%v", q6, ok6)
	}
}

// A real unattended run printed this no-question sentence as a protocol
// payload. It must not turn a completed task into a clarification park.
func TestParseQuestionRequestNoQuestionSentinel(t *testing.T) {
	for _, payload := range []string{
		"无，任务已完成，无需用户补充信息。",
		"无，任务已完成，无需用户补充信息",
		" \t无，任务已完成，无需用户补充信息. \t",
		// Punctuation drift: the same sentence comes back with half-width
		// commas, a shouting exclamation mark or a trailing ellipsis.
		"无,任务已完成,无需用户补充信息",
		"无，任务已完成，无需用户补充信息！",
		"无，任务已完成，无需用户补充信息……",
		"无 ，任务已完成 ， 无需用户补充信息。",
		// A separator other than "，" is the same clause break.
		"无；任务已完成；无需用户补充信息？",
		"无：任务已完成：无需用户补充信息．",
		"无;任务已完成;无需用户补充信息!",
	} {
		t.Run(payload, func(t *testing.T) {
			q, cleaned, ok := parseQuestionRequest("xin: v26.5.0\nPANDA_QUESTION: " + payload)
			if ok || q != "" {
				t.Fatalf("no-question payload parked: question=%q ok=%v", q, ok)
			}
			if cleaned != "xin: v26.5.0" {
				t.Fatalf("no-question marker not removed cleanly: %q", cleaned)
			}
		})
	}
	// Prefixes and completion wording are not enough to dismiss a real
	// question, especially one asking for a permission the task lacks.
	for _, payload := range []string{
		"无权限，请授权我运行命令。",
		"任务已完成，是否需要部署？",
		"无，任务已完成，无需用户补充信息，但请确认部署环境。",
	} {
		q, _, ok := parseQuestionRequest("PANDA_QUESTION: " + payload)
		if !ok || q != payload {
			t.Fatalf("real question was dismissed: %q -> %q ok=%v", payload, q, ok)
		}
	}
	q, cleaned, ok := parseQuestionRequest("partial\nPANDA_QUESTION: 哪个环境？\nPANDA_QUESTION: 无，任务已完成，无需用户补充信息。")
	if !ok || q != "哪个环境？" || cleaned != "partial" {
		t.Fatalf("sentinel hid a real question: question=%q cleaned=%q ok=%v", q, cleaned, ok)
	}
}

func TestClarificationNoQuestionSentinelNotParked(t *testing.T) {
	const sentinel = "无，任务已完成，无需用户补充信息。"
	for _, status := range []string{"marker", "done", "question"} {
		t.Run(status, func(t *testing.T) {
			c := newSuperviseCore(t, "no-question", 1)
			calls := 0
			c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
				calls++
				res := commander.AgentResult{OK: true, Result: "xin: v26.5.0"}
				if status == "marker" {
					res.Result += "\nPANDA_QUESTION: " + sentinel
				} else {
					res.Structured, _ = json.Marshal(structuredAgentResult{
						Status: status, Answer: res.Result, Question: sentinel,
					})
				}
				return res
			})
			task, result, err := c.SubmitLocal(context.Background(), TaskInput{
				Title: "read version", ContextType: "command",
				Intent: "query Node version", Requires: []string{"code:modify"},
			})
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if task.State != StateDone || !result.OK || result.Question != "" {
				t.Fatalf("no-question run parked: state=%s ok=%v question=%q", task.State, result.OK, result.Question)
			}
			if calls != 1 || result.Stdout != "xin: v26.5.0" {
				t.Fatalf("unexpected rerun or protocol leakage: calls=%d stdout=%q", calls, result.Stdout)
			}
		})
	}
}

func TestClarificationNoQuestionSentinelPreservesFailuresAndQuestions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		result   commander.AgentResult
		question string
	}{
		{
			name: "adapter failure",
			result: commander.AgentResult{
				OK: false, ExitCode: 1, Stderr: "query failed",
				Result: "PANDA_QUESTION: 无，任务已完成，无需用户补充信息。",
			},
		},
		{
			name: "structured failure",
			result: commander.AgentResult{
				OK: true, Result: "query failed",
				Structured: json.RawMessage(`{"status":"failed","answer":"query failed","question":"无，任务已完成，无需用户补充信息。"}`),
			},
		},
		{
			name: "real structured permission question",
			result: commander.AgentResult{
				OK: true, Result: "partial",
				Structured: json.RawMessage(`{"status":"question","question":"无权限，请授权我运行命令。"}`),
			},
			question: "无权限，请授权我运行命令。",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newSuperviseCore(t, "no-question-safety", 1)
			c.router.SetAdapterRunner(func(context.Context, string, string, string) commander.AgentResult {
				return tc.result
			})
			task, result, err := c.SubmitLocal(context.Background(), TaskInput{
				Title: "read version", ContextType: "command",
				Intent: "query Node version", Requires: []string{"code:modify"},
			})
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if tc.question != "" {
				if task.State != StateReview || result.Question != tc.question ||
					result.ApprovalDisposition != string(ApprovalResumeExecution) {
					t.Fatalf("permission question lost: state=%s result=%+v", task.State, result)
				}
			} else if task.State == StateDone || result.OK || result.Question != "" {
				t.Fatalf("failure promoted or parked as a question: state=%s result=%+v", task.State, result)
			}
		})
	}
}
