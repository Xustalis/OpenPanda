// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/artifact"
)

// newPreviewForTest packs a small static site into a fresh pool and brings the
// preview origin up against it.
func newPreviewForTest(t *testing.T) (*Preview, string) {
	t.Helper()
	arts := artifact.NewStore(t.TempDir())
	tree := t.TempDir()
	page := `<!doctype html><html><head><link rel="stylesheet" href="style.css"></head>` +
		`<body><h1>报告</h1><p>全部通过。</p></body></html>`
	if err := os.WriteFile(filepath.Join(tree, "index.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "style.css"), []byte("h1{color:red}"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := arts.PackDir(tree)
	if err != nil {
		t.Fatal(err)
	}
	p, err := StartPreview(PreviewDeps{Artifacts: arts, Host: "127.0.0.1", Ancestors: "*"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, m.Hash
}

func fetchPreview(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestPreviewServesSandboxedDocument(t *testing.T) {
	p, hash := newPreviewForTest(t)

	resp, body := fetchPreview(t, p.URL(hash, "index.html"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox") {
		t.Errorf("document is not sandboxed: %q", csp)
	}
	if strings.Contains(csp, "allow-same-origin") {
		t.Errorf("sandbox must not carry allow-same-origin: %q", csp)
	}
	if !strings.Contains(body, "data-openpanda-picker") {
		t.Errorf("picker script was not injected:\n%s", body)
	}
	if !strings.Contains(body, "报告") {
		t.Errorf("document body was mangled:\n%s", body)
	}

	// A relative resource resolves inside the unpacked tree.
	resp, css := fetchPreview(t, p.Origin()+"/p/"+p.Token()+"/"+hash+"/style.css")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("css status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Errorf("css content-type = %q", got)
	}
	if !strings.Contains(css, "color:red") {
		t.Errorf("css body = %q", css)
	}
}

// TestPreviewRequiresTheToken is the whole access model: without the path token
// nothing is served, and the answer does not distinguish a missing artifact
// from a missing token.
func TestPreviewRequiresTheToken(t *testing.T) {
	p, hash := newPreviewForTest(t)

	for _, url := range []string{
		p.Origin() + "/p/wrong-token/" + hash + "/index.html",
		p.Origin() + "/p/" + p.Token() + "/" + strings.Repeat("0", 64) + "/index.html",
		p.Origin() + "/p/" + p.Token() + "/not-a-hash/index.html",
	} {
		resp, _ := fetchPreview(t, url)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s → %d, want 404", url, resp.StatusCode)
		}
	}
}

// TestPreviewRefusesTraversal keeps a request from reading anything outside the
// unpacked artifact — including the temp root itself.
func TestPreviewRefusesTraversal(t *testing.T) {
	p, hash := newPreviewForTest(t)

	for _, rel := range []string{
		"../../../etc/passwd",
		"..%2f..%2fetc%2fpasswd",
		"/etc/passwd",
	} {
		resp, _ := fetchPreview(t, p.Origin()+"/p/"+p.Token()+"/"+hash+"/"+rel)
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s was served", rel)
		}
	}
}

// TestPreviewRejectsAHashThePoolDoesNotHold: the preview serves this node's
// pool, not any artifact anyone names.
func TestPreviewRejectsAHashThePoolDoesNotHold(t *testing.T) {
	p, _ := newPreviewForTest(t)

	sum := sha256.Sum256([]byte("never packed"))
	missing := hex.EncodeToString(sum[:])
	resp, _ := fetchPreview(t, p.Origin()+"/p/"+p.Token()+"/"+missing+"/index.html")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestPickPreviewEntry pins what the frame opens on.
func TestPickPreviewEntry(t *testing.T) {
	cases := []struct {
		name    string
		entries []artifact.EntryMeta
		want    string
		kind    string
	}{
		{
			name: "index wins",
			entries: []artifact.EntryMeta{
				{Path: "a.css"}, {Path: "index.html"}, {Path: "b.js"},
			},
			want: "index.html", kind: "html",
		},
		{
			name: "first document when there is no index",
			entries: []artifact.EntryMeta{
				{Path: "notes.md"}, {Path: "report.html"},
			},
			want: "report.html", kind: "html",
		},
		{
			// macOS tars carry "._index.html" next to the real file; framing the
			// resource fork would show binary garbage.
			name: "apple double siblings are skipped",
			entries: []artifact.EntryMeta{
				{Path: "._index.html"}, {Path: "index.html"},
				{Path: "._style.css"}, {Path: "style.css"},
			},
			want: "index.html", kind: "html",
		},
		{
			// The entry point is often packed after its assets.
			name: "index after its assets still wins",
			entries: []artifact.EntryMeta{
				{Path: "app.css"}, {Path: "app.js"}, {Path: "index.html"},
			},
			want: "index.html", kind: "html",
		},
		{
			name: "a screenshot is still worth opening",
			entries: []artifact.EntryMeta{
				{Path: "run.log"}, {Path: "shot.png"},
			},
			want: "shot.png", kind: "media",
		},
		{
			name:    "anything at all",
			entries: []artifact.EntryMeta{{Path: "data.bin"}},
			want:    "data.bin", kind: "file",
		},
		{name: "empty", entries: nil, want: "", kind: "none"},
	}
	for _, tc := range cases {
		got, kind := pickPreviewEntry(tc.entries)
		if got != tc.want || kind != tc.kind {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.name, got, kind, tc.want, tc.kind)
		}
	}
}
