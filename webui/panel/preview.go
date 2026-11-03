// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// The artifact preview origin.
//
// Task output is untrusted bytes from whichever node ran the task, and some of
// it is HTML with scripts. The console must not render that on its own origin:
// a crafted result would be running with the panel's token in localStorage. So
// previews live on a second HTTP listener — a different port is a different
// origin, which is the only boundary a browser enforces for this.
//
// The server is deliberately narrow. It answers only for archives this node's
// pool actually holds, under a random path token, with every document sandboxed
// and none of it carrying credentials. There is no listing, no traversal, and
// no navigation away from the artifact.

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
)

// PreviewDeps configures the preview origin.
type PreviewDeps struct {
	Artifacts *artifact.Store
	// Host is the interface to bind. The preview follows the console: loopback
	// for a console only this machine can reach, all interfaces when the
	// console itself was opened to the LAN — a preview that stayed on loopback
	// would be an empty frame on every other device.
	Host string
	// Ancestors is the space-separated list of origins allowed to frame a
	// preview (the console's own). Empty allows none, which makes the server
	// useless but honest if a caller forgets.
	Ancestors string
}

// Preview is the running preview origin.
type Preview struct {
	origin    string
	token     string
	srv       *http.Server
	ln        net.Listener
	arts      *artifact.Store
	root      string
	ancestors string

	mu     sync.Mutex
	unpack map[string]*unpackSlot
}

type unpackSlot struct {
	mu   sync.Mutex
	done bool
}

// StartPreview brings up the preview listener on an ephemeral port.
func StartPreview(d PreviewDeps) (*Preview, error) {
	if d.Artifacts == nil {
		return nil, errors.New("panel: preview needs the artifact pool")
	}
	host := d.Host
	if host == "" {
		host = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, fmt.Errorf("panel: preview listen: %w", err)
	}
	root, err := os.MkdirTemp("", "openpanda-preview-*")
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("panel: preview temp dir: %w", err)
	}
	p := &Preview{
		token:     NewToken(),
		ln:        ln,
		arts:      d.Artifacts,
		root:      root,
		ancestors: d.Ancestors,
		unpack:    make(map[string]*unpackSlot),
	}
	p.origin = "http://" + ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /p/{token}/{hash}/{file...}", p.serve)
	p.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// Origin is the base URL a console builds iframe src from.
func (p *Preview) Origin() string { return p.origin }

// Token is the path token; the console prefixes every preview URL with it.
func (p *Preview) Token() string { return p.token }

// URL is the address of one artifact's entry file.
func (p *Preview) URL(hash, entry string) string {
	if entry == "" {
		entry = "index.html"
	}
	return p.origin + "/p/" + p.token + "/" + hash + "/" + escapePath(entry)
}

// OriginFor is the base URL as the browser behind one particular request can
// reach it. Under a wildcard bind the listener's own address is 0.0.0.0 or ::,
// which no browser will load; the console was reached on some host, and the
// preview listens on the same address family, so that host is the one to hand
// back. A loopback request yields the loopback origin unchanged.
func (p *Preview) OriginFor(requestHost string) string {
	_, port, err := net.SplitHostPort(strings.TrimPrefix(p.origin, "http://"))
	if err != nil {
		return p.origin
	}
	host, _, err := net.SplitHostPort(requestHost)
	if err != nil || host == "" {
		return p.origin
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return p.origin
	}
	return "http://" + net.JoinHostPort(host, port)
}

// URLFor is URL resolved for the host a request arrived on.
func (p *Preview) URLFor(requestHost, hash, entry string) string {
	if entry == "" {
		entry = "index.html"
	}
	return p.OriginFor(requestHost) + "/p/" + p.token + "/" + hash + "/" + escapePath(entry)
}

// Close stops the listener and drops the unpack cache.
func (p *Preview) Close() error {
	err := p.srv.Close()
	_ = os.RemoveAll(p.root)
	return err
}

// escapePath percent-escapes each segment of a slash-separated relative path,
// leaving the separators so the URL keeps its shape.
func escapePath(rel string) string {
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// serve answers one preview request.
func (p *Preview) serve(w http.ResponseWriter, r *http.Request) {
	// The token is compared first: an unauthenticated caller learns nothing,
	// not even whether the hash exists.
	if r.PathValue("token") != p.token {
		http.NotFound(w, r)
		return
	}
	hash := r.PathValue("hash")
	if !validArtifactHash(hash) {
		http.NotFound(w, r)
		return
	}
	rel := r.PathValue("file")
	if rel == "" {
		rel = "index.html"
	}
	clean, ok := safePreviewPath(rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	dir, err := p.unpackArchive(hash)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("artifact is not in this node's pool"))
		return
	}

	full := filepath.Join(dir, filepath.FromSlash(clean))
	if st, err := os.Stat(full); err == nil && st.IsDir() {
		full = filepath.Join(full, "index.html")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Every response is a sandboxed document. allow-same-origin is withheld on
	// purpose: the artifact's scripts run, but in an opaque origin with no
	// storage and no access to anything the console holds. Forms and popups
	// stay for pages that legitimately use them; navigation of the top frame
	// does not, so a crafted result cannot steer the console's tab.
	//
	// The source directives matter as much as the sandbox: the page URL carries
	// the preview token, and an artifact script can read location.href. With no
	// connect-src/img-src restriction, one fetch to an attacker's host hands
	// that token over. 'self' keeps the artifact able to load its own resources
	// and go nowhere else.
	h := w.Header()
	policy := "sandbox allow-scripts allow-forms allow-popups; " +
		"default-src 'self'; script-src 'self' 'unsafe-inline'; " +
		"style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"font-src 'self' data:; connect-src 'self'; " +
		"form-action 'none'; base-uri 'none'"
	// frame-ancestors is the one directive that makes the token meaningful:
	// only the console may put this document in a frame.
	if p.ancestors != "" {
		policy += "; frame-ancestors " + p.ancestors
	}
	h.Set("Content-Security-Policy", policy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")

	if isHTMLPreview(clean, data) {
		data = transcodePage(data, "")
		h.Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(p.injectPicker(data))
		return
	}
	ctype := previewContentType(clean, data)
	if strings.HasPrefix(ctype, "text/") || ctype == "application/json" {
		data = transcodePage(data, "")
	}
	h.Set("Content-Type", ctype)
	_, _ = w.Write(data)
}

// injectPicker puts the annotation script at the end of the body. It is
// injected at serve time rather than written into the archive, so the artifact
// stays byte-for-byte what the task produced (the review binds to its hash, and
// a modified archive would be a different artifact).
func (p *Preview) injectPicker(data []byte) []byte {
	idx := lastIndexFold(data, []byte("</body>"))
	if idx < 0 {
		return append(data, []byte(pickerScript)...)
	}
	out := make([]byte, 0, len(data)+len(pickerScript))
	out = append(out, data[:idx]...)
	out = append(out, []byte(pickerScript)...)
	out = append(out, data[idx:]...)
	return out
}

// lastIndexFold finds needle in haystack case-insensitively without copying it.
// Lowercasing a copy and using the index on the original is the bug this
// avoids: simple case mapping changes UTF-8 length for some runes (U+0130 İ is
// two bytes and lowercases to one, U+212A K is three and lowercases to one), so
// an index taken from the copy lands inside a tag.
func lastIndexFold(haystack, needle []byte) int {
	n := len(needle)
	if n == 0 || len(haystack) < n {
		return -1
	}
	for i := len(haystack) - n; i >= 0; i-- {
		if bytes.EqualFold(haystack[i:i+n], needle) {
			return i
		}
	}
	return -1
}

// pickerScript reports clicks up to the console. It never renders anything
// itself — the comment box, the draft and the decision to send all belong to
// the console, which is the only side that knows what a task is.
const pickerScript = `<script data-openpanda-picker>
(function () {
  if (window.__openpandaPicker) return;
  window.__openpandaPicker = true;
  // Pick mode is on unless something turned it off. The artifact preview wants
  // it on: pointing at what is wrong is that surface's only verb. The browser
  // pane loads the same script and sets it to false, then flips it from its own
  // toolbar — a page being read must not swallow every click.
  function picking() { return window.__openpandaPick !== false; }
  var last = null;
  function cssPath(el) {
    if (!el || el.nodeType !== 1) return "";
    if (el.id) return "#" + (window.CSS && CSS.escape ? CSS.escape(el.id) : el.id);
    var parts = [];
    while (el && el.nodeType === 1 && parts.length < 6) {
      var name = el.nodeName.toLowerCase();
      var parent = el.parentElement;
      if (parent) {
        var idx = Array.prototype.indexOf.call(parent.children, el) + 1;
        name += ":nth-child(" + idx + ")";
      }
      parts.unshift(name);
      el = parent;
    }
    return parts.join(" > ");
  }
  function clear() {
    if (last) { last.style.outline = ""; last = null; }
  }
  window.addEventListener("message", function (e) {
    var d = e.data;
    if (!d || d.source !== "openpanda-pick-mode") return;
    window.__openpandaPick = !!d.on;
    if (!window.__openpandaPick) clear();
  });
  document.addEventListener("mouseover", function (e) {
    if (!picking()) return;
    clear();
    var el = e.target;
    if (!el || el.nodeType !== 1) return;
    el.style.outline = "2px solid #2f6ca0";
    last = el;
  }, true);
  document.addEventListener("click", function (e) {
    if (!picking()) return;
    e.preventDefault();
    e.stopPropagation();
    var el = e.target;
    if (!el || el.nodeType !== 1) return;
    var text = (el.textContent || "").trim().slice(0, 160);
    var rect = el.getBoundingClientRect();
    try {
      parent.postMessage({
        source: "openpanda-preview",
        selector: cssPath(el),
        tag: el.nodeName.toLowerCase(),
        text: text,
        rect: { x: Math.round(rect.x), y: Math.round(rect.y), w: Math.round(rect.width), h: Math.round(rect.height) }
      }, "*");
    } catch (err) {}
  }, true);
  // The frame's own name, for a panel that has chrome around it. A sandboxed
  // document has an opaque origin, so the parent cannot read document.title —
  // this message is the only path. baseURI is the <base> the browser pane
  // injected, i.e. the page the reader actually asked for.
  try {
    parent.postMessage({
      source: "openpanda-page",
      title: document.title || "",
      url: document.baseURI || ""
    }, "*");
  } catch (e) {}
})();
</script>`

// unpackArchive materialises one archive under the preview temp root.
func (p *Preview) unpackArchive(hash string) (string, error) {
	p.mu.Lock()
	slot, ok := p.unpack[hash]
	if !ok {
		slot = &unpackSlot{}
		p.unpack[hash] = slot
	}
	p.mu.Unlock()

	slot.mu.Lock()
	defer slot.mu.Unlock()
	dir := filepath.Join(p.root, hash)
	if slot.done {
		return dir, nil
	}
	if _, err := os.Stat(dir); err == nil {
		slot.done = true
		return dir, nil
	}
	if err := p.extract(hash, dir); err != nil {
		return "", err
	}
	slot.done = true
	return dir, nil
}

// extract materialises the archive under its cache directory.
//
// The unpack is the artifact store's, not a local copy: that version enforces
// the entry count cap, the total-size ceiling and the free-space floor — the
// three limits that keep a peer's archive (a few hundred KB of gzip can expand
// to tens of GB) from filling this node's disk — and it verifies that what it
// unpacked hashes to the name it was stored under. A second, weaker unpacker
// here would be the one place an artifact is trusted without those checks.
func (p *Preview) extract(hash, dir string) error {
	tmp := dir + ".partial"
	_ = os.RemoveAll(tmp)
	if _, err := p.arts.Extract(hash, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	return nil
}

// validArtifactHash accepts the sha256 hex the pool names archives with.
func validArtifactHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// safePreviewPath normalises a request path and refuses anything that would
// leave the unpacked tree.
func safePreviewPath(rel string) (string, bool) {
	clean := path.Clean("/" + rel)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		return "index.html", true
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	if !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", false
	}
	return clean, true
}

// isHTMLPreview decides whether a document goes through the picker injection.
func isHTMLPreview(rel string, data []byte) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".html", ".htm":
		return true
	case "":
		// An extensionless entry point is common for built sites; sniff it.
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(string(headBytes(data, 256)))),
			"<!doctype html")
	}
	return false
}

func headBytes(data []byte, n int) []byte {
	if len(data) < n {
		return data
	}
	return data[:n]
}

// previewContentType names the type from the extension, which is what a page's
// own resource references expect. The archive is not a network surface, so a
// mislabelled file costs the reader one broken image, not an origin.
func previewContentType(rel string, data []byte) string {
	switch strings.ToLower(path.Ext(rel)) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".log", ".md":
		return "text/plain; charset=utf-8"
	}
	return http.DetectContentType(headBytes(data, 512))
}

// ---- HTTP surface for the console ----

// previewTaskArtifact serves GET /api/tasks/{id}/artifact/preview: where the
// console should point its frame, or an explanation it can act on. A pool that
// does not hold the archive is not an error about the page — the bytes are on
// the node that ran the task, and `can_fetch` says whether pulling them is a
// thing this node can do.
func (h *handler) previewTaskArtifact(w http.ResponseWriter, r *http.Request) {
	if h.preview == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("artifact preview is not configured"))
		return
	}
	if h.artifacts == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("artifact pool not configured"))
		return
	}
	t, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such task"))
		return
	}
	hash := t.OutputArtifact
	if hash == "" {
		writeErr(w, http.StatusNotFound, errors.New("task recorded no output artifact"))
		return
	}
	if _, ok := h.artifacts.Has(hash); !ok {
		// Not an error about the task: the bytes are still on the node that ran
		// it. The console reads `state` and offers to pull them. The row's
		// owner is the origin after a delegation, so the source comes from the
		// dispatch target / result executor instead.
		source := artifactSource(r.Context(), h.store, t)
		writeJSON(w, map[string]any{
			"state":     "remote",
			"hash":      hash,
			"source":    source,
			"can_fetch": source != "" && h.currentEngine() != nil,
		})
		return
	}
	entries, err := h.artifacts.Entries(hash)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	entry, kind := pickPreviewEntry(entries)
	writeJSON(w, map[string]any{
		"state":  "ready",
		"id":     t.TaskID,
		"hash":   hash,
		"entry":  entry,
		"kind":   kind,
		"url":    h.preview.URLFor(r.Host, hash, entry),
		"origin": h.preview.OriginFor(r.Host),
		"files":  len(entries),
		"source": t.OwnerNode,
	})
}

// fetchTaskArtifact serves POST /api/tasks/{id}/artifact/fetch — pull the
// archive back from the node that made it so the preview has bytes to serve.
func (h *handler) fetchTaskArtifact(w http.ResponseWriter, r *http.Request) {
	if h.artifacts == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("artifact pool not configured"))
		return
	}
	t, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no such task"))
		return
	}
	hash := t.OutputArtifact
	if hash == "" {
		writeErr(w, http.StatusNotFound, errors.New("task recorded no output artifact"))
		return
	}
	if _, ok := h.artifacts.Has(hash); ok {
		writeJSON(w, map[string]any{"hash": hash, "already": true})
		return
	}
	source := artifactSource(r.Context(), h.store, t)
	if source == "" {
		writeErr(w, http.StatusBadRequest, errors.New("this task does not record the node that ran it"))
		return
	}
	eng := h.currentEngine()
	if eng == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("ask engine not configured"))
		return
	}
	// The transfer is bounded by the request context so a client that walks
	// away does not leave a pull running against a slow peer.
	m, err := eng.FetchArtifact(r.Context(), source, t.TaskID, hash)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"hash": hash, "files": len(m.Entries), "source": source})
}

// pickPreviewEntry chooses what the frame opens on. index.html is the site's
// front door; failing that, the first document, then the first thing a browser
// can show at all — a task that produced only a screenshot is still worth
// opening.
func pickPreviewEntry(entries []artifact.EntryMeta) (string, string) {
	var firstHTML, firstMedia, firstAny string
	for _, e := range entries {
		name := strings.TrimPrefix(filepath.ToSlash(e.Path), "./")
		if name == "" || strings.HasSuffix(name, "/") {
			continue
		}
		// A macOS tar carries AppleDouble siblings ("._index.html") next to the
		// real files. They are resource forks, not pages, and picking one would
		// frame a few hundred bytes of binary.
		if strings.HasPrefix(path.Base(name), "._") {
			continue
		}
		if firstAny == "" {
			firstAny = name
		}
		ext := strings.ToLower(path.Ext(name))
		if ext == ".html" || ext == ".htm" {
			// The front door wins wherever it sits in the archive — an explicit
			// check before the first-document fallback, because a site's entry
			// point is often packed after its assets.
			if base := path.Base(name); base == "index.html" || base == "index.htm" {
				return name, "html"
			}
			if firstHTML == "" {
				firstHTML = name
			}
		}
		switch ext {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".pdf":
			if firstMedia == "" {
				firstMedia = name
			}
		}
	}
	switch {
	case firstHTML != "":
		return firstHTML, "html"
	case firstMedia != "":
		return firstMedia, "media"
	case firstAny != "":
		return firstAny, "file"
	}
	return "", "none"
}
