// SPDX-License-Identifier: AGPL-3.0-or-later

package self

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/security"
)

// GateLegs is the release gate spelled out as the Makefile's `gate` target
// runs it — seven legs, each invoked through make so the Makefile stays the
// single definition of what "release-equivalent" means. A patch set that
// cannot pass all seven is not mergeable.
var GateLegs = []string{
	"fmt-check",
	"vet",
	"staticcheck",
	"build",
	"adapter-test",
	"test",
	"race-focused",
}

// legTimeout bounds one leg; the race-focused leg dominates the budget on a
// clean tree, and a stuck gate must not hold a merge approval hostage.
const legTimeout = 20 * time.Minute

// RunGate executes the release gate inside dir (normally the merge worktree)
// leg by leg, stopping at the first failure — the digest records how far the
// set got, which is the reviewer's fastest signal for "what broke".
func RunGate(ctx context.Context, dir string) GateDigest {
	d := GateDigest{At: time.Now().Unix(), OK: true}
	if v, err := exec.CommandContext(ctx, "go", "version").Output(); err == nil {
		d.Maker = strings.TrimSpace(string(v))
	}
	for _, leg := range GateLegs {
		lctx, cancel := context.WithTimeout(ctx, legTimeout)
		start := time.Now()
		cmd := exec.CommandContext(lctx, "make", leg)
		cmd.Dir = dir
		// The gate runs patch-authored tests and build scripts. Inheriting the
		// daemon's environment would let them read OPENPANDA_* (panel token,
		// mesh secret, model keys) and cloud credentials — the gate would then
		// certify a patch that exfiltrated them. The sandbox allow-list is the
		// same one every adapter gets.
		cmd.Env = security.NewSandbox(dir).Env()
		out, err := cmd.CombinedOutput()
		cancel()
		l := GateLeg{Name: leg, OK: err == nil, Sec: int64(time.Since(start).Seconds())}
		if err != nil {
			l.Tail = tailLines(string(out), 40)
			d.OK = false
			d.Legs = append(d.Legs, l)
			return d
		}
		d.Legs = append(d.Legs, l)
	}
	return d
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
