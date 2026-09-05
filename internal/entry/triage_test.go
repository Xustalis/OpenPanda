package entry

import (
	"testing"
)

func TestFastTriage(t *testing.T) {
	cases := []struct {
		prompt     string
		wantFast   bool
		wantReason string
	}{
		{prompt: "你好", wantFast: true, wantReason: "query pattern match"},
		{prompt: "hello", wantFast: true, wantReason: "query pattern match"},
		{prompt: "介绍一下你自己", wantFast: true, wantReason: "query pattern match"},
		{prompt: "什么是 goroutine？", wantFast: true, wantReason: "query pattern match"},
		{prompt: "explain the difference between process and thread", wantFast: true, wantReason: "query pattern match"},
		{prompt: "为什么会有死锁？", wantFast: true, wantReason: "query pattern match"},
		{prompt: "请解释一下什么是 CAP 定理", wantFast: true, wantReason: "query pattern match"},
		// "latest" must not trip the word-boundary "test" action veto.
		{prompt: "What is the latest version of Go?", wantFast: true, wantReason: "query pattern match"},
		// Long CJK questions were previously excluded by byte-length counting
		// (a 50-rune Chinese question is ~150 bytes); runes get the same
		// headroom as English.
		{prompt: "Kubernetes 里的 Pod 在节点宕机之后一般要经历哪些状态流转，为什么整个恢复过程可能长达数分钟之久？", wantFast: true, wantReason: "pure question"},

		// Empty prompt keeps pre-fast-path behavior: standard orchestration.
		{prompt: "", wantFast: false, wantReason: "empty prompt"},
		{prompt: "   ", wantFast: false, wantReason: "empty prompt"},

		// Action oriented prompts must not take fast path
		{prompt: "重构 internal/core/handlers.go", wantFast: false, wantReason: "file reference"},
		{prompt: "运行测试并修复 bug", wantFast: false, wantReason: "action intent"},
		{prompt: "编译当前项目", wantFast: false, wantReason: "action intent"},
		{prompt: "/model list", wantFast: false, wantReason: "command prefix"},
		{prompt: "!git status", wantFast: false, wantReason: "command prefix"},
		{prompt: "修改 main.go 添加日志", wantFast: false, wantReason: "file reference"},

		// Time/weather/date queries need the built-in tools, which the fast
		// path deliberately does not attach — they must fall back.
		{prompt: "现在几点了？", wantFast: false, wantReason: "needs built-in tools"},
		{prompt: "明天天气怎么样？", wantFast: false, wantReason: "needs built-in tools"},
		{prompt: "今天是几号？", wantFast: false, wantReason: "needs built-in tools"},
		{prompt: "What time is it now?", wantFast: false, wantReason: "needs built-in tools"},
	}

	for _, c := range cases {
		res := FastTriage(c.prompt, nil)
		if res.IsFastPath != c.wantFast {
			t.Errorf("FastTriage(%q) = %v (reason: %s), want %v", c.prompt, res.IsFastPath, res.Reason, c.wantFast)
		}
		if res.Reason != c.wantReason {
			t.Errorf("FastTriage(%q) reason = %q, want %q", c.prompt, res.Reason, c.wantReason)
		}
	}
}
