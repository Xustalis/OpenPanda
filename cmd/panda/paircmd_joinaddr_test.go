// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestJoinDialAddr(t *testing.T) {
	cases := []struct {
		name     string
		conf     string
		lan      string
		want     string
		replaced bool
	}{
		{"wildcard uses lan", "0.0.0.0:7836", "192.168.1.5", "192.168.1.5:7836", true},
		{"empty host uses lan", ":7836", "10.0.0.9", "10.0.0.9:7836", true},
		{"ipv6 wildcard uses lan", "[::]:7836", "192.168.1.5", "192.168.1.5:7836", true},
		{"wildcard without lan keeps editable placeholder", "0.0.0.0:7836", "", "<this-machine>:7836", true},
		{"empty host without lan keeps editable placeholder", ":7836", "", "<this-machine>:7836", true},
		{"loopback uses lan", "127.0.0.1:7836", "192.168.1.5", "192.168.1.5:7836", true},
		{"localhost uses lan", "localhost:7836", "192.168.1.5", "192.168.1.5:7836", true},
		{"explicit host stays", "build.lan:7836", "192.168.1.5", "build.lan:7836", false},
		{"malformed stays", "not-a-host-port", "192.168.1.5", "not-a-host-port", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, replaced := joinDialAddr(tc.conf, tc.lan)
			if got != tc.want || replaced != tc.replaced {
				t.Fatalf("joinDialAddr(%q, %q) = (%q, %v), want (%q, %v)",
					tc.conf, tc.lan, got, replaced, tc.want, tc.replaced)
			}
		})
	}
}
