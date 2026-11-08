// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"fmt"
	"net"
	"os"
	"syscall"
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

// TestAddrInUseCoversTheWindowsErrno: WSAEADDRINUSE is 10048, and a windows
// Errno does not compare equal to syscall.EADDRINUSE (98), so a POSIX-only test
// reads the held port as a bind fault on Windows — which is exactly what
// TestDiscoveryCheckSeesListener did on the cross-platform job. Feeding both
// errnos here pins the branch on every OS instead of waiting for that one
// runner to disagree.
func TestAddrInUseCoversTheWindowsErrno(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"windows", os.NewSyscallError("bind", syscall.Errno(10048)), true},
		{"posix", os.NewSyscallError("bind", syscall.EADDRINUSE), true},
		{"wrapped", &net.OpError{Op: "listen", Net: "udp", Err: os.NewSyscallError("bind", syscall.Errno(10048))}, true},
		{"denied", os.NewSyscallError("bind", syscall.EACCES), false},
	}
	for _, tc := range cases {
		if got := addrInUse(tc.err); got != tc.want {
			t.Errorf("addrInUse(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
