// SPDX-License-Identifier: AGPL-3.0-or-later

// Package doctor is the structured self-check suite shared by `panda doctor`
// (which renders the checks as a terminal report) and the panel's
// /api/doctor (which serializes them to JSON). Each check is a stable i18n
// key plus ordered interpolation pairs, so the two surfaces can never drift
// on what is checked or how it reads — only on how it is painted.
package doctor

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/carddetect"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/install"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/providers"
	"github.com/Xustalis/OpenPanda/internal/pyexec"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// Check is one self-check line: the i18n key that describes it, the ordered
// key/value pairs the message interpolates, and whether it passed.
type Check struct {
	Key   string   `json:"key"`
	OK    bool     `json:"ok"`
	Pairs []string `json:"pairs,omitempty"` // flattened k,v,k,v for i18n.Tf
}

// pass/fail build Check values; pairs are flattened k,v strings.
func pass(key string, pairs ...string) Check { return Check{Key: key, OK: true, Pairs: pairs} }
func fail(key string, pairs ...string) Check { return Check{Key: key, OK: false, Pairs: pairs} }

// Run executes every check against the resolved config path and returns the
// ordered results. Problems are the checks with OK=false — count them for
// the exit-code-style summary.
func Run(configPath string) []Check {
	var out []Check
	add := func(c Check) { out = append(out, c) }

	exe, _ := os.Executable()
	add(pass("doctor.exe", "path", exe))

	dir, err := install.Dir()
	if err == nil {
		bin := filepath.Join(dir, install.ExeName())
		if _, err := os.Stat(bin); err == nil {
			if vout, err := install.Verify(bin); err == nil {
				add(pass("doctor.installed", "path", bin, "out", vout))
			} else {
				add(fail("doctor.installed.fail", "path", bin, "err", err.Error()))
			}
		} else {
			add(fail("doctor.notinstalled", "path", bin))
		}
		if lp, err := exec.LookPath(install.ExeName()); err == nil {
			add(pass("doctor.path.ok", "path", lp))
			if where := install.PathPersistedAt(dir); len(where) > 0 {
				add(pass("doctor.persist.ok", "where", joinPaths(where)))
			} else if filepath.Clean(filepath.Dir(lp)) == filepath.Clean(dir) || strings.Contains(os.Getenv("PATH"), dir) {
				add(pass("doctor.persist.ok", "where", "$PATH environment"))
			} else {
				add(fail("doctor.persist.no"))
			}
		} else {
			add(fail("doctor.path.no"))
			add(fail("doctor.persist.no"))
		}
	} else {
		add(fail("doctor.persist.no"))
	}

	if cfg, err := config.Load(configPath); err == nil {
		add(pass("doctor.config.ok", "path", config.ResolvePath(configPath), "name", cfg.Node.Name))
		if st, err := os.Stat(cfg.Storage.DBPath); err == nil {
			add(pass("doctor.db.ok", "path", cfg.Storage.DBPath, "size", fmt.Sprintf("%d B", st.Size())))
			if c := checkDBGrowth(cfg); c.Key != "" {
				add(c)
			}
		} else {
			add(fail("doctor.db.no", "path", cfg.Storage.DBPath))
		}
		p, ok := providers.Lookup(cfg.Model.Provider)
		noAuth := cfg.Model.NoAuth || (ok && p.NoAuth)
		if cfg.Model.APIKey != "" || noAuth {
			add(pass("doctor.modelkey.ok"))
		} else {
			add(fail("doctor.modelkey.no"))
		}
		// A key that exists is not a model that answers: probe the endpoint
		// the entry model would call, with the same short budget the
		// pre-dispatch check uses, so "configured but dead" is visible here
		// instead of only after a hung ask.
		if ep := commander.EffectiveBaseURL(cfg.Model); ep != "" {
			start := time.Now()
			v, _ := commander.ProbeModel(commander.ProbeSpec{
				Endpoint:      ep,
				APIType:       cfg.Model.NormalizedAPIType(),
				APIKey:        cfg.Model.APIKey,
				Model:         cfg.Model.Model,
				KeyDefinitive: cfg.Model.APIKey != "",
			})
			if v.OK {
				add(pass("doctor.modelendpoint.ok", "url", ep, "ms", fmt.Sprintf("%d", time.Since(start).Milliseconds()), "detail", v.Detail))
			} else {
				add(fail("doctor.modelendpoint.no", "url", ep, "detail", v.Detail))
			}
		}

		// Network planes. Validate() already guarantees listen_addr and
		// udp_listen parse, so what remains silent is an empty shared_secret
		// — the WS listener then refuses every inbound peer and the daemon
		// only warns once at startup — and an explicit udp_listen=off, which
		// is deliberate but worth surfacing since it disables punching.
		if cfg.Network.SharedSecret == "" {
			add(fail("doctor.network.nosecret"))
		} else {
			add(pass("doctor.network.ok", "addr", cfg.Network.ListenAddr))
		}
		switch cfg.Network.UDPListen {
		case "off":
			add(pass("doctor.udp.off"))
		case "":
			// "" follows listen_addr's port on the wildcard interface.
			if _, port, err := net.SplitHostPort(cfg.Network.ListenAddr); err == nil {
				add(pass("doctor.udp.ok", "addr", ":"+port))
			}
		default:
			add(pass("doctor.udp.ok", "addr", cfg.Network.UDPListen))
		}

		// Mesh link health: every configured peer gets the same handshake
		// self-check `nodes add` runs — WS dial, signed hello, session-AEAD
		// negotiation, cleartext policy — so "paired but silent" is a red
		// line here instead of a WARN buried in a keepalive log. A punch:
		// peer has no TCP to probe; it links on demand over the datagram
		// plane and reports as informational.
		if len(cfg.Network.Peers) > 0 {
			selfID := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())
			card := ledger.Card{Device: cfg.Node.Name, NodeKind: cfg.Node.Kind}
			var pub ed25519.PublicKey
			var priv ed25519.PrivateKey
			if kdb, kerr := sql.Open("sqlite",
				fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", escapeURIPath(cfg.Storage.DBPath))); kerr == nil {
				pub, priv, _ = core.LoadNodeKey(kdb)
				kdb.Close()
			}
			for _, peer := range cfg.Network.Peers {
				if strings.HasPrefix(peer, "punch:") {
					add(pass("doctor.mesh.peer.punch", "peer", peer))
					continue
				}
				res, err := core.ProbePeer(context.Background(), selfID, card, cfg.Model, cfg.Network, peer, pub, priv)
				switch {
				case err != nil:
					add(fail("doctor.mesh.peer.no", "peer", peer, "err", err.Error()))
				case res.Encrypted:
					add(pass("doctor.mesh.peer.enc", "peer", peer, "id", res.PeerID))
				default:
					add(pass("doctor.mesh.peer.plain", "peer", peer, "id", res.PeerID))
				}
			}
		}

		// OS sandbox: "off" is a valid choice and reports as information; a
		// configured mode with no platform backend is a real problem — the
		// confinement the operator asked for is silently absent.
		switch m := cfg.Sandbox.NormalizedMode(); {
		case m == "off":
			add(pass("doctor.sandbox.off"))
		case security.Backend() == "":
			add(fail("doctor.sandbox.nobackend", "mode", m))
		default:
			add(pass("doctor.sandbox.ok", "mode", m, "backend", security.Backend()))
		}
	} else {
		add(fail("doctor.config.no", "err", err.Error()))
	}

	// Adapter runtime: the agent adapters are Python scripts driven by the
	// resolved interpreter (pyexec), and they wrap the agent CLIs. Each is
	// reported; only "no agent CLI at all" counts as a problem — native-only
	// nodes stay valid.
	if py := pyexec.Describe(); py != "" {
		add(pass("doctor.python3.ok", "path", py))
	} else {
		add(fail("doctor.python3.no"))
	}
	if dir := AdaptersDir(); dir != "" {
		add(pass("doctor.adapters.ok", "path", dir))
	} else {
		add(fail("doctor.adapters.no"))
	}
	agentsFound := 0
	for _, k := range agents.Registry() {
		if bin := carddetect.InstalledBinary(k); bin != "" {
			agentsFound++
			lp, _ := exec.LookPath(bin)
			add(pass("doctor.agent.ok", "name", bin, "path", lp))
		} else {
			add(pass("doctor.agent.no", "name", k.PrimaryBinary()))
		}
	}
	if agentsFound == 0 {
		add(fail("doctor.agent.none"))
	}

	return out
}

// Problems counts the failed checks.
func Problems(checks []Check) int {
	n := 0
	for _, c := range checks {
		if !c.OK {
			n++
		}
	}
	return n
}

// AdaptersDir locates the adapters/ directory — relative to the working
// directory first (the documented run-from-repo-root layout), then next to
// the executable (a relocated install). A packaged install (Homebrew or the
// one-click script) symlinks the binary onto PATH, so we follow the link to
// the real binary and probe beside it too. Empty when not found.
func AdaptersDir() string {
	candidates := []string{"adapters"}
	if exe, err := os.Executable(); err == nil {
		real := exe
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			real = r
		}
		for _, base := range []string{exe, real} {
			candidates = append(candidates,
				filepath.Join(filepath.Dir(base), "adapters"),
				filepath.Join(filepath.Dir(base), "..", "adapters"),
				filepath.Join(filepath.Dir(base), "..", "share", "openpanda", "adapters"),
			)
		}
	}
	if ucd, err := os.UserConfigDir(); err == nil && ucd != "" {
		candidates = append(candidates, filepath.Join(ucd, "openpanda", "adapters"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local", "adapters"),
			filepath.Join(home, ".local", "share", "openpanda", "adapters"),
			filepath.Join(home, ".openpanda", "adapters"),
		)
	}
	for _, dir := range candidates {
		if st, err := os.Stat(filepath.Join(dir, "claude_code.py")); err == nil && !st.IsDir() {
			return dir
		}
	}
	return ""
}

func joinPaths(ps []string) string { return strings.Join(ps, ", ") }

// doctorSettledWarn is the settled-history row count where "retention off"
// stops being a preference and becomes a scaling problem: at tens of
// thousands of dead rows every bounded board query still pays the index
// scan, and a full clear becomes a multi-second delete.
const doctorSettledWarn = 20000

// checkDBGrowth counts task/audit rows in the local database and flags the
// one unbounded-growth combination that has a knob: a large settled history
// with retention disabled. It reports a Check{}-with-empty-Key when the
// database cannot be read — the sibling db.ok check already covers
// reachability, so a second red line would only duplicate it.
func checkDBGrowth(cfg *config.Config) Check {
	db, err := sql.Open("sqlite",
		fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", escapeURIPath(cfg.Storage.DBPath)))
	if err != nil {
		return Check{}
	}
	defer db.Close()
	var tasks, settled, audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&tasks); err != nil {
		return Check{}
	}
	// A failed count would report as zero — a schema old enough to lack one
	// of these tables makes any printed number a lie, so stay silent instead.
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE state IN ('done','failed','cancelled','expired')`).Scan(&settled); err != nil {
		return Check{}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&audits); err != nil {
		return Check{}
	}
	if settled > doctorSettledWarn && cfg.Storage.EffectiveTaskRetentionDays() == 0 {
		return fail("doctor.db.growth.no",
			"settled", fmt.Sprintf("%d", settled), "audit", fmt.Sprintf("%d", audits))
	}
	return pass("doctor.db.growth.ok",
		"tasks", fmt.Sprintf("%d", tasks), "settled", fmt.Sprintf("%d", settled),
		"audit", fmt.Sprintf("%d", audits))
}

// escapeURIPath keeps a database path inside a file: URI intact — the same
// three characters storage.escapeDBPath encodes, duplicated here so doctor
// stays free of a storage dependency for two queries.
func escapeURIPath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}
