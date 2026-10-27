# P2P 总线协议（internal/bus）

> 本文从 `internal/bus` 包提炼，描述节点间任务委派的传输与线协议。
> 以代码为准（`msg.go` / `payloads.go` / `ws.go` / `udp.go` / `auth.go` / `ed25519.go` /
> `sessenc.go` / `bundle.go`）；与代码冲突时以代码为准。

## 传输层

有两条数据面，跑的是**同一套信封**（见下）：

- **WebSocket**（`gorilla/websocket`），端点路径 `/ws`。TCP 层的 `ws://`
  本身不加密，但 hello 交换完成后会按需武装**会话加密**（见下「会话
  AEAD」）；协商不出加密的链路才过明文门禁（`cleartextOK`，握手后判
  定）：`ws://` 只允许拨向 loopback / Tailscale 形态的目标
  （`100.64.0.0/10`、`fd7a:115e:a214::/48`、`*.ts.net`，即链路已被下层
  加密或出不了本机）或 `network.allow_cleartext_for` 列出的网段/主机，
  其余目标必须 `wss://` 或走 `punch:` UDP 端点；`network.allow_cleartext`
  是运维侧的显式退出开关。入站同理：验完 hello 的对端若武装不了
  sessaead，来源 IP 不在安全/放行集合内也会被断开。
- **UDP 数据报平面**（`udp.go`，NAT 场景）。TCP/WebSocket 会话建立不了的
  链路靠它打通和兜底。每个数据报是四种帧之一（4 字节头区分）：
  - `kind 1 punch` / `kind 2 punch_ack`：JSON `PunchFrame`，HMAC 签名
    （`punch|<nonce>|<from>|<ts>`）证明 mesh 成员身份，负责开 NAT 洞；
  - `kind 3 data`：`ver=2` 时为 `idlen(1) || 发送方 id || nonce(12) ||
    AES-256-GCM(JSON Envelope)`，密钥由双方 X25519 身份经 ECDH + HKDF 派生，
    每对端一把；发送方 id 明文充当密钥选择器，与帧头一并计入 AAD，信封的
    `from` 还要再与它对账，所以接收方证明的是「谁封的」而不只是「有成员封的」。
    `ver=1` 是由共享密钥派生的旧式，仅为未发布身份密钥的对端保留，且一旦
    来源地址属于已发布身份密钥的对端就一律丢弃；
  - `kind 4 keepalive`：密封空体，保活 NAT 映射，封法与 `kind 3` 相同。
  同一 socket 还应答 STUN binding 请求（RFC 5389，按 magic cookie 识别）：
  反射地址发现零外部依赖。
- 服务端只接受**不带 `Origin` 头**的握手（节点间 Go 客户端不发 Origin；浏览器会发），
  因此跨站页面无法连到节点的控制通道（PWA 走面板端口的 HTTP，不走这里）。
- 拨号端 `TLSClientConfig.InsecureSkipVerify = false`：`wss://` 必须证书有效。
- 连接限额：全局并发连接数 `max_connections` 与单 IP 并发连接数 `max_connections_per_ip`
  （0 = 不限），超限返回 `503`——慢连接 / 握手型 DoS 的第一道闸。
- 入站连接有一个**有界的握手窗口** `defaultHelloTimeout = 10s`：
  第一条消息必须是合法 `hello`，否则断开。未认证连接上除 `hello` 外唯一
  受理的帧是 `pair_hello`（LAN 配对入口，见下）。

## 会话 AEAD（sessaead）

`ws://` 之上的帧级加密，v0.0.11 起按能力协商（hello `caps` 宣告
`sessaead`，不解析版本号）：双方 hello 都带 nonce 且存在共享密钥时，
`SessionAEAD` 以 `HKDF-SHA256(secret, info=domain‖dialerID‖listenerID‖
dialerNonce‖listenerNonce)` 派生每连接 AES-256-GCM。此后每个应用帧密封为
`nonce(12) ‖ AEAD(tag ‖ payload)`，一律走 BinaryMessage；`tag` 区分密封内
的 JSON 文本帧（0x01）与数据帧（0x02）。nonce 逐拨号随机、密钥绑定两端
身份——抓包重放 hello 得不出会话密钥，拆 `caps` 降级明文只会撞回上面的
明文门禁（fail closed）。对端不支持 `sessaead` 时链路保持明文并交由明文
门禁裁决，旧版本互通不受影响。

## 二进制数据帧（binart）

hello `caps` 宣告 `binart` 的对端之间，承载批量字节的信封
（`artifact_chunk` / `artifact_push`）可以走 WS 二进制帧：2 字节头部长度
+ JSON 头 + 原始字节，省掉 base64 的 4/3 膨胀。发送侧先查 `caps` 再选编
码，混合版本链路退回 JSON 形态；二进制帧只在直连上使用（中继/出停靠
JSON 重打包）。

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
| `hello` | `HelloPayload` | 连接时声明身份：`card` 摘要 + HMAC `sig`（绑 `ts`/`nonce`）+ Ed25519 `pub_key`/`ed_sig` + X25519 `x_pub` + `udp_port` + `caps` + `authoritative` |
| `hello_reject` | `HelloRejectPayload` | 握手失败时服务端在断连前给出的唯一回执：`reason` ∈ `auth`/`identity`/`replay` |
| `join` | — | 加入（常量定义在 `bus`，处理在核心层） |
| `heartbeat` | `HeartbeatPayload` | 状态 + 容量；可顺带最新能力卡 `card` |
| `task_delegate` | `TaskDelegatePayload` | 任务移交（核心帧，见下） |
| `task_accept` | `TaskAcceptPayload` | 接受任务；新节点附 Ed25519 签名 `sig`/`pub`/`ts` |
| `task_decline` | `TaskDeclinePayload` | 拒绝任务，附 `reason`；可选机器可读 `reason_code` |
| `task_progress` | `TaskProgressPayload` | 执行方心跳，按续租节拍上行 |
| `task_result` | `TaskResultPayload` | 完成结果（见下） |
| `task_retry` | — | 重试（常量在 `bus`） |
| `task_transfer` | — | 任务转移（常量在 `bus`） |
| `task_cancel` | `TaskCancelPayload` | 取消，附 `reason` |
| `task_resume` | `TaskResumePayload` | 对「停在审批」的重新放行（可带澄清答案 `answer`；`accept` 置位时是把已接受处置镜像到对侧停驻副本，review→done，不携带任何再执行同意） |
| `task_message` | `TaskMessagePayload` | 任务间消息板的一条短消息（同 plan 的兄弟阶段、或同一父任务的子任务集，见下） |
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
| `transit` | `TransitPayload` | 盲中继帧（跨网段 mesh M3）：`dest`（稳定 `k:<pub>` 或实例 id）+ `ttl` + `seal`（`x25519`\|`mesh`）+ `inner` 密封 blob。中继只见外壳，逐跳转发；终点开封、验内层 Ed25519 认证后按直收分发 |
| `dir_pull` | `DirPullPayload` | 目录差量同步请求（跨网段 mesh M1）：请求方完整 `id→content_hash` 戳表 + 可选 `after` 续页游标。心跳 `dir_digest` 不匹配才发，30s/对端限速（续页豁免但有页数上限） |
| `dir_rows` | `DirRowsPayload` | pull 的应答：差量行数组（每行是源节点 Ed25519 签名的规范化 JSON，逐跳原样转发）+ `want`（应答方缺的 id，一次往返双向收敛）+ `cursor`/`more` 分页（双闸：256 行 ∧ 1 MiB 帧体） |
| `punch_offer` | `PunchOfferPayload` | NAT 打洞协调：会话 nonce + 候选端点 + TTL |
| `punch_ready` | `PunchOfferPayload` | 应答 offer：回显 nonce，附己方候选端点 |
| `punch_want` | `PunchWantPayload` | 自动打洞提示（跨网段 mesh M3）：发送方按字典序选举不是 offerer，请对方（较小 id 一侧）向自己发 `punch_offer`；`ttl` 约束中继跳数，与 offer 同套逐跳转发纪律 |
| `pair_hello` | `PairHelloPayload` | LAN 配对发起（未认证连接上除 hello 外唯一受理的帧）：会话 id + 临时 X25519 公钥 + Ed25519 身份 + nonce |
| `pair_ready` | `PairReadyPayload` | 配对应答：对端的 X25519/nonce/身份，两侧由此推出同一短验证码 |
| `pair_secret` | `PairSecretPayload` | 配对确认后投递共享密钥：AES-256-GCM 密封（HKDF(dh, nonces)），仅 responder→initiator |
| `pair_reject` | `PairRejectPayload` | 配对终止：`rejected`/`expired`/`busy` |

## 关键帧细节

### `hello`
`node_id`、`ver`（版本）、`card`（能力摘要的紧凑 JSON，原始负载）、`ts`、`nonce`、
`sig`、`pub_key`、`ed_sig`（Ed25519 身份对，见认证节）、`udp_port`（数据报平面
监听端口，0 = 无 UDP 面）、`caps`（可选线能力清单：`sessaead` 会话加密、
`binart` 二进制数据帧——不认识某项能力的对端只是不启用它）、
`authoritative`（声明发送进程持有节点身份锁，同 id 仲裁让真正的 daemon 从
借用引擎的兄弟进程手里收回对端边）、`x_pub`（X25519 身份公钥 hex——双钥
身份的另一半：Ed25519 签名、X25519 做 transit 密钥协商；仅在该 hello 的
EdSig 验过后才入目录 `x_pub` 列，旧对端不带此字段、发往它们的 transit 退化为
成员级 mesh 密封）、`prov`（构建出处：`release`|`self`|`unsigned`——
version.Provenance 戳，自声明且在签名域外，接收方白名单化后落目录行，
供 `accept.<tier>.self_signed` 信任策略权衡自建二进制的任务来源；
缺席 = 不认识该字段的旧节点）。能力摘要刻意以原始 JSON 透传，让传输层
与持有 `CapabilitySummary` 类型的 ledger 包解耦。hello **应答**额外带
`you`——对端观察到本连接来源 IP，是白送的反射地址发现（不签名：篡改它只
换来错误的候选列表，打洞握手本身会识破）。

hello 校验失败时服务端回 `hello_reject`（`reason`：`auth` 签名/密钥不符、
`identity` id 冲突或已钉住身份换钥、`replay` 签名被消费）再断开——这是
未认证通道上唯一的回执帧，不带任何从密钥派生的内容。

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
  （fleet 面板的版本错位告警即拿它与自身构建对比）；
- `prov`：构建出处（`release`|`self`|`unsigned`），同 hello 镜像——
  mid-life 换二进制（update apply、self-build）不等重连就刷新目录行；
- `dir_digest`：目录戳表 `id→content_hash`（剔除 session 行）排序后的
  SHA-256——M1 差量同步的扳机，见流行病节目；
- `edge_load`：本机当前活跃边数（跨网段 mesh M3）——会合边打分偏向
  少被钉住的候选，避免全网格把会合职责堆到同几个热节点上。

### `task_delegate`
任务移交（§10.3 示例）。除 `task_id` / `parent_id` / `project` / `title` /
`context_type` / `intent` / `spec_json` / `requires` / `preferred_node`（软提示）/
`target_node`（硬钉住：能解析则执行或向其转发，解析不到则如实 decline）/
`chain`（防环链） / `timeout_ms` / `max_retries` / `complexity` / `risk` /
`attempt_id` / `resume_session_id` / `user_locale` 外，还有几类要点：

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
- **同项目并行声明**：`paths` 声明子任务在项目工作树内独占的相对目录
  ——执行方为它锁 `scope:<项目>/<路径>` 叶键而非整棵项目根，路径互不
  重叠的兄弟在同一棵树上真正并行，重叠或未声明的仍按项目锁串行；
  `isolate` 则让子任务拿到项目树的私有副本（仓库根上是 detached git
  worktree 叠加未提交改动，否则跳过派生目录的拷贝），只锁自己的
  `task:` 键与 actuator，与一切任务并行，完成时按（基线/主树/子树）
  三方逐路径合并——主树未动处的子改动与删除落回，双方都动的路径记
  冲突保留主树字节并把工作区留作人工比对。两字段随行持久化，转发/
  重路由/接受端按行内容重建同样的锁集与行为。
- **计划面**（v0.0.6）：`plan_id` / `stage_id` 标识该阶段供编排者审计，
  `inputs`（`ArtifactRef` 列表）声明每个前置阶段打包产物及其所在节点，
  执行方据此拉取起始树。独立任务为空。
- **传输与预算**：`transport`（`live` | `dtn`）选定实时链路还是 store-and-
  forward；`deadline_unix` 是 DTN 任务的绝对期限（过期即失效而非租约杀）；
  `depth`/`delegation_budget`/`token_budget` 是因果深度与逐跳递减的网状
  配额——归零的任务只能执行或 decline，不再路由；`max_depth` 随 depth
  同行，携带发送方生效的递归上限（本机 delegation.max_depth 与继承值
  取严），接收方按 min(本机配置, 线路值) 落库为 spawn_cap，整棵子树执行
  沿途各跳中最保守的深度策略。`bundled_artifacts`
  是主动推送形态自带的输入工件（§8.3 fat-bundle）。
- **信任钳制**（信任分区，experimental）：发向 `guarded` 档对端的委派按
  `network.trust.send.guarded` 裁剪暴露面——`context_level` 最高
  `pointer`（快照只在审计拉的 `context_fetch` 里出网）、`project_pack`/
  `inputs`/同意签名一律剥离；会因钳制失去必备输入的任务改走重路由而非
  降级送达。`open` 档对端不发也不收推送态委派（offer/bid 市场未建）。
  **截屏任务是一等敏感类别**：`requires`/`action_spec` 命中
  `hardware:screen_capture`（含 `hardware`/`hardware:*` 这类可解析到它的
  宽令牌）的任务只在 mesh 链上移动——非 mesh 来源在准入即拒（
  `trust_policy_refused`），发送侧钳制直接拒绝而非降级，其输出工件在
  artifact_fetch/push 服务侧同样只放给 mesh 对端（参与关系与签名 grant
  都不足以放松这一条）；`screen: true` 是同一所有者场景的显式开关。
  运维自定义的 `screen_sensing` 类目 actuator id 静态谓词认不出——
  由执行端认领时的 `screen_output` 运行时打标接力（随 `task_result`
  逐跳补标，见 `task_result` 节）。屏幕阶段的产物作为下游输入交接
  时绑定同样成立：`ArtifactRef.screen` 由编排者从生产者行的标记打
  上，路由（Needs.Screen）、准入与服务判定三面读取——凡消费显示
  字节的任务自身即为 display-bound（其工作树与产出都可能嵌有像素），
  标记沿阶段逐级级联；伪造的标记只能收窄受众，与结果标志同向安全。

### `task_accept` / `task_decline`

`task_accept` 是接受回执；新节点附 `sig`/`pub`/`ts`——执行方 Ed25519 对
(task, attempt, signer, ts) 的签名，委派方与中继各自验签（>5min 偏移、
钥不符、畸形一律丢弃）。同意说「什么可以跑」，签名接受说「谁同意跑」
——转发任意一侧都无法伪造另一侧；无签名的旧节点帧仍兼容。

两帧都携带 `attempt_id` 并按同一**方向门**落地：线上 attempt 严格旧于行
内 attempt 的帧是已被取代的 dispensation 的迟到回声，丢弃（行已轮转后
再 decline/accept，会把活 dispensation 弹回队列或标成运行中）；线上
attempt 相同或更新则照常——镜像副本之间 attempt 合法分化（各自轮转、
惰性 adopt），裁决语义跟着工作走而非跟着某次执行走。门之后的写路径
同样带 attempt CAS（`Accept`/`Decline` 的期望谓词），关掉读检与落库
之间的 TOCTOU；CAS 失败的 accept 不再向上游转播。

`task_decline` 在人读 `reason` 之外可带机器稳定 `reason_code`：
`trust_open_origin`（源档为 open，推送态起源被拒）、`trust_policy_refused`
（接收方准入策略拒绝）、`trust_send_ceiling`（发送侧钳制剥掉了任务跑不了
的依赖——正确响应是重路由而非重试）。空 code 是信任分区前的普通拒绝
（drain、容量、防环），重路由处理不变。

### `task_cancel`

`TaskCancelPayload` 的 `attempt_id` 把取消分成两种语义，对应两类发起者：

- **树级取消**（`attempt_id` 为空）：人工取消/驳回——`panda cancel`、
  控制台 reject、审批否决。无条件级联整棵任务树：本地 cascade 杀掉
  行与其全部子代，`cancel_outbox` 托管转发下游执行方，接收端的
  `handleCancel` 递归再级联，直到末梢。
- **dispensation 取消**（`attempt_id` 非空）：生命周期子系统对**某次
  具体派遣**的停机令——租约过期重路由、supersede 顶替、绝对死线
  判定。它只该落在它点名的那次派遣上：接收端行内 attempt 已严格
  更新（新 delegate 先到、或对端 accept/轮转 adopt 过）则整帧丢弃，
  并记审计而非误杀现役 attempt。授权先于一切任务级判定：发送方必须
  是行的 owner 或链上直接前驱（按运行时身份比对，容忍 daemon 重启
  换实例号），行外 peer 连审计都写不进。

`cancel_outbox` 的停泊行按同一 scope 规则管理：新 delegate 泊车时只清
掉同任务的 dispensation-cancel（旧派遣的停机令被新派遣取代），人工
树级 cancel 永不因泊车而丢；flush 时 cancel 先于 delegate 投递，live
送达的 dispensation-cancel 也只清同类停泊行。

### `task_delegate` 的顶替语义

重复送达的 `task_delegate` 不是天然幂等：行还在且线上 attempt 严格更新
时，它是重路由的顶替令——但顶替权的认证不只看链尾签名：发送方必须是
**存储行链路中 self 之前任一上游持有者**（多跳链路里中间节点失联时，
更上游的 delegator 可越级直派）。行外 peer 伪造的新 attempt delegate
不能删除/顶替任何行——没有这条绑定，同一帧就是远程任务清除原语。
顶替落地前活跃副本先经 `CancelCascade`+`finishCancel` 级联收尸（杀
runner、清 context 预留、向下游转发停机令），终态/失败副本直接删除
重建；旧 attempt 的迟到 delegate 一律丢弃，绝不复活已结案的副本。

### `task_result`
`task_id` / `attempt_id` / `chain`（执行方的真实委派链，供丢失本地行的
接收端重建上游路径）/ `state`（执行方持久化的状态，旧节点可省略；
缺失时由 `ok` 推导 done/failed，新节点须保留 `review` 以免被父节点误升为 done）/
`approval_disposition`（这个 review 审批意味着什么：恢复执行、接受已完成
工作、或信任准入复核——缺省按保守规则归类）/ `deferred`（结果报告的是托管
收执而非执行：审批已停车进 outbox，等链路恢复再送——界面应显示「排队待送」
而非失败退出码）/ `ok` / `exit_code` / `stdout` / `stderr` / `artifacts` /
`tokens` / `cost` / `output_artifact`（该阶段打包产物的哈希，编排者记下来
交给后继阶段作输入）/ `screen_output`（结果为 true 时该任务的输出工件
被标记为显示内容——执行端在认领时解析到 `screen_sensing` 类 actuator 即
打标，每个收到结果的委派跳都给自己的任务行补标；单向收紧语义：伪造的
true 只会收窄受众，伪造不出放宽）/ `files_changed`（工作树相对路径清单，上限 200，
让委派方看到任务实际动了什么而非只信 agent 叙述）/ `question`（agent 挂起
的澄清问题，随 review 态带给源端，`task_resume.answer` 回答）/ `executor` /
`agent` / `model` / `work_dir` / `duration_ms` / `session_id` /
`agent_session_id`（归属标注：谁在哪跑的、跑了多久）。

`stdout`/`stderr` 受 `maxWireText`（512 KiB）裁剪：子进程单流最多能产 8 MiB，
而帧上限 4 MiB，若不裁剪结果就永远落不了地。裁剪保留**头尾**（开头是意图、
结尾是结论），在 rune 边界切分以维持合法 UTF-8，并插一行
「中间已截断，完整输出留在执行节点」。完整输出留在执行节点，按需以工件方式带走。

### 任务间消息板：`task_message`
兄弟任务间的有界短消息通道（实现：`internal/core/messages.go`），
走 QoSSignal 车道。scope 只有两种：`plan:<plan-id>`（同一 plan 的 stage）
与 `task:<parent-id>`（父任务与它的子任务集，父本身是成员）。

- **member → owner 腿**：`scope` / `sender`（发送任务 id）/ `to`（未解析的
  地址：`stage:<id>`、`parent`、`siblings`、`children`、或成员 task id）/
  `grade`（`status` ≤400B / `report` ≤4096B / `artifact_ref`）/ `body` /
  `seq`（发送方单调计数，接收端据此拒绝迟到的旧拷贝）/ `sent_at` /
  `sender_label`（发送方的显示名，stage id 或标题）。
- **owner → member 扇出腿**：`fanout=true`，`rcpt` 为 owner 解析出的收件
  task id（`""` = 广播），`member` 指明这条副本落在哪个成员名下——接收端
  用它校验「本节点确实持有该 scope 的成员行」，校验不过即丢弃（伪造/误投）。
- **owner 是谁**：plan 板是计划原点（`chain[0]`，它持有全部 stage 行）；
  children 板是父任务的执行节点（本地 spawn 的行即本节点，wire 到达的行
  取 `Predecessor`，与结果回传同规则）。
- **投递语义**：尽力而为、无独立 outbox——槽位模型（每发送者每收件人一格、
  新覆盖旧）让丢帧由下一次发送自愈；读取发生在监督轮边界，不阻塞运行中的
  agent。TTL 24h，每 episode 发送预算 `messages.budget`（默认 8）。

### 工件帧：`artifact_fetch` / `artifact_chunk`
固定大小分块的**拉取**：需要产物的一方按 offset 一段段要，
上一段落地才要下一段，掉块 / 坏块是「重新请求」而非整次失败。

- `artifact_fetch`：`task_id` / `hash` / `offset`。
- `artifact_chunk`：`offset` / `data`（base64）/ `total`（全档大小，供进度并拒绝
  中途变长的流）/ `eof`（最后一段；收齐后按 `hash` 校验才允许入池）/ `ok` /
  `reason`（对端不持有或拒供时 `ok = false`，请求方改问别的节点而非无限重试）。

服务侧授权（`artifactTransferAuthorized`）在参与校验之上叠加一条显示内容
规则：被标记 `screen_output` 的任务产出的工件只放给 mesh 档对端——参与
关系、上游身份、签名 grant 都不能放宽；授权判定缓存（1 min TTL）记住
screen 标记并在每次命中时复检对端档位，信任降级即时切断传输而不是等到
TTL 期满。同一规则约束 `artifact_push` 的收方。

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

### 盲中继：`transit`
`sendTo` 直连与打洞路由都不通时的兜底（跨网段 mesh 设计 M3）。外层信封是
普通 `transit` 帧，`env.From`/`msg_id` 走逐跳重写与去重（同 punch 中继纪律），
`env.To` 记最终目的地；`TransitPayload` 是**中继唯一可读的外壳**——`dest`
（优先稳定键 `k:<pub>`，经目录解析当前实例）、`ttl`（`TransitMaxTTL=6` 逐跳递减，
耗尽即弃）、`seal` 级别、`inner` 密封 blob（≤1 MiB）。

`inner` 密封内容 = `{env, pub, sig}`：原信封字节 + 发送方 Ed25519 对内层字节的
认证（`panda-transit-inner-v1` 域前缀），所以中继改不动 `From`/`MsgID`/负载。
`pub`/`sig` 是必需字段，无签名的内层直接丢弃；实例/会话 id 必须能经目录行
绑定到同一签名密钥。未钉住的 id 只放行 heartbeat 与 punch 协调帧作为首次
接触，任务/结果/授权类帧必须先由签名 hello 建立身份，避免幽灵 id 借用默认
mesh 信任。落地端还要核内层信封的收件人确是自己（空收件人按广播放行）：否则
成员可以把给别人的帧封好后路由过来，让本节点替别人执行任务并替人回结果。
两级密封：`x25519` 用 ECIES（临时 X25519 公钥前缀 + HKDF 绑双方公钥 +
AES-256-GCM）封到目的节点的 `x_pub`，中继只读到 dest+大小+TTL；目的端没发布
`x_pub` 时退化 `mesh` 级——HKDF(mesh 密钥 + dest 绑定) ，任何成员都能开，
发送方记 `transit_degraded` 审计。hello `caps` 里 `e2et` 宣告可当被密封目的地
（发布了 `x_pub`），`trnz` 宣告会替成员转发；转发只走 WS conn（UDP 1400B 装
不下），hello/pair 类 conn 绑定帧永远不可被密封进内层。工件面（`artifact_fetch`
及其 chunk/push 应答）同样不进 transit：请求虽小，应答是 1 MiB 级块，密封后
必然超过 inner 上限；放行请求只会跳过调用方的链上/枢纽回退，让拉取空等预算。
数据面应走直连或既有中继路径。

### 流行病目录：`dir_pull`/`dir_rows`（M1）

每拍心跳捎带 `dir_digest`——发送方目录戳表（`id→content_hash`，会话/幽灵行
除外）排序拼接后的 SHA-256。表一致即零额外帧；不一致才触发 `dir_pull`：
请求方发自己的全量戳表，应答方差量回 `dir_rows`——对方缺/哈希不同的行
（≤256 行 ∧ ≤1 MiB，触顶处生成 `cursor` 供 `after` 续页）加上 `want`
（应答方自己缺的 id），一次往返两侧收敛。行是**凭证不是视图**：源节点
Ed25519 签名（`sig` 覆盖 seen_at 在内的全部字段，规范化 JSON），逐跳原样
转发；`content_hash` 覆盖除 `seen_at`/`sig` 外的**稳定**内容——实测 RTT
（`links[].rtt_ms`）也被剔除，否则摘要随每次 ping 变动、两端目录永远不收敛，
只能每 30s 互拉一次；源端重铸 seen_at 同样不动 digest。冲突裁决：签名行 >
未签名行，同级取 `seen_at` 新者；未签名行只能**新建**一行，永远不能顶掉已有
行（否则任何成员都能改写别人的能力卡/地址，并把它踢出拨号路径）；未来时标按
±5min 钳制。无签名行可流通（直接观测本来就没签名）但永不写
身份列（`pub_key`/`x_pub` 只收签名行），且断言能力/地址的分量低于任何
签名行。自己的行每次心跳现签；拉取双向限速（新鲜 pull 30s/对端，续页
按会话 ≤8 页）；`learned_via` 记录投递该行的对端。收侧另有两道闸：每发送者
每 10s 窗口最多引入 256 个新 id（锁内预留，未用即退还），且单个发送者占用的
目录份额不超过全表四分之一（按 `learned_via` 与其已钉密钥归属合并计账，
轮换 session id 也逃不掉）——否则一个成员可以把共享目录填满自己的行，
诚实的对端再也挤不进自己的行。老节点字段缺省、帧型
不识——同步对它们静默不发生，协议其余部分不变。

### 动态会合边与可达性阶梯（M2/M3）

**会合边**不是角色而是偏好：每个节点在目录行里挑 K=2 个广告了
`relay.rendezvous` 且在线、有 `pub_key`、可达的候选，保持长连。打分 =
行龄衰减（分钟级）+ 活边加成 − 心跳 `edge_load` 惩罚；选举带 1.25× 滞回
（挑战者须决定性胜过在位者才换边）和故障域偏好（同 v4 /24、同 v6 /48 的候选
优先错开，但只是偏好不是硬过滤——LAN 内所有节点同域，硬过滤只会选出第二个
节点就停手，网格只剩一条边），每 5min 重评。会合边是 punch 信封与 transit 转发
赖以流通的骨架——候选归零时节点如实降级：直连/打洞照旧、目录注明
degraded、outbox/DTN 兜底，不谎报可达性。

**可达性阶梯**在 `sendTo` 发现无活轨时后台点燃（30s/对端去重），让
*下一次* 发送能走直连而不是永远靠中继：

1. **addrs 直拨**：目录行自广告地址按序尝试（全局 v6 → 公网 v4 →
   主机名 → 其余；并发上限 4，失败地址冷却 5min）。来源闸：未签名的
   间接（gossip）行不许指挥拨号；签名/直连行的私网与回环提示只在
   直连学来的行上生效——转发行的 LAN 地址对本网段毫无意义。
2. **自动打洞**：目录行 `udp_port>0` 时进入打洞级。**选举**：字典序较小
   的 node id 是 offerer，直接发 `punch_offer`（60s/对端间隔）；较大侧
   只发 `punch_want` 提示——它感知到失败但 spray 归对方出。喷候选从
   目录 addr 的 IP × udp_port 合成（同套来源闸）。两个失败窗口仍无路由
   记 `nat_suspect`（10min），期间打洞级整体跳过——诚实的答案是
   outbox/DTN，不是无尽喷包；路由一旦证实即刻洗清。
3. **transit → outbox**：阶梯不改变既有的深层回退，只是让中继越来越少
   被需要。

**会合能力是证明出来的（P5）**：节点宣告 `relay.rendezvous` 后，必须向
≥2 个已认证邻居发 `dial_check{addr,udp,nonce}`，请对方真去拨自己的公网
地址；对方拨通回 `dial_check_ack{nonce,ok}`。凑够 2 个**不同**对端的成功回执
才算 proven（证明 30 分钟过期，12 分钟刷新一轮），期间能力卡上才写
`rendezvous: true`。探测方的闸与可达性阶梯同源：只拨全局可路由地址，
私网/回环/overlay 一律不拨，每对端每分钟至多一次——这是成员间的互助，
不是扫描服务。`network.rendezvous: always` 可跳过证明（运维声明），
`auto` 在没有邻居可问时退化为"有公网地址即可宣告"。

静态 `punch:<id>` 配置路径不变：它是显式 SLA，自动级只是给没有配置
的对端补上同样的机制；`punchAt`/`punchMaxSessions` 等预算两侧共享。

### `panda://join` 冷启动 URI（P4a）

零信息 = 零发现，这是物理不是缺陷，所以入网只需要**一个可敲门的地址**：

```
panda://join?b=203.0.113.5:7836&b=node.example.com:7836&k=k:<pub>&c=482913
```

| 字段 | 语义 |
|---|---|
| `b` | 可重复的接触点地址（`host:port` 或 `ws(s)://`）。任意在线成员都可充当，无专用服务器；按序尝试直到一个应答。 |
| `k` | 引入者的稳定 Ed25519 身份。`pair_ready` 回的 `pub` 不匹配则跳过该接触点——接触点可以是任何人，**引入者不能**。 |
| `c` | 期望的配对码（SAS，`482913` 与 `482-913` 等价）。给出即自动比对，不匹配直接中止：这就是 MITM 判定，没有留给人眼跳过。 |

流程复用既有配对仪式：拨 `b` → `pair_hello`/`pair_ready` → 两端显示同一
SAS → 引入者人工确认 → `pair_secret` 带密封密钥回程 → 落地为
`network.shared_secret` + 该 `b` 进 `peers`。URI 只加了两道**收密前**的闸
（`k`/`c`），不改配对协议本身。`panda nodes invite --uri` 打印的本机 URI
就是 `panda join` 解析的那一行（往返有单测）。

### pair 帧经 transit：接触点代理（P4b）

joiner 与引入者双向不可达时，`b` 里的任意在线成员充当**接触点**转发配对
握手。两端仍然只说原来的配对协议——没有新方言，也没有改变客户端：

```
joiner ──明文 pair_hello{target=k:<I>}──▶ 接触点 C
C ──transit{seal:"none", dest=k:<I>, inner=pair_hello}──▶ 引入者 I
I ──transit{seal:"none", dest=k:<J>}──▶ C
C ──明文 pair_ready / pair_secret──▶ joiner
```

- **接触点回程绑定（att_clients）**：C 收到带 `target` 的 `pair_hello` 时，
  按其中的 `pub` 记 `k:<joiner-pub> → 该未认证 conn`，TTL = 配对窗口
  4 分钟、上限 16 条、conn 死亡即清。transit 的 `dest` 解析顺序变为
  `instanceForStable → att_clients → 丢/回执`。绑定只存本地、不 gossip、
  不授予任何成员权利；冒名者顶多让两端 SAS 不一致——那是配对本来就要
  检测的东西。
- **`target` 只被消费一次**：C 转发前必须清空它，否则引入者会把自己当成
  第二个接触点继续转。
- **`seal:"none"` 是唯一的未密封豁免**，且**只对 pair_\* 内层生效**：
  joiner 此刻既无 shared_secret（mesh 密封不成立）、也不知引入者的
  X25519 钥（x25519 密封不成立）。pair 帧从设计第一天起就按"敌对信道"
  处理——机密性靠 DH 盒，认证靠 SAS；中继能读、能改、能丢，任何一种
  都会表现为两个人屏幕上的码不一致或盒子打不开。任何非 pair 内层若以
  `seal:"none"` 到达，接收端直接丢弃并告警。
- 引入者的会话不再绑定 conn：`pairSession` 记录 `replyHop`（来路中继）
  与 `replyDest`（joiner 的稳定身份），`pair_ready`/`pair_secret`/
  `pair_reject` 全部沿同一条回路发出，并以 `transit_degraded` 记账。
- x25519 升级（joiner 在 `pair_hello` 里带 `x_pub`，引入者直接密封回程）
  留给后续阶段：它需要客户端能解 transit 外壳，本阶段刻意不引入。

## LAN 发现 beacon 与配对

发现面不在信封体系内，是独立的 UDP 广播（`internal/core/discovery.go`，
`network.discovery_addr`，默认 `:7837`，`"off"` 关闭）：每 15s 一个
`panda-beacon/1` 数据报（≤1 KiB），携带 id/addr/ver/pubkey/ts 与 Ed25519
签名。beacon 只是**提示**、不是成员证明——签名钉住的是「这个地址宣称这个
指纹」，防止局域网陌生人冒用他人 node id+公钥收割签名 hello；验签过的行进
`pending_nodes`（60s TTL、64 行上限，已验签行优先挤掉未验签行），准入仍要
`panda nodes admit`/`nodes add` 后的正常 hello 握手。自己的 beacon 与已入伙
id 忽略不录；回环监听与无共享密钥的节点不广播。

配对（`panda pair`）走上面 `pair_*` 四帧：X25519 临时交换 + 双侧短验证码
人工确认 + AES-256-GCM 密封下发共享密钥——共享密钥永不明文过网，配对码
比对挡住 MITM 中继（两端 DH 不同则验证码不同）。
