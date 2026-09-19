package entry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SuperviseVerdict is the outcome of a post-execution completion check: did
// the agent's result actually satisfy the task, or does work remain?
type SuperviseVerdict struct {
	// Status is "done" (the task is complete), "continue" (work remains), or
	// "review" (no verdict could be obtained — hand it to a human).
	Status string `json:"status"`
	// Reason is a one-line justification for the verdict.
	Reason string `json:"reason"`
	// Followup is, for a "continue" verdict, the remaining work and the next
	// step for the following agent. Empty for "done".
	Followup string `json:"followup"`
}

// superviseCacheNS is the DiskCache namespace for supervise verdicts.
const superviseCacheNS = "supervise"

// superviseSystemPrompt instructs the entry model to act as the reviewing
// superior ("上级"): it judges an agent's result against the task's success
// criteria and emits a strict JSON verdict.
const superviseSystemPrompt = `你是执行结果审核员（上级）。一个智能体刚刚执行了一项任务，下面是任务要求与它的最终汇报。请务实、客观地判断任务是否已实质性完成。

判断规则：
- 实质完成即判 done：智能体针对任务核心目标开展了实质工作，并给出了明确、结构化的结论、分析报告或修改产出，即使存在客套前言、小幅格式差异或细节润色空间，均应判为 done。
- 对调研、排查、体检、分析类任务：只要核心发现、关键证据与结论已呈现，即判为 done。严禁因要求“删除前言”、“重新排版”、“提供更多衍生建议”等次要要求而苛刻判为 continue。
- 仅当核心目标严重缺失、智能体执行中断/崩溃、或智能体明确说明工作未完成且确有关键必要步骤未执行时，才判为 continue，并在 followup 中简明写清下一步核心指令。

只输出一个 JSON 对象，不要输出任何其他文字或解释：
{"status":"done"|"continue","reason":"一句话结论","followup":"continue 时必填：剩余工作与下一步指令"}`

// Supervise verdict statuses. Done/Continue are the two judgments the model may
// emit; Review is produced by Supervise itself when the model answers without
// a usable verdict — or when the model is unreachable (review P1-6) — and asks
// the caller to park the task for a human.
const (
	VerdictDone     = "done"
	VerdictContinue = "continue"
	VerdictReview   = "review"
)

// Supervise asks the configured entry model whether an agent's result fully
// satisfies the task described by intent (which carries the success criteria).
// A model that answers without producing a verdict returns VerdictReview:
// unverified work is handed to a human rather than silently promoted to done.
// An unreachable model also parks the task for review (review P1-6): the
// project's primary guarantee is that only work meeting its success definition
// ever reaches done, and a supervisor outage is precisely when nothing can be
// verified — degrading to done then would silently accept every agent task
// while the endpoint is unhealthy. Parking keeps the terminal state honest; a
// human can approve (or re-run) once the supervisor recovers. The error still
// travels to the caller, which logs it. Parked verdicts are never cached — a
// re-run under a healthy supervisor judges from scratch.
func Supervise(ctx context.Context, c *Client, intent, result string) (SuperviseVerdict, error) {
	dc := c.diskCache()
	k1, k2 := hashString(intent), hashString(result)
	if dc != nil {
		var v SuperviseVerdict
		if dc.Get(ctx, superviseCacheNS, k1, k2, &v) {
			return v, nil
		}
	}
	user := "任务要求：\n" + intent + "\n\n智能体回报：\n" + result
	text, err := c.Complete(ctx, superviseSystemPrompt, user)
	if err != nil {
		// Park, don't accept: with no supervisor there is no verification, and
		// an unverified result must not be promoted to done (review P1-6).
		// Hand it to a human; the error travels to the caller for logging.
		return SuperviseVerdict{Status: VerdictReview, Reason: "supervisor unavailable: result unverified, parked for review"}, err
	}
	v, err := parseSuperviseVerdict(text)
	if err != nil {
		// Unparsable verdict: the result is unverified, so it goes to a human
		// instead of looping on a model that will not produce the expected
		// shape. The reason records the defect.
		return SuperviseVerdict{Status: VerdictReview, Reason: "verdict unparsable: " + err.Error()}, nil
	}
	// Only cache completed (done) verdicts: a continue or review verdict is
	// transient and must not trap subsequent runs into deterministic repeat loops.
	if dc != nil && v.Status == VerdictDone {
		dc.Put(ctx, superviseCacheNS, k1, k2, v)
	}
	return v, nil
}

// parseSuperviseVerdict extracts a {status, reason, followup} object from the
// model's raw text, tolerating markdown code fences and surrounding prose.
func parseSuperviseVerdict(raw string) (SuperviseVerdict, error) {
	s := strings.TrimSpace(raw)
	// Strip a leading/trailing ```json / ``` fence if present.
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	lo, hi := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if lo < 0 || hi <= lo {
		return SuperviseVerdict{}, fmt.Errorf("no JSON object found")
	}
	var v struct {
		Status   string `json:"status"`
		Reason   string `json:"reason"`
		Followup string `json:"followup"`
	}
	if err := json.Unmarshal([]byte(s[lo:hi+1]), &v); err != nil {
		return SuperviseVerdict{}, err
	}
	v.Status = strings.ToLower(strings.TrimSpace(v.Status))
	switch v.Status {
	case "done", "continue":
	default:
		return SuperviseVerdict{}, fmt.Errorf("unexpected status %q", v.Status)
	}
	return SuperviseVerdict{Status: v.Status, Reason: v.Reason, Followup: v.Followup}, nil
}
