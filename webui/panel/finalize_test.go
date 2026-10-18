// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Xustalis/OpenPanda/internal/core"
)

// The digest is what the chat thread shows for a finished task. It used to
// read only the "result" key, so an adapter that reports its answer on stdout
// produced an empty digest and the summary fell through to the risk level —
// the thread showed "[done] low" next to a completed task.
func TestExtractResultTextFindsTheAnswerWhereverItLives(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{"structured result", `{"result":"done via result"}`, "done via result"},
		{"answer key", `{"answer":"done via answer"}`, "done via answer"},
		{"agent stdout", `{"ok":true,"stdout":"任务完成。\n\n编译无错误"}`, "任务完成。\n\n编译无错误"},
		{"failure stderr", `{"ok":false,"stderr":"gcc: not found"}`, "gcc: not found"},
		{"failure reason", `{"failed":"lease expired"}`, "lease expired"},
		{"result wins over stdout", `{"result":"best","stdout":"noise"}`, "best"},
		{"blank values are skipped", `{"result":"   ","stdout":"real text"}`, "real text"},
		{"nothing readable", `{"ok":true}`, ""},
		{"not an object", `"plain"`, ""},
		{"empty payload", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractResultText(c.json); got != c.want {
				t.Fatalf("extractResultText(%q) = %q, want %q", c.json, got, c.want)
			}
		})
	}
}

// The digest is the record of what the task did, so it arrives whole. It used
// to be capped at 800 bytes compared with len() rather than a rune count, which
// cut a Chinese result to roughly 347 characters and left the thread ending on
// a "…" the reader could not expand — the finished task looked like a model
// that had stopped mid-sentence.
func TestExtractResultTextKeepsTheWholeAnswer(t *testing.T) {
	long := strings.Repeat("汉", 2000) // 6 KB, well past the old 800-byte cap
	got := extractResultText(`{"result":"` + long + `"}`)
	if got != long {
		t.Fatalf("digest was cut: %d chars of %d", len(got), len(long))
	}
	if strings.HasSuffix(got, "…") {
		t.Fatal("digest grew an ellipsis")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("digest is not valid UTF-8: %q", got)
	}

	// A payload that already fits must still come back byte-for-byte.
	if short := extractResultText(`{"result":"短"}`); short != "短" {
		t.Fatalf("short digest was mangled: %q", short)
	}
}

func TestTaskSummaryPrefersTheDigestOverTheRisk(t *testing.T) {
	done := core.Task{
		State:      core.StateDone,
		Risk:       "low",
		Title:      "写个 Hello World",
		ResultJSON: `{"ok":true,"stdout":"编译并运行通过，输出 Hello, World!"}`,
	}
	if got := summaryText(done, ""); !strings.Contains(got, "Hello, World!") || strings.Contains(got, "low") {
		t.Fatalf("summary fell back to the risk level: %q", got)
	}
}

// A failed row with no payload still owes the reader a reason — the one its
// last result event recorded. Without it the thread says "[failed] low".
func TestTaskSummaryCarriesTheFailureReason(t *testing.T) {
	failed := core.Task{State: core.StateFailed, Risk: "low", Title: "总结仓库"}
	if got := summaryText(failed, "lease expired"); got != "[failed] lease expired" {
		t.Fatalf("summary = %q, want the failure reason", got)
	}

	// Nothing anywhere: the title is still more useful than the risk level.
	if got := summaryText(core.Task{State: core.StateFailed, Title: "总结仓库"}, ""); got != "[failed] 总结仓库" {
		t.Fatalf("bare summary = %q", got)
	}

	// The failure path is not capped either: a stack trace or a multi-line
	// stderr is exactly what the reader needs to see in full.
	long := strings.Repeat("栈帧\n", 900)
	if got := summaryText(failed, long); got != "[failed] "+long {
		t.Fatalf("failure reason was cut: %d chars of %d", len(got), len(long)+len("[failed] "))
	}

	// A finished task never shows a stale reason from an earlier attempt.
	if got := summaryText(core.Task{State: core.StateDone, Title: "写个网页"}, "reason ignored"); got != "[done] 写个网页" {
		t.Fatalf("done summary = %q", got)
	}
}
