#!/bin/sh
# Build and package release archives for every supported platform.
#
# Produces, under dist/:
#   panda-<version>-<os>-<arch>.tar.gz   (darwin/linux)
#   panda-<version>-windows-<arch>.zip    (windows, for install.ps1's Expand-Archive)
#   checksums.txt                         (SHA-256 of each archive, "hash  name")
#
# Each archive is a single top-level `openpanda/` directory containing:
#   bin/panda(.exe)   adapters/*.py   extensions/voice/*.py
#   config.example.yaml   capabilities.example-*.yaml   LICENSE
#
# Run `make web` first so the embedded web console is baked in.
#
# Usage: scripts/package.sh [version]   (default: $VERSION or 0.0.9)
#
# Env:
#   OPENPANDA_PACKAGE_TARGETS  space-separated "os-arch" list to build instead
#                              of all six (e.g. "linux-amd64"). Used by the
#                              installer tests, which only need the host's own
#                              archive and should not pay for five cross
#                              compiles they will never execute.

set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-${VERSION:-0.0.9}}"
VERSION="${VERSION#v}"
VERSION_PKG="github.com/Xustalis/OpenPanda/internal/version"
LDFLAGS="-s -w -X ${VERSION_PKG}.Version=${VERSION}"

TARGETS="${OPENPANDA_PACKAGE_TARGETS:-darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64 windows-arm64 lite-linux-amd64 lite-linux-arm64 lite-linux-armv7}"

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

# build <os> <arch> <exe-name> [goarch-goarm] — cross-compiles ./cmd/panda.
# build_lite does the same with -tags lite (no embedded console, no TUI) into
# a lite-<os>-<arch> stage, for constrained nodes.
build() {
    os="$1"; arch="$2"; exe="$3"
    dir="$STAGE/$os-$arch/openpanda"
    mkdir -p "$dir/bin"
    echo "→ build $os/$arch"
    GOOS="$os" GOARCH="$arch" go build -ldflags "$LDFLAGS" -o "$dir/bin/$exe" ./cmd/panda
    mkdir -p "$dir/adapters"
    find adapters -maxdepth 1 -type f -name '*.py' -exec cp {} "$dir/adapters/" \;
    mkdir -p "$dir/extensions/voice"
    find extensions/voice -maxdepth 1 -type f -name '*.py' -exec cp {} "$dir/extensions/voice/" \;
    cp config.example.yaml "$dir/"
    for c in config/capabilities.example-*.yaml; do
        [ -e "$c" ] && cp "$c" "$dir/"
    done
    cp LICENSE "$dir/"
}

build_lite() {
    os="$1"; arch="$2"; goarm="${3:-}"
    # The archive/arch name is "armv7" but Go's GOARCH for 32-bit ARM is
    # "arm" + a GOARM level; keep the two vocabularies apart.
    goarch="$arch"; [ "$goarch" = armv7 ] && goarch=arm
    dir="$STAGE/lite-$os-$arch/openpanda"
    mkdir -p "$dir/bin"
    echo "→ build lite $os/$arch"
    env GOOS="$os" GOARCH="$goarch" ${goarm:+GOARM="$goarm"} \
        go build -tags lite -ldflags "$LDFLAGS" -o "$dir/bin/panda" ./cmd/panda
    mkdir -p "$dir/adapters"
    find adapters -maxdepth 1 -type f -name '*.py' -exec cp {} "$dir/adapters/" \;
    mkdir -p "$dir/extensions/voice"
    find extensions/voice -maxdepth 1 -type f -name '*.py' -exec cp {} "$dir/extensions/voice/" \;
    cp config.example.yaml "$dir/"
    for c in config/capabilities.example-*.yaml; do
        [ -e "$c" ] && cp "$c" "$dir/"
    done
    cp LICENSE "$dir/"
}

# Build only the requested targets. Filtering here rather than at the archive
# step is the whole point of OPENPANDA_PACKAGE_TARGETS: skipping a compile is
# where the time is saved.
wanted() {
    for t in $TARGETS; do
        [ "$t" = "$1" ] && return 0
    done
    return 1
}

want_build() { # <os> <arch> <exe>
    wanted "$1-$2" && build "$1" "$2" "$3"
    return 0
}

want_build_lite() { # <os> <arch> [goarm]
    wanted "lite-$1-$2" && build_lite "$1" "$2" "${3:-}"
    return 0
}

want_build darwin amd64 panda
want_build darwin arm64 panda
want_build linux  amd64 panda
want_build linux  arm64 panda
want_build windows amd64 panda.exe
want_build windows arm64 panda.exe
want_build_lite linux amd64
want_build_lite linux arm64
want_build_lite linux armv7 7

for osarch in $TARGETS; do
    case "$osarch" in
        darwin-amd64|darwin-arm64|linux-amd64|linux-arm64|windows-amd64|windows-arm64) ;;
        lite-linux-amd64|lite-linux-arm64|lite-linux-armv7) ;;
        *) echo "package.sh: unsupported target '$osarch'" >&2; exit 1 ;;
    esac
    src="$STAGE/$osarch/openpanda"
    [ -d "$src" ] || { echo "package.sh: $osarch was not staged" >&2; exit 1; }
    rel="panda-$VERSION-$osarch"
    # macOS stamps provenance xattrs on every file it touches, and bsdtar
    # encodes them as "._name" AppleDouble shadow entries — invisible to
    # `tar -t` on macOS but real members everywhere else. Strip them and tell
    # tar not to re-derive them so the published archive is clean.
    if command -v xattr >/dev/null 2>&1; then
        xattr -rc "$src" 2>/dev/null || true
    fi
    case "$osarch" in
    windows-*)
        make_zip "$DIST/$rel.zip" "$src"
        ;;
    *)
        (cd "$src/.." && COPYFILE_DISABLE=1 tar -czf "$DIST/$rel.tar.gz" openpanda)
        ;;
    esac
done

( cd "$DIST" && for f in panda-*; do [ -f "$f" ] && printf '%s  %s\n' "$(hash_file "$f")" "$f"; done > checksums.txt )

rm -rf "$STAGE"
echo "Packaged (version $VERSION):"
ls -1 "$DIST"
