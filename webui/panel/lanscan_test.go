// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"net"
	"testing"
)

// TestLanScanTargetsSkipSelf pins the rule that makes a sweep result
// meaningful: this node's own addresses must never come back as candidates.
// Dialing yourself always succeeds and would fill the list with the one device
// the operator already knows about.
func TestLanScanTargetsSkipSelf(t *testing.T) {
	targets, subnets := lanScanTargets(defaultMeshPort)
	if len(subnets) == 0 {
		t.Skip("no IPv4 interface to scan on this host")
	}
	self := map[string]bool{}
	ifs, err := net.Interfaces()
	if err != nil {
		t.Fatalf("interfaces: %v", err)
	}
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipn.IP.To4(); ip4 != nil {
				self[ip4.String()] = true
			}
		}
	}
	for _, tgt := range targets {
		host, port, err := net.SplitHostPort(tgt)
		if err != nil {
			t.Fatalf("target %q is not host:port", tgt)
		}
		if port != "7836" {
			t.Fatalf("target %q does not carry the mesh port", tgt)
		}
		if self[host] {
			t.Fatalf("target %q is one of this node's own addresses", tgt)
		}
	}
}

// TestMeshPortFromListenAddr: the sweep must probe the port the node actually
// listens on, and fall back to the shipped default rather than failing when
// the configured address is absent or unparseable.
func TestMeshPortFromListenAddr(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", defaultMeshPort},
		{"0.0.0.0:7836", 7836},
		{":9000", 9000},
		{"127.0.0.1:12345", 12345},
		{"nonsense", defaultMeshPort},
		{"host:notaport", defaultMeshPort},
	}
	for _, c := range cases {
		if got := meshPortFromListenAddr(c.in); got != c.want {
			t.Errorf("meshPortFromListenAddr(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
