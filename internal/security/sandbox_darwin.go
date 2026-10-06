//go:build darwin

package security

import (
	"fmt"
	"os/exec"
	"strings"
)

// platformBackend reports "seatbelt" when sandbox-exec is reachable. The tool
// is deprecated-but-present on every shipping macOS; honoring it only when the
// binary resolves keeps the sandbox honest on systems where it was removed.
func platformBackend() string {
	if _, err := exec.LookPath("sandbox-exec"); err == nil {
		return "seatbelt"
	}
	return ""
}

// wrapSubprocess prefixes the command with sandbox-exec -p <profile>. Anything
// the profile denies fails at exec time inside the child (EPERM), which the
// caller's stderr capture surfaces the same way it surfaces any tool error.
// When the mode is off — or sandbox-exec is missing — the command is left
// untouched so a non-off mode degrades to the historical env-filter behavior.
func wrapSubprocess(cmd *exec.Cmd, p Policy) {
	if p.Mode == ModeOff {
		return
	}
	sbx, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return
	}
	target := cmd.Path
	cmd.Path = sbx
	args := []string{sbx, "-p", seatbeltProfile(p), target}
	cmd.Args = append(args, cmd.Args[1:]...)
}

// seatbeltProfile renders a deny-default SBPL profile. Everything not listed
// is denied, which is what turns "write whatever the uid can" into "write the
// task directory plus the whitelisted homes a CLI actually needs".
//
// Ordering is deliberate but not semantic — seatbelt denies win over allows no
// matter where they sit; denies are emitted first purely so a reader sees the
// holes before the surface.
func seatbeltProfile(p Policy) string {
	var sb strings.Builder
	sb.WriteString("(version 1)\n(deny default)\n")

	workDir := p.resolveWorkDir()
	writable := canonicalPaths(append(append([]string{workDir}, p.WritablePaths...), p.WritableFiles...))
	denyWrite := canonicalPaths(p.DenyWritePaths)

	// Holes first. DenyWritePaths lose write access in every mode; under
	// ModeStrict they lose read too, and DenyReadPaths join them. A deny on a
	// path that is also writable still wins — that is what makes it a deny.
	sb.WriteString(";; deny list\n")
	for _, d := range denyWrite {
		for _, dp := range canonicalPaths([]string{d}) {
			fmt.Fprintf(&sb, "(deny file-write* (literal %q) (subpath %q))\n", dp, dp)
		}
	}
	if p.Mode == ModeStrict {
		for _, d := range canonicalPaths(append(denyWrite, p.DenyReadPaths...)) {
			fmt.Fprintf(&sb, "(deny file-read* file-write* (literal %q) (subpath %q))\n", d, d)
		}
	}

	sb.WriteString(";; process lifecycle: fork/exec itself, and signals so job\n")
	sb.WriteString(";; control (and our own process-group cleanup) still works.\n")
	sb.WriteString("(allow process-exec process-fork process-info*)\n")
	sb.WriteString("(allow signal)\n")
	sb.WriteString(";; reads are the baseline — confinement is about writes.\n")
	sb.WriteString("(allow file-read* file-ioctl)\n")
	sb.WriteString(";; writes: scratch dirs, the task dir, the whitelist.\n")
	sb.WriteString("(allow file-write*\n")
	sb.WriteString("        (literal \"/dev/null\") (literal \"/dev/zero\")\n")
	sb.WriteString("        (literal \"/dev/random\") (literal \"/dev/urandom\")\n")
	sb.WriteString("        (subpath \"/dev\")\n")
	sb.WriteString("        (subpath \"/tmp\") (subpath \"/private/tmp\")\n")
	sb.WriteString("        (subpath \"/private/var/tmp\")\n")
	// /var/folders is where per-user TMPDIR and tool caches (xcrun_db-*,
	// compiler caches) live; denying it broke git under Xcode CLT.
	sb.WriteString("        (subpath \"/var/folders\")\n")
	for _, w := range writable {
		// literal covers a bare file (~/.claude.json), subpath the directory
		// tree — emitting both keeps the spelling honest either way.
		fmt.Fprintf(&sb, "        (literal %q) (subpath %q)\n", w, w)
	}
	sb.WriteString(")\n")
	if p.AllowNetwork {
		// Endpoint-level allow-listing stays with NetworkGuard, which decides
		// which model URL the adapter is told about. Allowing the syscall
		// layer here is what lets a CLI reach any host its config names.
		sb.WriteString("(allow network*)\n")
	}
	sb.WriteString("(allow mach-lookup mach-priv-host-port)\n")
	sb.WriteString("(allow sysctl-read)\n")
	sb.WriteString("(allow ipc-posix* pseudo-tty)\n")
	sb.WriteString("(allow system-socket user-preference-read)\n")
	return sb.String()
}
