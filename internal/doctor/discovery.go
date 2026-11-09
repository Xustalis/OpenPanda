// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"errors"
	"net"
	"syscall"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// checkDiscovery reports whether the LAN-discovery socket is actually held.
//
// Neither the pairing panel nor doctor could tell "no devices on this LAN" from
// "my discovery listener never came up": both rendered as a healthy node with
// an empty list. Binding the discovery port answers it from outside the
// daemon's memory — a free port means nobody is listening for beacons, and that
// is the failure worth naming. The probe binds and releases immediately, so a
// running node loses at most one datagram; beacons repeat every 15s and the
// pending row they refresh lives for 60s.
func checkDiscovery(cfg *config.Config) Check {
	if cfg.Network.DiscoveryAddr == "off" {
		// An explicit off is an operator decision, not a defect.
		return pass("doctor.discovery.off")
	}
	bind := cfg.Network.DiscoveryAddrOrDefault()
	uaddr, err := net.ResolveUDPAddr("udp", bind)
	if err != nil {
		return fail("doctor.discovery.badaddr", "addr", bind, "err", err.Error())
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: uaddr.Port})
	if err == nil {
		_ = conn.Close()
		return fail("doctor.discovery.idle", "addr", bind)
	}
	if addrInUse(err) {
		return pass("doctor.discovery.ok", "addr", bind)
	}
	return fail("doctor.discovery.bindfail", "addr", bind, "err", err.Error())
}

// wsaEAddrInUse is Windows' WSAEADDRINUSE. A windows.Errno does not compare
// equal to syscall.EADDRINUSE — 10048 against 98 — so the POSIX constant alone
// reads a socket that is held exactly as intended as a bind fault, which is the
// one verdict this check must never get backwards.
const wsaEAddrInUse = syscall.Errno(10048)

// addrInUse reports whether the bind lost the address to another socket. That
// is the passing state here: the other socket is the node's own discovery
// listener.
func addrInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == wsaEAddrInUse
}
