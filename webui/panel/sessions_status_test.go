// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/askengine"
)

// TestStatusEventOfToolCarriesSubject pins the wire shape the console's side
// conversation uses to show what was checked: a tool progress event names the
// tool and, when the call has one, its subject (a path, a search pattern).
func TestStatusEventOfToolCarriesSubject(t *testing.T) {
	ev := statusEventOf(askengine.Progress{
		Kind:   askengine.ProgressTool,
		Name:   "workspace_read",
		Detail: "internal/askengine/side.go",
	})
	if ev["kind"] != "tool" {
		t.Errorf("kind = %v, want tool", ev["kind"])
	}
	if ev["name"] != "workspace_read" {
		t.Errorf("name = %v, want workspace_read", ev["name"])
	}
	if ev["detail"] != "internal/askengine/side.go" {
		t.Errorf("detail = %v, want the call's subject", ev["detail"])
	}

	ev = statusEventOf(askengine.Progress{Kind: askengine.ProgressTool, Name: "system_status"})
	if _, ok := ev["detail"]; ok {
		t.Errorf("empty subject should not be sent, got %v", ev["detail"])
	}
}
