// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"archive/tar"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifyChecksumsSig pins the accepted encodings and the fail-closed
// posture: a wrong key, a malformed key or signature, or an empty signature
// must all refuse.
func TestVerifyChecksumsSig(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sums := "abc123  panda-9.9.9-linux-amd64.tar.gz\n"
	sig := ed25519.Sign(priv, []byte(sums))
	hexKey := hex.EncodeToString(pub)
	b64Key := base64.StdEncoding.EncodeToString(pub)

	for _, tc := range []struct {
		name   string
		key    string
		sigTxt string
		ok     bool
	}{
		{"hex key and hex sig", hexKey, hex.EncodeToString(sig), true},
		{"base64 key and base64 sig", b64Key, base64.StdEncoding.EncodeToString(sig), true},
		{"raw base64 sig", b64Key, base64.RawStdEncoding.EncodeToString(sig), true},
		{"comment line before sig", b64Key, "untrusted comment: panda release\n" + base64.StdEncoding.EncodeToString(sig), true},
		{"wrong key", hex.EncodeToString(mustKey(t)), hex.EncodeToString(sig), false},
		{"malformed key", "not-a-key", hex.EncodeToString(sig), false},
		{"malformed sig", hexKey, "!!!not-a-sig!!!", false},
		{"empty sig", hexKey, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyChecksumsSig(tc.key, sums, tc.sigTxt)
			if tc.ok && err != nil {
				t.Fatalf("verifyChecksumsSig = %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("verifyChecksumsSig accepted an invalid signature")
			}
		})
	}
}

func mustKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// TestDownloadReleaseSignatureEnforcement drives the full download path
// against a fake release channel: with a release key configured, a valid
// checksums.txt.sig is required; without one, the download fails closed
// unless the caller explicitly allows unsigned releases.
func TestDownloadReleaseSignatureEnforcement(t *testing.T) {
	const version = "9.9.9"
	name := mustAssetName(t, version)
	asset := []byte("release archive bytes")
	sum := sha256.Sum256(asset)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(sums)))

	mux := http.NewServeMux()
	basePath := "/test/repo/releases/download/v" + version
	mux.HandleFunc(basePath+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sums)
	})
	mux.HandleFunc(basePath+"/checksums.txt.sig", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sig)
	})
	mux.HandleFunc(basePath+"/"+name, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(asset)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cleanup := SetDownloadBaseForTest(srv.URL)
	defer cleanup()

	key := hex.EncodeToString(pub)
	dir := t.TempDir()
	if _, err := downloadRelease(context.Background(), "test/repo", version, dir, key, false, ""); err != nil {
		t.Fatalf("signed release refused: %v", err)
	}

	// A wrong key must refuse the release outright.
	if _, err := downloadRelease(context.Background(), "test/repo", version, t.TempDir(), hex.EncodeToString(mustKey(t)), false, ""); err == nil {
		t.Fatal("a release signed by another key was accepted")
	}

	// A configured key with no signature asset must refuse (fail closed).
	noSig := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			fmt.Fprint(w, sums)
		case strings.HasSuffix(r.URL.Path, name):
			_, _ = w.Write(asset)
		default:
			http.NotFound(w, r)
		}
	}))
	defer noSig.Close()
	cleanup2 := SetDownloadBaseForTest(noSig.URL)
	defer cleanup2()
	if _, err := downloadRelease(context.Background(), "test/repo", version, t.TempDir(), key, false, ""); err == nil {
		t.Fatal("an unsigned release passed with a release key configured")
	}
	// …but allowUnsigned clears even a configured/baked key: the operator's
	// opt-out is what keeps a self-hosted channel that ships no signature
	// reachable from a build that has one compiled in.
	if _, err := downloadRelease(context.Background(), "test/repo", version, t.TempDir(), key, true, ""); err != nil {
		t.Fatalf("unsigned channel refused under allowUnsigned despite a configured key: %v", err)
	}

	// No key: the checksum-only posture is a refusal by default — the
	// hash and the archive cross the same channel, so nothing on the wire
	// can anchor them. An explicit allowUnsigned opts back in.
	cleanup3 := SetDownloadBaseForTest(srv.URL)
	defer cleanup3()
	if _, err := downloadRelease(context.Background(), "test/repo", version, t.TempDir(), "", false, ""); err == nil {
		t.Fatal("an unsigned release passed without allowUnsigned")
	}
	archive, err := downloadRelease(context.Background(), "test/repo", version, t.TempDir(), "", true, "")
	if err != nil {
		t.Fatalf("key-less download refused under allowUnsigned: %v", err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("archive missing: %v", err)
	}
	if filepath.Base(archive) != name {
		t.Fatalf("archive = %s, want %s", archive, name)
	}
}

// TestManagerPipeline drives the check → download → stage → apply-gate →
// cancel flow end-to-end against fake GitHub/release servers. Apply's success
// path (swap the running binary, restart the process) is deliberately not
// exercised in-process — only its refusal gates, which are the contract a
// live fleet depends on.
func TestManagerPipeline(t *testing.T) {
	const version = "9.9.9"

	// Real payload so extractRelease runs for real. The asset name decides
	// which extractor runs — AssetName is a .zip on Windows and a .tar.gz
	// everywhere else — so the fixture must be built to match, or the
	// staging step hands a tar.gz to unzip.
	dir0 := t.TempDir()
	archivePath := filepath.Join(dir0, mustAssetName(t, version))
	if strings.HasSuffix(archivePath, ".zip") {
		writeZip(t, archivePath, map[string][]byte{
			"openpanda/bin/" + exeName(): make([]byte, 16),
		}, nil)
	} else {
		writeTarGz(t, archivePath, []*tar.Header{
			{Name: "openpanda/", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "openpanda/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "openpanda/bin/" + exeName(), Typeflag: tar.TypeReg, Mode: 0o755, Size: 16},
		})
	}
	asset, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(asset)
	name := mustAssetName(t, version)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(sums)))

	mux := http.NewServeMux()
	dlPath := "/test/repo/releases/download/v" + version
	mux.HandleFunc(dlPath+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, sums) })
	mux.HandleFunc(dlPath+"/checksums.txt.sig", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, sig) })
	mux.HandleFunc(dlPath+"/"+name, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(asset) })
	mux.HandleFunc("/repos/test/repo/releases", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"v9.9.9","name":"9.9.9 Testname","body":"notes"}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer SetAPIBaseForTest(srv.URL)()
	defer SetDownloadBaseForTest(srv.URL)()

	var notified []string
	m := New(Options{
		Repo:        "test/repo",
		Current:     "1.0.0",
		ReleaseKey:  hex.EncodeToString(pub),
		Idle:        func(context.Context) bool { return true },
		OnAvailable: func(v, c string) { notified = append(notified, v+":"+c) },
	})

	ctx := context.Background()

	// Download before a check must refuse — nothing is known to be newer.
	// It is a precondition refusal, not a download failure, so the stage
	// stays put rather than going StageError.
	if err := m.Download(ctx); err == nil || !strings.Contains(err.Error(), "no newer") {
		t.Fatalf("Download with no known latest = %v", err)
	}

	if err := m.Check(ctx); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if st := m.Status(); st.Stage != StageAvailable || st.Latest != version || !st.Available {
		t.Fatalf("status after check = %+v", st)
	}
	if len(notified) != 1 || notified[0] != "9.9.9:Testname" {
		t.Errorf("OnAvailable = %v", notified)
	}
	// A second check on the same version must not re-fire the notice.
	if err := m.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 1 {
		t.Errorf("OnAvailable re-fired: %v", notified)
	}

	if err := m.Download(ctx); err != nil {
		t.Fatalf("Download: %v", err)
	}
	st := m.Status()
	if st.Stage != StageStaged {
		t.Fatalf("stage after download = %v", st.Stage)
	}
	if m.staged == nil {
		t.Fatal("staged release not recorded")
	}
	stagedDir := m.staged.dir
	if _, err := os.Stat(filepath.Join(m.staged.root, "bin", exeName())); err != nil {
		t.Fatalf("staged binary missing: %v", err)
	}

	// Apply refuses while the queue reports busy. The success path is not
	// exercised: applyRelease swaps the running executable, which in a test
	// is the test binary itself.
	busy := New(Options{Repo: "test/repo", Current: "1.0.0", Idle: func(context.Context) bool { return false }})
	busy.staged = m.staged // same staged payload, different idle gate
	if err := busy.Apply(ctx); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("Apply while busy = %v", err)
	}
	busy.staged = nil // hand custody back to m for Cancel's cleanup check

	// A fresh manager with nothing staged refuses Apply outright.
	empty := New(Options{Repo: "test/repo"})
	if err := empty.Apply(ctx); err == nil {
		t.Error("Apply with nothing staged succeeded")
	}

	// Cancel drops the staged payload and scrubs the staging dir.
	m.Cancel()
	if st := m.Status(); st.Stage != StageIdle || st.Latest != "" {
		t.Errorf("status after cancel = %+v", st)
	}
	if _, err := os.Stat(stagedDir); !os.IsNotExist(err) {
		t.Error("staging dir left behind after Cancel")
	}
}
