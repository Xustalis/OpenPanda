#!/usr/bin/env bash
# 部署 PANDA 到香橙派（嵌入式节点）。
# 用法: ./scripts/deploy-opi.sh [host]
# 步骤: 交叉编译 → scp 二进制/能力卡/配置 → 安装 systemd 服务 → 重启
set -euo pipefail

HOST="${1:-orangepi}"
REMOTE_DIR="${REMOTE_DIR:-/home/xenith/panda}"

echo "==> 交叉编译 linux-arm64"
make build-linux-arm64

echo "==> 停止服务（释放正在运行的二进制）"
ssh "$HOST" "sudo -n systemctl stop panda 2>/dev/null || true"

echo "==> 上传到 $HOST:$REMOTE_DIR"
ssh "$HOST" "mkdir -p $REMOTE_DIR/data"
scp bin/panda-linux-arm64 "$HOST:$REMOTE_DIR/panda"
scp config/capabilities.orangepi3b.yaml "$HOST:$REMOTE_DIR/capabilities.yaml"
scp testdata/deploy-opi.yaml "$HOST:$REMOTE_DIR/config.yaml"
ssh "$HOST" "chmod +x $REMOTE_DIR/panda"

echo "==> 安装 systemd 服务"
scp scripts/panda.service "$HOST:/tmp/panda.service"
ssh "$HOST" "sudo -n install -m 644 /tmp/panda.service /etc/systemd/system/panda.service && rm -f /tmp/panda.service"

echo "==> 启用并重启服务"
ssh "$HOST" "sudo -n systemctl daemon-reload && sudo -n systemctl enable --now panda >/dev/null 2>&1; sudo -n systemctl restart panda"

echo "==> 完成。状态："
ssh "$HOST" "systemctl is-active panda; journalctl -u panda -n 5 --no-pager"
