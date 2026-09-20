package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAssetNameFor(t *testing.T) {
	tests := []struct {
		os, arch, want string
	}{
		{"darwin", "amd64", "panda-1.2.3-darwin-amd64.tar.gz"},
		{"darwin", "arm64", "panda-1.2.3-darwin-arm64.tar.gz"},
		{"linux", "amd64", "panda-1.2.3-linux-amd64.tar.gz"},
		{"linux", "arm64", "panda-1.2.3-linux-arm64.tar.gz"},
		{"windows", "amd64", "panda-1.2.3-windows-amd64.zip"},
		{"windows", "arm64", "panda-1.2.3-windows-arm64.zip"},
	}
	for _, tt := range tests {
		if got := assetNameFor("1.2.3", tt.os, tt.arch); got != tt.want {
			t.Errorf("assetNameFor(%q, %q) = %q, want %q", tt.os, tt.arch, got, tt.want)
		}
	}
}

func TestCompareVersion(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"older", "v0.0.2", "0.0.3", -1},
		{"equal", "0.0.3", "v0.0.3", 0},
		{"newer", "0.0.10", "0.0.3", 1},
		{"missing patch", "1.2", "1.2.0", 0},
		// SemVer 2.0 §11.4: normal release > prerelease
		{"preview vs release", "0.0.8", "0.0.8-preview", 1},
		{"preview vs release reversed", "0.0.8-preview", "0.0.8", -1},
		{"suffix older than release", "1.2-rc1", "1.2", -1},
		{"release newer than suffix", "1.2", "1.2-rc1", 1},
		// SemVer 2.0 §11.4.4: prerelease ordering
		{"alpha vs beta", "1.0.0-alpha", "1.0.0-beta", -1},
		{"alpha vs alpha.1", "1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"alpha.1 vs alpha.beta", "1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"beta.2 vs beta.11", "1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"beta.11 vs rc.1", "1.0.0-beta.11", "1.0.0-rc.1", -1},
		{"rc.1 vs 1.0.0", "1.0.0-rc.1", "1.0.0", -1},
		{"equal prereleases", "0.0.8-preview", "v0.0.8-preview", 0},
		{"newer major with prerelease", "0.0.9-preview", "0.0.8", 1},
		{"build metadata ignored", "1.0.0+build1", "1.0.0+build2", 0},
		{"build metadata with prerelease", "1.0.0-alpha+001", "1.0.0-alpha", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompareVersion(tt.a, tt.b); got != tt.want {
				t.Fatalf("CompareVersion(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestChecksumFor(t *testing.T) {
	data := "abc  panda-1.2.3-linux-amd64.tar.gz\n" +
		"def *panda-1.2.3-windows-amd64.zip\n"
	if got := checksumFor(data, "panda-1.2.3-linux-amd64.tar.gz"); got != "abc" {
		t.Fatalf("checksumFor returned %q, want %q", got, "abc")
	}
	if got := checksumFor(data, "missing.zip"); got != "" {
		t.Fatalf("checksumFor missing entry = %q", got)
	}
}

func TestSummarizeNotes(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"empty", "", ""},
		{"blank only", "\n \n\t\n", ""},
		{"keeps plain lines", "## 新增\n- adapter harness\n- 更新器回滚", "## 新增\n- adapter harness\n- 更新器回滚"},
		{"strips images and html", "![logo](https://x/y.png) hello <b>world</b>", "hello world"},
		{"caps line count", "1\n\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14",
			"1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12"},
		{"caps length with ellipsis", string(make([]rune, 700)), string(make([]rune, 600)) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeNotes(tt.body); got != tt.want {
				t.Fatalf("summarizeNotes = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckPreviewToRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]githubRelease{
			{TagName: "v0.0.8", Body: "Release 0.0.8 notes", Prerelease: false},
			{TagName: "v0.0.8-preview", Body: "Preview notes", Prerelease: true},
			{TagName: "v0.0.7", Body: "Release 0.0.7 notes", Prerelease: false},
		})
	}))
	defer srv.Close()

	origBase := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = origBase }()

	m := New(Options{Current: "0.0.8-preview"})
	if err := m.Check(context.Background()); err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	st := m.Status()
	if !st.Available {
		t.Fatalf("expected update to be available from 0.0.8-preview to 0.0.8, got: %+v", st)
	}
	if st.Latest != "0.0.8" {
		t.Fatalf("expected latest version to be 0.0.8, got %q", st.Latest)
	}
}

func TestCheckPrereleaseFiltering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]githubRelease{
			{TagName: "v0.0.9-rc.1", Body: "RC notes", Prerelease: true},
			{TagName: "v0.0.8", Body: "Release 0.0.8 notes", Prerelease: false},
		})
	}))
	defer srv.Close()

	origBase := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = origBase }()

	// Case 1: stable user (0.0.8) without --pre should not see 0.0.9-rc.1
	m1 := New(Options{Current: "0.0.8", IncludePrerelease: false})
	if err := m1.Check(context.Background()); err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if m1.Status().Available {
		t.Fatalf("stable user should not see prerelease update, got: %+v", m1.Status())
	}

	// Case 2: stable user with --pre should see 0.0.9-rc.1
	m2 := New(Options{Current: "0.0.8", IncludePrerelease: true})
	if err := m2.Check(context.Background()); err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !m2.Status().Available || m2.Status().Latest != "0.0.9-rc.1" {
		t.Fatalf("expected prerelease 0.0.9-rc.1 to be available, got: %+v", m2.Status())
	}
}
