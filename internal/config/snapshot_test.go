// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestUpdateKeepsPreWriteSnapshot covers the reason the snapshot exists: an
// updater replaces the whole document, so whatever it did not see is gone. The
// copy taken before the write must hold the previous generation, not the new
// one.
func TestUpdateKeepsPreWriteSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("ui:\n  locale: zh-CN\n  onboarded: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := UpdateSectionField(path, []string{"ui"}, "locale", "en"); err != nil {
		t.Fatal(err)
	}

	snaps, err := filepath.Glob(path + ConfigSnapshotMark + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("want exactly one snapshot after one overwrite, got %d (%v)", len(snaps), snaps)
	}
	prev, err := os.ReadFile(snaps[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prev), "locale: zh-CN") {
		t.Errorf("snapshot holds the new generation, not the previous one:\n%s", prev)
	}
	if !strings.Contains(string(prev), "onboarded: true") {
		t.Errorf("snapshot lost a field the edit did not touch:\n%s", prev)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur), "locale: en") {
		t.Errorf("live config did not take the edit:\n%s", cur)
	}
}

// TestSnapshotRingKeepsNewest pins the ring: a wizard run writes several
// times in a row, and pruning must drop the oldest copies rather than the
// file that still matters.
func TestSnapshotRingKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("ui:\n  locale: gen-00\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	writes := configSnapshotKeep + 5
	for i := 1; i <= writes; i++ {
		if err := UpdateSectionField(path, []string{"ui"}, "locale", fmt.Sprintf("gen-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}

	snaps, err := filepath.Glob(path + ConfigSnapshotMark + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != configSnapshotKeep {
		t.Fatalf("ring kept %d snapshots, want %d", len(snaps), configSnapshotKeep)
	}
	// The newest copy is the generation written just before the last one.
	newest := snaps[0]
	for _, s := range snaps[1:] {
		if s > newest {
			newest = s
		}
	}
	data, err := os.ReadFile(newest)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("gen-%02d", writes-1); !strings.Contains(string(data), want) {
		t.Errorf("newest snapshot = %s, want %s:\n%s", filepath.Base(newest), want, data)
	}
	// The oldest generations must be the ones pruned.
	for _, s := range snaps {
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "gen-00\n") || strings.Contains(string(b), "gen-01\n") {
			t.Errorf("old generation survived pruning: %s", filepath.Base(s))
		}
	}
}

// TestSnapshotLeavesHandMadeBackupsAlone guards the prefix choice: the config
// directory is full of config.yaml.bak-* files made by hand, and pruning must
// not treat them as its own.
func TestSnapshotLeavesHandMadeBackupsAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	manual := path + ".bak-before-tunnel-20261023"
	if err := os.WriteFile(manual, []byte("ui:\n  locale: zh-CN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("ui:\n  locale: zh-CN\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < configSnapshotKeep+3; i++ {
		if err := UpdateSectionField(path, []string{"ui"}, "locale", fmt.Sprintf("gen-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(manual); err != nil {
		t.Fatalf("hand-made backup was pruned: %v", err)
	}
}

// TestMissingFileWritesWithoutLosingTheEdit covers the create-from-defaults
// path. The first write materializes the defaults skeleton and the caller's
// edit then replaces it, so one snapshot of that skeleton is expected — what
// must never happen is the write failing, or the copy holding the new value
// (that would mean the snapshot ran after the write).
func TestMissingFileWritesWithoutLosingTheEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := UpdateSectionField(path, []string{"ui"}, "locale", "ja"); err != nil {
		t.Fatal(err)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config was not created: %v", err)
	}
	if got := localeOf(t, cur); got != "ja" {
		t.Fatalf("edit did not land: ui.locale = %q", got)
	}
	snaps, err := filepath.Glob(path + ConfigSnapshotMark + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) > 1 {
		t.Fatalf("one edit on a missing file left %d snapshots: %v", len(snaps), snaps)
	}
	for _, s := range snaps {
		b, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		if localeOf(t, b) == "ja" {
			t.Errorf("snapshot was taken after the write, not before: %s", filepath.Base(s))
		}
	}
}

// localeOf reads ui.locale back out of a config document. Assertions parse
// instead of matching text because the marshaller quotes values it inherited
// a quoted style for (a defaults file emits locale: "" and keeps the quotes).
func localeOf(t *testing.T, data []byte) string {
	t.Helper()
	var doc struct {
		UI struct {
			Locale string `yaml:"locale"`
		} `yaml:"ui"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return doc.UI.Locale
}

// TestWriteRefusesToClearModel covers the failure that motivated the guard: a
// writer working from a stale or default view of the document replaces the
// whole file, and the model section it never saw comes back empty. That is
// silent data loss — the console keeps running, chat just stops answering —
// so the write must fail loudly instead of landing.
func TestWriteRefusesToClearModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	live := "model:\n  api_type: openai\n  base_url: https://api.example.com/v1\n  api_key: sk-live\n  model: gpt-x\n"
	if err := os.WriteFile(path, []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}

	stale := "model:\n  api_type: \"\"\n  base_url: \"\"\n  api_key: \"\"\n  model: \"\"\n  max_tokens: 0\n"
	err := writeConfigFile(path, []byte(stale))
	if err == nil {
		t.Fatal("clearing a configured model was allowed")
	}
	if !strings.Contains(err.Error(), "refusing to clear") {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != live {
		t.Fatalf("file was modified by a refused write:\n%s", got)
	}
}

// TestWriteAllowsModelChange is the other half: replacing one configured model
// with another is the normal edit and must not trip the guard, and neither is
// the first model landing on a file that had none.
func TestWriteAllowsModelChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("model:\n  base_url: https://a.example.com\n  model: a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next := "model:\n  base_url: https://b.example.com\n  model: b\n"
	if err := writeConfigFile(path, []byte(next)); err != nil {
		t.Fatalf("switching models was refused: %v", err)
	}

	fresh := filepath.Join(dir, "fresh.yaml")
	// Valid YAML with no `model:` section: the case this name is about. (A
	// malformed document takes the unparseable branch instead, which proves
	// nothing about a config that simply had no model yet.)
	if err := os.WriteFile(fresh, []byte("ui:\n  locale: en\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigFile(fresh, []byte(next)); err != nil {
		t.Fatalf("first model on a config that had none was refused: %v", err)
	}
}

// TestClearModelSectionIsDeliberate pins the one write the downgrade guard
// lets through: deleting the last model. Without it the registry entry is
// removed while `model:` still points at it, and the TUI discards the refusal.
func TestClearModelSectionIsDeliberate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	configured := []byte("model:\n  base_url: https://a.example.com\n  model: a\n")
	if err := os.WriteFile(path, configured, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClearModelSection(path); err != nil {
		t.Fatalf("clearing the last model was refused: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var id modelIdentity
	if err := yaml.Unmarshal(raw, &id); err != nil {
		t.Fatal(err)
	}
	if id.configured() {
		t.Fatalf("model section still configured after a deliberate clear:\n%s", raw)
	}

	// The ordinary path keeps refusing the same write: an empty model arriving
	// at a document that had one is still treated as a stale-document write.
	guarded := filepath.Join(dir, "guarded.yaml")
	if err := os.WriteFile(guarded, configured, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateModelSection(guarded, ModelConfig{}); err == nil {
		t.Fatal("UpdateModelSection must still refuse to clear a configured model")
	}
}
