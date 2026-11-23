// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import "testing"

func TestSessionCallName(t *testing.T) {
	for _, tc := range []struct{ data, want string }{
		{`{"attempt_id":"current","agent":"codex","adapter":"codex.py"}`, "codex"},
		{`{"attempt_id":"current","adapter":"claude_code.py"}`, "claude_code"},
		{`{"attempt_id":"current","plan_kind":"native","ability":"proof:left"}`, "proof:left"},
		{`{"attempt_id":"previous","agent":"codex"}`, ""},
		{`{"attempt_id":"current","profile":{"agent":"deepseek_harness","adapter":"deepseek_harness.py"}}`, "deepseek_harness"},
		{`{"attempt_id":"previous","profile":{"agent":"codex"}}`, ""},
		{`{"agent":"codex"}`, ""},
		{`{"attempt_id":"current","plan_kind":"manual"}`, ""},
		{`not JSON`, ""},
	} {
		if got, _ := sessionCall(tc.data, "current"); got != tc.want {
			t.Errorf("sessionCallName(%s): got %q, want %q", tc.data, got, tc.want)
		}
	}
}
