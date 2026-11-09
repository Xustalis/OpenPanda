// SPDX-License-Identifier: AGPL-3.0-or-later

package localmodels

import "testing"

// The discover sheet greys a free OpenCode model with "inside OpenCode only"
// instead of "export OPENCODE_API_KEY": the free tier is a client-side grant,
// so an imported row would answer 429 FreeUsageLimitError on every call. That
// note is chosen by this predicate, so it has to recognise both spellings the
// catalogue uses.
func TestFreeOnProvider(t *testing.T) {
	cases := []struct {
		provider, model string
		want            bool
	}{
		// The suffix the catalogue uses for most of the free roster.
		{"opencode", "deepseek-v4.1-flash-free", true},
		{"opencode", "iceman-free", true},
		{"opencode", "nemotron-3.5-super-vl-free", true},
		// The one free model whose id does not say so; the provider table does.
		{"opencode", "big-pickle", true},
		// Paid ids of the same provider are not free.
		{"opencode", "deepseek-v4.1-flash", false},
		{"opencode", "kimi-k3", false},
		// A provider the catalogue has no row for cannot claim a free tier.
		{"alibaba-cn", "deepseek-v4.1-flash", false},
		{"alibaba-cn", "qwen3.8-flash", false},
	}
	for _, c := range cases {
		if got := freeOnProvider(c.provider, c.model); got != c.want {
			t.Errorf("freeOnProvider(%q, %q) = %v, want %v", c.provider, c.model, got, c.want)
		}
	}
}
