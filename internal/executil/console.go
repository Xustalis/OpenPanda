// SPDX-License-Identifier: AGPL-3.0-or-later

package executil

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

// consoleDecoder maps a Windows code page to its x/text decoder. Codepages
// without a mapping (or without a CJK decoder in x/text) fall through to the
// replacement form — better mojibake with visible markers than silent bytes.
func consoleDecoder(cp uint32) encoding.Encoding {
	switch cp {
	case 936:
		return simplifiedchinese.GB18030 // superset of GBK
	case 932:
		return japanese.ShiftJIS
	case 949:
		return korean.EUCKR
	case 950:
		return traditionalchinese.Big5
	}
	return nil
}

// normalizeWithCodePage converts captured child output to valid UTF-8.
// Already-valid UTF-8 passes through untouched — modern CLIs (Node, Go, Rust)
// write UTF-8 regardless of the console — while invalid bytes are decoded
// with the given code page. A decode failure keeps whatever decoded cleanly
// and degrades the rest to U+FFFD, so one stray byte cannot poison the whole
// payload.
func normalizeWithCodePage(b []byte, cp uint32) string {
	if utf8.Valid(b) {
		return string(b)
	}
	if dec := consoleDecoder(cp); dec != nil {
		out, _, err := transform.Bytes(dec.NewDecoder(), b)
		if err == nil {
			return string(out)
		}
		if len(out) > 0 {
			return string(out) + "\uFFFD"
		}
	}
	return strings.ToValidUTF8(string(b), "\uFFFD")
}
