package commander

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
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

// Hash returns a reproducible SHA-256 of the context (excluding volatile
// paths). Used as the context_store key and pointer hit check.
func (fc *FileContext) Hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "type=%s\n", fc.Type)
	fmt.Fprintf(h, "repo=%s\n", fc.Repo)
	fmt.Fprintf(h, "branch=%s\n", fc.Branch)
	fmt.Fprintf(h, "commit=%s\n", fc.Commit)
	fmt.Fprintf(h, "scope=%s\n", strings.Join(fc.Scope, ","))
	// Sort env keys: Go randomizes map iteration order, which would otherwise
	// make this "reproducible" hash drift and break it as a pointer-hit key.
	keys := make([]string, 0, len(fc.Env))
	for k := range fc.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "env:%s=%s\n", k, fc.Env[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}
