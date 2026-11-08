// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"fmt"
	"net"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// TestDiscoveryCheckReportsIdlePort pins the failure this check exists to
// name: a free discovery port means nothing is listening for beacons. Before
// the check, that state was indistinguishable from a LAN with no peers — both
// rendered as an empty device list — so an operator had no signal that the
// node's own listener had died.
func TestDiscoveryCheckReportsIdlePort(t *testing.T) {
	cfg := config.Default()
	// A port just released is the closest thing to "free" a test can arrange
	// without racing the machine.
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	_ = conn.Close()
	cfg.Network.DiscoveryAddr = fmt.Sprintf(":%d", port)

	c := checkDiscovery(cfg)
	if c.Key != "doctor.discovery.idle" {
		t.Fatalf("checkDiscovery = %+v, want the idle-port key", c)
	}
	if c.OK {
		t.Fatal("a free discovery port must not pass: nobody is listening for beacons")
	}
}

// TestDiscoveryCheckSeesListener covers the other side: a held port IS the
// passing state. The probe must not read "address already in use" as a fault
// when that is exactly the evidence discovery is running.
func TestDiscoveryCheckSeesListener(t *testing.T) {
	cfg := config.Default()
	held, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("hold a port: %v", err)
	}
	defer held.Close()
	cfg.Network.DiscoveryAddr = fmt.Sprintf(":%d", held.LocalAddr().(*net.UDPAddr).Port)

	c := checkDiscovery(cfg)
	if c.Key != "doctor.discovery.ok" || !c.OK {
		t.Fatalf("checkDiscovery = %+v, want the held-port pass", c)
	}
}

// TestDiscoveryCheckOffPasses: an explicit off is an operator decision, not a
// defect to report.
func TestDiscoveryCheckOffPasses(t *testing.T) {
	cfg := config.Default()
	cfg.Network.DiscoveryAddr = "off"
	c := checkDiscovery(cfg)
	if c.Key != "doctor.discovery.off" || !c.OK {
		t.Fatalf("checkDiscovery = %+v, want the explicit-off pass", c)
	}
}
