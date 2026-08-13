# Phase 2 · Sprint 2.2 完成报告 · PANDA

> 阶段：Phase 2 · 上下文分级传输
> 日期：2026-08-13
> 状态：✅ 代码完成，待硬件/网络联调

---

## 1. 交付物概览

Sprint 2.2 实现上下文分级传输的纯代码子集——「pointer/summary/full 打包 + LRU」。在 loopback 上用真实 WebSocket 验证了「指针命中零传输」与「指针未命中 → 拉取 → 校验 → 恢复执行」两条闭环。GPIO、Tailscale 组网、香橙派部署仍留到多机联调（Sprint 2.5）。

| 交付物 | 产出 | 状态 |
|---|---|---|
| 上下文存储 + LRU | `internal/ctxstore/`：`context` 表 hash→data KV，按资源类设容量上限（Micro=5 / Standard=50 / Full=无限）| ✅ |
| 打包原语 | `Pack` / `Hash`（SHA-256）+ `Snapshot` 序列化 | ✅ |
| wire 协议字段 | `task_delegate.context_level/context_data`、`context_fetch`、`context_ack`（含 `data`）| ✅ |
| core 接线 | `packContext` + `waiting_context` 状态 + `Resume` + fetch/ack 处理 + 结果回传 | ✅ |
| 集成测试 | `TestContextPointerHit` / `TestContextFetchMiss` / `TestPackContextLevels` | ✅ |

## 2. 核心机制

### 三级上下文（设计 §12.3）

任务上下文以三种级别在线上流转，按成本从低到高：

- **summary**（默认）：`intent`/`spec_json` 已在线路上，就是全部上下文，无快照传输。
- **pointer**（~64B）：`context_hash` 引用一个 SHA-256 快照。执行者若已缓存则零传输；未命中则从打包方 `chain[0]` 拉取。
- **full**：`context_data` 内联携带完整快照，落地即执行，无往返。

`packContext` 决定打包级别：显式 `ContextHash` 透传；`file` 类型 + 已知 `RepoPath` 自动打包为 pointer；其余为 summary。

### 存储与 LRU（设计 §12.4）

`context` 表以 `ctx_hash` 为主键存 `data_blob`，按 `last_access ASC, access_count ASC` 驱逐，容量由资源类决定——Micro 设备存储有限只留 5 份快照，Standard 留 50，Full 节点视为无限。

### 拉取 / 校验 / 恢复

执行者收到 pointer 且未命中时：`prepare`（入队→派发）→ `SetWaitingContext`（dispatched→waiting_context）→ 注册 `pendingCtx` → 向 `chain[0]` 发 `context_fetch`。收到 `context_ack` 后校验 `ctxstore.Hash(data) == hash`，落库，`Resume`（waiting_context→running）→ 执行 → 结果沿链回传。

关键正确性点：**拉取恢复路径必须像同步执行路径一样回传结果**——`handleContextAck` 复用 `run` 后经 `relayToParent` 把 `task_result`（或 `task_decline`）送回父节点，否则根调度器会永久阻塞。

## 3. 改动文件清单

新增：
- `internal/ctxstore/store.go` + `store_test.go`（Store / Pack / Hash / LRU）
- `internal/core/context.go`（packContext / sendContextFetch / handleContextFetch / handleContextAck / pendingContext）
- `internal/core/context_test.go`（三级打包 + 两条集成闭环）

修改：
- `internal/core/core.go`：`ctx *ctxstore.Store` + `pendingCtx` 注册表 + dispatch 分支
- `internal/core/handlers.go`：`handleLocalDelegate` 上下文解析分支；`run` 支持 Resume；`delegateDetail` 带 `ContextHash`
- `internal/core/submit.go`：`TaskInput` 增 `ContextHash/ContextLevel/RepoPath`；`createTask` 返回 hash+level
- `internal/core/state.go` / `tasks.go`：`Task.ContextHash`、`SetDetail`、`SetWaitingContext` / `Resume`
- `internal/bus/payloads.go`：`TaskDelegatePayload.ContextLevel/ContextData`、`ContextFetchPayload`、`ContextAckPayload`

## 4. 测试统计

| 包 | 覆盖率 | 关键新增测试 |
|---|---|---|
| `internal/ctxstore` | 83.9% | `Put`/`Get` 往返、原地 upsert、LRU 驱逐、无限不驱逐、`MaxEntriesForResourceClass`、`Pack` 确定性 |
| `internal/core` | 63.2% | `TestPackContextLevels`（summary/pointer/passthrough）、`TestContextPointerHit`（零传输）、`TestContextFetchMiss`（fetch→ack→resume→回传）|
| `go vet` | ✅ 无告警 | |
| `gofmt -l` | ✅ 无输出 | |

## 5. 验收对照

| 验收项 | 结果 |
|---|---|
| 指针命中：执行者已缓存快照，零传输直接执行 | ✅ `TestContextPointerHit`（断言 leaf 不进入 waiting_context）|
| 指针未命中：拉取 → SHA-256 校验 → 落库 → 恢复执行 → 结果回传根节点 | ✅ `TestContextFetchMiss`（断言 leaf 出现 waiting/resume 事件、缓存落库、根 done + 非空结果）|
| 三级打包决策正确 | ✅ `TestPackContextLevels` |
| LRU 按资源类设上限并驱逐 | ✅ ctxstore 单测 |

## 6. 边界与后续

**本 Sprint 明确不做**：
- 执行器**消费**快照：拉取的快照已存储并校验，但执行器（agent 读 repo 上下文）尚未消费它——这是后续 agent 适配 Sprint 的接入点（`packContext` 已预留 `RepoPath`，CLI 尚未接线）。
- 快照的增量传输 / 压缩 / 跨节点预取策略（设计 §12.5+）—— 各自成 Sprint 或依赖真实多机。
- 上下文权限与加密（防御链 §2.3）—— 上下文快照目前明文内联/传输，防御链 Sprint 统一处理。

**遗留注意**：
- `summary` 级别目前只是「不传快照」，`intent`/`spec` 之外若需结构化摘要（模型生成的一句话任务意图），尚未落地——入口模型已产出 `intent`，够 MVP 用。
- `context_fetch` 源固定为 `chain[0]`（打包方）。若打包方掉线，执行者 `ForceFail` 而非重试其他缓存副本；多副本 fetch 是后续优化。

---

*Sprint 2.2 完成 · 2026-08-13 · 下一步：2.3 防御链/权限（纯代码：熔断器/权限判定引擎）*
