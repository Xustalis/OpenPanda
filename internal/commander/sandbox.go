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

// adapterSandboxPolicy is the base policy plus what this agent's CLI needs:
// its credential dirs writable, everyone else's credential dirs (and the
// platform's secret locations) read-denied under strict.
func adapterSandboxPolicy(adapter, cwd string) security.Policy {
	p := security.DefaultPolicy(cwd)
	p.WritablePaths = append(p.WritablePaths, sharedToolchainPaths()...)
	own := adapterCredentialDirs(adapter)
	p.WritablePaths = append(p.WritablePaths, own...)
	if p.Mode == security.ModeStrict {
		deny := append(security.SystemSecretPaths(), foreignCredentialDirs(adapter)...)
		p.DenyReadPaths = append(p.DenyReadPaths, deny...)
	}
	return p
}

// nativeSandboxPolicy confines a native shell command: workdir-only writes
// plus, under strict, read denial of the OS secrets AND every agent's
// credential dirs — a plain command has no business reading claude's login.
func nativeSandboxPolicy(dir string) security.Policy {
	p := security.DefaultPolicy(dir)
	if p.Mode == security.ModeStrict {
		p.DenyReadPaths = append(p.DenyReadPaths, security.SystemSecretPaths()...)
		p.DenyReadPaths = append(p.DenyReadPaths, allCredentialDirs()...)
	}
	return p
}

// adapterCredentialDirs returns the home-relative directories the agent's
// credential files live in: "~/.codex" for codex's auth.json/config.toml,
// "~/.config/opencode" for opencode's two files, and the bare file itself
// (".claude.json") when the credential sits directly in $HOME.
func adapterCredentialDirs(adapter string) []string {
	k, ok := agents.ByAdapter(adapter)
	if !ok {
		return nil
	}
	return credentialDirs(k.CredentialFiles)
}

func foreignCredentialDirs(adapter string) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range agents.Registry() {
		if k.Adapter == adapter {
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
	return foreignCredentialDirs("")
}

// credentialDirs collapses a credential file list to the paths that need
// write (or deny) coverage: a file nested inside a dir contributes the dir;
// a file sitting at $HOME root contributes itself.
func credentialDirs(files []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range files {
		f = strings.TrimPrefix(filepath.ToSlash(f), "~/")
		dir := path.Dir(f)
		entry := f
		if dir != "." && dir != "/" && dir != "" {
			entry = dir
		}
		if !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	return out
}

// sharedToolchainPaths are the home-relative directories a CLI toolchain
// legitimately writes at run time — package caches, npm/bun shims, Go build
// cache. They are deliberately shared across agents: distinguishing "codex's
// npm cache" from "claude's npm cache" buys nothing, since both are the same
// program.
func sharedToolchainPaths() []string {
	paths := []string{
		".cache", ".config", ".local", ".npm", ".nvm", ".bun", ".deno",
		".cargo", ".rustup", ".m2", ".gradle", ".nuget", ".vscode",
		"go", ".composer", ".gem", ".rbenv", ".pyenv", ".volta",
	}
	if runtime.GOOS == "darwin" {
		paths = append(paths, "Library/Caches", "Library/Application Support/Claude")
	}
	return paths
}
