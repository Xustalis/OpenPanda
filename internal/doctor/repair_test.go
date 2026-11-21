// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"gopkg.in/yaml.v3"
)

// fileMutate is the CLI's Mutate: load the file, let fn edit and persist it.
func fileMutate(t *testing.T, path string) Mutate {
	t.Helper()
	return func(fn func(*config.Config) error) error {
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		return fn(cfg)
	}
}

// readNetwork decodes just the network section of a config file, so an
// assertion sees what was written rather than what Load() normalizes.
func readNetwork(t *testing.T, path string) config.NetworkConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Network config.NetworkConfig `yaml:"network"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
	return doc.Network
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The one-click repair for the screenshot's finding: a pre-0.1.5 config that
// listens on every interface gets an explicit loopback mode AND the binds
// written down, so the file says what the daemon will actually bind.
func TestRepairExposureNarrowsLegacyBinds(t *testing.T) {
	isolatedDoctorEnv(t)
	path := writeConfig(t, `node:
  name: legacy-wide
network:
  listen_addr: "0.0.0.0:7836"
  panel_addr: "0.0.0.0:7840"
`)

	checks := []Check{{Key: "doctor.network.exposure.legacy", Fix: FixExposure}}
	res := Repair(checks, path, nil, RepairOptions{Mutate: fileMutate(t, path)})
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("repair = %+v, want one success", res)
	}
	if !res[0].Restart {
		t.Fatal("exposure repair must say a restart is needed")
	}

	n := readNetwork(t, path)
	if n.ExposureMode != config.ExposureLoopback {
		t.Fatalf("exposure_mode = %q, want loopback", n.ExposureMode)
	}
	if n.ListenAddr != "127.0.0.1:7836" || n.PanelAddr != "127.0.0.1:7840" {
		t.Fatalf("binds not narrowed: %+v", n)
	}
	// "" means "every interface on the WS port" — the repair has to spell the
	// datagram bind out too, or the widest socket survives the fix.
	if n.UDPListen != "127.0.0.1:7836" {
		t.Fatalf("udp_listen = %q, want 127.0.0.1:7836", n.UDPListen)
	}
	// The repaired file must load cleanly: a fix that bricks the next start
	// is worse than the finding.
	if _, err := config.Load(path); err != nil {
		t.Fatalf("repaired config does not load: %v", err)
	}
}

// lan keeps the mesh reachable (that is what the wide bind already did) but
// must not leave a wide console without a stable token — that combination is
// a load error.
func TestRepairExposureLANKeepsMeshAndNarrowsConsole(t *testing.T) {
	isolatedDoctorEnv(t)
	path := writeConfig(t, `node:
  name: legacy-lan
network:
  listen_addr: "0.0.0.0:7836"
  panel_addr: "0.0.0.0:7840"
  udp_listen: "off"
`)

	checks := []Check{{Key: "doctor.network.exposure.legacy", Fix: FixExposure}}
	res := Repair(checks, path, nil, RepairOptions{Exposure: config.ExposureLAN, Mutate: fileMutate(t, path)})
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("repair = %+v, want one success", res)
	}

	n := readNetwork(t, path)
	if n.ExposureMode != config.ExposureLAN {
		t.Fatalf("exposure_mode = %q, want lan", n.ExposureMode)
	}
	if n.ListenAddr != "0.0.0.0:7836" {
		t.Fatalf("lan repair must keep the mesh bind, got %q", n.ListenAddr)
	}
	if n.PanelAddr != "127.0.0.1:7840" {
		t.Fatalf("wide console without a token must be narrowed, got %q", n.PanelAddr)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatalf("lan repair produced an unloadable config: %v", err)
	}
}

// A node with peers must never get a fresh secret from a button: that would
// disconnect every peer it has. The repair refuses and says why.
func TestRepairSecretRefusesWhenPeersConfigured(t *testing.T) {
	isolatedDoctorEnv(t)
	path := writeConfig(t, `node:
  name: meshed
network:
  listen_addr: "127.0.0.1:7836"
  peers:
    - "peer-1:7836"
`)

	checks := []Check{{Key: "doctor.network.nosecret", Fix: FixSecret}}
	res := Repair(checks, path, nil, RepairOptions{Mutate: fileMutate(t, path)})
	if len(res) != 1 || res[0].OK {
		t.Fatalf("repair = %+v, want a refusal", res)
	}
	if res[0].Msg != "doctor.fix.secret.peers" {
		t.Fatalf("msg = %q, want doctor.fix.secret.peers", res[0].Msg)
	}
	if n := readNetwork(t, path); n.SharedSecret != "" {
		t.Fatalf("refused repair still wrote a secret: %q", n.SharedSecret)
	}
}

// The first-run case: no peers, no secret — mint one and persist it so the
// next start (and every pairing command) sees the same material.
func TestRepairSecretMintsAndPersists(t *testing.T) {
	isolatedDoctorEnv(t)
	path := writeConfig(t, `node:
  name: fresh
network:
  listen_addr: "127.0.0.1:7836"
`)

	checks := []Check{{Key: "doctor.network.nosecret", Fix: FixSecret}}
	res := Repair(checks, path, nil, RepairOptions{Mutate: fileMutate(t, path)})
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("repair = %+v, want one success", res)
	}
	if !res[0].Restart {
		t.Fatal("a minted secret only reaches the WS listener on the next start")
	}
	secret := readNetwork(t, path).SharedSecret
	if len(secret) != 32 {
		t.Fatalf("persisted secret = %q, want 32 hex chars", secret)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.SharedSecret != secret {
		t.Fatalf("loaded secret %q != persisted %q", cfg.Network.SharedSecret, secret)
	}
}

// Retention off plus a huge settled history is the one growth combination
// with a knob; the repair turns the default window back on.
func TestRepairRetentionEnablesPruning(t *testing.T) {
	isolatedDoctorEnv(t)
	path := writeConfig(t, `node:
  name: hoarder
storage:
  task_retention_days: 0
`)

	checks := []Check{{Key: "doctor.db.growth.no", Fix: FixRetention}}
	res := Repair(checks, path, nil, RepairOptions{Mutate: fileMutate(t, path)})
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("repair = %+v, want one success", res)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Storage.EffectiveTaskRetentionDays(); got != config.DefaultTaskRetentionDays {
		t.Fatalf("retention = %d, want %d", got, config.DefaultTaskRetentionDays)
	}
}

// A one-click repair on a single row must not touch the other findings, and a
// check that already passes must never re-run its repair.
func TestRepairOnlyTouchesRequestedAndFailingChecks(t *testing.T) {
	isolatedDoctorEnv(t)
	path := writeConfig(t, `node:
  name: selective
storage:
  task_retention_days: 0
`)
	checks := []Check{
		{Key: "doctor.db.growth.no", Fix: FixRetention},
		{Key: "doctor.network.exposure.legacy", Fix: FixExposure},
		{Key: "doctor.path.no", Fix: FixPath, OK: true},
	}

	res := Repair(checks, path, []string{"doctor.db.growth.no"}, RepairOptions{Mutate: fileMutate(t, path)})
	if len(res) != 1 || res[0].Key != "doctor.db.growth.no" {
		t.Fatalf("repair = %+v, want only the requested key", res)
	}
	if n := readNetwork(t, path); n.ExposureMode != "" {
		t.Fatalf("unrequested repair ran: exposure_mode = %q", n.ExposureMode)
	}
}

// Two failing checks can share one repair (path.no + persist.no). It runs
// once — a second AddToPATH cannot add anything — and reports once.
func TestRepairRunsSharedFixOnce(t *testing.T) {
	isolatedDoctorEnv(t)
	path := writeConfig(t, `node:
  name: dedupe
storage:
  task_retention_days: 0
`)
	checks := []Check{
		{Key: "doctor.db.growth.no", Fix: FixRetention},
		{Key: "doctor.db.growth.no.second-row", Fix: FixRetention},
	}
	res := Repair(checks, path, nil, RepairOptions{Mutate: fileMutate(t, path)})
	if len(res) != 1 {
		t.Fatalf("repair ran %d times, want 1: %+v", len(res), res)
	}
}

// copyAdapterScripts moves the scripts and the release manifest, and leaves
// __pycache__ (which belongs to another interpreter) behind.
func TestCopyAdapterScripts(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "target")
	for name, body := range map[string]string{
		"claude_code.py":     "print('hi')\n",
		"_harness.py":        "print('harness')\n",
		".adapters.manifest": "claude_code.py\n",
		"README.md":          "not an adapter\n",
	} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(src, "__pycache__"), 0o755); err != nil {
		t.Fatal(err)
	}

	n, err := copyAdapterScripts(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("copied %d files, want 3", n)
	}
	for _, name := range []string{"claude_code.py", "_harness.py", ".adapters.manifest"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Fatalf("%s not copied: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "README.md")); err == nil {
		t.Fatal("README.md is not an adapter and must not be copied")
	}
	if _, err := os.Stat(filepath.Join(dst, "__pycache__")); err == nil {
		t.Fatal("__pycache__ must not be copied")
	}
}

// The console paints a repair button from Check.Fix, so the failing exposure
// row must carry one and the passing row must not.
func TestRunMarksLegacyExposureFixable(t *testing.T) {
	isolatedDoctorEnv(t)
	path := writeConfig(t, `node:
  name: legacy-marked
network:
  listen_addr: "0.0.0.0:7836"
  udp_listen: "off"
`)
	var found *Check
	for _, c := range Run(path) {
		if c.Key == "doctor.network.exposure.legacy" {
			cc := c
			found = &cc
		}
	}
	if found == nil {
		t.Fatal("wide legacy config did not report the exposure finding")
	}
	if found.Fix != FixExposure {
		t.Fatalf("fix = %q, want %q", found.Fix, FixExposure)
	}
}

// A node that has meshed before must not have its shared secret replaced: a
// fresh secret exiles every node it already paired with. The history lives in
// employee_cache, not in the current peer list, so the check has to read the
// database — a path the repair used to reach without any test.
func TestMeshHistoryReadsOtherNodes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "panda.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE employee_cache (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO employee_cache(id) VALUES('another-node')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.Storage.DBPath = dbPath
	cfg.Node.Name = "meshed"
	cfg.Node.Kind = config.NodeKindPhysical
	if !hasMeshHistory(cfg) {
		t.Fatal("another node in employee_cache is mesh history")
	}

	// The node's own row is not history.
	again, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := again.Exec(`DELETE FROM employee_cache`); err != nil {
		t.Fatal(err)
	}
	if _, err := again.Exec(`INSERT INTO employee_cache(id) VALUES(?)`, core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())); err != nil {
		t.Fatal(err)
	}
	if hasMeshHistory(cfg) {
		t.Fatal("the node's own row is not mesh history")
	}
}

func TestRepairSecretRefusesAfterMeshHistory(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "panda.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE employee_cache (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO employee_cache(id) VALUES('another-node')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.Storage.DBPath = dbPath
	cfg.Node.Name = "meshed"
	cfg.Node.Kind = config.NodeKindPhysical
	checks := []Check{{Key: "doctor.network.nosecret", Fix: FixSecret}}
	res := Repair(checks, filepath.Join(dir, "config.yaml"), nil, RepairOptions{
		Mutate: func(fn func(*config.Config) error) error { return fn(cfg) },
	})
	if len(res) != 1 || res[0].OK {
		t.Fatalf("repair = %+v, want a refusal", res)
	}
	if res[0].Msg != "doctor.fix.secret.meshed" {
		t.Fatalf("msg = %q, want doctor.fix.secret.meshed", res[0].Msg)
	}
	if cfg.Network.SharedSecret != "" {
		t.Fatalf("refused repair still minted a secret: %q", cfg.Network.SharedSecret)
	}
}
