#!/usr/bin/env bash
# 部署 OpenPanda 到嵌入式 Linux 节点（示例脚本，按设备调整）。
# 用法: ./scripts/deploy-opi.sh [host]
# 步骤: 交叉编译 → scp 二进制/能力卡/配置 → 安装 systemd 服务 → 重启
set -euo pipefail

HOST="${1:-node-arm64-0}"
REMOTE_DIR="${REMOTE_DIR:-/opt/openpanda}"
REMOTE_USER="${REMOTE_USER:-openpanda}"
TARGET="${REMOTE_USER}@${HOST}"

echo "==> 交叉编译 linux-arm64"
make build-linux-arm64

echo "==> 停止服务（释放正在运行的二进制）"
ssh "$TARGET" "sudo -n systemctl stop openpanda 2>/dev/null || true"

echo "==> 上传到 $TARGET:$REMOTE_DIR"
ssh "$TARGET" "sudo -n mkdir -p $REMOTE_DIR/data $REMOTE_DIR/memory $REMOTE_DIR/projects $REMOTE_DIR/skills && sudo -n chown -R ${REMOTE_USER}:${REMOTE_USER} $REMOTE_DIR"
scp bin/panda-linux-arm64 "$TARGET:$REMOTE_DIR/panda"
scp config/capabilities.orangepi3b.yaml "$TARGET:$REMOTE_DIR/capabilities.yaml"
scp testdata/deploy-opi.yaml "$TARGET:$REMOTE_DIR/config.yaml"
ssh "$TARGET" "chmod +x $REMOTE_DIR/panda"

# 共享密钥（gitignored，来自 data/network-secret）经 systemd EnvironmentFile 注入，
# 不写入 config.yaml——后者由 testdata/deploy-opi.yaml 生成、会被 git 跟踪。
if [ -f data/network-secret ]; then
  printf 'OPENPANDA_SHARED_SECRET=%s\n' "$(cat data/network-secret)" > /tmp/openpanda.env
  scp /tmp/openpanda.env "$TARGET:$REMOTE_DIR/openpanda.env"
  ssh "$TARGET" "chmod 600 $REMOTE_DIR/openpanda.env"
  rm -f /tmp/openpanda.env
else
  echo "!! 缺少 data/network-secret；WS 监听将因无 shared_secret 而禁用" >&2
fi

echo "==> 安装 systemd 服务"
scp scripts/openpanda.service "$TARGET:/tmp/openpanda.service"
ssh "$TARGET" "sudo -n install -m 644 /tmp/openpanda.service /etc/systemd/system/openpanda.service && rm -f /tmp/openpanda.service"

echo "==> 启用并重启服务"
ssh "$TARGET" "sudo -n systemctl daemon-reload && sudo -n systemctl enable --now openpanda >/dev/null 2>&1; sudo -n systemctl restart openpanda"

echo "==> 完成。状态："
ssh "$TARGET" "systemctl is-active openpanda; journalctl -u openpanda -n 5 --no-pager"
