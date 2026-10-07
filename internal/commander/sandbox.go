// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// This file turns the deployer's sandbox config plus each agent's credential
// manifest into the security.Policy that ApplyPolicy enforces. Two call sites
// use it:
//
//   - adapter subprocesses get home-write access to their own credential
//     directories (claude needs ~/.claude, codex ~/.codex, …) so logins keep
//     working, and — under strict mode — read+write denial of every OTHER
//     agent's credential directories plus the OS-level secret locations.
//   - native commands get no agent credential access at all: under strict
//     they lose read access to every agent's credential dirs.
//
// Home-relative paths resolve against $HOME inside the sandbox layer, so the
// lists below stay account-agnostic.

// SetSandboxConfig installs the node-wide base policy from config plus the
// node's own protected state paths (database, memory, card, config file —
// the bookkeeping the drift detector and audit chain rely on). Returns the
// backend name actually in effect ("" when the mode is off or the platform
// has no backend) so the caller can log it.
func SetSandboxConfig(sc config.SandboxConfig, protected []string) string {
	mode := security.Mode(sc.NormalizedMode())
	if mode == security.ModeOff {
		security.SetBasePolicy(security.Policy{Mode: security.ModeOff, AllowNetwork: true})
		return ""
	}
	security.SetBasePolicy(security.Policy{
		Mode:           mode,
		AllowNetwork:   sc.NetworkEnabled(),
		WritablePaths:  sc.WritablePaths,
		DenyReadPaths:  sc.DenyReadPaths,
		DenyWritePaths: append(sc.DenyWritePaths, protected...),
	})
	return security.Backend()
}

// ProtectedPaths builds the node-state deny list shared by the daemon and
// the embedded engine (askengine): the database + WAL directory, the memory
// and skills stores, the artifact pool, the config and card files, and the
// arbitration shadow tree — bookkeeping the drift detector and audit chain
// verify, so a sandboxed subprocess must never be able to rewrite it.
func ProtectedPaths(cfg *config.Config, configPath, cardPath string) []string {
	protected := []string{
		filepath.Dir(cfg.Storage.DBPath),
		cfg.Storage.DBPath,
		cfg.Storage.MemoryPath,
		cfg.Storage.ProjectsPath,
		cfg.Storage.SkillsPath,
		cfg.Storage.ArtifactPath,
		cfg.Storage.ContextPath,
		configPath,
		filepath.Join(cfg.Storage.WorkPath, ".panda-shadow"),
	}
	protected = append(protected, cfg.Storage.ArtifactExtraPaths...)
	if cardPath != "" {
		protected = append(protected, cardPath)
	}
	return protected
}

// adapterSandboxPolicy is the base policy plus what this agent's CLI needs:
// its credential dirs writable, everyone else's credential dirs (and the
// platform's secret locations) read-denied under strict. Those same foreign
// credential paths are write-denied in EVERY mode: standard leaves reads
// open as the baseline, but a subprocess must never rewrite credentials it
// does not own — a token under ~/.config/gh or a sibling agent's config is
// as much a takeover vector as a read leak.
func adapterSandboxPolicy(agent, adapter, cwd string) security.Policy {
	p := security.DefaultPolicy(cwd)
	p.WritablePaths = append(p.WritablePaths, sharedToolchainPaths()...)
	dirs, files := adapterCredentialPaths(agent, adapter)
	p.WritablePaths = append(p.WritablePaths, dirs...)
	p.WritableFiles = append(p.WritableFiles, files...)
	p.DenyWritePaths = append(p.DenyWritePaths, security.SystemSecretPaths()...)
	p.DenyWritePaths = append(p.DenyWritePaths, foreignCredentialDirs(agent, adapter)...)
	if p.Mode == security.ModeStrict {
		deny := append(security.SystemSecretPaths(), foreignCredentialDirs(agent, adapter)...)
		p.DenyReadPaths = append(p.DenyReadPaths, deny...)
	}
	return p
}

// nativeSandboxPolicy confines a native shell command: workdir-only writes
// plus, under strict, read denial of the OS secrets AND every agent's
// credential dirs — a plain command has no business reading claude's login.
// The same locations are write-denied in every mode: reading may be the
// standard-mode baseline, but no command may rewrite a credential.
func nativeSandboxPolicy(dir string) security.Policy {
	p := security.DefaultPolicy(dir)
	p.DenyWritePaths = append(p.DenyWritePaths, security.SystemSecretPaths()...)
	p.DenyWritePaths = append(p.DenyWritePaths, allCredentialDirs()...)
	if p.Mode == security.ModeStrict {
		p.DenyReadPaths = append(p.DenyReadPaths, security.SystemSecretPaths()...)
		p.DenyReadPaths = append(p.DenyReadPaths, allCredentialDirs()...)
	}
	return p
}

// adapterCredentialPaths resolves the agent's credential entries split by
// kind: directories (the common case — "~/.codex" for codex's
// auth.json/config.toml, "~/.config/opencode" for opencode's files) and bare
// files sitting at $HOME root (".claude.json"). The split matters for
// sandbox backends that pre-create missing bind sources: a directory gets
// mkdir -p, a file gets touched — a dotfile basename is NOT a file
// signature (".claude" is a directory), so the kind has to come from the
// manifest, not the name.
func adapterCredentialPaths(agent, adapter string) (dirs, files []string) {
	// Lookup, not ByAdapter: generic.py serves many unrelated CLIs, so an
	// adapter-only hit would mount the wrong agent's credential manifest.
	k, ok := agents.Lookup(agent, adapter)
	if !ok {
		return nil, nil
	}
	return splitCredentialDirs(k.CredentialFiles)
}

// foreignCredentialDirs lists every OTHER agent's credential dirs — what
// this run must not rewrite (or read, under strict). The exclusion is the
// resolved agent entry (name + adapter via Lookup), not the adapter alone:
// every generic.py card agent would otherwise exempt each other's secrets.
func foreignCredentialDirs(agent, adapter string) []string {
	self, _ := agents.Lookup(agent, adapter)
	var out []string
	seen := map[string]bool{}
	for _, k := range agents.Registry() {
		if self.Name != "" && k.Name == self.Name {
			continue
		}
		for _, d := range credentialDirs(k.CredentialFiles) {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out
}

func allCredentialDirs() []string {
	// No self to exempt: the union of every agent's credential dirs.
	return foreignCredentialDirs("", "")
}

// credentialDirs collapses a credential file list to the paths that need
// write (or deny) coverage: a file nested inside a dir contributes the dir;
// a file sitting at $HOME root contributes itself. Deny lists use this
// merged form — for denies the dir/file split is irrelevant.
func credentialDirs(files []string) []string {
	dirs, bare := splitCredentialDirs(files)
	return append(dirs, bare...)
}

// splitCredentialDirs is credentialDirs with the two kinds kept apart:
// files nested inside a directory contribute the directory to dirs; files
// sitting at $HOME root contribute themselves to bare.
func splitCredentialDirs(files []string) (dirs, bare []string) {
	seenDirs := map[string]bool{}
	seenFiles := map[string]bool{}
	for _, f := range files {
		f = strings.TrimPrefix(filepath.ToSlash(f), "~/")
		if dir := path.Dir(f); dir == "." || dir == "/" || dir == "" {
			if !seenFiles[f] {
				seenFiles[f] = true
				bare = append(bare, f)
			}
		} else if !seenDirs[dir] {
			seenDirs[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs, bare
}

// sharedToolchainPaths are the home-relative directories a CLI toolchain
// legitimately writes at run time — package caches, npm/bun shims, Go build
// cache. They are deliberately shared across agents: distinguishing "codex's
// npm cache" from "claude's npm cache" buys nothing, since both are the same
// program. ~/.config is deliberately ABSENT: it is a shared config root that
// also holds other tools' credentials (.config/gh, .config/gcloud, …), and
// the one agent storing its own config there (opencode) already gets
// .config/opencode through its credential manifest. A toolchain that truly
// needs more can be named in the deployer's writable_paths.
func sharedToolchainPaths() []string {
	paths := []string{
		".cache", ".local", ".npm", ".nvm", ".bun", ".deno",
		".cargo", ".rustup", ".m2", ".gradle", ".nuget", ".vscode",
		"go", ".composer", ".gem", ".rbenv", ".pyenv", ".volta",
	}
	if runtime.GOOS == "darwin" {
		paths = append(paths, "Library/Caches", "Library/Application Support/Claude")
	}
	return paths
}
