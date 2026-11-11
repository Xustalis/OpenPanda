// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestGzipAssetsServesCompressedAndPlain: the whole point of the handler is
// the same asset answering two ways — a gzipped copy for clients that asked
// for it, the plain FileServer for those that did not.
func TestGzipAssetsServesCompressedAndPlain(t *testing.T) {
	js := "var x = '" + string(make([]byte, 0)) + "';" + string(jsPad(600))
	// index.html must clear the 512-byte compressibility threshold
	// (static_gzip.compressibleAsset) or gzipAssets never precompresses it
	// and "/" falls through to the plain FileServer instead.
	indexHTML := "<html><body><script src=/app.js></script></body></html>" + string(jsPad(512))
	fsys := fstest.MapFS{
		"index.html":      {Data: []byte(indexHTML)},
		"app.js":          {Data: []byte(js)},
		"tiny.txt":        {Data: []byte("small")},
		"assets/logo.png": {Data: append([]byte("\x89PNG"), jsPad(700)...)},
	}
	h := gzipAssets(fsys)

	// gzip-capable client gets the precompressed copy.
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected gzip encoding, headers: %v", resp.Header)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if string(body) != js {
		t.Fatalf("decompressed body mismatch: %d bytes", len(body))
	}
	// The exact JS mime depends on the OS registry — Windows resolves
	// .js to application/javascript, Linux/macOS to text/javascript.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") && !strings.HasPrefix(ct, "application/javascript") {
		t.Fatalf("content-type = %q", ct)
	}
	if resp.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("Vary missing: %v", resp.Header)
	}

	// A gzip-less client gets the plain file.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	resp2 := rec2.Result()
	if resp2.Header.Get("Content-Encoding") == "gzip" {
		t.Fatal("plain client got gzipped bytes")
	}
	body2, _ := io.ReadAll(resp2.Body)
	if string(body2) != js {
		t.Fatalf("plain body mismatch: %d bytes", len(body2))
	}

	// Non-compressible files never get an encoding header.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/assets/logo.png", nil)
	req3.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec3, req3)
	if rec3.Result().Header.Get("Content-Encoding") != "" {
		t.Fatal("binary asset compressed")
	}

	// "/" resolves to index.html gzipped, like the FileServer's index rule.
	rec4 := httptest.NewRecorder()
	req4 := httptest.NewRequest(http.MethodGet, "/", nil)
	req4.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec4, req4)
	resp4 := rec4.Result()
	zr4, _ := gzip.NewReader(resp4.Body)
	idx, _ := io.ReadAll(zr4)
	if string(idx) != indexHTML {
		t.Fatalf("index.html not served at /: %q", idx)
	}
}

func jsPad(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

// TestGzipAssetsCachePolicy pins the two halves of the policy: the HTML entry
// point is revalidated on every load, because a cached index.html points at
// bundle names a new binary no longer ships — the console then keeps painting
// the old UI after a rebuild, which is indistinguishable from "the build did
// not run". Everything under /assets/ is content-hashed and can be cached
// forever.
func TestGzipAssetsCachePolicy(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":          {Data: []byte("<html><body>" + string(jsPad(600)) + "</body></html>")},
		"assets/index-abc.js": {Data: []byte("var x=1;" + string(jsPad(600)))},
		"app.js":              {Data: []byte("var y=2;" + string(jsPad(600)))},
	}
	h := gzipAssets(fsys)

	cases := []struct {
		path string
		want string
	}{
		{"/", "no-cache"},
		{"/index.html", "no-cache"},
		{"/assets/index-abc.js", "public, max-age=31536000, immutable"},
		{"/app.js", ""}, // neither the entry point nor a hashed asset
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Result().Header.Get("Cache-Control"); got != tc.want {
			t.Errorf("%s: Cache-Control = %q, want %q", tc.path, got, tc.want)
		}
	}
}

var _ fs.FS = fstest.MapFS{} // keep fstest honest
