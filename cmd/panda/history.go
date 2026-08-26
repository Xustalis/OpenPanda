package main

// History file codec, shared by every platform's terminal layer.
//
// The file is one entry per physical line, which stopped being enough the
// moment the line editor learned multi-line input: a pasted twelve-line prompt
// written raw would come back as twelve separate entries, and recalling it with
// Up would replay one line of it. Entries are therefore escaped on the way out
// (`\` → `\\`, newline → `\n`) and unescaped on the way in.
//
// Unrecognized escapes pass through untouched, so a history file written by an
// older build — where `C:\temp` was stored verbatim — still reads back as
// `C:\temp` rather than as a tab.

import "strings"

// encodeHistoryLine flattens one entry to a single physical line.
func encodeHistoryLine(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

// decodeHistoryLine restores one entry from its physical line.
func decodeHistoryLine(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
			i++
		case '\\':
			b.WriteByte('\\')
			i++
		default:
			b.WriteByte(s[i]) // unknown escape: keep both bytes
		}
	}
	return b.String()
}

// loadHistoryFile decodes a history file's contents into entries, oldest
// first, capped to the newest limit.
func loadHistoryFile(data []byte, limit int) []string {
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, decodeHistoryLine(l))
	}
	if n := len(out); limit > 0 && n > limit {
		out = out[n-limit:]
	}
	return out
}

// encodeHistoryFile renders entries back to file bytes.
func encodeHistoryFile(entries []string) []byte {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, encodeHistoryLine(e))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}
