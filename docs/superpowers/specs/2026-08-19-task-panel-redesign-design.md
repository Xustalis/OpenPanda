# 任务面板重构与资源感知队列调度 设计文档

日期：2026-08-19
状态：已确认（方案 A 功能 × 方案 C 架构）

## 1. 目标

1. 任务队列面板从「机器视角」改为「用户视角」：保留四列看板（todo/doing/review/done），卡片展示标题、状态、优先级、归属 agent；点击进入**关联会话**看思维链，而非任务内部字段。
2. 任务支持用户直接提交、拖拽排序、设置优先级。
3. 调度语义：默认 FIFO；任务声明资源键，资源无冲突可并行/插队，同资源冲突排队；并发上限受能力卡 MaxConcurrent 约束；用户拖拽可决定先后。
4. 接入 Codex CLI（与 claude code / opencode 同一 adapter 协议），并在面板中暴露已安装 agent 列表与连通性测试。
5. 分三波实施：① 任务模型+调度器+队列 UI+会话集成；② 会话思维链渲染 + codex + agent 可见性；③ 全视图设计语言统一。

## 2. 任务模型扩展

`core.Task` 与 `tasks` 表新增（迁移 V9，`addColumnIfMissingTx`，存量默认值保持行为不变）：

| 字段 | 类型 | 默认 | 语义 |
|---|---|---|---|
| priority | INTEGER | 1 | 0=high, 1=normal, 2=low |
| seq | INTEGER | 0 | 手动拖拽序，小者先；0=未手动排序 |
| session_id | TEXT | '' | 关联会话 ID |
| resource_keys_json | TEXT | '' | 资源键数组 JSON |
| scheduled | INTEGER | 0 | 1=由本地队列调度器接管（区分委派重路由任务） |

排序策略（确定性规则，不引入学习机制）：
`(seq>0 ? seq : +inf) ASC, priority ASC, created_at ASC`。

资源键约定：`project:<name>`（同项目串行）、`node:<id>`（设备控制）、
无显式键且无 project 的 agent 任务占用 `agent:*`（保守串行）。

## 3. 调度器（方案 C：独立模块 + 资源锁注册表）

新包 `internal/scheduler/queue`，不依赖 core，仅依赖注入接口：

- `ResourceRegistry`：resource→taskID 锁表。`TryAcquire(keys, taskID)` 全部无冲突才加锁；`Release(taskID)`。
- `QueueScheduler`：字段为 `Store`（列就绪任务/运行数/认领）、`Runner`（执行一个任务）、`MaxConcurrent`。
  tick 触发源：入队/完成/取消/拖拽事件 + 2s 兜底轮询。
  每 tick：取就绪任务 → 策略排序 → 依次 TryAcquire 成功且 running<MaxConcurrent 则启动 goroutine 执行；终态 Release 并唤醒。
- 多节点演进路径：换分布式锁实现与集群 Store，策略代码复用。

Core 侧：
- `Enqueue(ctx, TaskInput)`：createTask + SetDetail + 置 scheduled/priority/resource_keys + Queue，立即返回。
- 调度器认领 = `Dispatch(self→self)` + `Accept`，随后走现有 execute/retry/review 逻辑（重试循环保守提取为共用方法）。
- 现有 `Submit`/`SubmitLocal` 同步路径保留（CLI `panda ask` 依赖）。

## 4. API（webui/panel）

- `POST /api/tasks` `{title, prompt, priority?, project?, resource_keys?}`：建任务（Enqueue）+ 自动创建关联会话（title=任务标题，prompt 为首条用户消息）；返回 `{task_id, session_id}`。
- `PATCH /api/tasks/{id}` `{priority?}`。
- `POST /api/tasks/reorder` `{ids: [...]}`：按数组下标重写 seq（1..n）。
- `GET /api/agents`：探测 claude/opencode/codex CLI 的安装路径与版本。
- `POST /api/agents/{name}/test`：跑 `<cli> --version` 连通性检查。
- `GET /api/tasks` 响应扩展：priority / seq / session_id / resource_keys。

## 5. 会话集成与思维链

- 队列「新建任务」自动建会话；卡片点击 → `#/chat/{session_id}`（无会话则回退任务详情）。
- 会话中任务消息渲染为任务卡：历史回放 `GET /api/tasks/{id}/logs`（task_events），实时经现有 `/api/events` SSE 按 task_id 过滤；完成时固化结果摘要为 assistant turn。
- 对话中判定为「任务」的请求改为入队（面板会话路径），立即返回「任务已入队」+ 跳转入口。

## 6. Agent 接入

- `adapters/codex.py`：统一协议（stdin `{prompt, timeout_s, cwd}` → stdout `{ok, result, exit_code, tokens?, cost?}`），命令 `codex exec --json <prompt>`，cwd 传入工作目录；超时/缺二进制走与其它 adapter 一致的错误码。
- `detect.go`：codex 探测（capabilities: coding/shell/file_edit，Tier 2）。
- doctor：codex 列入 agent 检查清单。

## 7. 波次划分

1. 波次 1（本次）：迁移 V9、任务模型、queue 调度器、Core.Enqueue、Panel API、看板 UI（优先级/拖拽/新建/跳会话）。
2. 波次 2：会话内思维链渲染、codex adapter、agent 可见性设置页。
3. 波次 3：projects/nodes/skills/reminders/memory 视图设计语言统一。

## 8. 测试计划

- queue 包：策略排序、锁冲突/释放、MaxConcurrent 上限、唤醒。
- core：Enqueue → 调度执行 → 终态；同资源串行、异资源并行。
- panel API：创建任务+会话、PATCH 优先级、reorder。
- 前端：`make webui` 构建通过；手工验证看板拖拽与跳会话。
