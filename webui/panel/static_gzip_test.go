// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
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
	if resp.Header.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("content-type = %q", resp.Header.Get("Content-Type"))
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

var _ fs.FS = fstest.MapFS{} // keep fstest honest
