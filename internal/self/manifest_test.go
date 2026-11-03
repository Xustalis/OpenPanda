// SPDX-License-Identifier: AGPL-3.0-or-later

package self

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPatch = `diff --git a/foo.txt b/foo.txt
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/foo.txt
@@ -0,0 +1 @@
+hello
`

func testKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

func producerID(pub ed25519.PublicKey) string {
	return "k:" + hex.EncodeToString(pub)
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func validManifest(t *testing.T) Manifest {
	t.Helper()
	pub, _ := testKeys(t)
	return Manifest{
		Kind:     ManifestKind,
		ID:       "ps-test",
		Base:     testBaseSHA,
		Version:  "0.0.12-preview+self.1",
		Producer: producerID(pub),
		Title:    "test set",
		Files:    []string{"0001-x.patch"},
		Digests:  map[string]string{"0001-x.patch": digestOf([]byte(testPatch))},
	}
}

// testBaseSHA is a pinned commit sha — the only base form a manifest may
// carry, since a symbolic ref would resolve at apply time.
const testBaseSHA = "0123456789abcdef0123456789abcdef01234567"

func TestManifestValidate(t *testing.T) {
	m := validManifest(t)
	if err := m.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	bad := validManifest(t)
	bad.Kind = "openpanda-patchset/2"
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown kind must be rejected — a v1 node must not parse v2")
	}
	bad = validManifest(t)
	bad.ID = "../escape"
	if err := bad.Validate(); err == nil {
		t.Fatal("id with traversal must be rejected — the id is a directory name")
	}
	bad = validManifest(t)
	bad.Base = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("empty base must be rejected")
	}
	bad = validManifest(t)
	bad.Base = "main"
	if err := bad.Validate(); err == nil {
		t.Fatal("a symbolic base must be rejected — it resolves at apply time, not at review time")
	}
	bad = validManifest(t)
	bad.Files = nil
	if err := bad.Validate(); err == nil {
		t.Fatal("no patch files must be rejected")
	}
	bad = validManifest(t)
	bad.Files = []string{"sub/dir.patch"}
	if err := bad.Validate(); err == nil {
		t.Fatal("patch file with a path element must be rejected")
	}
	bad = validManifest(t)
	bad.Files = []string{"..\\win.patch"}
	if err := bad.Validate(); err == nil {
		t.Fatal("windows-style traversal must be rejected")
	}
	// Content must be bound to the signature: a listed patch without a digest
	// could be rewritten after signing.
	bad = validManifest(t)
	bad.Digests = nil
	if err := bad.Validate(); err == nil {
		t.Fatal("manifest without patch digests must be rejected")
	}
}

func TestManifestSignVerify(t *testing.T) {
	pub, priv := testKeys(t)
	m := validManifest(t)
	m.Producer = producerID(pub)

	if err := m.VerifySignature(); err == nil {
		t.Fatal("unsigned manifest must not verify")
	}
	m.Sign(priv)
	if err := m.VerifySignature(); err != nil {
		t.Fatalf("signed manifest must verify: %v", err)
	}
	// Tamper after signing: the recompute-vs-claim contract only holds if any
	// field flip breaks the signature.
	m.Title = "replaced after the fact"
	if err := m.VerifySignature(); err == nil {
		t.Fatal("tampered manifest must not verify")
	}
	// Digests are part of the signed body: swapping one out is tampering.
	m = validManifest(t)
	m.Producer = producerID(pub)
	m.Sign(priv)
	m.Digests["0001-x.patch"] = digestOf([]byte("different bytes"))
	if err := m.VerifySignature(); err == nil {
		t.Fatal("rewritten patch digest must break the signature")
	}
	// A signature by a different key under the same producer id must fail.
	_, other := testKeys(t)
	m = validManifest(t)
	m.Producer = producerID(pub)
	m.Sign(other)
	if err := m.VerifySignature(); err == nil {
		t.Fatal("signature by the wrong key must not verify")
	}
	// A producer id that is not a k:<pub> must fail cleanly.
	m = validManifest(t)
	m.Producer = "node-1234"
	m.Sign(priv)
	if err := m.VerifySignature(); err == nil {
		t.Fatal("non k:<pub> producer must not verify")
	}
}

func TestLoadManifest(t *testing.T) {
	pub, priv := testKeys(t)
	dir := t.TempDir()
	m := validManifest(t)
	m.Producer = producerID(pub)
	m.Sign(priv)
	// Marshal the signed struct — field order is canonical, so the signature
	// verifies against exactly these bytes.
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0001-x.patch"), []byte(testPatch), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.ID != m.ID || got.Producer != m.Producer {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// Rewriting the patch after signing must fail the load even though the
	// manifest itself still verifies.
	tampered := []byte(testPatch + "+smuggled\n")
	if err := os.WriteFile(filepath.Join(dir, "0001-x.patch"), tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("patch bytes that do not match the signed digest must not load")
	}
	if err := os.WriteFile(filepath.Join(dir, "0001-x.patch"), []byte(testPatch), 0o644); err != nil {
		t.Fatal(err)
	}
	// A listed patch that is missing must fail the load.
	if err := os.Remove(filepath.Join(dir, "0001-x.patch")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("manifest listing a missing patch must not load")
	}
}

func TestPatchTouchedPathsAndCritical(t *testing.T) {
	patch := `diff --git a/internal/core/x.go b/internal/core/x.go
index 1..2 100644
--- a/internal/core/x.go
+++ b/internal/core/x.go
@@ -1 +1 @@
-a
+b
diff --git a/docs/x.md b/docs/x.md
index 1..2 100644
--- a/docs/x.md
+++ b/docs/x.md
@@ -1 +1 @@
-a
+b
`
	paths := PatchTouchedPaths([]byte(patch))
	if len(paths) != 2 || paths[0] != "internal/core/x.go" || paths[1] != "docs/x.md" {
		t.Fatalf("touched paths: %v", paths)
	}
	for _, p := range []string{
		"internal/updater/apply.go", "internal/security/keys.go",
		"internal/trust/policy.go", "internal/defense/x.go",
		"internal/bus/payloads.go", "internal/storage/migrations.go",
		"Makefile", "go.mod", "go.sum", ".github/workflows/ci.yml",
		"scripts/package.sh", "scripts/sign-release.sh",
		// Payloads that leave the Go build or run in the gate itself.
		"adapters/claude_code.py", "extensions/voice/tts.py",
		"drivers/panda-input", "tests/adapter_contract_test.py",
		"cmd/panda/main.go",
	} {
		if !IsCriticalPath(p) {
			t.Fatalf("%s must be critical", p)
		}
	}
	for _, p := range []string{"internal/core/x.go", "webui/app/src/x.tsx", "docs/x.md"} {
		if IsCriticalPath(p) {
			t.Fatalf("%s must NOT be critical", p)
		}
	}
}

// TestPatchTouchedPathsCoverage pins the ways a critical path can hide from a
// `+++ b/`-only parser: deletions (+++ /dev/null), pure renames, paths git
// quotes because of spaces, and quoted octal bytes.
func TestPatchTouchedPathsCoverage(t *testing.T) {
	patch := `diff --git a/internal/updater/download.go b/internal/updater/download.go
deleted file mode 100644
index 1111111..0000000
--- a/internal/updater/download.go
+++ /dev/null
@@ -1 +0,0 @@
-x
diff --git a/internal/defense/moved.go b/internal/security/moved.go
similarity index 100%
rename from internal/defense/moved.go
rename to internal/security/moved.go
diff --git "a/docs/a b.md" "b/docs/a b.md"
index 1..2 100644
--- "a/docs/a b.md"
+++ "b/docs/a b.md"
@@ -1 +1 @@
-a
+b
diff --git "a/internal/updater/\344\270\255.go" "b/internal/updater/\344\270\255.go"
index 1..2 100644
--- "a/internal/updater/\344\270\255.go"
+++ "b/internal/updater/\344\270\255.go"
@@ -1 +1 @@
-a
+b
`
	paths := PatchTouchedPaths([]byte(patch))
	want := map[string]bool{
		"internal/updater/download.go": true,
		"internal/security/moved.go":   true,
		"docs/a b.md":                  true,
		"internal/updater/中.go":        true,
	}
	for _, p := range paths {
		delete(want, p)
		if !IsCriticalPath(p) && p != "docs/a b.md" {
			t.Fatalf("%s should have been recognised as critical", p)
		}
	}
	if len(want) != 0 {
		t.Fatalf("paths missed: %v (got %v)", want, paths)
	}
	crit, hits, err := TouchesCritical(t.TempDir(), nil)
	if err != nil || crit || len(hits) != 0 {
		t.Fatalf("empty set: crit=%v hits=%v err=%v", crit, hits, err)
	}
}

func TestTouchesCriticalRecomputes(t *testing.T) {
	// The merge side must never trust the manifest's claim: recompute reads
	// the patch bodies themselves (design §2.3).
	dir := t.TempDir()
	criticalPatch := `diff --git a/internal/trust/policy.go b/internal/trust/policy.go
index 1..2 100644
--- a/internal/trust/policy.go
+++ b/internal/trust/policy.go
@@ -1 +1 @@
-a
+b
`
	if err := os.WriteFile(filepath.Join(dir, "a.patch"), []byte(criticalPatch), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.patch"), []byte(testPatch), 0o644); err != nil {
		t.Fatal(err)
	}
	crit, hits, err := TouchesCritical(dir, []string{"a.patch", "b.patch"})
	if err != nil || !crit {
		t.Fatalf("critical patch must be detected: crit=%v err=%v", crit, err)
	}
	if len(hits) != 1 || hits[0] != "internal/trust/policy.go" {
		t.Fatalf("hits: %v", hits)
	}
	crit, _, err = TouchesCritical(dir, []string{"b.patch"})
	if err != nil || crit {
		t.Fatalf("benign patch must not flag critical: %v %v", crit, err)
	}
	if _, _, err := TouchesCritical(dir, []string{"missing.patch"}); err == nil {
		t.Fatal("missing patch file must error")
	}
}

// TestPinnedBaseAcceptsBothObjectFormats pins the base grammar: a full SHA-1
// or SHA-256 commit id, never a ref and never a truncated prefix.
func TestPinnedBaseAcceptsBothObjectFormats(t *testing.T) {
	for _, base := range []string{strings.Repeat("a", 40), strings.Repeat("B", 64)} {
		if !pinnedBaseRe.MatchString(base) {
			t.Fatalf("pinned base %q rejected", base)
		}
	}
	for _, base := range []string{"main", "HEAD", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("a", 63), strings.Repeat("z", 40)} {
		if pinnedBaseRe.MatchString(base) {
			t.Fatalf("unpinned base %q accepted", base)
		}
	}
}
