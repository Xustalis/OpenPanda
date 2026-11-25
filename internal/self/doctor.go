// SPDX-License-Identifier: AGPL-3.0-or-later

package self

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Check is one doctor finding. OK=false on a required item means the
// self-evolution pipeline cannot run on this node; a warn-only item (OK=true,
// Note set) is a real cost or partial capability the operator should know.
type Check struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

// Doctor runs the toolchain preflight (docs/design/self-evolution.md): the gate's footprint is
// developer-grade — go, git, python3 for the adapter contract, a C toolchain
// for the CGO race leg, and network on the first staticcheck fetch. The point
// of the report is to answer "can this node self-evolve" before a proposal
// burns a review on a machine that was never going to merge.
func (m *Manager) Doctor(ctx context.Context) []Check {
	var out []Check
	probe := func(name, bin string, args ...string) (string, bool) {
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		o, err := exec.CommandContext(pctx, bin, args...).CombinedOutput()
		if err != nil {
			out = append(out, Check{Name: name, OK: false, Note: fmt.Sprintf("%s: %v", bin, err)})
			return "", false
		}
		out = append(out, Check{Name: name, OK: true, Note: strings.TrimSpace(strings.SplitN(string(o), "\n", 2)[0])})
		return string(o), true
	}

	probe("git", "git", "--version")
	goVer, hasGo := probe("go", "go", "version")
	_ = goVer
	probe("make", "make", "--version")
	probe("python3 (adapter contract)", "python3", "--version")

	// The race-focused gate leg forces CGO_ENABLED=1, which needs a C
	// toolchain even though the project itself is CGO-free — the one gate
	// requirement that surprises on a pure-Go codebase.
	cc := "cc"
	if runtime.GOOS == "windows" {
		cc = "gcc"
	}
	probe("C toolchain (race leg, CGO)", cc, "--version")

	// staticcheck is `go run`ed by the Makefile, so its first-ever invocation
	// downloads the module. Probe the module cache; a miss is a warn because
	// the first gate run will just take longer or fail offline.
	if hasGo {
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		cache, err := exec.CommandContext(pctx, "go", "env", "GOMODCACHE").Output()
		cancel()
		if err == nil {
			hit, _ := os.Stat(filepath.Join(strings.TrimSpace(string(cache)), "honnef.co"))
			out = append(out, Check{
				Name: "staticcheck",
				OK:   true,
				Note: map[bool]string{true: "module cache warm", false: "first `make staticcheck` needs network"}[hit != nil],
			})
		}
	}

	if m.repo == "" {
		out = append(out, Check{Name: "self_evolution.repo", OK: false,
			Note: "not configured — set it to this node's OpenPanda checkout"})
	} else if st, err := os.Stat(filepath.Join(m.repo, ".git")); err != nil || !st.IsDir() {
		out = append(out, Check{Name: "self_evolution.repo", OK: false,
			Note: m.repo + " is not a git checkout"})
	} else {
		out = append(out, Check{Name: "self_evolution.repo", OK: true, Note: m.repo})
	}

	// macOS ad-hoc signing changes cdhash on every rebuild → the TCC local-
	// network grant dies with each self swap (Makefile install-local notes
	// the same for manual installs). Warn so a macOS node knows the cost of
	// every merge approval before it pays it.
	if runtime.GOOS == "darwin" {
		out = append(out, Check{Name: "macOS signing", OK: true,
			Note: "ad-hoc signed builds change cdhash each swap — LAN/TCC grants must be re-approved or a stable signing identity configured (docs/design/self-evolution.md)"})
	}
	return out
}
