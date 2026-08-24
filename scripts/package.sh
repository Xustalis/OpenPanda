#!/bin/sh
# Build and package release archives for every supported platform.
#
# Produces, under dist/:
#   panda-<version>-<os>-<arch>.tar.gz   (darwin/linux)
#   panda-<version>-windows-<arch>.zip    (windows, for install.ps1's Expand-Archive)
#   checksums.txt                         (SHA-256 of each archive, "hash  name")
#
# Each archive is a single top-level `openpanda/` directory containing:
#   bin/panda(.exe)   adapters/*.py   config.example.yaml
#   capabilities.example-*.yaml        LICENSE
#
# Run `make web` first so the embedded web console is baked in.
#
# Usage: scripts/package.sh [version]   (default: $VERSION or 0.0.3)

set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-${VERSION:-0.0.3}}"
VERSION="${VERSION#v}"
VERSION_PKG="github.com/Xustalis/OpenPanda/internal/version"
LDFLAGS="-s -w -X ${VERSION_PKG}.Version=${VERSION}"

DIST="${OPENPANDA_DIST_DIR:-$ROOT/dist}"
STAGE="$DIST/package"
rm -rf "$STAGE" "$DIST"/panda-* "$DIST/checksums.txt"
mkdir -p "$DIST"

# hash_file <path> → lowercase hex (macOS shasum / Linux sha256sum).
hash_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        openssl dgst -sha256 "$1" | awk '{print $NF}'
    fi
}

# make_zip <zipfile> <srcdir>  — portable zip through `zip` or `python3`.
make_zip() {
    local_out="$1"; local_src="$2"
    if command -v zip >/dev/null 2>&1; then
        (cd "$(dirname "$local_src")" && zip -qr "$local_out" "$(basename "$local_src")")
    else
        (cd "$(dirname "$local_src")" && python3 -m zipfile -c "$local_out" "$(basename "$local_src")")
    fi
}

# build <os> <arch> <exe-name>
build() {
    os="$1"; arch="$2"; exe="$3"
    dir="$STAGE/$os-$arch/openpanda"
    mkdir -p "$dir/bin"
    echo "→ build $os/$arch"
    GOOS="$os" GOARCH="$arch" go build -ldflags "$LDFLAGS" -o "$dir/bin/$exe" ./cmd/panda
    mkdir -p "$dir/adapters"
    find adapters -maxdepth 1 -type f -name '*.py' -exec cp {} "$dir/adapters/" \;
    cp config.example.yaml "$dir/"
    for c in config/capabilities.example-*.yaml; do
        [ -e "$c" ] && cp "$c" "$dir/"
    done
    cp LICENSE "$dir/"
}

build darwin amd64 panda
build darwin arm64 panda
build linux  amd64 panda
build linux  arm64 panda
build windows amd64 panda.exe
build windows arm64 panda.exe

for osarch in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64 windows-arm64; do
    src="$STAGE/$osarch/openpanda"
    rel="panda-$VERSION-$osarch"
    case "$osarch" in
    windows-*)
        make_zip "$DIST/$rel.zip" "$src"
        ;;
    *)
        (cd "$src/.." && tar -czf "$DIST/$rel.tar.gz" openpanda)
        ;;
    esac
done

( cd "$DIST" && for f in panda-*; do [ -f "$f" ] && printf '%s  %s\n' "$(hash_file "$f")" "$f"; done > checksums.txt )

rm -rf "$STAGE"
echo "Packaged (version $VERSION):"
ls -1 "$DIST"
