// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/security"
)

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestAdapterCredentialSplitIsDeclaredNotGuessed pins the fix for the Linux
// bind-source bug: ".claude" is a dotfile DIRECTORY while ".claude.json" is a
// bare file, and only the credential manifest knows which is which. The old
// heuristic read filepath.Ext — non-empty for every dotfile — so ~/.claude,
// ~/.codex and every toolchain cache dir were created as regular files,
// mounted wrong, and left a junk file that blocked the real directory on the
// host forever.
func TestAdapterCredentialSplitIsDeclaredNotGuessed(t *testing.T) {
	dirs, files := adapterCredentialPaths("claude_code", "claude_code.py")
	if !contains(dirs, ".claude") {
		t.Fatalf(".claude must come out as a directory: %v", dirs)
	}
	if !contains(files, ".claude.json") {
		t.Fatalf(".claude.json must come out as a bare file: %v", files)
	}
	if contains(files, ".claude") || contains(dirs, ".claude.json") {
		t.Fatalf("kinds crossed: dirs=%v files=%v", dirs, files)
	}

	// The policy carries the two kinds in separate fields so a backend
	// pre-creating missing bind sources makes a directory for one and a file
	// for the other.
	SetSandboxConfig(config.SandboxConfig{Mode: "standard"}, nil)
	t.Cleanup(func() { security.SetBasePolicy(security.Policy{Mode: security.ModeOff, AllowNetwork: true}) })
	p := adapterSandboxPolicy("claude_code", "claude_code.py", "/tmp/work")
	if !contains(p.WritablePaths, ".claude") {
		t.Fatalf(".claude missing from WritablePaths: %v", p.WritablePaths)
	}
	if !contains(p.WritableFiles, ".claude.json") {
		t.Fatalf(".claude.json missing from WritableFiles: %v", p.WritableFiles)
	}

	// Deny lists keep the merged form — for denies the dir/file split is
	// irrelevant and both spellings must appear.
	deny := allCredentialDirs()
	if !contains(deny, ".claude") || !contains(deny, ".claude.json") {
		t.Fatalf("deny list must cover dir and file spellings: %v", deny)
	}
}

// TestStandardModeDeniesCredentialWrites pins the standard-mode posture:
// reads stay the baseline, but a subprocess must never rewrite credentials
// it does not own — the platform's secret locations (SystemSecretPaths, now
// including ~/.config/gh and the login keyring) and every other agent's
// credential paths are write-denied in every mode, while the running
// agent's own credential dir stays writable for login refresh. ~/.config
// itself must NOT be a shared writable root anymore: narrowing it was the
// review fix — opencode's own config under it still arrives through the
// credential manifest.
func TestStandardModeDeniesCredentialWrites(t *testing.T) {
	SetSandboxConfig(config.SandboxConfig{Mode: "standard"}, nil)
	t.Cleanup(func() { security.SetBasePolicy(security.Policy{Mode: security.ModeOff, AllowNetwork: true}) })

	p := adapterSandboxPolicy("claude_code", "claude_code.py", "/tmp/work")
	if contains(p.WritablePaths, ".config") {
		t.Fatalf("shared .config root must not be writable: %v", p.WritablePaths)
	}
	for _, want := range []string{".ssh", ".config/gh", ".local/share/keyrings", ".codex", ".grok"} {
		if !contains(p.DenyWritePaths, want) {
			t.Fatalf("%s must be write-denied for a claude task: %v", want, p.DenyWritePaths)
		}
	}
	if contains(p.DenyWritePaths, ".claude") || contains(p.DenyWritePaths, ".claude.json") {
		t.Fatalf("own credential paths must stay writable: %v", p.DenyWritePaths)
	}
	if len(p.DenyReadPaths) != 0 {
		t.Fatalf("standard mode must not deny reads: %v", p.DenyReadPaths)
	}

	// opencode keeps its own .config subtree through the manifest, not the
	// removed shared root.
	p = adapterSandboxPolicy("opencode", "opencode.py", "/tmp/work")
	if !contains(p.WritablePaths, ".config/opencode") {
		t.Fatalf("opencode's own config dir not writable: %v", p.WritablePaths)
	}

	// Native commands get the same write-denial without any credential
	// writes of their own.
	n := nativeSandboxPolicy("/tmp/work")
	if !contains(n.DenyWritePaths, ".ssh") || !contains(n.DenyWritePaths, ".claude") {
		t.Fatalf("native policy must write-deny secrets and all credentials: %v", n.DenyWritePaths)
	}
}

// TestProtectedPathsCoversNodeState pins the shared deny list the daemon and
// the embedded engine install: the database directory (WAL siblings travel
// with it), the memory/artifact stores, the config and card files, and the
// arbitration shadow tree inside the writable work dir.
func TestProtectedPathsCoversNodeState(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.Storage.DBPath = filepath.Join(root, "data", "openpanda.db")
	cfg.Storage.MemoryPath = filepath.Join(root, "memory")
	cfg.Storage.ProjectsPath = filepath.Join(root, "projects")
	cfg.Storage.SkillsPath = filepath.Join(root, "skills")
	cfg.Storage.ArtifactPath = filepath.Join(root, "artifacts")
	cfg.Storage.ArtifactExtraPaths = []string{filepath.Join(root, "extra")}
	cfg.Storage.ContextPath = filepath.Join(root, "context")
	cfg.Storage.WorkPath = filepath.Join(root, "work")
	configPath := filepath.Join(root, "config.yaml")
	cardPath := filepath.Join(root, "capabilities.yaml")

	got := ProtectedPaths(cfg, configPath, cardPath)
	for _, want := range []string{
		filepath.Dir(cfg.Storage.DBPath), // the whole data/ dir, -wal/-shm included
		cfg.Storage.DBPath,
		cfg.Storage.MemoryPath,
		cfg.Storage.ProjectsPath,
		cfg.Storage.SkillsPath,
		cfg.Storage.ArtifactPath,
		cfg.Storage.ContextPath,
		configPath,
		cardPath,
		filepath.Join(root, "extra"),
		filepath.Join(cfg.Storage.WorkPath, ".panda-shadow"),
	} {
		if !contains(got, want) {
			t.Fatalf("protected list missing %s: %v", want, got)
		}
	}

	// An empty card path contributes nothing (the engine may run card-less
	// until the lazy init), while everything else stays.
	if contains(ProtectedPaths(cfg, configPath, ""), "") {
		t.Fatal("empty card path leaked an empty entry into the deny list")
	}
}
