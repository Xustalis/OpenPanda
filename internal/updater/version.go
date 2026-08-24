// Package updater implements the self-update path for the `panda` CLI: it
// checks GitHub for a newer release, downloads and verifies the platform's
// release archive, and — once the task queue is idle — atomically swaps the
// running binary (and its agent adapters) into place. Everything it writes is
// staged under a temporary directory and removed on completion or cancel, so
// a cancelled or deleted update leaves no residue behind.
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
)

// Stage is the updater's position in the check → download → apply pipeline.
// It is also the wire value the web console reads to render its controls.
type Stage string

const (
	StageIdle        Stage = "idle"        // no update known (or already up to date)
	StageChecking    Stage = "checking"    // a version check is in flight
	StageAvailable   Stage = "available"   // a newer release exists
	StageDownloading Stage = "downloading" // release archive is downloading
	StageStaged      Stage = "staged"      // downloaded + verified, ready to apply
	StageApplying    Stage = "applying"    // replacing the binary now
	StageDone        Stage = "done"        // applied successfully
	StageError       Stage = "error"
)

// Status is the wire shape served at GET /api/update. It is a point-in-time
// snapshot; the console polls it (or reads the POST responses) to track
// progress through check, download, and apply.
type Status struct {
	Stage     Stage  `json:"stage"`
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	Idle      bool   `json:"idle"`
	Error     string `json:"error,omitempty"`
}

// Options configures a Manager.
type Options struct {
	// Repo is the "owner/repo" whose GitHub releases host the assets. Empty
	// falls back to DefaultRepo.
	Repo string
	// Current is the running version (internal/version.Version).
	Current string
	Logger  *slog.Logger
	// Idle reports whether the task queue is idle (no running / dispatched /
	// waiting-for-context tasks). Apply refuses to proceed while it returns
	// false, so an update never interrupts live work.
	Idle func(context.Context) bool
}

// DefaultRepo is where release archives and the checksums file live.
const DefaultRepo = "Xustalis/OpenPanda"

// CompareVersion orders two semantic versions (a leading "v" is ignored).
// Returns -1, 0, or 1 as a<b, a==b, a>b. Comparison is numeric over the
// dot-separated segments; a pre-release/build suffix ("-rc1", "-beta") makes
// that segment sort no higher than the numeric prefix it carries.
func CompareVersion(a, b string) int {
	av := numericParts(a)
	bv := numericParts(b)
	n := len(av)
	if len(bv) > n {
		n = len(bv)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

func numericParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	var out []int
	for _, seg := range strings.Split(v, ".") {
		n := 0
		for _, r := range seg {
			if r < '0' || r > '9' {
				break // stop at a pre-release/build suffix
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}

// LatestVersion queries the GitHub "latest" release for repo and returns its
// tag with the leading "v" stripped.
func LatestVersion(ctx context.Context, repo string) (string, error) {
	if repo == "" {
		repo = DefaultRepo
	}
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "OpenPanda-updater")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("release lookup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release lookup returned %s", resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("release lookup: %w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("release has no tag_name")
	}
	return strings.TrimPrefix(rel.TagName, "v"), nil
}

// AssetName returns the release asset file name for the current platform and
// the given version. Unix and Windows targets use the same GOARCH naming
// convention as scripts/package.sh; this keeps self-update aligned with the
// standalone installers.
func AssetName(version string) string {
	return assetNameFor(version, runtime.GOOS, runtime.GOARCH)
}

func assetNameFor(version, os, arch string) string {
	if os == "windows" {
		return fmt.Sprintf("panda-%s-%s-%s.zip", version, os, arch)
	}
	return fmt.Sprintf("panda-%s-%s-%s.tar.gz", version, os, arch)
}

// defaultLogger returns a discard logger when a Manager is built without one.
func defaultLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
