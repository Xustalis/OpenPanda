package memory

import (
	"fmt"
	"os"
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

// Append records one sweep as a dated section. It is append-only and creates
// the file on first use.
func (d *DreamDiary) Append(report Report, now time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "- 扫描候选：%d 条\n", report.Candidates)
	if len(report.Promoted) > 0 {
		b.WriteString("- 提升到长期记忆：\n")
		for _, p := range report.Promoted {
			fmt.Fprintf(&b, "  - %s\n", p)
		}
	} else {
		b.WriteString("- 提升到长期记忆：无\n")
	}

	if err := os.MkdirAll(dirOf(d.path), 0o755); err != nil {
		return fmt.Errorf("memory: create dream diary dir: %w", err)
	}
	f, err := os.OpenFile(d.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("memory: open DREAMS.md: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return fmt.Errorf("memory: append DREAMS.md: %w", err)
	}
	return nil
}

// dirOf returns the directory part of a path (the DREAMS.md file lives in the
// memory root, which may not exist yet on first run).
func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}
