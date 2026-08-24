#!/bin/sh
# Verify the local release contract used by all three installers.
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="${OPENPANDA_DIST_DIR:-$ROOT/dist}"
VERSION="${1:-0.0.3}"
VERSION="${VERSION#v}"

set -- \
  "panda-$VERSION-darwin-amd64.tar.gz" \
  "panda-$VERSION-darwin-arm64.tar.gz" \
  "panda-$VERSION-linux-amd64.tar.gz" \
  "panda-$VERSION-linux-arm64.tar.gz" \
  "panda-$VERSION-windows-amd64.zip" \
  "panda-$VERSION-windows-arm64.zip"

[ -f "$DIST/checksums.txt" ] || { echo "missing dist/checksums.txt" >&2; exit 1; }
for archive in "$@"; do
    [ -f "$DIST/$archive" ] || { echo "missing release asset: $archive" >&2; exit 1; }
    awk -v f="$archive" '$2==f { found=1 } END { exit(found ? 0 : 1) }' "$DIST/checksums.txt" \
      || { echo "missing checksum entry: $archive" >&2; exit 1; }
done
(cd "$DIST" && {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum -c checksums.txt;
    else
        while read -r want name; do
            [ -n "$name" ] || continue
            got="$(shasum -a 256 "$name" | awk '{print $1}')"
            [ "$got" = "$want" ] || { echo "checksum mismatch: $name" >&2; exit 1; }
        done < checksums.txt
    fi
})
echo "release contract verified for v$VERSION"
