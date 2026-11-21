// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

// One-click repairs for the checks that fail in a way this program may
// actually fix. The suite is shared by `panda doctor` and the console's
// /api/doctor, so the repair list lives here too: a check advertises its
// repair through Check.Fix, and both surfaces call Repair with the keys they
// want applied. Anything that needs a human decision (a model key, a URL, an
// installed agent CLI) deliberately carries no Fix — a button that cannot
// succeed is worse than no button.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/install"
)

// Fix ids. Stable identifiers: the console picks its button style from them
// and Repair switches on them, so adding a repair is one const plus one case
// — never a second key list to keep in sync with the checks.
const (
	FixPath      = "path"      // record the install dir on PATH
	FixSecret    = "secret"    // mint network.shared_secret
	FixExposure  = "exposure"  // declare network.exposure_mode
	FixRetention = "retention" // turn task retention back on
	FixAdapters  = "adapters"  // copy adapter scripts into the executor's dir
)

// Mutate is how a repair edits configuration: it applies fn to the live config
// the caller owns and lets fn persist its own file edits, under whatever lock
// that caller holds. The panel passes mutateCfgErr so the running snapshot and
// config.yaml cannot disagree; the CLI loads the file and lets fn write it.
// A nil Mutate makes the config-touching repairs refuse instead of guessing.
type Mutate func(fn func(*config.Config) error) error

// RepairOptions carries the one decision a repair cannot make for the user.
type RepairOptions struct {
	// Exposure is the mode written for a legacy-exposure finding: loopback
	// (default) or lan. Public is deliberately not offered — declaring a
	// public bind is an operator decision with credentials attached.
	Exposure string
	Mutate   Mutate
}

// RepairResult is one applied (or refused) repair, phrased as an i18n key plus
// flattened k,v pairs so the console and the terminal localize it themselves.
type RepairResult struct {
	// Key is the check the repair was run for.
	Key   string   `json:"key"`
	Fix   string   `json:"fix"`
	OK    bool     `json:"ok"`
	Msg   string   `json:"msg"`
	Pairs []string `json:"pairs,omitempty"`
	// Restart marks a repair the running process will not pick up: the
	// listeners, the WS secret and the retention sweeper are all read at
	// startup, so the next start is when the fix takes effect.
	Restart bool `json:"restart,omitempty"`
}

// Repair applies the repair attached to each requested check key, in report
// order. An empty keys slice means "everything fixable" — that is what the
// console's one-click button and `panda doctor --fix` send. Each repair runs
// at most once even when two failing checks share it (path.no and
// persist.no), and a key whose check now passes is ignored: the caller passes
// the report it just rendered, so a stale client cannot re-run a repair for a
// problem that is already gone.
func Repair(checks []Check, configPath string, keys []string, opts RepairOptions) []RepairResult {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			want[k] = true
		}
	}
	all := len(want) == 0
	var out []RepairResult
	applied := make(map[string]bool, len(checks))
	for _, c := range checks {
		if c.OK || c.Fix == "" || applied[c.Fix] {
			continue
		}
		if !all && !want[c.Key] {
			continue
		}
		applied[c.Fix] = true
		out = append(out, applyFix(c, configPath, opts))
	}
	return out
}

// applyFix dispatches one repair and turns its outcome into a result. Every
// branch reports through the same two helpers so no path can return a result
// with an empty message.
func applyFix(c Check, configPath string, opts RepairOptions) RepairResult {
	res := RepairResult{Key: c.Key, Fix: c.Fix}
	ok := func(key string, pairs ...string) RepairResult {
		res.OK, res.Msg, res.Pairs = true, key, pairs
		return res
	}
	fail := func(key string, pairs ...string) RepairResult {
		res.OK, res.Msg, res.Pairs = false, key, pairs
		return res
	}

	switch c.Fix {
	case FixPath:
		dir, err := install.Dir()
		if err != nil {
			return fail("doctor.fix.failed", "err", err.Error())
		}
		files, err := install.AddToPATH(dir)
		if err != nil {
			return fail("doctor.fix.failed", "err", err.Error())
		}
		if len(files) == 0 {
			// The entry was already there; only this process's environment
			// predates it. Saying so beats claiming a write that never
			// happened.
			return ok("doctor.fix.path.kept", "dir", dir)
		}
		return ok("doctor.fix.path.ok", "files", joinPaths(files), "dir", dir)

	case FixSecret:
		if opts.Mutate == nil {
			return fail("doctor.fix.failed", "err", "no config writer")
		}
		err := opts.Mutate(func(cfg *config.Config) error {
			// Minting a fresh secret for a node that has meshed is not a
			// repair — it is an exile. Refuse and let the operator restore
			// the original credential (pairing re-adopts a mesh secret).
			if len(cfg.Network.Peers) > 0 {
				return errMeshedPeers
			}
			if hasMeshHistory(cfg) {
				return errMeshedHistory
			}
			secret, err := newSharedSecret()
			if err != nil {
				return err
			}
			if err := config.UpdateNetworkSection(configPath, config.NetworkConfig{SharedSecret: secret}); err != nil {
				return err
			}
			cfg.Network.SharedSecret = secret
			return nil
		})
		switch {
		case errors.Is(err, errMeshedPeers):
			return fail("doctor.fix.secret.peers")
		case errors.Is(err, errMeshedHistory):
			return fail("doctor.fix.secret.meshed")
		case err != nil:
			return fail("doctor.fix.failed", "err", err.Error())
		}
		res.Restart = true
		return ok("doctor.fix.secret.ok", "path", config.ResolvePath(configPath))

	case FixExposure:
		mode := strings.ToLower(strings.TrimSpace(opts.Exposure))
		if mode == "" {
			mode = config.ExposureLoopback
		}
		if mode != config.ExposureLoopback && mode != config.ExposureLAN {
			return fail("doctor.fix.exposure.invalid", "mode", mode)
		}
		if opts.Mutate == nil {
			return fail("doctor.fix.failed", "err", "no config writer")
		}
		var next config.NetworkConfig
		var addr, panel, udp string
		err := opts.Mutate(func(cfg *config.Config) error {
			n := cfg.Network
			next = config.NetworkConfig{ExposureMode: mode}
			switch mode {
			case config.ExposureLoopback:
				// Same narrowing applyExposure performs, written out so the
				// file says what the daemon will bind instead of leaving a
				// 0.0.0.0 the loader silently overrides.
				next.ListenAddr = config.LoopbackAddr(n.ListenAddr, "7836")
				next.PanelAddr = config.LoopbackAddr(n.PanelAddr, "7840")
				if n.UDPListen == "off" {
					next.UDPListen = "off"
				} else {
					_, port, perr := net.SplitHostPort(next.ListenAddr)
					if perr != nil || port == "" {
						port = "7836"
					}
					// An empty udp_listen means "every interface on the WS
					// port" — the single most common accidental widening.
					next.UDPListen = config.LoopbackAddr(n.UDPListen, port)
				}
			case config.ExposureLAN:
				// Keep the mesh reachable from the LAN — that is what the
				// wide legacy bind already did — but the console stays
				// loopback unless a stable panel_token guards it. lan with a
				// wide panel and no token is a load error; a repair must
				// never brick the next start.
				if n.ListenAddr == "" {
					// A legacy config can declare only the console bind. Name
					// the mesh bind too, so the file and the message agree
					// with what the daemon will listen on.
					next.ListenAddr = "0.0.0.0:7836"
				}
				if n.PanelAddr != "" && !config.IsLoopbackBind(n.PanelAddr) && n.PanelToken == "" {
					next.PanelAddr = config.LoopbackAddr(n.PanelAddr, "7840")
				}
			}
			if err := config.UpdateNetworkSection(configPath, next); err != nil {
				return err
			}
			cfg.Network.ExposureMode = next.ExposureMode
			if next.ListenAddr != "" {
				cfg.Network.ListenAddr = next.ListenAddr
			}
			if next.PanelAddr != "" {
				cfg.Network.PanelAddr = next.PanelAddr
			}
			if next.UDPListen != "" {
				cfg.Network.UDPListen = next.UDPListen
			}
			addr, panel, udp = cfg.Network.ListenAddr, cfg.Network.PanelAddr, cfg.Network.UDPListen
			return nil
		})
		if err != nil {
			return fail("doctor.fix.failed", "err", err.Error())
		}
		res.Restart = true
		return ok("doctor.fix.exposure.ok", "mode", mode, "addr", addr, "panel", panel, "udp", udp)

	case FixRetention:
		if opts.Mutate == nil {
			return fail("doctor.fix.failed", "err", "no config writer")
		}
		days := config.DefaultTaskRetentionDays
		err := opts.Mutate(func(cfg *config.Config) error {
			if err := config.UpdateSectionFieldInt(configPath, []string{"storage"}, "task_retention_days", days); err != nil {
				return err
			}
			d := days
			cfg.Storage.TaskRetentionDays = &d
			return nil
		})
		if err != nil {
			return fail("doctor.fix.failed", "err", err.Error())
		}
		res.Restart = true
		return ok("doctor.fix.retention.ok", "days", strconv.Itoa(days))

	case FixAdapters:
		src := AdaptersDir()
		if src == "" {
			return fail("doctor.fix.adapters.nosrc")
		}
		dst := commander.AdapterDir()
		if dst == "" {
			return fail("doctor.fix.adapters.nosrc")
		}
		n, err := copyAdapterScripts(src, dst)
		if err != nil {
			return fail("doctor.fix.failed", "err", err.Error())
		}
		return ok("doctor.fix.adapters.ok", "count", strconv.Itoa(n), "src", src, "dst", dst)
	}
	return fail("doctor.fix.failed", "err", "unknown fix "+c.Fix)
}

var (
	errMeshedPeers   = errors.New("doctor: node has configured peers")
	errMeshedHistory = errors.New("doctor: node has mesh history")
)

// newSharedSecret mints the same 16-byte hex secret `panda nodes invite` does,
// so a repaired node and a paired node agree on the format.
func newSharedSecret() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// hasMeshHistory reports whether this database has ever seen another node.
// The read is best-effort: a database that cannot be read (or predates the
// table) proves nothing, and the peers check above already covers the common
// case — refusing to mint on an unreadable database would block the very
// first-run repair this exists for.
func hasMeshHistory(cfg *config.Config) bool {
	if cfg.Storage.DBPath == "" {
		return false
	}
	db, err := sql.Open("sqlite",
		fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", escapeURIPath(cfg.Storage.DBPath)))
	if err != nil {
		return false
	}
	defer db.Close()
	selfID := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM employee_cache WHERE id != ?`, selfID).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// copyAdapterScripts copies the adapter scripts (and the release manifest the
// updater uses to manage them) from src into dst. Only regular .py files and
// the manifest move: a stale __pycache__ from another interpreter is worse
// than none, and anything else in the tree is not an adapter.
func copyAdapterScripts(src, dst string) (int, error) {
	if filepath.Clean(src) == filepath.Clean(dst) {
		return 0, fmt.Errorf("doctor: adapter source and target are the same directory (%s)", src)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".py") && name != ".adapters.manifest") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return n, err
		}
		perm := os.FileMode(0o755)
		if info, ierr := e.Info(); ierr == nil {
			perm = info.Mode().Perm()
		}
		if err := os.WriteFile(filepath.Join(dst, name), data, perm); err != nil {
			return n, err
		}
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("doctor: no adapter scripts in %s", src)
	}
	return n, nil
}
