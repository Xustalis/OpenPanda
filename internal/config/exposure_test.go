// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadExposureYAML(t *testing.T, body string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestFreshInstallIsLoopbackOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Network.Exposure(); got != ExposureLoopback {
		t.Fatalf("exposure = %q, want loopback", got)
	}
	if cfg.Network.ListenAddr != "127.0.0.1:7836" {
		t.Errorf("listen_addr = %q, want loopback", cfg.Network.ListenAddr)
	}
	if cfg.Network.PanelAddr != "127.0.0.1:7840" {
		t.Errorf("panel_addr = %q, want loopback", cfg.Network.PanelAddr)
	}
	if cfg.Network.UDPListen != "127.0.0.1:7836" {
		t.Errorf("udp_listen = %q, want loopback", cfg.Network.UDPListen)
	}
	if cfg.Network.DiscoveryAddr != "off" || cfg.Network.DiscoveryAutoDial {
		t.Errorf("discovery = %q/%v, want off/false", cfg.Network.DiscoveryAddr, cfg.Network.DiscoveryAutoDial)
	}
}

func TestExplicitLoopbackRewritesEveryBind(t *testing.T) {
	cfg := loadExposureYAML(t, `
network:
  exposure_mode: loopback
  listen_addr: "0.0.0.0:9001"
  panel_addr: "0.0.0.0:9002"
  udp_listen: ":9003"
  discovery_addr: ":9004"
  discovery_auto_dial: true
`)
	if cfg.Network.ListenAddr != "127.0.0.1:9001" {
		t.Errorf("listen_addr = %q", cfg.Network.ListenAddr)
	}
	if cfg.Network.PanelAddr != "127.0.0.1:9002" {
		t.Errorf("panel_addr = %q", cfg.Network.PanelAddr)
	}
	if cfg.Network.UDPListen != "127.0.0.1:9001" {
		t.Errorf("udp_listen = %q, want the WS port on loopback", cfg.Network.UDPListen)
	}
	if cfg.Network.DiscoveryAddr != "off" || cfg.Network.DiscoveryAutoDial {
		t.Errorf("discovery not disabled: %q/%v", cfg.Network.DiscoveryAddr, cfg.Network.DiscoveryAutoDial)
	}
}

func TestExistingExplicitBindStaysLegacy(t *testing.T) {
	cfg := loadExposureYAML(t, `
network:
  listen_addr: "0.0.0.0:7836"
  discovery_auto_dial: true
`)
	if got := cfg.Network.Exposure(); got != ExposureLegacy {
		t.Fatalf("exposure = %q, want legacy", got)
	}
	if cfg.Network.ListenAddr != "0.0.0.0:7836" {
		t.Errorf("legacy bind was rewritten to %q", cfg.Network.ListenAddr)
	}
	if cfg.Network.DiscoveryAddr != "" || !cfg.Network.DiscoveryAutoDial {
		t.Errorf("legacy discovery defaults changed: %q/%v", cfg.Network.DiscoveryAddr, cfg.Network.DiscoveryAutoDial)
	}
}

func TestLANExposureRefusesPublicListen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "network:\n  exposure_mode: lan\n  listen_addr: \"203.0.113.7:7836\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("lan exposure accepted a public listen_addr")
	}
}

func TestLANExposureKeepsPrivateBind(t *testing.T) {
	cfg := loadExposureYAML(t, `
network:
  exposure_mode: lan
  listen_addr: "192.168.1.10:7836"
`)
	if cfg.Network.ListenAddr != "192.168.1.10:7836" {
		t.Errorf("private bind rewritten: %q", cfg.Network.ListenAddr)
	}
	if cfg.Network.PanelAddr != "127.0.0.1:7840" {
		t.Errorf("lan widened the console: %q", cfg.Network.PanelAddr)
	}
}

func TestPublicExposureFailsClosed(t *testing.T) {
	cases := map[string]string{
		"no secret": `
network:
  exposure_mode: public
  listen_addr: "0.0.0.0:7836"
`,
		"cleartext": `
network:
  exposure_mode: public
  shared_secret: "s"
  allow_cleartext: true
`,
		"wide console without token": `
network:
  exposure_mode: public
  shared_secret: "s"
  panel_addr: "0.0.0.0:7840"
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("public exposure accepted an unsafe config")
			}
		})
	}
}

func TestPublicExposureHardensDefaults(t *testing.T) {
	cfg := loadExposureYAML(t, `
network:
  exposure_mode: public
  shared_secret: "s"
  discovery_addr: ":7837"
  discovery_auto_dial: true
`)
	if cfg.Network.DiscoveryAddr != "off" || cfg.Network.DiscoveryAutoDial {
		t.Errorf("public discovery not disabled: %q/%v", cfg.Network.DiscoveryAddr, cfg.Network.DiscoveryAutoDial)
	}
	if cfg.Network.MaxConnectionsPerIP <= 0 {
		t.Error("public exposure left the per-IP connection limit unlimited")
	}
}

func TestInvalidExposureModeRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("network:\n  exposure_mode: everywhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unknown exposure mode accepted")
	}
}

func TestExposureEnvOverridesOnDiskMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("network:\n  exposure_mode: legacy\n  listen_addr: \"0.0.0.0:7836\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENPANDA_EXPOSURE_MODE", "loopback")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.Exposure() != ExposureLoopback || cfg.Network.ListenAddr != "127.0.0.1:7836" {
		t.Errorf("env override ignored: %s / %s", cfg.Network.Exposure(), cfg.Network.ListenAddr)
	}
}

func TestBindClassifiers(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:1", "localhost:1", "[::1]:1"} {
		if !IsLoopbackBind(addr) {
			t.Errorf("%q classified as non-loopback", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0:1", "192.168.1.1:1", "203.0.113.1:1", "garbage"} {
		if IsLoopbackBind(addr) {
			t.Errorf("%q classified as loopback", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0:1", "[::]:1", "192.168.1.1:1", "10.0.0.1:1", "100.88.1.2:1"} {
		if !IsPrivateBind(addr) {
			t.Errorf("%q classified as public", addr)
		}
	}
	for _, addr := range []string{"203.0.113.1:1", "8.8.8.8:1", "garbage"} {
		if IsPrivateBind(addr) {
			t.Errorf("%q classified as private", addr)
		}
	}
}
