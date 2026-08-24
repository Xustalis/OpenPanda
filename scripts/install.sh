#!/bin/sh
# OpenPanda one-click installer (macOS + Linux).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
#   sh install.sh --version 0.0.3            # pin a release
#   sh install.sh --prefix /opt/openpanda    # custom install dir
#   sh install.sh --yes                      # also register auto-start (no prompt)
#   sh install.sh --no-service               # never touch auto-start
#
# Installs the `panda` binary and its agent adapters (adapters/*.py) into
#   $OPENPANDA_PREFIX  (default: ${XDG_DATA_HOME:-~/.local/share}/openpanda)
# and symlinks the binary onto PATH at ~/.local/bin/panda. When run in a
# terminal it interactively asks whether to register an auto-start service
# (macOS LaunchAgent / Linux systemd --user) that runs `panda daemon` at login.
#
# See docs/install.md for the full guide and the Windows equivalent.

set -eu

# ── Render helpers ──────────────────────────────────────────────────────────
if [ -t 1 ]; then
    C_DIM="\033[2m"; C_GREEN="\033[32m"; C_YELLOW="\033[33m"; C_RED="\033[31m"; C_RESET="\033[0m"
else
    C_DIM=""; C_GREEN=""; C_YELLOW=""; C_RED=""; C_RESET=""
fi

info()  { printf '%b' "${C_DIM}[openpanda]${C_RESET} $1\n"; }
ok()    { printf '%b' "${C_GREEN}✓${C_RESET} $1\n"; }
warn()  { printf '%b' "${C_YELLOW}⚠${C_RESET} $1\n"; }
die()   { printf '%b' "${C_RED}✗${C_RESET} $1\n" >&2; exit 1; }

usage() {
    sed -n 's/^# \{0,1\}//p' "$0" | sed -n '/^Usage:/,/^$/p' | sed 's/^/  /'
    exit 0
}

# ── Args ────────────────────────────────────────────────────────────────────
VERSION="${OPENPANDA_VERSION:-latest}"
PREFIX="${OPENPANDA_PREFIX:-}"
SERVICE_MODE="ask"

while [ $# -gt 0 ]; do
    case "$1" in
        --version|-v) [ $# -gt 1 ] || die "--version 需要一个值"; VERSION="$2"; shift 2 ;;
        --prefix|-p)  [ $# -gt 1 ] || die "--prefix 需要一个值"; PREFIX="$2"; shift 2 ;;
        --yes|-y)     SERVICE_MODE="yes"; shift ;;
        --no-service) SERVICE_MODE="no"; shift  ;;
        --help|-h)    usage ;;
        *) die "未知参数: $1（用 --help 查看）" ;;
    esac
done

# ── Download helpers (curl preferred, wget fallback) ─────────────────────────
download() { # <url> <dest>
    if command -v curl >/dev/null 2>&1; then
        curl -fL --retry 3 --progress-bar -o "$2" "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "$2" "$1"
    else
        die "需要 curl 或 wget 才能下载安装包"
    fi
}

fetch_stdout() { # <url> → stdout
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1"
    else
        wget -q -O - "$1"
    fi
}

# ── SHA-256 across macOS (shasum) and Linux (sha256sum) ─────────────────────
sha256() { # <file> → lowercase hex
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    elif command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 "$1" | awk '{print $NF}'
    else
        die "未找到 SHA-256 校验工具（sha256sum / shasum / openssl）"
    fi
}

# ── Platform / arch detection ───────────────────────────────────────────────
OS="$(uname -s)"
ARCH="$(uname -m)"
case "$OS" in
    Darwin) OS=darwin ;;
    Linux)  OS=linux ;;
    *) die "不支持的系统: $OS（仅支持 macOS 与 Linux；Windows 请用 scripts/install.ps1）" ;;
esac
case "$ARCH" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "不支持的架构: $ARCH（仅提供 amd64 与 arm64）" ;;
esac

REPO="${OPENPANDA_REPO_URL:-https://github.com/Xustalis/OpenPanda}"
API="${OPENPANDA_RELEASE_API:-https://api.github.com/repos/Xustalis/OpenPanda/releases/latest}"

# ── Resolve version ─────────────────────────────────────────────────────────
if [ "$VERSION" = "latest" ]; then
    info "查询最新发行版…"
    tag="$(fetch_stdout "$API" \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
    [ -n "$tag" ] || die "无法解析最新版本号（网络/API 问题？也可用 --version 直接指定）"
    VERSION="${tag#v}"
    info "最新版本: v$VERSION"
else
    VERSION="${VERSION#v}"
fi

ARCHIVE="panda-$VERSION-$OS-$ARCH.tar.gz"
BASE="${OPENPANDA_RELEASE_BASE:-$REPO/releases/download/v$VERSION}"

# ── Install prefix (aligns with Go os.UserConfigDir / XDG) ───────────────────
if [ -z "$PREFIX" ]; then
    PREFIX="${XDG_DATA_HOME:-$HOME/.local/share}/openpanda"
fi
BINDIR="$PREFIX/bin"

info "安装 $ARCHIVE 到 $PREFIX …"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

download "$BASE/$ARCHIVE" "$WORK/$ARCHIVE" || die "下载失败: $BASE/$ARCHIVE"
download "$BASE/checksums.txt" "$WORK/checksums.txt" 2>/dev/null \
    || die "下载 checksums.txt 失败，拒绝在无法校验的情况下安装"

if [ -f "$WORK/checksums.txt" ]; then
    want="$(awk -v f="$ARCHIVE" '$2==f {print $1; exit}' "$WORK/checksums.txt")"
    [ -n "$want" ] || die "checksums.txt 缺少 $ARCHIVE 条目"
    got="$(sha256 "$WORK/$ARCHIVE")"
    if [ "$got" != "$want" ]; then
        die "SHA-256 校验失败（期望 $want，得到 $got）"
    fi
    ok "SHA-256 校验通过"
fi

# Unpack: the archive is a single top-level `openpanda/` directory holding
# bin/panda + adapters/*.py + example configs.
mkdir -p "$PREFIX"
tar -xzf "$WORK/$ARCHIVE" -C "$PREFIX" --strip-components=1
[ -x "$BINDIR/panda" ] || die "安装包缺少可执行的 $BINDIR/panda"

# ── Symlink onto PATH ───────────────────────────────────────────────────────
LINK_DIR="$HOME/.local/bin"
mkdir -p "$LINK_DIR"
ln -sf "$BINDIR/panda" "$LINK_DIR/panda"
ok "已链接 $LINK_DIR/panda → $BINDIR/panda"

case ":$PATH:" in
    *":$LINK_DIR:"*) : ;;
    *) warn "$LINK_DIR 不在 PATH 中；请在 shell 配置里加入:\n     export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
esac

# ── Self-verify ─────────────────────────────────────────────────────────────
if "$LINK_DIR/panda" version >/dev/null 2>&1; then
    ok "自检通过: $("$LINK_DIR/panda" version)"
else
    die "自检失败：请运行 '$LINK_DIR/panda version' 查看原因"
fi

# ── Auto-start (LaunchAgent / systemd --user) ───────────────────────────────
[ -f "$PREFIX/config.yaml" ] && HAVE_CONFIG=1 || HAVE_CONFIG=0

register_service() {
    if [ "$OS" = darwin ]; then
        launch_agent="$HOME/Library/LaunchAgents/com.openpanda.node.plist"
        mkdir -p "$HOME/Library/LaunchAgents"
        cat > "$launch_agent" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>com.openpanda.node</string>
    <key>ProgramArguments</key>
    <array>
        <string>$BINDIR/panda</string>
        <string>daemon</string>
        <string>--config</string><string>$PREFIX/config.yaml</string>
        <string>--card</string><string>$PREFIX/capabilities.yaml</string>
    </array>
    <key>WorkingDirectory</key><string>$PREFIX</string>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>ProcessType</key><string>Background</string>
    <key>StandardOutPath</key><string>/tmp/openpanda-daemon.out.log</string>
    <key>StandardErrorPath</key><string>/tmp/openpanda-daemon.err.log</string>
</dict>
</plist>
EOF
        launchctl unload "$launch_agent" 2>/dev/null || true
        launchctl load "$launch_agent"
        ok "已注册登录自启（LaunchAgent）。手动控制：\n     launchctl unload ~/Library/LaunchAgents/com.openpanda.node.plist   # 停用\n     launchctl load   ~/Library/LaunchAgents/com.openpanda.node.plist   # 启用"
    else
        unit_dir="$HOME/.config/systemd/user"
        mkdir -p "$unit_dir"
        cat > "$unit_dir/openpanda.service" <<EOF
[Unit]
Description=OpenPanda node daemon (user)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BINDIR/panda daemon --config $PREFIX/config.yaml --card $PREFIX/capabilities.yaml
WorkingDirectory=$PREFIX
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF
        if command -v systemctl >/dev/null 2>&1; then
            systemctl --user daemon-reload
            systemctl --user enable --now openpanda.service
            ok "已注册登录自启（systemd --user）。手动控制：\n     systemctl --user disable --now openpanda.service   # 停用\n     systemctl --user enable  --now openpanda.service   # 启用"
        else
            warn "未找到 systemctl，已写入 $unit_dir/openpanda.service（请手动启用）"
        fi
    fi
    if [ "$HAVE_CONFIG" = 0 ]; then
        warn "尚未生成配置：请先运行 '$LINK_DIR/panda init'，否则自启的后台 daemon 会因缺 config.yaml 无法启动。"
    fi
}

ask_service() {
    printf '%b' "${C_DIM}[openpanda]${C_RESET} 是否注册开机自启服务（后台运行 panda daemon）？[y/N] "
    read -r ans
    case "$ans" in
        [yY][eE][sS]|[yY]) register_service ;;
        *) info "跳过开机自启（可稍后手动注册，见 docs/install.md）" ;;
    esac
}

case "$SERVICE_MODE" in
    yes) register_service ;;
    ask)
        if [ -t 0 ]; then ask_service; else info "非交互终端：跳过开机自启（可用 --yes 显式启用）"; fi
        ;;
    no) : ;;
esac

echo
ok "安装完成"
printf '%b' "${C_DIM}快速开始:${C_RESET}\n"
printf '%b' "      panda init      # 交互式生成配置与能力卡\n"
printf '%b' "      panda repl      # 进入交互命令行\n"
printf '%b' "      panda web       # 打开内嵌 Web 控制台（自动登录）\n"
printf '%b' "卸载：删除 $PREFIX 与 $LINK_DIR/panda（开机自启见上文停用命令）\n"
