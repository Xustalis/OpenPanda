package panel

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"strings"
)

// NewToken returns a fresh random panel token. Used for ephemeral,
// loopback-only sessions when no network.panel_token is configured: the
// caller prints (or embeds in the opened URL) the token it returns, so a
// personal node works out of the box without hand-editing config.yaml.
func NewToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing means the OS entropy source is gone; a
		// fixed token would be worse than refusing to serve.
		panic("panel: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// IsLoopbackAddr reports whether a listen address binds only to loopback
// ("127.0.0.1:7840", "[::1]:7840", "localhost:7840"). A bare port (":7840")
// binds every interface and is not loopback. A non-loopback bind still gets
// an ephemeral token — /api/* never runs open — but the token travels over
// plain HTTP, so a stable configured token (or a TLS proxy) is the fix for
// anything long-lived.
func IsLoopbackAddr(addr string) bool {
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

// LANURLs returns the console URLs reachable from other devices on the LAN —
// one per non-loopback IPv4 — when bound to a non-loopback address, and
// nothing for a loopback bind. The bound listener address supplies the port.
// A bind on one specific address (or a hostname) serves exactly that address,
// so it is the only URL reported; wildcard binds (":7840", "0.0.0.0", "::")
// get one URL per detected LAN interface.
func LANURLs(bound string) []string {
	host, port, err := net.SplitHostPort(bound)
	if err != nil || IsLoopbackAddr(bound) {
		return nil
	}
	if host != "" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsUnspecified() {
			// Specific IP or hostname bind — the listener serves that one.
			return []string{"http://" + net.JoinHostPort(host, port)}
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
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

// AppendToken adds ?token=… (or &token=… when a query already exists) to a
// panel URL. The console's auto-login consumes it once and strips it from
// the address bar, so the token reaches the browser without a manual paste
// and does not linger in the visible URL.
func AppendToken(url, token string) string {
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	return url + sep + "token=" + token
}
