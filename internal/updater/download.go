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

// checksumFor parses checksums.txt ("<hash>  <name>", one per line) and
// returns the lowercase hex hash for name, or "" if the file has no entry.
func checksumFor(data, name string) string {
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == name {
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
// bin/panda(.exe) and adapters/*.py. Extraction strips that top component and
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

// sanitizeEntry strips the leading "openpanda/" component, rejects absolute
// paths and ".." traversal (returns ok=false), and returns the relative path
// to materialize inside root.
func sanitizeEntry(name string) (string, bool) {
	name = strings.TrimPrefix(name, "./")
	name = filepath.ToSlash(name)
	if name == "" || name == "openpanda" || strings.HasPrefix(name, "/") {
		return "", false
	}
	name = strings.TrimPrefix(name, "openpanda/")
	if name == "" {
		return "", false // the openpanda/ dir entry itself
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	return clean, true
}

func directoryTraversal(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return true
	}
	return false
}

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
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rel, ok := sanitizeEntry(hdr.Name)
		if !ok {
			continue
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
			if err := writeFile(tr, target, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			_ = os.Remove(target)
			_ = os.Symlink(hdr.Linkname, target)
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
	for _, f := range zr.File {
		rel, ok := sanitizeEntry(f.Name)
		if !ok {
			continue
		}
		target := filepath.Join(root, rel)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
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
		err = writeFile(rc, target, mode)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// writeFile streams r into path with mode, via a temp file + atomic rename so
// a partially-extracted file never appears at the final path.
func writeFile(r io.Reader, path string, mode os.FileMode) error {
	tmp := path + ".extract"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
