// SPDX-License-Identifier: AGPL-3.0-or-later

package util

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path atomically: the content goes to a temp
// file in the same directory, is fsynced, and then renamed over path (P1-20).
// An in-place os.WriteFile leaves a truncated file behind when the process
// dies mid-write; a same-directory rename is atomic on POSIX and Windows, so
// readers only ever see the old or the new content, never a torn mix.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup; a successful rename makes this a no-op.
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	// Durability of the rename itself needs the directory entry fsynced. Without
	// it a power loss may resurrect the old file (or leave none) even though the
	// data blocks were synced. Best effort: Windows cannot open a directory, and
	// a failure here does not undo a write that already landed.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
