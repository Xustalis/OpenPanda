#!/bin/bash
# run-local.sh — 一键启动本地 PANDA（守护进程 + webui 侧车）
#
# 用法：
#   ./scripts/run-local.sh              # 使用 config.example.local.yaml
#   ./scripts/run-local.sh --config     # 使用当前目录的 config.yaml
#
# 特性：
#   - 自动构建缺失的二进制
#   - 自动生成 OPENPANDA_PANEL_TOKEN（如未设置）
#   - Ctrl+C 一次停止两个进程
#   - 默认绑定 loopback，安全用于本机开发

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

# 选择配置：优先 config.example.local.yaml，除非显式指定 --config
CONFIG="config.example.local.yaml"
if [[ "${1:-}" == "--config" ]]; then
    CONFIG="config.yaml"
fi

if [[ ! -f "$CONFIG" ]]; then
    echo "错误：找不到配置文件 $CONFIG" >&2
    echo "请复制 config.example.yaml 到 $CONFIG 并编辑。" >&2
    exit 1
fi

# 自动构建缺失的二进制
if [[ ! -x bin/panda ]]; then
    echo "[run-local] 正在构建 bin/panda ..."
    make build
fi
if [[ ! -x bin/panda-webui ]]; then
    echo "[run-local] 正在构建 bin/panda-webui ..."
    make build-webui
fi

# 自动生成本地共享密钥与面板令牌（仅用于本地开发）
if [[ -z "${OPENPANDA_SHARED_SECRET:-}" ]]; then
    OPENPANDA_SHARED_SECRET="local-$(openssl rand -hex 32 2>/dev/null || date +%s%N | sha256sum | head -c 64)"
    export OPENPANDA_SHARED_SECRET
    echo "[run-local] 已生成本地共享密钥：$OPENPANDA_SHARED_SECRET"
fi
if [[ -z "${OPENPANDA_PANEL_TOKEN:-}" ]]; then
    OPENPANDA_PANEL_TOKEN="local-$(openssl rand -hex 16 2>/dev/null || date +%s%N | sha256sum | head -c 32)"
    export OPENPANDA_PANEL_TOKEN
    echo "[run-local] 已生成本地面板令牌：$OPENPANDA_PANEL_TOKEN"
fi

# 模型 API 密钥检查（ask 子命令需要）
if [[ -z "${OPENPANDA_MODEL_API_KEY:-}" ]]; then
    echo "[run-local] 提示：未设置 OPENPANDA_MODEL_API_KEY，panda ask 将无法调用模型"
fi

# 确保数据目录存在
mkdir -p data context memory projects skills

# 清理函数：停止子进程（幂等，防止 INT/TERM/EXIT 重复触发）
CLEANING_UP=0
cleanup() {
    if [[ "$CLEANING_UP" -eq 1 ]]; then
        return
    fi
    CLEANING_UP=1
    echo
    echo "[run-local] 正在停止 PANDA ..."
    # 优先使用已记录的 PID 发送 SIGTERM
    if [[ -n "${DAEMON_PID:-}" ]] && kill -0 "$DAEMON_PID" 2>/dev/null; then
        kill -TERM "$DAEMON_PID" 2>/dev/null || true
    fi
    if [[ -n "${WEBUI_PID:-}" ]] && kill -0 "$WEBUI_PID" 2>/dev/null; then
        kill -TERM "$WEBUI_PID" 2>/dev/null || true
    fi
    # 等待子进程退出，最多 2 秒
    for _ in {1..4}; do
        if { [[ -z "${DAEMON_PID:-}" ]] || ! kill -0 "$DAEMON_PID" 2>/dev/null; } && \
           { [[ -z "${WEBUI_PID:-}" ]] || ! kill -0 "$WEBUI_PID" 2>/dev/null; }; then
            break
        fi
        sleep 0.5
    done
    echo "[run-local] 已停止"
}
trap cleanup INT TERM EXIT

# 启动守护进程
echo "[run-local] 启动核心守护进程 (config: $CONFIG) ..."
./bin/panda --config "$CONFIG" --card config/capabilities.example-desktop.yaml > /tmp/panda-core.log 2>&1 &
DAEMON_PID=$!

# 等待守护进程就绪（最多 5 秒）
for i in {1..10}; do
    if kill -0 "$DAEMON_PID" 2>/dev/null && grep -q "panda core started" /tmp/panda-core.log 2>/dev/null; then
        break
    fi
    sleep 0.5
done

if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
    echo "[run-local] 核心守护进程启动失败，日志：" >&2
    tail -30 /tmp/panda-core.log >&2
    exit 1
fi

# 启动 webui 侧车
echo "[run-local] 启动 webui 侧车 ..."
./bin/panda-webui --config "$CONFIG" > /tmp/panda-webui.log 2>&1 &
WEBUI_PID=$!

# 等待 webui 就绪（最多 5 秒）
for i in {1..10}; do
    if kill -0 "$WEBUI_PID" 2>/dev/null && curl -sf -o /dev/null http://127.0.0.1:7840/ 2>/dev/null; then
        break
    fi
    sleep 0.5
done

if ! kill -0 "$WEBUI_PID" 2>/dev/null; then
    echo "[run-local] webui 侧车启动失败，日志：" >&2
    tail -30 /tmp/panda-webui.log >&2
    exit 1
fi

echo
echo "==============================================="
echo "PANDA 本地服务已启动"
echo "==============================================="
echo "Web 面板:   http://127.0.0.1:7840"
echo "API 令牌:   $OPENPANDA_PANEL_TOKEN"
echo "核心日志:   /tmp/panda-core.log"
echo "WebUI 日志: /tmp/panda-webui.log"
echo "配置文件:   $ROOT_DIR/$CONFIG"
echo ""
echo "按 Ctrl+C 停止"
echo "==============================================="

# 等待任意子进程结束
wait
