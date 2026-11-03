// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// The console's browser pane: fetch a page the reader named, and hand it back
// as a document the pane can frame.
//
// The pane is an iframe onto this endpoint, not onto the site. Three reasons,
// in order of how much they decide the design:
//
//   - X-Frame-Options and frame-ancestors refuse the console as an ancestor on
//     most of the web, so "just point an iframe at the URL" does not work.
//   - A same-origin frame lets the page's own scripts reach this console. The
//     response therefore carries a sandbox without allow-same-origin, exactly
//     like the artifact preview: the document's origin is opaque, its scripts
//     still run, and nothing it holds is reachable from here.
//   - A server-side fetch is the only place a routable-address check can be
//     enforced. A browser asked for http://127.0.0.1:8080/api/... would happily
//     answer; this endpoint refuses it before a socket is opened.
//
// What the sandbox costs is honest and worth stating: no cookies, no
// localStorage, no same-origin XHR. A page that needs a login session will
// render its logged-out face. That is the price of framing the web from a node
// that also holds the user's tasks, and it is the right side of the trade.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

const (
	// browseMaxBytes caps one page. Beyond this the reader asked for something
	// that is not a page, and holding it in memory is the console's problem.
	browseMaxBytes = 8 << 20
	// browseTimeout bounds the whole fetch, redirects included.
	browseTimeout = 20 * time.Second
	// browseMaxHops bounds redirects. Every hop is re-checked, so a public URL
	// cannot bounce the fetch into the LAN.
	browseMaxHops = 5
	// browseMaxURLLen keeps a pathological query out of the resolver.
	browseMaxURLLen = 2048
)

// errBlockedAddress is the one refusal worth naming: it is not a failure of the
// site, it is this console declining to reach into the network it lives in.
var errBlockedAddress = errors.New("this address is not reachable from the console (loopback, private, or link-local)")

// browsePickOff precedes the picker in every browsed page: reading is the
// default state of a browser pane, and a page that eats every click cannot be
// read at all. `__openpandaProxied` is what tells the script that this document
// came through the fetcher — links then stay on the proxied route instead of
// stepping out to the site, which most sites refuse to frame.
const browsePickOff = `<script>window.__openpandaPick=false;window.__openpandaProxied=true</script>`

// browsePolicy is deliberately not the artifact policy. An artifact may only
// load its own files; a browsed page must load the site's stylesheets, scripts
// and images from wherever the site put them, so the source list is the web at
// large. The sandbox directive is what carries the isolation, and it is the
// same shape the artifact preview uses: scripts run, same-origin does not.
const browsePolicy = "sandbox allow-scripts allow-forms allow-popups allow-modals; " +
	"default-src https: http: data: blob: 'unsafe-inline' 'unsafe-eval'; " +
	"script-src https: http: data: blob: 'unsafe-inline' 'unsafe-eval'; " +
	"style-src https: http: data: 'unsafe-inline'; " +
	"img-src https: http: data: blob:; " +
	"font-src https: http: data:; " +
	"media-src https: http: data: blob:; " +
	"connect-src https: http:; " +
	"form-action https: http:; " +
	"base-uri https: http:; " +
	"frame-ancestors 'self'"

// browseUA identifies the console rather than impersonating a browser: a site
// that wants to serve something else to a tool is entitled to know.
const browseUA = "Mozilla/5.0 (compatible; OpenPanda-Console/1.0)"

// browseHTTP is one client for every fetch, so TLS sessions and connections are
// reused across a browsing session. Its dialer resolves the name itself and
// refuses any answer inside the local network — a hostname that resolves to
// 127.0.0.1 is the same refusal as typing 127.0.0.1, and checking only the
// literal would miss it.
var browseHTTP = &http.Client{
	Timeout: browseTimeout,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DialContext:           browseDial,
		// Compression is negotiated and decoded by net/http; a page served
		// gzip arrives as plain HTML, which is what the base injection needs.
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= browseMaxHops {
			return fmt.Errorf("stopped after %d redirects", browseMaxHops)
		}
		return checkBrowsableURL(req.URL)
	},
}

// browseDial resolves the host itself and dials a vetted address. Handing the
// name to the system dialer would let a second resolution answer differently
// from the one checked here.
func browseDial(ctx context.Context, network, addr string) (net.Conn, error) {
	// A dial to the egress proxy is not a dial into the local network — it is
	// the way out of it, and the proxy is very often 127.0.0.1. The target
	// still passed the URL gate (a literal private address was refused before
	// any socket existed), and this is the same path a browser takes for the
	// same name.
	if proxy := proxyAddress(); proxy != "" && strings.EqualFold(addr, proxy) {
		d := &net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error = errBlockedAddress
	for _, ip := range ips {
		if blockedIP(ip.IP) {
			continue
		}
		d := &net.Dialer{Timeout: 10 * time.Second}
		conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if derr == nil {
			return conn, nil
		}
		last = derr
	}
	return nil, last
}

// checkProxyTarget closes the gap the dial-time check cannot see when an
// egress proxy is configured: the socket goes to the proxy, which resolves the
// target itself. The name is resolved locally and refused if any answer is in
// a blocked range.
func checkProxyTarget(ctx context.Context, u *url.URL) error {
	if proxyAddress() == "" {
		return nil
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return errBlockedAddress
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		// The proxy may have a resolver this host does not; refusing here
		// would break every proxy-only (split-DNS) deployment.
		return nil
	}
	for _, ip := range ips {
		if blockedIP(ip.IP) {
			return errBlockedAddress
		}
	}
	return nil
}

// proxyAddress is the host:port of the configured egress proxy, in the order
// net/http reads the environment.
func proxyAddress() string {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if u.Host != "" {
			return u.Host
		}
		// "127.0.0.1:8080" with no scheme parses as a path.
		return raw
	}
	return ""
}

// blockedIP reports whether an address belongs to a network the console must
// not reach on a page's behalf: the machine itself, the LAN, the cloud metadata
// service, and the multicast/unspecified ranges that are never a web server.
func blockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
		// 0.0.0.0/8: "this network", never a destination.
		if ip4[0] == 0 {
			return true
		}
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}

// checkBrowsableURL is the gate every hop passes: scheme, shape, and a host
// that is not a bare local name. The address check itself happens at dial time,
// because only the resolver knows what a name means right now.
func checkBrowsableURL(u *url.URL) error {
	if u == nil {
		return errors.New("no url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http and https can be browsed (got %q)", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return errors.New("url has no host")
	}
	// A literal address is decided here, not at dial time: the resolver has
	// nothing to add, and the refusal should not depend on a network round
	// trip. This also keeps IPv6 literals out of the single-label rule below —
	// "[::1]" carries no dot and would otherwise be read as a local name.
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return errBlockedAddress
		}
		return nil
	}
	// A single-label name is a local name on almost every network the console
	// runs in ("router", "nas"), and no public site is named that way.
	if !strings.Contains(host, ".") {
		return errBlockedAddress
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".home.arpa") {
		return errBlockedAddress
	}
	return nil
}

// parseBrowsableURL accepts what a person would type: a bare host gets https,
// and surrounding whitespace is not an error.
func parseBrowsableURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("url is required")
	}
	if len(raw) > browseMaxURLLen {
		return nil, errors.New("url is too long")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("unparseable url: %w", err)
	}
	if err := checkBrowsableURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

// browsePage serves GET /api/browse?url=... as a framed document — the route
// for callers that can send the Bearer header.
func (h *handler) browsePage(w http.ResponseWriter, r *http.Request) {
	serveBrowsed(w, r)
}

// browseOrigin serves GET /api/browse/origin: the prefix the pane puts its
// iframe src under. The token lives in the path because an iframe cannot send
// a header, and it is a separate capability from the console's own so that a
// leaked frame URL cannot drive the rest of the API.
func (h *handler) browseOrigin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"prefix": "/browse/" + h.browseToken})
}

// browseFramed serves GET /browse/{token}?url=... — the iframe's route. The
// token is compared before anything else: an unauthenticated caller learns
// nothing, not even whether the target would have been reachable.
func (h *handler) browseFramed(w http.ResponseWriter, r *http.Request) {
	if !sameSecret(r.PathValue("token"), h.browseToken) {
		http.NotFound(w, r)
		return
	}
	serveBrowsed(w, r)
}

// serveBrowsed is the one fetcher both routes share.
func serveBrowsed(w http.ResponseWriter, r *http.Request) {
	u, err := parseBrowsableURL(r.URL.Query().Get("url"))
	if err != nil {
		browseFail(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), browseTimeout)
	defer cancel()

	// With an egress proxy, browseDial connects to the proxy and the proxy
	// resolves the target — the dial-time address check never sees the answer.
	// Resolve the name here and refuse a private answer before the proxy can
	// fetch it. A name this host cannot resolve is left to the proxy; a
	// rebinding between this lookup and the proxy's is the residual gap.
	if err := checkProxyTarget(ctx, u); err != nil {
		browseFail(w, http.StatusBadRequest, err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		browseFail(w, http.StatusBadRequest, err)
		return
	}
	req.Header.Set("User-Agent", browseUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,text/plain;q=0.8,*/*;q=0.5")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := browseHTTP.Do(req)
	if err != nil {
		browseFail(w, http.StatusBadGateway, browseError(err))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	if !browsableType(ctype) {
		browseFail(w, http.StatusUnsupportedMediaType,
			fmt.Errorf("this target is %s, not a page the pane can render", firstToken(ctype)))
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, browseMaxBytes+1))
	if err != nil {
		browseFail(w, http.StatusBadGateway, browseError(err))
		return
	}
	if len(body) > browseMaxBytes {
		browseFail(w, http.StatusRequestEntityTooLarge,
			fmt.Errorf("page is larger than %d MiB", browseMaxBytes>>20))
		return
	}
	// The response below always declares UTF-8, so a legacy page must be
	// converted first — otherwise a GBK/GB18030 site renders as mojibake.
	body = transcodePage(body, ctype)

	final := resp.Request.URL
	title := titleOf(body)
	doc := injectBase(body, final.String())
	// The picker rides every page, switched off. Turning it on is a message to
	// the frame, not a reload: a reader who points at one element should not
	// lose their scroll position a paragraph further down.
	doc = injectBefore(doc, "</body>", []byte(browsePickOff+pickerScript))

	hdr := w.Header()
	// The panel's middleware denies framing on every response. These two exist
	// to be framed, and browsePolicy's frame-ancestors 'self' is the same guard
	// scoped to the console — a duplicate DENY here would white-screen the pane.
	hdr.Del("X-Frame-Options")
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("Content-Security-Policy", browsePolicy)
	// Both headers are percent-escaped: an HTTP header is bytes, and a title
	// can be any language, a final URL can carry a path in any script.
	hdr.Set("X-Browse-URL", url.QueryEscape(final.String()))
	hdr.Set("X-Browse-Title", url.QueryEscape(title))
	_, _ = w.Write(doc)
}

// sameSecret compares a caller-supplied credential with the one this process
// minted. Both sides are hashed so the comparison is constant-time regardless
// of length — ConstantTimeCompare otherwise early-returns on a length
// mismatch, leaking how long the token is. An empty want fails closed.
func sameSecret(got, want string) bool {
	if want == "" {
		return false
	}
	gotSum := sha256.Sum256([]byte(got))
	wantSum := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) == 1
}

// browseFail answers in HTML, not JSON: the caller is an iframe. A JSON error
// body inside the pane is a page of braces where the explanation should be.
func browseFail(w http.ResponseWriter, status int, err error) {
	msg := err.Error()
	// The pane cannot read this document (its origin is opaque) and cannot
	// learn the status of a frame load, so the page reports the failure itself.
	// json.Marshal of a string cannot fail, and it is the JS string literal:
	// it escapes the quotes, the backslashes and — importantly — the `<`, so
	// an error naming a tag cannot close the script that carries it.
	wire, _ := json.Marshal(msg)
	hdr := w.Header()
	// The panel's middleware denies framing on every response. These two exist
	// to be framed, and browsePolicy's frame-ancestors 'self' is the same guard
	// scoped to the console — a duplicate DENY here would white-screen the pane.
	hdr.Del("X-Frame-Options")
	hdr.Set("Content-Type", "text/html; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Content-Type-Options", "nosniff")
	// allow-scripts exists for the one inline script below. The document has
	// no other source to load from: default-src 'none' keeps a failed fetch
	// from becoming a second one.
	hdr.Set("Content-Security-Policy",
		"sandbox allow-scripts; default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w,
		`<!doctype html><meta charset="utf-8"><title>Preview unavailable</title>`+
			`<body style="margin:0;padding:28px;font:14px/1.6 system-ui,-apple-system,sans-serif;color:#3a3a3a;background:#fafafa">`+
			`<p style="margin:0;color:#a33">%s</p>`+
			`<script>try{parent.postMessage({source:"openpanda-browse-error",error:%s},"*")}catch(e){}</script>`+
			`</body>`,
		html.EscapeString(msg), wire)
}

// browseError turns a transport failure into a sentence the pane can show. The
// address refusal is the only one a reader can act on, so it is the only one
// that keeps its own wording.
func browseError(err error) error {
	if errors.Is(err, errBlockedAddress) {
		return errBlockedAddress
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("the site did not answer in time")
	}
	msg := err.Error()
	// net/http wraps the dial failure twice and prints the same host twice;
	// the operator's half is the tail.
	if i := strings.LastIndex(msg, ": "); i > 0 && i+2 < len(msg) {
		msg = msg[i+2:]
	}
	return fmt.Errorf("could not reach the site: %s", msg)
}

// transcodePage converts a declared legacy charset to UTF-8. The HTTP header
// wins over the document's own meta tag, matching browsers. Unknown labels
// keep the bytes as they arrived: a wrong guess is worse than mojibake a
// reader can still recognise.
func transcodePage(body []byte, contentType string) []byte {
	label := declaredCharset(contentType, body)
	if label == "" {
		if utf8.Valid(body) {
			return body
		}
		// No declaration and the bytes are not UTF-8: GB18030 is the safest
		// fallback for the pages this pane is most likely to meet.
		label = "gb18030"
	}
	if strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "utf8") {
		return body
	}
	enc, err := htmlindex.Get(label)
	if err != nil || enc == nil {
		return body
	}
	out, _, err := transform.Bytes(enc.NewDecoder(), body)
	if err != nil {
		return body
	}
	return out
}

// declaredCharset prefers the Content-Type parameter and falls back to the
// first <meta charset> / <meta http-equiv content="...charset=..."> in the
// document head.
func declaredCharset(contentType string, body []byte) string {
	if _, params, err := mime.ParseMediaType(contentType); err == nil {
		if cs := strings.TrimSpace(params["charset"]); cs != "" {
			return cs
		}
	}
	head := body
	if len(head) > 4096 {
		head = head[:4096]
	}
	if m := metaCharsetRe.FindSubmatch(head); m != nil {
		return string(m[1])
	}
	return ""
}

// metaCharsetRe covers both spellings: a bare charset attribute and the
// http-equiv form, where the label sits inside the content attribute.
var metaCharsetRe = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?\s*([a-z0-9._:-]+)`)

func browsableType(ctype string) bool {
	if ctype == "" {
		// No Content-Type at all: let the bytes speak, which in practice means
		// an HTML page from a small server.
		return true
	}
	return strings.HasPrefix(ctype, "text/html") ||
		strings.HasPrefix(ctype, "application/xhtml") ||
		strings.HasPrefix(ctype, "text/plain")
}

func firstToken(ctype string) string {
	if i := strings.IndexByte(ctype, ';'); i > 0 {
		return ctype[:i]
	}
	if ctype == "" {
		return "an unknown type"
	}
	return ctype
}

var (
	baseTagRe   = regexp.MustCompile(`(?is)<base\b[^>]*>`)
	headOpenRe  = regexp.MustCompile(`(?is)<head\b[^>]*>`)
	htmlOpenRe  = regexp.MustCompile(`(?is)<html\b[^>]*>`)
	titleRe     = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	titleTagsRe = regexp.MustCompile(`(?is)<[^>]*>`)
)

// injectBase gives the document an absolute base so every relative asset URL
// resolves against the site, not against /api/browse. An existing <base> is
// dropped first: the browser honours the first one it meets, and a site that
// shipped its own would otherwise send every asset to a path this console does
// not serve.
func injectBase(doc []byte, finalURL string) []byte {
	doc = baseTagRe.ReplaceAll(doc, nil)
	tag := []byte(`<base href="` + html.EscapeString(finalURL) + `">`)
	if loc := headOpenRe.FindIndex(doc); loc != nil {
		return splice(doc, loc[1], tag)
	}
	if loc := htmlOpenRe.FindIndex(doc); loc != nil {
		return splice(doc, loc[1], tag)
	}
	return append(tag, doc...)
}

// injectBefore inserts at the last occurrence of a closing tag, so an inline
// script that writes markup cannot land after the script that must follow it.
func injectBefore(doc []byte, closing string, injection []byte) []byte {
	idx := lastIndexFold(doc, []byte(closing))
	if idx < 0 {
		return append(doc, injection...)
	}
	return splice(doc, idx, injection)
}

func splice(doc []byte, at int, insertion []byte) []byte {
	out := make([]byte, 0, len(doc)+len(insertion))
	out = append(out, doc[:at]...)
	out = append(out, insertion...)
	out = append(out, doc[at:]...)
	return out
}

// titleOf reads the document's own title, for the pane's tab. The sandbox has
// an opaque origin, so the console cannot ask the frame — this header is the
// only way the name of the page reaches the chrome around it.
func titleOf(doc []byte) string {
	m := titleRe.FindSubmatch(doc)
	if m == nil {
		return ""
	}
	title := html.UnescapeString(string(titleTagsRe.ReplaceAll(m[1], nil)))
	title = strings.Join(strings.Fields(title), " ")
	if len(title) > 200 {
		title = title[:200]
	}
	return title
}
