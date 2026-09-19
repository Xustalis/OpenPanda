#!/bin/sh
# Sync Formula/openpanda.rb in the Homebrew tap from a *published* release.
#
# Why this exists: .github/workflows/release.yml renders the formula from the
# local dist/ it just built, so the tap can only ever be written by a release
# run. When a tag is re-cut or a release is deleted, nothing can bring the tap
# back into agreement with reality — which is how the tap ended up pinned to
# `version "0.0.8"` after v0.0.8 was removed, leaving every
# `brew install Xustalis/openpanda/openpanda` on a 404.
#
# This script starts from the release's own checksums.txt (downloaded, not
# rebuilt), so it repairs or advances the tap from whatever is actually
# published.
#
# Usage:
#   scripts/sync-homebrew-tap.sh                 # latest stable release
#   scripts/sync-homebrew-tap.sh 0.0.7           # a specific version
#   scripts/sync-homebrew-tap.sh 0.0.7 --render-only   # write dist/openpanda.rb, no push
#   scripts/sync-homebrew-tap.sh 0.0.7 --force   # allow a pre-release tag
#
# Env:
#   OPENPANDA_REPO_URL     override the source repository
#   OPENPANDA_TAP_REPO     override the tap repository (owner/name)
#   OPENPANDA_DIST_DIR     where the rendered formula is written
#   GH_TOKEN / GITHUB_TOKEN  used for the tap clone/push (gh CLI otherwise)
#
# Homebrew has no pre-release concept: the formula is a single version, so
# publishing a preview here would move the brew channel off stable. Pre-release
# versions are refused unless --force is passed explicitly.

set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO_URL="${OPENPANDA_REPO_URL:-https://github.com/Xustalis/OpenPanda}"
TAP_REPO="${OPENPANDA_TAP_REPO:-Xustalis/homebrew-openpanda}"
DIST="${OPENPANDA_DIST_DIR:-$ROOT/dist}"

VERSION=""
RENDER_ONLY=0
FORCE=0

for arg in "$@"; do
    case "$arg" in
        --render-only) RENDER_ONLY=1 ;;
        --force)       FORCE=1 ;;
        --help|-h)     sed -n 's/^# \{0,1\}//p' "$0" | sed -n '/^Usage:/,/^$/p'; exit 0 ;;
        -*)            echo "sync-homebrew-tap: unknown flag: $arg" >&2; exit 2 ;;
        *)             VERSION="$arg" ;;
    esac
done

die() { printf '%s\n' "$1" >&2; exit 1; }

for tool in curl awk sed; do
    command -v "$tool" >/dev/null 2>&1 || die "sync-homebrew-tap: $tool is required"
done

# ── Resolve the version ─────────────────────────────────────────────────────
# `releases/latest` skips drafts and pre-releases, which is exactly the
# channel the formula should track.
if [ -z "$VERSION" ]; then
    tag="$(curl -fsSL --max-time 30 "$REPO_URL/releases/latest" -o /dev/null -w '%{url_effective}' 2>/dev/null || true)"
    tag="${tag##*/}"
    [ -n "$tag" ] || die "sync-homebrew-tap: could not resolve the latest release (pass a version explicitly)"
    VERSION="$tag"
fi
VERSION="${VERSION#v}"

case "$VERSION" in
    *-*)
        if [ "$FORCE" != 1 ]; then
            die "sync-homebrew-tap: $VERSION looks like a pre-release; the tap tracks stable only (pass --force to override)"
        fi
        ;;
esac

# ── Pull the published checksums ────────────────────────────────────────────
# Downloading beats rebuilding: the point is to match what is on the release,
# and it means a repair needs no Go toolchain at all.
BASE="${OPENPANDA_RELEASE_BASE:-$REPO_URL/releases/download/v$VERSION}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "→ fetching checksums for v$VERSION"
if ! curl -fsSL --retry 3 --max-time 60 -o "$WORK/checksums.txt" "$BASE/checksums.txt"; then
    die "sync-homebrew-tap: no checksums.txt at $BASE — does release v$VERSION exist?"
fi

# The formula only covers the four unix targets; a missing entry would render
# an empty sha256, so fail before writing anything.
for target in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
    archive="panda-$VERSION-$target.tar.gz"
    awk -v f="$archive" '$2==f && length($1)==64 { found=1 } END { exit(found ? 0 : 1) }' \
        "$WORK/checksums.txt" \
        || die "sync-homebrew-tap: checksums.txt has no entry for $archive"
done

# ── Render ──────────────────────────────────────────────────────────────────
mkdir -p "$DIST"
OUT="$DIST/openpanda.rb"
OPENPANDA_DIST_DIR="$WORK" "$ROOT/scripts/render-homebrew-formula.sh" "$VERSION" "$OUT" >/dev/null
echo "→ rendered $OUT"

if [ "$RENDER_ONLY" = 1 ]; then
    echo "sync-homebrew-tap: render-only, tap untouched"
    exit 0
fi

# ── Publish to the tap ──────────────────────────────────────────────────────
if ! command -v gh >/dev/null 2>&1; then
    die "sync-homebrew-tap: gh is required to update $TAP_REPO"
fi

echo "→ updating $TAP_REPO"
gh repo clone "$TAP_REPO" "$WORK/tap" -- --quiet >/dev/null 2>&1 \
    || die "sync-homebrew-tap: could not clone $TAP_REPO"
mkdir -p "$WORK/tap/Formula"
cp "$OUT" "$WORK/tap/Formula/openpanda.rb"

cd "$WORK/tap"
if git diff --quiet -- Formula/openpanda.rb; then
    echo "sync-homebrew-tap: tap already at v$VERSION, nothing to push"
    exit 0
fi
git config user.name "${GIT_AUTHOR_NAME:-github-actions[bot]}"
git config user.email "${GIT_AUTHOR_EMAIL:-41898282+github-actions[bot]@users.noreply.github.com}"
git add Formula/openpanda.rb
git commit -q -m "openpanda v$VERSION"

if [ -n "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]; then
    token="${GH_TOKEN:-$GITHUB_TOKEN}"
    git push "https://x-access-token:${token}@github.com/${TAP_REPO}.git" HEAD
else
    # Local run: gh's credential helper is already configured for github.com.
    git push
fi
echo "sync-homebrew-tap: $TAP_REPO now pins v$VERSION"
