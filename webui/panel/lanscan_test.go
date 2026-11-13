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

// TestRankInterface pins the rule that keeps a tunnel address out of the
// pairing panel. The broadcast flag is what actually distinguishes a tunnel —
// on Windows the WireGuard adapter is user-named ("xin-laptop"), so a name
// check alone would miss it and print 10.66.66.2 as "this device".
func TestRankInterface(t *testing.T) {
	mac := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	cases := []struct {
		name string
		ifc  net.Interface
		want int
	}{
		{
			name: "wireguard tunnel, renamed by the operator",
			ifc:  net.Interface{Name: "xin-laptop", Flags: net.FlagUp | net.FlagRunning, HardwareAddr: mac},
			want: addrRankTunnel,
		},
		{
			name: "macOS utun keeps multicast but no broadcast",
			ifc:  net.Interface{Name: "utun3", Flags: net.FlagUp | net.FlagMulticast | net.FlagRunning, HardwareAddr: mac},
			want: addrRankTunnel,
		},
		{
			name: "tunnel name wins even when the flags look like a LAN",
			ifc:  net.Interface{Name: "wg0", Flags: net.FlagUp | net.FlagBroadcast | net.FlagMulticast, HardwareAddr: mac},
			want: addrRankTunnel,
		},
		{
			name: "wi-fi",
			ifc:  net.Interface{Name: "WLAN", Flags: net.FlagUp | net.FlagBroadcast | net.FlagMulticast | net.FlagRunning, HardwareAddr: mac},
			want: addrRankPhysical,
		},
		{
			name: "hyper-v bridge",
			ifc:  net.Interface{Name: "vEthernet (WSL)", Flags: net.FlagUp | net.FlagBroadcast | net.FlagMulticast, HardwareAddr: mac},
			want: addrRankVirtual,
		},
		{
			name: "broadcast domain without a hardware address",
			ifc:  net.Interface{Name: "en0", Flags: net.FlagUp | net.FlagBroadcast | net.FlagMulticast},
			want: addrRankVirtual,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rankInterface(c.ifc); got != c.want {
				t.Fatalf("rankInterface(%q) = %d, want %d", c.ifc.Name, got, c.want)
			}
		})
	}
}

// TestPickSelfAddr: the panel must print an address the machine next to it can
// actually dial. Interface order alone is not that answer on Windows, where a
// WireGuard adapter enumerates ahead of the physical NICs.
func TestPickSelfAddr(t *testing.T) {
	netOf := func(cidr string) *net.IPNet {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatalf("ParseCIDR(%q): %v", cidr, err)
		}
		n.IP = ip.To4()
		return n
	}
	tunnel := localAddr{ipnet: netOf("10.66.66.2/24"), rank: addrRankTunnel}
	wifi := localAddr{ipnet: netOf("192.168.124.6/24"), rank: addrRankPhysical}
	eth := localAddr{ipnet: netOf("192.168.3.103/24"), rank: addrRankPhysical}
	bridge := localAddr{ipnet: netOf("172.28.0.1/20"), rank: addrRankVirtual}

	cases := []struct {
		name   string
		addrs  []localAddr
		routed net.IP
		want   string
		wantOK bool
	}{
		{"tunnel enumerated first loses to the NIC behind it", []localAddr{tunnel, eth, wifi}, nil, "192.168.3.103", true},
		{"routed address wins inside the same rank", []localAddr{tunnel, eth, wifi}, net.ParseIP("192.168.124.6"), "192.168.124.6", true},
		{"a routed tunnel address is refused", []localAddr{tunnel, wifi}, net.ParseIP("10.66.66.2"), "192.168.124.6", true},
		{"host-only bridge loses to a real NIC", []localAddr{bridge, wifi}, nil, "192.168.124.6", true},
		{"a routed bridge address does not outrank a real NIC", []localAddr{bridge, wifi}, net.ParseIP("172.28.0.1"), "192.168.124.6", true},
		{"tunnel-only host still answers", []localAddr{tunnel}, nil, "10.66.66.2", true},
		{"no usable address", nil, nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ip, ok := pickSelfAddr(c.addrs, c.routed)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			got := ""
			if ip != nil {
				got = ip.String()
			}
			if got != c.want {
				t.Fatalf("pickSelfAddr = %q, want %q", got, c.want)
			}
		})
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
