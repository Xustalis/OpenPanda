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
// binds every interface and is not loopback. Ephemeral tokens are only ever
// generated for loopback binds — a wider bind must fail closed instead.
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
