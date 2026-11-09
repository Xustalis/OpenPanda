// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import "testing"

// TestMatchLocalAction pins the fast path's gate: only a bare imperative for
// one of this node's own subcommands may skip the model. Every negative here
// is a case that used to reach the agent, or must keep reaching it.
func TestMatchLocalAction(t *testing.T) {
	positives := []struct {
		prompt string
		id     string
		argv   []string
	}{
		{"打开 web 控制台", "web", []string{"web", "--daemon"}},
		{"帮我启动一下控制台吧", "web", []string{"web", "--daemon"}},
		{"启动 web 服务", "web", []string{"web", "--daemon"}},
		{"运行 web 服务器", "web", []string{"web", "--daemon"}},
		{"打开面板", "web", []string{"web", "--daemon"}},
		{"open the console", "web", []string{"web", "--daemon"}},
		{"Please start the web service", "web", []string{"web", "--daemon"}},
		{"launch dashboard", "web", []string{"web", "--daemon"}},
		{"doctor", "doctor", []string{"doctor"}},
		{"跑一下自检", "doctor", []string{"doctor"}},
		{"诊断", "doctor", []string{"doctor"}},
		{"status", "status", []string{"status"}},
		{"看看状态", "status", []string{"status"}},
		{"节点状态", "status", []string{"status"}},
		// Trailing sentence punctuation is not a question.
		{"打开 web 控制台。", "web", []string{"web", "--daemon"}},
	}
	for _, tc := range positives {
		act, ok := MatchLocalAction(tc.prompt)
		if !ok {
			t.Errorf("%q did not match; want %s", tc.prompt, tc.id)
			continue
		}
		if act.ID != tc.id {
			t.Errorf("%q matched %s, want %s", tc.prompt, act.ID, tc.id)
		}
		if len(act.Argv) != len(tc.argv) {
			t.Errorf("%q argv = %v, want %v", tc.prompt, act.Argv, tc.argv)
			continue
		}
		for i := range tc.argv {
			if act.Argv[i] != tc.argv[i] {
				t.Errorf("%q argv = %v, want %v", tc.prompt, act.Argv, tc.argv)
				break
			}
		}
	}

	negatives := []string{
		// Questions about the thing, not orders to do it.
		"怎么打开 web 控制台？",
		"如何启动 web 服务",
		"how do I open the web console?",
		"what is the status",
		"为什么控制台打不开",
		// A bare imperative plus more work is a task, not an action.
		"打开 web 控制台并把端口改成 9000",
		"open the console and change the port to 9000",
		"启动 web 服务然后配置反向代理",
		// Unrelated imperatives that share a verb.
		"打开 README 看看",
		"帮我把 CI 修好",
		"检查一下构建状态",
		"status of the last build",
		// Empty and noise.
		"",
		"   ",
	}
	for _, prompt := range negatives {
		if act, ok := MatchLocalAction(prompt); ok {
			t.Errorf("%q matched %s; it must stay on the model path", prompt, act.ID)
		}
	}
}

// TestMatchLocalActionStripsCourtesy keeps the prefix/suffix peeling honest:
// the wrapper must not become part of the imperative, or the same request
// with and without "帮我" would take different paths.
func TestMatchLocalActionStripsCourtesy(t *testing.T) {
	for _, prompt := range []string{
		"打开控制台",
		"请打开控制台",
		"麻烦帮我打开控制台一下",
		"帮我直接打开控制台吧",
	} {
		if act, ok := MatchLocalAction(prompt); !ok || act.ID != "web" {
			t.Errorf("%q matched %v/%v, want web", prompt, act.ID, ok)
		}
	}
}
