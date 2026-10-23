// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// githubDownloadBase is where release assets are fetched from. Overridable
// for tests, mirroring githubAPIBase in version.go.
var githubDownloadBase = "https://github.com"

// SetDownloadBaseForTest overrides the release-asset base URL for testing and
// returns a cleanup function that restores the previous value.
func SetDownloadBaseForTest(base string) func() {
	orig := githubDownloadBase
	if base == "" {
		githubDownloadBase = "https://github.com"
	} else {
		githubDownloadBase = base
	}
	return func() {
		githubDownloadBase = orig
	}
}

// httpClient bounds the phases a caller's context may not: a wedged connect
// or a server that never finishes its response headers would otherwise park
// the call until TCP gives up (minutes). Body streaming stays ctx-bound so a
// large asset download on a slow link is not clipped mid-flight.
//
// TLSHandshakeTimeout is deliberately raised from the 10s DefaultTransport
// value. GitHub redirects release downloads to release-assets.githubusercontent.com,
// a separate host that has to complete its own TCP + TLS setup; on links with
// a high RTT to that CDN, or through a proxy that only forwards after
// establishing its own upstream tunnel, a 10s budget expires while the
// handshake is still making progress. The failure then reads "net/http: TLS
// handshake timeout" — indistinguishable from a genuine network block, but
// happening on a link that would otherwise have worked.
var httpClient = &http.Client{Transport: func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	t.TLSHandshakeTimeout = 30 * time.Second
	return t
}()}

// Retry policy. GitHub's asset host is the flakiest hop in this pipeline, and
// a single TCP reset or one slow handshake used to fail the whole update: the
// fetch helpers below are one-shot, so the operator saw "update failed" for a
// condition that clears by itself a second later. Retries are bounded and
// spaced so a genuinely blocked link still fails fast instead of hanging.
const (
	fetchAttempts    = 3
	downloadAttempts = 2 // a release archive is ~35MB; a blocked link must not re-fetch it three times
	retryBaseDelay   = 400 * time.Millisecond
)

// retryable reports whether err is a transient transport failure worth another
// attempt. The test is intentionally about transport, not HTTP status: a 404
// or a 403 from GitHub answers the same way every time, while a reset, a
// timeout, or a DNS hiccup does not. 5xx is not retried here — GitHub's release
// assets are served from object storage that does not return them in practice,
// and treating them as retryable would mean classifying status codes, which
// this helper has no response to do.
func retryable(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	// Any dial/read/write error: connection reset by peer, EOF mid-response,
	// a broken pipe through a proxy. *net.OpError is the wrapper net returns
	// for all of them, including the "remote error" shape a TLS alert takes.
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// withRetry runs fn up to attempts times, sleeping with exponential backoff
// between attempts, and returns the last error unchanged when every attempt
// fails. It stops early on ctx cancellation so a failed update never outlives
// the caller's deadline, and on any non-retryable error so a definitive answer
// (404, signature mismatch) is not masked by two pointless retries.
func withRetry[T any](ctx context.Context, attempts int, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	for attempt := 1; ; attempt++ {
		v, err := fn(ctx)
		if err == nil {
			return v, nil
		}
		if attempt >= attempts || !retryable(err) {
			return zero, err
		}
		timer := time.NewTimer(retryBaseDelay * time.Duration(1<<(attempt-1)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, err
		case <-timer.C:
		}
	}
}

// redactURL keeps scheme, host and path and replaces the query with an
// ellipsis. GitHub release assets are served from signed URLs whose query
// carries a several-hundred-byte token; embedded verbatim in an error, it
// buries the actual cause ("net/http: TLS handshake timeout") far off-screen
// and leaks a credential-shaped string into logs and bug reports.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	u.RawQuery = "…"
	return u.String()
}

// scrubURLError rewrites a *url.Error so its message carries the redacted URL.
// net/http wraps every request failure in one, and the default rendering
// includes the full original URL.
func scrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s %q: %w", ue.Op, redactURL(ue.URL), ue.Err)
	}
	return err
}

// downloadRelease fetches the release asset for version and its checksums.txt,
// verifies the asset's SHA-256, and returns the archive path inside destDir.
// destDir must already exist. A missing checksums entry or a hash mismatch is
// an error: a silent corrupt archive is worse than a failed update.
//
// baseOverride, when non-empty, replaces the whole per-version asset directory
// (not just the host) — the shape OPENPANDA_RELEASE_BASE already has in
// scripts/install.ps1 and install.sh, so one environment variable steers both
// the installer and the self-updater. It exists because the official asset host
// (release-assets.githubusercontent.com) is unreachable from some networks
// while api.github.com and github.com answer fine: without a way to point the
// download at a mirror, such an install could check for updates but never
// complete one. A mirror changes only the transport — the checksums, the
// optional Ed25519 signature and the archive still have to agree with each
// other, so a hostile mirror cannot install anything the official channel
// would not have installed.
//
// When releaseKey is non-empty the release must also carry a valid
// checksums.txt.sig — a detached Ed25519 signature over the raw checksums.txt
// bytes (see verifyChecksumsSig). That is the out-of-band trust anchor: the
// checksums and the archive come from the same channel, so without it a
// compromised channel defeats the hash check by shipping both. An empty
// releaseKey therefore fails closed unless allowUnsigned explicitly accepts
// the checksum-only posture — the refusal happens before any fetch so no
// network path can route around it. allowUnsigned also clears a configured
// or baked-in key: the operator's opt-out covers self-hosted channels that
// ship no signature at all.
func downloadRelease(ctx context.Context, repo, version, destDir, releaseKey string, allowUnsigned bool, baseOverride string) (string, error) {
	if allowUnsigned {
		releaseKey = ""
	} else if releaseKey == "" {
		return "", errors.New("no update release key configured: refusing an unverifiable release (set OPENPANDA_UPDATE_PUBKEY — the official channel's key is published in SECURITY.md — or OPENPANDA_UPDATE_UNSIGNED=1 to accept checksum-only verification)")
	}
	base := githubDownloadBase + "/" + repo + "/releases/download/v" + version
	if o := strings.TrimRight(strings.TrimSpace(baseOverride), "/"); o != "" {
		base = o
	}
	name, err := AssetName(version)
	if err != nil {
		return "", err
	}

	checksumsURL := base + "/checksums.txt"
	sums, err := fetchText(ctx, checksumsURL)
	if err != nil {
		return "", fmt.Errorf("fetch checksums: %w", err)
	}
	if releaseKey != "" {
		sigText, err := fetchText(ctx, base+"/checksums.txt.sig")
		if err != nil {
			return "", fmt.Errorf("fetch checksums signature: %w (a release key is configured, so an unsigned release is refused)", err)
		}
		if err := verifyChecksumsSig(releaseKey, sums, sigText); err != nil {
			return "", err
		}
	}
	want := checksumFor(sums, name)
	if want == "" {
		return "", fmt.Errorf("checksums.txt has no entry for %s", name)
	}

	archive, err := downloadFile(ctx, base+"/"+name, destDir)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", name, err)
	}
	got, err := sha256File(archive)
	if err != nil {
		os.Remove(archive)
		return "", err
	}
	if got != want {
		os.Remove(archive)
		return "", fmt.Errorf("SHA-256 mismatch for %s: want %s, got %s", name, want, got)
	}
	return archive, nil
}

// verifyChecksumsSig checks a detached Ed25519 signature over the raw
// checksums.txt bytes. pubKey and sigText are hex or base64 (standard or URL
// alphabet, padded or raw); sigText may carry a leading comment line, so the
// last whitespace-separated field is used. Any decode failure, wrong length,
// or verification failure is an error — with a release key configured,
// nothing unsigned or unverifiable may install.
//
// Raw Ed25519 keeps this dependency-free (crypto/ed25519, stdlib). The
// release pipeline can produce the signature with:
//
//	openssl pkeyutl -sign -rawin -inkey ed25519.pem -in checksums.txt \
//	  | base64 > checksums.txt.sig
//
// minisign/cosign formats remain future work (they need extra primitives).
func verifyChecksumsSig(pubKey, sums, sigText string) error {
	pub, err := parseEd25519PubKey(pubKey)
	if err != nil {
		return fmt.Errorf("update release key: %w", err)
	}
	sig, err := decodeSignatureText(sigText)
	if err != nil {
		return fmt.Errorf("checksums.txt.sig: %w", err)
	}
	if !ed25519.Verify(pub, []byte(sums), sig) {
		return errors.New("checksums.txt.sig does not verify under the configured release key")
	}
	return nil
}

// parseEd25519PubKey decodes a 32-byte Ed25519 public key from hex or base64.
func parseEd25519PubKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	if raw, err := hex.DecodeString(s); err == nil && len(raw) == ed25519.PublicKeySize {
		return ed25519.PublicKey(raw), nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := enc.DecodeString(s); err == nil && len(raw) == ed25519.PublicKeySize {
			return ed25519.PublicKey(raw), nil
		}
	}
	return nil, errors.New("not a 32-byte Ed25519 public key (hex or base64)")
}

// decodeSignatureText decodes a 64-byte Ed25519 signature from hex or base64,
// tolerating a leading comment line (the minisign-style shape) by taking the
// last whitespace-separated field.
func decodeSignatureText(s string) ([]byte, error) {
	if fields := strings.Fields(s); len(fields) > 0 {
		s = fields[len(fields)-1]
	}
	if raw, err := hex.DecodeString(s); err == nil && len(raw) == ed25519.SignatureSize {
		return raw, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := enc.DecodeString(s); err == nil && len(raw) == ed25519.SignatureSize {
			return raw, nil
		}
	}
	return nil, errors.New("not a 64-byte Ed25519 signature (hex or base64)")
}

// fetchText GETs url and returns its body as a string. Transient transport
// failures are retried (fetchAttempts); a definitive response is not.
func fetchText(ctx context.Context, rawURL string) (string, error) {
	return withRetry(ctx, fetchAttempts, func(ctx context.Context) (string, error) {
		// Metadata payloads are ≤1MB; bound the whole request even when the
		// caller's context is open-ended. Per attempt, so each retry gets a
		// full budget instead of inheriting a nearly spent one.
		actx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(actx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "OpenPanda-updater")
		resp, err := httpClient.Do(req)
		if err != nil {
			return "", scrubURLError(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%s returned %s", redactURL(rawURL), resp.Status)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return "", scrubURLError(err)
		}
		return string(b), nil
	})
}

// downloadFile fetches url into destDir (reusing the URL's base name) and
// returns the written path. The download goes through a ".part" temp file so a
// half-downloaded archive never sits at the final name — which is also what
// makes a retry safe: a truncated attempt removes its temp file and the next
// one starts clean.
func downloadFile(ctx context.Context, rawURL, destDir string) (string, error) {
	return withRetry(ctx, downloadAttempts, func(ctx context.Context) (string, error) {
		// Release archives are ~35MB; a generous ceiling for slow links when
		// the caller's context is open-ended, while ResponseHeaderTimeout
		// still catches a server that stalls before streaming.
		actx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		req, err := http.NewRequestWithContext(actx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "OpenPanda-updater")
		resp, err := httpClient.Do(req)
		if err != nil {
			return "", scrubURLError(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("download %s: %s", redactURL(rawURL), resp.Status)
		}
		if directoryTraversal(filepath.Base(rawURL)) {
			return "", fmt.Errorf("refusing to download from unsafe path %q", rawURL)
		}
		dst := filepath.Join(destDir, filepath.Base(rawURL))
		tmp := dst + ".part"
		f, err := os.Create(tmp)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(f, resp.Body); err != nil {
			f.Close()
			os.Remove(tmp)
			return "", scrubURLError(err)
		}
		if err := f.Close(); err != nil {
			os.Remove(tmp)
			return "", err
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			return "", err
		}
		return dst, nil
	})
}

// checksumFor parses checksums.txt ("<hash>  <name>" or "<hash> *<name>",
// one per line) and returns the lowercase hex hash for name, or "" if the
// file has no entry. The "*" is sha256sum's binary-mode marker — the release
// pipeline writes text mode, but a re-generated file must still parse.
func checksumFor(data, name string) string {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}

// sha256File returns the hex SHA-256 of the file at path.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractRelease unpacks archive into destDir and returns the path of the
// top-level "openpanda/" directory it created. The archive layout is produced
// by scripts/package.sh: a single top-level openpanda/ dir containing
// bin/panda(.exe), adapters/*.py and extensions/voice/*.py. Extraction strips that top component and
// rejects any entry that would escape destDir (zip/tar slip).
func extractRelease(archive, destDir string) (string, error) {
	root := filepath.Join(destDir, "openpanda")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	if strings.HasSuffix(archive, ".zip") {
		if err := unzip(archive, root); err != nil {
			return "", err
		}
	} else {
		if err := untargz(archive, root); err != nil {
			return "", err
		}
	}
	if err := os.Remove(archive); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return root, nil
}

// sanitizeEntry strips the leading "openpanda/" component and returns the
// relative path to materialize inside root. The empty string means "skip" —
// the top-level openpanda dir entry itself, a bare "./", and OS metadata
// junk all earn that: archives packaged on macOS carry AppleDouble shadows
// ("._openpanda", "openpanda/._bin") and "__MACOSX/" trees that hold no
// payload bytes. Anything else outside the release layout is an error:
// absolute paths, ".." traversal, and foreign top-level entries would
// install a partial or unexpected tree, so they refuse the archive outright
// rather than being skipped (the same contract artifact.Unpack enforces).
func sanitizeEntry(name string) (string, error) {
	raw := filepath.ToSlash(strings.TrimPrefix(name, "./"))
	if raw == "" || raw == "openpanda" {
		return "", nil
	}
	if isArchiveJunk(raw) {
		return "", nil
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("release archive entry %q is absolute; refusing", name)
	}
	rel := strings.TrimPrefix(raw, "openpanda/")
	if rel == "" {
		return "", nil // the openpanda/ dir entry itself
	}
	if rel == raw {
		return "", fmt.Errorf("release archive entry %q is outside the openpanda/ tree; refusing", name)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("release archive entry %q traverses out of the extraction root; refusing", name)
	}
	return clean, nil
}

// isArchiveJunk reports whether raw (slash-separated, "openpanda/" prefix not
// yet stripped) is OS packaging noise that carries no payload: macOS
// AppleDouble files are named "._<name>" beside the real entry (a top-level
// "._openpanda" would otherwise read as a foreign top-level entry and sink a
// perfectly good archive), and ditto-made zips wrap their shadows in a
// "__MACOSX/" tree.
func isArchiveJunk(raw string) bool {
	if raw == "__MACOSX" || strings.HasPrefix(raw, "__MACOSX/") {
		return true
	}
	for _, part := range strings.Split(raw, "/") {
		if strings.HasPrefix(part, "._") {
			return true
		}
	}
	return false
}

func directoryTraversal(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return true
	}
	return false
}

// safeLinkTarget rejects a symlink whose target resolves outside root. The link
// is resolved the way the filesystem will resolve it — relative to the directory
// holding the link — so "a/b -> ../../etc" is judged from a/, not from root.
func safeLinkTarget(root, rel, linkname string) error {
	if linkname == "" {
		return fmt.Errorf("release archive symlink %s has an empty target; refusing", rel)
	}
	if filepath.IsAbs(linkname) || strings.HasPrefix(filepath.ToSlash(linkname), "/") {
		return fmt.Errorf("release archive symlink %s -> %s is absolute; refusing", rel, linkname)
	}
	resolved := filepath.Clean(filepath.Join(root, filepath.Dir(rel), filepath.FromSlash(linkname)))
	cleanRoot := filepath.Clean(root)
	if resolved != cleanRoot && !strings.HasPrefix(resolved, cleanRoot+string(filepath.Separator)) {
		return fmt.Errorf("release archive symlink %s -> %s resolves outside the extraction root; refusing", rel, linkname)
	}
	return nil
}

// Release archives bound their decompressed footprint: the checksum verifies
// the archive is the published one, not that the published one is sane, and a
// malformed or hostile release must not fill the disk. The real payload is a
// binary plus small adapter scripts, so these ceilings are generous.
const (
	maxReleaseEntries = 4096
	maxReleaseBytes   = 512 << 20
)

func untargz(archive, root string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var entries, written int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// Only types that materialize a filesystem object need a sane
		// in-tree name; everything else (pax/GNU metadata and unknown
		// vendor headers) writes nothing, so its name is not a path.
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeDir, tar.TypeSymlink, tar.TypeLink,
			tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		default:
			continue
		}
		rel, err := sanitizeEntry(hdr.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		entries++
		if entries > maxReleaseEntries {
			return fmt.Errorf("release archive exceeds %d entries; refusing", maxReleaseEntries)
		}
		target := filepath.Join(root, rel)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode & 0o777)
			if mode == 0 {
				mode = 0o644
			}
			n, err := writeFile(tr, target, mode, maxReleaseBytes-written)
			written += n
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			// A symlink's danger is not its own path — sanitizeEntry already
			// bounded that — but where it points. Left unchecked, an archive can
			// ship "bin -> /usr/local" and then a later, perfectly well-named
			// entry "bin/panda" writes through the link to a path outside root.
			// This is the extraction half of the self-update trust chain, so a
			// bad link fails the whole update instead of being skipped.
			if err := safeLinkTarget(root, rel, hdr.Linkname); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			// A hard link names an inode, not a path: it can alias a file
			// outside root that no path check can see.
			return fmt.Errorf("release archive contains a hard link (%s -> %s); refusing", rel, hdr.Linkname)
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			// Devices and FIFOs have no place in a release archive.
			return fmt.Errorf("release archive entry %s has unsupported tar type %d; refusing", rel, hdr.Typeflag)
		default:
			// Everything else is pax/GNU metadata (global and per-file extended
			// headers, long-name entries), which the reader has already folded
			// into the headers it hands back. Ignoring them keeps ordinary
			// GNU-tar archives extractable.
			continue
		}
	}
	return nil
}

func unzip(archive, root string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	var entries, written int64
	for _, f := range zr.File {
		rel, err := sanitizeEntry(f.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		entries++
		if entries > maxReleaseEntries {
			return fmt.Errorf("release archive exceeds %d entries; refusing", maxReleaseEntries)
		}
		target := filepath.Join(root, rel)
		fi := f.FileInfo()
		if fi.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			// Zip carries Unix type bits in external attrs: symlinks, devices
			// and FIFOs get the same verdict their tar forms get — a release
			// installs regular files only.
			return fmt.Errorf("release archive entry %s has unsupported file type %s; refusing", rel, fi.Mode().Type())
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		n, werr := writeFile(rc, target, mode, maxReleaseBytes-written)
		rc.Close()
		written += n
		if werr != nil {
			return werr
		}
	}
	return nil
}

// writeFile streams r into path with mode, via a temp file + atomic rename so
// a partially-extracted file never appears at the final path. It writes at
// most budget bytes and returns the count so callers can enforce a whole-
// archive ceiling; exceeding the budget is an error.
func writeFile(r io.Reader, path string, mode os.FileMode, budget int64) (int64, error) {
	tmp := path + ".extract"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(r, budget+1))
	if err == nil && n > budget {
		err = fmt.Errorf("release archive exceeds %d decompressed bytes; refusing", maxReleaseBytes)
	}
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return n, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return n, err
	}
	return n, os.Rename(tmp, path)
}
