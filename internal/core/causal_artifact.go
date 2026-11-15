// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/artifact"
)

const artifactMergeVersion = 1
const maxCausalMetadataBytes = 32 << 20

// These versioned structs are internal archive metadata; field names are wire keys.
type fileRevision struct {
	Writer string
	Digest string
	Mode   uint32
}
type causalTree struct {
	Version int
	Plan    string
	Heads   []string
	Parents map[string][]string
	Files   map[string][]fileRevision
}

type artifactMergeError struct{ Reason string }

func (e *artifactMergeError) Error() string { return "stage input merge blocked: " + e.Reason }
func mergeBlocked(format string, args ...any) error {
	return &artifactMergeError{Reason: fmt.Sprintf(format, args...)}
}

func emptyCausalTree(plan string) causalTree {
	return causalTree{Version: artifactMergeVersion, Plan: plan, Parents: map[string][]string{}, Files: map[string][]fileRevision{}}
}
func fileRevisionEqual(a, b fileRevision) bool { return a.Digest == b.Digest && a.Mode == b.Mode }

// Extract verifies the archive hash before metadata is read. Every file and
// tombstone is checked against the materialized tree; unbound sidecars and
// agent prose never become a merge baseline.
func readCausalTree(dir, hash, plan string, entries []artifact.EntryMeta) (causalTree, bool, error) {
	p := filepath.Join(dir, artifact.KernelMetadataFile)
	st, err := os.Stat(p)
	legacy := os.IsNotExist(err)
	if err != nil && !legacy {
		return causalTree{}, false, err
	}
	state := emptyCausalTree(plan)
	if !legacy {
		if st.Size() > maxCausalMetadataBytes {
			return state, false, mergeBlocked("metadata too large")
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return state, false, err
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			return state, false, mergeBlocked("invalid metadata: %v", err)
		}
		if state.Version != artifactMergeVersion || state.Plan != plan {
			return state, false, mergeBlocked("unsupported version or foreign plan")
		}
		if err := validateCausalTree(state); err != nil {
			return state, false, err
		}
	} else {
		writer := "source:" + hash
		state.Heads = []string{writer}
		state.Parents[writer] = nil
	}
	actual, err := artifactTreeFiles(dir)
	if err != nil {
		return state, legacy, err
	}
	if legacy {
		// Root identity comes from the archive, not a receiver's umask.
		for _, e := range entries {
			if rev, ok := actual[e.Path]; ok {
				rev.Mode = uint32(e.ArchiveMode.Perm())
				actual[e.Path] = rev
			}
		}
		for path, rev := range actual {
			rev.Writer = state.Heads[0]
			state.Files[path] = []fileRevision{rev}
		}
		return state, true, nil
	}
	for path, revs := range state.Files {
		expected := revs[0]
		observed, exists := actual[path]
		if expected.Digest == "" {
			if exists {
				return state, false, mergeBlocked("deleted path present: %s", path)
			}
		} else if !exists || expected.Digest != observed.Digest || !causalModeCompatible(expected.Mode, observed.Mode) {
			return state, false, mergeBlocked("file metadata mismatch: %s", path)
		}
		delete(actual, path)
	}
	if len(actual) > 0 {
		return state, false, mergeBlocked("archive contains files missing from metadata")
	}
	return state, false, nil
}

// Extraction may remove bits through umask, and Windows cannot express POSIX
// permissions. Preserve the archive's intended mode across those copies, as
// the existing artifact extractor does for executable bits. Added permissions
// still constitute an explicit mode change on a POSIX executor.
func causalModeCompatible(expected, observed uint32) bool {
	return runtime.GOOS == "windows" || observed & ^expected == 0
}

func validateCausalTree(s causalTree) error {
	if s.Parents == nil || s.Files == nil || len(s.Parents) > 4096 || len(s.Files) > 200000 || len(s.Heads) == 0 {
		return mergeBlocked("invalid metadata dimensions")
	}
	colors := map[string]int{}
	var visit func(string) error
	visit = func(node string) error {
		if node == "" {
			return mergeBlocked("empty causal writer")
		}
		if colors[node] == 1 {
			return mergeBlocked("causal graph cycle")
		}
		if colors[node] == 2 {
			return nil
		}
		parents, ok := s.Parents[node]
		if !ok {
			return mergeBlocked("unknown causal writer %s", node)
		}
		colors[node] = 1
		for _, p := range parents {
			if err := visit(p); err != nil {
				return err
			}
		}
		colors[node] = 2
		return nil
	}
	for n := range s.Parents {
		if err := visit(n); err != nil {
			return err
		}
	}
	reachable := map[string]bool{}
	var mark func(string)
	mark = func(n string) {
		if reachable[n] {
			return
		}
		reachable[n] = true
		for _, p := range s.Parents[n] {
			mark(p)
		}
	}
	for _, h := range s.Heads {
		if _, ok := s.Parents[h]; !ok {
			return mergeBlocked("unknown head")
		}
		mark(h)
	}
	if len(reachable) != len(s.Parents) {
		return mergeBlocked("disconnected causal graph")
	}
	for path, revs := range s.Files {
		if !filepath.IsLocal(filepath.FromSlash(path)) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) != path || strings.Contains(path, "\\") || path == artifact.KernelMetadataFile || len(revs) == 0 || len(revs) > 4096 {
			return mergeBlocked("invalid revision path %q", path)
		}
		for _, r := range revs {
			if !reachable[r.Writer] || !fileRevisionEqual(r, revs[0]) {
				return mergeBlocked("invalid revisions for %s", path)
			}
			if r.Digest != "" {
				if b, err := hex.DecodeString(r.Digest); err != nil || len(b) != sha256.Size {
					return mergeBlocked("invalid digest for %s", path)
				}
			} else if r.Mode != 0 {
				return mergeBlocked("deleted path carries permissions")
			}
			if r.Mode & ^uint32(0777) != 0 {
				return mergeBlocked("invalid file mode")
			}
		}
	}
	return nil
}

func artifactTreeFiles(dir string) (map[string]fileRevision, error) {
	files := map[string]fileRevision{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == artifact.KernelMetadataFile {
			return nil
		}
		if !d.Type().IsRegular() {
			return mergeBlocked("unexpected non-file %s", rel)
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		digest, err := sha256File(p)
		if err != nil {
			return err
		}
		files[rel] = fileRevision{Digest: digest, Mode: uint32(st.Mode().Perm())}
		return nil
	})
	return files, err
}

// Retain maximal causal writers: unchanged ancestor snapshots cannot undo
// descendant edits. Different concurrent results require explicit resolution.
func mergeCausalTrees(plan string, states []causalTree) (causalTree, error) {
	out := emptyCausalTree(plan)
	for _, s := range states {
		for node, parents := range s.Parents {
			if old, ok := out.Parents[node]; ok && !slices.Equal(old, parents) {
				return out, mergeBlocked("writer lineage disagrees: %s", node)
			}
			out.Parents[node] = parents
		}
		out.Heads = append(out.Heads, s.Heads...)
		for path, revs := range s.Files {
			out.Files[path] = append(out.Files[path], revs...)
		}
	}
	sort.Strings(out.Heads)
	out.Heads = slices.Compact(out.Heads)
	// Validate the union before recursively collecting ancestors.
	colors := map[string]int{}
	var visit func(string) bool
	visit = func(n string) bool {
		if colors[n] == 1 {
			return false
		}
		if colors[n] == 2 {
			return true
		}
		colors[n] = 1
		for _, p := range out.Parents[n] {
			if _, ok := out.Parents[p]; !ok || !visit(p) {
				return false
			}
		}
		colors[n] = 2
		return true
	}
	for n := range out.Parents {
		if !visit(n) {
			return out, mergeBlocked("combined causal cycle")
		}
	}
	ancestors := map[string]map[string]bool{}
	var collect func(string) map[string]bool
	collect = func(node string) map[string]bool {
		if m, ok := ancestors[node]; ok {
			return m
		}
		m := map[string]bool{}
		for _, p := range out.Parents[node] {
			m[p] = true
			for a := range collect(p) {
				m[a] = true
			}
		}
		ancestors[node] = m
		return m
	}
	var conflicts []string
	for path, revs := range out.Files {
		byWriter := map[string]fileRevision{}
		for _, r := range revs {
			if old, ok := byWriter[r.Writer]; ok && !fileRevisionEqual(old, r) {
				return out, mergeBlocked("writer content disagrees: %s", path)
			}
			byWriter[r.Writer] = r
		}
		var maximal []fileRevision
		for writer, r := range byWriter {
			superseded := false
			for other := range byWriter {
				if collect(other)[writer] {
					superseded = true
					break
				}
			}
			if !superseded {
				maximal = append(maximal, r)
			}
		}
		sort.Slice(maximal, func(i, j int) bool { return maximal[i].Writer < maximal[j].Writer })
		for _, r := range maximal {
			if !fileRevisionEqual(r, maximal[0]) {
				conflicts = append(conflicts, path)
				break
			}
		}
		out.Files[path] = maximal
	}
	for path, revs := range out.Files {
		if revs[0].Digest == "" {
			continue
		}
		for d := filepath.ToSlash(filepath.Dir(filepath.FromSlash(path))); d != "."; d = filepath.ToSlash(filepath.Dir(filepath.FromSlash(d))) {
			if parent, ok := out.Files[d]; ok && parent[0].Digest != "" {
				conflicts = append(conflicts, path)
				break
			}
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return out, mergeBlocked("conflicting paths: %s", strings.Join(conflicts, ", "))
	}
	return out, nil
}

func (c *Core) hydrateCausalInputs(ctx context.Context, t Task, workDir string) error {
	var states []causalTree
	var dirs []string
	legacyHashes := map[string]bool{}
	defer func() {
		for _, d := range dirs {
			_ = os.RemoveAll(d)
		}
	}()
	for _, in := range t.Inputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir, err := os.MkdirTemp("", "panda-stage-input-*")
		if err != nil {
			return err
		}
		dirs = append(dirs, dir)
		manifest, err := c.artifacts.Extract(in.Hash, dir)
		if err != nil {
			return err
		}
		state, legacy, err := readCausalTree(dir, in.Hash, t.PlanID, manifest.Entries)
		if err != nil {
			return err
		}
		if legacy {
			legacyHashes[in.Hash] = true
		}
		states = append(states, state)
	}
	if len(legacyHashes) > 1 {
		return mergeBlocked("multiple legacy snapshots lack a trustworthy merge baseline")
	}
	for hash := range legacyHashes {
		for _, state := range states {
			if _, ok := state.Parents["source:"+hash]; !ok {
				return mergeBlocked("legacy snapshot is not a shared causal baseline")
			}
		}
	}
	merged, err := mergeCausalTrees(t.PlanID, states)
	if err != nil {
		return err
	}
	next, err := os.MkdirTemp(filepath.Dir(workDir), ".stage-merge-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(next)
	landedModes := map[string]uint32{}
	for path, revs := range merged.Files {
		wanted := revs[0]
		if wanted.Digest == "" {
			continue
		}
		found := false
		for i, s := range states {
			r, ok := s.Files[path]
			if ok && fileRevisionEqual(r[0], wanted) {
				if err := copyRegularFile(filepath.Join(dirs[i], filepath.FromSlash(path)), filepath.Join(next, filepath.FromSlash(path)), os.FileMode(wanted.Mode)); err != nil {
					return err
				}
				found = true
				break
			}
		}
		if !found {
			return mergeBlocked("no file bytes for %s", path)
		}
		st, err := os.Stat(filepath.Join(next, filepath.FromSlash(path)))
		if err != nil {
			return err
		}
		landedModes[path] = uint32(st.Mode().Perm())
	}
	if t.TaskID != "" {
		raw, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		modes, err := json.Marshal(landedModes)
		if err != nil {
			return err
		}
		if _, err := c.db.ExecContext(ctx, "INSERT INTO task_artifact_inputs(task_id,plan_id,metadata_json,landed_modes_json) VALUES(?,?,?,?) ON CONFLICT(task_id) DO UPDATE SET plan_id=excluded.plan_id,metadata_json=excluded.metadata_json,landed_modes_json=excluded.landed_modes_json", t.TaskID, t.PlanID, string(raw), string(modes)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(workDir, e.Name())); err != nil {
			return err
		}
	}
	entries, err = os.ReadDir(next)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(next, e.Name()), filepath.Join(workDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (c *Core) causalOutputBuilder(ctx context.Context, t Task) (artifact.MetadataBuilder, error) {
	state := emptyCausalTree(t.PlanID)
	landedModes := map[string]uint32{}
	var raw, modes string
	err := c.db.QueryRowContext(ctx, "SELECT metadata_json,landed_modes_json FROM task_artifact_inputs WHERE task_id=? AND plan_id=?", t.TaskID, t.PlanID).Scan(&raw, &modes)
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(modes), &landedModes); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	} else if len(t.Inputs) > 0 {
		return nil, mergeBlocked("hydrated input baseline missing")
	}
	writer := t.TaskID + ":" + t.AttemptID
	state.Parents[writer] = slices.Clone(state.Heads)
	state.Heads = []string{writer}
	return func(entries []artifact.EntryMeta) ([]byte, error) {
		actual := map[string]fileRevision{}
		for _, e := range entries {
			actual[e.Path] = fileRevision{Writer: writer, Digest: e.SHA256, Mode: uint32(e.Mode.Perm())}
		}
		for path, revs := range state.Files {
			observed, ok := actual[path]
			if !ok {
				observed = fileRevision{Writer: writer}
			} else if landed, known := landedModes[path]; known && landed == observed.Mode {
				observed.Mode = revs[0].Mode // unchanged locally; retain archive intent
			}
			if !fileRevisionEqual(revs[0], observed) {
				state.Files[path] = []fileRevision{observed}
			}
			delete(actual, path)
		}
		for path, rev := range actual {
			state.Files[path] = []fileRevision{rev}
		}
		if err := validateCausalTree(state); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(state)
		if len(raw) > maxCausalMetadataBytes {
			return nil, mergeBlocked("output metadata too large")
		}
		return raw, err
	}, nil
}

func artifactProtocolOf(spec string) int {
	var s struct {
		Version int `json:"artifact_merge_version"`
	}
	_ = json.Unmarshal([]byte(spec), &s)
	return s.Version
}

func (c *Core) requireArtifactProtocol(ctx context.Context, target, spec string) error {
	version := artifactProtocolOf(spec)
	if version <= 0 {
		return nil
	}
	resolved := c.resolvePin(ctx, target)
	if resolved.self {
		if version <= artifactMergeVersion {
			return nil
		}
	} else if resolved.found && resolved.node.Capacity.ArtifactMergeVersion >= version {
		return nil
	}
	return fmt.Errorf("node %s has not advertised artifact merge protocol %d", target, version)
}
