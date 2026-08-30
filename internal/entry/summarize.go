package entry

import (
	"context"
	"fmt"
	"strings"
)

// summarizeSystemPrompt instructs the entry model to act as a dispatch
// result reporter: it takes a task's outcome and produces a concise,
// user-facing summary in the user's language. Success: what was done + key
// output. Failure: why + what to do next. The summary is display-only; the
// raw output still travels in Result.Stdout/Stderr for callers that want it.
const summarizeSystemPrompt = `你是调度结果汇报员。一个任务刚刚执行完毕，下面是任务标题、意图和它的执行结果。请用简洁的中文向用户汇报：

- 成功时：说明做了什么、关键输出是什么（一两句话即可，不要复述全部 stdout）。
- 失败时：说明失败原因，并给出用户可以执行的具体下一步操作（例如如何批准、如何重试、如何修改配置）。
- 只输出汇报正文，不要输出 JSON、标签或其他格式。
- 控制在 3 句话以内。`

// SummarizeResult asks the entry model to produce a user-facing summary of a
// task's outcome. It is the dedicated "report after execution" call: the
// engine invokes it after every inline task (success or failure) so the user
// sees a human-readable summary instead of raw stdout/stderr. A model
// failure degrades gracefully — the caller falls back to raw output, so the
// summary never blocks result delivery (review: LLM 汇报必须可降级).
func SummarizeResult(ctx context.Context, c *Client, title, intent string, ok bool, exitCode int, stdout, stderr string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("no model client")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "任务标题：%s\n", title)
	if intent != "" {
		fmt.Fprintf(&b, "任务意图：%s\n", truncate(intent, 500))
	}
	if ok {
		b.WriteString("执行结果：成功\n")
	} else {
		fmt.Fprintf(&b, "执行结果：失败（退出码 %d）\n", exitCode)
	}
	if out := truncate(strings.TrimSpace(stdout), 3000); out != "" {
		fmt.Fprintf(&b, "输出摘录：\n%s\n", out)
	}
	if errText := truncate(strings.TrimSpace(stderr), 1500); errText != "" {
		fmt.Fprintf(&b, "错误摘录：\n%s\n", errText)
	}
	text, err := c.Complete(ctx, summarizeSystemPrompt, b.String())
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("empty summary")
	}
	return text, nil
}
