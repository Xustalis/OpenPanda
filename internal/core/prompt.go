// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/skills"
)

// taskScope extracts the declared scope from a task's persisted spec JSON
// (entry.TaskSpecDetail.Scope). A parse failure or absent field yields an
// empty scope, which disables drift checking for that task.
func taskScope(specJSON string) string {
	if specJSON == "" {
		return ""
	}
	var spec struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return ""
	}
	return spec.Scope
}

// taskToolsPolicy extracts the per-task tools policy from a task's persisted
// spec JSON (entry.TaskSpec.ToolsPolicy). Empty means "use the global
// routing.tools_policy"; "minimal" or "extended" override the router's
// default for this task alone. A parse failure yields empty (global default).
func taskToolsPolicy(specJSON string) string {
	if specJSON == "" {
		return ""
	}
	var spec struct {
		ToolsPolicy string `json:"tools_policy"`
	}
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return ""
	}
	if spec.ToolsPolicy != "minimal" && spec.ToolsPolicy != "extended" {
		return ""
	}
	return spec.ToolsPolicy
}

// taskMaxTurns extracts the per-task agent turn cap from a task's persisted
// spec JSON (entry.TaskSpecDetail.MaxTurns). A parse failure or a non-positive
// value yields 0 (the adapter's own default).
func taskMaxTurns(specJSON string) int {
	if specJSON == "" {
		return 0
	}
	var spec struct {
		MaxTurns int `json:"max_turns"`
	}
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil || spec.MaxTurns < 0 {
		return 0
	}
	return spec.MaxTurns
}

// taskSpecEnvelope extracts the acceptance side of a task's persisted spec —
// the entry model's success definition and constraints — rendered as one
// bracketed block. Both fold into the intent so the agent prompt states what
// "done" means and the supervise judge (which reads the same intent) can hold
// the work to it, instead of the criteria dying inside spec_json.
func taskSpecEnvelope(specJSON string) string {
	if specJSON == "" {
		return ""
	}
	var spec struct {
		Scope             string   `json:"scope"`
		SuccessDefinition string   `json:"success_definition"`
		Constraints       []string `json:"constraints"`
	}
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return ""
	}
	var b strings.Builder
	if spec.SuccessDefinition != "" {
		b.WriteString("\nsuccess definition: " + spec.SuccessDefinition)
	}
	for _, cst := range spec.Constraints {
		if strings.TrimSpace(cst) != "" {
			b.WriteString("\nconstraint: " + cst)
		}
	}
	if scope := strings.TrimSpace(spec.Scope); scope != "" {
		b.WriteString("\nallowed scope: " + scope + " — modify only files under these paths")
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\n[acceptance criteria — judge the result against these]" + b.String()
}

// agentPromptBudget is the soft cap (bytes) on the assembled agent prompt.
// The budget exists because the prompt is composed of unbounded parts
// (matched skill bodies, the memory manifest) and no adapter can see an
// agent's context window from here; overflowing the window fails the run
// deterministically (ContextOverflow parks it). When the budget is tight the
// skills section degrades first — full bodies shrink to index lines — while
// the intent and the memory manifest are kept whole: the degradation order
// is intent > memory manifest > skill index > skill body. Set to 128KB so
// multi-byte UTF-8 (e.g. Chinese text) has ample headroom without prematurely
// degrading skills into index lines.
const agentPromptBudget = 128000

// outputLangName renders a locale as the language name an agent recognizes
// ("English", "简体中文"…), in the instruction language zh or en.
func outputLangName(loc i18n.Locale, zh bool) string {
	names := map[i18n.Locale][2]string{
		i18n.ChineseSimp: {"简体中文", "Simplified Chinese"},
		i18n.Japanese:    {"日文", "Japanese"},
		i18n.Spanish:     {"西班牙文", "Spanish"},
		i18n.German:      {"德文", "German"},
		i18n.English:     {"英文", "English"},
	}
	if n, ok := names[loc]; ok {
		if zh {
			return n[0]
		}
		return n[1]
	}
	if zh {
		return "英文"
	}
	return "English"
}

// getAgentOutputRider is the execution contract appended to every agent
// prompt. Each line is something the supervisor can actually check — verify
// against the success definition, stay in scope, report what was verified —
// not motivational phrasing the judge cannot weigh.
func getAgentOutputRider(locs ...i18n.Locale) string {
	promptLang := i18n.English
	outputLang := i18n.English
	if len(locs) == 1 {
		if locs[0] != "" {
			promptLang = locs[0]
			outputLang = locs[0]
		}
	} else if len(locs) >= 2 {
		if locs[0] != "" {
			promptLang = locs[0]
		}
		if locs[1] != "" {
			outputLang = locs[1]
		}
	}

	if promptLang == i18n.ChineseSimp {
		return fmt.Sprintf(`

执行约定：
- 在 workspace 目录内工作；动手前先读项目里相关的源文件与文档，有源码可读就不要去反汇编二进制。
- 宣布完成前必须验证：用该项目自身的构建/测试方式（按其 Makefile、scripts、CI 或包管理器惯例）跑通能证明成功定义的命令；只写"应该可行"不算完成。
- 只改动任务范围内（scope）的文件；不要顺手重构或格式化无关代码。
- 最终回复用%s，直接说明：改了什么、在哪个文件、如何验证（运行的命令及其结果）。有无法满足的验收标准要明确写出，不许含糊带过。
- 不要使用表情符号，不要寒暄或与任务无关的自我介绍。`, outputLangName(outputLang, true))
	}
	return fmt.Sprintf(`

Execution contract:
- Work inside the workspace; read the project's relevant source files and docs before editing — never disassemble a binary whose source is on disk.
- Verify before claiming done: run whatever proves the success definition using the project's own build/test convention (its Makefile, scripts, CI, or package manager); reporting what "should" work is not completion.
- Touch only files inside the declared scope; do not refactor or reformat unrelated code.
- Final reply in %s, stating plainly: what changed, in which files, and how it was verified (the commands run and their outcomes). Name any acceptance criterion you could not verify — do not imply it passed.
- No emojis, no pleasantries, no meta-commentary about being an AI.`, outputLangName(outputLang, false))
}

// buildAgentPrompt assembles the full agent execution prompt — the memory
// file manifest (A3 selective loading) plus the task intent plus any matched
// skills (design §8.5) — and returns the skills that were actually loaded, so
// the caller can record their use. Skill matching keys off the short title,
// not the full intent, so a long instruction does not over-match on common
// words. The assembly stays under agentPromptBudget by degrading skills to
// index lines when the budget is tight.
func buildAgentPrompt(c *Core, intent, project, title, workDir string, loc ...i18n.Locale) (string, []*skills.Skill) {
	promptLang := i18n.ChineseSimp
	outputLang := i18n.ChineseSimp
	if len(loc) == 1 && loc[0] != "" {
		promptLang = loc[0]
		outputLang = loc[0]
	} else if len(loc) >= 2 {
		if loc[0] != "" {
			promptLang = loc[0]
		}
		if loc[1] != "" {
			outputLang = loc[1]
		}
	}
	rider := getAgentOutputRider(promptLang, outputLang)

	// Execution frame first: the agent runs headless with cwd already set to
	// the workspace — stating the task, project, and directory up front keeps
	// it from guessing where it is or which tree it may touch.
	var frame strings.Builder
	frame.WriteString("[execution context]")
	if title != "" {
		frame.WriteString("\ntask: " + title)
	}
	if project != "" {
		frame.WriteString("\nproject: " + project)
	}
	if workDir != "" {
		frame.WriteString("\nworkspace: " + workDir + " (your working directory — resolve relative paths from here)")
	}

	manifest := ""
	if c.memory != nil {
		if project == "" {
			if files, err := c.memory.Manifest(); err == nil {
				manifest = memory.RenderManifest(files)
			}
		} else if pm, err := c.memory.ProjectManifest(project, workDir, promptLang); err == nil {
			manifest = pm
		}
	}
	// The intent, the manifest and the output rider are non-negotiable; only
	// the skills section degrades. A negative remainder (a huge intent) still
	// runs — withSkills treats it as "no room for skill bodies".
	budget := agentPromptBudget - len(intent) - len(manifest) - len(rider) - frame.Len()
	prompt, used := withSkills(c, intent, project, title, budget, promptLang)
	if manifest != "" {
		prompt = manifest + "\n\n" + prompt
	}
	prompt = frame.String() + "\n\n" + prompt
	// Output style rider: the agent's final message is shown to the user
	// (often verbatim in a terminal) and may be spoken aloud by the voice
	// pipeline, so it must read as a direct answer — not a transcript of
	// the agent's exploration. Execution details stay in the task's event
	// stream (panda task <id>) for anyone who wants the full trail.
	prompt += rider
	// §4.2 Sub-MainAgent protocol hint: the marker format the run loop
	// parses. Kept terse — it rides every agent round.
	prompt += i18n.T(promptLang, "prompt.delegate.hint")
	// §4.3 clarification protocol hint: headless execution cannot prompt
	// mid-run, so a blocked agent ends its turn with the marker the run
	// loop parks on. Same rider placement — every agent round.
	prompt += i18n.T(promptLang, "prompt.question.hint")
	// Self-management hint: under the extended tools policy the agent may
	// reach the node's own surface — MCP-capable CLIs see the openpanda
	// server from the materialized .mcp.json, shell-capable agents can run
	// the panda binary already on PATH. Minimal policy keeps the tool face
	// closed, so the hint only rides extended runs.
	if c.router != nil && c.router.ToolsPolicy() == "extended" && c.router.PandaTools() {
		prompt += i18n.T(promptLang, "prompt.selftools.hint")
	}
	return prompt, used
}

// withSkills prepends matched active skills to the intent via the lightweight
// index (design §8.5 progressive loading): only a matched skill's full body is
// loaded, never the whole bank. Global skills apply everywhere; project skills
// apply within their own project. budget bounds the skills section (bytes):
// a body that does not fit degrades to an index line (name + description) so
// the agent still knows the skill exists without the prompt overflowing the
// agent's window. The returned slice is the set actually loaded.
func withSkills(c *Core, intent, project, query string, budget int, loc ...i18n.Locale) (string, []*skills.Skill) {
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	if c.skills == nil {
		return intent, nil
	}
	if query == "" {
		query = intent // degenerate task with no title: fall back to intent
	}
	index, err := c.skills.Index()
	if err != nil {
		c.logger.Warn("load skill index", "err", err)
		return intent, nil
	}
	var matched []skills.IndexEntry
	matched = append(matched, skills.Match(index, skills.ScopeGlobal, "", query)...)
	if project != "" {
		matched = append(matched, skills.Match(index, skills.ScopeProject, project, query)...)
	}
	if len(matched) == 0 {
		return intent, nil
	}
	var b strings.Builder
	availableKey := i18n.T(targetLoc, "prompt.skills.available")
	b.WriteString(availableKey + "\n")
	remaining := budget - b.Len()
	var used []*skills.Skill
	for _, e := range matched {
		sk, err := c.skills.Load(e.Scope, e.Key, e.Name)
		if err != nil || sk == nil {
			continue
		}
		used = append(used, sk)
		full := fmt.Sprintf("## %s\n%s\n", sk.Name, sk.Body)
		if len(full) <= remaining {
			b.WriteString(full)
			remaining -= len(full)
			continue
		}
		// Budget exhausted: degrade to an index line — the agent sees the
		// skill exists (and what it is for) without the full body.
		omittedMsg := i18n.Tf(targetLoc, "prompt.skills.omitted", "desc", sk.Description)
		line := fmt.Sprintf("## %s\n%s\n", sk.Name, omittedMsg)
		b.WriteString(line)
		c.logger.Warn("agent prompt budget: skill degraded to index line",
			"skill", sk.Name, "budget", budget)
	}
	if len(used) == 0 {
		return intent, nil
	}
	intentHeader := i18n.T(targetLoc, "prompt.task.intent_header")
	return b.String() + "\n" + intentHeader + "\n" + intent, used
}

// logTask appends one daily-log line recording a task outcome. The daily log is
// the warm layer the Dreaming engine (design §17.3) consolidates from, so this
// is the point where task history becomes candidate long-term memory.
func (c *Core) logTask(title string, ok bool, loc ...i18n.Locale) {
	if c.daily == nil {
		return
	}
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	status := i18n.T(targetLoc, "prompt.task.status.success")
	if !ok {
		status = i18n.T(targetLoc, "prompt.task.status.fail")
	}
	line := i18n.Tf(targetLoc, "prompt.task.log_format", "title", title, "status", status)
	if err := c.daily.AppendExternal(time.Now(), line); err != nil {
		c.logger.Warn("append daily log", "err", err)
	}
}

// recordSkillUse bumps the use counters of the skills that were loaded into an
// agent's prompt, so the skill lifecycle (dormant/expired) reflects real usage.
func recordSkillUse(c *Core, used []*skills.Skill, ok bool) {
	for _, sk := range used {
		sk.RecordUse(ok, time.Now())
		if err := c.skills.Save(sk); err != nil {
			c.logger.Warn("save skill use", "name", sk.Name, "err", err)
		}
	}
}

// trackTask feeds the skill tracker with one task outcome, so a recurring task
// class can eventually clear the quality gate and generate a skill (design §8.2).
func trackTask(c *Core, project string, required []string, title string, ok bool) {
	if c.tracker == nil {
		return
	}
	if _, err := c.tracker.Record(project, required, title, ok); err != nil {
		c.logger.Warn("track task for skill", "err", err)
	}
}

// audit records a high-risk operation in the audit log (P3-32). It never fails
// the execution path: a write error is logged and dropped, because audit must
// not break the hot loop.
func (c *Core) audit(ctx context.Context, taskID, what, target, result, detail string) {
	if c.auditLog == nil {
		return
	}
	if err := c.auditLog.Record(ctx, security.Entry{
		Who:    c.nodeID,
		What:   what,
		Target: target,
		Result: result,
		Detail: detail,
	}); err != nil {
		c.logger.Warn("audit record", "err", err)
	}
}
