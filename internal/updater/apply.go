package updater

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
)

// exeName is the installed binary's file name for this platform.
func exeName() string {
	if runtime.GOOS == "windows" {
		return "panda.exe"
	}
	return "panda"
}

// applyRelease installs the staged release over the running install: the
// binary is swapped atomically, adapter scripts are refreshed in place, and
// the process is restarted so the newly written code runs. It is only reached
// once the queue is idle (Apply gates on it).
func applyRelease(ctx context.Context, m *Manager, s *stagedRelease) error {
	// Replace the running binary. Follow a PATH symlink so the real installed
	// file is what gets swapped, not the link.
	dst := runningBinary()
	newBin := filepath.Join(s.root, "bin", exeName())
	if _, err := os.Stat(newBin); err != nil {
		return fmt.Errorf("release missing binary: %w", err)
	}
	if err := replaceBinary(newBin, dst); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}

	// Refresh the agent adapters beside the running binary. Adapters are
	// secondary to the binary, so a failure here is logged, not fatal — the
	// swap already succeeded (and a bare-binary install legitimately has none).
	if err := installAdapters(s); err != nil {
		m.opts.Logger.Warn("update: adapter install failed", "err", err)
	}

	// Restart on a slight delay so the HTTP apply response can flush before
	// the process image is replaced.
	go delayedRestart(m)
	return nil
}

// runningBinary returns the executable path, resolved through symlinks, so the
// file we replace is the installed copy rather than a PATH link.
func runningBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// installAdapters copies the release's adapters/*.py over the adapter dir the
// running process resolves scripts from, so the updated binary and its adapters
// stay in lock-step. A missing release adapters dir (or an unresolvable target)
// is a no-op.
func installAdapters(s *stagedRelease) error {
	src := filepath.Join(s.root, "adapters")
	dst := commander.AdapterDir()
	if dst == "" || dst == "adapters" {
		return nil
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".py" {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// copyFile streams src to dst via a temp file + rename so a partial copy never
// sits at the destination.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// delayedRestart gives the HTTP server a beat to flush the apply response,
// then replaces this process with the freshly written binary.
func delayedRestart(m *Manager) {
	time.Sleep(300 * time.Millisecond)
	if err := restartSelf(); err != nil {
		m.opts.Logger.Error("update: restart failed", "err", err)
	}
}
