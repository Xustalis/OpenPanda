package security

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Mode selects how hard ApplyPolicy confines a subprocess. ModeOff keeps the
// historical behavior — environment filtering and cwd only. ModeStandard adds
// the platform's deny-default file-write confinement plus explicit deny lists.
// ModeStrict additionally denies reads of credential-shaped paths so a
// subprocess cannot exfiltrate secrets that standard mode still lets it read.
type Mode string

const (
	ModeOff      Mode = "off"
	ModeStandard Mode = "standard"
	ModeStrict   Mode = "strict"
)

// Policy is the resolved confinement for one subprocess launch. The base
// policy (SetBasePolicy) carries the deployer's choices; per-callsite code
// derives the final policy from it (adapter credential dirs, extra deny lists).
type Policy struct {
	Mode Mode
	// AllowNetwork gates outbound networking as a whole; endpoint-level
	// filtering stays with NetworkGuard.
	AllowNetwork bool
	// WorkDir is the subprocess's cwd and is always writable. Empty means
	// "wherever the daemon would run it" — resolved to the daemon cwd.
	WorkDir string
	// WritablePaths get write access beyond WorkDir. Absolute paths are used
	// as-is; relative paths and "~/…" resolve against the user's home —
	// that convention lets config files and credential manifests name
	// "~/.claude" without knowing the account name.
	WritablePaths []string
	// DenyReadPaths lose both read and write access under ModeStrict (and
	// keep write-denied only under ModeStandard — reading is the baseline
	// there). Home-relative spelling follows WritablePaths.
	DenyReadPaths []string
	// DenyWritePaths lose write access under every mode. The node's own
	// state (database, memory, config) lands here so a subprocess cannot
	// corrupt bookkeeping the drift detector relies on.
	DenyWritePaths []string
}

var (
	policyMu   sync.RWMutex
	basePolicy = Policy{Mode: ModeOff, AllowNetwork: true}
)

// SetBasePolicy installs the deployer-level sandbox settings every ApplyPolicy
// call starts from. Called once during daemon wiring.
func SetBasePolicy(p Policy) {
	if p.Mode != ModeStandard && p.Mode != ModeStrict {
		p.Mode = ModeOff
	}
	policyMu.Lock()
	basePolicy = p
	policyMu.Unlock()
}

// BasePolicy returns a copy of the configured base policy.
func BasePolicy() Policy {
	policyMu.RLock()
	defer policyMu.RUnlock()
	return basePolicy
}

// DefaultPolicy derives the launch policy for a subprocess whose working
// directory is workDir — the shape every caller shares before adding its own
// writable/deny entries.
func DefaultPolicy(workDir string) Policy {
	p := BasePolicy()
	p.WorkDir = workDir
	return p
}

// Backend names the OS confinement the platform implements: "seatbelt" on
// macOS (sandbox-exec), "bwrap" on Linux (bubblewrap), "" elsewhere or when
// the tool is absent — in which case a non-off mode is a no-op and callers
// should surface that rather than pretend isolation exists.
func Backend() string { return platformBackend() }

// homeRelative resolves a policy path: absolute stays absolute, "~/…" and bare
// relative names resolve against the account's home directory.
func homeRelative(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			return filepath.Join(h, p[1:])
		}
		return p
	}
	if filepath.IsAbs(p) {
		return p
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, p)
	}
	return p
}

// canonicalPaths expands each entry to the spellings a sandbox profile needs:
// the cleaned path plus its symlink-resolved form, because filesystem
// confinement matches on the resolved path while callers (and users) name the
// raw one.
func canonicalPaths(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		p = homeRelative(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
		}
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
		if r, err := filepath.EvalSymlinks(p); err == nil && r != p && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// resolveWorkDir returns the directory a subprocess should be confined to:
// the policy's WorkDir, falling back to the daemon's own cwd when unset.
func (p Policy) resolveWorkDir() string {
	if p.WorkDir != "" {
		return filepath.Clean(p.WorkDir)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// SystemSecretPaths lists the home-relative locations where this platform
// family keeps reusable credentials. ModeStrict denies subprocesses read and
// write access to them; the list errs toward the well-known names rather than
// completeness.
func SystemSecretPaths() []string {
	paths := []string{
		".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker",
		".config/gcloud", ".config/gh", ".config/hub", ".config/1password",
		".netrc", ".git-credentials", ".gitconfig.local",
		".pgpass", ".my.cnf", ".s3cfg", ".boto", ".gsutil",
		".pki", ".cert", ".config/certbot",
		".terraform.d", ".vault-token", ".minio",
		".config/rclone", ".config/s3fs", ".credentials",
		".bash_history", ".zsh_history", ".mysql_history", ".psql_history",
		".python_history", ".node_repl_history", ".sqlite_history",
		".rediscli_history", ".lesshst", ".viminfo",
		".bash_sessions", ".zsh_sessions", ".local/share/fish/fish_history",
	}
	if runtime.GOOS == "darwin" {
		paths = append(paths,
			"Library/Keychains", "Library/Application Support/1Password",
			"Library/Cookies", "Library/Group Containers/group.com.apple.ssh")
	}
	return paths
}
