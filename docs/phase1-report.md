# Phase 1 完成报告 · PANDA

> 阶段：Phase 1 · 单层委派 + 统一入口模型
> 日期：2026-08-13
> 状态：✅ 开发完成

---

## 1. 交付物概览

Phase 1 打通「用户输入 → 统一入口模型 → 本地任务落地 → 三层能力执行 → 持久化」的单节点闭环，并把入口模型产出的结构化字段（intent/spec/context_type/complexity/risk/resource）完整落库，补齐 Phase 0 遗留的 schema 缺口。

| 交付物 | 产出 | 状态 |
|---|---|---|
| 统一入口模型 | `internal/entry/`：prompt + model + router + spec 校验 + 降级 | ✅ |
| DeepSeek provider | `internal/config/` model 段 + `internal/entry/model.go` Anthropic 兼容调用 | ✅ |
| 任务落地本地执行管线 | `internal/core/submit.go`：`SubmitLocal` + `TaskDetail` + `SetDetail` | ✅ |
| 委派/本地共享执行管线 | `internal/core/handlers.go`：`routeDelegated` → 抽取 `execute` | ✅ |
| 入口 → 执行接线 | `cmd/panda/ask.go`：`task` 分支走 `SubmitLocal`（不再只打印 JSON）| ✅ |
| CLI 面板 | `cmd/panda/panel.go`：`status` / `queue` / `task` / `cancel` / `logs` | ✅ |
| 第二个 adapter | `adapters/opencode.py` + 卡片注册 + 路由测试 | ✅ |

## 2. 统一入口模型（设计 §7）

一次模型调用把用户输入分类为三种 `kind`：

- **answer**：自然语言直接回复，无副作用。
- **tool_call**：受控工具调用（校验 → 白名单 → 授权 → 执行，MVP 仅打印）。
- **task**：结构化任务，落入本地执行管线。

模型只产出意图，**副作用全部由 Go 核心执行**——模型输出绝不直接当作 shell 命令（设计 §7 核心原则）。

`panda ask` 端到端路径：`entry.Classify` → `ParseOutput`（非 JSON 降级为 answer，永不静默失败）→ `task` 分支 `toTaskInput` 转 `core.TaskInput` → `SubmitLocal` 落库并执行。

## 3. 代码债清偿

Phase 1 开发中识别并清偿了以下 Phase 0 遗留债务：

| 债务 | 清偿 |
|---|---|
| **`.gitignore` 的 `panda` 模式误匹配整个 `cmd/panda/` 源码目录，导致 CLI 源码从未被 git 跟踪** | 锚定为 `/panda`（只匹配仓库根目录的编译产物；实际二进制在 `bin/`，已由 `bin/` 行忽略）|
| `tasks` 表 schema 有 `context_type`/`complexity`/`risk`/`resource_json` 列，但 `Task` struct 与查询从未暴露、从未写入 | `Task` struct 暴露全字段；`Get`/`Children`/`ListByState`/`scanTasks` 统一经 `taskColumns` 常量查询；新增 `TaskDetail` + `SetDetail` |
| `intent`/`spec_json` 字段虽在 struct 中但**从未写入** | `SetDetail` 一并写入；委派路径 `delegateDetail` 把 wire payload 的 detail 落库 |
| 委派与本地将来需要的执行逻辑会重复 | 从 `routeDelegated` 抽取共享 `execute`，两条路径复用同一实现 |
| `routeDelegated` 接受无用的 `chain` 参数 | 移除 |
| `RotateAttempt` 接受无用的 `owner` 参数、UPDATE 不过滤 owner，破坏所有权不变量 | UPDATE 加 `owner_node=?` 守卫，非 owner 返回 `ErrConflict` |
| `CancelCascade` 计数把「已终态」的任务也算作「已取消」 | 只统计实际转换的任务数 |

> 未清偿（有意为之，非债）：`context_hash` 与 `model_tier` 两列是 Phase 2/3 特性（上下文传输、模型档位选择）的预留，MVP 不填充，`TaskDetail` 注释已说明。

## 4. 测试统计

| 包 | 覆盖率 | 关键新增测试 |
|---|---|---|
| `internal/core` | 62.1% | `TestSubmitLocalRunsNative` / `TestSubmitLocalNoCapability` / `TestSetDetailRoundTrip` / `TestDelegatePersistsDetail` / `TestCancelCascadeSkipsTerminalRoot` / `TestRotateAttemptOwnerGuarded` |
| `internal/commander` | 67.5% | `TestRouteSecondAgent`（OpenCode 路由）|
| `internal/ledger` | 77.3% | `TestLoadCardWithAgents` |
| `cmd/panda` | 5.9% | `TestToTaskInput`（intent 合成）|
| `internal/entry` | 66.7% | 入口模型三分类 + 校验 + 降级 |
| `go vet` | ✅ 无告警 | |
| `gofmt -l` | ✅ 无输出 | |

## 5. 验收对照

| 验收项 | 结果 |
|---|---|
| 统一入口模型三分类可用 | ✅ `panda ask` 端到端调用 DeepSeek |
| task 输出落地本地执行（不再只打印 JSON）| ✅ `SubmitLocal` → native/agent 执行 → 持久化 |
| 入口模型结构化字段完整落库 | ✅ context_type/intent/spec/complexity/risk/resource 全写入 |
| 委派与本地共享同一执行管线 | ✅ `execute` 单一实现 |
| CLI 面板可查任务/状态/日志/取消 | ✅ `queue`/`task`/`logs`/`status`/`cancel` 冒烟通过 |
| 第二个 adapter（OpenCode）注册并可路由 | ✅ `opencode.py` + 卡片 + `TestRouteSecondAgent` |

## 6. 遗留与 Phase 2 输入

- **跨节点自动路由**：Phase 0 `hello` 握手不交换能力卡，`employee_cache` 是每节点本地表。Phase 1 明确为**单节点闭环**；跨节点委派需 Phase 2 的 employee 表 + P2P 边委派。
- **tool_call 执行**：MVP 仅打印校验后的 tool JSON；工具白名单/授权/执行链路未接（Phase 3 记忆/语音/安全阶段）。
- **香橙派部署**：`bin/panda-linux-arm64` 静态二进制已就绪，待 Armbian 实测。
- **agent adapter 真实调用**：opencode.py 协议镜像 claude_code.py，但真实 CLI 调用需在装有 opencode 的环境验证。
- **意图精炼**：MVP 里「意图精炼」即同一次调用的 spec 字段（设计 §7.4）；后续可拆独立精炼步骤。

---

*Phase 1 完成 · 2026-08-13 · 下一步：Phase 2 多级 P2P 委派 + 上下文传输 + 防御*
