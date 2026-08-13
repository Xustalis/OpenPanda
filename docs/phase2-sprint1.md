# Phase 2 · Sprint 2.1 完成报告 · PANDA

> 阶段：Phase 2 · P2P 逐边委派
> 日期：2026-08-13
> 状态：✅ 代码完成，待硬件/网络联调

---

## 1. 交付物概览

Sprint 2.1 打通「根节点发起 → 中间节点无能力则转发 → 叶子节点执行 → 结果沿委派链回传」的三节点委派闭环。这是**纯代码交付**，在 loopback 上用真实 WebSocket 验证；GPIO、Tailscale 组网、香橙派部署全部留到多机联调（Sprint 2.5）。

| 交付物 | 产出 | 状态 |
|---|---|---|
| 能力卡交换 | `hello` 握手带 `CapabilitySummary`，远程节点写入本地 `employee_cache` | ✅ |
| 路由决策包 | `internal/scheduler/`：`chain.go`（环路检测）+ `route.go`（local/forward/decline）| ✅ |
| 委派转发 + 结果回传 | `handleDelegate` 按 `Route` 决策本地执行 / 转发 / 拒绝；`handleResult/Accept/Decline` 沿链中继 | ✅ |
| 发起端 `Core.Submit` | 根调度器入口：本地执行或转发后阻塞等结果；`ask` 变短命调度器跨设备派活 | ✅ |
| 集成测试 | `TestThreeNodeForward` / `TestResultRelay` / `TestLoopGuard` | ✅ |

## 2. 核心机制

### 能力卡交换（设计 §2.1）

`hello` 握手新增 `card` 字段（`json.RawMessage`，总线层不 import ledger），携带 `CapabilitySummary`：`device` / `resource_class` / `scheduler_tier` / `native_ids` / `agent_caps` / `manual_ids` / `capacity`。收到对方卡片后 `ledger.UpsertRemote` 写入本地 `employee_cache`（标记 online，`scheduler_tier` 一并落库）；断线时 `MarkOffline` 使路由不再考虑该节点。

关键取舍：远程节点只存 **ID-only 能力**（无可执行命令），因为本节点从不直接执行远端命令，只转发——这保证了「模型输出绝不当 shell 命令」的设计原则在跨节点场景依然成立。

### 逐边委派（设计 §6.4 / §10.5）

每个节点独立决策「本地能干就干、不能干就查表转发」：

```
香橙派 → Mac: task_delegate        （香橙派本地无匹配，Mac 是子调度器）
Mac 本地决策: 干不了 → 查表 → Windows 匹配 → 直接发
Mac → Windows: task_delegate
Windows 执行 → task_result 沿链回传 → 香橙派
```

路由优先级（`scheduler.Route`，纯函数、无副作用）：**本地匹配 > 匹配 peer（id 最小）> 子调度器（tier>1，可继续路由）> decline**。子调度器回退是关键：中间节点自己干不了、也不认识能干的节点时，仍可把任务交给更高层的节点继续编排（tier：Full=10 / Standard=5 / Micro=1，Micro 是纯 worker 不中转）。

### 委派链与环路检测

`chain_json` 逐跳 append；`AppendChain` 发现节点已在链中则返回 `ErrLoop`，`handleDelegate` 回 `task_decline("delegation loop")`。父节点把任务退回 queued，可改路由重试。

### attempt_id 传播

根节点 mint 一次 `attempt_id` 随 wire 携带，下游 `AdoptAttempt` 采纳同一 id。这样沿链回传的结果不会被中间节点判为「过期」——没有这一步，每个 hop 各自 mint，结果会在中间节点被静默丢弃。

### 发起端 `Core.Submit` + `ask` 跨设备

`ask` 不再是「必本机执行」，而是「短命调度器」：加载 config → `DialPeer` 逐个连 peers → 交换能力卡 → `Core.Submit` 路由 → 本地执行或转发后阻塞等结果 → 打印退出。无 peers 配置时回退为本地执行（等价 Phase 1 行为），**不新增 IPC**——谁发命令谁就是根调度器。

## 3. 改动文件清单

新增：
- `internal/scheduler/chain.go` / `route.go` / `route_test.go`
- `internal/core/delegation_test.go`（三节点集成测试）

修改：
- `internal/bus/payloads.go`：`HelloPayload.Card`
- `internal/ledger/capability.go`：`CapabilitySummary` + `UpsertRemote` + `Node.Matches` + `Query`/`Node` 暴露 `SchedulerTier`
- `internal/ledger/ledger_test.go`：`UpsertRemote`/`Node.Matches` 单测
- `internal/core/core.go`：hello 带卡、断线 MarkOffline、`summary`/`helloCard`/`signalResult`
- `internal/core/handlers.go`：`handleDelegate` 路由决策 + 三处理器沿链中继 + decline 解锁等待者
- `internal/core/submit.go`：`Submit` + `createTask`/`runLocal` 抽取
- `internal/core/tasks.go`：`AdoptAttempt`
- `cmd/panda/ask.go`：`runAskTask` 改为拨号 + `Submit`

## 4. 测试统计

| 包 | 覆盖率 | 关键新增测试 |
|---|---|---|
| `internal/scheduler` | 100.0% | `AppendChain` 环路 / `Predecessor` / `Route` 本地优先、转发匹配 peer、子调度器回退、匹配优先于子调度器、跳过链上节点 |
| `internal/ledger` | 80.6% | `UpsertRemote` 往返 / `Node.Matches` 三层匹配 |
| `internal/core` | 63.9% | `TestThreeNodeForward` / `TestResultRelay` / `TestLoopGuard`（真实 WS，loopback）|
| `go vet` | ✅ 无告警 | |
| `gofmt -l` | ✅ 无输出 | |

## 5. 验收对照

| 验收项 | 结果 |
|---|---|
| 三节点 `opi3b→mac→windows`：mac 无匹配则转发，windows 执行，结果沿链回传，根节点 `done` 且 `result_json` 非空 | ✅ `TestThreeNodeForward` |
| 结果经中间节点中继，`attempt_id` 不丢 | ✅ `TestResultRelay` |
| 环路：chain 含自身时拒绝（`task_decline`）| ✅ `TestLoopGuard` |
| 能力卡在 hello 中交换、远程节点可路由 | ✅ 集成测试隐式覆盖 + `UpsertRemote` 单测 |
| 发起端 `ask` 可跨设备派活 | ✅ `Core.Submit` + `ask` 拨号路径（真实多机在 2.5 联调）|

## 6. 边界与后续

**本 Sprint 明确不做**（依赖真实多机 / 后续 Sprint）：
- P2-04 任务 transfer、P2-05 容量并行、P2-06 优先级评分 —— 设计明示 MVP 先做能力匹配，且都依赖真实多机容量数据。
- 上下文分级传输（2.2）、防御链/权限（2.3）、GPIO（2.4）—— 各自成 Sprint。

**遗留注意**：
- 断线重连：转发到离线 peer 的 `sendTo` 失败会回 `task_decline`，但「中间节点掉线后重连」的自动重拨与状态对账尚未做（`RunHeartbeat` 已有，reconnect loop 是 Phase 0 骨架，真实多机验证）。
- `Submit` 阻塞等待结果期间若收到 `task_cancel` 不会解锁等待者（依赖 ctx 超时），不在本 Sprint 的 CLI 主路径上。

---

*Sprint 2.1 完成 · 2026-08-13 · 下一步：2.2 上下文分级传输（纯代码：pointer/summary/full 打包 + LRU）*
