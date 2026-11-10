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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	"github.com/Xustalis/OpenPanda/internal/storage"
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
		// Load synthesizes defaults when the file is missing and returns no
		// error, so "loaded" is not "a config exists". Say which it is: on a
		// fresh machine the report used to claim a config the user had never
		// written.
		resolved := config.ResolvePath(configPath)
		if _, statErr := os.Stat(resolved); statErr != nil {
			add(pass("doctor.config.defaults", "path", resolved))
		} else {
			add(pass("doctor.config.ok", "path", resolved, "name", cfg.Node.Name))
		}
		if st, err := os.Stat(cfg.Storage.DBPath); err == nil {
			add(pass("doctor.db.ok", "path", cfg.Storage.DBPath, "size", fmt.Sprintf("%d B", st.Size())))
			if c := checkSchemaVersion(cfg); c.Key != "" {
				add(c)
			}
			if c := checkDBGrowth(cfg); c.Key != "" {
				add(c)
			}
			if c := checkMesh(cfg); c.Key != "" {
				add(c)
			}
			// Discovery is checked last among the network rows: its verdict
			// ("nobody is listening for beacons") only makes sense once the
			// mesh and database states above are known.
			if c := checkDiscovery(cfg); c.Key != "" {
				add(c)
			}
		} else {
			// A missing database on a writable path is the state every fresh
			// `panda init` leaves behind: the daemon creates it on first run.
			// Calling that a failed self-check made a healthy install look
			// broken; an unwritable path is still a real failure.
			if databaseDirWritable(cfg.Storage.DBPath) {
				add(pass("doctor.db.uninitialized", "path", cfg.Storage.DBPath))
			} else {
				add(fail("doctor.db.no", "path", cfg.Storage.DBPath))
			}
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
			// Three verdicts, not two: "unreachable" and "reachable" do not
			// cover "reachable but the endpoint rejected our credential"
			// (a different fix — the key, not the route) or the keyless-✓
			// case where a 401 only proves the service is up.
			switch {
			case v.Rejected:
				add(fail("doctor.modelendpoint.rejected", "url", ep, "detail", v.Detail))
			case v.OK && endpointProbeKeyless(v.Detail, cfg.Model.APIKey):
				add(pass("doctor.modelendpoint.keyless", "url", ep, "ms", fmt.Sprintf("%d", time.Since(start).Milliseconds()), "detail", v.Detail))
			case v.OK && probeRefusedRequest(v.Status):
				// The endpoint answered, but it refused the probe's own request
				// (a bad or missing model name, an oversized body). Routing
				// keeps calling that alive on purpose — a harness may run a
				// model the config never names — so this stays a pass. The
				// wording is the fix: a bare "http 400" on a green line reads as
				// noise, while "rejected the request, check the model name" is
				// the actual next step, and a typo'd or retired model id is the
				// most common way a configured endpoint turns out to be unusable.
				add(pass("doctor.modelendpoint.refused", "url", ep, "ms", fmt.Sprintf("%d", time.Since(start).Milliseconds()), "detail", v.Detail))
			case v.OK:
				add(pass("doctor.modelendpoint.ok", "url", ep, "ms", fmt.Sprintf("%d", time.Since(start).Milliseconds()), "detail", v.Detail))
			default:
				add(fail("doctor.modelendpoint.no", "url", ep, "detail", v.Detail))
			}
		} else if c, show := missingEndpointCheck(cfg.Model); show {
			add(c)
		}

		// Exposure is the single truth for every bind: report the effective
		// mode next to the sockets it produced. A legacy config is only a
		// finding when it actually listens wide — an upgrade that kept
		// `listen_addr: 127.0.0.1:7836` wrote its binds out by hand and is
		// as closed as a loopback install, so failing it would teach people
		// to ignore the check.
		exposure := cfg.Network.Exposure()
		switch {
		case exposure == config.ExposureLegacy && !narrowBinds(cfg.Network):
			add(fail("doctor.network.exposure.legacy",
				"addr", cfg.Network.ListenAddr, "panel", cfg.Network.PanelAddr))
		default:
			add(pass("doctor.network.exposure",
				"mode", exposure, "addr", cfg.Network.ListenAddr,
				"panel", cfg.Network.PanelAddr, "udp", cfg.Network.UDPListen))
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
			for _, peer := range cfg.Network.PeerAddrs() {
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

		// LAN discovery can see a signed beacon from a machine that never
		// completed pairing. Its hint row is NOT a configured peer, so the
		// probe loop above never looks at it: `panda status` happily prints
		// "✓ signed beacon" while the mesh stays down and auto-dial retries
		// a wrong-secret handshake for hours. Surface those unpaired leads
		// here with the exact remediation. Read-only SQL on purpose —
		// ledger.ListPending sweeps expired rows and doctor opens mode=ro.
		unpaired := func() []string {
			roDB, e := sql.Open("sqlite",
				fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", escapeURIPath(cfg.Storage.DBPath)))
			if e != nil {
				return nil
			}
			defer roDB.Close()
			rows, e := roDB.Query(`SELECT id, addr FROM pending_nodes WHERE last_seen > ? ORDER BY last_seen DESC`,
				time.Now().Add(-2*time.Minute).Unix())
			if e != nil {
				return nil
			}
			defer rows.Close()
			peers := cfg.Network.PeerAddrs()
			covers := func(addr string) bool {
				host := addr
				if h, _, err := net.SplitHostPort(addr); err == nil {
					host = h
				}
				for _, p := range peers {
					if p == addr || (host != "" && strings.Contains(p, host)) {
						return true
					}
				}
				return false
			}
			var ids []string
			for rows.Next() {
				var id, addr string
				if rows.Scan(&id, &addr) != nil {
					continue
				}
				if !covers(addr) && !covers(id) {
					ids = append(ids, id)
				}
			}
			return ids
		}()
		if len(unpaired) > 0 {
			add(fail("doctor.mesh.discovered.unpaired",
				"count", strconv.Itoa(len(unpaired)), "nodes", strings.Join(unpaired, ", ")))
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
	// The check reports the directory the EXECUTOR would use, not the one this
	// probe would pick for copying: commander.resolveAdapterDir applies the
	// dev-adapter trust gate and the cwd → ancestors → beside-the-binary
	// order, so a daemon started from $HOME and a `panda doctor` run from a
	// repository root can disagree. Printing this probe's answer made doctor
	// vouch for a directory no task would ever load.
	//
	// A path is not evidence. resolveAdapterDir always answers with something —
	// its last resort is "where an install would put it" — so a pass on
	// dir != "" reported a healthy adapters tree on a machine that had none,
	// and the resulting task failure looked like an agent bug. The check now
	// counts the scripts the executor would actually spawn.
	if dir := commander.AdapterDir(); dir != "" {
		if n := adapterScriptCount(dir); n > 0 {
			add(pass("doctor.adapters.ok", "path", dir, "count", strconv.Itoa(n)))
		} else {
			add(fail("doctor.adapters.empty", "path", dir))
		}
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

// endpointProbeKeyless reports the confusing-✓ case: the endpoint answered an
// auth-class status (401/402/403/429) while no credential was probed, so the
// verdict is "the service is up" — not "the configured model would answer".
// A bare ✓ next to "http 401" reads as both pass and failure at once.
func endpointProbeKeyless(detail, apiKey string) bool {
	if apiKey != "" {
		return false
	}
	code, ok := strings.CutPrefix(detail, "http ")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(strings.Fields(code)[0])
	if err != nil {
		return false
	}
	return n == 401 || n == 402 || n == 403 || n == 429
}

// probeRefusedRequest reports whether the probe's status is a client error
// that refused the request itself: a bad model name (400/404), an oversized
// body (413), or an unprocessable payload (422). Credential failures and rate
// limits are excluded — those have their own sentences, and 429 says "busy",
// not "wrong".
func probeRefusedRequest(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound,
		http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// missingEndpointCheck explains a model that names no destination. Run only
// probes the endpoint when EffectiveBaseURL resolves to something, so before
// this check an unresolvable endpoint was silence: a row whose catalogue entry
// declares no endpoint (`custom`) and a config with no base_url still read as
// "key configured" while every agent run went out un-injected. The refusal
// existed in the injection path but nothing on the diagnostic surface said
// why, so the fix here is the missing sentence rather than a new behaviour.
//
// The resolver guard is part of the predicate, not of the call site: called on
// its own, this must never claim "no endpoint" about a model that has one,
// whichever branch Run happens to take.
//
// Silent when the node names no model at all: a fresh install has nothing to
// explain, and doctor.modelkey.no already reports that the model is
// unconfigured. `panda model add` is named because it is the only writer that
// validates a URL, so pointing at it is a fix, not a suggestion.
func missingEndpointCheck(m config.ModelConfig) (Check, bool) {
	if commander.EffectiveBaseURL(m) != "" {
		return Check{}, false
	}
	switch {
	case strings.TrimSpace(m.Provider) != "":
		return fail("doctor.modelendpoint.none", "provider", strings.TrimSpace(m.Provider)), true
	case strings.TrimSpace(m.BaseURL) != "" || strings.TrimSpace(m.APIKey) != "":
		return fail("doctor.modelendpoint.unset"), true
	}
	return Check{}, false
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

// adapterScriptCount reports how many adapter scripts the executor would find
// in dir: the .py files it can spawn, not every file that happens to live
// there. A directory holding only __pycache__ or a README is an install that
// failed halfway, and calling that healthy is the same false green as the
// empty-path check it replaces.
func adapterScriptCount(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		n++
	}
	return n
}

// AdaptersDir locates an adapters/ directory to COPY FROM — the checkout's or
// the install's, relative to the working directory first (the documented
// run-from-repo-root layout), then next to the executable. `panda install` uses
// it as the source for the install prefix.
//
// It is deliberately NOT the runtime answer: commander.AdapterDir applies the
// dev-adapter trust gate and orders cwd → ancestors → beside-the-binary, so a
// daemon started from $HOME and this probe started in a repository root can
// name different directories. Asking this function "which adapters will a task
// load?" is how doctor once vouched for a directory no task would ever read.
// A packaged install (Homebrew or the one-click script) symlinks the binary
// onto PATH, so the link is followed before probing. Empty when not found.
func AdaptersDir() string {
	candidates := []string{"adapters"}
	if exe, err := os.Executable(); err == nil {
		real := exe
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			real = r
		}
		// Follow the PATH symlink first: a packaged install puts the real
		// binary and its adapters/ under the prefix, while the symlink's own
		// directory may hold a stale adapters/ from an older layout.
		for _, base := range []string{real, exe} {
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

// checkSchemaVersion reports a database written by a NEWER binary than the one
// on this machine. storage.Migrate refuses that data directory outright, so the
// daemon exits at startup — and under launchd/systemd KeepAlive it exits again
// on every restart interval, forever, with nothing in the operator's vocabulary
// saying why. `panda doctor` is where that becomes one line instead of a wall of
// identical stderr.
//
// The comparison is against storage.LatestVersion() (this binary's migration
// ceiling) rather than a literal, so the answer cannot drift from the migrations.
func checkSchemaVersion(cfg *config.Config) Check {
	db, err := sql.Open("sqlite",
		fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", escapeURIPath(cfg.Storage.DBPath)))
	if err != nil {
		return Check{}
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return Check{}
	}
	latest := storage.LatestVersion()
	if version > latest {
		return fail("doctor.db.schema.no",
			"db", strconv.Itoa(version), "binary", strconv.Itoa(latest))
	}
	return pass("doctor.db.schema.ok", "db", strconv.Itoa(version), "binary", strconv.Itoa(latest))
}

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

// databaseDirWritable reports whether the DB's parent directory exists and
// accepts a file. A missing DB there is "not initialized yet", not a fault.
func databaseDirWritable(dbPath string) bool {
	parent := filepath.Dir(dbPath)
	st, err := os.Stat(parent)
	if err != nil || !st.IsDir() {
		return false
	}
	f, err := os.CreateTemp(parent, ".panda-doctor-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// escapeURIPath keeps a database path inside a file: URI intact — the same
// three characters storage.escapeDBPath encodes, duplicated here so doctor
// stays free of a storage dependency for two queries.
func escapeURIPath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

// narrowBinds reports whether every socket a config will open stays on the
// loopback interface. An empty udp_listen is NOT narrow: the daemon materialises
// it as ":" + the WS port (see cmd/panda/main.go), i.e. every interface — the
// single most common way a pre-0.1.5 config was wider than its author thought.
func narrowBinds(n config.NetworkConfig) bool {
	if !config.IsLoopbackBind(n.ListenAddr) {
		return false
	}
	if n.PanelAddr != "" && !config.IsLoopbackBind(n.PanelAddr) {
		return false
	}
	switch n.UDPListen {
	case "off":
		return true
	case "":
		return false
	default:
		return config.IsLoopbackBind(n.UDPListen)
	}
}
