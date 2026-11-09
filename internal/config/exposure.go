// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Exposure modes describe who may open a connection to this node. They are a
// single source of truth for every listening socket the daemon, the embedded
// console and the sidecar open: Load() rewrites the effective bind addresses
// from the mode, so a diagnostic (doctor, status, card) and the actual
// listeners can never disagree about the node's reach.
//
//	loopback — nothing leaves the machine. New installs land here.
//	lan      — the mesh is reachable from the local network; the web console
//	           stays on loopback unless panel_addr is set explicitly.
//	public   — the mesh is reachable from a public interface. Discovery is
//	           disabled, cleartext dials are refused, and stable credentials
//	           are mandatory (Validate fails closed otherwise).
//	legacy   — pre-0.1.5 behaviour, kept for configs that already declared an
//	           explicit listen_addr. No bind is rewritten.
const (
	ExposureLoopback = "loopback"
	ExposureLAN      = "lan"
	ExposurePublic   = "public"
	ExposureLegacy   = "legacy"
)

// ValidExposureMode reports whether m is a mode the loader understands. The
// empty mode is valid: Load() resolves it from the on-disk config.
func ValidExposureMode(m string) bool {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "", ExposureLoopback, ExposureLAN, ExposurePublic, ExposureLegacy:
		return true
	default:
		return false
	}
}

// Exposure returns the normalized mode. Callers that did not go through
// Load() (tests, programmatically built configs) get the safe default.
func (n NetworkConfig) Exposure() string {
	m := strings.ToLower(strings.TrimSpace(n.ExposureMode))
	if m == "" {
		return ExposureLoopback
	}
	return m
}

// IsLoopbackBind reports whether addr only accepts local connections. An
// unparsable address is deliberately NOT loopback: failing open on a string
// the operator mistyped would widen the bind instead of refusing it.
func IsLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsPrivateBind reports whether addr is a wildcard or an RFC1918 / ULA /
// link-local address — the shapes a `lan` exposure may legitimately use.
func IsPrivateBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	// Tailscale's CGNAT range and ULA: an encrypted underlay, treated as LAN.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

// exposureFields reads the two fields the legacy decision depends on from the
// raw YAML. It deliberately does not use decodeStrict: an old config with
// unknown keys still has to load, and this probe only needs presence/absence.
func exposureFields(data []byte) (mode string, explicitBind bool) {
	var probe struct {
		Network struct {
			ExposureMode *string `yaml:"exposure_mode"`
			ListenAddr   *string `yaml:"listen_addr"`
			PanelAddr    *string `yaml:"panel_addr"`
			UDPListen    *string `yaml:"udp_listen"`
		} `yaml:"network"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return "", false
	}
	if probe.Network.ExposureMode != nil {
		mode = strings.ToLower(strings.TrimSpace(*probe.Network.ExposureMode))
	}
	explicitBind = probe.Network.ListenAddr != nil || probe.Network.PanelAddr != nil || probe.Network.UDPListen != nil
	return mode, explicitBind
}

// exposureFromEnv reports whether the operator set any bind explicitly via the
// environment (container deploys do this instead of editing config.yaml).
func exposureFromEnv() bool {
	for _, k := range []string{
		"OPENPANDA_EXPOSURE_MODE", "OPENPANDA_LISTEN_ADDR",
		"OPENPANDA_PANEL_ADDR", "OPENPANDA_UDP_LISTEN",
	} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// resolveExposureMode implements the 0.1.x compatibility rule: an explicit
// exposure_mode wins; a config that already declared a bind keeps its old
// behaviour as legacy; everything else (a fresh install) is loopback.
func resolveExposureMode(raw []byte, envMode bool) string {
	if envMode && os.Getenv("OPENPANDA_EXPOSURE_MODE") != "" {
		return strings.ToLower(strings.TrimSpace(os.Getenv("OPENPANDA_EXPOSURE_MODE")))
	}
	mode, explicitBind := exposureFields(raw)
	if mode != "" {
		return mode
	}
	if explicitBind || exposureFromEnv() {
		return ExposureLegacy
	}
	return ExposureLoopback
}

// applyExposure rewrites the effective binds. It runs inside normalize(), so
// every reader of NetworkConfig — daemon listener, UDP plane, discovery,
// panel, doctor, capability card, pairing — sees exactly what will be bound.
func (c *Config) applyExposure() {
	n := &c.Network
	mode := n.Exposure()
	switch mode {
	case ExposureLoopback:
		n.ListenAddr = loopbackAddr(n.ListenAddr, "7836")
		n.PanelAddr = loopbackAddr(n.PanelAddr, "7840")
		if n.UDPListen != "off" {
			_, port, err := net.SplitHostPort(n.ListenAddr)
			if err != nil || port == "" {
				port = "7836"
			}
			n.UDPListen = net.JoinHostPort("127.0.0.1", port)
		}
		// A loopback node has no LAN peers to announce itself to, and an
		// auto-dial to an inbound beacon would immediately undo the boundary.
		n.DiscoveryAddr = "off"
		n.DiscoveryAutoDial = false
	case ExposureLAN:
		if n.ListenAddr != "" && !IsPrivateBind(n.ListenAddr) {
			// Refuse silent widening: a `lan` node may not bind a public IP.
			// Validate() reports it; keep the value so the message is faithful.
			return
		}
	case ExposurePublic:
		n.DiscoveryAddr = "off"
		n.DiscoveryAutoDial = false
		if n.MaxConnectionsPerIP <= 0 {
			n.MaxConnectionsPerIP = 8
		}
	case ExposureLegacy:
		// Deliberately untouched.
	}
}

// loopbackAddr keeps the port of an existing bind and replaces its host with
// the loopback address. An empty value gets the supplied default port.
func loopbackAddr(addr, defaultPort string) string {
	if addr == "" {
		return net.JoinHostPort("127.0.0.1", defaultPort)
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = defaultPort
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// validateExposure is the fail-closed half of the exposure contract: widening
// the mesh without stable credentials or with plaintext dials is a load error,
// not a warning buried in a startup log.
func (c *Config) validateExposure() error {
	n := c.Network
	if !ValidExposureMode(n.ExposureMode) {
		return fmt.Errorf("config: network.exposure_mode %q is invalid (want %s, %s, %s or %s)",
			n.ExposureMode, ExposureLoopback, ExposureLAN, ExposurePublic, ExposureLegacy)
	}
	mode := n.Exposure()
	panelWide := n.PanelAddr != "" && !IsLoopbackBind(n.PanelAddr)
	switch mode {
	case ExposureLAN:
		if n.ListenAddr != "" && !IsPrivateBind(n.ListenAddr) {
			return fmt.Errorf("config: network.exposure_mode %s requires a private or wildcard listen_addr, got %q", ExposureLAN, n.ListenAddr)
		}
		if panelWide && n.PanelToken == "" {
			return fmt.Errorf("config: network.exposure_mode %s binds the console to %q without a stable network.panel_token (set it or OPENPANDA_PANEL_TOKEN)", mode, n.PanelAddr)
		}
	case ExposurePublic:
		if n.SharedSecret == "" {
			return fmt.Errorf("config: network.exposure_mode %s requires an explicit network.shared_secret (or OPENPANDA_SHARED_SECRET)", ExposurePublic)
		}
		if panelWide && n.PanelToken == "" {
			return fmt.Errorf("config: network.exposure_mode %s binds the console to %q without a stable network.panel_token (set it or OPENPANDA_PANEL_TOKEN)", mode, n.PanelAddr)
		}
		if n.AllowCleartext || len(n.AllowCleartextFor) > 0 {
			return fmt.Errorf("config: network.exposure_mode %s forbids network.allow_cleartext and network.allow_cleartext_for", ExposurePublic)
		}
	}
	return nil
}
