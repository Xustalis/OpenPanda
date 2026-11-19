// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import (
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// clearProxyEnv blanks every proxy variable the sanitizer inspects so a test
// observes only the value it sets. t.Setenv restores the originals on cleanup,
// which keeps the developer's own proxy out of the assertions.
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, key := range proxyEnvKeys {
		t.Setenv(key, "")
	}
	t.Setenv(KeepProxyEnv, "")
}

// liveLoopbackAddr starts a listener and returns its address. The listener
// answers a TCP connect, which is the entire liveness signal the sanitizer uses.
func liveLoopbackAddr(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln.Addr().String(), func() { _ = ln.Close() }
}

// deadLoopbackAddr returns an address on loopback with nothing listening. It
// binds a port, records it, and closes the listener, so the port is free and
// the connect is refused rather than hanging.
func deadLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

// TestSanitizeKeepsLiveLoopbackProxy is the case that must not regress: a
// working local proxy is left alone, because dropping it would silently route
// traffic around the tunnel the operator configured.
func TestSanitizeKeepsLiveLoopbackProxy(t *testing.T) {
	clearProxyEnv(t)
	addr, stop := liveLoopbackAddr(t)
	defer stop()
	t.Setenv("HTTPS_PROXY", "http://"+addr)

	dropped := SanitizeLocalProxyEnv()

	if len(dropped) != 0 {
		t.Fatalf("live proxy dropped: %v", dropped)
	}
	if got := os.Getenv("HTTPS_PROXY"); got != "http://"+addr {
		t.Fatalf("HTTPS_PROXY = %q, want it preserved", got)
	}
}

// TestSanitizeDropsDeadLoopbackProxy is the incident this code exists for: the
// exported port belongs to a client that is no longer running.
func TestSanitizeDropsDeadLoopbackProxy(t *testing.T) {
	clearProxyEnv(t)
	addr := deadLoopbackAddr(t)
	t.Setenv("HTTPS_PROXY", "http://"+addr)
	t.Setenv("https_proxy", "http://"+addr)

	dropped := SanitizeLocalProxyEnv()

	// Windows resolves environment names case-insensitively, so HTTPS_PROXY and
	// https_proxy are one variable there: the second set overwrites the first
	// and only one drop can be observed. The sanitizer walks the names in order
	// and stops reporting a key once Getenv returns empty, which is the correct
	// Windows behaviour — the fixture just cannot create two.
	want := 2
	if runtime.GOOS == "windows" {
		want = 1
	}
	if len(dropped) != want {
		t.Fatalf("dropped %d entries, want %d: %v", len(dropped), want, dropped)
	}
	for _, key := range []string{"HTTPS_PROXY", "https_proxy"} {
		if got := os.Getenv(key); got != "" {
			t.Fatalf("%s = %q, want unset", key, got)
		}
	}
	for _, d := range dropped {
		if d.Addr != addr {
			t.Fatalf("drop addr = %q, want %q", d.Addr, addr)
		}
		if d.Err == nil {
			t.Fatal("drop carries nil error")
		}
		if !strings.Contains(d.String(), addr) {
			t.Fatalf("String() = %q, want it to name %q", d.String(), addr)
		}
	}
}

// TestSanitizeIgnoresRemoteProxy pins the deliberate limit: a remote proxy is
// reached over the network and is never judged by a raw TCP probe.
func TestSanitizeIgnoresRemoteProxy(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://10.255.255.1:3128")

	if dropped := SanitizeLocalProxyEnv(); len(dropped) != 0 {
		t.Fatalf("remote proxy touched: %v", dropped)
	}
	if got := os.Getenv("HTTPS_PROXY"); got == "" {
		t.Fatal("remote proxy was cleared")
	}
}

// TestSanitizeHonoursKeepSwitch covers the escape hatch for an operator whose
// tunnel comes up after the node does.
func TestSanitizeHonoursKeepSwitch(t *testing.T) {
	clearProxyEnv(t)
	addr := deadLoopbackAddr(t)
	t.Setenv("HTTPS_PROXY", "http://"+addr)
	t.Setenv(KeepProxyEnv, "1")

	if dropped := SanitizeLocalProxyEnv(); len(dropped) != 0 {
		t.Fatalf("keep switch ignored: %v", dropped)
	}
	if os.Getenv("HTTPS_PROXY") == "" {
		t.Fatal("keep switch should preserve the variable")
	}
}

// TestSanitizeNoProxyConfigured keeps the no-op path cheap and silent.
func TestSanitizeNoProxyConfigured(t *testing.T) {
	clearProxyEnv(t)
	if dropped := SanitizeLocalProxyEnv(); len(dropped) != 0 {
		t.Fatalf("dropped without any proxy set: %v", dropped)
	}
}

// TestLoopbackProxyAddr pins the value shapes that reach the parser: real
// clients write URLs, bare host:port pairs, credentialed URLs, and localhost.
func TestLoopbackProxyAddr(t *testing.T) {
	cases := []struct {
		value    string
		wantAddr string
		wantOK   bool
	}{
		{"http://127.0.0.1:54079", "127.0.0.1:54079", true},
		{"127.0.0.1:54079", "127.0.0.1:54079", true},
		{"http://localhost:8080", "localhost:8080", true},
		{"http://[::1]:8080", "[::1]:8080", true},
		{"http://user:pass@127.0.0.1:3128", "127.0.0.1:3128", true},
		{"http://127.0.0.1", "127.0.0.1:80", true},
		{"https://127.0.0.1", "127.0.0.1:443", true},
		{"http://10.0.0.5:3128", "", false},
		{"http://proxy.corp.example:3128", "", false},
		{"", "", false},
		{"not a url at all", "", false},
	}
	for _, tc := range cases {
		addr, ok := loopbackProxyAddr(tc.value)
		if ok != tc.wantOK || addr != tc.wantAddr {
			t.Errorf("loopbackProxyAddr(%q) = (%q, %v), want (%q, %v)",
				tc.value, addr, ok, tc.wantAddr, tc.wantOK)
		}
	}
}

// TestProxyProbeCachesVerdict covers the request-path cost: once a proxy is
// known live, subsequent requests must not each pay a dial. Expiring the window
// by hand keeps the test fast and still exercises the re-probe branch.
func TestProxyProbeCachesVerdict(t *testing.T) {
	addr, stop := liveLoopbackAddr(t)
	var p proxyProbe

	if !p.allows(addr) {
		t.Fatal("live proxy refused")
	}
	stop() // now dead, but the live verdict is still cached
	if !p.allows(addr) {
		t.Fatal("re-probed inside the cache window; the request path would pay a dial per call")
	}
	p.mu.Lock()
	p.checked = time.Now().Add(-proxyAliveTTL - time.Second)
	p.mu.Unlock()
	if p.allows(addr) {
		t.Fatal("dead proxy still allowed after the live window expired")
	}
}

// TestProxyProbeRecovers is the recovery half: a tunnel that comes back must be
// picked up without restarting panda. The dead window is short precisely so this
// happens quickly.
func TestProxyProbeRecovers(t *testing.T) {
	addr := deadLoopbackAddr(t)
	var p proxyProbe

	if p.allows(addr) {
		t.Fatal("dead address allowed")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// The port can be stolen between close and re-bind; that is not a
		// failure of the code under test.
		t.Skipf("could not re-bind %s: %v", addr, err)
	}
	defer ln.Close()
	p.mu.Lock()
	p.checked = time.Now().Add(-proxyDeadTTL - time.Second)
	p.mu.Unlock()
	if !p.allows(addr) {
		t.Fatal("restored proxy not detected after the dead window expired")
	}
}
