// SPDX-License-Identifier: AGPL-3.0-or-later

package executil

import "testing"

// TestNormalizeWithCodePage pins the console-codepage policy: valid UTF-8 is
// never reinterpreted, invalid bytes decode through the mapped codepage, and
// an unmapped codepage degrades to replacement characters instead of leaking
// invalid UTF-8 into JSON payloads (where Go would silently replace them).
func TestNormalizeWithCodePage(t *testing.T) {
	utf8Text := "中文 ok"
	if got := normalizeWithCodePage([]byte(utf8Text), 936); got != utf8Text {
		t.Fatalf("valid utf-8 rewritten: %q", got)
	}

	// "中文" in GBK/GB18030.
	gbk := []byte{0xd6, 0xd0, 0xce, 0xc4}
	if got := normalizeWithCodePage(gbk, 936); got != "中文" {
		t.Fatalf("gbk decode = %q, want 中文", got)
	}

	// "テスト" in Shift-JIS.
	sjis := []byte{0x83, 0x65, 0x83, 0x58, 0x83, 0x67}
	if got := normalizeWithCodePage(sjis, 932); got != "テスト" {
		t.Fatalf("sjis decode = %q, want テスト", got)
	}

	// Unmapped codepage: bytes must still come out as valid UTF-8 with the
	// replacement marker, never as raw invalid bytes.
	got := normalizeWithCodePage(gbk, 437)
	if got == string(gbk) {
		t.Fatalf("invalid bytes passed through for unmapped codepage")
	}
	for _, r := range got {
		if r == 0xFFFD {
			return // replacement form is the documented degradation
		}
	}
	t.Fatalf("unmapped codepage output = %q, want replacement characters", got)
}
