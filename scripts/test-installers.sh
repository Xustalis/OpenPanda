#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# test-installers.sh — exercise scripts/install.sh the way a user does.
#
# Why: nothing in CI ever ran the installers. installers.yml only *builds* the
# release archives and checks their checksums, so a broken installer shipped
# green and was discovered by whoever ran it first. Every platform-specific
# branch in the installer (os/arch mapping, rc-file persistence, service
# registration, checksum refusal) was therefore untested.
#
# The release layout is staged locally and served over file:// URLs, so this
# needs no published release and no network:
#
#   OPENPANDA_RELEASE_BASE  → file://$WORK/dist
#   OPENPANDA_RELEASE_API   → file://$WORK/dist/latest.json
#
# That also means it runs on Windows and macOS runners, not just Linux. What it
# deliberately does not cover is the HTTP transport itself (redirects, the
# 60 req/h API fallback); those were verified by hand against a real release.
#
# Usage: scripts/test-installers.sh
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="0.0.0-test"

case "$(uname -s)" in
    Darwin) HOST_OS=darwin ;;
    Linux)  HOST_OS=linux ;;
    *) echo "test-installers: unsupported host $(uname -s); run scripts/test-installers.ps1 on Windows" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64|amd64) HOST_ARCH=amd64 ;;
    arm64|aarch64) HOST_ARCH=arm64 ;;
    *) echo "test-installers: unsupported arch $(uname -m)" >&2; exit 1 ;;
esac
TARGET="$HOST_OS-$HOST_ARCH"
ARCHIVE="panda-$VERSION-$TARGET.tar.gz"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# HOME redirects service files, but launchctl/systemctl still control the real
# logged-in user's service manager. Never activate or unload their services
# from an installer fixture. Exercise service-file generation with recorders.
SERVICE_TOOLS="$WORK/service-tools"
mkdir -p "$SERVICE_TOOLS"
for manager in launchctl systemctl; do
    cat > "$SERVICE_TOOLS/$manager" <<'MANAGER'
#!/bin/sh
printf '%s\n' "$0 $*" >> "$HOME/service-activation.log"
exit 0
MANAGER
    chmod +x "$SERVICE_TOOLS/$manager"
done
TEST_PATH="$SERVICE_TOOLS:${PATH:-/usr/bin:/bin}"

pass=0
fail=0
skip=0
ok()   { pass=$((pass + 1)); printf '  ok    %s\n' "$1"; }
bad()  { fail=$((fail + 1)); printf '  FAIL  %s\n' "$1" >&2; }
skipped() { skip=$((skip + 1)); printf '  skip  %s\n' "$1"; }

# ── Stage a release tree ────────────────────────────────────────────────────
echo "→ packaging $TARGET as v$VERSION"
rm -rf "$WORK/dist"
if ! OPENPANDA_DIST_DIR="$WORK/dist" OPENPANDA_PACKAGE_TARGETS="$TARGET" \
        sh scripts/package.sh "$VERSION" > "$WORK/package.log" 2>&1; then
    echo "test-installers: packaging failed:" >&2
    tail -20 "$WORK/package.log" >&2
    exit 1
fi
[ -f "$WORK/dist/$ARCHIVE" ] || { echo "test-installers: $ARCHIVE was not produced" >&2; exit 1; }

# The installer queries this when no version is pinned. The shape mirrors the
# GitHub API closely enough for the sed/ConvertFrom-Json parse.
printf '{"tag_name": "v%s"}\n' "$VERSION" > "$WORK/dist/latest.json"

# ── Isolated HOME ──────────────────────────────────────────────────────────
# Pre-create two rc files so PATH persistence has somewhere to land and can be
# checked; a fresh HOME would only exercise the ~/.profile fallback.
HOME_DIR="$WORK/home"
mkdir -p "$HOME_DIR"
: > "$HOME_DIR/.zshrc"
printf '# user content\n' > "$HOME_DIR/.bashrc"
PREFIX="$WORK/prefix"

run_installer() { # <extra args...>
    # env -i is deliberate, and do not "fix" it by adding the usual variables
    # back: the point is the minimal environment installers actually meet —
    # containers, CI runners, ssh sessions and systemd units — and a variable
    # this script happens to export is exactly the one an installer cannot
    # assume. It has already paid for itself: with USER absent, install.sh
    # aborted inside its own systemd warning path (`set -u`, "USER: parameter
    # not set") on the very branch that exists to warn and continue.
    env -i \
        PATH="$TEST_PATH" \
        HOME="$HOME_DIR" \
        OPENPANDA_RELEASE_BASE="file://$WORK/dist" \
        OPENPANDA_RELEASE_API="file://$WORK/dist/latest.json" \
        sh scripts/install.sh "$@"
}

# ── 1. Fresh install, resolving "latest" through the API stub ───────────────
echo "→ install (latest)"
if run_installer --no-service --prefix "$PREFIX" > "$WORK/run1.log" 2>&1; then
    ok "installer exited 0"
else
    bad "installer exited non-zero"
    tail -20 "$WORK/run1.log" >&2
fi

BIN="$PREFIX/bin/panda"
if [ -x "$BIN" ]; then
    ok "installed $BIN is executable"
else
    bad "$BIN missing or not executable"
fi

if got="$("$BIN" version 2>/dev/null)"; then
    case "$got" in
        *"$VERSION"*) ok "installed binary reports '$got'" ;;
        *) bad "installed binary reports '$got', expected version $VERSION" ;;
    esac
else
    bad "installed binary does not run"
fi

if [ -n "$(ls -A "$PREFIX/adapters" 2>/dev/null)" ]; then
    ok "adapters/ extracted"
else
    bad "adapters/ empty or missing"
fi

if [ -L "$HOME_DIR/.local/bin/panda" ]; then
    ok "PATH symlink created"
else
    bad "PATH symlink $HOME_DIR/.local/bin/panda missing"
fi

for rc in .zshrc .bashrc; do
    n="$(grep -c '^# >>> openpanda path >>>$' "$HOME_DIR/$rc" 2>/dev/null || true)"
    case "$n" in
        1) ok "$rc carries exactly one PATH block" ;;
        *) bad "$rc has $n PATH blocks, expected 1" ;;
    esac
    # The block must reference $PATH symbolically, not a snapshot of the
    # installer's environment: a frozen PATH would shadow anything a later rc
    # line prepends.
    if grep -q 'openpanda path' "$HOME_DIR/$rc" && ! grep -q 'export PATH=.*\$PATH' "$HOME_DIR/$rc"; then
        bad "$rc PATH block does not preserve \$PATH"
    fi
done

# ── 2. Re-run over an existing install (upgrade path) ──────────────────────
echo "→ re-install (idempotency)"
if run_installer --no-service --prefix "$PREFIX" > "$WORK/run2.log" 2>&1; then
    ok "second install exited 0"
else
    bad "second install exited non-zero"
    tail -20 "$WORK/run2.log" >&2
fi
for rc in .zshrc .bashrc; do
    n="$(grep -c '^# >>> openpanda path >>>$' "$HOME_DIR/$rc" 2>/dev/null || true)"
    [ "$n" = 1 ] || bad "$rc duplicated its PATH block on re-install ($n)"
done
# `if`, not `[ ... ] && ok`: a bare &&-chain that evaluates false is the last
# command of the statement and would trip `set -e`.
if [ -n "$("$BIN" version 2>/dev/null || true)" ]; then
    ok "binary still runs after re-install"
else
    bad "binary stopped working after re-install"
fi

# ── 3. Explicit --version (skips the API entirely) ──────────────────────────
echo "→ install (pinned version)"
if run_installer --no-service --prefix "$WORK/prefix-pinned" --version "$VERSION" > "$WORK/run3.log" 2>&1; then
    ok "pinned install exited 0"
else
    bad "pinned install exited non-zero"
    tail -20 "$WORK/run3.log" >&2
fi

# ── 4. A wrong checksum must abort the install ──────────────────────────────
# The whole point of shipping checksums.txt is that a corrupted or substituted
# archive never lands. If this path silently succeeded, the verification is
# decoration.
echo "→ install (tampered checksum, must refuse)"
rm -rf "$WORK/bad"
mkdir -p "$WORK/bad"
cp "$WORK/dist/$ARCHIVE" "$WORK/bad/$ARCHIVE"
printf '%064d  %s\n' 0 "$ARCHIVE" > "$WORK/bad/checksums.txt"
if env -i PATH="${PATH:-/usr/bin:/bin}" HOME="$HOME_DIR" \
        OPENPANDA_RELEASE_BASE="file://$WORK/bad" \
        sh scripts/install.sh --no-service --prefix "$WORK/prefix-bad" --version "$VERSION" \
        > "$WORK/run4.log" 2>&1; then
    bad "installer accepted a mismatched SHA-256"
else
    # The installer is bilingual (English default, zh under a zh locale);
    # env -i above leaves LANG unset, so this is the English form — match
    # either so the assertion survives a locale-detection change.
    if grep -qE 'SHA-256 (mismatch|校验失败)' "$WORK/run4.log"; then
        ok "installer refused the mismatched checksum"
    else
        bad "installer refused, but not for the checksum (see below)"
        tail -10 "$WORK/run4.log" >&2
    fi
fi

# ── 5. Unknown version must fail cleanly, not half-install ──────────────────
echo "→ install (missing release, must fail)"
if env -i PATH="${PATH:-/usr/bin:/bin}" HOME="$HOME_DIR" \
        OPENPANDA_RELEASE_BASE="file://$WORK/empty" \
        sh scripts/install.sh --no-service --prefix "$WORK/prefix-missing" --version 9.9.9 \
        > "$WORK/run5.log" 2>&1; then
    bad "installer reported success for a nonexistent release"
else
    ok "installer failed for a nonexistent release"
fi
if [ ! -e "$WORK/prefix-missing/bin/panda" ]; then
    ok "no binary left behind by the failed run"
else
    bad "a failed run left $WORK/prefix-missing/bin/panda behind"
fi

# ── 6. Auto-start registration must not abort the install ───────────────────
# The regression this guards: `systemctl --user` needs a user D-Bus session,
# which a headless server does not have. The old code ran it unguarded under
# `set -e`, so `--yes` on exactly the machines this node targets (pure SSH, no
# desktop session) exited non-zero *after* writing the unit file and *before*
# printing "安装完成" — a successful install reported as a failure. GitHub's
# ubuntu runner has systemctl but no user bus, so it reproduces that state.
echo "→ install (--yes, service registration)"
SERVICE_PREFIX="$WORK/prefix-service"
if run_installer --yes --prefix "$SERVICE_PREFIX" > "$WORK/run6.log" 2>&1; then
    ok "install with --yes exited 0"
else
    bad "install with --yes exited non-zero (service activation aborted it)"
    tail -20 "$WORK/run6.log" >&2
fi

if [ "$HOST_OS" = darwin ]; then
    unit="$HOME_DIR/Library/LaunchAgents/com.openpanda.node.plist"
    # The plist splits the argv across <string> elements.
    want="$SERVICE_PREFIX/bin/panda</string>"
else
    unit="$HOME_DIR/.config/systemd/user/openpanda.service"
    want="$SERVICE_PREFIX/bin/panda daemon"
fi
if [ -f "$unit" ]; then
    ok "service definition written to $(basename "$unit")"
else
    bad "service definition $unit missing"
fi
if [ -f "$unit" ] && grep -qF "$want" "$unit"; then
    ok "service definition points at the installed binary"
else
    bad "service definition does not reference $want"
fi
if [ -s "$HOME_DIR/service-activation.log" ]; then
    ok "service activation stayed inside the test recorder"
else
    bad "service manager recorder was not invoked"
fi

# ── 7. A release older than the data directory must be refused ──────────────
# The guard exists because installing a binary whose migration ceiling is below
# the database's user_version strands the data directory: the next launch dies
# with "schema version newer than this binary". It has to fire *before* the
# prefix is touched, and --force has to override it for a deliberate downgrade.
echo "→ install (schema downgrade, must refuse)"
if command -v sqlite3 >/dev/null 2>&1; then
    mkdir -p "$WORK/data" "$WORK/cfg"
    sqlite3 "$WORK/data/openpanda.db" "PRAGMA user_version=999;"
    printf 'storage:\n  db_path: "%s"\n' "$WORK/data/openpanda.db" > "$WORK/cfg/config.yaml"

    downgrade_install() { # <prefix> [extra args...]
        pfx="$1"; shift
        env -i PATH="${PATH:-/usr/bin:/bin}" HOME="$HOME_DIR" \
            OPENPANDA_CONFIG_PATH="$WORK/cfg/config.yaml" \
            OPENPANDA_RELEASE_BASE="file://$WORK/dist" \
            OPENPANDA_RELEASE_API="file://$WORK/dist/latest.json" \
            sh scripts/install.sh --no-service --prefix "$pfx" --version "$VERSION" "$@"
    }

    if downgrade_install "$WORK/prefix-downgrade" > "$WORK/run7.log" 2>&1; then
        bad "installer accepted a downgrade over a newer data directory"
    elif grep -qE 'refusing to install|拒绝安装' "$WORK/run7.log"; then
        ok "installer refused the downgrade"
    else
        bad "installer refused, but not for the schema (see below)"
        tail -10 "$WORK/run7.log" >&2
    fi
    if [ ! -e "$WORK/prefix-downgrade/bin/panda" ]; then
        ok "refused downgrade left no binary behind"
    else
        bad "a refused downgrade still wrote $WORK/prefix-downgrade/bin/panda"
    fi
    if downgrade_install "$WORK/prefix-downgrade" --force > "$WORK/run7b.log" 2>&1; then
        ok "--force overrode the schema guard"
    else
        bad "--force did not override the schema guard"
        tail -10 "$WORK/run7b.log" >&2
    fi
else
    skipped "schema guard (sqlite3 not available to stage a newer database)"
fi

# ── 8. A release signed by a foreign key must be refused ────────────────────
# SHA-256 alone cannot catch a substituted checksums.txt (it travels with the
# archive); the Ed25519 signature is what the self-updater trusts. The installer
# verifies it wherever the local OpenSSL can (pkeyutl -rawin). Stock macOS
# LibreSSL cannot, and degrades to a warning by design — that is a skip, not a
# pass, so a host that silently stopped verifying cannot look green here.
echo "→ install (foreign signature, must refuse)"
mkdir -p "$WORK/badsig"
cp "$WORK/dist/$ARCHIVE" "$WORK/badsig/$ARCHIVE"
cp "$WORK/dist/checksums.txt" "$WORK/badsig/checksums.txt"
head -c 64 /dev/zero | openssl base64 -A > "$WORK/badsig/checksums.txt.sig"
if env -i PATH="${PATH:-/usr/bin:/bin}" HOME="$HOME_DIR" \
        OPENPANDA_RELEASE_BASE="file://$WORK/badsig" \
        sh scripts/install.sh --no-service --prefix "$WORK/prefix-badsig" --version "$VERSION" \
        > "$WORK/run8.log" 2>&1; then
    if grep -q 'cannot verify Ed25519' "$WORK/run8.log"; then
        skipped "release signature (this host's openssl has no Ed25519)"
    else
        bad "installer accepted a checksums.txt signed by another key"
    fi
elif grep -q 'does not verify' "$WORK/run8.log"; then
    ok "installer refused the foreign signature"
else
    bad "installer failed, but not on the signature (see below)"
    tail -10 "$WORK/run8.log" >&2
fi

# ── 8b. A release with no signature installs, but says so ───────────────────
# Forks and mirrors stage releases without a signing key. That has to stay
# installable — SHA-256 still passed — but it must not be silent, and
# OPENPANDA_REQUIRE_SIG=1 must turn it into a refusal.
echo "→ install (no checksums.txt.sig)"
mkdir -p "$WORK/nosig"
cp "$WORK/dist/$ARCHIVE" "$WORK/nosig/$ARCHIVE"
cp "$WORK/dist/checksums.txt" "$WORK/nosig/checksums.txt"
if env -i PATH="${PATH:-/usr/bin:/bin}" HOME="$HOME_DIR" \
        OPENPANDA_RELEASE_BASE="file://$WORK/nosig" \
        sh scripts/install.sh --no-service --prefix "$WORK/prefix-nosig" --version "$VERSION" \
        > "$WORK/run8b.log" 2>&1; then
    if grep -q 'ships no checksums.txt.sig' "$WORK/run8b.log"; then
        ok "unsigned release installed with a warning"
    else
        bad "unsigned release installed without saying the signature was missing"
    fi
else
    bad "installer refused a release that merely lacks a signature"
    tail -10 "$WORK/run8b.log" >&2
fi
if env -i PATH="${PATH:-/usr/bin:/bin}" HOME="$HOME_DIR" \
        OPENPANDA_RELEASE_BASE="file://$WORK/nosig" OPENPANDA_REQUIRE_SIG=1 \
        sh scripts/install.sh --no-service --prefix "$WORK/prefix-nosig2" --version "$VERSION" \
        > "$WORK/run8c.log" 2>&1; then
    bad "OPENPANDA_REQUIRE_SIG accepted an unsigned release"
else
    ok "OPENPANDA_REQUIRE_SIG refused the unsigned release"
fi

# ── 9. --lite target selection ──────────────────────────────────────────────
# Lite ships Linux targets only; the flag has to be refused on any other host
# instead of resolving an archive that does not exist. On a Linux host the real
# lite install runs end to end, since that is the only place the binary is
# executable.
echo "→ install (--lite)"
if [ "$HOST_OS" = linux ]; then
    lite_target="lite-linux-$HOST_ARCH"
    if OPENPANDA_DIST_DIR="$WORK/dist" OPENPANDA_PACKAGE_TARGETS="$lite_target" \
            sh scripts/package.sh "$VERSION" > "$WORK/package-lite.log" 2>&1; then
        if env -i PATH="${PATH:-/usr/bin:/bin}" HOME="$HOME_DIR" \
                OPENPANDA_RELEASE_BASE="file://$WORK/dist" \
                sh scripts/install.sh --no-service --lite --prefix "$WORK/prefix-lite" --version "$VERSION" \
                > "$WORK/run9.log" 2>&1; then
            if "$WORK/prefix-lite/bin/panda" version 2>/dev/null | grep -q "$VERSION"; then
                ok "--lite installed a working binary"
            else
                bad "--lite installed a binary that does not run"
            fi
        else
            bad "--lite install failed"
            tail -10 "$WORK/run9.log" >&2
        fi
    else
        bad "packaging $lite_target failed"
        tail -10 "$WORK/package-lite.log" >&2
    fi
else
    if env -i PATH="${PATH:-/usr/bin:/bin}" HOME="$HOME_DIR" \
            OPENPANDA_RELEASE_BASE="file://$WORK/dist" \
            sh scripts/install.sh --no-service --lite --prefix "$WORK/prefix-lite" --version "$VERSION" \
            > "$WORK/run9.log" 2>&1; then
        bad "--lite was accepted on $HOST_OS, which ships no lite target"
    elif grep -qE 'lite build currently ships Linux targets only|lite 构建目前只发布 Linux 目标' "$WORK/run9.log"; then
        ok "--lite refused on $HOST_OS"
    else
        bad "--lite failed, but not for the platform reason (see below)"
        tail -10 "$WORK/run9.log" >&2
    fi
fi

# ── Summary ─────────────────────────────────────────────────────────────────
echo
if [ "$fail" -ne 0 ]; then
    echo "test-installers: FAILED ($fail failed, $pass passed, $skip skipped)"
    exit 1
fi
echo "test-installers: OK ($pass checks passed on $TARGET, $skip skipped)"
