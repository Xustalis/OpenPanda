// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// TestInterruptionNoteCompact pins the TUI-noise fix: when a task already
// carries the outcome and the model was cut off mid-loop, the reply gets a
// one-line interruption note pointing at `task show` — not the raw tool
// transcript (the live Windows report dumped a ten-line task-list after a
// successful browser-open task).
func TestInterruptionNoteCompact(t *testing.T) {
	note := interruptionNote(10, "01a124c4-62f0", i18n.ChineseSimp)
	if !strings.Contains(note, "10") || !strings.Contains(note, "01a124c4-62f0") {
		t.Fatalf("note = %q, want the operation count and task id", note)
	}
	if strings.Count(note, "\n") != 0 {
		t.Fatalf("note should be one line, got %q", note)
	}
	en := interruptionNote(3, "abc", i18n.English)
	if !strings.Contains(en, "task show abc") {
		t.Fatalf("english note = %q", en)
	}
}
