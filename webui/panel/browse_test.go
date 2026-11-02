// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/transform"
)

// The gate is the whole feature: a browser pane that can be pointed at the
// console's own loopback is a request-forgery primitive with a URL bar. Every
// shape below is a way somebody has reached a local service from a browser.
func TestParseBrowsableURLRefusesLocalTargets(t *testing.T) {
	refused := []string{
		"http://localhost:8080/",
		"http://LOCALHOST/",
		"https://127.0.0.1/",
		"https://127.1.2.3/",
		"http://[::1]/",
		"https://192.168.1.1/",
		"https://10.0.0.5/",
		"https://172.16.4.4/",
		"https://169.254.169.254/latest/meta-data/",
		"http://0.0.0.0/",
		"https://router/",
		"http://nas.local/",
		"http://box.internal/",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"gopher://example.com/",
		"",
		"   ",
	}
	for _, raw := range refused {
		if u, err := parseBrowsableURL(raw); err == nil {
			t.Errorf("parseBrowsableURL(%q) accepted it as %s", raw, u)
		}
	}
}

func TestParseBrowsableURLAddsScheme(t *testing.T) {
	u, err := parseBrowsableURL("  example.com/docs  ")
	if err != nil {
		t.Fatalf("refused a public host: %v", err)
	}
	if u.Scheme != "https" || u.Host != "example.com" || u.Path != "/docs" {
		t.Fatalf("got %s, want https://example.com/docs", u)
	}
}

func TestParseBrowsableURLRejectsOverlongInput(t *testing.T) {
	raw := "https://example.com/" + strings.Repeat("a", browseMaxURLLen)
	if _, err := parseBrowsableURL(raw); err == nil {
		t.Fatal("a URL past the length cap was accepted")
	}
}

// The address check runs at dial time, so a name that resolves to loopback is
// refused the same way the literal is. This is the DNS-rebinding case: the
// string looks public and the answer is not.
func TestBrowseClientRefusesLoopbackDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><head><title>local</title></head><body>hi</body></html>"))
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := browseHTTP.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("browse client reached a loopback server (%s)", srv.URL)
	}
	if !errors.Is(err, errBlockedAddress) {
		t.Fatalf("want the blocked-address refusal, got %v", err)
	}
}

func TestBlockedIP(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":       true,
		"127.0.0.53":      true,
		"::1":             true,
		"10.1.2.3":        true,
		"192.168.0.1":     true,
		"172.20.0.1":      true,
		"169.254.169.254": true,
		"0.1.2.3":         true,
		"fe80::1":         true,
		"fc00::1":         true,
		"224.0.0.1":       true,
		"8.8.8.8":         false,
		"1.1.1.1":         false,
		"2606:4700::1111": false,
	}
	for raw, want := range cases {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("bad fixture %q", raw)
		}
		if got := blockedIP(ip); got != want {
			t.Errorf("blockedIP(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestInjectBaseRewritesExistingBase(t *testing.T) {
	doc := []byte(`<html><head><base href="http://evil.test/"><title>t</title></head><body>x</body></html>`)
	out := string(injectBase(doc, "https://example.com/a/b"))
	if strings.Contains(out, "evil.test") {
		t.Fatalf("the site's own <base> survived: %s", out)
	}
	if !strings.Contains(out, `<base href="https://example.com/a/b">`) {
		t.Fatalf("no base pointing at the page: %s", out)
	}
	if strings.Index(out, "<base") > strings.Index(out, "<title>") {
		t.Fatalf("base landed after the head content: %s", out)
	}
}

func TestInjectBaseWithoutHead(t *testing.T) {
	out := string(injectBase([]byte(`<div>bare</div>`), "https://example.com/"))
	if !strings.HasPrefix(out, `<base href="https://example.com/">`) {
		t.Fatalf("a fragment with no head did not get a leading base: %s", out)
	}
}

func TestTitleOfStripsMarkup(t *testing.T) {
	doc := []byte(`<html><head><title>  Foo &amp; <b>Bar</b>
	 baz </title></head></html>`)
	if got := titleOf(doc); got != "Foo & Bar baz" {
		t.Fatalf("title = %q", got)
	}
	if got := titleOf([]byte(`<html></html>`)); got != "" {
		t.Fatalf("a page without a title returned %q", got)
	}
}

func TestBrowsableType(t *testing.T) {
	ok := []string{"text/html; charset=utf-8", "application/xhtml+xml", "text/plain", ""}
	for _, c := range ok {
		if !browsableType(c) {
			t.Errorf("%q was refused", c)
		}
	}
	no := []string{"application/pdf", "image/png", "application/octet-stream"}
	for _, c := range no {
		if browsableType(c) {
			t.Errorf("%q was accepted as a page", c)
		}
	}
}

// The refusal has to survive being turned into a redirect target, or a public
// URL becomes a way around the first check.
func TestCheckRedirectRefusesLocalHop(t *testing.T) {
	local, _ := url.Parse("http://127.0.0.1:2375/containers/json")
	if err := checkBrowsableURL(local); err == nil {
		t.Fatal("a redirect to loopback passed the hop check")
	}
	pub, _ := url.Parse("https://example.com/next")
	if err := checkBrowsableURL(pub); err != nil {
		t.Fatalf("a public redirect was refused: %v", err)
	}
}

// The pane's frame is an iframe, and an iframe cannot send the Bearer header —
// so the credential travels in the path instead. That route is the only thing
// standing between "the console framed a page" and "any web page on this
// machine can use the console as a fetcher with a URL bar", so the two halves
// are pinned here: a wrong token never reaches the fetch, and the right one
// does.
func TestBrowseFramedRefusesWrongToken(t *testing.T) {
	h := &handler{browseToken: "known"}
	// A blocked target would answer 400 — reaching the fetcher at all is the
	// failure this test is looking for.
	for _, tok := range []string{"", "guess", "known ", "known\n"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/browse/x?url=http%3A%2F%2F127.0.0.1%2F", nil)
		req.SetPathValue("token", tok)
		h.browseFramed(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("token %q was admitted (%d)", tok, rec.Code)
		}
		// Nothing about the target leaks in the refusal.
		if body := rec.Body.String(); strings.Contains(body, "127.0.0.1") {
			t.Fatalf("a refused token leaked the target: %q", body)
		}
	}
}

func TestBrowseFramedAdmitsTokenAndFetches(t *testing.T) {
	h := &handler{browseToken: "known"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/browse/known?url=http%3A%2F%2F127.0.0.1%2F", nil)
	req.SetPathValue("token", "known")
	h.browseFramed(rec, req)
	// The target is loopback: the fetch refuses it, which proves the request
	// got past the token and into the address gate. The body is HTML, not
	// JSON, because the caller is an iframe.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("loopback target answered %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("refusal body is %q, want HTML", ct)
	}
	if xfo := rec.Header().Get("X-Frame-Options"); xfo != "" {
		t.Fatalf("X-Frame-Options %q would white-screen the frame", xfo)
	}
}

// The pane asks the server where to frame instead of building the path itself,
// so the capability never has to be known to the JavaScript bundle.
func TestBrowseOriginReturnsTokenPath(t *testing.T) {
	h := &handler{browseToken: "abc123"}
	rec := httptest.NewRecorder()
	h.browseOrigin(rec, httptest.NewRequest(http.MethodGet, "/api/browse/origin", nil))
	if got, want := strings.TrimSpace(rec.Body.String()), `{"prefix":"/browse/abc123"}`; got != want {
		t.Fatalf("origin = %s, want %s", got, want)
	}
}

func TestSameSecretFailsClosed(t *testing.T) {
	cases := []struct {
		got, want string
		ok        bool
	}{
		{"", "", false}, // no configured secret admits nobody
		{"x", "", false},
		{"", "x", false},
		{"a", "a", true},
		{"a", "b", false},
		{"a", "a ", false},
	}
	for _, c := range cases {
		if got := sameSecret(c.got, c.want); got != c.ok {
			t.Errorf("sameSecret(%q, %q) = %v, want %v", c.got, c.want, got, c.ok)
		}
	}
}

func gb18030(t *testing.T, s string) []byte {
	t.Helper()
	enc, err := htmlindex.Get("gb18030")
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := transform.Bytes(enc.NewEncoder(), []byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A page that declares a legacy charset must be converted before it is served
// under the UTF-8 header, or every non-ASCII character renders as mojibake.
func TestTranscodePageHonoursHeaderCharset(t *testing.T) {
	raw := gb18030(t, "你好，世界")
	out := transcodePage(raw, "text/html; charset=gbk")
	if got := string(out); got != "你好，世界" {
		t.Fatalf("transcoded = %q, want 你好，世界", got)
	}
}

func TestTranscodePageHonoursMetaCharset(t *testing.T) {
	src := `<html><head><meta charset="gbk"></head><body>你好</body></html>`
	out := transcodePage(gb18030(t, src), "")
	if !utf8.Valid(out) {
		t.Fatal("transcoded page is not valid UTF-8")
	}
	if !strings.Contains(string(out), "你好") {
		t.Fatalf("transcoded page lost its text: %q", string(out))
	}
}

func TestTranscodePageKeepsUTF8(t *testing.T) {
	src := []byte("<p>你好</p>")
	if out := transcodePage(src, "text/html; charset=utf-8"); string(out) != string(src) {
		t.Fatalf("UTF-8 page was rewritten: %q", out)
	}
}

func TestDeclaredCharsetPrefersHeader(t *testing.T) {
	body := []byte(`<meta charset="gbk">`)
	if got := declaredCharset("text/html; charset=shift_jis", body); got != "shift_jis" {
		t.Fatalf("declaredCharset = %q, want shift_jis", got)
	}
	if got := declaredCharset("text/html", body); got != "gbk" {
		t.Fatalf("declaredCharset = %q, want gbk", got)
	}
}
