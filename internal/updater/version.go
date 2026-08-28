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
	"os"
	"regexp"
	"runtime"
	"strconv"
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
// progress through check, download, and apply. Notes carries the latest
// release's changelog digest once a check has found one.
type Status struct {
	Stage     Stage  `json:"stage"`
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Notes     string `json:"notes,omitempty"`
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
	// OnAvailable, when set, is invoked once per discovered version when a
	// check finds a newer release — the headless daemon's only channel to
	// surface an update notice. The web panel needs no callback: it polls
	// GET /api/update.
	OnAvailable func(version string)
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

// Release is the latest GitHub release: its version tag (leading "v"
// stripped) and the raw release-notes body, which the console surfaces as a
// changelog digest when an update is available.
type Release struct {
	Version string
	Notes   string
}

// RateLimitExceeded is returned when GitHub's REST API enforces a cap: 60
// requests/hour unauthenticated or 5,000/hour for a token. The Manager maps
// this to StageIdle so the surface never flashes an error for a temporary
// constraint.
type RateLimitExceeded struct {
	Reset int64 // Unix epoch when the counter resets; 0 if unknown
}

func (e *RateLimitExceeded) Error() string {
	if e.Reset == 0 {
		return "GitHub rate limit exceeded"
	}
	return "GitHub rate limit exceeded (resets at epoch " + strconv.FormatInt(e.Reset, 10) + ")"
}

// AccessDenied signals that the repository cannot be read (private repo with
// no or a wrong token, org IP restrictions, or a revoked token). The
// Manager turns this into StageIdle plus a Notes hint pointing to the
// PANDA_GITHUB_TOKEN env var so the UI can surface actionable text instead of
// a naked "403 Forbidden".
type AccessDenied struct {
	Hint string
}

func (e *AccessDenied) Error() string { return "release lookup: access denied — " + e.Hint }

// Latest queries the GitHub "latest" release for repo. It reads
// PANDA_GITHUB_TOKEN (GITHUB_TOKEN works too) to authenticate — required on
// private repos and useful to lift the anonymous 60/hr cap.
//
// Returns typed sentinels for 403 / 429 so callers can gracefully degrade
// rather than show a raw HTTP error in the console.
func Latest(ctx context.Context, repo string) (Release, error) {
	if repo == "" {
		repo = DefaultRepo
	}
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "OpenPanda-updater")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// Token: PANDA_GITHUB_TOKEN > GITHUB_TOKEN. We keep both for users who
	// already have GITHUB_TOKEN wired in their shell (the common pattern).
	if tok := strings.TrimSpace(os.Getenv("PANDA_GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	} else if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("release lookup: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusForbidden:
		// GitHub distinguishes rate-limit 403 (X-RateLimit-Remaining: 0) from
		// an auth/IP-rule 403 via the remaining header. Treat the two cases
		// so the UI can show the right fix.
		if remain := resp.Header.Get("X-RateLimit-Remaining"); remain == "0" {
			r, _ := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
			return Release{}, &RateLimitExceeded{Reset: r}
		}
		hint := "如果这是私有仓库，先 export PANDA_GITHUB_TOKEN=github_pat_… 再启动；如果仓库公开，稍等一会儿或配置 token 提升 60/小时上限"
		return Release{}, &AccessDenied{Hint: hint}
	case http.StatusUnauthorized:
		return Release{}, &AccessDenied{Hint: "GitHub token 已失效或权限不足，请检查 PANDA_GITHUB_TOKEN / GITHUB_TOKEN"}
	case http.StatusNotFound:
		return Release{}, &AccessDenied{Hint: "仓库 " + repo + " 不存在，或 token 无内容读取权限（需 repo/public_repo scope）"}
	case http.StatusTooManyRequests:
		r, _ := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
		return Release{}, &RateLimitExceeded{Reset: r}
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Release{}, fmt.Errorf("release lookup returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("release lookup: %w", err)
	}
	if rel.TagName == "" {
		return Release{}, fmt.Errorf("release has no tag_name")
	}
	return Release{Version: strings.TrimPrefix(rel.TagName, "v"), Notes: rel.Body}, nil
}

// summarizeNotes trims a release-notes body to the short changelog digest
// shown next to "update available": the first 12 non-empty lines capped at
// 600 runes, with markdown images and HTML tags stripped — they render as
// noise in the console card.
func summarizeNotes(body string) string {
	var lines []string
	for _, ln := range strings.Split(strings.TrimSpace(body), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		ln = imgRE.ReplaceAllString(ln, "")
		ln = tagRE.ReplaceAllString(ln, "")
		if ln = strings.TrimSpace(ln); ln != "" {
			lines = append(lines, ln)
		}
		if len(lines) >= 12 {
			break
		}
	}
	out := strings.Join(lines, "\n")
	if r := []rune(out); len(r) > 600 {
		out = string(r[:600]) + "…"
	}
	return out
}

var (
	imgRE = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	tagRE = regexp.MustCompile(`<[^>]+>`)
)

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
