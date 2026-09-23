// Package doctor is the structured self-check suite shared by `panda doctor`
// (which renders the checks as a terminal report) and the panel's
// /api/doctor (which serializes them to JSON). Each check is a stable i18n
// key plus ordered interpolation pairs, so the two surfaces can never drift
// on what is checked or how it reads — only on how it is painted.
package doctor

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/carddetect"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/install"
	"github.com/Xustalis/OpenPanda/internal/providers"
	"github.com/Xustalis/OpenPanda/internal/pyexec"
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
