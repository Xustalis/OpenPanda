# 设计稿 — 任务间消息板（Sibling Message Board）

> 状态：已实现（2026-10-13）。`internal/core/messages.go` 为核心实现，
> 迁移 v44 建表，wire 类型 `task_message`，agent 侧双协议
> （结构化 `messages` 字段 + `PANDA_MSG` marker），轮边界注入
> `[sibling board]` 段。`make gate` 全绿（fmt/vet/build/adapter-test/
> test/race-focused，2026-10-13）。引用代码以 `internal/` 现状为准；
> 与代码冲突时以代码为准。

## 0. 一句话

给同一执行域内的任务一个**异步短消息通道**：一个 plan 的兄弟 stage、
或一个父任务的子任务集，可以互发有界的小消息（一行状态、一个避免
重复挖掘的发现、一个产出文件的指针），在对方下一次监督轮次读到——
不必等终结态、不必整树传 artifact、不必让父任务中转。

## 1. 动机

现状的节点间/任务间通信全为 hub-and-spoke：

- **父→子→父**：delegate 子任务结果以 `[delegated child result]` 折进
  父任务下一轮 prompt（`internal/core/run.go`）。方向单一、以终结态为
  单位。
- **plan 阶段间**：`output_artifact` 整树快照 + 签名授权传递。粒度是
  文件树，重且粗。
- **ask 循环内**：entry 模型当中转站（`internal/askengine`）。

缺一个轻量层：并行的兄弟任务无法互告"我在哪、我发现了什么、别重复
挖"。长任务协同只剩两条路——要么塞进 artifact（贵），要么等父任务
下一轮转述（慢且串行化）。

参考论文（ATC-MARL，通信高效 MARL）给了三个工程原语的学术表述：
TMB（逐发送者槽位缓存、异步消费）、ALC（上下文相关的消息粒度）、
DCPS（逐边通信伙伴选择）。本设计**只移植其结构**，不引入学习组件：
消息是自然语言，发送决策者是 LLM 而非可微策略。

## 2. 映射：论文机制 → 工程实现

| ATC-MARL | 本设计 | 关键差异 |
|---|---|---|
| TMB：逐发送者一格、新覆盖旧、延迟折扣 | `task_messages` 按 `(scope, sender, rcpt)` UPSERT，新 seq 胜；读取渲染 `ago=` 时间戳 | 新旧权衡交给读者（LLM），不做注意力加权 |
| ALC：多级编码器按上下文选粒度 | 三档 grade：`status`(≤400B) / `report`(≤4096B) / `artifact_ref`(指针) | 决策者是 LLM；超限截断而非拒绝 |
| DCPS：逐边 sigmoid 门控 + 软惩罚 | 显式 `to:` 寻址（stage id / parent / siblings / children / task id）+ 每 episode 预算计费 | 枚举换成指定；软惩罚 = 超支拒发并折回提示 |
| CTDE：集中规划、去中心执行 | 本已同构：entry 模型集中拆解，agent 去中心执行 | — |

明确不做：MARL 训练、PPO/GAE、可微消息、通用 pub/sub 总线。

## 3. 结构

### 3.1 scope（可见域）

消息板严格限定两种 scope，不开放任意话题：

- `plan:<plan_id>` — 同一 plan 的 stage 互发
- `task:<parent_id>` — 同一父任务的子任务互发；父任务本身是成员
  （父→子用 `to:"children"` 或子任务 id，子→父用 `to:"parent"`）

一个任务的**可读**板 = 自身 scope + 自己的子任务板
（`BoardScopes`，父任务不阻塞地听孩子）。

### 3.2 槽位语义（TMB 工程版）

表 `task_messages`（迁移 v44）：

```sql
CREATE TABLE IF NOT EXISTS task_messages (
    scope        TEXT NOT NULL,
    sender       TEXT NOT NULL,
    rcpt         TEXT NOT NULL DEFAULT '',   -- '' = 广播；否则为收件任务 id
    sender_seq   INTEGER NOT NULL,
    sender_label TEXT NOT NULL DEFAULT '',   -- stage id 或标题，owner 端解析
    grade        TEXT NOT NULL,
    body         TEXT NOT NULL,
    sent_at      INTEGER NOT NULL,
    recv_at      INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    PRIMARY KEY (scope, sender, rcpt)
)
```

- **每发送者每收件人一格**：同一 sender 的新消息覆盖旧槽位
  （`sender_seq` 单调守卫，迟到旧拷贝不覆盖新）；高产的发送者不会
  把别人的槽位挤掉（FIFO 的经典病）。
- **容量天然有界**：一个 scope 的槽位数 ≤ 成员数（plan ≤ 64 stage）。
- **TTL 24h**：`expires_at` 过期即不可读；板属于活跃工作，陈年消息
  不得在同名 scope 复活。
- **sender_label**：owner 解析出的发送者显示名（stage id 或标题），
  随扇出拷贝一起落地——从未持有发送者行的副本节点也能渲染与寻址。
  注意 UUIDv7 同毫秒创建的任务（StartPlan 批量建 stage 就是如此）
  前 12 位相同，短 id 前缀不足以定位；标签与全 id 才是可靠地址。

### 3.3 寻址

| `to` | 语义 |
|---|---|
| `""` / `siblings` / `all` | 广播（scope 全体成员，除自己） |
| `parent` | 子任务板的父成员（仅 `task:` scope） |
| `children` | 父任务向自己的子任务集广播（仅自己 host 的板） |
| `stage:<id>` | plan scope 内按 stage id 寻址 |
| `<task id>` 或 ≥8 字符唯一前缀 | 按任务 id 寻址；歧义前缀拒绝而非猜测 |

板行渲染 `from=<sender_label>`（owner 解析的 stage id / 标题）；副本
本地无该行且无标签时回退为完整 task id——长，但总能精确寻址。

寻址在 **scope owner 端**解析：陌生人地址、歧义前缀、自寄自收一律
拒绝并在发送回执里说明（"message to X failed: ..."折进下一轮）。

### 3.4 分级与预算

- `status`：一行状态，正文 ≤400 字节
- `report`：结构化摘要，≤4096 字节
- `artifact_ref`：指向产出文件的指针（正文仍是文本）
- 超限**截断不拒发**（截断仍携带信号；拒发不教任何东西）
- 每任务 episode 发送预算 `messages.budget`（默认 8）：超支拒发并
  折回 `message to X not sent: budget spent`——计费式软抑制

## 4. 数据通路

```
agent 输出
  ├─ 结构化 messages 字段（schema-capable 适配器）
  └─ PANDA_MSG {"to":...,"grade":...,"body":...} 行（marker 降级）
        │
        ▼  parseMessages（先于 PANDA_DELEGATE 处理——delegation 会
        │   round-- 重驱，后处理会丢同输出的消息）
   sendAgentMessages（预算计费 → deliverTaskMessage）
        │
        ├─ 本地副本：先写自己节点的槽位
        ▼
   scope owner（plan → chain[0] 计划原点；task → 本地 spawn 即 self，
   wire 到达即 Predecessor，与结果回传同规则）
        │
        ▼  resolveMsgRcpt（owner 端成员解析）+ 按成员节点 fanout
   （扇出拷贝携带 owner 解析的 sender_label；发送端本地副本就地解析
     rcpt：parent / 具体 task id 可解，stage:<id> 留给 owner）
   成员节点 replica（校验 Member 确为本节点持有的 scope 成员）
        │
        ▼  drainTaskMessages（轮边界，增量：msgSeen 按槽位记 seq）
   currentIntent += "[sibling board] from=<stage> grade=... ago=...
        │
        ▼
   下一轮 agent 执行
```

要点：

- **异步**：不阻塞运行中的 agent。消息在监督轮边界消费，与
  `foldLateChildren`（迟到的子结果）同一位置、同一语义。
- **不丢消息**：发送解析先于 delegation 分支（`run.go` 注释标明）。
- **增量读取**：`msgSeen` 按 `scope|sender|rcpt` 记已折 seq；新 episode
  重读整板一次（恢复运行的正确追赶）。
- **跨节点**：`bus.MsgTaskMessage`（QoSSignal 车道），复用 HMAC 信封、
  link-state 路由、多跳转发；owner 不在本地时沿 delegation 链转发。
- **优雅降级**：老节点收到 `task_message` 走 default 丢弃；槽位模型
  使丢帧可由下一次发送自愈（无独立 outbox）。

## 5. 挂载点（代码索引）

| 位置 | 内容 |
|---|---|
| `internal/core/messages.go` | 板全部逻辑：scope/槽位/寻址/owner/fanout/drain/渲染/预算 |
| `internal/core/plan.go` | `PANDA_MSG` 解析（`parseMessages`）、`agentResultSchema` 的 `messages` 字段、`structuredAgentResult.Messages` |
| `internal/core/run.go` | 轮边界 drain 注入；发送解析先于 delegation；`prompt.msgboard.hint` 注入（仅当任务有 scope） |
| `internal/bus/msg.go` / `payloads.go` / `ws.go` | `MsgTaskMessage`、`TaskMessagePayload`、QoS 归类 |
| `internal/core/core.go` | 信封 dispatch case、`SetMessageBudget`/`msgBudget` |
| `internal/storage/migrations.go` | v44 建表 + 索引 |
| `internal/config/config.go` | `messages.budget` |
| `internal/i18n/messages.go` | `prompt.msgboard.hint` 五语言 |
| `cmd/panda/taskcmd.go` | `panda task board <id>` 查看板 |
| `cmd/panda/main.go` / `internal/askengine/askengine.go` | 配置接线 |

## 6. 安全与边界

- **接收端校验**：fanout 副本必须落在本节点确实持有的 scope 成员行上
  （伪造/误投丢弃）；成员→owner 腿的 sender 行必须存在。
- **scope 成员资格在 owner 端解析**：任务不能往别人的 plan/父子域
  说话（scope-drift 的边界检查）。
- **信任层级**：消息是 Tier 1（纯信息，不直接改世界），不进审批门；
  delegate（要资源）仍走原审批。
- **不改变现有语义**：plan artifact 传递、delegate 结果折叠、ask
  循环、租约/监督/回滚全部照旧；消息板只是叠加的协调层。
- **三重抑制**：正文截断（单条）、预算计费（episode）、TTL（时间）。

## 7. 测试与验证

- `internal/core/messages_test.go`：槽位覆盖/迟到拷贝/定向与广播/
  scope 隔离/TTL 过滤/seq 单调/marker 解析（含畸形保留、散文误报）/
  寻址（含前缀与歧义）/owner 判定（本地 spawn vs wire 到达）/
  drain 增量与定向过滤/预算/伪造副本丢弃。
- `make gate` 全绿（2026-10-13）：fmt、vet、build、adapter-test(52)、
  全量 test、race-focused（core/commander/bus/storage -race）。
- 手工：`panda task board <task-id>` 查看任意任务可读板；
  `--json` 输出结构化。

## 8. 未实现 / 后续

- **MCP 工具**（extended policy 下 agent 主动拉板/发消息，不等轮边界）
- **webui 板视图**（观察通信流量与陈旧度）
- **通信效率遥测**（消息数/陈旧率/预算命中率进 metrics）
- **跨节点 e2e 冒烟**（双节点真实互发；当前验证为单节点集成 + 单测）
- **scout→plan 两阶段协议**：先探测、拿报告再动态拆解——本消息板
  是它的通信基座，拆解协议本身仍待设计
