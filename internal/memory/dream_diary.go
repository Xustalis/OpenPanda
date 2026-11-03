// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DreamDiary appends human-readable consolidation entries to DREAMS.md (OpenClaw
// Dream Diary, design §17.3). The diary is for a human to read — it is not a
// promotion source; only the Dreamer's Deep phase writes MEMORY.md.
type DreamDiary struct {
	path string
}

// NewDreamDiary wraps a DREAMS.md path.
func NewDreamDiary(path string) *DreamDiary {
	return &DreamDiary{path: path}
}

// diaryThemeRows caps how many REM themes a section lists — the themes are a
// reflection hint for the reader, not an exhaustive histogram.
const diaryThemeRows = 8

// Append records one sweep as a dated section. It is append-only and creates
// the file on first use.
func (d *DreamDiary) Append(report Report, now time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "- Scanned %d candidates / 扫描候选：%d 条\n", report.Candidates, report.Candidates)
	if len(report.Themes) > 0 {
		fmt.Fprintf(&b, "- Themes / 主题：%s\n", themeLine(report.Themes))
	}
	if len(report.Promoted) > 0 {
		b.WriteString("- Promoted to long-term memory / 提升到长期记忆：\n")
		for _, p := range report.Promoted {
			fmt.Fprintf(&b, "  - %s\n", p)
		}
	} else {
		b.WriteString("- Promoted to long-term memory / 提升到长期记忆：无 none\n")
	}

	dir := dirOf(d.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("memory: create dream diary dir: %w", err)
	}
	// As in the daily log: MkdirAll and O_CREATE only set modes on fresh
	// nodes, so tighten a pre-existing dir/file on every append.
	_ = os.Chmod(dir, 0o700)
	f, err := os.OpenFile(d.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open DREAMS.md: %w", err)
	}
	defer f.Close()
	_ = os.Chmod(d.path, 0o600)
	if _, err := f.WriteString(b.String()); err != nil {
		return fmt.Errorf("memory: append DREAMS.md: %w", err)
	}
	return nil
}

// themeLine renders the REM histogram as "name×count" pairs, most frequent
// first and name-sorted within a tie so the diary is deterministic.
func themeLine(themes map[string]int) string {
	names := make([]string, 0, len(themes))
	for name := range themes {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if themes[names[i]] != themes[names[j]] {
			return themes[names[i]] > themes[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > diaryThemeRows {
		names = names[:diaryThemeRows]
	}
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s×%d", n, themes[n]))
	}
	return strings.Join(parts, "、")
}

// dirOf returns the directory part of a path (the DREAMS.md file lives in the
// memory root, which may not exist yet on first run).
func dirOf(p string) string {
	// filepath.Dir, not a '/' scan: the path is built with filepath.Join, so a
	// Windows build hands this backslashes and the scan returned "." — the
	// diary then failed on its first write when the directory did not exist.
	return filepath.Dir(p)
}
