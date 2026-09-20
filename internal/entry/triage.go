package entry

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TriageResult indicates whether a prompt should take the fast conversational path.
type TriageResult struct {
	IsFastPath bool
	Reason     string
}

// Action-oriented keywords that suggest tasks or external execution. The CJK
// entries are substring-matched; the ASCII entries are matched on word
// boundaries by actionWordsRe, so "latest" does not trigger the "test" veto.
var actionKeywords = []string{
	"运行", "执行", "创建", "删除", "修改", "部署", "编译", "测试", "重构",
	"写入", "保存", "下载", "克隆", "配置", "排查", "修复", "安装",
	"调度", "派发", "唤起", "呼叫", "启动", "委派",
}
var actionWords = []string{
	"run", "exec", "create", "delete", "remove", "modify", "edit",
	"deploy", "build", "test", "refactor", "write", "save", "clone",
	"fix", "install", "git", "bash", "curl", "docker", "make",
	"dispatch", "schedule", "launch", "start",
}

var actionWordsRe = regexp.MustCompile(`\b(?:` + strings.Join(actionWords, "|") + `)\b`)

// Prompts that need the engine's built-in tools (clock, weather, reminders)
// rather than a pure conversational answer. A false veto merely costs one
// heavy classification round; a miss would make the fast path — which runs
// with no tools attached — answer a time or weather question by guessing.
var toolKeywords = []string{
	"几点", "时间", "日期", "几号", "星期几", "礼拜几", "天气", "气温", "温度",
	"weather", "what time", "what day", "what date", "temperature",
}

// Direct conversational/query patterns.
var queryPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^(你好|您好|hello|hi|hey|在吗|早上好|晚上好)[\s!！?？.~。]*$`),
	regexp.MustCompile(`^(你是谁|介绍一下你自己|你能做什么|who are you)[\s!！?？.~。]*$`),
	regexp.MustCompile(`^(请问|请)?(解释|什么是|如何理解|为什么|简述|科普|总结一下|介绍一下|区别是什么|有哪些|怎么用|用法|语法)`),
	regexp.MustCompile(`^(please\s+)?(explain|what is|how to|why|summarize|tell me about|difference between)`),
}

// FastTriage analyzes user prompt to check if it's a simple query that does not
// require cluster device topology scanning, heavy JSON task schema, or external tools.
func FastTriage(prompt string, history []Turn) TriageResult {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		// Nothing conversational to answer; keep the pre-fast-path behavior
		// and let standard orchestration decide (it also handles the error).
		return TriageResult{IsFastPath: false, Reason: "empty prompt"}
	}

	lower := strings.ToLower(trimmed)

	// Slash commands or shell prefixes are handled elsewhere or via tools.
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "!") {
		return TriageResult{IsFastPath: false, Reason: "command prefix"}
	}

	// If prompt contains file paths or code extensions, it likely needs tooling/context.
	if strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") ||
		strings.Contains(lower, ".go") || strings.Contains(lower, ".ts") ||
		strings.Contains(lower, ".py") || strings.Contains(lower, ".json") ||
		strings.Contains(lower, ".yaml") || strings.Contains(lower, ".yml") {
		return TriageResult{IsFastPath: false, Reason: "file reference"}
	}

	// Action or tool intent always falls back to the full pipeline, which is
	// the only path that can execute tools or delegate tasks.
	if hasActionIntent(lower) {
		return TriageResult{IsFastPath: false, Reason: "action intent"}
	}
	if containsAny(lower, toolKeywords) {
		return TriageResult{IsFastPath: false, Reason: "needs built-in tools"}
	}

	// Check if matches conversational greetings or common query patterns.
	for _, p := range queryPatterns {
		if p.MatchString(lower) {
			return TriageResult{IsFastPath: true, Reason: "query pattern match"}
		}
	}

	// Short conceptual questions (under 120 chars, counted as runes so CJK
	// text gets the same ~40-120-char headroom as English) ending with
	// question marks and carrying no action/tool intent.
	if utf8.RuneCountInString(trimmed) < 120 && (strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, "？")) {
		return TriageResult{IsFastPath: true, Reason: "pure question"}
	}

	return TriageResult{IsFastPath: false, Reason: "standard orchestration"}
}

// hasActionIntent reports whether the lowercased prompt carries an explicit
// execution verb: CJK keywords as substrings, ASCII keywords on word
// boundaries (a CJK character is a non-word rune, so "部署docker" still
// matches \bdocker\b).
func hasActionIntent(lower string) bool {
	if containsAny(lower, actionKeywords) {
		return true
	}
	return actionWordsRe.MatchString(lower)
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// FastPathPrompt returns a minimal system prompt for pure conversation & conceptual answering.
func FastPathPrompt(asciiOnly bool, loc ...i18n.Locale) string {
	if asciiOnly {
		return i18n.T(i18n.English, "prompt.fast_triage.prompt")
	}
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	return i18n.T(targetLoc, "prompt.fast_triage.prompt")
}
