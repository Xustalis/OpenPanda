#!/usr/bin/env bash
# 一键部署 OpenPanda 到香橙派（PI_HOST/PI_USER 可覆盖）。
#
# 用法（在 Mac / 开发机上执行）:
#   PI_HOST=<pi 的 IP 或主机名> ./scripts/deploy-pi.sh
#   PI_HOST=orangepi.local PI_USER=pi ./scripts/deploy-pi.sh
#
# 前置: Pi 的 sshd 可达（若未开启，先在 Pi 本机终端执行:
#   sudo systemctl enable --now ssh）
#
# 步骤:
#   1. 交叉编译 linux-arm64
#   2. 上传二进制（原子替换 ~/openpanda/panda 与 ~/.local/bin/panda）
#   3. 安装 systemd 服务（unit 文件经 scp 落盘，避免远端 heredoc 嵌套）
#      —— 开机自启、崩溃自动拉起
#   4. 顺带确保 sshd 开机自启
#   5. 健康检查: 服务 active + 7836 监听 + 版本号
set -euo pipefail

# PI_HOST 没有默认值，且必须由调用方给出: 这里曾经硬编码一台开发机的
# 局域网地址，于是任何人跑这个脚本都会去部署别人网段里的某台机器 —— 要么
# 超时，要么打到一台不相干的主机上。没有 PI_HOST 就直接退出。
HOST="${PI_HOST:-}"
if [ -z "$HOST" ]; then
  echo "用法: PI_HOST=<pi 的 IP 或主机名> [PI_USER=<登录用户>] $0" >&2
  exit 2
fi
USER_="${PI_USER:-$(id -un)}"
TARGET="${USER_}@${HOST}"
REMOTE_DIR="/home/${USER_}/openpanda"
HERE="$(cd "$(dirname "$0")/.." && pwd)"

echo "==> 交叉编译 linux-arm64"
(cd "$HERE" && make build-linux-arm64)

echo "==> 上传二进制与 unit 文件"
scp -q "$HERE/bin/panda-linux-arm64" "$TARGET:/tmp/panda.new"
scp -q "$HERE/testdata/run/openpanda.service" "$TARGET:/tmp/openpanda.service"

echo "==> 替换二进制并安装 systemd 服务"
# 注意: 用 pgrep -x 精确匹配进程名；pkill -f 'panda daemon' 会匹配到
# 这条 ssh 命令自身的命令行，把远端 shell 一起杀掉导致连接中断。
ssh "$TARGET" "
  set -e
  sudo -n systemctl stop openpanda 2>/dev/null || true
  PIDS=\$(pgrep -x panda || true)
  [ -n \"\$PIDS\" ] && kill \$PIDS || true
  sleep 1
  install -m 755 /tmp/panda.new $REMOTE_DIR/panda
  install -m 755 /tmp/panda.new /home/${USER_}/.local/bin/panda
  rm -f /tmp/panda.new
  sudo -n install -m 644 /tmp/openpanda.service /etc/systemd/system/openpanda.service
  rm -f /tmp/openpanda.service
  sudo -n systemctl daemon-reload
  sudo -n systemctl enable --now openpanda
  sudo -n systemctl restart openpanda
  sudo -n systemctl enable ssh 2>/dev/null || true
"

echo "==> 健康检查"
sleep 3
ssh "$TARGET" "
  systemctl is-active openpanda
  /home/${USER_}/.local/bin/panda --version
  ss -ltn | grep -q ':7836' && echo 'listen :7836 OK' || echo 'listen :7836 MISSING'
  journalctl -u openpanda -n 3 --no-pager -o cat
"
echo "==> 完成。"
