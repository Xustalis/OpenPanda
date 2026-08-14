package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Daily retention windows (design §17.2): logs older than dailyArchiveDays are
// moved to daily/archive/ (kept, but out of the warm scan path); logs older
// than dailyDeleteDays are deleted.
const (
	dailyArchiveDays = 30
	dailyDeleteDays  = 90
	dailyLayout      = "2006-01-02"
)

// Daily appends to and prunes the warm-layer daily logs under memory/daily/.
type Daily struct {
	dir string
	mu  sync.Mutex // serializes appends so concurrent task completions never interleave
}

// NewDaily wraps a daily/ directory.
func NewDaily(dir string) *Daily { return &Daily{dir: dir} }

// PathFor returns the daily log path for a date.
func (d *Daily) PathFor(date time.Time) string {
	return filepath.Join(d.dir, date.Format(dailyLayout)+".md")
}

// Append writes one timestamped line to the day's log, creating the file and
// directory as needed. Append is safe for concurrent callers — the core's task
// goroutines complete tasks in parallel, so appends are serialized here.
func (d *Daily) Append(now time.Time, entry string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(d.dir, 0o755); err != nil {
		return fmt.Errorf("memory: create daily dir: %w", err)
	}
	f, err := os.OpenFile(d.PathFor(now), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("memory: open daily log: %w", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "- %s %s\n", now.Format("15:04:05"), entry); err != nil {
		return fmt.Errorf("memory: append daily log: %w", err)
	}
	return nil
}

// Prune enforces the retention windows at now: files older than deleteDays are
// deleted, files older than archiveDays (but not yet deleteDays) are moved to
// daily/archive/. Files that do not match the YYYY-MM-DD.md naming are left
// untouched, as is the archive directory itself. It shares the Daily mutex with
// Append so a prune cannot race an in-flight append.
func (d *Daily) Prune(now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("memory: read daily dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		date, ok := parseDailyName(e.Name())
		if !ok {
			continue
		}
		age := now.Sub(date)
		switch {
		case age >= dailyDeleteDays*24*time.Hour:
			if err := os.Remove(filepath.Join(d.dir, e.Name())); err != nil {
				return fmt.Errorf("memory: delete daily %s: %w", e.Name(), err)
			}
		case age >= dailyArchiveDays*24*time.Hour:
			if err := d.archive(e.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Daily) archive(name string) error {
	dir := filepath.Join(d.dir, "archive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("memory: create archive dir: %w", err)
	}
	if err := os.Rename(filepath.Join(d.dir, name), filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("memory: archive daily %s: %w", name, err)
	}
	return nil
}

// parseDailyName parses a YYYY-MM-DD.md filename. The bool is false for any
// name that is not a daily log.
func parseDailyName(name string) (time.Time, bool) {
	base, ok := strings.CutSuffix(name, ".md")
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(dailyLayout, base)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
