// SPDX-License-Identifier: AGPL-3.0-or-later

package devserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// devCookie is the per-listener credential. It is HttpOnly: the dev app and
// its dependencies may read cookies, but none of them can lift the preview
// capability out of the browser.
const devCookie = "openpanda_dev"

// newProxy builds the one reverse proxy a running dev server uses. The
// listener's root is the app's root, so root-absolute asset URLs, routes and
// the dev server's own WebSocket work untouched. The proxy adds only what a
// reverse proxy must: forwarding headers, a browser-reachable frame policy,
// and cookie attributes that survive the different origin.
func (m *Manager) newProxy(s *slot) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(s.target)
			pr.SetXForwarded()
			// The dev server may build absolute URLs from the forwarding
			// headers; the console's own host is the one the browser used.
			pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
			pr.Out.Header.Set("X-Forwarded-Proto", "http")
		},
		ModifyResponse: rewriteDevResponse(s.target),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprintf(w,
				`<!doctype html><meta charset="utf-8"><title>Dev server unavailable</title>`+
					`<body style="margin:0;padding:28px;font:14px/1.6 system-ui,-apple-system,sans-serif;color:#3a3a3a;background:#fafafa">`+
					`<p style="margin:0;color:#a33">The dev server did not answer: %s</p></body>`,
				html.EscapeString(err.Error()))
		},
		// SSE and other streamed responses must not wait for a full buffer.
		FlushInterval: 100 * time.Millisecond,
		Transport:     devTransport(),
	}
}

// serveEnter exchanges the one-time link token for the listener's cookie.
// The token is in the entry URL only; every later request — the app's own
// script, asset, XHR and WebSocket handshake — authenticates by cookie.
func (m *Manager) serveEnter(s *slot) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !sameSecret(r.URL.Query().Get("token"), s.token) {
			http.Error(w, "this preview link is not valid", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     devCookie,
			Value:    s.token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// authCookie guards the proxied app. A request without the listener's cookie
// learns nothing: not the target, not whether it is running.
func (s *slot) authCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie(devCookie)
		got := ""
		if cookie != nil {
			got = cookie.Value
		}
		if !sameSecret(got, s.token) && !sameSecret(bearer(r), s.token) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Preview link required</title>` +
				`<body style="margin:0;padding:28px;font:14px/1.6 system-ui,-apple-system,sans-serif;color:#3a3a3a;background:#fafafa">` +
				`<p style="margin:0">Open this dev server from the console's preview panel.</p></body>`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// rewriteDevResponse makes a response frameable and keeps its cookies and
// redirects on this origin.
func rewriteDevResponse(target *url.URL) func(*http.Response) error {
	return func(resp *http.Response) error {
		resp.Header.Del("X-Frame-Options")
		if csp := resp.Header.Get("Content-Security-Policy"); csp != "" {
			resp.Header.Set("Content-Security-Policy", frameableCSP(csp))
		}
		if loc := resp.Header.Get("Location"); loc != "" {
			if u, err := url.Parse(loc); err == nil && u.IsAbs() && strings.EqualFold(u.Host, target.Host) {
				// A redirect into the dev server's own host must stay on the
				// proxy origin; a relative Location does that.
				u.Scheme = ""
				u.Host = ""
				resp.Header.Set("Location", u.String())
			}
		}
		if cookies := resp.Header.Values("Set-Cookie"); len(cookies) > 0 {
			resp.Header.Del("Set-Cookie")
			for _, c := range cookies {
				resp.Header.Add("Set-Cookie", proxyCookie(c))
			}
		}
		return nil
	}
}

// frameableCSP drops a site's own frame-ancestors directive and allows the
// console to frame it. The preview URL carries a per-start token, so the
// looser ancestor rule does not expose the page to arbitrary sites.
func frameableCSP(csp string) string {
	parts := strings.Split(csp, ";")
	out := make([]string, 0, len(parts)+1)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		name := p
		if i := strings.IndexAny(name, " \t"); i >= 0 {
			name = name[:i]
		}
		if strings.EqualFold(name, "frame-ancestors") {
			continue
		}
		out = append(out, p)
	}
	out = append(out, "frame-ancestors *")
	return strings.Join(out, "; ")
}

// proxyCookie keeps the cookie's name, value and path, and drops the
// attributes that assume the dev server's own origin: Domain would not match
// the proxy host, Secure would be dropped over http, and SameSite=None
// without Secure is rejected by browsers.
func proxyCookie(raw string) string {
	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		trimmed := strings.TrimSpace(p)
		lower := strings.ToLower(trimmed)
		if i > 0 {
			if strings.HasPrefix(lower, "domain=") ||
				lower == "secure" ||
				strings.HasPrefix(lower, "samesite=none") {
				continue
			}
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "; ")
}

// devTransport is one transport for every project: loopback only, no proxy,
// and no HTTP/2 upgrade (the dev server's WebSocket is HTTP/1.1). Response
// header timeouts stay generous because a dev server may compile on the first
// request.
func devTransport() *http.Transport {
	return &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// sameSecret compares a caller-supplied credential with the one this slot
// minted. Hashing both sides keeps the comparison constant-time regardless of
// length. An empty want fails closed.
func sameSecret(got, want string) bool {
	if want == "" {
		return false
	}
	gotSum := sha256.Sum256([]byte(got))
	wantSum := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) == 1
}
