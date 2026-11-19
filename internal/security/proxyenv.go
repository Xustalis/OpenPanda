// SPDX-License-Identifier: AGPL-3.0-or-later

package security

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// localProxyDialTimeout bounds the liveness probe against a proxy that lives on
// this machine. A loopback listener answers in microseconds or not at all, so a
// few hundred milliseconds is generous; the probe runs once per process start,
// and only when a proxy is actually configured.
const localProxyDialTimeout = 300 * time.Millisecond

// KeepProxyEnv is the escape hatch for the rare operator whose local proxy is
// intentionally down at start (started later, or a tunnel brought up after the
// node). It disables the drop entirely rather than whitelisting one variable,
// so the behaviour is a single, obvious switch.
const KeepProxyEnv = "OPENPANDA_KEEP_PROXY"

// proxyEnvKeys lists the variables net/http and the surrounding ecosystem read,
// in the order net/http resolves them. Upper case wins over lower case for the
// same scheme (httpproxy.getEnvAny), but each name is judged on its own so a
// dead upper-case value does not hide a live lower-case one — the surviving
// name still steers the request.
var proxyEnvKeys = []string{
	"HTTPS_PROXY", "https_proxy",
	"HTTP_PROXY", "http_proxy",
	"ALL_PROXY", "all_proxy",
}

// ProxyDrop records one proxy variable that named a dead local port and was
// removed from the process environment.
type ProxyDrop struct {
	// Key is the environment variable that was unset.
	Key string
	// Addr is the loopback host:port the value pointed at.
	Addr string
	// Err is the failed dial, kept for the log line and for tests.
	Err error
}

// String renders the drop for a single-line log message.
func (d ProxyDrop) String() string {
	return fmt.Sprintf("%s=%s (dial failed: %v)", d.Key, d.Addr, d.Err)
}

// SanitizeLocalProxyEnv removes proxy variables that point at a loopback
// address nothing is listening on, and reports what it removed.
//
// The failure this prevents: a long-lived process started from a shell that
// exports a loopback proxy — a desktop VPN client, a container helper, a
// sandbox sidecar — keeps that value for its entire lifetime. When the client
// restarts on a new port (or the sandbox session ends), every outbound request
// dies in the proxy handshake with "connection refused" against an address that
// no longer exists, and no amount of configuration editing inside the running
// process helps: net/http caches the proxy for the process lifetime.
//
// Only loopback proxies are probed. A remote proxy host is reached over the
// network, so a failed TCP connect says nothing dependable about it (a corporate
// proxy can sit behind a firewall that refuses raw probes while still serving
// CONNECT), and dropping a working proxy would break egress. A loopback
// listener, by contrast, either accepts the connection or the client is not
// running — there is no ambiguous middle state.
//
// The caller runs this once, before any HTTP client is built, so the decision
// also reaches children spawned afterwards: panda's daemon mode re-execs itself,
// and exec.Command inherits the sanitized environment.
func SanitizeLocalProxyEnv() []ProxyDrop {
	if strings.TrimSpace(os.Getenv(KeepProxyEnv)) != "" {
		return nil
	}
	var dropped []ProxyDrop
	for _, key := range proxyEnvKeys {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}
		addr, ok := loopbackProxyAddr(value)
		if !ok {
			continue
		}
		conn, err := net.DialTimeout("tcp", addr, localProxyDialTimeout)
		if err == nil {
			_ = conn.Close()
			continue
		}
		// Unset rather than overwrite with empty: an empty value still counts as
		// "configured" to some consumers, and net/http would keep trying it.
		if unsetErr := os.Unsetenv(key); unsetErr != nil {
			continue
		}
		dropped = append(dropped, ProxyDrop{Key: key, Addr: addr, Err: err})
	}
	return dropped
}

// loopbackProxyAddr extracts a dialable host:port from a proxy value and
// reports whether the host is loopback. Values arrive in three shapes: a URL
// with a scheme, a bare host:port, and (rarely) a URL with credentials, which
// url.Parse handles while the dial only needs the host.
func loopbackProxyAddr(value string) (string, bool) {
	candidate := value
	if !strings.Contains(candidate, "://") {
		candidate = "http://" + candidate
	}
	u, err := url.Parse(candidate)
	if err != nil || u.Host == "" {
		return "", false
	}
	host := u.Hostname()
	if host == "" || !isLoopbackHost(host) {
		return "", false
	}
	port := u.Port()
	if port == "" {
		// Scheme defaults, so a value like "http://127.0.0.1" still gets probed
		// on the port the client would actually use.
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(host, port), true
}

// isLoopbackHost reports whether host is this machine through the loopback
// interface. "localhost" is included because that is what most proxy clients
// write, and it cannot resolve anywhere else by definition on a sane resolver.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Verdicts about a loopback proxy are cached, because the check runs on the
// request path: probing on every model call would add a dial to each one, and a
// dead proxy would add its timeout to every call. Re-probing is cheap and drives
// recovery, so the windows are short — a live proxy is re-confirmed often enough
// to notice a restart, a dead one is retried quickly enough that bringing the
// tunnel back takes effect without restarting panda.
const (
	proxyAliveTTL = 30 * time.Second
	proxyDeadTTL  = 5 * time.Second
)

// proxyProbe remembers the last verdict for one loopback address.
type proxyProbe struct {
	mu      sync.Mutex
	addr    string
	alive   bool
	checked time.Time
}

func (p *proxyProbe) allows(addr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	ttl := proxyAliveTTL
	if !p.alive {
		ttl = proxyDeadTTL
	}
	if p.addr == addr && !p.checked.IsZero() && time.Since(p.checked) < ttl {
		return p.alive
	}
	conn, err := net.DialTimeout("tcp", addr, localProxyDialTimeout)
	alive := err == nil
	if conn != nil {
		_ = conn.Close()
	}
	p.addr, p.alive, p.checked = addr, alive, time.Now()
	return alive
}

// defaultProxyProbe backs ProxyForRequest for the whole process. One cache is
// enough: the environment resolves to a single loopback proxy per scheme in
// practice, and a changed address simply invalidates the entry.
var defaultProxyProbe proxyProbe

// ProxyForRequest is the http.Transport.Proxy hook every model client should
// use. It resolves the proxy from the environment exactly like net/http would,
// then refuses a **loopback** proxy that has stopped listening, falling back to
// a direct connection.
//
// Start-time sanitising covers a proxy that is already dead when panda launches.
// This covers the rest of the process lifetime, which is the case that actually
// bites: panda is started from a shell that owns a working local proxy (a
// desktop tunnel, or a sandbox sidecar), the session ends or the tunnel
// restarts on a new port, and the exported address outlives the listener. The
// entry model would then fail every call with "proxyconnect … connection
// refused" until someone restarted the process — and no configuration change
// inside the running console could help, because net/http had already cached
// the value. Returning nil here turns that failure into a direct connection,
// and the cache re-probes so a restored tunnel is picked up again.
//
// A non-loopback proxy is returned untouched: it is reached over the network,
// where a refused probe says nothing dependable.
func ProxyForRequest(req *http.Request) (*url.URL, error) {
	u, err := http.ProxyFromEnvironment(req)
	if err != nil || u == nil {
		return u, err
	}
	if keep := strings.TrimSpace(os.Getenv(KeepProxyEnv)); keep != "" {
		return u, nil
	}
	addr, ok := loopbackProxyAddr(u.String())
	if !ok {
		return u, nil
	}
	if defaultProxyProbe.allows(addr) {
		return u, nil
	}
	return nil, nil
}
