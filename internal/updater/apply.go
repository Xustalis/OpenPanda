package updater

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/entry"
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

	// Refresh the agent adapters and voice sidecars beside the running binary.
	// They are secondary to the binary, so a failure here is logged, not fatal
	// — the swap already succeeded (and a bare-binary install legitimately has
	// none).
	if err := installAdapters(s); err != nil {
		m.opts.Logger.Warn("update: adapter install failed", "err", err)
	}
	if err := installVoiceScripts(s); err != nil {
		m.opts.Logger.Warn("update: voice sidecar install failed", "err", err)
	}

	// Restart on a slight delay so the HTTP apply response can flush before
	// the process image is replaced. If NoRestart is requested (CLI one-shot),
	// skip restarting.
	if !m.opts.NoRestart {
		go delayedRestart(m)
	}
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

// adapterManifest records the adapter filenames the last update installed.
// A file in the old manifest but absent from the new release was dropped
// upstream and gets removed; files never listed (user-added scripts) are
// left alone — the manifest is what makes deletion safe.
const adapterManifest = ".adapters.manifest"

// voiceManifest plays the same role for extensions/voice.
const voiceManifest = ".voice.manifest"

// installAdapters copies the release's adapters/*.py over the adapter dir the
// running process resolves scripts from, so the updated binary and its adapters
// stay in lock-step. A missing release adapters dir (or an unresolvable target)
// is a no-op.
func installAdapters(s *stagedRelease) error {
	return installResourceDir(s, "adapters", commander.AdapterDir(), "adapters", adapterManifest)
}

// installVoiceScripts does the same refresh for the release's
// extensions/voice/*.py, targeting the directory the voice pipeline resolves
// (see entry.VoiceDir) — a packaged install keeps them at
// <prefix>/extensions/voice.
//
// Unlike adapters, extensions/voice did not ship in older releases, so the
// resolved dir may legitimately not exist yet: when the running binary sits
// in a <prefix>/bin layout the target is created there rather than letting a
// cwd-relative fallback write a stray extensions/ under the daemon's
// working directory.
func installVoiceScripts(s *stagedRelease) error {
	dst := entry.VoiceDir()
	if st, err := os.Stat(dst); err != nil || !st.IsDir() {
		dst = ""
		if exe := runningBinary(); exe != "" && filepath.Base(filepath.Dir(exe)) == "bin" {
			cand := filepath.Join(filepath.Dir(filepath.Dir(exe)), "extensions", "voice")
			if st, err := os.Stat(filepath.Dir(cand)); err == nil && st.IsDir() {
				dst = cand
			}
		}
	}
	if dst == "" {
		return nil
	}
	return installResourceDir(s, filepath.Join("extensions", "voice"), dst, filepath.Join("extensions", "voice"), voiceManifest)
}

// installResourceDir syncs one resource dir (adapters/, extensions/voice/)
// from the staged release over dst, tracking installed filenames in manifest
// so files a new release dropped are removed instead of lingering.
// relFallback is the unresolved relative name the resolver returns when no
// installed dir exists; dst equal to it means "nothing to update".
func installResourceDir(s *stagedRelease, srcRel, dst, relFallback, manifest string) error {
	src := filepath.Join(s.root, srcRel)
	if dst == "" || dst == relFallback {
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
	keep := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".py" {
			continue
		}
		keep[e.Name()] = true
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), 0o644); err != nil {
			return err
		}
	}
	// Reconcile against the previous manifest before writing the new one: a
	// release that drops a script must not leave the old file answering
	// requests forever.
	if old, err := os.ReadFile(filepath.Join(dst, manifest)); err == nil {
		for _, name := range strings.Split(strings.TrimSpace(string(old)), "\n") {
			// The manifest is ours, but only ever holds bare filenames —
			// refuse anything pathlike so a corrupted list cannot remove
			// files outside the resource dir.
			if name == "" || filepath.Base(name) != name {
				continue
			}
			if !keep[name] {
				_ = os.Remove(filepath.Join(dst, name))
			}
		}
	}
	names := make([]string, 0, len(keep))
	for name := range keep {
		names = append(names, name)
	}
	sort.Strings(names)
	return os.WriteFile(filepath.Join(dst, manifest), []byte(strings.Join(names, "\n")+"\n"), 0o644)
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
