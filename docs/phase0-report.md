# Phase 0 完成报告 · PANDA

> 阶段：Phase 0 · 本地任务闭环
> 日期：2026-08-12
> 状态：✅ 开发完成，待决策门评估

---

## 1. 交付物概览

| 交付物 | 产出 | 状态 |
|---|---|---|
| Go module + 骨架 | `go.mod`, `cmd/panda/main.go`, 目录结构 | ✅ |
| 配置加载 | `internal/config/`, YAML + env 覆盖 | ✅ |
| 结构化日志 | `internal/log/`, slog JSON + level 过滤 | ✅ |
| SQLite 初始化 + 迁移 | `internal/storage/`, WAL + 7 张表 | ✅ |
| UUIDv7 | `internal/util/uuid.go` | ✅ |
| 能力目录 | `internal/ledger/`, capabilities.yaml 解析 + CRUD | ✅ |
| 节点生命周期 | `internal/core/node.go`, 注册/心跳/下线 | ✅ |
| 任务状态机 | `internal/core/state.go` + `tasks.go`, 10 态 + 事件 + 幂等 + 级联取消 | ✅ |
| WebSocket 通信 | `internal/bus/`, server/client + 信封协议 | ✅ |
| Core 路由 | `internal/core/core.go` + `handlers.go` | ✅ |
| Native 执行器 | `internal/commander/native.go` | ✅ |
| Agent adapter | `adapters/claude_code.py` | ✅ |
| 超时监控 + 崩溃恢复 | `internal/core/` monitor + recover | ✅ |
| 端到端测试 | `internal/core/e2e_test.go` | ✅ |

## 2. 验收标准对照

| 验收项 | 结果 | 证据 |
|---|---|---|
| 入口节点启动，Go 核心内存 < 8MB | ⚠️ **19 MB**（见 §4） | release 构建实测 |
| 香橙派节点启动 | ⏳ 待部署验证 | 二进制已交叉编译为静态 ARM64 |
| 两个节点互相可见 | ✅ | hello 双向握手 + peer 注册 |
| delegate→accept→running→result→done | ✅ | `TestTwoNodeProtocol` |
| 重复 UUIDv7 消息不产生重复任务 | ✅ | `TestDelegateIdempotent` |
| 旧 attempt 结果不覆盖新 attempt | ✅ | 状态机 attempt_id 校验 + `TestAttemptRotation` |
| task_cancel → 子任务级联取消 | ✅ | `TestCancelPropagates` + 单机级联测试 |
| task_decline → 调度器收到原因 | ✅ | decline 处理路径 |
| 任务超时 → 自动标记 failed | ✅ | `TestTaskTimeoutFails` |
| 节点断线 → 心跳超时检测 | ✅ | pong 看门狗 + 心跳超时 |
| 节点重启 → 从 SQLite WAL 恢复 | ✅ | `TestRecoverRestoresState` |
| native 命令执行并回传 | ✅ | `TestExecuteNative` + E2E |
| agent adapter 执行 | ✅ | adapter 协议测试（真实调用待 API key）|
| native 命令不调用任何模型 | ✅ | 架构保证：exec.Command 直跑 |

## 3. 测试统计

| 包 | 覆盖率 | 说明 |
|---|---|---|
| `internal/util` | 95.7% | UUIDv7 |
| `internal/commander` | 66.7% | 路由 + native 执行 + adapter 桥 |
| `internal/core` | 61.3% | 状态机 + 协议 + E2E |
| `internal/bus` | 20.3% | WS 序列化（server/client 需真连接）|
| `go vet` | ✅ 无告警 | |

## 4. 关键发现：内存基线偏离

**目标**：Phase 0 验收「Go 核心空载 < 8MB」。

**实测**：release 构建（`-ldflags "-s -w"`）运行中 RSS ≈ **19 MB**。

**根因**：`modernc.org/sqlite`（纯 Go SQLite 驱动）。设计文档 §4.1 选择了它以保证 `GOOS=linux GOARCH=arm64 go build` 一行交叉编译（无 C 工具链）。但纯 Go 驱动的运行时基线显著高于 CGO 驱动。

**影响评估**：
- 香橙派 2GB RAM：19MB 依然非常充裕（设计文档 §5.2 的 Micro 峰值 ~30MB 仍可实现，安全净空 >1.5GB）
- 但决策门「内存 < 8MB」不达标

**选项**（需决策）：
1. **接受 ~19MB，调整验收标准**：香橙派 2GB 完全够用，保留交叉编译零依赖优势
2. **换 CGO 驱动（mattn/go-sqlite3）**：内存降到 ~8-10MB，但破坏一行交叉编译，违背 §4.1 核心决策；需在每台目标设备装 C 工具链或用 Docker 交叉编译
3. **运行时按需驱动**：Mac/桌面用 CGO 驱动，香橙派用 modernc —— 增加构建矩阵复杂度

**我的建议**：选项 1。Phase 0 的核心价值是「任务闭环可靠」，19MB 对目标硬件无实际压力；8MB 目标是估算值，应基于实测调整而非为数字牺牲架构优势。

## 5. Phase 0 决策门

| 决策门 | 状态 | 备注 |
|---|---|---|
| 现有架构是否需要调整？ | 否 | Go + WS + SQLite 架构验证成立 |
| Go 核心内存基线达标（<8MB）？ | ⚠️ 需调整 | 19MB，见 §4 |
| 任务闭环延迟可接受？ | ✅ | 非执行开销 < 100ms（loopback 实测）|
| SQLite WAL 崩溃恢复可靠？ | ✅ | 重启恢复测试通过 |
| 是否进入 Phase 1？ | ✅ 建议 | 需先解决内存基线决策 |

## 6. 遗留与 Phase 1 输入

- **Agent adapter 真实调用**：claude_code.py 协议已验证，真实执行需 Anthropic API key 且 Mac 上已装 Claude Code CLI
- **香橙派实际部署**：`bin/panda-linux-arm64` 已交叉编译为静态二进制，需在 Armbian 上实测内存和心跳
- **Tailscale 组网**：本机未装，Phase 0 用 loopback 验证了协议；跨设备联调需组网
- **bus 包覆盖**：E2E 覆盖了真连接路径，但包级覆盖率统计低，Phase 1 可补充连接测试
- **跨平台命令**：测试卡片中的命令（uname/sleep）跨平台可用，产品卡片需按目标设备定制

---

*Phase 0 完成 · 2026-08-12 · 下一步：决策门评估后进入 Phase 1 统一入口模型*
