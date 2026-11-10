// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// A workspace-backed question about this repository must not take the
// no-tools conversational fast path. Generic programming questions remain cheap.
func refersToRepository(prompt string) bool {
	s := strings.ToLower(prompt)
	return hasAnyPhrase(s, "这个项目", "该项目", "本项目", "当前项目", "代码库", "仓库", "源码", "这份代码",
		"this project", "this repo", "the repo", "repository", "codebase", "source tree")
}

// This routes explicit broad investigations to a coding harness, rather than
// pretending that a fixed number of shallow workspace reads proves quality.
func needsRepositoryInvestigation(prompt string) bool {
	if !refersToRepository(prompt) {
		return false
	}
	s := strings.ToLower(prompt)
	return hasAnyPhrase(s, "深入", "深度", "实际调研", "全面", "仔细", "探索", "调研", "审查", "审计", "分析", "问题", "缺陷", "风险",
		"investigat", "explor", "audit", "review", "analyz", "deep", "thorough", "issues", "problems")
}

func repositoryInvestigationReadOnly(prompt string) bool {
	s := strings.ToLower(prompt)
	if hasAnyPhrase(s, "只读", "只分析", "不要改", "不要修改", "不要编辑", "不修改", "不编辑", "read-only", "no edits", "do not edit", "don't edit") {
		return true
	}
	// A request for advice about fixes/builds is still analysis. Explicit
	// combined execution wins; this fallback is not a substitute for the
	// structured task_kind contract.
	if hasAnyPhrase(s, "建议", "方案", "计划", "如何", "recommend", "proposal", "how to") &&
		!hasAnyPhrase(s, "并修复", "并修改", "并重构", "并实现", "直接修复", "直接修改", "and fix", "and edit", "and implement") {
		return true
	}
	return !hasAnyPhrase(s, "修复", "修改", "重构", "并实现", "优化", "改进", "编写", "新增", "删除", "编辑", "开发") && !repositoryEditWords.MatchString(s)
}

var repositoryEditWords = regexp.MustCompile(`\b(?:fix|edit|refactor|implement|improve|optimize)\b`)

func hasAnyPhrase(s string, phrases ...string) bool {
	for _, p := range phrases {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func investigationRouteReminder(loc i18n.Locale) string {
	if loc == i18n.English {
		return "This request explicitly requires a deep investigation of the current repository. Do not finalize from orientation tools or an unverified search. Dispatch a coding-harness task to inspect implementation and relevant tests; use read-only/plan permission if no edits were requested. If no capable agent is available, report that the investigation cannot be completed instead of inventing findings."
	}
	return "用户明确要求深入调研当前代码库。不能凭目录、文档或未查全的搜索直接定论；请派发 coding harness 任务检查实现及相关测试。未要求改动时使用只读/plan 权限；没有可用 agent 时诚实说明无法完成，不要编造结论。"
}

func incompleteInvestigationNotice(loc i18n.Locale) string {
	return i18n.T(loc, "ask.investigation.incomplete")
}

// repositoryRequest uses the current turn's intent and bounded user history
// to resolve repository follow-ups. The side channel uses the same policy;
// reference history supplies an object, never an instruction to execute.
func repositoryRequest(prompt string, history []entry.Turn, workspace bool) (question, deep bool) {
	if !workspace {
		return false, false
	}
	explicit := refersToRepository(prompt) || repositoryCodeReference.MatchString(prompt)
	antecedent := false
	start := len(history) - 8
	if start < 0 {
		start = 0
	}
	for _, turn := range history[start:] {
		if turn.Role == "user" && (refersToRepository(turn.Content) || repositoryCodeReference.MatchString(turn.Content)) {
			antecedent = true
		}
	}
	lower := strings.ToLower(prompt)
	followup := antecedent && hasAnyPhrase(lower, "继续", "这里", "这个", "这些", "不对", "全面", "深入", "仔细", "检查", "分析", "follow up", "continue", "this", "these", "thorough", "deep", "review", "audit")
	question = explicit || followup
	deep = needsRepositoryInvestigation(prompt) || (question && hasAnyPhrase(lower,
		"深入", "深度", "全面", "仔细", "审查", "审计", "调研", "问题", "缺陷", "风险",
		"investigat", "explor", "audit", "review", "deep", "thorough", "issues", "problems"))
	return question, deep
}

// A code identifier/path is concrete workspace context, not a conceptual
// explanation. This only vetoes the no-tools fast path; narrow questions can
// still finish cheaply after a targeted source read instead of a paid agent.
var repositoryCodeReference = regexp.MustCompile("`[^`]+`|\\b[A-Za-z_][A-Za-z0-9_]*_[A-Za-z0-9_]+\\b|\\b[A-Za-z]*[a-z][A-Z][A-Za-z0-9]*\\b|[A-Za-z0-9_./-]+\\.(?:go|py|tsx?|jsx?|rs|java|c|cpp|h|sql|yaml|json)\\b")

func sourceReadEvidence(call *entry.ToolCall, result string, runErr error) bool {
	if runErr != nil || call == nil || call.Tool != "workspace_read" || !strings.Contains(result, "\t") {
		return false
	}
	path, _ := call.Arguments["path"].(string)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".rs", ".java", ".c", ".cpp", ".h", ".sql", ".yaml", ".yml", ".json", ".sh", ".toml":
		return true
	}
	return false
}

// incompleteRepositoryTask is shared by normal answers, streaming and the
// tool-loop convergence boundary. A receipt is not implementation evidence;
// neither another summarization call nor compact recovery can promote it.
func incompleteRepositoryTask(deep bool, task *Result) bool {
	return deep && task != nil && (task.TaskState != core.StateDone || !task.OK)
}
