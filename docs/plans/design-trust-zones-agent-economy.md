# 设计稿 — 信任分区、agent 对话与去中心化工作市场

> 状态：部分实现（2026-10-11）。实验分支 `feat/trust-zones` 已落地
> Phase 0（结构化 peers + 信任档 + 发送侧暴露策略）与 Phase 1
> （guarded 审议式准入 + 可选 LLM 分类器），以及 Phase 2 中不依赖
> 市场的两个件：双边签名 `task_accept`（Ed25519 绑定
> task/attempt/签名者/ts，旧节点无签帧仍兼容）与信誉进路由分
> （`routing.reputation`，本地 `delegation_metrics` 的 Laplace 平滑
> 成功率，opt-in）。review park 期间 executor 以 `task_progress`
> 拍子为 delegator 侧行续租（首拍注明"held for operator admission
> review"，上界 24h，重启后经 `Recover` 重新武装），慢速人工评审
> 不再租约超期成重复重路由；准入 park 用独立 `admission_review`
> disposition——线上 `task_resume` 对它一律拒绝，只有本机
> `panda approve` 能放行（委派方正是被审对象）。`nodes trust`
> 另支持 `k:<pubkey>` 稳定身份钉档。入口点：CLI `nodes add/
> admit/trust` 与 `pair --trust`、REPL `/nodes add|admit|trust`、
> Web 节点卡片的档位选择器（`POST /api/nodes/{id}/trust`，
> 支持 `k:` 身份并回写 peers 配置）。offer/bid 市场与信用
> 账本/结算**未实现**——open 档因此只能存在于目录中，在钉了它的
> 节点上既不能作委派目标也不能发起委派（信任是局部主观断言，
> 未钉它的节点仍视其为 mesh）。全部功能默认关闭：未配置任何
> tier 的舰队与特性前行为逐字节一致。
> 引用代码以 `internal/` 现状为准；与代码冲突时以代码为准。

## 0. 一句话

把"持有共享密钥即完全信任"的单一信任域，演化为**三层信任分区 +
声明式协商 + 双边记账**的去中心化工作网络：内网设备保持今天的
确定性委派，半信任设备进入有边界的对话协商，陌生设备在 offer-bid
市场里按信誉与记账工作。架构原则：**薄宪法、厚认知**——协议只冻结
身份/授权/边界/生命周期这些不可压缩的关系事实，把"怎么想、怎么做"
留给各节点的智能去发挥。

## 1. 动机

现状的委派模型是「A 路由打分 → B 确定性接收 → B 在沙箱里执行」：

- `handleDelegate`（`internal/core/handlers.go:50`）的接收判断是纯
  规则：幂等、链防环、deadline、token 预算、consent 验签、
  `scheduler.RouteP` 再打分。B 从不思考"要不要接"。
- `commander.Route`（`internal/commander/commander.go:371`）把
  `requires` 匹配到 B 的能力卡：native 是模板命令（B 是函数），
  agent 才调本地 LLM——但它只被允许思考"怎么做"，不许思考
  "该不该做、要不要换做法、这事涉不涉我的隐私"。
- 信任粒度只有一个：`shared_secret` = mesh 成员 = 同等待遇。成员
  内部只有单比特的区分（`remote` 溯源 + `WithRestricted` 只读工具面，
  commander.go:483）。在深空链路、跨所有者的网络里，"全是自己人"
  这个假设不成立。

这个结构在"我的几台设备"场景是对的，但有两个更深的问题：

1. **它把 B 当方法调用**。多智能体的愿景应该是节点间对话——B 思考
   后自己决定做不做、怎么做、要不要反提案，而不是无条件执行 A 编译
   出的 `requires`。
2. **它冻结的是弱模型时代的契约**。`requires`（A 猜 B 需要什么能力）、
   `spec_json`（A 替 B 规划的做法）都是认知脚手架。模型越强，这些
   字段越像绳索；而真正不可压缩的——身份、授权、资源边界、生命周期
   ——会被埋在同一个载荷里一起腐烂。

## 2. 现状切分：宪法层 vs 认知层

把 `TaskDelegatePayload`（`internal/bus/payloads.go:167`）和相关帧
按"AGI 也不能删"vs"智能可以吸收"重新分类：

| 层 | 字段/机制 | 性质 |
|---|---|---|
| **宪法** | `from`/`msg_id`/`ts`、hello 双重签名、Ed25519 钉住 | 身份归属，不可压缩 |
| **宪法** | `chain`、`remote` 溯源、`auth_hops` | 经手历史，防中继伪造 |
| **宪法** | `authorized`+`auth_sig`（`ConsentDigest` 绑 intent/spec） | "what 不 how"的授权载体 |
| **宪法** | `deadline_unix`、`token_budget`、`delegation_budget`、lease | 资源边界，服务主人 |
| **宪法** | `task_decline`+结构化 reason、`task_result.state`/`ok`/artifact hash/`files_changed` | 拒绝权与结果完整性 |
| **宪法** | 生命周期动词：delegate/accept/progress/cancel/resume/result | 最小状态机 |
| 认知 | `intent`/`spec_json`/`requires` | spec 降为可选提示；requires 是 A 的猜测 |
| 认知 | `complexity`/`risk`/`preferred_node` | 全是 advisory |
| 认知 | `context_level`/`context_hash`/`project_pack` | 传输机制，可协商；同时是暴露物 |
| 认知 | `plan_id`/`stage_id`、`Inputs` | A 侧编排痕迹 |
| 认知 | 能力卡静态 id 列表、RouteP 打分权重、prompt 组装、judge | 各节点本地事务 |

**协议设计结论**：宪法字段保持强类型、随版本冻结；认知字段收拢进
一个可自由扩展的协商文档（开放 JSON），让"修宪要 bump 版本、认知
字段随便长"。`hello.caps`（`binart`/`sessaead` 先例）就是新旧节点
自选模式的通道。

## 3. 信任分区

三档，按对等的 Ed25519 身份挂级别（`nodes verify` 的 TOFU 指纹钉住
已存在，是挂级别的天然锚点）：

| 档 | 语义 | 行为 |
|---|---|---|
| `mesh` | 我的设备/全信任 | 现状原样：确定性委派、consent 可传递、context 全开 |
| `guarded` | 半信任（朋友设备、不可信网段上的自有机器） | 策略审 + 受限工具面 + consent 不跨档传递 + 暴露策略收紧 |
| `open` | 陌生节点 | 只走 offer-bid；零上下文暴露；纯投机执行；按信誉与记账工作 |

落地形态：

- `network.peers` 从 `[]string` 升级为结构化条目（地址 + `trust`），
  默认值保持 `mesh`——配置向后兼容，无信任字段 = 现状。
- 配对/纳管时定级：`panda pair --trust`（现代握手与 legacy
  `--secret --peer` 两条路径均支持）、`panda nodes add|admit
  --trust`、`panda nodes trust <id|k:pubkey>`；REPL `/nodes`
  同参；Web 节点卡片档位选择器（`POST /api/nodes/{id}/trust`）。
  钉档落在 `employee_cache.trust` 列（storage migration v43）与
  `network.peers` 结构化条目两处，least-trusting 合并后生效。
- 换钥 = 重定级：`recordPeerPubKey` 已会在换钥时清 `key_verified`
  （roadmap Track 1），信任级别随行——重装的机器不继承旧信任。

## 4. 两个正交旋钮（比"信/不信"更有表达力）

**(a) 来源信任 → 暴露面**：这个 peer 的任务能拿到什么——
`context_level` 上限、`project_pack` 发不发、agent 工具面、
consent 能不能跨过来。**隐私主要决定在发送侧**：`context_level:
full` 会把快照整个推过去；对不可信执行方这些根本不该上链。B 拒绝
执行是第二道闸，第一道是 A 的暴露策略。

**(b) 执行自治度 → 计划权归谁**：A 的 `requires` 直接匹配（现状），
还是 B 从 intent 自行推导并可反提案（对话模式）。副产物：B 自推导
requires 还顺带消除了跨版本能力命名空间的脆弱——A 不再需要猜 B 的
能力表怎么写。

边界规则：`native`/`actuator` 能力不进对话模式——模板命令前加 LLM
思考只增加成本和注入面。对话只对 agent-kind 有意义。

## 5. 对话与协商：offer-bid 拉式市场

**已存在的对话原语**（v0.0.10 Track 0/Q4 已落地）：agent 输出
`PANDA_QUESTION:` → 任务停 review → `task_resume.answer` 带回答
折回 intent 重跑。这是一个完整的"执行方反问、发起方回答"的跨节点
一轮对话——对话模式不是造新协议，是把它前移到 accept 之前，再加
"反提案"语义。

**为什么用拉式而不是推式**：`task_delegate` 发给陌生节点的瞬间
intent 本身就泄漏（"读我 ~/diary"暴露了路径）。改为招聘模式：

1. A 广播 `task_offer`：抽象需求（能力类、预算上限 `token_budget`、
   期限 `deadline_unix`），不含细节 intent；
2. B 们回 `task_bid`：能做、条件、报价；
3. A 选定中标者，才把完整 `task_delegate`（intent+spec）发给中标者
   ——且 B 中标后按需要 `context_fetch` 拉上下文（拉取即授权点，
   现有帧复用，天然最小暴露）。

**有界协商**：counter 最多一轮，超时/超限回落到单发 delegate 或
`ActionDecline` 路径。DTN 场景下 offer/counter 塌缩进 bundle 元数据
（一个 contact window 经不起多轮谈判——bundle 本来就不能协商，
执行方必须能拿本地策略自主决策，这与 §3 的分区恰好同向）。

**结构化 decline**：`TaskDeclinePayload.reason` 需要区分
「能力不匹配 / 策略拒绝 / 反提案」，否则 A 无法决定是改写重试还是
改路由。加一个机器可读 reason code 字段（旧节点忽略）。

## 6. 双边 consent = 合同

今天 consent 只有 A 侧（`signConsentGrant`，`internal/core/nodekey.go:
205`）：「用户批准了这个 intent」。对话模式下 B 的 owner 策略也要
表态——A 签「我批准此 intent」，B 对签「我的策略接受」：

- accept 帧升级：带 B 的 Ed25519 签名 + 接受的约束摘要（工作目录、
  工具面、报价），任务成为双签名合同才执行；
- 签名顺序防"我签了你跑"：A 的 consent 已有 `auth_ts`+digest 绑定；
  B 的 accept 签名绑定同一 `task_id`+digest+TTL，过期未执行则作废。
  （实现注：当前签名 accept 已上链验证，但**无签帧仍被接受**以兼容
  旧节点——它还不是双边承诺。Phase 2 的授标语义落地时，对非 mesh
  tier 必须把签名设为强制：guarded/open 来源的 unsigned accept
  应拒绝，否则"双签合同"对半信任节点形同虚设。）
- `ConsentDigest` 的方向天然兼容：它绑 intent/spec（what），刻意不绑
  执行计划（how）——B 换做法不 invalidate consent。

## 7. 能力令牌统一

代码里已有两个特设的"签名能力凭证"，尚未统一：

- consent grant：`auth_sig` 绑 ConsentDigest（任务授权）；
- `ArtifactRef.Grant`：编排者签名授权产物拉取（数据授权）。

加上 `auth_hops` 已是"逐跳衰减"的雏形，把它们统一为**可衰减授权
令牌**（macaroon 式）：每跳中继只能往令牌里加更严的 caveat——
跳数上限、路径范围、时间窗、egress 限制、金额上限。开放区的 per-
resource 授权（"只读 /srv/data 24h"）用同一格式表达，不需要新机制。

## 8. 活性保障：为什么不会卡死

活性不在市场层，在租约机制上——原封不动复用：

- B 中标不执行 → 每拍 `task_progress` 续租停止 → lease sweep 收尸
  → A 授给次高出价者或回落推式委派；
- B 执行中崩溃 → 同上，`max_retries` 重试；
- 无人应标 → offer 自带 TTL（`deadline_unix` 已管），过期走今天的
  `ActionDecline` 终态：回落/排队/如实报告"全网没人接"；
- 中继丢结果 → outbox 按 `(peer, task)` 持久化重投（P0-2 已修）。

最坏情况是**有界延迟 + 逐级降级**，不存在"等一个不存在的回复"。

## 9. 诚实经济学

市场新增的风险是激励问题，不是活性问题：

| 风险 | 对策 | 现状支点 |
|---|---|---|
| 中标 ghost | lease 收回 + 信誉记账 | 已有 |
| 垃圾结果 | 执行回执 + A 侧 judge 验证 | `files_changed` 是雏形回执 |
| 低报能力 | 出价自我揭示：永远不中标=自己承担成本 | — |
| 合谋拒标逼提价 | offer 带底价+截止；回落通道封死勒索空间 | — |
| 女巫换钥洗信誉 | 信誉锚 Ed25519，换钥=重新排队积累；准入摩擦就是身份成本 | pairing/pair 已有握手成本 |
| 高价值任务 | 冗余授标（前两名，比对输出，拜占庭取共识） | 可选，按值启用 |

回执扩展方向：`task_result` 增加 transcript hash + 工具调用清单 +
产物哈希清单——A 的 judge（`internal/entry/supervise.go`，已在做
"上级审核"）从"看文本打分"升级为"验证据验收"。

信誉喂进 `score()`（`internal/scheduler/score.go:183`）作为新加权项
——同一个分数函数在内网也在优化路由（"哪台设备干哪类活靠谱"），
即使开放市场永远不长大，这层也不白做。

## 10. 信用账本与分层结算（不发币版"报酬"）

"B 干完获得报酬"不需要链——双边署名欠条即可，双花问题只对不记名
货币存在：

- **发票原语已有**：签名的 `task_result` 就是发票——`token_budget`
  是报价上限，`Tokens` 是实耗，Ed25519 签名防伪。
- **双边 credit 账本**：每对节点维护签名余额（存储走 storage
  migration，新表）。内网同一主人自动作废；半信任是人情记账
  （"你欠我 3 万 tokens 算力"），用 reciprocal compute 抵债。
- **陌生人结算两条路**：
  1. 传递抵销——A 欠 B、B 欠 C、C 欠 A → 环路净额。link-state 图
     已在跑，信用边就是图上另一种权重；
  2. 外部支付轨插槽（Lightning/L2），adapter 化，不进协议核心。
- **分层结算**复用 tier 思想：低值任务走信誉+欠条，高值任务走
  托管+争议窗（A 签收放款或超时放款；争议的社会机制——信誉广播、
  永不接单——兜底，不做链上仲裁）。

**明确不做**：发币、全局共识账本、链。三者对这个系统是负资产
（监管、流动性、复杂度），而 oracle 问题（"谁判定干完了"）靠
escrow+签收+信誉的社会方案解决——认知劳动产出不可加密验证，
这是物理事实，不是工程欠债。

## 11. 硬边界保持确定性（LLM 不做安全边界）

这是整个设计成立的分水岭：B 的"思考"只裁决**生产力问题**（值不值得
做、能不能换做法、要不要反提案）；**安全问题**继续由确定性层裁决：

- policy LLM（如果启用）跑在零工具纯分类位，verdict 输入到现有
  `commander.Route` 管线，不绕过；
- tier 门、scope 白名单、`WithRestricted`、seatbelt/bwrap 沙箱、
  `NetworkGuard` 出口白名单——一个都不撤，它们是下限不是脚手架；
- "只读 ≠ 隐私"：restricted 防写不防读，agent 回复文本本身就是
  外泄通道 → 决定性控制是 egress 白名单 + 结果文本过
  `internal/security` 的 redaction；
- `context_level: pointer` 同样要诚实定性：executor 经
  `context_fetch` 拉走的是**全量**快照——认证、授权、审计都在，
  但字节照样移动。它是传输分级+审计点，**不是**保密边界；保密
  用 `summary`（引用整体剥除，拉取通道在 summary 上限下拒绝）；
- `inputs: false` 在拉取侧同样成立：`artifact_fetch` 区分方向——
  下游请求方在任务链中位于本地节点**之后**时按其 ceiling 拒绝
  （`peer trust ceiling bars input pulls`）；上游 delegator 回拉
  自己任务的 outputs 不受 inputs 上限约束（那本来就不是输入）；
- 入站 consent 有独立策略位：`accept.<tier>.foreign_consent`
  （`strip` 默认 / `honor`）。非 mesh 来源携带的 `authorized=true`
  在 strip 下剥除——任务落受限工具面、不可逆操作进本机 review
  队列；**对端用户的"批准"不能替代本机用户的批准**。honor 留给
  "同一主人的不可信网段机器"场景（此时两端的用户是同一人）；
- `policy_llm: enforce` 的 verdict 输入是来源方可控的 intent
  文本——确定性安全门不受影响，但 admit/decline 这个裁量本身
  可以被构造文本引导，enforce 档应视为"生产力裁量外包"而非
  准入加固；
- plan-then-execute 选项：guarded 节点可先回"我打算这么做"的计划，
  A 侧批准后才执行（review/ApprovalDisposition 状态机已有）——
  计划比执行便宜，是 scope-drift 之前更靠前的防线；
- 记忆污点门同构延伸：`memory.Sources.Trusted()` 已是"来源不可信
  则不晋升"的结构门，远程对话内容自动不进 MEMORY.md。

## 12. 分阶段落地（main 分支，全程 feature-gated）

不进实验分支的理由：线协议改动全是增量的（新 optional 字段 + 少量
新消息类型，旧节点按惯例忽略）；`caps` 协商自动区分新旧节点；危险
部件（LLM verdict）是纯本地决策点、默认关；仓库演进极快，长期
分支会烂在 rebase 里。

- **Phase 0（纯确定性，零新协议）**：`peers` 结构化 + `trust` 字段 +
  暴露策略表（context_level 上限、project_pack 开关、工具面映射）
  + storage migration 加 peer 信任列。默认全 mesh = 行为不变。
- **Phase 1**：guarded peer 的 deliberative accept——`handleLocalDelegate`
  admission 点后插策略门（先确定性规则版，LLM policy 可选开启）；
  结构化 decline reason code。
- **Phase 2**：`task_offer`/`task_bid` 消息 + 授标流程 + 双边签名
  accept + 回执扩展 + 信誉账本与 score() 新权重。
- **Phase 3（可选，真·open zone）**：open 档位、credit 账本、
  传递抵销/结算插槽、冗余授标、能力证明挑战。

前两阶段拿到绝大部分价值；Phase 2/3 是否做取决于"公网"的真实定义
（见 §14）。

## 13. 明确排除

- 发币、全局共识、区块链；
- LLM 作为安全边界（只做生产力裁量）；
- native/actuator 能力进入对话模式；
- 无界协商轮次；
- 信任级别随 hello 自报（必须由本地 operator 配置/配对授予，对端
  声明的信任一律忽略——`remote` 溯源字段已是此原则先例）。

## 14. 开放问题

1. **"公网"指什么**：自有但处在不可信网段的机器（VPS/实验室主机）
   vs 陌生人的机器——前者只需暴露控制（Phase 0/1 即可），后者才
   需要拜占庭经济学（Phase 2/3）。这个答案决定后两个阶段是核心
   还是屠龙技。
2. **counter 由谁拍板**：B 反提案"换做法"时，A 侧 judge 自动裁决
   （全自动闭环）还是弹给用户（DTN 下等于再等一个窗口）。
3. **能力证明**：bid 是否需要附可验证的硬件/能力证明（挑战-应答），
   防"声称有 GPU"。
4. **争议解决**：高值任务托管后双方对"干没干完"分歧时的机制
   （评审团多签？），或干脆不做高值陌生任务。
5. **offer 广播的暴露面**：抽象 offer 本身泄漏多少信息（"有人在
   找会做 X 的节点"），需要暴露预算评估。
