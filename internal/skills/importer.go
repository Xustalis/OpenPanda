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
	"path/filepath"
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

// ImportBytes imports a skill from raw markdown bytes (YAML frontmatter + body).
func (s *Store) ImportBytes(data []byte, opts ImportOptions) (*Skill, error) {
	sk, err := ParseSkill(data)
	if err != nil {
		return nil, fmt.Errorf("skills: parse skill data: %w", err)
	}

	if opts.Name != "" {
		if err := validateName(opts.Name); err != nil {
			return nil, err
		}
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
	if opts.Status != "" {
		sk.Status = opts.Status
	} else {
		sk.Status = StatusActive // user-initiated import defaults to active
	}

	// Validate scope key
	key := sk.keyForScope()
	if sk.Scope == ScopeProject && sk.Project == "" {
		return nil, fmt.Errorf("skills: project scope requires project name")
	}
	if sk.Scope == ScopeDevice && sk.Device == "" {
		return nil, fmt.Errorf("skills: device scope requires device name")
	}

	// Check if already exists unless Force is set
	if !opts.Force {
		existing, err := s.Load(sk.Scope, key, sk.Name)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return nil, fmt.Errorf("skills: skill %q already exists (use --force to overwrite)", sk.Name)
		}
	}

	if err := s.Save(sk); err != nil {
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

// importFromDir recursively walks dir looking for SKILL.md or files with valid frontmatter.
func (s *Store) importFromDir(dir string, opts ImportOptions) ([]*Skill, error) {
	var imported []*Skill
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if name != "SKILL.md" && !strings.HasSuffix(name, ".md") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if _, err := ParseSkill(data); err != nil {
			// Skip files that aren't valid skills
			return nil
		}
		// If multiple skills in directory, do not override Name with opts.Name
		subOpts := opts
		if len(imported) > 0 || name == "SKILL.md" {
			subOpts.Name = ""
		}
		res, err := s.ImportBytes(data, subOpts)
		if err != nil {
			// If already exists and not forced, return error
			if !opts.Force && strings.Contains(err.Error(), "already exists") {
				return err
			}
			return nil
		}
		imported = append(imported, res)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(imported) == 0 {
		return nil, fmt.Errorf("skills: no valid skills found in directory %s", dir)
	}
	return imported, nil
}

// importFromZip extracts skills from a zip archive.
func (s *Store) importFromZip(r io.ReaderAt, size int64, opts ImportOptions) ([]*Skill, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("skills: open zip: %w", err)
	}

	var imported []*Skill
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// ZipSlip defense
		clean := filepath.Clean(f.Name)
		if strings.HasPrefix(clean, "..") || strings.Contains(clean, ":") {
			continue
		}
		base := filepath.Base(clean)
		if base != "SKILL.md" && !strings.HasSuffix(base, ".md") {
			continue
		}
		if f.UncompressedSize64 > maxDownloadBytes {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxDownloadBytes))
		rc.Close()
		if err != nil {
			continue
		}

		if _, err := ParseSkill(data); err != nil {
			continue
		}

		subOpts := opts
		if len(imported) > 0 {
			subOpts.Name = ""
		}
		res, err := s.ImportBytes(data, subOpts)
		if err != nil {
			if !opts.Force && strings.Contains(err.Error(), "already exists") {
				return nil, err
			}
			continue
		}
		imported = append(imported, res)
	}

	if len(imported) == 0 {
		return nil, fmt.Errorf("skills: no valid skills found in zip archive")
	}
	return imported, nil
}

// importFromTarGz extracts skills from a tar.gz reader.
func (s *Store) importFromTarGz(r io.Reader, opts ImportOptions) ([]*Skill, error) {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("skills: open gzip: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	var imported []*Skill

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("skills: read tar: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}

		clean := filepath.Clean(header.Name)
		if strings.HasPrefix(clean, "..") || strings.Contains(clean, ":") {
			continue
		}
		base := filepath.Base(clean)
		if base != "SKILL.md" && !strings.HasSuffix(base, ".md") {
			continue
		}

		data, err := io.ReadAll(io.LimitReader(tr, maxDownloadBytes))
		if err != nil {
			continue
		}

		if _, err := ParseSkill(data); err != nil {
			continue
		}

		subOpts := opts
		if len(imported) > 0 {
			subOpts.Name = ""
		}
		res, err := s.ImportBytes(data, subOpts)
		if err != nil {
			if !opts.Force && strings.Contains(err.Error(), "already exists") {
				return nil, err
			}
			continue
		}
		imported = append(imported, res)
	}

	if len(imported) == 0 {
		return nil, fmt.Errorf("skills: no valid skills found in tar.gz archive")
	}
	return imported, nil
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes))
	if err != nil {
		return nil, fmt.Errorf("skills: read download body: %w", err)
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
