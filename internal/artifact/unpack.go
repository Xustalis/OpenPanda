// SPDX-License-Identifier: AGPL-3.0-or-later

package artifact

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Unpack materializes the tar.gz in r under dst and returns the manifest of
// what it wrote, with Hash set to the SHA-256 of the stream it consumed (so the
// caller can compare it against the hash it expected without a second pass).
// dst is created if missing.
//
// Every rejection here is a rejection of the *whole archive*, not a skipped
// entry. A tar that tries to escape its destination is not a tar with one bad
// file in it — it is an attack, and honouring the other 99 entries would leave
// the caller with a half-written tree it believes is complete. The rules:
//
//   - no absolute paths and no ".." components, after cleaning;
//   - symlinks are allowed only when the link target resolves back inside dst.
//     Path-only checks are not enough: "link -> /etc" is a relative-looking name
//     with an absolute target, and a later entry writing through it lands
//     outside dst;
//   - hard links are rejected outright — their target is an inode, so a hard
//     link to a file outside dst hands over write access to it;
//   - devices, FIFOs and sockets are rejected: an artifact is content;
//   - the decompressed total is bounded: by the configured limit when one is
//     set, else by the destination filesystem's free space (a gzip bomb is a
//     few KiB that expands to terabytes, and "unlimited" must still stop
//     before the disk does).
func Unpack(r io.Reader, dst string) (Manifest, error) {
	return unpack(r, dst, 0, 0, nil)
}

// unpack is Unpack with an explicit byte limit, a free-space watermark, and
// an optional skip set: a positive limit rejects archives whose
// compressed or decompressed size exceeds it; 0 instead refuses entries that
// would push the destination's free space below minFree — the honest bound
// when no fixed cap is configured. Entries at or below a key in skip — keys
// match a leading path prefix, so ".git" covers the dir and
// ".github/workflows" the subtree — are not written and not listed in
// Entries (counted in Manifest.Skipped, named in Manifest.SkippedPaths);
// paths under non-matching leading elements are inert content, not the
// checkout's own plumbing, and unpack normally.
func unpack(r io.Reader, dst string, limit, minFree int64, skip map[string]bool) (Manifest, error) {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("artifact: create dst: %w", err)
	}
	root, err := filepath.Abs(dst)
	if err != nil {
		return Manifest{}, fmt.Errorf("artifact: resolve dst: %w", err)
	}
	// EvalSymlinks so the containment check compares real paths: if dst itself
	// is reached through a symlink, an entry inside it is not an escape.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	h := sha256.New()
	counter := &countingWriter{w: io.Discard}
	// Cap the compressed side when a limit is configured, so a stream that
	// never ends cannot be read forever; the decompressed bound below is the
	// one a bomb hits first either way.
	var clipped io.Reader = r
	if limit > 0 {
		clipped = io.LimitReader(r, limit+1)
	}
	src := io.TeeReader(clipped, io.MultiWriter(h, counter))

	gz, err := gzip.NewReader(src)
	if err != nil {
		return Manifest{}, fmt.Errorf("artifact: gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var meta []EntryMeta
	var written int64
	var skipped int
	var skippedPaths []string
	var entries int
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("artifact: read tar: %w", err)
		}
		// The cap is on every entry, not just the regular files meta
		// records: an archive of empty directories compresses to almost
		// nothing and would otherwise run an unbounded MkdirAll/Symlink
		// sweep — an inode flood that no byte limit ever sees.
		entries++
		if entries > maxEntries {
			return Manifest{}, fmt.Errorf("%w: more than %d entries", ErrTooLarge, maxEntries)
		}
		rel, err := safeRel(hdr.Name)
		if err != nil {
			return Manifest{}, err
		}
		if skippedEntry(rel, skip) {
			skipped++
			if len(skippedPaths) < maxSkippedPaths {
				skippedPaths = append(skippedPaths, filepath.ToSlash(rel))
			}
			continue
		}
		target := filepath.Join(root, rel)
		if err := ancestorsInside(root, target); err != nil {
			return Manifest{}, err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, dirMode(hdr)); err != nil {
				return Manifest{}, fmt.Errorf("artifact: mkdir %s: %w", rel, err)
			}
		case tar.TypeReg:
			written += hdr.Size
			if limit > 0 && written > limit {
				return Manifest{}, fmt.Errorf("%w: decompressed content exceeds %d bytes", ErrTooLarge, limit)
			}
			if limit <= 0 {
				// Unbounded mode's bound is the volume itself: refuse the
				// entry that cannot fit inside the watermark rather than
				// writing until ENOSPC — a filesystem driven to zero takes
				// SQLite's WAL down with it, which is a much worse failure
				// than a rejected artifact.
				if avail, ok := FreeBytes(root); ok && hdr.Size > avail-minFree {
					return Manifest{}, fmt.Errorf("%w: entry %s declares %d bytes with %d free (min %d)", ErrNoSpace, rel, hdr.Size, avail, minFree)
				}
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return Manifest{}, fmt.Errorf("artifact: mkdir %s: %w", filepath.Dir(rel), err)
			}
			landed, err := writeRegular(tr, target, fileMode(hdr), hdr.Size)
			if err != nil {
				return Manifest{}, fmt.Errorf("artifact: write %s: %w", rel, err)
			}
			meta = append(meta, EntryMeta{Path: filepath.ToSlash(rel), Mode: landed, Size: hdr.Size})
		case tar.TypeSymlink:
			if err := checkLinkTarget(root, rel, hdr.Linkname); err != nil {
				return Manifest{}, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return Manifest{}, fmt.Errorf("artifact: mkdir %s: %w", filepath.Dir(rel), err)
			}
			// Same read-only trap as writeRegular: Windows refuses to remove
			// a read-only file (the error is deliberately ignored here), and
			// the following Symlink then dies "file exists" on exactly the
			// re-extracted git trees this path serves. Clear the attribute
			// first so the replace can land on every platform. The guard is
			// "not a symlink": os.Chmod follows links, so chmodding a link
			// that happens to sit at target would retag whatever it points
			// at — the link itself needs no permission to be removed.
			if fi, lerr := os.Lstat(target); lerr == nil && fi.Mode()&os.ModeSymlink == 0 {
				_ = os.Chmod(target, 0o666)
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return Manifest{}, fmt.Errorf("artifact: symlink %s: %w", rel, err)
			}
		case tar.TypeLink:
			return Manifest{}, fmt.Errorf("artifact: refusing hard link %q -> %q: its target is an inode, which can lie outside the destination", rel, hdr.Linkname)
		default:
			return Manifest{}, fmt.Errorf("artifact: refusing entry %q of tar type %d: an artifact holds files and directories only", rel, hdr.Typeflag)
		}
	}

	// Drain whatever follows the gzip member so the hash covers the full stream
	// the sender sent, matching what Pack hashed.
	if _, err := io.Copy(io.Discard, src); err != nil {
		return Manifest{}, fmt.Errorf("artifact: drain stream: %w", err)
	}
	if limit > 0 && counter.n > limit {
		return Manifest{}, fmt.Errorf("%w: archive is larger than %d bytes", ErrTooLarge, limit)
	}
	return Manifest{Hash: hex.EncodeToString(h.Sum(nil)), Size: counter.n,
		Entries: meta, Skipped: skipped, SkippedPaths: skippedPaths}, nil
}

// maxSkippedPaths bounds how many declined paths a manifest carries: enough to
// show the operator exactly which protected entries were withheld, without
// letting a padded archive grow the manifest unboundedly.
const maxSkippedPaths = 64

// skippedEntry reports whether rel lands inside a protected path. A key
// matches the entry itself or any descent below it, so whole directories
// (".vscode", ".git") and single files ("Jenkinsfile", or a subtree like
// ".github/workflows") all work as keys — and a key only ever matches at the
// head of the path: a vendored checkout's nested "vendor/x/.git" remains
// inert content, not the destination's own plumbing.
func skippedEntry(rel string, skip map[string]bool) bool {
	if len(skip) == 0 {
		return false
	}
	sep := string(filepath.Separator)
	for k := range skip {
		k = filepath.FromSlash(k)
		if rel == k || strings.HasPrefix(rel, k+sep) {
			return true
		}
	}
	return false
}

// safeRel validates a tar entry name and returns the relative path to write.
// It rejects rather than sanitizes: an entry named "../../etc/passwd" has no
// safe interpretation, and quietly rewriting it to "etc/passwd" would write a
// file the archive never described.
func safeRel(name string) (string, error) {
	slash := filepath.ToSlash(name)
	if slash == "" {
		return "", fmt.Errorf("artifact: refusing entry with empty name")
	}
	if strings.HasPrefix(slash, "/") || filepath.IsAbs(name) {
		return "", fmt.Errorf("artifact: refusing absolute path %q", name)
	}
	// Reject the drive-letter and UNC forms too: on a non-Windows host
	// filepath.IsAbs says no, but the archive may be unpacked on Windows later.
	if len(slash) >= 2 && slash[1] == ':' {
		return "", fmt.Errorf("artifact: refusing absolute path %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSuffix(slash, "/")))
	if clean == "." || clean == string(filepath.Separator) {
		return "", fmt.Errorf("artifact: refusing entry %q", name)
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact: refusing path traversal %q", name)
	}
	return clean, nil
}

// checkLinkTarget is the check the audit flagged as missing in updater's
// untargz (P3-12): a symlink's *name* being safe says nothing about where it
// points. The target is resolved relative to the link's own directory and must
// land inside root.
func checkLinkTarget(root, rel, linkname string) error {
	if linkname == "" {
		return fmt.Errorf("artifact: refusing symlink %q with empty target", rel)
	}
	if filepath.IsAbs(linkname) || strings.HasPrefix(filepath.ToSlash(linkname), "/") {
		return fmt.Errorf("artifact: refusing symlink %q -> %q: absolute target escapes the destination", rel, linkname)
	}
	// Resolve as the filesystem would: relative to the directory holding the link.
	resolved := filepath.Clean(filepath.Join(root, filepath.Dir(rel), filepath.FromSlash(linkname)))
	if !contains(root, resolved) {
		return fmt.Errorf("artifact: refusing symlink %q -> %q: target resolves outside the destination", rel, linkname)
	}
	return nil
}

// contains reports whether path is root itself or lies beneath it. The
// separator suffix stops "/tmp/a" from being read as inside "/tmp/ab".
func contains(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// ancestorsInside verifies that path's parent chain stays under root in the
// filesystem, not just textually: safeRel vets the entry's own name and
// checkLinkTarget the archive's own links, but dst is often a pre-existing
// tree (the project checkout an adoption writes over) whose already-present
// symlinked directories MkdirAll and OpenFile follow without comment — an
// entry below one of those writes straight outside the destination. The
// probe resolves the deepest existing ancestor for real and re-appends the
// not-yet-created tail textually, so the verdict reflects what the entry
// will hit, not the layout the archive claimed.
func ancestorsInside(root, path string) error {
	probe := filepath.Dir(path)
	var tail []string
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("artifact: stat %s: %w", probe, err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return fmt.Errorf("artifact: %q has no existing ancestor under the destination", path)
		}
		tail = append([]string{filepath.Base(probe)}, tail...)
		probe = parent
	}
	resolved, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return fmt.Errorf("artifact: resolve %s: %w", probe, err)
	}
	for _, seg := range tail {
		resolved = filepath.Join(resolved, seg)
	}
	if !contains(root, resolved) {
		return fmt.Errorf("artifact: refusing %q: lands at %q outside the destination through a pre-existing link", path, resolved)
	}
	return nil
}

// writeRegular streams exactly size bytes into path through a temp file and an
// atomic rename, so a partially written file never appears at the final name —
// and never at a name a concurrent reader might pick up as complete. It
// returns the mode the file landed with — the archive's mode merged with the
// exec-bit rule below and masked by the process umask — so a manifest entry
// describes the file a verifier will stat, not the request that produced it.
func writeRegular(r io.Reader, path string, mode os.FileMode, size int64) (os.FileMode, error) {
	// A packer that cannot express POSIX exec bits reports every file without
	// them (Windows fi.Mode().Perm() is 0666 on a regular file), so an archive
	// mode carrying no exec bits is "unknown", not "strip them": an existing
	// executable keeps its bits or every project-tree adoption from such a
	// peer silently de-executabilizes the checkout's scripts. The trade-off —
	// a POSIX packer's deliberate chmod -x then fails to propagate — is the
	// better error: a stale exec bit is harmless next to scripts that stop
	// running. New files get the archive mode verbatim. The probe is Lstat:
	// only a regular file at the very path carries its own bits over, not
	// whatever a symlink there happens to point at.
	if mode&0o111 == 0 {
		if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
			mode |= fi.Mode().Perm() & 0o111
		}
	}
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return mode, err
	}
	// What actually landed can differ from the request (umask; a stale
	// .partial from a crashed run keeps its own mode under O_TRUNC).
	landed := mode
	if fi, err := f.Stat(); err == nil {
		landed = fi.Mode().Perm()
	}
	n, err := io.Copy(f, io.LimitReader(r, size))
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return landed, err
	}
	if n != size {
		f.Close()
		os.Remove(tmp)
		return landed, fmt.Errorf("truncated entry: %d of %d bytes", n, size)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return landed, err
	}
	// A destination that already exists may be read-only — git marks every
	// object file read-only, and a re-extraction of the same tree lands
	// exactly on those — and Windows refuses the overwrite rename with
	// ACCESS_DENIED in that case. Clearing the attribute first keeps the
	// atomic-rename contract on every platform: the chmod touches only the
	// doomed old inode, the rename still installs tmp's mode and contents.
	// Lstat, and links are skipped: os.Chmod follows a symlink to its target,
	// so a pre-existing link at path would retag an unrelated file while the
	// rename that replaces the link needs no permission from it.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		_ = os.Chmod(path, landed|0o200)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return landed, err
	}
	return landed, nil
}

// fileMode returns a sane permission set for a file entry: an archive may
// carry mode 0, and a 0-perm file is unreadable even by its owner.
func fileMode(hdr *tar.Header) os.FileMode {
	mode := os.FileMode(hdr.Mode).Perm()
	if mode == 0 {
		mode = 0o644
	}
	return mode
}

// dirMode is fileMode for directories, which additionally need the execute bit
// to be traversable at all.
func dirMode(hdr *tar.Header) os.FileMode {
	mode := os.FileMode(hdr.Mode).Perm()
	if mode&0o100 == 0 {
		mode = 0o755
	}
	return mode
}

// Hash returns the hex SHA-256 of the file at path — the artifact's name in
// storage, and the value a receiver compares against after a chunked transfer.
func Hash(path string) (string, error) {
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
