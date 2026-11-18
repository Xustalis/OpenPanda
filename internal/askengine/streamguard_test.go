// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"strings"
	"testing"
)

func TestDirectiveStreamGuardSuppressesEnvelope(t *testing.T) {
	var got []string
	w, flush := guardedDelta(func(s string) { got = append(got, s) })
	for _, d := range []string{`{"kind":`, `"task","task":`, `{"title":"x"}`, `}}`} {
		w(d)
	}
	flush()
	if len(got) != 0 {
		t.Fatalf("envelope reached the surface: %q", strings.Join(got, ""))
	}
}

func TestDirectiveStreamGuardReleasesProse(t *testing.T) {
	var b strings.Builder
	w, flush := guardedDelta(func(s string) { b.WriteString(s) })
	text := "好的，我先看一下这个目录里都有什么文件，然后决定要不要新建一个单独的 HTML 文件来放这只骑车的鹈鹕。"
	for _, r := range text {
		w(string(r))
	}
	flush()
	if b.String() != text {
		t.Fatalf("streamed text = %q, want the original %q", b.String(), text)
	}
}

func TestDirectiveStreamGuardFlushesShortAnswer(t *testing.T) {
	var b strings.Builder
	w, flush := guardedDelta(func(s string) { b.WriteString(s) })
	w("已完成。")
	if b.Len() != 0 {
		t.Fatalf("a buffered answer leaked before the call ended: %q", b.String())
	}
	flush()
	if b.String() != "已完成。" {
		t.Fatalf("flush = %q", b.String())
	}
	// A second flush must not repeat the text.
	flush()
	if b.String() != "已完成。" {
		t.Fatalf("second flush duplicated: %q", b.String())
	}
}

func TestDirectiveStreamGuardNewlineSettlesEarly(t *testing.T) {
	var b strings.Builder
	w, flush := guardedDelta(func(s string) { b.WriteString(s) })
	w("第一行\n")
	if b.String() != "第一行\n" {
		t.Fatalf("a newline-terminated line is prose and must stream: %q", b.String())
	}
	flush()
	if b.String() != "第一行\n" {
		t.Fatalf("flush rewrote the stream: %q", b.String())
	}
}

func TestDirectiveStreamGuardNilCallback(t *testing.T) {
	w, flush := guardedDelta(nil)
	if w != nil {
		t.Fatalf("nil delta must not gain a writer")
	}
	flush()
}
