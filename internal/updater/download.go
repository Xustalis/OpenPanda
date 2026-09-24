package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// downloadRelease fetches the release asset for version and its checksums.txt,
// verifies the asset's SHA-256, and returns the archive path inside destDir.
// destDir must already exist. A missing checksums entry or a hash mismatch is
// an error: a silent corrupt archive is worse than a failed update.
func downloadRelease(ctx context.Context, repo, version, destDir string) (string, error) {
	base := "https://github.com/" + repo + "/releases/download/v" + version
	name := AssetName(version)

	checksumsURL := base + "/checksums.txt"
	sums, err := fetchText(ctx, checksumsURL)
	if err != nil {
		return "", fmt.Errorf("fetch checksums: %w", err)
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

// fetchText GETs url and returns its body as a string.
func fetchText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "OpenPanda-updater")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// downloadFile fetches url into destDir (reusing the URL's base name) and
// returns the written path. The download goes through a ".part" temp file so a
// half-downloaded archive never sits at the final name.
func downloadFile(ctx context.Context, url, destDir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "OpenPanda-updater")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	if directoryTraversal(filepath.Base(url)) {
		return "", fmt.Errorf("refusing to download from unsafe path %q", url)
	}
	dst := filepath.Join(destDir, filepath.Base(url))
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
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
// only the top-level openpanda dir entry itself or a bare "./" earns that.
// Anything else outside the release layout is an error: absolute paths,
// ".." traversal, and foreign top-level entries would install a partial or
// unexpected tree, so they refuse the archive outright rather than being
// skipped (the same contract artifact.Unpack enforces).
func sanitizeEntry(name string) (string, error) {
	raw := filepath.ToSlash(strings.TrimPrefix(name, "./"))
	if raw == "" || raw == "openpanda" {
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
