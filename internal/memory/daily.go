// SPDX-License-Identifier: AGPL-3.0-or-later

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

// ExternalMarker prefixes a daily-log line whose content derives from
// external input (task titles, user text, wire payloads) rather than the
// agent's own observations. The Dreaming engine's provenance gate (design
// §17.3, OpenClaw taint model) treats such lines as untrusted and never
// promotes them into MEMORY.md (P1-22).
const ExternalMarker = "[ext]"

// Append writes one timestamped line to the day's log, creating the file and
// directory as needed. Append is safe for concurrent callers — the core's task
// goroutines complete tasks in parallel, so appends are serialized here.
//
// The entry is sanitized to a single line: an embedded newline would inject
// phantom log lines and corrupt the Dreaming statistics built on top (P2-16).
// Use AppendExternal instead when the entry carries user- or wire-controlled
// text, so the provenance gate can taint it.
func (d *Daily) Append(now time.Time, entry string) error {
	return d.append(now, entry, false)
}

// AppendExternal is Append for content derived from external input (task
// titles, user instructions, wire payloads). The line is tagged with
// ExternalMarker so light() marks its provenance untrusted and the Deep phase
// can never consolidate it into long-term memory — closing the stored
// prompt-injection channel where task text was "consolidated" into MEMORY.md
// and from there into every future system prompt (P1-22).
func (d *Daily) AppendExternal(now time.Time, entry string) error {
	return d.append(now, entry, true)
}

func (d *Daily) append(now time.Time, entry string, external bool) error {
	entry = sanitizeEntry(entry)
	if external {
		entry = ExternalMarker + " " + entry
	}
	release, err := lockRoot(d.dir)
	if err != nil {
		return fmt.Errorf("memory: lock daily log: %w", err)
	}
	defer release()
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		return fmt.Errorf("memory: create daily dir: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone and O_CREATE's mode
	// only applies to a fresh file: tighten both so a log first written under
	// a looser umask does not stay readable to other local accounts.
	_ = os.Chmod(d.dir, 0o700)
	path := d.PathFor(now)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("memory: daily log %s is not a regular file", path)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("memory: inspect daily log: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open daily log: %w", err)
	}
	defer f.Close()
	_ = os.Chmod(path, 0o600)
	if _, err := f.Write([]byte(fmt.Sprintf("- %s %s\n", now.Format("15:04:05"), entry))); err != nil {
		return fmt.Errorf("memory: append daily log: %w", err)
	}
	return nil
}

// sanitizeEntry collapses newlines/tabs to spaces so one entry is always one
// log line.
func sanitizeEntry(entry string) string {
	return strings.Join(strings.Fields(entry), " ")
}

// Prune enforces the retention windows at now: files older than deleteDays are
// deleted, files older than archiveDays (but not yet deleteDays) are moved to
// daily/archive/. The delete window sweeps the archive too — archiving moves a
// log out of the warm scan path, it does not exempt it from deletion, or
// archive/ would grow without bound. Files that do not match the YYYY-MM-DD.md
// naming are left untouched. The store lock serializes across Daily instances
// and processes, including console deletes; take it before the instance mutex.
func (d *Daily) Prune(now time.Time) error {
	release, err := lockRoot(d.dir)
	if err != nil {
		return fmt.Errorf("memory: lock daily logs: %w", err)
	}
	defer release()
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
	return d.pruneArchive(now)
}

// pruneArchive deletes archived logs past the delete window. The archive is a
// cooler shelf, not a retention exemption.
func (d *Daily) pruneArchive(now time.Time) error {
	dir := filepath.Join(d.dir, "archive")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("memory: read archive dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		date, ok := parseDailyName(e.Name())
		if !ok {
			continue
		}
		if now.Sub(date) >= dailyDeleteDays*24*time.Hour {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return fmt.Errorf("memory: delete archived daily %s: %w", e.Name(), err)
			}
		}
	}
	return nil
}

func (d *Daily) archive(name string) error {
	dir := filepath.Join(d.dir, "archive")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("memory: create archive dir: %w", err)
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("memory: archive path is not a directory: %v", err)
	}
	src, dst := filepath.Join(d.dir, name), filepath.Join(dir, name)
	// A late append can recreate a warm file for a date already archived.
	// Rename would overwrite the archived file on Unix and silently lose its
	// original entries. Merge before removing the warm copy instead.
	if info, err := os.Lstat(dst); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("memory: archived daily %s is not a regular file", name)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("memory: read daily %s: %w", name, err)
		}
		f, err := os.OpenFile(dst, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("memory: open archived daily %s: %w", name, err)
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return fmt.Errorf("memory: merge archived daily %s: %w", name, writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("memory: close archived daily %s: %w", name, closeErr)
		}
		if err := os.Remove(src); err != nil {
			return fmt.Errorf("memory: remove merged daily %s: %w", name, err)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("memory: stat archived daily %s: %w", name, err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("memory: archive daily %s: %w", name, err)
	}
	return nil
}

// Delete removes a warm daily log by exact date, under the same store lock
// as Append and Prune. A missing log returns os.ErrNotExist to the caller.
func (d *Daily) Delete(name string) error {
	if _, ok := parseDailyName(name + ".md"); !ok || name != filepath.Base(name) {
		return fmt.Errorf("memory: invalid daily name %q", name)
	}
	release, err := lockRoot(d.dir)
	if err != nil {
		return fmt.Errorf("memory: lock daily logs: %w", err)
	}
	defer release()
	d.mu.Lock()
	defer d.mu.Unlock()
	path := filepath.Join(d.dir, name+".md")
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("memory: daily %s is not a regular file", name)
	}
	return os.Remove(path)
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
