// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestParsePastedCode(t *testing.T) {
	for _, tc := range []struct {
		in, code, state string
	}{
		{"abc123#st-xyz", "abc123", "st-xyz"},
		{"abc123", "abc123", ""},
		{"  padded#st  ", "", ""}, // leading space trims first, then # splits — see note
		{"https://console.anthropic.com/oauth/code/callback?code=abc&state=st", "abc", "st"},
		{"code=abc&state=st", "abc", "st"},
		{"", "", ""},
		{"#onlystate", "", "onlystate"},
	} {
		code, state := parsePastedCode(tc.in)
		if tc.in == "  padded#st  " {
			// TrimSpace runs inside parsePastedCode, so this is really "padded#st".
			if code != "padded" || state != "st" {
				t.Errorf("parsePastedCode(%q) = %q,%q want padded,st", tc.in, code, state)
			}
			continue
		}
		if code != tc.code || state != tc.state {
			t.Errorf("parsePastedCode(%q) = %q,%q want %q,%q", tc.in, code, state, tc.code, tc.state)
		}
	}
}
