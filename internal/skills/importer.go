// SPDX-License-Identifier: AGPL-3.0-or-later

package skills

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// maxDownloadBytes limits downloaded archives or files to prevent DoS (10 MiB).
	maxDownloadBytes = 10 * 1024 * 1024
	// defaultHTTPTimeout is the deadline for remote skill fetching.
	defaultHTTPTimeout = 30 * time.Second
)

// ImportOptions customizes how skills are imported.
type ImportOptions struct {
	Scope   Scope  // overrides scope in frontmatter if non-empty
	Project string // required if Scope == ScopeProject
	Device  string // required if Scope == ScopeDevice
	Name    string // overrides skill name if non-empty (only for single skill)
	Status  Status // defaults to StatusActive if empty
	Force   bool   // overwrite existing skill if true
}

// ImportBytes imports a standalone skill. Bundle imports also preserve resources.
func (s *Store) ImportBytes(data []byte, opts ImportOptions) (*Skill, error) {
	if len(data) > maxDownloadBytes {
		return nil, fmt.Errorf("skills: input exceeds size limit")
	}
	sk, err := prepareImport(data, opts)
	if err != nil {
		return nil, err
	}
	if !opts.Force {
		existing, err := s.Load(sk.Scope, sk.keyForScope(), sk.Name)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return nil, fmt.Errorf("skills: skill %q already exists (use --force to overwrite)", sk.Name)
		}
	}
	encoded, err := sk.Bytes()
	if err != nil {
		return nil, err
	}
	// Import replaces the complete skill. Unlike a metadata-only Save, a
	// standalone import must also remove resources from the previous bundle.
	if err := s.saveBundle(sk, bundleFiles{"SKILL.md": encoded}); err != nil {
		return nil, err
	}
	return sk, nil
}

func prepareImport(data []byte, opts ImportOptions) (*Skill, error) {
	sk, err := ParseSkill(data)
	if err != nil {
		return nil, err
	}
	if opts.Name != "" {
		sk.Name = opts.Name
	}
	if opts.Scope != "" {
		sk.Scope = opts.Scope
	}
	if opts.Project != "" {
		sk.Project = opts.Project
	}
	if opts.Device != "" {
		sk.Device = opts.Device
	}
	sk.Status = opts.Status
	if sk.Status == "" {
		sk.Status = StatusActive
	}
	if sk.Scope == ScopeProject && sk.Project == "" {
		return nil, fmt.Errorf("skills: project scope requires project name")
	}
	if sk.Scope == ScopeDevice && sk.Device == "" {
		return nil, fmt.Errorf("skills: device scope requires device name")
	}
	if sk.Scope != ScopeGlobal && sk.Scope != ScopeProject && sk.Scope != ScopeDevice {
		return nil, fmt.Errorf("skills: unknown scope %q", sk.Scope)
	}
	if _, err := scopeDir(sk); err != nil {
		return nil, err
	}
	return sk, nil
}

// ImportFile imports one or more skills from a local file, directory, or archive (.zip, .tar.gz).
func (s *Store) ImportFile(path string, opts ImportOptions) ([]*Skill, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("skills: stat %s: %w", path, err)
	}

	if !fi.IsDir() && fi.Size() > maxDownloadBytes {
		return nil, fmt.Errorf("skills: input exceeds size limit")
	}
	if fi.IsDir() {
		return s.importFromDir(path, opts)
	}

	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("skills: read zip %s: %w", path, err)
		}
		return s.importFromZip(bytes.NewReader(data), int64(len(data)), opts)
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("skills: open tar.gz %s: %w", path, err)
		}
		defer f.Close()
		return s.importFromTarGz(f, opts)
	default:
		// Regular markdown file
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("skills: read %s: %w", path, err)
		}
		sk, err := s.ImportBytes(data, opts)
		if err != nil {
			return nil, err
		}
		return []*Skill{sk}, nil
	}
}

// Bundle inputs are bounded in aggregate before writing anything to the store.
const maxBundleFiles = 1024

type bundleFiles map[string][]byte

func (b bundleFiles) add(name string, r io.Reader) error {
	if !validResourcePath(name) {
		return fmt.Errorf("skills: unsafe archive path %q", name)
	}
	if _, ok := b[name]; ok {
		return fmt.Errorf("skills: duplicate archive path %q", name)
	}
	if len(b) >= maxBundleFiles {
		return fmt.Errorf("skills: too many bundle files")
	}
	total := 0
	for _, data := range b {
		total += len(data)
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(maxDownloadBytes-total+1)))
	if err != nil {
		return err
	}
	if total+len(data) > maxDownloadBytes {
		return fmt.Errorf("skills: bundle exceeds size limit")
	}
	b[name] = data
	return nil
}

func (s *Store) importFromDir(dir string, opts ImportOptions) ([]*Skill, error) {
	files := bundleFiles{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("skills: bundle contains non-regular file %s", p)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		return files.add(filepath.ToSlash(rel), f)
	})
	if err != nil {
		return nil, err
	}
	return s.importBundles(files, opts)
}

func (s *Store) importFromZip(r io.ReaderAt, size int64, opts ImportOptions) ([]*Skill, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	files := bundleFiles{}
	for _, f := range zr.File {
		name := archiveResourceName(f.Name)
		if name == "" && f.FileInfo().IsDir() {
			continue
		}
		if !validResourcePath(name) {
			return nil, fmt.Errorf("skills: unsafe archive path %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, fmt.Errorf("skills: non-regular zip entry %q", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		err = files.add(name, rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
	}
	return s.importBundles(files, opts)
}

func (s *Store) importFromTarGz(r io.Reader, opts ImportOptions) ([]*Skill, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := bundleFiles{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := archiveResourceName(h.Name)
		if name == "" && h.Typeflag == tar.TypeDir {
			continue
		}
		if !validResourcePath(name) {
			return nil, fmt.Errorf("skills: unsafe archive path %q", h.Name)
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("skills: non-regular tar entry %q", h.Name)
		}
		if err := files.add(name, tr); err != nil {
			return nil, err
		}
	}
	return s.importBundles(files, opts)
}

func (s *Store) importBundles(files bundleFiles, opts ImportOptions) ([]*Skill, error) {
	// Canonical SKILL.md roots own descendants. Markdown references with
	// frontmatter are resources, not additional installed skills.
	names := make([]string, 0)
	roots := make([]string, 0)
	for name := range files {
		if path.Base(name) == "SKILL.md" {
			roots = append(roots, path.Dir(name))
			names = append(names, name)
		}
	}
	for name, data := range files {
		if path.Base(name) == "SKILL.md" || !strings.HasSuffix(name, ".md") {
			continue
		}
		owned := false
		for _, root := range roots {
			if root == "." || strings.HasPrefix(name, root+"/") {
				owned = true
				break
			}
		}
		if !owned {
			if _, err := ParseSkill(data); err == nil {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("skills: no valid skills found in bundle")
	}
	if opts.Name != "" && len(names) != 1 {
		return nil, fmt.Errorf("skills: --name requires a single skill")
	}
	prepared := make([]*Skill, 0, len(names))
	destinations := map[string]bool{}
	for _, name := range names {
		sk, err := prepareImport(files[name], opts)
		if err != nil {
			return nil, err
		}
		dest, err := s.Path(sk)
		if err != nil {
			return nil, err
		}
		if destinations[dest] {
			return nil, fmt.Errorf("skills: duplicate destination %s", sk.Name)
		}
		destinations[dest] = true
		if !opts.Force {
			if _, err := os.Stat(dest); err == nil {
				return nil, fmt.Errorf("skills: skill %q already exists (use --force to overwrite)", sk.Name)
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
		prepared = append(prepared, sk)
	}
	for i, sk := range prepared {
		resources := bundleFiles{}
		root := path.Dir(names[i])
		if path.Base(names[i]) == "SKILL.md" {
			for name, data := range files {
				if root != "." && !strings.HasPrefix(name, root+"/") {
					continue
				}
				nested := false
				for _, other := range roots {
					if other != root && (root == "." || strings.HasPrefix(other, root+"/")) && strings.HasPrefix(name, other+"/") {
						nested = true
						break
					}
				}
				if nested {
					continue
				}
				rel := name
				if root != "." {
					rel = strings.TrimPrefix(name, root+"/")
				}
				resources[rel] = data
			}
		}
		data, err := sk.Bytes()
		if err != nil {
			return nil, err
		}
		resources["SKILL.md"] = data
		if err := s.saveBundle(sk, resources); err != nil {
			return nil, err
		}
	}
	return prepared, nil
}

// Stage a complete skill before replacing its directory, so a failed copy
// never leaves a procedure referring to half-imported scripts or references.
func (s *Store) saveBundle(sk *Skill, files bundleFiles) error {
	if s.root == "" {
		return fmt.Errorf("skills: empty store root")
	}
	total := 0
	for _, data := range files {
		total += len(data)
		if total > maxDownloadBytes {
			return fmt.Errorf("skills: serialized bundle exceeds size limit")
		}
	}
	dest, err := s.Path(sk)
	if err != nil {
		return err
	}
	dest = filepath.Dir(dest)
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".skill-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for name, data := range files {
		p := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if strings.HasPrefix(name, "scripts/") {
			mode = 0o700
		}
		if err := os.WriteFile(p, data, mode); err != nil {
			return err
		}
	}
	s.useMu.Lock()
	defer s.useMu.Unlock()
	backup := stage + "-old"
	if err := os.Rename(dest, backup); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(stage, dest); err != nil {
		_ = os.Rename(backup, dest)
		return err
	}
	return os.RemoveAll(backup)
}

// NormalizeURL converts common repository URLs (like GitHub blob URLs) to direct raw content URLs.
func NormalizeURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	// GitHub blob URL -> raw content URL
	// e.g. https://github.com/owner/repo/blob/branch/path/to/SKILL.md
	//   -> https://raw.githubusercontent.com/owner/repo/branch/path/to/SKILL.md
	if parsed.Host == "github.com" || parsed.Host == "www.github.com" {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) >= 4 && parts[2] == "blob" {
			owner := parts[0]
			repo := parts[1]
			branch := parts[3]
			rest := strings.Join(parts[4:], "/")
			return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, branch, rest)
		}
	}

	return rawURL
}

// ImportURL downloads a skill or archive from a URL and imports it.
//
// The URL is user- and agent-supplied, so the fetch is guarded (fetchguard.go):
// non-loopback hosts require https, and the dialer refuses to connect to
// loopback/private/link-local/reserved addresses — including on redirects —
// so the function cannot be used to probe the LAN or a cloud metadata
// endpoint from inside the node.
func (s *Store) ImportURL(ctx context.Context, rawURL string, opts ImportOptions) ([]*Skill, error) {
	fetchURL := NormalizeURL(rawURL)

	u, policy, err := classifyFetchURL(fetchURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("skills: create request: %w", err)
	}
	req.Header.Set("User-Agent", "OpenPanda-Skill-Importer/1.0")

	resp, err := fetchClient(policy).Do(req)
	if err != nil {
		return nil, fmt.Errorf("skills: download from %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skills: download from %s: HTTP status %d", rawURL, resp.StatusCode)
	}

	// Read body with limit
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("skills: read download body: %w", err)
	}

	if len(body) > maxDownloadBytes {
		return nil, fmt.Errorf("skills: download exceeds size limit")
	}
	contentType := resp.Header.Get("Content-Type")
	lowerURL := strings.ToLower(fetchURL)

	// Check if body is a zip archive
	if strings.Contains(contentType, "application/zip") || strings.HasSuffix(lowerURL, ".zip") || isZip(body) {
		return s.importFromZip(bytes.NewReader(body), int64(len(body)), opts)
	}

	// Check if body is tar.gz
	if strings.Contains(contentType, "gzip") || strings.HasSuffix(lowerURL, ".tar.gz") || strings.HasSuffix(lowerURL, ".tgz") {
		return s.importFromTarGz(bytes.NewReader(body), opts)
	}

	// Try as markdown skill
	sk, err := s.ImportBytes(body, opts)
	if err != nil {
		return nil, fmt.Errorf("skills: failed to import from %s: %w", rawURL, err)
	}
	return []*Skill{sk}, nil
}

// ImportSource imports from either a URL or a local file/directory path.
func (s *Store) ImportSource(ctx context.Context, source string, opts ImportOptions) ([]*Skill, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, fmt.Errorf("skills: import source cannot be empty")
	}

	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return s.ImportURL(ctx, source, opts)
	}

	return s.ImportFile(source, opts)
}

// isZip checks for the PK\x03\x04 zip header signature.
func isZip(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 3 && data[3] == 4
}

func archiveResourceName(name string) string {
	for strings.HasPrefix(name, "./") {
		name = strings.TrimPrefix(name, "./")
	}
	return strings.TrimSuffix(name, "/")
}
