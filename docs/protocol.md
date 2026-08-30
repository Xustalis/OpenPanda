# P2P 总线协议（internal/bus）

> 本文从 `internal/bus` 包提炼，描述节点间任务委派的 WebSocket 传输与线协议。
> 以代码为准（`msg.go` / `payloads.go` / `ws.go` / `auth.go`）；与代码冲突时以代码为准。

## 传输层

- 传输介质：WebSocket（`gorilla/websocket`），端点路径 `/ws`，纯 `ws://` **不加密**。
- 服务端只接受**不带 `Origin` 头**的握手（节点间 Go 客户端不发 Origin；浏览器会发），
  因此跨站页面无法连到节点的控制通道（PWA 走面板端口的 HTTP，不走这里）。
- 拨号端 `TLSClientConfig.InsecureSkipVerify = false`：`wss://` 必须证书有效。
- 连接限额：全局并发连接数 `max_connections` 与单 IP 并发连接数 `max_connections_per_ip`
  （0 = 不限），超限返回 `503`——慢连接 / 握手型 DoS 的第一道闸。
- 入站连接有一个**有界的握手窗口** `defaultHelloTimeout = 10s`：
  第一条消息必须是合法 `hello`，否则断开。

## 帧与尺寸

| 常量 | 值 | 含义 |
|---|---|---|
| `readLimit` | `4 << 20` = 4 MiB | 单条消息上限；超限接收端直接关连接 |
| `pongWait` | 60 s | 等待 pong 的超时（判对端死亡） |
| `pingPeriod` | 30 s | 发送 ping 的周期，须小于 `pongWait` |
| `writeWait` | 10 s | 单次写（数据或 ping）的最大阻塞时长 |
| `ArtifactChunkBytes` | `1 << 20` = 1 MiB | 单个 `artifact_chunk` 携带的负载大小 |
| `maxWireText` | `512 << 10` = 512 KiB | 结果帧里 `stdout`/`stderr` 单字段上限 |

1 MiB 的 `[]byte` 在 JSON 里按 base64 编码（约 4/3 膨胀），即约 1.4 MiB 上链，
连同信封也远低于 4 MiB 帧上限。

## 消息信封

每条消息是一个 JSON 信封（`Envelope`，设计文档 §10.3）：

```json
{
  "v": 1,
  "type": "task_delegate",
  "msg_id": "<uuidv7>",
  "from": "<node-id>",
  "to": "<node-id>",
  "ts": 1710000000,
  "payload": { }
}
```

- `v`：协议版本，当前恒为 `1`。
- `type`：路由键，取下面「消息类型」之一。
- `msg_id`：UUIDv7，接收端用它去重（幂等）。构建信封时必须非空。
- `from` / `to`：发送 / 目标节点；`to` 省略表示广播 / 逐边路由。
- `ts`：Unix 秒。
- `payload`：类型相关的负载（原始 JSON，由处理层按类型解码）。

构建信封时（`NewEnvelope`）：实现了 `wireClamper` 接口的负载会先 `clampForWire()`
裁剪超大字段（见下文结果帧）。在**构建点**统一裁剪是为了——一条跑了一小时的任务，
不能因为日志太长把帧撑爆、被接收端断连、连结果带链路一起丢掉。

## 认证：hello 的 HMAC 签名

`hello` 的 `sig` 是 `HMAC-SHA256(secret, nodeID + ":" + ts)` 的十六进制（`HelloSig`）。
把时间戳 `ts` 绑进签名，意味着一条被抓包的 `hello` 只在接收方的容忍窗口内有效，
过期即不能重放（设计 §16 / P0-1）。

校验规则（`VerifyHello`）：

- 共享密钥为空或签名为空 → **恒失败（fail-closed）**：没有共享密钥的节点不得为任何对端背书。
- `ts` 距当前时间超过 `maxHelloAge = 5 * time.Minute`（过去或未来）→ 失败。
- 否则用恒定时间比较 `hmac.Equal`。

`5 min` 窗口既容忍 P2P 时钟漂移，又能让抓到的旧 `hello` 过期。

## 消息类型

| `type` | 负载结构 | 说明 |
|---|---|---|
| `hello` | `HelloPayload` | 连接时声明身份；带能力摘要 `card` 与 `sig` |
| `join` | — | 加入（常量定义在 `bus`，处理在核心层） |
| `heartbeat` | `HeartbeatPayload` | 状态 + 容量；可顺带最新能力卡 `card` |
| `task_delegate` | `TaskDelegatePayload` | 任务移交（核心帧，见下） |
| `task_accept` | `TaskAcceptPayload` | 接受任务 |
| `task_decline` | `TaskDeclinePayload` | 拒绝任务，附 `reason` |
| `task_progress` | `TaskProgressPayload` | 执行方心跳，按续租节拍上行 |
| `task_result` | `TaskResultPayload` | 完成结果（见下） |
| `task_retry` | — | 重试（常量在 `bus`） |
| `task_transfer` | — | 任务转移（常量在 `bus`） |
| `task_cancel` | `TaskCancelPayload` | 取消，附 `reason` |
| `task_resume` | `TaskResumePayload` | 对「停在审批」的重新放行 |
| `context_fetch` | `ContextFetchPayload` | 向源节点要完整上下文快照 |
| `context_ack` | `ContextAckPayload` | `context_fetch` 的应答 |
| `artifact_fetch` | `ArtifactFetchPayload` | 按 offset 拉一段工件 |
| `artifact_chunk` | `ArtifactChunkPayload` | `artifact_fetch` 的应答（一段数据） |

## 关键帧细节

### `hello`
`node_id`、`ver`（版本）、`card`（能力摘要的紧凑 JSON，原始负载）、`ts`、`sig`。
能力摘要刻意以原始 JSON 透传，让传输层与持有 `CapabilitySummary` 类型的 ledger 包解耦。

### `heartbeat`
`status`（`online`/`busy`/`offline`）、`load`（0.0–1.0）、`capacity`（卡里的原始 JSON），
可选 `card`。心跳每几秒一次、`hello` 只在拨号时——节点热重载能力卡后，
靠心跳里的 `card` 让对端立刻学到新能力，而不必等重连。旧节点不认识该字段就忽略，
新节点没收到就回退用 `hello` 时的卡。

### `task_delegate`
任务移交（§10.3 示例）。除 `task_id` / `intent` / `spec_json` / `requires` /
`preferred_node` / `chain`（防环链） / `timeout_ms` / `max_retries` / `complexity` /
`risk` / `attempt_id` 外，还有三类要点：

- **上下文传递**（§12.4）由 `context_level` 决定执行方如何拿到完整上下文：
  - `pointer`：`context_hash` 指向一份执行方可能已有的快照；缺失时向源节点拉取。
  - `summary`：线上的 intent/spec 即全部上下文，不传快照。
  - `full`：`context_data` 内联完整快照（base64）。
- **硬件需求** `resource_json`：任务声明的 `entry.ResourceProfile`。它随移交传递，
  因为硬件需求是「工作」的属性、不是「首个节点」的属性——中继节点重新路由时，
  得能把训练任务挡在没有显存的节点外，没有这个字段约束会在第一跳丢失。
- **授权** `authorized`：原始用户的 tier-2 同意（§16），让委派任务不在执行方的
  防御层被拦。它只有在**已认证**的总线上才有意义——正是共享密钥的 HMAC
  让它对非对端不可伪造，且源节点只在用户显式授权后才置位。
- **计划面**（v0.0.6）：`plan_id` / `stage_id` 标识该阶段供编排者审计，
  `inputs`（`ArtifactRef` 列表）声明每个前置阶段打包产物及其所在节点，
  执行方据此拉取起始树。独立任务为空。

### `task_result`
`task_id` / `attempt_id` / `state`（执行方持久化的状态，旧节点可省略；
缺失时由 `ok` 推导 done/failed，新节点须保留 `review` 以免被父节点误升为 done）/
`ok` / `exit_code` / `stdout` / `stderr` / `artifacts` / `tokens` / `cost` /
`output_artifact`（该阶段打包产物的哈希，编排者记下来交给后继阶段作输入）。

`stdout`/`stderr` 受 `maxWireText`（512 KiB）裁剪：子进程单流最多能产 8 MiB，
而帧上限 4 MiB，若不裁剪结果就永远落不了地。裁剪保留**头尾**（开头是意图、
结尾是结论），在 rune 边界切分以维持合法 UTF-8，并插一行
「中间已截断，完整输出留在执行节点」。完整输出留在执行节点，按需以工件方式带走。

### 工件帧：`artifact_fetch` / `artifact_chunk`
固定大小分块的**拉取**：需要产物的一方按 offset 一段段要，
上一段落地才要下一段，掉块 / 坏块是「重新请求」而非整次失败。

- `artifact_fetch`：`task_id` / `hash` / `offset`。
- `artifact_chunk`：`offset` / `data`（base64）/ `total`（全档大小，供进度并拒绝
  中途变长的流）/ `eof`（最后一段；收齐后按 `hash` 校验才允许入池）/ `ok` /
  `reason`（对端不持有或拒供时 `ok = false`，请求方改问别的节点而非无限重试）。
