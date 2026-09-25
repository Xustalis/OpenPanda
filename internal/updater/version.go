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
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
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
	Stage           Stage  `json:"stage"`
	Current         string `json:"current"`
	CurrentCodename string `json:"current_codename,omitempty"`
	Latest          string `json:"latest,omitempty"`
	LatestCodename  string `json:"latest_codename,omitempty"`
	Notes           string `json:"notes,omitempty"`
	Available       bool   `json:"available"`
	Idle            bool   `json:"idle"`
	Error           string `json:"error,omitempty"`
}

// Options configures a Manager.
type Options struct {
	// Repo is the "owner/repo" whose GitHub releases host the assets. Empty
	// falls back to DefaultRepo.
	Repo string
	// Current is the running version (internal/version.Version).
	Current string
	// CurrentCodename is the running build's release codename
	// (internal/version.Codename) — surfaced in Status so displays can print
	// the full "v0.0.9 Periapsis" identity without a second lookup.
	CurrentCodename string
	Logger          *slog.Logger
	// Idle reports whether the task queue is idle (no running / dispatched /
	// waiting-for-context tasks). Apply refuses to proceed while it returns
	// false, so an update never interrupts live work.
	Idle func(context.Context) bool
	// OnAvailable, when set, is invoked once per discovered version when a
	// check finds a newer release — the headless daemon's only channel to
	// surface an update notice. The web panel needs no callback: it polls
	// GET /api/update. codename is the release's thematic name when the
	// release title carries one ("" otherwise).
	OnAvailable func(version, codename string)
	// IncludePrerelease enables checking for prereleases even if Current is stable.
	IncludePrerelease bool
	// NoRestart prevents delayedRestart from running, for one-shot CLI commands.
	NoRestart bool
	// SchemaFloor, when set, reports the data directory's current schema
	// version (PRAGMA user_version). Apply probes the staged binary's
	// migration ceiling via `panda version --json` and refuses the swap when
	// it sits below the floor — installing such a binary would strand the
	// database behind a "schema version newer than binary" startup error.
	// A staged binary too old to report its schema is treated as
	// incompatible whenever the floor is non-zero. Nil disables the guard;
	// Force bypasses it.
	SchemaFloor func(context.Context) (int, error)
	// Force skips the schema-floor guard (CLI --force).
	Force bool
}

// DefaultRepo is where release archives and the checksums file live.
const DefaultRepo = "Xustalis/OpenPanda"

// CompareVersion orders two semantic versions (a leading "v" or "V" is ignored).
// Returns -1, 0, or 1 as a<b, a==b, a>b.
// Comparison follows SemVer 2.0:
//  1. Major, minor, and patch numbers are compared numerically (e.g. 0.0.10 > 0.0.3).
//  2. When major, minor, and patch are equal, a normal release has higher precedence
//     than a pre-release (e.g. 0.0.8 > 0.0.8-preview).
//  3. Precedence between two pre-releases is determined by comparing each dot-separated
//     identifier (numeric vs numeric numerically, non-numeric ASCII, numeric < non-numeric).
//  4. Build metadata (suffix starting with "+") is ignored.
func CompareVersion(a, b string) int {
	coreA, preA := splitSemVer(a)
	coreB, preB := splitSemVer(b)

	n := len(coreA)
	if len(coreB) > n {
		n = len(coreB)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(coreA) {
			x = coreA[i]
		}
		if i < len(coreB) {
			y = coreB[i]
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}

	switch {
	case preA == "" && preB == "":
		return 0
	case preA == "" && preB != "":
		return 1
	case preA != "" && preB == "":
		return -1
	default:
		return comparePrerelease(preA, preB)
	}
}

func splitSemVer(v string) ([]int, string) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")

	if idx := strings.Index(v, "+"); idx >= 0 {
		v = v[:idx]
	}

	pre := ""
	if idx := strings.Index(v, "-"); idx >= 0 {
		pre = v[idx+1:]
		v = v[:idx]
	}

	var nums []int
	for _, seg := range strings.Split(v, ".") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			nums = append(nums, 0)
			continue
		}
		n, err := strconv.Atoi(seg)
		if err != nil {
			d := 0
			for _, r := range seg {
				if r < '0' || r > '9' {
					break
				}
				d = d*10 + int(r-'0')
			}
			nums = append(nums, d)
		} else {
			nums = append(nums, n)
		}
	}
	return nums, pre
}

func comparePrerelease(preA, preB string) int {
	segsA := strings.Split(preA, ".")
	segsB := strings.Split(preB, ".")
	n := len(segsA)
	if len(segsB) < n {
		n = len(segsB)
	}
	for i := 0; i < n; i++ {
		sA, sB := segsA[i], segsB[i]
		numA, isNumA := parseUint(sA)
		numB, isNumB := parseUint(sB)

		switch {
		case isNumA && isNumB:
			if numA < numB {
				return -1
			}
			if numA > numB {
				return 1
			}
		case isNumA && !isNumB:
			return -1
		case !isNumA && isNumB:
			return 1
		default:
			if sA < sB {
				return -1
			}
			if sA > sB {
				return 1
			}
		}
	}
	switch {
	case len(segsA) < len(segsB):
		return -1
	case len(segsA) > len(segsB):
		return 1
	default:
		return 0
	}
}

func parseUint(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	u, err := strconv.ParseUint(s, 10, 64)
	return u, err == nil
}

// Release is the latest GitHub release: its version tag (leading "v"
// stripped), its codename when the release title carries one ("v0.0.9
// Periapsis" → "Periapsis"), and the raw release-notes body, which the
// console surfaces as a changelog digest when an update is available.
type Release struct {
	Version  string
	Codename string
	Notes    string
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
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

var githubAPIBase = "https://api.github.com"

// SetAPIBaseForTest overrides the GitHub API base URL for testing and returns
// a cleanup function that restores the previous value.
func SetAPIBaseForTest(base string) func() {
	orig := githubAPIBase
	if base == "" {
		githubAPIBase = "https://api.github.com"
	} else {
		githubAPIBase = base
	}
	return func() {
		githubAPIBase = orig
	}
}

// FindLatest queries GitHub releases for repo. If includePrerelease is true,
// pre-release versions are considered alongside stable releases; otherwise
// only stable releases are considered. If current contains a pre-release suffix
// (e.g. "-preview"), pre-releases are automatically considered.
func FindLatest(ctx context.Context, repo string, includePrerelease bool, current string) (Release, error) {
	if repo == "" {
		repo = DefaultRepo
	}
	if strings.Contains(current, "-") {
		includePrerelease = true
	}

	token := gitHubToken(ctx)

	// First attempt: query recent releases list to discover both stable and pre-releases.
	url := githubAPIBase + "/repos/" + repo + "/releases?per_page=15"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err == nil {
		setGitHubHeaders(req, token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if checkErr := checkGitHubStatus(resp, repo); checkErr != nil {
				if _, ok := checkErr.(*RateLimitExceeded); ok {
					return Release{}, checkErr
				}
				if _, ok := checkErr.(*AccessDenied); ok {
					return Release{}, checkErr
				}
			} else if resp.StatusCode == http.StatusOK {
				var releases []githubRelease
				if err := json.NewDecoder(resp.Body).Decode(&releases); err == nil && len(releases) > 0 {
					var best *githubRelease
					for i := range releases {
						r := &releases[i]
						if r.Draft || r.TagName == "" {
							continue
						}
						if !includePrerelease && r.Prerelease {
							continue
						}
						ver := strings.TrimPrefix(strings.TrimPrefix(r.TagName, "v"), "V")
						if best == nil || CompareVersion(ver, strings.TrimPrefix(strings.TrimPrefix(best.TagName, "v"), "V")) > 0 {
							best = r
						}
					}
					// If no non-prerelease was found but includePrerelease was false,
					// fall back to considering any non-draft release.
					if best == nil && !includePrerelease {
						for i := range releases {
							r := &releases[i]
							if r.Draft || r.TagName == "" {
								continue
							}
							ver := strings.TrimPrefix(strings.TrimPrefix(r.TagName, "v"), "V")
							if best == nil || CompareVersion(ver, strings.TrimPrefix(strings.TrimPrefix(best.TagName, "v"), "V")) > 0 {
								best = r
							}
						}
					}
					if best != nil {
						v := strings.TrimPrefix(strings.TrimPrefix(best.TagName, "v"), "V")
						return Release{Version: v, Codename: releaseCodename(best.Name, v), Notes: best.Body}, nil
					}
				}
			}
		}
	}

	// Fallback to /releases/latest endpoint
	return latestSingle(ctx, repo, token)
}

func latestSingle(ctx context.Context, repo, token string) (Release, error) {
	url := githubAPIBase + "/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	setGitHubHeaders(req, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("release lookup: %w", err)
	}
	defer resp.Body.Close()
	if err := checkGitHubStatus(resp, repo); err != nil {
		return Release{}, err
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("release lookup: %w", err)
	}
	if rel.TagName == "" {
		return Release{}, fmt.Errorf("release has no tag_name")
	}
	v := strings.TrimPrefix(strings.TrimPrefix(rel.TagName, "v"), "V")
	return Release{Version: v, Codename: releaseCodename(rel.Name, v), Notes: rel.Body}, nil
}

// releaseCodename extracts the thematic name from a GitHub release title like
// "v0.0.9 Periapsis". The title must lead with the release's own version tag —
// anything else returns "", so an unrelated naming scheme never produces a
// bogus codename. Multi-word names are accepted; digits and punctuation are
// not, which keeps "v1.2.3 — emergency rollup"-style titles from leaking in.
func releaseCodename(name, version string) string {
	rest := ""
	for _, p := range []string{"v" + version, "V" + version, version} {
		if r, ok := strings.CutPrefix(strings.TrimSpace(name), p); ok {
			rest = r
			break
		}
	}
	rest = strings.Trim(rest, " \t-–—·:=\"'")
	if rest == "" || len([]rune(rest)) > 32 {
		return ""
	}
	for _, r := range rest {
		if !unicode.IsLetter(r) && r != ' ' {
			return ""
		}
	}
	return rest
}

func gitHubToken(ctx context.Context) string {
	if tok := strings.TrimSpace(os.Getenv("PANDA_GITHUB_TOKEN")); tok != "" {
		return tok
	}
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		return tok
	}
	ghCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ghCtx, "gh", "auth", "token")
	if out, err := cmd.Output(); err == nil {
		if t := strings.TrimSpace(string(out)); t != "" {
			return t
		}
	}
	return ""
}

func setGitHubHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "OpenPanda-updater")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func checkGitHubStatus(resp *http.Response, repo string) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusForbidden:
		if remain := resp.Header.Get("X-RateLimit-Remaining"); remain == "0" {
			r, _ := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
			return &RateLimitExceeded{Reset: r}
		}
		hint := "如果这是私有仓库，先 export PANDA_GITHUB_TOKEN=github_pat_… 再启动；如果仓库公开，稍等一会儿或配置 token 提升 60/小时上限"
		return &AccessDenied{Hint: hint}
	case http.StatusUnauthorized:
		return &AccessDenied{Hint: "GitHub token 已失效或权限不足，请检查 PANDA_GITHUB_TOKEN / GITHUB_TOKEN"}
	case http.StatusNotFound:
		return &AccessDenied{Hint: "仓库 " + repo + " 不存在，或 token 无内容读取权限（需 repo/public_repo scope）"}
	case http.StatusTooManyRequests:
		r, _ := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
		return &RateLimitExceeded{Reset: r}
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("release lookup returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
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
