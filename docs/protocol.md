# P2P 总线协议（internal/bus）

> 本文从 `internal/bus` 包提炼，描述节点间任务委派的传输与线协议。
> 以代码为准（`msg.go` / `payloads.go` / `ws.go` / `udp.go` / `auth.go` / `ed25519.go` /
> `bundle.go`）；与代码冲突时以代码为准。

## 传输层

有两条数据面，跑的是**同一套信封**（见下）：

- **WebSocket**（`gorilla/websocket`），端点路径 `/ws`。`ws://` 本身不加密，
  但出站有明文门禁（`cleartextOK`）：`ws://` 只允许拨向 loopback /
  Tailscale 形态的目标（`100.64.0.0/10`、`fd7a:115e:a214::/48`、`*.ts.net`，
  即链路已被下层加密或出不了本机），其余目标必须 `wss://` 或走 `punch:`
  UDP 端点；`network.allow_cleartext` 是运维侧的显式退出开关。
- **UDP 数据报平面**（`udp.go`，NAT 场景）。TCP/WebSocket 会话建立不了的
  链路靠它打通和兜底。每个数据报是四种帧之一（4 字节头区分）：
  - `kind 1 punch` / `kind 2 punch_ack`：JSON `PunchFrame`，HMAC 签名
    （`punch|<nonce>|<from>|<ts>`）证明 mesh 成员身份，负责开 NAT 洞；
  - `kind 3 data`：`nonce(12) || AES-256-GCM(JSON Envelope)`，AAD 为帧头——
    密钥由共享密钥域分离派生，所以 UDP 平面上信封**全程加密**；
  - `kind 4 keepalive`：密封空体，保活 NAT 映射。
  同一 socket 还应答 STUN binding 请求（RFC 5389，按 magic cookie 识别）：
  反射地址发现零外部依赖。
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

## 认证：hello 的双重签名

`hello` 携带两层身份：

- **成员资格（HMAC）**：`sig` 是 `HMAC-SHA256(secret, nodeID + ":" + ts + ":" + nonce)`
  的十六进制（`HelloSigN`）。`ts` 绑定时间戳限定重放窗口；`nonce` 是每拨号随机值，
  让同一秒内的两次合法重连不会被接收端的单次性重放缓存误判。无 nonce 的旧对端
  按双字段形式校验（`HelloSig`，仅兼容）。
- **密码学身份（Ed25519）**：`pub_key` 是节点持久化身份公钥（hex），`ed_sig` 是对
  `NodeID:Ts:Nonce` 的 Ed25519 签名。HMAC 证明「你持有 mesh 密钥」，Ed25519 证明
  「你是这个 node id 的密钥本体」——二者缺一不可：光有有效 EdSig 而无共享密钥一律
  拒绝，持有 `pub_key` 却签不出 EdSig 同样拒绝（防身份冒称）。

校验规则（`VerifyHelloP`）：

- `ts` 距当前时间超过 `maxHelloAge = 5 * time.Minute`（过去或未来）→ 失败。
- 共享密钥为空或签名为空 → **恒失败（fail-closed）**：没有共享密钥的节点不得为任何对端背书。
- `sig` 按 nonce 形态（有 `nonce` 用 `HelloSigN`，无则按旧双字段 `HelloSig`）
  恒定时间比较；不符 → 失败。接收侧另以签名覆盖字段建单次性重放缓存。
- `pub_key`/`ed_sig` 都不带 → 按无身份旧对端放行（仅成员资格）；只带一半
  或签不出来 → 按伪造/剥离处理，失败而非降级。

`5 min` 窗口既容忍 P2P 时钟漂移，又能让抓到的旧 `hello` 过期。经认证的 Ed25519
身份随后成为 outbox 稳定托管键（`k:<pub>`）与 `panda nodes verify` 指纹钉住的对象。

## 消息类型

| `type` | 负载结构 | 说明 |
|---|---|---|
| `hello` | `HelloPayload` | 连接时声明身份：`card` 摘要 + HMAC `sig`（绑 `ts`/`nonce`）+ Ed25519 `pub_key`/`ed_sig` + `udp_port` |
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
| `task_resume` | `TaskResumePayload` | 对「停在审批」的重新放行（可带澄清答案 `answer`） |
| `context_fetch` | `ContextFetchPayload` | 向源节点要完整上下文快照 |
| `context_ack` | `ContextAckPayload` | `context_fetch` 的应答 |
| `artifact_fetch` | `ArtifactFetchPayload` | 按 offset 拉一段工件 |
| `artifact_chunk` | `ArtifactChunkPayload` | `artifact_fetch` 的应答（一段数据） |
| `artifact_push` | `ArtifactPushPayload` | 主动推一段工件（DTN/断连场景，可续传） |
| `artifact_push_status` | `ArtifactPushStatusPayload` | 接收方托管回报：`received_through` 连续字节数 |
| `artifact_push_done` | `ArtifactPushDonePayload` | 推送收尾：按哈希校验入池（`ok`）或失败停推 |
| `agent_negotiate` | `AgentNegotiatePayload` | 对等冲突协商信号（§5.1，TargetScope + 权重） |
| `agent_grant` | `AgentGrantPayload` | 租约锁授权/拒绝（§5.2，`denied` 区分显式拒绝与无应答） |
| `agent_yield` | `AgentYieldPayload` | agent 在检查点让出执行 |
| `dtn_bundle` | `DTNBundlePayload` | 一个 CBOR 编码的 DTN bundle 原样透传（见下） |
| `punch_offer` | `PunchOfferPayload` | NAT 打洞协调：会话 nonce + 候选端点 + TTL |
| `punch_ready` | `PunchOfferPayload` | 应答 offer：回显 nonce，附己方候选端点 |

## 关键帧细节

### `hello`
`node_id`、`ver`（版本）、`card`（能力摘要的紧凑 JSON，原始负载）、`ts`、`nonce`、
`sig`、`pub_key`、`ed_sig`（Ed25519 身份对，见认证节）、`udp_port`（数据报平面
监听端口，0 = 无 UDP 面）。能力摘要刻意以原始 JSON 透传，让传输层与持有
`CapabilitySummary` 类型的 ledger 包解耦。hello **应答**额外带 `you`——对端
观察到本连接来源 IP，是白送的反射地址发现（不签名：篡改它只换来错误的候选
列表，打洞握手本身会识破）。

### `heartbeat`
`status`（`online`/`busy`/`offline`/`draining`——维护排空态，入站委派被谢绝）、
`load`（0.0–1.0）、`capacity`（卡里的原始 JSON，内含实测容量 `live`：
`mem_free`/`disk_free`/`gpu_util`，以及 `queued_tasks` 队列深度）、可选 `card`。
心跳每几秒一次、`hello` 只在拨号时——节点热重载能力卡后，
靠心跳里的 `card` 让对端立刻学到新能力，而不必等重连。旧节点不认识该字段就忽略，
新节点没收到就回退用 `hello` 时的卡。此外每拍还顺带：

- `blocked_agents`：本机熔断中的 agent 名单，对端路由时从能力集剔除；
- `neighbors` + `links`：邻接表与链路度量（`peer`/`rtt_ms`/`kind` 传输类型），
  让链路状态图的边集与权重跟随活拓扑——加权最短路（Dijkstra 取第一跳）靠它喂；
- `contacts`：接触计划（预约传输窗口），供 DTN 托管路由规划；新节点恒发
  （空数组 = 无计划），字段缺席才表示旧节点；
- `projects`：本机持有 checkout 的项目名（驻留路由打分的输入）；
- `ver`：本机版本号，mid-release 升级不必等重连就刷新对端目录行
  （fleet 面板的版本错位告警即拿它与自身构建对比）。

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
- **授权** `authorized` + `auth_sig`/`auth_pub`/`auth_ts`：原始用户的 tier-2
  同意（§16）以 Ed25519 签名形式随任务传递——`auth_sig` 是对
  `TaskID:Authorized:TS:ConsentDigest` 的签名，执行端按目录中登记的发源公钥验签，
  剥离/篡改/换钥重签一律拒绝；`auth_hops` 记录同意随中继前进的跳数。无有效
  同意钳制的远端 agent 运行被压成只读工具面（adapter 表达不了就直接
  `ErrNotAuthorized`）。裸 `authorized` 布尔只是无密钥旧节点的兼容形。
- **项目随行**：`project`/`title` 标识归属项目；`project_pack`/`project_dir`
  内联项目记忆与元数据，`context_type=file` 的仓库任务再把工作树打成
  `__worktree__` artifact 输入（≤256 MiB，跳过 `.git`/`node_modules`/缓存目录），
  执行方在私有 per-task 目录解开、跑完打包收回——委派的文件改动能落回
  发起节点的 checkout 而不是死在远端。
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

### 工件推送：`artifact_push` 三帧
拉取的镜像：**主动推**，给 DTN/断连场景用——对方未必能发起 `artifact_fetch`。
`artifact_push` 逐段携带 `offset`/`data`/`total`；接收方回 `artifact_push_status`
报告 `received_through`（从 0 起连续字节数），发送侧 outbox 只按这个覆盖数退役
行——发完即崩也能在下次 flush 重传未确认段，不留永久缺口；`artifact_push_done`
收尾：收齐并按内容哈希校验入池（`ok`），或对端拒收/已持有（`reason`）。

### DTN bundle：`dtn_bundle`
`blob` 是一个 CBOR 编码 bundle 的原样字节（§8.3）。接收方解包、验签、查 TTL，
然后把内层负载回灌正常消息路径——bundle 是签名的传输容器，不是第二套任务
协议。v1 明文载荷仅兼容验证，v2+ 载荷 AES-256-GCM 密封；`DestEID` 用稳定身份
键（`k:<pub>`），经目录解析到当前实例。

### NAT 打洞协调：`punch_offer` / `punch_ready`
走信封（有直连走直连，没有就沿链路状态图经 mesh 中继，TTL 自 `PunchMaxTTL=8`
逐跳递减），协调两个 NAT 后节点的 UDP 打洞会话：`nonce` 是打洞数据报里的
会话凭证，`src` 是真源节点 id（中继会以自己重打包信封，`env.From` 只是上一跳），
`hosts` 是发起方全部候选端点（本地接口 + `hello.you` 观测到的反射 IP + STUN
结果，统一在 UDP 监听端口——打洞永远从共享 socket 喷出，NAT 开的洞就是监听
口）。`punch_ready` 回显 nonce 并附己方候选，双方同时开喷。`network.peers` 里
`punch:<node-id>` 形态的条目触发这条路径；打通的端点成为 `sendTo` 的回退路由。
