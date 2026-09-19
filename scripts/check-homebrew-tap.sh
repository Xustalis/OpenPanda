#!/bin/sh
# check-homebrew-tap.sh — fails when the Homebrew tap points at a release that
# no longer exists or has moved.
#
# The regression this guards: `openpanda.rb` in Xustalis/homebrew-openpanda was
# left pinned to `version "0.0.8"` after the v0.0.8 tag and release were
# deleted. Nothing complained, and every `brew install
# Xustalis/openpanda/openpanda` (the command the README and every release body
# advertise) returned 404 until someone happened to look.
#
# Read-only; works anonymously against a public repository. Exits non-zero only
# on *definitive* drift: an unreachable API (rate limit, network) reports SKIP
# instead, so a scheduled run does not go red for reasons that have nothing to
# do with the formula.
#
# Env:
#   OPENPANDA_REPO_URL   source repository (default Xustalis/OpenPanda)
#   OPENPANDA_TAP_REPO   tap repository (default Xustalis/homebrew-openpanda)
#   OPENPANDA_TAP_BRANCH tap branch (default main)
set -u

REPO_URL="${OPENPANDA_REPO_URL:-https://github.com/Xustalis/OpenPanda}"
REPO_SLUG="${REPO_URL#https://github.com/}"
REPO_SLUG="${REPO_SLUG%/}"
TAP_REPO="${OPENPANDA_TAP_REPO:-Xustalis/homebrew-openpanda}"
TAP_BRANCH="${OPENPANDA_TAP_BRANCH:-main}"

fail=0
note() { printf '%s\n' "$1"; }
bad()  { printf '%s\n' "$1" >&2; fail=1; }
skip() { printf 'SKIP: %s\n' "$1"; exit 0; }

command -v curl >/dev/null 2>&1 || skip "curl not available"

# get <url> [accept] → body on stdout, or empty on transport failure. The HTTP
# status lands in $http_code so a caller can tell 404 (definitive) from 403
# (rate limit).
http_code=""
get() {
    _url="$1"; _accept="${2:-}"
    if [ -n "$_accept" ]; then
        _out="$(curl -sSL --retry 2 --max-time 30 -H "Accept: $_accept" -w '\n%{http_code}' "$_url" 2>/dev/null || true)"
    else
        _out="$(curl -sSL --retry 2 --max-time 30 -w '\n%{http_code}' "$_url" 2>/dev/null || true)"
    fi
    http_code="$(printf '%s' "$_out" | tail -n1)"
    printf '%s' "$_out" | sed '$d'
}

# head_status <url> → HTTP status of a HEAD request (no body).
head_status() {
    curl -sSL -o /dev/null -w '%{http_code}' --retry 2 --max-time 30 -I "$1" 2>/dev/null || echo 000
}

# ── 1. Read the formula ─────────────────────────────────────────────────────
# The contents API is preferred over raw.githubusercontent.com: the raw
# endpoint serves a cached copy for minutes after a push, which would report
# the previous version right when a repair is being verified.
formula="$(get "https://api.github.com/repos/${TAP_REPO}/contents/Formula/openpanda.rb?ref=${TAP_BRANCH}" 'application/vnd.github.raw')"
if [ -z "$formula" ]; then
    # Fall back to the CDN (not rate limited) before giving up.
    formula="$(get "https://raw.githubusercontent.com/${TAP_REPO}/${TAP_BRANCH}/Formula/openpanda.rb")"
fi
if [ -z "$formula" ]; then
    case "$http_code" in
        404)
            bad "check-homebrew-tap: $TAP_REPO has no Formula/openpanda.rb on $TAP_BRANCH"
            note "check-homebrew-tap: FAILED"
            exit 1
            ;;
        *) skip "could not read the tap formula (HTTP $http_code; rate limit or network?)" ;;
    esac
fi

version="$(printf '%s\n' "$formula" | sed -n 's/^ *version "\([^"]*\)".*/\1/p' | head -n1)"
if [ -z "$version" ]; then
    bad "check-homebrew-tap: no 'version \"...\"' line in the formula"
    note "check-homebrew-tap: FAILED"
    exit 1
fi

# ── 2. Does that release still exist, and is it stable? ─────────────────────
meta="$(get "https://api.github.com/repos/${REPO_SLUG}/releases/tags/v${version}" 'application/vnd.github+json')"
if [ -z "$meta" ]; then
    if [ "$http_code" = 404 ]; then
        bad "check-homebrew-tap: formula pins v$version but that release does not exist (brew install is a 404)"
        note "check-homebrew-tap: FAILED"
        exit 1
    fi
    skip "could not read release metadata for v$version (HTTP $http_code; rate limit or network?)"
fi

case "$meta" in
    *'"draft": true'*) bad "check-homebrew-tap: v$version is still a draft release";;
esac
case "$meta" in
    *'"prerelease": true'*)
        bad "check-homebrew-tap: formula pins pre-release v$version; the tap must track a stable tag" ;;
esac

# ── 3. Are the four archives the formula references actually downloadable? ──
for target in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
    url="$REPO_URL/releases/download/v${version}/panda-${version}-${target}.tar.gz"
    code="$(head_status "$url")"
    case "$code" in
        200|302) ;;
        *) bad "check-homebrew-tap: asset for $target returned HTTP $code ($url)";;
    esac
done

# ── 4. Do the recorded sha256 values still match the published checksums? ───
sums="$(get "$REPO_URL/releases/download/v${version}/checksums.txt")"
if [ -z "$sums" ]; then
    bad "check-homebrew-tap: v$version publishes no checksums.txt"
else
    for target in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
        archive="panda-${version}-${target}.tar.gz"
        want="$(printf '%s\n' "$sums" | awk -v f="$archive" '$2==f { print $1; exit }')"
        [ -n "$want" ] || { bad "check-homebrew-tap: checksums.txt has no $archive"; continue; }
        # Requiring the published value to appear somewhere in the formula is
        # enough to catch one rendered against a different build.
        printf '%s\n' "$formula" | grep -q "$want" \
            || bad "check-homebrew-tap: formula sha256 for $archive does not match the published checksum"
    done
fi

if [ "$fail" -ne 0 ]; then
    note "check-homebrew-tap: FAILED (repair with scripts/sync-homebrew-tap.sh $version)"
    exit 1
fi
note "check-homebrew-tap: OK (tap pins v$version; release, assets and checksums verified)"
