// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

// bareInputIsAsk is the safety gate on routing `panda <words>` to the model:
// a multi-word line is a prompt, a single token that cannot spell a command
// is a prompt, and a lone lowercase-ASCII word stays a typo candidate.
func TestBareInputIsAsk(t *testing.T) {
	cases := []struct {
		name string
		sub  string
		rest []string
		want bool
	}{
		{"typo stays error", "staatus", nil, false},
		{"unknown single word stays error", "frobnicate", nil, false},
		{"hyphenated single word stays error", "fix-the-thing", nil, false},
		{"two words is a prompt", "fix", []string{"the", "bug"}, true},
		{"three words is a prompt", "deploy", []string{"to", "prod"}, true},
		{"cjk token is a prompt", "你好", nil, true},
		{"cjk sentence token is a prompt", "帮我看下日志", nil, true},
		{"uppercase token is a prompt", "Deploy", nil, true},
		{"punctuated token is a prompt", "don't", nil, true},
		{"flags alone don't make a phrase", "fix", []string{"--authorize"}, false},
		{"value flag's argument isn't a word", "fix", []string{"--project", "demo"}, false},
		{"value flag = form isn't a word", "fix", []string{"--project=demo"}, false},
		{"flag plus real word is a phrase", "fix", []string{"the", "bug", "--json"}, true},
		{"word then flag value still a phrase", "fix", []string{"--project", "demo", "quickly"}, true},
		{"unknown flag doesn't count as word", "frobnicate", []string{"--bogus"}, false},
	}
	for _, tc := range cases {
		if got := bareInputIsAsk(tc.sub, tc.rest); got != tc.want {
			t.Errorf("%s: bareInputIsAsk(%q, %v) = %v, want %v", tc.name, tc.sub, tc.rest, got, tc.want)
		}
	}
}

func TestLooksLikeSubcommand(t *testing.T) {
	for _, s := range []string{"status", "task", "fix-the-thing", "x", "v2"} {
		if !looksLikeSubcommand(s) {
			t.Errorf("looksLikeSubcommand(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "你好", "Deploy", "don't", "x.y", "FIX"} {
		if looksLikeSubcommand(s) {
			t.Errorf("looksLikeSubcommand(%q) = true, want false", s)
		}
	}
}
