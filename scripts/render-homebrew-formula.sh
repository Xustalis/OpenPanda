#!/bin/sh
# Render a versioned, checksum-pinned Homebrew formula from release artifacts.
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${1:?usage: render-homebrew-formula.sh VERSION [OUTPUT]}"
VERSION="${VERSION#v}"
DIST="${OPENPANDA_DIST_DIR:-$ROOT/dist}"
OUTPUT="${2:-$DIST/openpanda.rb}"
SUMS="$DIST/checksums.txt"

checksum() {
    awk -v f="$1" '$2==f { print $1; found=1; exit } END { if (!found) exit 1 }' "$SUMS"
}

DARWIN_AMD64="$(checksum "panda-$VERSION-darwin-amd64.tar.gz")"
DARWIN_ARM64="$(checksum "panda-$VERSION-darwin-arm64.tar.gz")"
LINUX_AMD64="$(checksum "panda-$VERSION-linux-amd64.tar.gz")"
LINUX_ARM64="$(checksum "panda-$VERSION-linux-arm64.tar.gz")"

mkdir -p "$(dirname "$OUTPUT")"
sed \
  -e "s/@VERSION@/$VERSION/g" \
  -e "s/@DARWIN_AMD64_SHA256@/$DARWIN_AMD64/g" \
  -e "s/@DARWIN_ARM64_SHA256@/$DARWIN_ARM64/g" \
  -e "s/@LINUX_AMD64_SHA256@/$LINUX_AMD64/g" \
  -e "s/@LINUX_ARM64_SHA256@/$LINUX_ARM64/g" \
  "$ROOT/deploy/homebrew/openpanda.rb.tmpl" > "$OUTPUT"

echo "rendered $OUTPUT for v$VERSION"
