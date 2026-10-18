// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// gzEntry is one asset's precompressed bytes plus the headers needed to
// serve it: the declared type (empty lets ServeContent sniff the name) and
// the source's modtime for conditional requests.
type gzEntry struct {
	body []byte
	mime string
	mod  time.Time
}

// gzHandler serves an immutable file tree from precompressed in-memory
// copies, falling back to the wrapped FileServer for anything it did not
// precompress or for clients that do not accept gzip.
type gzHandler struct {
	srv http.Handler
	gz  map[string]gzEntry // request path ("/assets/x.js") → compressed copy
}

// gzipAssets walks a read-only fs.FS once at startup and precompresses every
// compressible file it finds. The console bundle is ~700KB of JS/CSS that a
// plain FileServer ships raw on every cold load; over a Pi's LAN that is the
// panel's largest single transfer. Embedded assets never change inside the
// process, so the compressed cache never invalidates — the compression cost
// is paid once, not per request.
func gzipAssets(fsys fs.FS) http.Handler {
	g := &gzHandler{srv: http.FileServer(http.FS(fsys)), gz: make(map[string]gzEntry)}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil || !compressibleAsset(p, info.Size()) {
			return nil
		}
		raw, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil // unreadable file: the wrapped FileServer still serves it
		}
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(raw); err != nil {
			return nil
		}
		if err := zw.Close(); err != nil {
			return nil
		}
		g.gz["/"+p] = gzEntry{body: buf.Bytes(), mime: mime.TypeByExtension(path.Ext(p)), mod: info.ModTime()}
		return nil
	})
	return g
}

// ServeHTTP answers from the precompressed copy when the client announced
// gzip support; every other request (no gzip, non-precompressed files,
// directory listings) goes to the wrapped FileServer. Vary rides both paths
// so a shared cache never replays the gzipped body to a client that did not
// ask for it.
func (g *gzHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Accept-Encoding")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		p := r.URL.Path
		if p == "" || p == "/" {
			p = "/index.html"
		}
		if e, ok := g.gz[p]; ok {
			if e.mime != "" {
				w.Header().Set("Content-Type", e.mime)
			}
			w.Header().Set("Content-Encoding", "gzip")
			http.ServeContent(w, r, path.Base(p), e.mod, bytes.NewReader(e.body))
			return
		}
	}
	g.srv.ServeHTTP(w, r)
}

// compressibleAsset picks the files worth a gzip copy: textual formats,
// large enough that the wrapper bytes pay for themselves. Already-deflated
// types (png, woff2, gz) are left to the FileServer — recompressing them
// spends memory on a ratio near one.
func compressibleAsset(name string, size int64) bool {
	if size < 512 {
		return false
	}
	switch path.Ext(name) {
	case ".html", ".js", ".css", ".json", ".map", ".svg", ".txt", ".wasm", ".xml", ".webmanifest":
		return true
	}
	return false
}
