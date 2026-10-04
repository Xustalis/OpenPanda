//go:build linux

package security

import (
	"os"
	"os/exec"
	"path/filepath"
)

// platformBackend reports "bwrap" when bubblewrap is installed. It needs
// unprivileged user namespaces; where a distro disables them bwrap resolves
// but fails at run time, and the failure lands in the child's stderr like any
// other tool error.
func platformBackend() string {
	if _, err := exec.LookPath("bwrap"); err == nil {
		return "bwrap"
	}
	return ""
}

// wrapSubprocess re-roots the command inside a bubblewrap jail: the host tree
// mounts read-only, writable paths get a read-write bind on top, deny-write
// paths get bound read-only back over any writable superset, deny-read paths
// get masked (tmpfs for directories, /dev/null for files). --die-with-parent
// keeps the jail's lifetime tied to ours so a detached child cannot outlive
// the daemon's process-group kill.
func wrapSubprocess(cmd *exec.Cmd, p Policy) {
	if p.Mode == ModeOff {
		return
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return
	}
	workDir := p.resolveWorkDir()
	args := []string{
		"--die-with-parent", "--new-session",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
	}
	for _, w := range canonicalPaths(append([]string{workDir}, p.WritablePaths...)) {
		if ensureBindDir(w) {
			args = append(args, "--bind", w, w)
		}
	}
	for _, f := range canonicalPaths(p.WritableFiles) {
		if ensureBindFile(f) {
			args = append(args, "--bind", f, f)
		}
	}
	for _, d := range canonicalPaths(p.DenyWritePaths) {
		if _, err := os.Stat(d); err == nil {
			args = append(args, "--ro-bind", d, d)
		}
	}
	if p.Mode == ModeStrict {
		for _, d := range canonicalPaths(p.DenyReadPaths) {
			st, err := os.Stat(d)
			if err != nil {
				continue
			}
			if st.IsDir() {
				args = append(args, "--tmpfs", d)
			} else {
				args = append(args, "--ro-bind", "/dev/null", d)
			}
		}
	}
	if !p.AllowNetwork {
		args = append(args, "--unshare-net")
	}
	args = append(args, "--chdir", workDir, "--", cmd.Path)
	args = append(args, cmd.Args[1:]...)
	cmd.Path = bwrap
	cmd.Args = append([]string{bwrap}, args...)
}

// ensureBindDir makes a missing writable directory exist so --bind has
// something to attach to. Existing paths bind whatever they are; a path that
// cannot be created (permission denied on the parent, e.g.) is skipped —
// the child simply sees it read-only.
func ensureBindDir(p string) bool {
	if _, err := os.Stat(p); err == nil {
		return true
	}
	return os.MkdirAll(p, 0o755) == nil
}

// ensureBindFile is ensureBindDir for a bare credential file: the parent is
// made, the file touched empty. The inode type comes from the caller's
// declaration, never from the basename — ".claude" is a directory while
// ".claude.json" is a file, and guessing wrong creates a junk entry that
// both mounts wrong and blocks the real path on the host forever.
func ensureBindFile(p string) bool {
	if _, err := os.Stat(p); err == nil {
		return true
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false
	}
	f, err := os.OpenFile(p, os.O_CREATE, 0o644)
	if err != nil {
		return false
	}
	return f.Close() == nil
}
