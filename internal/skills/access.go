// SPDX-License-Identifier: AGPL-3.0-or-later

package skills

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var ErrUnavailable = errors.New("no usable skill in this scope")

// Access is shared by entry, agent CLI and MCP readers. All and Inactive are
// reserved for foreground library management, never ambient agent execution.
type Access struct {
	Project  string
	Devices  []string
	All      bool
	Inactive bool
}

func (a Access) Visible(e IndexEntry) bool {
	if !a.Inactive && (e.Status == StatusPending || e.Status == StatusExpired) {
		return false
	}
	if a.All {
		return true
	}
	switch e.Scope {
	case ScopeGlobal:
		return true
	case ScopeProject:
		return a.Project != "" && strings.EqualFold(a.Project, e.Key)
	case ScopeDevice:
		return MatchKey(e.Key, a.Devices)
	}
	return false
}

// Search returns metadata only, bounded even for an empty query.
func (s *Store) Search(a Access, query string, limit int) ([]IndexEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	index, err := s.Index()
	if err != nil {
		return nil, err
	}
	visible := make([]IndexEntry, 0)
	for _, e := range index {
		if a.Visible(e) {
			e.Always = false
			visible = append(visible, e)
		}
	}
	if strings.TrimSpace(query) != "" {
		visible = matchRankedLimit(visible, func(IndexEntry) bool { return true }, query, "", limit)
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if len(visible) > limit {
		visible = visible[:limit]
	}
	return visible, nil
}

type ReadRequest struct {
	Name   string `json:"name"`
	Scope  Scope  `json:"scope,omitempty"`
	Key    string `json:"key,omitempty"`
	Path   string `json:"path,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// ReadResult records delivered content, not merely a suggested match. Offsets
// and limits count bytes; UTF-8 boundaries are preserved for continued reads.
type ReadResult struct {
	Name       string   `json:"name"`
	Scope      Scope    `json:"scope"`
	Key        string   `json:"key,omitempty"`
	Path       string   `json:"path"`
	Root       string   `json:"root"`
	Content    string   `json:"content"`
	SHA256     string   `json:"sha256"`
	TotalBytes int      `json:"total_bytes"`
	Offset     int      `json:"offset"`
	NextOffset int      `json:"next_offset"`
	Truncated  bool     `json:"truncated"`
	Resources  []string `json:"resources,omitempty"`
}

// Read resolves an unambiguous visible skill and reads only a bounded page.
// os.Root prevents resource references (including symlinks) escaping a bundle.
func (s *Store) Read(a Access, req ReadRequest) (*ReadResult, error) {
	if err := validateName(req.Name); err != nil {
		return nil, err
	}
	index, err := s.Index()
	if err != nil {
		return nil, err
	}
	var selected *IndexEntry
	for _, e := range index {
		if !strings.EqualFold(e.Name, req.Name) || !a.Visible(e) {
			continue
		}
		if req.Scope != "" && (req.Scope != e.Scope || !strings.EqualFold(req.Key, e.Key)) {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("skills: ambiguous name %q; specify scope and key", req.Name)
		}
		copy := e
		selected = &copy
	}
	if selected == nil {
		return nil, fmt.Errorf("skills: %w: %q", ErrUnavailable, req.Name)
	}
	resource := req.Path
	if resource == "" {
		resource = "SKILL.md"
	}
	if !validResourcePath(resource) {
		return nil, fmt.Errorf("skills: invalid resource path %q", resource)
	}
	storeRoot, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer storeRoot.Close()
	bundle := filepath.Join(dirFor(selected.Scope, selected.Key), selected.Name)
	root, err := storeRoot.OpenRoot(bundle)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(resource))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxDownloadBytes {
		return nil, fmt.Errorf("skills: resource must be a regular file of at most %d bytes", maxDownloadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownloadBytes {
		return nil, fmt.Errorf("skills: resource exceeds size limit")
	}
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return nil, fmt.Errorf("skills: binary resource; use file at bundle root")
	}
	sum := sha256.Sum256(data)
	hash := fmt.Sprintf("%x", sum)
	if req.SHA256 != "" && !strings.EqualFold(req.SHA256, hash) {
		return nil, fmt.Errorf("skills: resource changed between pages; restart at offset 0 without sha256")
	}
	if req.Offset < 0 || req.Offset > len(data) || (req.Offset < len(data) && !utf8.RuneStart(data[req.Offset])) {
		return nil, fmt.Errorf("skills: invalid byte offset")
	}
	limit := req.Limit
	if limit <= 0 || limit > 16000 {
		limit = 6000
	}
	end := min(req.Offset+limit, len(data))
	for end > req.Offset && end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	if end == req.Offset && end < len(data) {
		_, n := utf8.DecodeRune(data[end:])
		end += n
	}
	absolute, _ := filepath.Abs(filepath.Join(s.root, bundle))
	result := &ReadResult{Name: selected.Name, Scope: selected.Scope, Key: selected.Key, Path: resource, Root: absolute, Content: string(data[req.Offset:end]), SHA256: hash, TotalBytes: len(data), Offset: req.Offset, NextOffset: end, Truncated: end < len(data)}
	if resource == "SKILL.md" {
		// List paths only; references and scripts enter context only when read.
		_ = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if len(result.Resources) >= 100 {
				return fs.SkipAll
			}
			if !d.IsDir() && d.Type().IsRegular() && p != "SKILL.md" {
				result.Resources = append(result.Resources, p)
			}
			return nil
		})
	}
	return result, nil
}

func validResourcePath(p string) bool {
	return p != "" && p != "." && fs.ValidPath(p) && path.Clean(p) == p && !strings.ContainsAny(p, "\\:")
}
