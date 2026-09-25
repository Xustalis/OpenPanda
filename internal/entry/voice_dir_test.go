package entry

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveVoiceDirEnvOverride: OPENPANDA_VOICE_DIR wins over every probe —
// it is how integration environments point a packaged binary at controlled
// sidecars.
func TestResolveVoiceDirEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(voiceDirEnv, dir)
	old := voiceDir
	voiceDir = "extensions/voice"
	defer func() { voiceDir = old }()
	if got := resolveVoiceDir(); got != dir {
		t.Fatalf("resolveVoiceDir = %q, want env override %q", got, dir)
	}
}

// TestResolveVoiceDirBesideBinary mirrors the packaged install layout:
// <prefix>/bin/panda + <prefix>/extensions/voice. The probe cannot re-point
// os.Executable, so the exe-relative candidates are exercised indirectly via
// the ancestor walk instead — a voiceDir under a temp "repo" resolves from a
// nested cwd the same way a repo checkout resolves from a subdir.
func TestResolveVoiceDirAncestorWalk(t *testing.T) {
	root := t.TempDir()
	sidecars := filepath.Join(root, "extensions", "voice")
	if err := os.MkdirAll(sidecars, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	old := voiceDir
	voiceDir = "extensions/voice"
	defer func() { voiceDir = old }()
	// Canonicalize both sides: macOS temp dirs sit under /var → /private/var,
	// and Windows runners hand t.TempDir() an 8.3 short name (RUNNER~1) that
	// filepath.Abs keeps verbatim while EvalSymlinks expands — the same
	// directory either way, so only canonical forms may be compared.
	want, got := canonPath(sidecars), canonPath(resolveVoiceDir())
	if got != want {
		t.Fatalf("resolveVoiceDir = %q, want ancestor match %q", got, want)
	}
}

// canonPath canonicalizes a possibly-nonexistent path for comparison:
// EvalSymlinks on the longest existing ancestor (resolving symlinks and,
// on Windows, 8.3 short names), with the missing tail rejoined.
func canonPath(p string) string {
	var tail []string
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return r
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(p)
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// TestResolveVoiceDirFallback: with nothing found the cwd-absolute default
// stands, so a spawn error names a stable path rather than a bare relative
// one re-resolved against wherever the caller happens to be.
func TestResolveVoiceDirFallback(t *testing.T) {
	dir := t.TempDir()
	// Pin HOME/XDG away from the real user dirs: a machine where `panda
	// install` already copied sidecars into the user config dir would
	// otherwise resolve those instead of exercising the fallback.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	old := voiceDir
	voiceDir = "extensions/voice-nonexistent"
	defer func() { voiceDir = old }()
	got := canonPath(resolveVoiceDir())
	want := canonPath(filepath.Join(dir, "extensions", "voice-nonexistent"))
	if got != want {
		t.Fatalf("resolveVoiceDir = %q, want cwd-absolute fallback %q", got, want)
	}
}
