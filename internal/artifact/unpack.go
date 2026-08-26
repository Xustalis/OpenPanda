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
//   - the decompressed total is capped at MaxBytes (a gzip bomb is a few KiB
//     that expands to terabytes).
func Unpack(r io.Reader, dst string) (Manifest, error) {
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
	// Cap the compressed side too, so a stream that never ends cannot be read
	// forever; the decompressed cap below is the one a bomb hits first.
	src := io.TeeReader(io.LimitReader(r, MaxBytes+1), io.MultiWriter(h, counter))

	gz, err := gzip.NewReader(src)
	if err != nil {
		return Manifest{}, fmt.Errorf("artifact: gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var meta []EntryMeta
	var written int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("artifact: read tar: %w", err)
		}
		if len(meta) > maxEntries {
			return Manifest{}, fmt.Errorf("%w: more than %d entries", ErrTooLarge, maxEntries)
		}
		rel, err := safeRel(hdr.Name)
		if err != nil {
			return Manifest{}, err
		}
		target := filepath.Join(root, rel)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, dirMode(hdr)); err != nil {
				return Manifest{}, fmt.Errorf("artifact: mkdir %s: %w", rel, err)
			}
		case tar.TypeReg:
			written += hdr.Size
			if written > MaxBytes {
				return Manifest{}, fmt.Errorf("%w: decompressed content exceeds %d bytes", ErrTooLarge, MaxBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return Manifest{}, fmt.Errorf("artifact: mkdir %s: %w", filepath.Dir(rel), err)
			}
			mode := fileMode(hdr)
			if err := writeRegular(tr, target, mode, hdr.Size); err != nil {
				return Manifest{}, fmt.Errorf("artifact: write %s: %w", rel, err)
			}
			meta = append(meta, EntryMeta{Path: filepath.ToSlash(rel), Mode: mode, Size: hdr.Size})
		case tar.TypeSymlink:
			if err := checkLinkTarget(root, rel, hdr.Linkname); err != nil {
				return Manifest{}, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return Manifest{}, fmt.Errorf("artifact: mkdir %s: %w", filepath.Dir(rel), err)
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
	if counter.n > MaxBytes {
		return Manifest{}, fmt.Errorf("%w: archive is larger than %d bytes", ErrTooLarge, MaxBytes)
	}
	return Manifest{Hash: hex.EncodeToString(h.Sum(nil)), Size: counter.n, Entries: meta}, nil
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

// writeRegular streams exactly size bytes into path through a temp file and an
// atomic rename, so a partially written file never appears at the final name —
// and never at a name a concurrent reader might pick up as complete.
func writeRegular(r io.Reader, path string, mode os.FileMode, size int64) error {
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, size))
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if n != size {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("truncated entry: %d of %d bytes", n, size)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
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
