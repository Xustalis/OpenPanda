package main

import "testing"

// TestPanelURL pins the listen-address → browser-URL mapping: a wildcard or
// empty host is not dialable, so it becomes localhost; concrete hosts
// (including IPv6) pass through with their port.
func TestPanelURL(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{":7840", "http://localhost:7840"},
		{"0.0.0.0:8080", "http://localhost:8080"},
		{"[::]:8080", "http://localhost:8080"},
		{"127.0.0.1:7840", "http://127.0.0.1:7840"},
		{"192.168.1.10:7840", "http://192.168.1.10:7840"},
		{"[::1]:9000", "http://[::1]:9000"},
		{"garbage", "http://garbage"},
	} {
		if got := panelURL(tc.addr); got != tc.want {
			t.Errorf("panelURL(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}
