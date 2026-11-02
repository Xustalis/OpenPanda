// SPDX-License-Identifier: AGPL-3.0-or-later

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

// supervisePromptVersion tags superviseSystemPrompt; bump it whenever the
// judging instructions change so verdicts produced under the old instructions
// are no longer served.
const supervisePromptVersion = "v2"

// superviseSystemPrompt instructs the entry model to act as the reviewing
// superior ("上级"): it judges an agent's result against the task's success
// criteria and emits a strict JSON verdict.
const superviseSystemPrompt = `你是执行结果审核员（上级）。一个智能体刚刚执行了一项任务，下面是任务要求与它的最终汇报。请务实、客观地判断任务是否已实质性完成。

判断规则：
- 实质完成即判 done：智能体针对任务核心目标开展了实质工作，并给出了明确、结构化的结论、分析报告或修改产出，即使存在客套前言、小幅格式差异或细节润色空间，均应判为 done。
- 对调研、排查、体检、分析类任务：只要核心发现、关键证据与结论已呈现，即判为 done。严禁因要求“删除前言”、“重新排版”、“提供更多衍生建议”等次要要求而苛刻判为 continue。
- 仅当核心目标严重缺失、智能体执行中断/崩溃、或智能体明确说明工作未完成且确有关键必要步骤未执行时，才判为 continue，并在 followup 中简明写清下一步核心指令。
- .git/ 下的对象、引用与索引变更，以及 panda-base-* / panda-shadow-* 分支，是编排器自身的快照簿记，并非智能体的越界改动；若证据中仅有此类痕迹，不得作为 continue 的依据。
- 证据中可能包含[本轮实际执行的命令]、[本轮工具用量]、[本轮实际变更的文件]和[项目声明的验证入口]。若工作目录确有文件改动，但证据显示本轮没有执行任何命令、或执行了命令却没有匹配到项目声明的验证入口，而任务的成功定义或验收标准涉及构建、测试或运行结果，则不得仅凭智能体的文字汇报判 done；判 continue，并在 followup 中明确要求运行项目声明的验证入口并给出结果。
- 若执行退出码非 0，或验证命令在证据中标记为[失败]，除非任务本身就是排查这个失败，否则不得判 done；判 continue 并指出需要修复的失败。失败记录要结合后续命令判断：探索性命令先失败、随后修正并成功验证的，仍可判 done。
- 证据中的[验收基线]是智能体动手前的检查结果：基线里已经失败的检查属于既有问题，除任务本身要求修复它之外，不得计为本次执行的失败。
- 不要因为缺少与核心目标无关的附加步骤而判 continue。

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
	// The verdict is a function of (prompt version, judge model, intent,
	// result): a model switch — manual or failover — must not serve the
	// previous judge's done verdict for up to the cache TTL.
	k1, k2 := hashString(supervisePromptVersion+"\x00"+c.ModelName()+"\x00"+intent), hashString(result)
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
