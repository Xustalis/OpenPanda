// SPDX-License-Identifier: AGPL-3.0-or-later

package config

// Every config updater round-trips the YAML document and replaces the whole
// file. That makes a write destructive in a way the call sites don't look:
// the thing being edited is one field, but what lands on disk is the entire
// document as the writer saw it. When the writer saw less than the file held
// — the defaults skeleton materialized for a missing file, a second `panda
// web` racing the same path, a restore landing mid-edit — the difference is
// silently deleted, and the first symptom is behavioural rather than an
// error: the console no longer sees a consent record and reopens the
// first-run wizard.
//
// Keeping the last few pre-write copies turns that from archaeology into a
// one-command restore. The copies are best effort by design: a failed copy
// must never block the write the caller actually asked for.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/util"
	"gopkg.in/yaml.v3"
)

const (
	// configSnapshotKeep is how many pre-write copies survive. One wizard run
	// writes 4-5 times in a row (locale, terms, terms version, timestamp,
	// model), so the ring has to be deeper than a single session's noise.
	configSnapshotKeep = 20
	// ConfigSnapshotMark separates the copies from the file itself. It is
	// deliberately not ".bak-": the hand-made backups next to a real config
	// use that prefix, and pruning must never delete one of those.
	ConfigSnapshotMark = ".snap-"
	// configSnapshotStamp is zero-padded and fixed width so the names sort
	// chronologically as plain strings — pruning never has to stat a file.
	configSnapshotStamp = "20060102-150405.000"
)

// writeConfigFile replaces the config at path, keeping a copy of what was
// there first. It refuses one specific write: a document that would leave the
// model section empty while the file on disk has an endpoint or a model name.
//
// That is never a real edit. Every surface that changes the model fills the
// section in (the wizard's model step, 设置 → 模型, a registry switch), and
// there is no "forget my model" button — so an empty model arriving at a file
// that had one means the writer was working from a stale or default view of
// the document (a second instance that booted against a skeleton, a restore
// landing mid-edit). Writing it deletes a working provider and the only
// symptom is that chat stops answering; the snapshot ring keeps a copy, but
// the operator has to notice first. Failing the write instead turns a silent
// data loss into a visible error on the surface that tried it.
func writeConfigFile(path string, data []byte) error {
	if err := guardModelDowngrade(path, data); err != nil {
		return err
	}
	snapshotConfig(path)
	return util.WriteFileAtomic(path, data, 0o600)
}

// writeConfigFileAllowingClear is writeConfigFile without the downgrade guard,
// for the one deliberate clear (ClearModelSection). Everything else keeps the
// guard: an empty model arriving at a file that had one is a stale-document
// write, not an edit.
func writeConfigFileAllowingClear(path string, data []byte) error {
	snapshotConfig(path)
	return util.WriteFileAtomic(path, data, 0o600)
}

// modelIdentity is the minimum a config has to say to count as "a model is
// configured here" — the pair the console keys the whole chat surface off.
type modelIdentity struct {
	Model struct {
		BaseURL string `yaml:"base_url"`
		Model   string `yaml:"model"`
	} `yaml:"model"`
}

func (m modelIdentity) configured() bool {
	return strings.TrimSpace(m.Model.BaseURL) != "" || strings.TrimSpace(m.Model.Model) != ""
}

// guardModelDowngrade reports an error when next would clear a model that the
// file at path still has. Unreadable or unparseable files are not evidence of
// anything, so they let the write through — this guard only speaks for a
// document it could actually read.
func guardModelDowngrade(path string, next []byte) error {
	prevRaw, err := os.ReadFile(path)
	if err != nil || len(prevRaw) == 0 {
		return nil
	}
	var prev, incoming modelIdentity
	if yaml.Unmarshal(prevRaw, &prev) != nil || !prev.configured() {
		return nil
	}
	if yaml.Unmarshal(next, &incoming) != nil || incoming.configured() {
		return nil
	}
	return fmt.Errorf(
		"refusing to clear the configured model in %s (disk has %q on %s): the new document carries an empty model section",
		path, prev.Model.Model, prev.Model.BaseURL,
	)
}

// snapshotConfig copies the current config aside and prunes the ring. Every
// failure is silent: the caller's write is what matters, and a read-only or
// missing file simply has nothing to preserve.
func snapshotConfig(path string) {
	prev, err := os.ReadFile(path)
	if err != nil || len(prev) == 0 {
		return
	}
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	stamp := time.Now().Format(configSnapshotStamp)
	// Two writes inside one millisecond (a wizard step saving locale and then
	// terms) would collide on the stamp; suffix the name until O_EXCL wins.
	for i := 0; i <= 50; i++ {
		name := base + ConfigSnapshotMark + stamp
		if i > 0 {
			name = fmt.Sprintf("%s%s%s-%02d", base, ConfigSnapshotMark, stamp, i)
		}
		f, cerr := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if cerr != nil {
			if os.IsExist(cerr) {
				continue
			}
			return
		}
		_, werr := f.Write(prev)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(filepath.Join(dir, name))
			return
		}
		break
	}
	pruneSnapshots(dir, base)
}

// pruneSnapshots keeps the newest configSnapshotKeep copies and deletes the
// rest. Only files this package named are candidates — a hand-made
// config.yaml.bak-* is never touched.
func pruneSnapshots(dir, base string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := base + ConfigSnapshotMark
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) <= configSnapshotKeep {
		return
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, n := range names[configSnapshotKeep:] {
		_ = os.Remove(filepath.Join(dir, n))
	}
}
