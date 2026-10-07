// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
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
// checksums.txt.sig is required; without one, the historical unsigned path
// still works.
func TestDownloadReleaseSignatureEnforcement(t *testing.T) {
	const version = "9.9.9"
	name := AssetName(version)
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
	if _, err := downloadRelease(context.Background(), "test/repo", version, dir, key); err != nil {
		t.Fatalf("signed release refused: %v", err)
	}

	// A wrong key must refuse the release outright.
	if _, err := downloadRelease(context.Background(), "test/repo", version, t.TempDir(), hex.EncodeToString(mustKey(t))); err == nil {
		t.Fatal("a release signed by another key was accepted")
	}

	// A configured key with no signature asset must refuse (fail closed).
	noSig := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			fmt.Fprint(w, sums)
			return
		}
		http.NotFound(w, r)
	}))
	defer noSig.Close()
	cleanup2 := SetDownloadBaseForTest(noSig.URL)
	defer cleanup2()
	if _, err := downloadRelease(context.Background(), "test/repo", version, t.TempDir(), key); err == nil {
		t.Fatal("an unsigned release passed with a release key configured")
	}

	// No key: the historical behavior — checksums only, no .sig required.
	cleanup3 := SetDownloadBaseForTest(srv.URL)
	defer cleanup3()
	archive, err := downloadRelease(context.Background(), "test/repo", version, t.TempDir(), "")
	if err != nil {
		t.Fatalf("key-less download refused: %v", err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("archive missing: %v", err)
	}
	if filepath.Base(archive) != name {
		t.Fatalf("archive = %s, want %s", archive, name)
	}
}
