package main

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/core"
)

// TestSpokenReplyReview is the one outcome the voice surface must not get wrong.
// A task parked in review has run nothing and will run nothing until a person
// approves it; speaking "做完了" there would be a lie told to someone who has no
// terminal in front of them to check.
func TestSpokenReplyReview(t *testing.T) {
	got := spokenReply(&askengine.Result{Kind: "task", TaskState: "review"})
	if !strings.Contains(got, "审批") {
		t.Fatalf("a review task was spoken as %q; it never mentions approval", got)
	}
}

// TestSpokenReplyTask covers the two settled task outcomes: success speaks the
// head of the output, failure says so instead of staying silent.
func TestSpokenReplyTask(t *testing.T) {
	ok := spokenReply(&askengine.Result{Kind: "task", TaskState: "done", OK: true,
		Stdout: "准确率 0.93\n更多细节在日志里"})
	if !strings.Contains(ok, "准确率 0.93") {
		t.Errorf("success reply dropped the result: %q", ok)
	}
	if strings.Contains(ok, "更多细节") {
		t.Errorf("success reply read the whole output aloud: %q", ok)
	}
	bad := spokenReply(&askengine.Result{Kind: "task", TaskState: "failed", ExitCode: 1,
		Stderr: "no such file"})
	if strings.Contains(bad, "做完了") {
		t.Errorf("a failed task was spoken as success: %q", bad)
	}
}

// TestSpokenReplyPlan checks that a started plan is announced as unfinished
// work. A plan's stages run on other machines, so the honest sentence is "开始跑
// 了…跑完再告诉你", not a result.
func TestSpokenReplyPlan(t *testing.T) {
	got := spokenReply(&askengine.Result{Kind: "plan", OK: true, PlanID: "p1", PlanGoal: "训练模型",
		PlanStages: []core.Task{{StageID: "develop"}, {StageID: "train"}, {StageID: "report"}}})
	if !strings.Contains(got, "3 段") {
		t.Errorf("plan reply does not say how many stages: %q", got)
	}
	if !strings.Contains(got, "跑完再告诉你") {
		t.Errorf("plan reply implies the work is finished: %q", got)
	}
}

// TestSpokenReplyTruncates keeps a long answer from being read aloud in full: a
// listener cannot skim or scroll back, so the speaker gets a bounded sentence
// while the terminal keeps the whole text.
func TestSpokenReplyTruncates(t *testing.T) {
	long := strings.Repeat("一段很长的解释。", 400)
	got := spokenReply(&askengine.Result{Kind: "answer", Answer: long})
	if n := len([]rune(got)); n > maxSpeakChars+1 { // +1 for the ellipsis
		t.Errorf("spoken answer is %d runes, cap is %d", n, maxSpeakChars)
	}
	if got == "" {
		t.Error("a long answer was reduced to silence")
	}
}
