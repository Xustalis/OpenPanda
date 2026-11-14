#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Install an already-built tree. make install-local builds web, then Go first.
set -eu

ROOT="${1:?usage: install-local.sh SOURCE_ROOT HOME_DIR}"
INSTALL_HOME="${2:?usage: install-local.sh SOURCE_ROOT HOME_DIR}"
PATH_BIN="$INSTALL_HOME/.local/bin"
RELEASE_PREFIX="$INSTALL_HOME/.local/share/openpanda"

# Validate all inputs before touching the installation. Never copy config/data.
test -f "$ROOT/bin/panda"
for files in "$ROOT"/adapters/*.py "$ROOT"/extensions/voice/*.py "$ROOT"/drivers/panda-*; do
    test -f "$files"
done
mkdir -p "$PATH_BIN"
if [ -d "$RELEASE_PREFIX/bin" ]; then
    DEST_BIN="$RELEASE_PREFIX/bin/panda"
else
    DEST_BIN="$PATH_BIN/panda"
fi

# Stage beside the destination so rename is atomic and preserves a running
# Mach-O's signed vnode. The make build signature survives the byte-for-byte copy.
STAGE="$(mktemp -d "$(dirname "$DEST_BIN")/.panda-install.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT HUP INT TERM
cp "$ROOT/bin/panda" "$STAGE/panda"
chmod 755 "$STAGE/panda"
mkdir -p "$STAGE/adapters" "$STAGE/extensions/voice" "$STAGE/drivers"
cp "$ROOT"/adapters/*.py "$STAGE/adapters/"
cp "$ROOT"/extensions/voice/*.py "$STAGE/extensions/voice/"
cp "$ROOT"/drivers/panda-* "$STAGE/drivers/"
chmod 755 "$STAGE"/drivers/panda-*

# Update both supported layouts. Fail immediately on any copy error. Copy only
# shipped files; keep custom helpers and omit Python bytecode/cache directories.
for prefix in "$INSTALL_HOME/.local" "$RELEASE_PREFIX"; do
    for dir in adapters extensions/voice drivers; do
        mkdir -p "$prefix/$dir"
        for src in "$STAGE/$dir/"*; do
            tmp="$(mktemp "$prefix/$dir/.panda.XXXXXX")"
            if ! cp -p "$src" "$tmp" || ! mv -f "$tmp" "$prefix/$dir/$(basename "$src")"; then
                rm -f "$tmp"
                exit 1
            fi
        done
    done
done
mv -f "$STAGE/panda" "$DEST_BIN"
if [ "$DEST_BIN" != "$PATH_BIN/panda" ]; then
    # Replace the PATH link by rename too, so it never points at a partial file.
    ln -s "$DEST_BIN" "$STAGE/path-link"
    mv -f "$STAGE/path-link" "$PATH_BIN/panda"
fi
echo "installed $DEST_BIN (adapters, voice helpers and drivers synced)"
echo "Existing processes keep their loaded binary; restart them when idle."
