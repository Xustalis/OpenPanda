// SPDX-License-Identifier: AGPL-3.0-or-later

// Package self is the self-evolution pipeline's local machinery: signed
// patch-set manifests, the on-disk
// queue under <data>/self/, the git worktree the patch sets merge into, and
// the release-equivalent gate runner. Everything here is opt-in — nothing in
// this package runs unless the operator enabled self_evolution, minted or
// imported a patch set, and approved its merge through the task-review gate.
package self

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ManifestKind is the only accepted `kind` value — the format version travels
// inside the tag so a future v2 cannot be parsed by v1 nodes.
const ManifestKind = "openpanda-patchset/1"

// GateLeg is one leg of the release gate's outcome on the PRODUCING node. The
// digest is evidence, not authority: the merge-side gate re-runs the real
// thing, but the digest lets a reviewer see what the producer claims passed
// and lets the mesh distrust a set that never saw a gate at all.
type GateLeg struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Sec  int64  `json:"sec"`
	Tail string `json:"tail,omitempty"` // last lines when the leg failed
}

// GateDigest summarizes the producer-side `make gate` run.
type GateDigest struct {
	OK    bool      `json:"ok"`
	At    int64     `json:"at"`
	Legs  []GateLeg `json:"legs"`
	Maker string    `json:"maker"` // go version string, ties the digest to a toolchain
}

// Manifest is openpanda-patchset/1: a signed declaration of what a patch set
// is, where it applies, and who produced it.
type Manifest struct {
	Kind     string   `json:"kind"`  // ManifestKind
	ID       string   `json:"id"`    // patch-set id, directory name under self/queue/
	Base     string   `json:"base"`  // git ref/commit the patches were produced against
	Version  string   `json:"ver"`   // target version stamp, e.g. 0.0.12-preview+self.3
	Producer string   `json:"prod"`  // k:<ed25519-pub> stable id of the producing node
	Title    string   `json:"title"` // human summary, shown in review
	Files    []string `json:"files"` // patch file names, applied in order
	// Digests binds each patch file's bytes to the signature: without it a
	// patch could be edited after signing and the manifest would still verify.
	// Go marshals string-keyed maps in sorted order, so canonicalBytes stays
	// deterministic.
	Digests map[string]string `json:"digests"`
	Gate    GateDigest        `json:"gate"`
	// TouchesCritical is the PRODUCER's claim; merge re-derives it from the
	// actual patches (TouchesCritical). A set that claims false but touches a
	// critical path is a red flag, not an error — the recomputed bit wins.
	TouchesCritical bool   `json:"critical"`
	Sig             string `json:"sig,omitempty"` // ed25519 hex over canonical manifest minus sig
}

// canonicalBytes is the signature payload: the manifest with Sig cleared,
// marshaled through the struct (field order is the struct order, so it is
// deterministic — no map key sorting needed).
func (m Manifest) canonicalBytes() []byte {
	m.Sig = ""
	b, _ := json.Marshal(m)
	return b
}

// Sign attaches the producing node's Ed25519 signature.
func (m *Manifest) Sign(priv ed25519.PrivateKey) {
	m.Sig = hex.EncodeToString(ed25519.Sign(priv, m.canonicalBytes()))
}

// ProducerPub returns the producer's Ed25519 key decoded from the manifest's
// k:<pub> id, so signature verification never depends on a directory lookup.
func (m Manifest) ProducerPub() (ed25519.PublicKey, error) {
	raw := strings.TrimPrefix(m.Producer, "k:")
	pub, err := hex.DecodeString(raw)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("manifest producer %q is not a k:<pub> id", m.Producer)
	}
	return ed25519.PublicKey(pub), nil
}

// VerifySignature checks the manifest's self-consistency: the signature must
// verify under the key the Producer id claims. Callers deciding whether THAT
// producer is trusted use the trust layer — this only proves the manifest was
// minted by the holder of its own claimed key.
func (m Manifest) VerifySignature() error {
	if m.Sig == "" {
		return fmt.Errorf("manifest carries no signature")
	}
	sig, err := hex.DecodeString(m.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("manifest signature malformed")
	}
	pub, err := m.ProducerPub()
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, m.canonicalBytes(), sig) {
		return fmt.Errorf("manifest signature does not verify")
	}
	return nil
}

// Validate checks the manifest's structural sanity (independent of signature).
func (m Manifest) Validate() error {
	if m.Kind != ManifestKind {
		return fmt.Errorf("manifest kind %q, want %q", m.Kind, ManifestKind)
	}
	if m.ID == "" || strings.ContainsAny(m.ID, "/\\") || strings.Contains(m.ID, "..") {
		return fmt.Errorf("manifest id %q is not a safe path element", m.ID)
	}
	if m.Base == "" {
		return fmt.Errorf("manifest base is empty")
	}
	// The base must be a pinned commit, never a ref. A symbolic base ("main",
	// "HEAD") resolves at apply time, so the tree the set was reviewed
	// against and the tree the patch lands on can differ — the merge would
	// apply bytes nobody reviewed.
	if !pinnedBaseRe.MatchString(m.Base) {
		return fmt.Errorf("manifest base %q is not a pinned commit sha", m.Base)
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("manifest carries no patch files")
	}
	for _, f := range m.Files {
		if f == "" || filepath.IsAbs(f) || strings.Contains(f, "..") || strings.ContainsRune(f, '/') || strings.ContainsRune(f, '\\') {
			return fmt.Errorf("patch file %q is not a plain filename", f)
		}
		if m.Digests[f] == "" {
			return fmt.Errorf("patch file %q carries no sha256 digest — the manifest would not bind its content", f)
		}
	}
	if len(m.Digests) != len(m.Files) {
		return fmt.Errorf("manifest has %d digests for %d patch files", len(m.Digests), len(m.Files))
	}
	return nil
}

// pinnedBaseRe matches the only base form a patch set may carry: a full
// commit sha. Both object formats are accepted — SHA-1 (40 hex) and SHA-256
// (64 hex) — so a set minted in a `git init --object-format=sha256` repo can
// still be read back instead of failing validation after its manifest was
// rewritten.
var pinnedBaseRe = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// Digest identifies the exact set a reviewer approved: the sha256 over the
// canonical manifest (the signature payload). A rebase rewrites the manifest,
// so the digest moves with it — a proposal can therefore detect that the
// artifact on disk is no longer the one that was reviewed.
func (m Manifest) Digest() string {
	sum := sha256.Sum256(m.canonicalBytes())
	return hex.EncodeToString(sum[:])
}

// patchDigests hashes every patch file in dir so the manifest can bind the
// exact bytes that will later be applied.
func patchDigests(dir string, files []string) (map[string]string, error) {
	out := make(map[string]string, len(files))
	for _, f := range files {
		body, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return nil, fmt.Errorf("read patch %s: %w", f, err)
		}
		sum := sha256.Sum256(body)
		out[f] = hex.EncodeToString(sum[:])
	}
	return out, nil
}

// CriticalPathPrefixes are the trees a patch set must flag before it may be
// merged — the parts of the system whose self-modification carries the most
// risk: the update/verify machinery itself, the trust and defense layers,
// the wire codec, storage migrations, and the gate definition.
var CriticalPathPrefixes = []string{
	"internal/updater/",
	"internal/security/",
	"internal/trust/",
	"internal/defense/",
	"internal/bus/",
	"internal/storage/",
	// The self-evolution pipeline itself: a patch that weakens the gate, the
	// manifest checks, or the swap guard is exactly the change a human must
	// look at.
	"internal/self/",
	// Executable payloads that leave this project's Go build: the Python
	// adapters, voice sidecars and hardware drivers are installed next to the
	// binary and run on every task, tests/ is executed by the gate itself,
	// and cmd/ becomes the swapped binary.
	"adapters/",
	"extensions/",
	"drivers/",
	"tests/",
	"cmd/",
	"Makefile",
	"go.mod", "go.sum",
	".github/",
	"scripts/package.sh", "scripts/sign",
}

// PatchTouchedPaths extracts the file paths a unified diff touches. It reads
// BOTH the `diff --git a/… b/…` header and `+++ b/…` lines: relying on `+++`
// alone misses deletions (`+++ /dev/null`), pure renames (no +++ line), and
// anything git quotes because the path carries spaces or non-ASCII bytes.
func PatchTouchedPaths(patch []byte) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		p = strings.TrimPrefix(p, "a/")
		p = strings.TrimPrefix(p, "b/")
		p = strings.TrimPrefix(filepath.ToSlash(p), "./")
		if p == "" || p == "/dev/null" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, raw := range bytes.Split(patch, []byte("\n")) {
		line := strings.TrimSuffix(string(raw), "\r")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			for _, tok := range scanGitArgs(strings.TrimPrefix(line, "diff --git ")) {
				add(tok)
			}
		case strings.HasPrefix(line, "+++ "):
			if tok, _ := scanGitArg(strings.TrimPrefix(line, "+++ "), 0); tok != "" {
				add(tok)
			}
		}
	}
	return out
}

// scanGitArgs splits a git path list that may contain C-quoted arguments
// (git quotes paths with spaces, quotes, or non-ASCII bytes).
func scanGitArgs(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		arg, next := scanGitArg(s, i)
		if arg != "" {
			out = append(out, arg)
		}
		i = next
	}
	return out
}

// scanGitArg reads one argument starting at i: a bare word up to the next
// space, or a quoted string with git's C-style escapes (\", \\, \n, \t and
// octal \NNN for non-UTF-8 bytes).
func scanGitArg(s string, i int) (string, int) {
	if i >= len(s) {
		return "", i
	}
	if s[i] != '"' {
		j := i
		for j < len(s) && s[j] != ' ' {
			j++
		}
		return s[i:j], j
	}
	var b strings.Builder
	j := i + 1
	for j < len(s) {
		c := s[j]
		if c == '"' {
			return b.String(), j + 1
		}
		if c == '\\' && j+1 < len(s) {
			n := s[j+1]
			if n >= '0' && n <= '7' {
				v, k := 0, j+1
				for k < len(s) && k < j+4 && s[k] >= '0' && s[k] <= '7' {
					v = v*8 + int(s[k]-'0')
					k++
				}
				b.WriteByte(byte(v))
				j = k
				continue
			}
			switch n {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte(n)
			}
			j += 2
			continue
		}
		b.WriteByte(c)
		j++
	}
	return b.String(), j
}

// IsCriticalPath reports whether p sits under a critical prefix.
func IsCriticalPath(p string) bool {
	p = strings.TrimPrefix(filepath.ToSlash(p), "./")
	for _, prefix := range CriticalPathPrefixes {
		if strings.HasPrefix(p, prefix) || p == strings.TrimSuffix(prefix, "/") {
			return true
		}
	}
	return false
}

// TouchesCritical recomputes the critical flag from the patch bodies on disk
// — the merge-side check never trusts the manifest's claim.
func TouchesCritical(dir string, files []string) (bool, []string, error) {
	var hits []string
	for _, f := range files {
		body, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return false, nil, fmt.Errorf("read patch %s: %w", f, err)
		}
		for _, p := range PatchTouchedPaths(body) {
			if IsCriticalPath(p) {
				hits = append(hits, p)
			}
		}
	}
	return len(hits) > 0, hits, nil
}

// LoadManifest reads and fully validates a manifest.json in dir: structure,
// signature self-consistency, and that every listed patch file exists.
func LoadManifest(dir string) (Manifest, error) {
	var m Manifest
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("manifest.json: %w", err)
	}
	if err := m.Validate(); err != nil {
		return m, err
	}
	if err := m.VerifySignature(); err != nil {
		return m, err
	}
	// Verify the patch bytes against the signed digests — a file edited after
	// minting must never reach the merge pipeline, even though the manifest
	// itself would still verify.
	for _, f := range m.Files {
		body, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return m, fmt.Errorf("manifest lists %s: %w", f, err)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != m.Digests[f] {
			return m, fmt.Errorf("patch %s does not match its signed digest (sha256 %s)", f, got)
		}
	}
	return m, nil
}
