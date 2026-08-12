package commander

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// FileContext is the file-type task context (design doc §12.5). It records
// repo identity plus optional scope so the executor knows what to touch.
type FileContext struct {
	Type   string            `json:"type"`
	Repo   string            `json:"repo,omitempty"`
	Branch string            `json:"branch,omitempty"`
	Commit string            `json:"commit,omitempty"`
	Scope  []string          `json:"scope,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

// PackFileContext builds a FileContext from a local repo path. If the path is
// a git repo, branch + commit are captured so a full snapshot can be
// reconstructed or fetched elsewhere.
func PackFileContext(ctx context.Context, repoPath string, scope []string) (*FileContext, error) {
	fc := &FileContext{Type: "file", Scope: scope, Env: map[string]string{}}
	if repoPath == "" {
		return fc, nil
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return fc, err
	}
	fc.Repo = abs
	fc.Branch = gitOut(ctx, abs, "rev-parse", "--abbrev-ref", "HEAD")
	fc.Commit = gitOut(ctx, abs, "rev-parse", "HEAD")
	return fc, nil
}

// Hash returns a reproducible SHA-256 of the context (excluding volatile
// paths). Used as the context_store key and pointer hit check.
func (fc *FileContext) Hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "type=%s\n", fc.Type)
	fmt.Fprintf(h, "repo=%s\n", fc.Repo)
	fmt.Fprintf(h, "branch=%s\n", fc.Branch)
	fmt.Fprintf(h, "commit=%s\n", fc.Commit)
	fmt.Fprintf(h, "scope=%s\n", strings.Join(fc.Scope, ","))
	for k, v := range fc.Env {
		fmt.Fprintf(h, "env:%s=%s\n", k, v)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// gitOut runs a git command and returns trimmed stdout, or "" on failure.
func gitOut(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
