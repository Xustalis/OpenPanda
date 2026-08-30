package askengine

import (
	"context"
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// The sub-agent round (design: "调度变成子代理"): a classified task is one step
// of the conversation, not its end. The dispatch is replayed as the entry
// model's own words and the outcome fed back as an observation, so the next
// classification reports on it — the session survives the task instead of
// being terminated by it, whether the task ran on this node's agent, another
// node's agent, or the queue. The helpers here compose those turns; the loop
// that drives them is AskTurns.

// taskDispatchNote replays a dispatch as the entry model's own words, so the
// transcript reads as one continuous conversation around a sub-agent round:
// the model said it would run the task, the outcome arrives as the next
// observation, and the following call reports on it. Feeding the dispatch
// back this way (rather than a synthetic "task submitted" note) keeps the
// model's authorship of the plan visible to itself when it converges.
func taskDispatchNote(spec *entry.TaskSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "已派发子代理任务：%s", spec.Title)
	if len(spec.Requires.Abilities) > 0 {
		fmt.Fprintf(&b, "（需要能力：%s）", strings.Join(spec.Requires.Abilities, "、"))
	}
	if spec.Spec.Target != "" {
		fmt.Fprintf(&b, "\n目标：%s", spec.Spec.Target)
	}
	if spec.Spec.Node != "" {
		fmt.Fprintf(&b, "\n指定节点：%s", spec.Spec.Node)
	}
	return b.String()
}

// taskBudgetNote is appended as a user turn when the loop refuses the model's
// latest dispatch because the task budget is spent: the final tool-free call
// then explains itself instead of silently dropping the model's intent.
const taskBudgetNote = "本轮对话的子代理任务预算（%d 个）已用完，不再派发新任务。请基于已执行任务的结果直接向用户汇报。"

// taskObservation formats one executed task's outcome as the observation the
// entry model reports on: state, exit code, and excerpts of the output. The
// agent transcript behind a task can be tens of thousands of tokens, so the
// observation carries an excerpt only — the full output travels in the Result
// (Stdout/Stderr) for the caller to surface on demand.
func taskObservation(res *Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[子代理任务结果] %s（%s）\n状态：%s", res.TaskTitle, res.TaskID, res.TaskState)
	if res.ExitCode != 0 {
		fmt.Fprintf(&b, "，退出码 %d", res.ExitCode)
	}
	if out := excerpt(res.Stdout, 4000); out != "" {
		fmt.Fprintf(&b, "\n输出摘录：\n%s", out)
	}
	if errText := excerpt(res.Stderr, 2000); errText != "" {
		fmt.Fprintf(&b, "\n错误摘录：\n%s", errText)
	}
	b.WriteString("\n请基于以上结果继续本轮对话：向用户汇报，或决定下一步。")
	return b.String()
}

// excerpt trims a log to at most limit runes keeping head and tail — the
// beginning says what was attempted, the end says how it finished.
func excerpt(s string, limit int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= limit {
		return string(runes)
	}
	head := limit * 2 / 3
	tail := limit / 3
	return string(runes[:head]) + "\n…（中间输出已省略）…\n" + string(runes[len(runes)-tail:])
}

// reportTaskOutcome converges on the model's report when the round budget
// leaves no room for another loop iteration after a task ran: the dispatch and
// its observation are appended to the accumulated history and one tool-free
// call produces the report the loop would have converged on. Without tools the
// model can only answer in text, so the ask ends in a report rather than raw
// output.
func (e *Engine) reportTaskOutcome(ctx context.Context, turns []entry.Turn, devices []ledger.Node, conversationMemory string, opts []entry.ClassifyOption, spec *entry.TaskSpec, res *Result) (string, error) {
	client := e.client.Load()
	t := append(append([]entry.Turn{}, turns...),
		entry.Turn{Role: "assistant", Content: taskDispatchNote(spec)},
		entry.Turn{Role: "user", Content: taskObservation(res)},
	)
	out, err := entry.ClassifyTurns(ctx, client, devices, conversationMemory, t, opts...)
	if err != nil {
		return "", err
	}
	if out.Kind != entry.KindAnswer || out.Answer == "" {
		return "", fmt.Errorf("report call converged on %s instead of an answer", out.Kind)
	}
	return out.Answer, nil
}
