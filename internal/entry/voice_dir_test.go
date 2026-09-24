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
	// macOS temp dirs sit under /var → /private/var: compare canonical paths.
	want, _ := filepath.EvalSymlinks(sidecars)
	if got := resolveVoiceDir(); got != want {
		t.Fatalf("resolveVoiceDir = %q, want ancestor match %q", got, want)
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
	got := resolveVoiceDir()
	base := dir
	if rw, err := filepath.EvalSymlinks(dir); err == nil {
		base = rw // EvalSymlinks on the missing leaf fails; canonicalize the dir
	}
	want := filepath.Join(base, "extensions", "voice-nonexistent")
	if got != want {
		t.Fatalf("resolveVoiceDir = %q, want cwd-absolute fallback %q", got, want)
	}
}
