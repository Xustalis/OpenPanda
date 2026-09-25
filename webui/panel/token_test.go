package panel

import (
	"net"
	"net/url"
	"sort"
	"testing"
)

func TestIsLoopbackAddr(t *testing.T) {
	loopback := []string{
		"127.0.0.1:7840",
		"127.0.0.2:7840",
		"localhost:7840",
		"LOCALHOST:7840",
		"[::1]:7840",
	}
	for _, addr := range loopback {
		if !IsLoopbackAddr(addr) {
			t.Errorf("IsLoopbackAddr(%q) = false, want true", addr)
		}
	}
	nonLoopback := []string{
		"0.0.0.0:7840",
		"[::]:7840",
		":7840",
		"192.168.1.10:7840",
		"myhost.local:7840",
	}
	for _, addr := range nonLoopback {
		if IsLoopbackAddr(addr) {
			t.Errorf("IsLoopbackAddr(%q) = true, want false", addr)
		}
	}
}

func TestLANURLsLoopbackIsEmpty(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:7840", "localhost:7840", "[::1]:7840", "not-an-addr"} {
		if got := LANURLs(addr); got != nil {
			t.Errorf("LANURLs(%q) = %v, want nil", addr, got)
		}
	}
}

func TestLANURLsSpecificBindReportsOnlyItself(t *testing.T) {
	// Bound to one address, the listener serves exactly that one — no other
	// interface's URL may be advertised.
	got := LANURLs("192.168.1.55:9000")
	if len(got) != 1 || got[0] != "http://192.168.1.55:9000" {
		t.Fatalf("LANURLs = %v, want [http://192.168.1.55:9000]", got)
	}
	got = LANURLs("panda.lan:9000")
	if len(got) != 1 || got[0] != "http://panda.lan:9000" {
		t.Fatalf("LANURLs = %v, want [http://panda.lan:9000]", got)
	}
}

func TestLANURLsWildcardEnumeratesLANIPv4(t *testing.T) {
	for _, bound := range []string{":7840", "0.0.0.0:7840"} {
		got := LANURLs(bound)
		want := enumerateLANIPv4(t, "7840")
		if len(got) == 0 && len(want) == 0 {
			continue // single-homed loopback-only box — nothing to share
		}
		sort.Strings(got)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Fatalf("LANURLs(%q) = %v, want %v", bound, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("LANURLs(%q) = %v, want %v", bound, got, want)
			}
		}
		for _, u := range got {
			pu, err := url.Parse(u)
			if err != nil {
				t.Fatalf("LANURLs(%q) produced unparseable %q", bound, u)
			}
			if pu.Scheme != "http" || pu.Port() != "7840" {
				t.Fatalf("LANURLs(%q) produced %q, want http scheme and port 7840", bound, u)
			}
		}
	}
}

// enumerateLANIPv4 independently computes the expected share-URL set: every
// up, non-loopback interface's non-link-local IPv4 at the given port.
func enumerateLANIPv4(t *testing.T, port string) []string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("net.Interfaces: %v", err)
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() {
				out = append(out, "http://"+net.JoinHostPort(ip4.String(), port))
			}
		}
	}
	return out
}

func TestAppendToken(t *testing.T) {
	if got := AppendToken("http://h:1", "tok"); got != "http://h:1?token=tok" {
		t.Fatalf("AppendToken = %q", got)
	}
	if got := AppendToken("http://h:1?lang=en", "tok"); got != "http://h:1?lang=en&token=tok" {
		t.Fatalf("AppendToken = %q", got)
	}
}
