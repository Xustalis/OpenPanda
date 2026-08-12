# PANDA · 异构分布式桌面助理系统 —— 总览设计文档

> **PANDA**: Personal Adaptive Node-based Distributed Assistant
>
> **版本**: v4.6
> **日期**: 2026-08-12
> **作者**: Xenith
> **状态**: 架构基线已收敛，已整合 ATC-MARL 论文（徐浩博, 2026.05）完整技术细节与定量数据；待落代码
>
> 本文档整合了原始设计文档（v1.0）、ATC-MARL 论文理论映射（含逐模块公式与实验数据）、以及多轮架构讨论。
> 它是系统实现的唯一权威参考。当前本地 MVP 与后续演进方向明确分层；旧版文档仅作历史记录。

---

## 目录

- [零、项目定位与远景](#零项目定位与远景)
- [一、原始构想与架构演变](#一原始构想与架构演变)
- [二、ATC-MARL 理论基础与工程映射](#二atc-marl-理论基础与工程映射)
- [三、系统总览架构](#三系统总览架构)
- [四、语言与技术栈终裁](#四语言与技术栈终裁)
- [五、核心层：Go 常驻守护进程](#五核心层go-常驻守护进程)
- [六、统领层：三层能力模型与 Agent 可插拔适配](#六统领层三层能力模型与-agent-可插拔适配)
- [七、统一入口模型：决定处理类型](#七统一入口模型决定处理类型)
- [八、Skill 系统：自进化的流程记忆](#八skill-系统自进化的流程记忆)
- [九、员工表与设备入职](#九员工表与设备入职)
- [十、通信管线：P2P 委派协议](#十通信管线p2p-委派协议)
- [十一、任务队列与用户面板](#十一任务队列与用户面板)
- [十二、任务上下文系统（多类型 + 分级传输）](#十二任务上下文系统多类型--分级传输)
- [十三、模型性能调度器](#十三模型性能调度器)
- [十四、任务循环防御体系](#十四任务循环防御体系)
- [十五、代码质量与漂移防御](#十五代码质量与漂移防御)
- [十六、权限模型（Tier 1/Tier 2）](#十六权限模型tier-1tier-2)
- [十七、记忆系统（Hermes + OpenClaw Dreaming + Harness Auto-Skills 融合）](#十七记忆系统hermes--openclaw-dreaming--harness-auto-skills-融合)
- [十八、Token 经济性分析与优化](#十八token-经济性分析与优化)
- [十九、安全架构（OpenClaw 教训规避）](#十九安全架构openclaw-教训规避)
- [二十、服务器策略（公网 + 开源替代方案）](#二十服务器策略公网--开源替代方案)
- [二十一、竞品分析与创新边界](#二十一竞品分析与创新边界)
- [二十二、AI 模型替代风险分析](#二十二ai-模型替代风险分析)
- [二十三、可行性综合评估](#二十三可行性综合评估)
- [二十四、面向未来的设计：万物智联](#二十四面向未来的设计万物智联)
- [二十五、开源与商业化策略](#二十五开源与商业化策略)
- [二十六、开发路线图](#二十六开发路线图)
- [二十七、附录：完整术语表](#二十七附录完整术语表)

> **阅读约定**：正文中标记为“当前基线”的内容属于本地 MVP 的实现契约；标记为“后续演进”的内容是保留的产品愿景，不作为 MVP 的交付或验收前提。

---

## 零、项目定位与远景

### 0.1 一句话定义

> **PANDA 是一个以异构设备为节点的个人任务编排系统：当前先在本地可靠地完成任务委派与结果回传，后续再扩展到语音、桌宠、记忆、自适应通信和更大规模的分布式设备网络。**

### 0.1.1 当前基线与后续愿景

**当前基线（本地 MVP）**：固定一个入口节点；使用文本或 CLI 提交任务；使用本地 SQLite 记录任务和能力；通过点对点 WebSocket 连接一个或多个执行节点；支持 `native` 命令和一个 Agent adapter；完成提交、排队、执行、结果回传、失败和取消的基本闭环。不依赖公网服务器、手机 PWA、语音、Dreaming 或桌宠硬件。

**后续愿景（保留）**：动态入口调度器、多级 P2P 委派、手机/语音入口、桌宠硬件、项目与个人双层记忆、Skill 自进化、上下文分级传输、模型调度、对抗性审查、服务器索引、开源生态和更大规模异构节点。它们必须在前一阶段的协议和状态契约稳定后逐项加入。

### 0.2 解决的问题

当前 AI Agent 工具的**结构性缺陷**：单机多子代理已经成熟（Claude Code sub-agents、Codex orchestration），但**多设备分布式算力没有被充分利用**。做项目时，iOS 构建必须手动传到 Mac，Windows 构建必须手动传到 Windows，GPU 训练必须手动提交到带 4060 的机器。每台电脑各自为战，用户充当人肉调度器。

### 0.3 核心愿景

**任何设备，任何算力，一个语音指令。**

- 香橙派/树莓派做嵌入式执行
- MacBook 做 iOS/macOS 构建和轻量编排
- Windows + RTX 4060 做 GPU 训练和重渲染
- 超算做大规模并行计算
- 手机做远程入口和审批终端
- 未来：自动驾驶车辆、救灾无人机集群、深空微星通讯——只要是异构算力节点，就能被统一调度

### 0.4 两个结构：引擎（软件基石）与桌宠（硬件载体）

PANDA 由**两个结构**构成。不是三层，是两个：

```
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│  引擎 (Engine) · 软件基石                                    │
│  分布式异构算力调度系统                                       │
│  - 受 ATC-MARL 启发的调度与通信工程，整个系统的核心             │
│  - 调度 / 算法 / 通信 / 记忆 / Skill / 统一入口模型           │
│  - 未来一切 AI 产品的基石                                     │
│  - 开源，可被任何人/任何项目复用                              │
│  - 独立存在，不依赖任何特定硬件形态                           │
│                                                             │
│              ▲ 引擎的大脑                                    │
│              │                                              │
│   ┌──────────┴──────────┐                                   │
│   │                     │                                   │
│   ▼                     ▼                                   │
│  桌宠 (Desk Pet) · 硬件载体                                  │
│  引擎的物理接口，伸向真实世界的手                              │
│  - 负责硬件处理：屏幕 / 舵机 / 麦克风 / 喇叭 / 传感器          │
│  - 给引擎一个看得见摸得着的实体接口                           │
│  - 积累硬件处理能力（驱动 / 状态感知 / 物理反馈）              │
│  - 为未来延展到真实物理世界做储备                             │
│                                                             │
│  未来延展路径:                                                │
│  桌宠 → 搜救无人机 → 卫星集群 → 救灾机器人                     │
│  （硬件处理能力原样平移，引擎不变）                             │
└─────────────────────────────────────────────────────────────┘
```

**一句话**：

> **引擎是整个系统的软件基石，桌宠是其后续的硬件载体。两者是产品结构，不等同于统领层的三种能力类型。**

**关键理解**：

1. **引擎是软件基石。** 调度、算法、通信、记忆、Skill 全在引擎里。它独立存在，不依赖任何特定硬件形态——可以跑在香橙派上，也可以跑在云上，未来也可以跑在卫星上。
2. **桌宠是硬件载体。** 桌宠负责的是引擎的硬件接入：屏幕显示表情、舵机做动作、麦克风听声音、喇叭说话、传感器感知环境。它把引擎的"意图"翻译成物理世界的"动作"。
3. **桌宠给引擎优化硬件处理，为未来做储备。** 在桌宠上练出来的硬件处理能力（如何驱动设备、如何感知状态、如何做物理反馈），未来原样平移到搜救无人机、卫星集群、救灾机器人上。**引擎不需要改，换的是"手"。**
4. **不是三层。** 空间智能不是独立的一层——它是桌宠积累的硬件能力在更大规模物理场景的复用。桌宠就是那条通往空间智能的路。
5. **桌宠的屏幕方案（墨水屏/小屏 LCD）和舵机自由度保持开放**，等硬件到位再定，不锁死。

### 0.5 为什么叫 PANDA

Personal Adaptive Node-based Distributed Assistant。

- **Personal**: 它是你个人的助理，演进你的记忆，了解你的偏好
- **Adaptive**: 自适应——根据设备能力、网络状况、任务复杂度动态调整策略
- **Node-based**: 一切皆节点。调度器不是固定身份，而是用户在某个任务上赋予某个节点的临时角色
- **Distributed**: 分布式去中心——任务通信点对点，不经过中心批准器，不要求所有设备在线

### 0.6 核心设计原则（贯穿全文档）

1. **入口角色与节点身份分离。** 当前 MVP 固定一个入口节点；后续任何节点都可以承担入口调度器角色，但必须通过状态同步和租约机制完成切换。
2. **控制面与数据面分离。** 当前使用本地能力目录；后续可用集中式员工表作为控制面，任务数据仍通过点对点通信传输。P2P 不等于没有目录服务。
3. **队列是用户视图，任务依赖是系统结构。** 用户界面可按 `SUBMITTED/RUNNING/REVIEW/DONE` 分组；任务内部使用父子关系和独立依赖边表达执行顺序。
4. **一次入口模型调用决定处理类型。** 输出可以是 `answer`、`tool_call` 或 `task`；模型负责判断和结构化，不直接绕过 Go 核心执行副作用操作。
5. **短任务和长任务使用不同管线，但不承诺固定延迟。** 延迟数字只能作为本地实测目标，不能写成系统保证。
6. **记忆是给助理用的，不是给工作用的。** Hermes 了解你，但不能在项目执行时把"用户喜欢暗色主题"注入 agent 的代码修改决策。
7. **统领管三层能力：native（确定性命令）、agent（AI 推理）、manual（人工操作）。** 不是只管 Agent。
8. **可挽回自动批，不可挽回必须问人。** 两层权限模型，不因为省事而放弃安全。
9. **架构不依赖任何特定基础模型。** 模型越好，系统越强——但不是替代关系。
10. **Agent 可插拔，不硬绑定任何单一 CLI。** MVP 先支持一个经过验证的 adapter；后续再扫描和接入更多 CLI。适配器的行数不是接口契约，兼容性、退出码、取消和输出格式才是契约。
11. **引擎是基石，桌宠是引擎伸向物理世界的手。** 引擎是软件基石（开源 + 论文产出），桌宠给引擎做硬件处理、为未来储备、延展到真实物理世界（搜救/无人机/卫星）。桌宠的硬件方案（屏幕/舵机）保持开放，等硬件到位再定。

---

## 一、原始构想与架构演变

### 1.1 起点：香橙派桌面助理（v1.0 原文档，2026-08-11）

原始文档定义了三层架构：

```
总管（香橙派 3B） → 统领（Mac/Win） → Agent（claude code/codex/qoder/...）
```

核心思想是"把香橙派 3B 做成一个可移动的桌面助理中枢，通过语音或手机对它下达任务，由它统一调度家中的 MacBook 和 Windows 电脑完成工作"。

原文档已经做对的五点（在后续讨论中得到保留和强化）：
- **能力声明机制**：设备启动时上报能力卡片，调度器基于能力匹配做任务分发
- **任务状态机**：pending→dispatched→running→review→done，含打回修正和跨设备转移
- **软中枢+对等心跳**：不做 raft 共识，做务实去中心化
- **顶级模型 API 全覆盖**：香橙派不跑本地模型，意图理解走 API
- **安全底线先行**：低权限账号+沙箱+高风险确认不可省

### 1.2 十轮架构修正（从 v1.0 到 v4.0）

| 轮次 | 原设计 | 修正后 | 修正原因 |
|---|---|---|---|
| 1（原文档） | 香橙派本地跑语音识别 | 语音链路改云端 | 2GB 内存 + Cortex-A55 跑不动 whisper |
| 2（原文档） | 香橙派做中枢大脑 | 香橙派降级为纯路由/总管 | 加入 4060 后，算力与决策分离 |
| 3（原文档） | 语音/意图走 4060 本地模型 | 全部改走顶级模型 API | Windows 回归纯算力池 |
| 4 | 全网维护注册表 | 员工表集中存放+API 按需查询（公司隐喻） | 员工不需要知道全公司人的简历 |
| 5 | 三层固定架构 | 任务驱动委派树（谁发命令谁就是调度器） | 调度器是用户选择的入口 |
| 6 | 总管→统领单向分发 | P2P 逐边直接委派（不经过中心批准） | 效率+去中心化+省 token |
| 7 | 所有消息全量传输 | ALC 启发的分级传输（pointer/summary/full） | 省带宽+省 token+省内存 |
| 8 | 单一 git 仓库上下文 | 多类型上下文（file/command/hardware/stream） | 舵机不需要 git |
| 9 | Rust 全栈 | Go 核心 + Python 胶水扩展（subprocess 模式） | 交叉编译兼容性+开发效率+内存基线 |
| 10 | 硬绑 Claude Code + L0 规则分类器 + 独立分类/执行步骤 | 可插拔 Agent 适配层 + 统一模型入口（分类即执行）+ 统领三层能力模型 + 队列化用户面板 + 不对称管线 | 规则判断效果差+每人 Agent 偏好不同+分类执行割裂导致延迟和 token 浪费 |

### 1.3 从"香橙派为中心"到"用户选择入口"

原始文档的"香橙派=总管"已经被重新定义为：

> **香橙派是用户当前选定的默认入口调度器。它同时也是能连舵机、做嵌入式工作的执行节点。它不是永久调度器——带 Mac 出门时 Mac 就是调度器，手机也能当调度器。**

调度的基础设施（任务状态机、员工表查询、路由决策）是每个节点的 Go 核心都具备的标准能力。谁被用户选中当入口，谁就临时承担第一层意图理解和任务分解。

---

## 二、ATC-MARL 理论基础与工程映射

### 2.1 论文背景

ATC-MARL (Asynchronous Temporally Compressed Multi-Agent Reinforcement Learning with Decentralized Communication Partner Selection) —— 自适应时序压缩多智能体强化学习与去中心化通信伙伴选择。作者徐浩博，2026 年 5 月。

论文提出在 CTDE（集中式训练去中心化执行）框架下整合三个互补模块来联合解决多智能体通信的三个维度问题：

| 通信维度 | 核心问题 | 对应模块 | 优化目标 |
|---|---|---|---|
| 时序 | 何时通信？ | TMB（时序消息缓存） | 降低同步等待延迟 |
| 内容 | 传输什么？ | ALC（自适应可学习压缩） | 降低每条消息比特数 |
| 拓扑 | 与谁通信？ | DCPS（去中心化通信伙伴选择） | 降低活动通信边数量 |

论文在 MPE 协作导航基准上以 3.1% 的得分损失，换来了通信量降低 60.0%、延迟降低 40.9% 的显著效率提升。15 个种子的统计检验确认了统计显著性。

### 2.2 TMB（时序消息缓存）→ 逐发送者独立槽位 + 延迟折扣注意力

**论文核心设计**（原文 §IV-B）：

1. **逐发送者缓存（per-sender cache）**：每个智能体为每个潜在发送者维护一个独立槽位，新消息直接覆盖旧消息，不存在跨发送者淘汰。缓存容量固定为 `N`（等于智能体数量），查询复杂度 O(1)。设计优势：(a) **公平性**——每个 teammates 都有独立保留空间；(b) **新鲜度保证**——每个发送者的最新消息始终可用；(c) **恒定查询**——按发送者索引直接查找，无需搜索。

2. **延迟折扣注意力（delay-discounted attention）**：采用加法形式的延迟折扣 `−λ_d · Δt_j`，数值稳定且梯度直接。消息越旧（`Δt_j` 越大），其注意力 logit 被减去越大的惩罚值，在 softmax 归一化后获得更低权重。

3. **命题 1（TMB 延迟鲁棒性）**：陈旧消息对聚合表示的贡献上界为 `1 / (1 + (N−1)·exp(λ_d·Δt_j + Δlogit_max))`。当 `Δt → ∞` 时，陈旧消息的注意力权重趋于零。论文证明来自延迟发送者的信息最多被折扣到这个程度。

**PANDA 工程映射**（逐模块对应）：

| 论文机制 | PANDA 映射 | 实现要点 |
|---|---|---|
| 逐发送者独立槽位 | 每节点对每个已知节点维护独立消息槽 | Windows 关机了，其槽位保留上次状态，不阻塞调度 |
| 新消息覆盖旧消息 | 心跳/能力更新直接覆写对应槽位 | 不需要 FIFO 淘汰策略，避免"热节点驱逐冷节点" |
| 延迟折扣注意力 `−λ_d · Δt_j` | 心跳的时间衰减权重 | 5 分钟前的心跳比 2 小时前的心跳更有参考价值 |
| 不等待特定消息 | 调度时不要求所有节点在线 | 丢包/离线不致命：旧数据还在槽位，只是"贬值"了 |
| 异步执行（消费任意可用消息） | 调度器用当前可用信息做路由决策 | 不因等待某个节点的状态更新而阻塞任务分发 |

**PANDA 的差异**：论文在 RL 训练中学习注意力查询向量 `q_i` 和键向量 `K_{j→i}`；PANDA 使用确定性的时间衰减函数（基于 `last_seen`），不涉及可学习参数。这在 Phase 0-2 已足够；如果后续发现需要更细粒度的消息重要性评估，可引入可学习的注意力机制。

### 2.3 ALC（自适应可学习压缩）→ Straight-Through Gumbel-Softmax 分级选择

**论文核心设计**（原文 §IV-C）：

1. **多级编码器架构**：提供 `K` 个编码器 `{f_θ^(1), ..., f_θ^(K)}`，共享相同输入（智能体隐藏状态 `h_i^t`），但产生不同维度的输出：`d_1 > d_2 > ... > d_K`。共享早期层以减少参数量并促进压缩级别间的知识迁移。论文实验中 `K=4`，维度分别为 64、32、16、8。

2. **Straight-Through Gumbel-Softmax（STGS）压缩选择**：将压缩级别选择形式化为 `K` 个选项的类别决策。**前向传播**：硬选择（argmax + Gumbel 噪声），与推理完全一致，选择单一压缩级别。**反向传播**：通过 Gumbel-Softmax 分布传递梯度（"硬前向、软反向"），使网络在推理时做离散决策，训练时保持端到端梯度流。

3. **温度退火**：Gumbel 温度 `τ` 从初始 `τ_0 = 1.0` 按 `τ ← τ · 0.995` 每轮衰减，逐步逼近 one-hot 分布。训练初期 τ 高 → 接近均匀（鼓励探索），训练后期 τ 低 → 接近 argmax（收敛到最优压缩级别）。

4. **命题 2（ALC 期望通信成本上界）**：期望单消息通信成本满足 `E[bits] ≤ d_1 · 32`，通过训练驱动高压缩级别（高 `k`，小 `d_k`）被分配到非关键状态，实际成本可远低于上界。

5. **上下文自适应**：论文实验显示 32 维和 16 维消息合计占 77% 传输（而非极端全高维或全低维），高维消息（64 维）主要在精确定位和紧急协调场景被选择。

**PANDA 工程映射**（逐模块对应）：

| 论文机制 | PANDA 映射 | 实现要点 |
|---|---|---|
| 多级编码器 `K` 个输出维度 | 上下文三级传输：pointer/summary/full | 每级对应不同的数据量；pointer ≈ 64B（最低维），full ≈ 1-50KB（最高维） |
| STGS 硬前向软反向 | 入口模型一次调用决定 answer/tool_call/task | 输出为离散三类，Go 核心校验后执行；不需要训练，但保留了离散选择 + 结构校验的模式 |
| 上下文自适应选择 | 根据任务复杂度和 urgency 动态选级 | 日常委派用 summary → 关键/首次委派用 full → 已有快照时用 pointer |
| 温度退火探索→收敛 | 模型调度器档位选择（后续） | 后续可引入：复杂度评分 → 模型档位映射，允许用户覆盖 |
| 命题 2 成本上界 | 分级传输的带宽上限 | pointer 最多 ~64B，summary 最多 ~500B，full ≤ 50KB；pointer 命中为零传输 |

**PANDA 的差异**：论文的 ALC 通过 RL 自动学习选择压缩级别；PANDA 的分级传输由确定性规则（是否有快照、是否首次委派）和入口模型提示词驱动。论文的 STGS 机制启发了 PANDA 的"统一入口模型三分类"设计——也是离散选择 + 结构校验的模式。如果后续引入可学习的压缩策略（如根据任务类型和网络状况自动调整上下文分级），可参考 STGS 的硬前向软反向训练方法。

### 2.4 DCPS（去中心化通信伙伴选择）→ 逐边独立建模 + 软惩罚 + 动态不对称拓扑

**论文核心设计**（原文 §IV-D）：

1. **逐边概率建模**：每条有向边的通信概率独立建模为 `p_ij = σ(logit_ij)`，不需要全局协调或子集枚举。计算复杂度 O(N²) 而非 O(2^(N−1))，对中等规模团队完全可处理。

2. **软惩罚替代硬阈值**：使用 `Σ p_ij`（概率和软惩罚）替代硬阈值计数 `#{p_ij > threshold}`。优势：(a) **端到端可微**——对 logits 处处可微，梯度通过 Sigmoid 反向传播；(b) **细粒度优化**——即使 `p_ij` 非零仍产生梯度，鼓励向 0 或 1 推进；(c) **避免梯度中断**——硬阈值在决策边界处梯度为零或不存在。

3. **自适应阈值机制**：`τ_adp = σ(−β_e · log(1/p_min − 1))`，推理时 `dcps_ij^t = 1` 当 `p_ij^t > τ_adp`。训练时使用软惩罚形式而非硬决策。

4. **命题 3（DCPS 边稀疏化保证）**：若边惩罚系数 `β_e` 足够大，活动边比例上界为 `1/β_e`。论文实验中 `β_e = 0.005`，活动边平均 5.2/12（稀疏度 56.7%，N=4）和 14.6/56（稀疏度 73.9%，N=8），验证了可扩展性。

5. **不对称和动态拓扑**：`p_ij ≠ p_ji` 自然成立（每条边独立建模），学习到的拓扑随任务阶段动态变化。论文观察到通信拓扑随回合进展演变：近距离智能体保持高频通信，远距离智能体通信稀疏化。

**PANDA 工程映射**（逐模块对应）：

| 论文机制 | PANDA 映射 | 实现要点 |
|---|---|---|
| 逐边独立决策 `p_ij = σ(logit_ij)` | 逐边委派：每节点本地决定"发给谁" | Mac 收到任务后自行决定是否转发给 Windows，不经过香橙派批准 |
| 软惩罚 `Σ p_ij` 替代硬阈值 | 委派评分的加权公式（resource_efficiency 0.4 + user_priority 0.3 + scheduler_tier 0.2 + wait_time 0.1） | 不是"发/不发"的二进制，而是连续评分排序，选出最佳设备 |
| 自适应阈值 `τ_adp` | 容量驱动的 accept/decline 判定 | 节点根据当前剩余容量自主决定接不接，不是被动等分配 |
| 不对称拓扑 `p_ij ≠ p_ji` | 委派链是单向的：香橙派→Mac→Windows | 回传沿同一链反向进行（task_result 沿 chain 回溯），但委派方向是不对称的 |
| 动态拓扑随时间变化 | 心跳驱动的节点状态动态更新 | 节点下线 → 自动跳过，恢复上线 → 自动重新纳入候选池 |
| 命题 3 稀疏化 | 员工表按部门/能力筛选 | 不是广播问全网，而是查表匹配缩小候选集 |

**与论文 DCPS 的工程差异**：

论文的边选择由神经网络学习（DCPS head 输出 logit → σ → 概率），是连续优化的产物。PANDA 的"边选择"目前是确定性的——由能力匹配 + 评分排序决定委派目标。两者共享的哲学是**去中心化逐边独立决策**和**避免硬阈值的梯度友好设计**。后续如引入可学习的通信伙伴选择（例如：训练一个小型模型预测"委派给节点 X 的任务成功率"），可直接复用 DCPS 的逐边 Sigmoid 门控 + 软惩罚公式。

### 2.5 为什么 ATC-MARL 适合作为 PANDA 的工程启发

PANDA 解决的”多设备分布式算力没有被充分利用”问题，本质上就是 ATC-MARL 所解决的三维通信效率问题在硬件层面的投射：

- **设备不总在线** = TMB 的异步消息消费。论文的逐发送者缓存结构 + 延迟折扣注意力为 PANDA 的心跳管理和节点状态衰减提供了精确的数学模型。
- **任务上下文大小差异巨大** = ALC 的自适应压缩。论文的 K 级编码器 + STGS 离散选择启发了 PANDA 的 pointer/summary/full 三级上下文和统一入口模型的三分类设计。
- **中心调度器效率低/token 消耗高** = DCPS 的去中心化逐边决策。论文的逐边 Sigmoid 门控 + 软惩罚启发了 PANDA 的逐边委派、加权评分排序和容量驱动的自主 accept/decline。

**论文实验对 PANDA 的量化参考**：论文通过 2³=8 配置的完整消融实验定量确认了三个模块的独立贡献——TMB 贡献约 46% 的延迟降低，ALC 贡献约 60% 的通信量降低，DCPS 贡献约 40% 的拓扑稀疏化。三个模块存在约 15-20% 的正协同效应（集成效果优于独立效果的简单相加）。这为 PANDA 的调度架构（去中心化选边 + 分级传输 + 异步执行）提供了独立验证：三层不是”也许有用”的猜测，而是在 MPE 基准上有统计显著性（15 个种子，p 值验证）的有效组合。

**当前表述边界**：PANDA 采用了 ATC-MARL 关于异步消费、分级传输和去中心化选边的工程启发，但当前版本不包含论文中的强化学习训练或可学习通信策略。因此，现阶段应称为”受 ATC-MARL 启发的工程映射”，而不是完整的算法落地。后续如果加入可学习的边选择、压缩策略和基准实验，再单独升级学术表述。

### 2.6 论文中的额外启发（后续演进参考）

以下论文内容为 PANDA 的后续版本提供了具体的设计参考，暂不纳入 MVP 范围。

**2.6.1 训练方法论启发**

论文的端到端训练流程（§IV-E）对 PANDA 后续的可学习调度有直接参考价值：

- **修改奖励函数**：`r'_t = r_t − β_c · Σ msg_dim(level_i) − β_e · Σ p_ij`，在任务奖励中显式减扣通信成本。PANDA 的 Token 预算系统（§13.5）已采用类似思路——成本和性能协同优化而非分别最大化。
- **PPO + GAE 训练**：使用同策略（on-policy）回合数据，含裁剪替代目标和广义优势估计。如果 PANDA 后续引入可学习的路由策略，该训练配置是可复用的工程模板。
- **温度退火调度**：`τ ← τ · 0.995` 每轮衰减，从探索（高 τ → 均匀分布）逐步收敛到利用（低 τ → argmax）。这与 PANDA Skill 系统的生成阈值退火（从低门槛到高门槛）在模式上同构。

**2.6.2 可扩展性数据**

论文在不同团队规模 `N ∈ {2, 4, 8}` 上的实验（§V-D）为 PANDA 的多节点扩展提供了基准：

| 团队规模 | 每智能体通信量 | 变化趋势 |
|---|---|---|
| N=2 | 102.5 比特 | baseline |
| N=4 | 102.5 比特 | 持平 |
| N=8 | 82.0 比特 | 反而下降 |

关键发现：DCPS 学习到的拓扑稀疏化使**每智能体通信量不随 N 增长而线性增长**，甚至略有下降。这对 PANDA 的意义：当设备从 2 台扩展到 8 台时，每个设备的消息处理负担不会等比增加——如果路由策略做得好，单节点负载可以保持稳定。

**2.6.3 模块交互的协同效应**

论文第六节（§VI-A）详细分析了三个模块的交互关系，对 PANDA 架构中各子系统的交互设计有参考价值：

- **TMB ↔ DCPS 交互**：DCPS 决定哪些边激活 → 直接影响 TMB 缓存中的消息来源。若 DCPS 长期不选节点 X，X 在 TMB 缓存中的信息将陈旧。这促使 DCPS 保持对关键节点的周期性通信。→ **PANDA 类比**：委派频率影响心跳新鲜度，进而影响调度评分——形成了”不委派→信息陈旧→评分降低→更少被委派”的负面循环。PANDA 应设置最小心跳新鲜度感知，防止关键节点被冷落。
- **ALC ↔ DCPS 交互**：系统面临”维度-拓扑”权衡——高维消息给少数伙伴 vs 低维消息给更多伙伴。论文实验显示系统倾向于后者（中等压缩 + 稀疏拓扑），这是大多数协作任务中的更优策略。→ **PANDA 类比**：任务上下文应采用 summary（中等信息量）+ 精准路由（少数候选节点），而非 full 上下文 + 全广播。
- **协同效应量化**：论文通过消融实验估算出 15-20% 的正协同效应——集成效果优于独立效果的简单相加。这验证了 PANDA 将调度、上下文传输和执行监控做在同一个 Go 核心中的一体化设计。

**2.6.4 论文的局限性在 PANDA 中的缓解**

论文本身承认的局限性（§VI-C）中，多个在 PANDA 的工程场景下得到自然缓解：

| 论文局限 | PANDA 的缓解 |
|---|---|
| 任务特异性（仅 MPE 仿真） | PANDA 面向真实异构设备，任务类型涵盖代码/命令/硬件/流式 |
| 仿真保真度（非物理硬件延迟） | PANDA 的延迟测量在真实网络（局域网/Tailscale）上进行 |
| 通信模型假设（可靠传输无丢包） | PANDA 通过 WebSocket 重连 + 幂等检测处理真实网络故障 |
| 超参数敏感性（β_c, β_e 需调参） | PANDA 的确定性规则（非 RL 训练）避免了超参数敏感性问题 |

但是，论文提出的可学习通信策略在泛化性上优于 PANDA 的确定性规则。这恰恰是后续版本引入学习机制的切入点。

---

## 三、系统总览架构

### 3.1 全景架构图

> 下图是完整产品目标架构，包含后续演进能力。当前 MVP 以 §3.4 的本地架构为准，不要求图中的服务器、语音、PWA、记忆、桌宠和多级委派同时存在。

```
┌─────────────────────────────────────────────────────────────────┐
│                         入口层                                  │
│    语音 (唤醒词 Porcupine + 云端 ASR)                            │
│    手机 PWA (Web Push + 队列面板)                                │
│    CLI (可选)                                                    │
└──────────────────────┬──────────────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────────────┐
│                 统一入口模型 (一次调用决定处理类型)                 │
│                                                                 │
│  系统提示词注入: 设备能力摘要 + Hermes 记忆(仅对话)                 │
│                                                                 │
│  模型输出三种处理类型:                                            │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ 类型 1: answer (直接回答)                                  │    │
│  │   纯信息回答，或模型可直接生成的内容                       │    │
│  │   → 先记账 → 流式输出 → 回用户（MVP）                     │    │
│  ├─────────────────────────────────────────────────────────┤    │
│  │ 类型 2: tool_call (工具调用)                              │    │
│  │   天气、提醒、硬件等副作用由 Go 核心校验后执行               │    │
│  │ 类型 3: task (长任务)                                      │    │
│  │   {kind:"task", task:{...spec...}} → Go 核心接管并委派       │    │
│  └─────────────────────────────────────────────────────────┘    │
└──────────────────────┬──────────────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────────────┐
│                     Go 核心常驻守护进程 (~6MB)                    │
│                                                                 │
│  ┌─────────────────────────────────────────────────────┐       │
│  │ 调度管线                                             │       │
│  │                                                     │       │
│  │ 收到 task JSON → 并行执行:                            │       │
│  │   ① 查 context_store (SQLite, pointer hit?)           │       │
│  │   ② 查能力目录 (MVP 本地；后续可用 HTTP API)          │       │
│  │   ③ 汇合 → 评分排序 → 选最佳设备                     │       │
│  │                                                     │       │
│  │ 选设备 → task_delegate (WebSocket, P2P, 不经过中心)   │       │
│  │                                                     │       │
│  │ 目标节点统领:                                        │       │
│  │   收到 task_delegate → 匹配三层能力                   │       │
│  │   ├── native: exec.Command → 确定性命令，不调用模型  │       │
│  │   ├── agent: 能力/用户配置选择 → adapter → CLI exec │       │
│  │   └── manual: 推通知 → 用户手动 → 标记 done          │       │
│  └─────────────────────────────────────────────────────┘       │
│                                                                 │
│  ┌─────────────────────────────────────────────────────┐       │
│  │ 防御层                                               │       │
│  │ Layer 0: 前置审查 (用户可选, 非强制)                  │       │
│  │ Layer 1: 执行监控 (范围漂移/收益递减/矛盾检测)        │       │
│  │ Layer 2: 上级决断 (3 次循环后委派链上级接管)          │       │
│  │ Layer 3: 对抗性剖析 (双模型根因分析)                  │       │
│  │ Layer 4: 用户通知 (诊断报告+可选行动方案)             │       │
│  └─────────────────────────────────────────────────────┘       │
│                                                                 │
│  ┌─────────────────────────────────────────────────────┐       │
│  │ 权限层: Tier 1(可挽回→双模型自审) / Tier 2(不可挽回→用户)│     │
│  │ 可靠性层: 幂等(UUIDv7) · 熔断器 · 超时+指数退避      │       │
│  │         崩溃恢复(SQLite WAL) · 网络分区容忍           │       │
│  │ 合并门禁: 3 个 Haiku 并行(范围/一致性/破坏性)          │       │
│  └─────────────────────────────────────────────────────┘       │
└──────────────────────┬──────────────────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────────────────┐
│                    通信层                                        │
│                                                                 │
│  ┌─────────────────────────────┐  ┌──────────────────┐          │
│  │ 当前 MVP：本地能力目录/SQLite │  │ 节点间 P2P 通信    │          │
│  │ 后续：服务器索引（可选）       │  │ WebSocket         │          │
│  │ 不承载完整任务上下文           │  │ JSON/MessagePack  │          │
│  │                               │  │ 局域网/Tailscale  │          │
│  └─────────────────────────────┘  └──────────────────┘          │
└──────────────────────┬──────────────────────────────────────────┘
                       │
        ┌──────────────┼──────────────┐
        ▼              ▼              ▼
┌───────────────┐ ┌───────────────┐ ┌───────────────┐
│ 节点: 香橙派   │ │ 节点: Mac/Win │ │ 节点: 超算     │
│ (Micro)        │ │ (Standard)   │ │ (Full)        │
│               │ │               │ │               │
│ Go 核心 ~6MB   │ │ Go 核心 ~8MB  │ │ Go 核心 ~10MB │
│               │ │               │ │               │
│ native:       │ │ native:       │ │ native:       │
│  gpio/servo   │ │  build:ios    │ │  gpu_compute  │
│  gpio/buzzer  │ │  build:macos  │ │  heavy_render │
│  voice:listen  │ │  serve:dev    │ │               │
│               │ │               │ │               │
│ agents:       │ │ agents:       │ │ agents:       │
│  opencode(轻量)│ │  claude_code   │ │  claude_code   │
│               │ │  opencode      │ │  opencode      │
│               │ │  codex         │ │  codex         │
│               │ │               │ │               │
│ manual: 无    │ │ manual:       │ │ manual:       │
│               │ │  design:figma │ │  edit:video   │
└───────────────┘ └───────────────┘ └───────────────┘

┌─────────────────────────────────────────────────────────────────┐
│                     记忆系统（双层隔离，后续演进）                 │
│                                                                 │
│  Hermes 个人助理记忆          项目记忆 (Project Memory)           │
│  ┌──────────────────┐        ┌──────────────────────┐          │
│  │ MEMORY.md (1300  │        │ project/{name}/      │          │
│  │ token 硬上限)     │        │ MEMORY.md            │          │
│  │ 用户偏好/风格/习惯│        │ 架构决策/技术栈/规范  │          │
│  │ 注入: 仅对话/短任务│       │ 注入: 仅项目任务     │          │
│  │ 严禁: 项目工作上下文│      │ 严禁: 拉取 Hermes    │          │
│  └──────────────────┘        └──────────────────────┘          │
│                                                                 │
│  唯一交叉通道: Skills（用户审批后全局可用的工作规范）               │
│                                                                 │
│  Dreaming 引擎 (OpenClaw 模式)                                  │
│  Light → REM → Deep (六维评分) → MEMORY.md 更新 + Dream Diary   │
└─────────────────────────────────────────────────────────────────┘
```

### 3.2 全局信息流（一图看全管线）

```
用户 (语音/手机/CLI)
  │
  ▼
统一入口模型 (Haiku, ~200-500 tok)
  ├── 模式1: 直接回答 ──→ 记录任务结果 → 流式输出 → 用户
  └── 模式2: 路由 JSON
        │
        ├── parallel ──→ ① 查 context_store (pointer hit?)
        │              ② 查能力目录 (谁在线+谁能干?)
        ├── 汇合 → 评分 → 路由 → task_delegate (WebSocket P2P)
        │
        ▼
目标节点统领
  ├── native → exec("xcodebuild ...") → 产物回传
  ├── agent → 能力/用户配置选择 → adapter → CLI exec → 结果回传
  └── manual → 推送通知 → 用户手动 → 标记 done
        │
        ▼
合并门禁 (3 个 Haiku 并行: 范围/一致性/破坏性, ~500ms)
  ├── 通过 → REVIEW → 用户审批
  └── 不通过 → retry (计入循环计数)
```

### 3.3 角色定义

| 角色 | 定义 | 哪个节点承担 | 何时变动 |
|---|---|---|---|
| **入口调度器 (Root Scheduler)** | 接收用户请求并创建/委派任务 | MVP 固定一个本地节点；后续可由用户切换 | 后续通过状态同步和租约切换 |
| **子调度器 (Sub-Scheduler)** | 委派后，进一步编排下级节点的节点 | 委派链的中间节点 | 每任务动态决定 |
| **执行节点 (Executor)** | 用自己的硬件/agent 执行任务的节点 | 任何有能力执行任务的节点 | 每任务动态决定 |
| **统领 (Commander)** | 每个设备的 Go 核心中的模块，管理该设备的三层能力（native/agent/manual） | **每节点都有** | 运行期间持续 |
| **能力目录 (Capability Ledger)** | 节点能力和状态的数据源 | MVP 为入口节点本地 SQLite；后续可扩展为服务器主表 | 按部署模式决定 |

**关键理解**：一个节点可以同时是入口调度器、执行节点、子调度器——角色是叠加的、临时的、任务驱动的。

### 3.4 当前本地 MVP 架构

当前实现只采用一个固定入口，避免在尚未验证任务协议前引入动态接管和公网索引：

```text
文本/CLI
   │
   ▼
固定入口节点（Mac 或香橙派）
   ├── Go Core
   ├── 本地 SQLite：任务、能力目录、事件、上下文快照
   └── 路由与租约管理
          │ WebSocket（局域网或 Tailscale）
          ├── Orange Pi：native 硬件/系统命令
          └── Mac/Windows：native 命令 + 一个已验证 Agent adapter
```

MVP 的完成标准是“任务能被可靠地接收、执行、回传、恢复和取消”，不是“所有入口和所有节点都能动态互换”。动态入口、多级委派、手机/语音和服务器索引属于后续版本。

---

## 四、语言与技术栈终裁

### 4.1 决策：Go 核心 + Python 胶水扩展

经过 Rust / Python / Go 三方对比，最终裁决：

| 维度 | Rust | Python | Go（选中） |
|---|---|---|---|
| aarch64 交叉编译 | 需 target triple + linker 配置，已知痛点 | N/A（解释型） | `GOOS=linux GOARCH=arm64 go build` 一行 |
| 静态二进制部署 | ✅ | ❌ 需要 Python 运行时 | ✅ |
| 内存基线（core 启动） | 2-5 MB | 15-20 MB | **5-8 MB** |
| GC 暂停 | 无 | 有 | 并发，亚毫秒级 |
| 并发模型 | async/await | asyncio（同步阻塞会卡 event loop） | goroutine（2KB 初始栈，天然非阻塞） |
| 错误处理 | Result/Option | 异常（asyncio 下容易丢失） | 显式 `if err != nil`（强制每层处理） |
| 动态加载扩展 | 无（plugin 包在 arm64 有 bug） | `importlib` 天然支持 | 不支持，改为 **subprocess 模式** |
| 开发速度 | 慢 3-5x | 最快 | 快 |

### 4.2 为什么不用 Rust

原文档选择 Rust 的理由是"纯 Rust crate 避免交叉编译 C 依赖坑"。但在经过 117club 项目的实战踩坑（memory 中记录了 better-sqlite3 的 Mac 编译产物在 VM 里 ERR_DLOPEN_FAILED）后，结论是：**Rust 的 aarch64 交叉编译本身就是一个坑**。Go 的交叉编译是一行命令，输出纯静态二进制，零运行时依赖。对于需要部署到 aarch64（香橙派/树莓派）、arm64（Mac M1）、amd64（Windows/超算/server）的异构设备集群，这是压倒性优势。

### 4.3 Rust 的保留角色

不排除未来可能性。如果 Python 胶水脚本的性能成为瓶颈，可以将特定的高频扩展（如语音 VAD、GPIO PWM 时序控制）用 Rust 重写并作为 sidecar 进程调用。**但核心调度层锁死 Go，不会回退到 Rust 或 Python。**

### 4.4 目录结构

```
panda/
├── cmd/
│   └── panda/               # Go 核心守护进程入口
│       └── main.go
├── internal/
│   ├── core/                # 核心层
│   │   ├── beat.go          # 心跳 + 能力卡上报
│   │   ├── recv.go          # WebSocket 接收器
│   │   ├── router.go        # 消息分发
│   │   ├── state.go         # SQLite 任务状态机
│   │   ├── ledger.go        # 员工表 API 客户端
│   │   └── context.go       # 上下文存储（hash→data KV）
│   ├── commander/           # 统领层
│   │   ├── capability.go    # 三层能力匹配（native/agent/manual）
│   │   ├── agent_selector.go # Agent 选择（Haiku 调用）
│   │   ├── native.go        # 确定性命令执行器
│   │   └── manual.go        # 人工任务通知
│   ├── entry/               # 入口层
│   │   ├── prompt.go        # 系统提示词构造
│   │   ├── model.go         # 统一入口模型调用
│   │   └── router.go        # 模型路由 JSON → 管线
│   ├── scheduler/           # 调度器
│   │   ├── routing.go       # DCPS 式逐边路由决策
│   │   ├── tree.go          # 任务内部结构管理
│   │   └── delegate.go      # 委派协议
│   ├── defense/             # 防御层
│   │   ├── circuit.go       # 熔断器
│   │   ├── loopdetect.go    # 循环检测
│   │   ├── preflight.go     # 前置审查（可选）
│   │   └── postmortem.go    # 对抗性剖析
│   ├── security/            # 安全层
│   │   ├── sandbox.go       # 执行沙箱
│   │   ├── permissions.go   # Tier 1/Tier 2 权限判定
│   │   └── audit.go         # 高风险操作审计
│   ├── bus/                 # 通信层
│   │   ├── ws.go            # WebSocket 管理
│   │   ├── msgpack.go       # MessagePack 序列化
│   │   └── tailscale.go     # Tailscale API 集成
│   └── storage/             # 存储层
│       ├── sqlite.go        # SQLite WAL 封装
│       └── migrate.go       # Schema 迁移
├── adapters/                # Agent 适配器 (Python, ~30 行每个)
│   ├── claude_code.py       # Claude Code CLI
│   ├── codex.py             # Codex CLI
│   ├── opencode.py          # OpenCode CLI
│   ├── continue.py          # Continue CLI
│   └── vtx.py               # Vtx Coding Agent
├── extensions/              # 扩展进程 (Python, sidecar 模式)
│   ├── voice/               # 语音扩展
│   │   ├── wake.py          # Porcupine 唤醒词
│   │   └── stt.py           # 云端 ASR
│   ├── gpio/                # 嵌入式扩展
│   │   └── servo.py         # 舵机/PWM 控制
│   └── dreamer/             # 梦境引擎
│       ├── light.py
│       ├── rem.py
│       └── deep.py
├── web/
│   └── pwa/                 # 手机 PWA 控制台
├── skills/
│   └── {skill-name}/SKILL.md
├── memory/
│   ├── MEMORY.md
│   ├── daily/YYYY-MM-DD.md
│   ├── dreams/
│   └── DREAMS.md
├── projects/{name}/MEMORY.md
├── docs/
│   ├── ARCHITECTURE.md
│   └── PROTOCOL.md
├── scripts/
│   ├── install.sh           # 安装脚本 (扫描 agent + 询问安装)
│   ├── join.sh              # 新节点入职
│   └── tailscale_up.sh      # Tailscale 组网
├── go.mod
├── go.sum
└── Makefile
```

### 4.5 核心与扩展的通信

Go 核心不通过 importlib 加载 Python 模块，而是**直接 subprocess 调用 Python 脚本**：

```
Go Core → exec.Command("python3", "adapters/claude_code.py", "--prompt", prompt)
        → stdin: prompt JSON
        → stdout: result JSON
        → 进程自然消亡, 内存完美回收
```

对于需要长期运行的扩展（如语音唤醒），通过 **Unix domain socket** 与 Go 核心通信：

```
Go Core ←→ 扩展进程 (sidecar)
    ├── spawn: Go 启动子进程, 传 --socket=/tmp/panda-ext.sock
    ├── register: 扩展连接 socket, 上报能力
    ├── task_dispatch: Go 转发任务, 扩展执行
    └── health_check: Go 定期 ping, 崩溃自动重启
```

选择 Unix domain socket 的理由：延迟 ~10-20μs（比 localhost TCP 快 5-10x）、无端口冲突、进程崩溃时 socket 自动断开、Go 和 Python 都是一行代码就能用。

---

## 五、核心层：Go 常驻守护进程

### 5.1 核心层的精确边界

核心层只做以下职责。除此之外的功能全是扩展或上层模块：

1. **节点状态维护**：MVP 在本地目录维护节点状态；后续可每 30s 向员工表 API 发送心跳，更新 status/capacity/last_seen
2. **消息接收**：维护 WebSocket 长连接，接收来自其他节点的消息
3. **消息分发**：根据消息 type 字段路由——唤醒对应扩展进程或直接处理
4. **最小状态机**：记录本节点参与的任务（task_id + state + 委派链中的位置）
5. **能力目录查询**：MVP 查询本地 SQLite 能力目录；后续才通过 HTTP API 拉取集中式员工表快照

### 5.2 内存基线（关键性能指标）

| 场景 | 内存占用 |
|---|---|
| Go 核心（启动后，0 扩展） | **5-8 MB** |
| + Python subprocess（一次性 agent adapter） | +15-25 MB（进程存活期间，跑完回收） |
| + voice_ext（常驻 sidecar，仅 Micro 按需加载） | +15-20 MB |
| 香橙派 Micro 峰值（core + voice + gpio） | **~30 MB** |
| Mac/Win Standard 常驻（core） | **~8 MB** |
| 超算 Full 常驻（core + 全扩展） | **~80-200 MB** |

**对比原文档**：原文档估计香橙派 `<80MB` 常驻。新方案香橙派常驻 ~6MB（core 空载状态），峰值 ~30MB。核心常驻不到原估计的 10%。

### 5.3 数据库表

每节点本地 SQLite（WAL 模式）：

```sql
-- 员工表快照（从服务器拉取的缓存）
CREATE TABLE employee_cache (
  id TEXT PRIMARY KEY,
  name TEXT, department TEXT, chip TEXT,
  native_json TEXT, agents_json TEXT, manual_json TEXT,
  capacity_json TEXT,
  status TEXT, last_seen INTEGER,
  scheduler_tier INTEGER
);

-- 任务表（本节点参与的任务）
CREATE TABLE tasks (
  task_id TEXT PRIMARY KEY,     -- UUIDv7，全局唯一，幂等键
  parent_id TEXT,               -- 父任务（任务内部结构）
  project TEXT,                 -- 所属项目
  title TEXT,                   -- 任务标题
  state TEXT NOT NULL,           -- submitted|queued|dispatched|running|waiting_context|review|done|failed|cancelled|expired
  owner_node TEXT NOT NULL,      -- 当前任务租约持有者
  attempt_id TEXT NOT NULL,      -- 每次执行尝试唯一；transfer/retry 必须新建
  state_version INTEGER NOT NULL DEFAULT 0,
  lease_expires_at INTEGER,
  chain_json TEXT,              -- 委派链 ["node_a", "node_b", ...]
  context_type TEXT,            -- file|command|hardware|stream
  context_hash TEXT,            -- full 完整上下文的 SHA-256 摘要
  intent TEXT,                  -- 精炼后的任务意图
  spec_json TEXT,               -- 结构化 spec
  result_json TEXT,             -- 执行结果
  complexity REAL,              -- 复杂度评分 0.0-1.0（非风险评分）
  risk TEXT,                    -- low|medium|high|critical
  resource_json TEXT,           -- CPU/RAM/GPU/时长等资源画像
  model_tier INT,               -- 使用的模型档位；命名避免与节点等级复用
  created_at INTEGER,
  updated_at INTEGER
);

-- 任务事件日志（审计/可重放）
CREATE TABLE task_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT, ts INTEGER,
  type TEXT,                    -- submit|queue|delegate|accept|decline|progress|result|review|retry|transfer|cancel|expire
  data_json TEXT
);

-- 上下文存储（hash→data KV，LRU 驱逐）
CREATE TABLE context (
  ctx_hash TEXT PRIMARY KEY,    -- SHA-256
  ctx_type TEXT,                -- file|command|hardware|stream
  data_blob BLOB,
  refs_json TEXT,
  created_at INTEGER,
  last_access INTEGER,
  access_count INTEGER DEFAULT 0
);

-- 父子归属与执行依赖分开表达，支持 DAG
CREATE TABLE task_dependencies (
  task_id TEXT NOT NULL,
  depends_on_task_id TEXT NOT NULL,
  condition TEXT NOT NULL DEFAULT 'success',
  PRIMARY KEY (task_id, depends_on_task_id)
);

-- 熔断器状态
CREATE TABLE circuit_breakers (
  id TEXT PRIMARY KEY,          -- agent:task_type 组合
  failure_count INTEGER DEFAULT 0,
  last_failure INTEGER,
  state TEXT DEFAULT 'closed',  -- closed|open|half_open
  cooldown_until INTEGER
);
```

### 5.3.1 任务状态、所有权与幂等契约（当前基线）

任务状态不是展示标签，而是跨节点协议的一部分。每个任务在任意时刻只有一个 `owner_node` 和一个有效的 `attempt_id`。状态更新必须携带 `state_version`，接收方只接受版本递增且满足状态转移规则的事件。

| 当前状态 | 允许的下一状态 | 说明 |
|---|---|---|
| `submitted` | `queued`, `cancelled` | 入口已接收，尚未分配执行者 |
| `queued` | `dispatched`, `cancelled`, `expired` | 等待资源或上下文 |
| `dispatched` | `running`, `waiting_context`, `queued`, `failed` | 已发送委派，等待接收或重试 |
| `waiting_context` | `running`, `failed`, `expired` | 等待上下文快照，不得假装已开始 |
| `running` | `review`, `done`, `failed`, `cancelled`, `queued` | 执行中；重试/转移必须创建新的 `attempt_id` |
| `review` | `done`, `queued`, `failed`, `cancelled` | 等待确定性检查或用户决定 |
| `done` | 无 | 终态 |
| `failed` | `queued`, `cancelled`, `expired` | 是否重试由上级按策略决定 |
| `cancelled` | 无 | 终态；取消必须向子任务传播 |
| `expired` | 无 | 超过 deadline 或审批租约到期 |

`task_id` 用于标识逻辑任务，`attempt_id` 用于标识一次实际执行。重复的 `accept`、`progress` 或 `result` 事件必须幂等处理；旧 `attempt_id` 的迟到结果不得覆盖新尝试。任务转移前，原节点必须先失去租约或被明确取消，以避免双重执行。

### 5.3.2 当前本地部署的控制面

MVP 不依赖公网服务器。入口节点维护本地 `employee_cache`/能力目录，并通过节点启动注册和周期性本地心跳更新状态。未来引入集中式员工表时，它只是控制面目录；任务结果、上下文和实时进度仍通过节点间连接传输。

### 5.4 异构设备的存储与内存策略（后续扩展设计）

PANDA 面向从 256MB 树莓派到数 TB 超算的全频谱设备。存储和内存策略必须分级，不能一刀切。

**内存分层策略**：

| 设备等级 | 物理内存 | 内存策略 |
|---|---|---|
| **Micro**（香橙派/树莓派） | 256MB - 4GB | zram swap（压缩存储在 RAM 中，2:1~3:1 压缩比）+ **禁止 SD 卡 swap**（磨损风险，延迟 ~5ms vs zram ~0.001ms）。M.2 到货后开 M.2 swap 做二级缓冲（zram priority high + M.2 priority low）。物理内存 2GB + zram 984MB = 等效 ~2.5-3GB。Go 核心 ~6MB + Py subprocess 瞬态 ~20MB = 峰值 ~50MB，安全净空 > 1.8GB。 |
| **Standard**（Mac/个人 PC） | 8-32GB | swap 无所谓（macOS 和 Windows 各自管理），PANDA 核心约 8MB。 |
| **Full**（超算/工作站） | 32GB+ | 不需要特殊策略。 |

**存储介质策略**：

| 设备等级 | 主存储 | 日志/任务数据存储 | 限制 |
|---|---|---|---|
| **Micro** | SD 卡（系统盘） | U 盘或 M.2（推荐），不写 SD 卡 | SD 卡只读为主。日志/上下文/任务数据重定向到 U 盘/M.2，保护 SD 卡寿命。无 M.2 时用 U 盘过渡（但 USB 口凸出易碰断、长期读写发热掉速、供电不稳，不推荐长期用）。 |
| **Standard** | SSD/NVMe | 同盘（有磨损均衡） | 无特殊限制。context_store 最多 50 条。 |
| **Full** | NVMe/分布式存储 | 同盘 | 无限制。context_store 不限条数。 |

**香橙派具体的 zram 配置**（当前状态）：

```
物理内存: 1.9 GB
zram swap: 984 MB (压缩存储在 RAM 中, 压缩比 2:1~3:1)
swappiness: 100 (积极使用 swap 防止 OOM)
持久化配置: /etc/sysctl.conf → vm.swappiness=100

有效可用内存: 1.9 + 984×(压缩比) ≈ 2.5~3.0 GB 等效
PANDA Go 核心: ~6MB 常驻 + Python subprocess 瞬态 ~20MB
安全净空: > 1.8 GB (给系统、网络缓冲、API 响应)
```

**M.2 到货后的 swap 分层配置**：

```bash
# zram 优先 (已在运行)
# M.2 添加二级 swap
mkswap /dev/nvme0n1p1
swapon --priority 0 /dev/nvme0n1p1   # zram 默认 priority 更高, 优先用 zram
```

**SD 卡保护铁律（来自 117club 项目踩坑确认）**：

1. **绝不在 VM 里写宿主 SD 卡数据库**（宿主机可能正在运行时写 → WAL/journal 不一致 → 主库损坏）
2. 日志/任务数据/上下文全迁 U 盘或 M.2（不写 SD 卡）
3. SQLite WAL 模式跨进程/跨 FUSE 并发写 = 损坏风险。生产环境建议 DELETE journal 或单写者
4. SD 卡没有高级磨损均衡，持续随机写入会在几周到几个月内烧穿特定区块 → 系统崩

**这就是"越智能越好"在存储层的回应**：策略是分层（zram 优先 → M.2 溢出 → SD 卡禁用），加上主动卸载（Micro 扩展用完即卸）。具体压缩比、峰值内存和长期稳定性需要在目标硬件上实测。

### 5.5 香橙派作为 24h 入口设备的假设

当前设计默认香橙派是 24h 在线的入口调度器，基于以下前提（如前提变化需重新评估）：

1. **低功耗**：RK3566 待机 < 2W，可长期通电
2. **不动**：放在固定位置，不随身携带
3. **有网**：网线（优先）或 WiFi 自动连接手机热点，两种网络模式覆盖家+学校
4. **可替代**：香橙派离线时，任何其他设备（Mac/手机）可立即接管入口调度器角色。入口调度器不是设备属性

如果某个部署场景不满足前提（如纯移动设备集群、无固定电源、所有设备都可能休眠），需重新评估入口选择策略。

### 5.6 硬件抽象层（HAL）与桌宠硬件配套

**为什么需要 HAL**：这些设备都有操作系统（Armbian/macOS/Windows），都能跑一个 Go 静态二进制。但"面向硬件设备"的系统，价值恰恰在于它感知和控制硬件，而不只是在一堆电脑上发消息。硬件差异需要被软件标准化地感知和驱动。

**三层硬件职责**（属引擎的一部分，独立于桌宠形态）：

#### 1. 硬件发现（`panda detect` 替代手写能力卡）

```
panda detect
       │
       ▼
自动扫描:
  ├── CPU: RK3566, 4 核, 1.8GHz
  ├── RAM: 2GB
  ├── Storage: SD 8GB, M.2 未检测到
  ├── GPU: RK3566 集成 Mali-G52 (不支持 CUDA)
  ├── Display: SPI 240x240 IPS LCD ✓
  ├── Servo: GPIO 12/13/18 检测到 PWM ✓
  ├── Mic: USB 麦克风 ✓
  ├── Speaker: GPIO 功放 ✓
  ├── Battery: 未检测到
  ├── Temp sensor: 可用 ✓
  └── Lux sensor: I2C 检测到 ✓
       │
       ▼
生成 capabilities.yaml + hardware.yaml
用户只需确认, 不用手写
```

#### 2. 硬件状态上报（心跳里带实时状态）

```
// 心跳消息 v4 格式
{
  "n": "orangepi3b",
  "s": "online",
  "hw": {
    "temp_c": 52,           // CPU 温度 (85°C 降权)
    "cpu_load": 0.3,
    "ram_free_mb": 1400,
    "battery_pct": null,    // 无电池
    "display_on": true,
    "servo_pos": [90, 45, 0]  // 当前姿态
  },
  "l": 0.15,                // 综合负载
  "ts": 1723456789
}
```

调度器评分函数消费这些数据：温度 >80°C 的设备自动降权；GPU 利用率 >90% 的不接新任务。

#### 3. 硬件控制驱动（标准化驱动接口）

```
硬件驱动接口 (Go 核心 → sidecar 进程):
  /dev/servo/rotate  {pin, angle, speed}
  /dev/display/draw  {image, duration}
  /dev/audio/play    {file, volume}
  /dev/mic/record    {duration}
  /dev/gpio/read     {pin}
  /dev/gpio/write    {pin, value}
  /dev/sensor/temp   {}
  /dev/sensor/lux    {}
```

每个设备声明自己能驱动的硬件类型（`peripherals` + `sensors`），调度器据此才知道"能控舵机的只有香橙派"。

#### 桌宠（Desk Pet）硬件配套规划

桌宠是引擎的硬件载体（见 §0.4 两个结构）——它给引擎做硬件处理、为未来延展到真实物理世界做储备。硬件方案**保持开放**，等硬件到位再定。当前规划：

| 部件 | 方案选项 | 状态 |
|---|---|---|
| **屏幕（表情）** | 墨水屏 / 1.3-2.4 寸 IPS LCD / 无屏纯机械 | **开放**，不锁死 |
| **动作自由度** | 2-3 舵机（头部/耳朵/身体）→ 后续可扩展 5+ | **开放**，后续考虑 |
| **麦克风（耳朵）** | USB 麦克风 / 3.5mm 模拟 | 确认 |
| **喇叭（嘴巴）** | GPIO 蜂鸣器 / 小喇叭 + 功放 | 确认 |
| **供电** | Type-C 直插（推荐起步）/ 移动电源卡槽 / 18650+升压 | 按移动形态演进 |
| **外壳** | 3D 打印宠物造型，预留屏幕/舵机/喇叭/散热开孔 | 硬件齐后设计 |
| **存储** | 首选 M.2（板载 M-Key），过渡用 U 盘挂 /data | 确认 |

**表情状态映射**（桌宠的反馈通道）：

| 状态 | 屏幕表情（方案开放） | 舵机动作（方案开放） |
|---|---|---|
| `idle` | 眯眯眼，慢眨眼 | 身体微微起伏 |
| `listening` | 大眼，竖耳朵 | 头转向用户 |
| `thinking` | 眼睛转圈圈 | 耳朵微颤 |
| `working` | 专注眼 | 低头，身体前倾 |
| `done` | 开心弯眼 | 头抬起，耳朵立起 |
| `error` | 难过眼 + 问号 | 垂头 |
| `waiting_review` | 期待眼，看向你 | 身体转向你 |
| `offline` | 困倦眼 | 慢慢垂头 |

**桌宠的价值**：用户不需要打开手机看队列，看一眼宠物表情就知道系统在干嘛。这是"桌面宠物"相比"命令行面板"的核心价值——它是状态反馈通道，也是产品 IP。

```

### 6.1 三层能力的定义

统领（Go 核心中的 commander 模块）管理的是**每个设备上三种性质完全不同的能力**。不是只管 Agent：

| 层 | 性质 | 延迟 | Token | 确定性 | 举例 |
|---|---|---|---|---|---|
| **native** | 已声明且受控的确定性命令 | 毫秒-秒 | 通常不需要模型调用 | 命令本身可重复，环境仍可能失败 | `xcodebuild`, `npm run build`, GPIO PWM, `npx eslint` |
| **agent** | AI 推理驱动 | 分钟-小时 | 有 | 非确定 | Claude Code 改代码, OpenCode 研究, Codex 调试 |
| **manual** | 人类操作 | 不定 | 不需要模型调用 | 结果取决于人工确认 | Figma 设计, DaVinci 剪辑, Blender 建模 |

### 6.2 能力卡结构（v4 格式）

```yaml
# /etc/panda/capabilities.yaml
device: macbook-m1
resource_class: Standard
chip: "Apple M1 · 16GB RAM · 256GB SSD"

native:
  - id: build:ios
    command: xcodebuild
    args: ["-workspace", "{workspace}", "-scheme", "{scheme}", "-sdk", "iphoneos"]
    description: "构建 iOS 应用"
  - id: build:macos
    command: swift
    args: ["build", "-c", "release"]
    description: "构建 macOS 应用"
  - id: test:ios
    command: xcodebuild
    args: ["test", "-scheme", "{scheme}", "-destination", "platform=iOS Simulator"]
  - id: serve:dev
    command: npm
    args: ["run", "dev"]
  - id: lint
    command: npx
    args: ["eslint", "{files}"]
  - id: format
    command: npx
    args: ["prettier", "--write", "{files}"]

agents:
  claude_code:
    adapter: claude_code.py
    install_check: "which claude"
    capabilities: [code:modify, code:review, code:debug, code:refactor, file:analyze, test:generate, docs:generate]
    best_at: [complex_refactor, system_design, security_audit]
    not_for: [simple_rename, web_scraping]
    max_context: 200000
    cost_tier: medium_high
    model_tiers: [L2, L3, L4, L5]  # 后续模型档位；MVP 不自动选择
  opencode:
    adapter: opencode.py
    install_check: "which opencode"
    capabilities: [code:modify, code:review, web:search, web:fetch, browser:automate]
    best_at: [web_research, multi_file_edit, quick_fix]
    not_for: [system_design]
    model_agnostic: true
    cost_tier: low_medium
    model_tiers: [L1, L2, L3, L4, L5]  # 后续模型档位；MVP 不自动选择
  codex:
    adapter: codex.py
    install_check: "which codex"
    capabilities: [code:modify, code:review, code:debug]
    best_at: [quick_fix, test:generate]
    not_for: [system_design, security_audit]
    cost_tier: medium
    model_tiers: [L1, L2, L3]  # 后续模型档位；MVP 不自动选择

manual:
  - id: design:figma
    notify: "请打开 Figma 并手动完成: {description}"
  - id: edit:video
    notify: "请打开 DaVinci Resolve 并手动完成: {description}"

capacity:
  cpu_cores: {total: 8, available: 6}
  ram_gb: {total: 16, available: 10}
  max_concurrent_tasks: 3
  current_tasks: 1
```

### 6.3 容量的并行调度与多任务冲突裁决（后续增强）

**核心观点**（用户原话）："一个员工不一定只能做一个任务啊，比如我的 Windows，这个性能很强，同时跑渲染和训练任务好像能扛住，就是可以并行处理，就和一下子给员工派两三个活一样，看着来呗。"

节点采用**容量驱动并行**而非简单的 idle/busy 二进制状态：

```go
// 执行节点收到 task_delegate 时的接受判定
func (c *Commander) AcceptOrQueue(task Task) Decision {
    required := task.Requires
    available := c.currentCapacity.available()
    
    // 直接够了 → 接受（支持并行执行多个任务）
    if available.cpuCores >= required.minCpuCores &&
       available.ramGB >= required.minRamGB &&
       available.gpuVRAM >= required.minGpuVRAM {
        c.allocateCapacity(required)
        return Decision{Action: "accept"}
    }
    
    // 不够 → 进队列，按加权评分排序
    score := computePriority(task)
    enqueue(task, score)
    return Decision{Action: "queued", Position: len(c.taskQueue)}
}
```

**权重公式**（依据用户要求）：

```go
func computePriority(task Task) float64 {
    w1 := float64(task.UserPriority)        // 用户指定: 0-10, 默认 5
    w2 := float64(task.SchedulerTier)       // 调度器层级: 根调度器=10, 子调度器=5, 代理=1
    w3 := -float64(task.WaitTimeSeconds())   // 等待越久越优先 (防饥饿)
    w4 := task.ResourceEfficiency()          // 该任务对此节点的资源利用率 (越高越好)
    
    return 0.3*w1 + 0.2*w2 + 0.1*w3 + 0.4*w4
}
```

各维度权重依据：
- `resource_efficiency` 权重最高（0.4）：完美匹配节点能力的任务（如 GPU 训练÷4060 节点）优先于泛用任务。映射论文 DCPS 的"选择使信息收益最大化的边"——选能把节点能力发挥到最大的任务。
- `user_priority` 次之（0.3）：用户明确提升了优先级的任务插队。
- `scheduler_tier`（0.2）：根调度器直接委派的任务略优于深度委派的子任务。
- `wait_time`（0.1）：防止排在最末尾的任务永远排不到。

**同分时 FIFO**（用户原话："优先级一样就 FIFO"）——评分相同的任务按 `enqueue_time` 排序，先进先出。不做更复杂的策略。

**后续转移规则**：任务在某节点排队超过阈值，且其他节点满足资源要求时，可由原调度器发起 `transfer`。MVP 不自动转移，只记录 queued 状态，避免重复执行。

### 6.4 统领的路由决策（MVP 先做能力匹配，复杂调度后续增强）

```go
func (c *Commander) Route(task Task) (ExecutionPlan, error) {
    // 1. 匹配三层能力
    candidates := c.matchCapabilities(task.Requires.Abilities)
    
    // 2. 选择执行方式
    switch {
    case task.AllowsNative && len(candidates.native) > 0:
        // 仅当任务明确匹配能力卡和前置条件时使用确定性命令
        return c.execNative(candidates.native[0], task)
        
    case len(candidates.agents) > 0:
        // 其次: Agent（需要推理的任务）
        agent := c.selectAgent(candidates.agents, task) // MVP 按配置；后续可由模型选择
        return c.execAgent(agent, task)
        
    case len(candidates.manual) > 0:
        // 最后: 人工（推通知给用户）
        return c.notifyManual(candidates.manual[0], task)
        
    default:
        return ExecutionPlan{}, ErrNoCapability
    }
}
```

### 6.5 Agent 适配器：可插拔架构

**设计原则**：每个人喜欢用的 Agent 不同。系统不硬绑定任何单一 Agent CLI。安装时自动扫描 + 询问安装；运行时统领动态选择。

**适配器模板**（`adapters/claude_code.py`，~30 行）：

```python
"""Adapter: Claude Code CLI → PANDA Commander"""
import sys, json, subprocess, os

prompt = sys.stdin.read()
cfg = json.loads(open("/etc/panda/capabilities.yaml").read())
agent = cfg["agents"]["claude_code"]

cmd = [
    "claude", "-p", prompt,
    "--output-format", "json",
    "--allowedTools", "Read,Write,Edit,Bash,Grep,Glob",
    "--max-turns", str(cfg.get("max_turns", 30)),
    "--permission-mode", "acceptEdits"
]

env = os.environ.copy()
env["ANTHROPIC_API_KEY"] = os.environ["ANTHROPIC_API_KEY"]  # Go 核心注入

result = subprocess.run(cmd, capture_output=True, text=True, timeout=600, env=env)
output = json.loads(result.stdout)

print(json.dumps({
    "ok": result.returncode == 0,
    "result": output.get("result", ""),
    "tokens": output.get("total_tokens", 0),
    "cost": output.get("total_cost_usd", 0),
    "exit_code": result.returncode
}))
```

**适配器模板**（`adapters/opencode.py`，~25 行）：

```python
"""Adapter: OpenCode CLI → PANDA Commander"""
import sys, json, subprocess

prompt = sys.stdin.read()

cmd = ["opencode", "run", "--model", "claude-sonnet-4-20250514", prompt]
result = subprocess.run(cmd, capture_output=True, text=True, timeout=600)

print(json.dumps({
    "ok": result.returncode == 0,
    "result": result.stdout.strip(),
    "exit_code": result.returncode
}))
```

每个适配器 ~30 行。新增一个 Agent CLI 只需：复制模板 → 改三行（命令、参数、输出解析）→ 注册到 capabilities.yaml。

### 6.6 安装时的 Agent 扫描

```
panda install
       │
       ▼
1. 检测设备类型
   ├── 嵌入式 (GPIO, <2GB RAM) → 跳过 agent 扫描，仅注册 native 能力
   └── 通用 (Mac/Win/Linux/超算)
       │
       ▼
2. 扫描已安装的 Agent CLI
   which claude && echo "✅ Claude Code"
   which codex && echo "✅ Codex CLI"  
   which opencode && echo "✅ OpenCode (开源, 推荐!)"
   which cn && echo "✅ Continue CLI"
   which vtx && echo "✅ Vtx Coding Agent"
       │
       ▼
3. 列出扫描结果 + 推荐安装
   ┌─────────────────────────────────────────┐
   │ 🔍 已检测到以下 Agent CLI:                 │
   │                                          │
   │ ✅ Claude Code (claude -p)                │
   │ ✅ Codex CLI (codex exec)                 │
   │ ❌ OpenCode (未安装) — 推荐! 开源, 75+ 模型│
   │ ❌ Continue CLI (未安装)                  │
   │                                          │
   │ 是否安装推荐的 Agent?                      │
   │ [安装 OpenCode] [安装全部开源] [跳过]       │
   └─────────────────────────────────────────┘
       │
       ▼
4. 生成能力卡 (capabilities.yaml)
   - native 能力自动检测 (which xcodebuild/npm/go/...)
   - agent 能力基于扫描结果 + 用户选择
   - manual 能力需要用户手动配置
```

### 6.7 Agent 选择（模型驱动，非规则判断）

后续版本中，统领可调用轻量模型选择最合适的 Agent；MVP 先按用户配置和能力声明选择，不引入额外模型调用：

```
Prompt to Haiku:
  本设备安装了以下 Agent:
    1. claude_code: 代码修改、审查、调试、重构 (擅长: 复杂重构、系统设计、安全审计)
    2. opencode: 代码修改、审查、Web搜索 (擅长: Web研究、多文件编辑、快速修复, 开源+任意模型)
    3. codex: 代码修改、审查、调试 (擅长: 快速修复、测试生成)

  任务: "重构支付模块的状态机，保持 API 兼容"
  复杂度: 0.7 (高)
  任务类型: code:modify
  用户偏好 agent: 未指定

  选择最合适的 Agent。输出 JSON: {"agent": "...", "reason": "..."}
```

**注意**：这不是硬编码规则判断。这是模型驱动的上下文相关选择——每台设备、每个任务、每个时刻的 agent 选择基于当前组合（能力匹配 + 任务需求 + agent 专长 + 成本偏好）。和"if 重构 in intent: use claude_code" 有本质区别。

如果用户明确指定了 agent（"用 Claude Code 帮我..."），跳过选择，直接路由。若指定 Agent 不存在，必须明确拒绝并说明原因。

### 6.8 当前可集成的 Agent CLI 全景

经调研确认的 headless CLI agent（按成熟度排序）：

| Agent | Headless 命令 | 许可 | 成熟度 | 支持的 Provider |
|---|---|---|---|---|
| **Claude Code** | `claude -p "{prompt}" --output-format json` | Proprietary | ⭐⭐⭐⭐⭐ 官方维护，SDK 完善 | Anthropic |
| **Codex CLI** | `codex exec "{prompt}" --json` | Proprietary | ⭐⭐⭐⭐ 官方维护 | OpenAI |
| **OpenCode** | `opencode run --model {model} "{prompt}"` | MIT, 147K⭐ | ⭐⭐⭐⭐ 生态最丰富 | **75+** 提供商（含本地） |
| **Continue CLI** | `cn -p "{prompt}" --format json` | Apache 2.0 | ⭐⭐⭐ | 多 provider |
| **Vtx** | `vtx -p "{prompt}"` | 开源 | ⭐⭐⭐ | **50+** provider |
| **ShipAny Code** | `shipany-code -p "{prompt}"` | 开源 | ⭐⭐ | MCP 支持 |
| **smolcode** | `smolcode "{prompt}" --no-tui` | 开源（Rust） | ⭐⭐ | Ollama/本地模型 |

**OpenCode（MIT, 147K GitHub stars）是最理想的开源默认选择**——支持 75+ 模型提供商、MCP 工具集成、自定义 agent 定义、plugin 系统。不绑定任何单一模型提供商。

### 6.9 合规声明：Headless CLI 调用的合法性

本节明确 PANDA 通过 headless/subprocess 调用 Agent CLI 的合规立场。

**合规的三条原则**：

1. **只使用官方文档化的 headless 接口**。Claude Code 的 `-p`（print mode，非交互式）和 `--output-format json` 是 Anthropic 官方设计用于脚本化和 CI/CD 管道的功能，属于公开 API。Codex CLI 的 `codex exec` 是 OpenAI 官方设计用于 headless 自动化的命令。OpenCode 的 `opencode run` 是 MIT 许可的开源项目的公开命令。PANDA 不进行屏幕抓取、逆向工程、非公开 API 调用。

2. **不使用同一 API key 并发跑多个实例**。每个设备使用自己的 API key。按需创建 session，用完即关。避免触发并发限制。

3. **权限隔离**。Agent 进程通过环境变量注入密钥（`os.environ["ANTHROPIC_API_KEY"]`），进程结束后环境变量随进程消亡。Go 核心不将密钥明文存储在任何文件中。

**注意**：Claude Code SDK 自 2026 年 6 月 15 日起有独立的月度 Credits 限额（Pro $20/月, Max $100/月）。PANDA 用户需确认自己的订阅计划覆盖 SDK/API 用量。这不影响架构——只是成本管理，在 Token 预算系统中已有框架支撑。

---

## 七、统一入口模型：决定处理类型

### 7.1 为什么不设独立分类器

经过多轮讨论和实践验证，独立分类器有三个致命缺陷：

1. **规则判断效果差**（用户多项目实测）：覆盖率低、边界脆弱、维护噩梦。140 亿人说中文，每个人说法不同。"帮我看看代码"可能是 code:review（长任务），也可能是"帮我看看这个文件有多少行"（即时任务）。规则判定不了。
2. **分类和执行割裂**：分类器判"短任务"→ 交给 handler → handler 发现其实需要路由 → 回退到管线。原本一个 API 调用解决的事变成了三段跳。
3. **短任务额外开销**：一个 80 token 的查天气任务，先花 200 token 分类它是不是"短任务"，然后另一个 handler 花 100 token 执行。两个 API 调用 + 两次网络往返。完全本末倒置。

### 7.2 新方案：一个提示词决定回答、工具调用或任务创建

**核心洞见**：不单独维护一个规则分类服务。一次入口模型调用决定请求是直接回答、调用受控工具，还是创建长任务；模型不直接执行副作用，所有工具调用和任务创建都由 Go 核心校验、记录和执行。

```
用户输入
    │
    ▼
统一入口模型 (Haiku, 一次调用)
    │
    ├── 类型 1: answer
    │   判断: "这是纯信息回答"
    │   输出: 自然语言 → 流式回用户
    │   延迟: ~300ms 首 token + 流式输出
    │   代价: 一次 Haiku 调用, ~100-200 tok
    │
    ├── 类型 2: tool_call
    │   判断: "需要调用一个受控工具"
    │   输出: {kind:"tool_call", tool, arguments} → Go 核心校验执行
    │
    └── 类型 3: task
        判断: "需要持久化、多步骤或跨设备执行"
        输出: {kind:"task", task:{...spec...}} → Go 核心接管
        延迟: ~500ms (一次 API 调用)
        代价: 一次 Haiku 调用, ~300-500 tok

无需: 独立的分类器调用。无需: L0 规则引擎。
```

### 7.3 系统提示词

入口模型加载此提示词：

```
你是 PANDA，一个分布式个人桌面助理。你有三种输出类型。

═══ 类型 1：answer ═══
对于不产生外部副作用、可以直接回答的请求，输出自然语言。

═══ 类型 2：tool_call ═══
当请求需要天气、提醒或硬件等受控工具时，输出工具名和参数；Go 核心负责校验、授权、执行和记录。

═══ 类型 3：task ═══
当任务需要修改文件、构建软件、运行 GPU 负载、或涉及多步骤跨设备执行时，输出结构化任务 JSON。

路由的判断标准：
- 需要改文件 → 路由
- 需要编译/构建/部署 → 路由
- 需要 GPU 训练/渲染 → 路由
- 需要控制物理硬件但当前设备不支持 → 路由
- 涉及多个代码仓库的协调 → 路由
- 你一个人能在 30 秒内独立完成的 → 直接回答

tool_call 或 task 时输出仅 JSON（不要其他文字）：
{
  "kind": "task",
  "task": {
    "title": "简短描述",
    "project": "项目名或 null",
    "context_type": "file|command|hardware|stream",
    "requires": {
      "abilities": ["code:modify", "build:ios", "gpu_compute"]
    },
    "spec": {
      "scope": "目标文件或组件",
      "target": "要达成什么",
      "constraints": ["不能做的事"],
      "success_definition": "怎么验证完成"
    },
    "complexity": 0.0-1.0,
    "risk": "low|medium|high|critical",
    "resource_profile": {"cpu": 1, "ram_gb": 1, "gpu_vram_gb": 0, "duration_hint": "short|long"}
  }
}

tool_call 示例（仍在本提示词代码块中）：
{"kind":"tool_call","tool":"weather.get","arguments":{"location":"济南","date":"today"}}

Go 核心必须先校验 `kind`、工具白名单、参数 schema、权限和当前节点能力，再执行工具；模型输出不能直接当作 shell 命令或硬件指令。

═══ 当前可用设备 ═══
{从 MVP 本地能力目录，或后续版本的员工表，拉取实时设备能力摘要}

═══ 用户记忆（仅对话参考，不进入项目工作） ═══
{从 Hermes MEMORY.md 注入，仅注入前 800 token}
```

### 7.4 管线延迟（不对称设计）

**短任务（MVP 仍先持久化，再返回结果；后续可加快捷旁路）**：

```
用户: "今天天气怎么样"
ASR (流式, 前 5 词):        200ms
Haiku 一次调用 (流式):      300ms (首 token)
  → 模型判断: 直接回答
  → 流式输出天气信息
TTS (与输出重叠):            0ms (流水线)
──────────────────────────────────
用户感知延迟:               ~1 秒
Token 成本:                 ~100 tok, ~$0.00003
```

**长任务**：

```
用户: "帮我把 117club 的导航栏改成响应式的"
ASR (流式):                 200ms
Haiku 一次调用:              500ms
  → 模型判断: 需要路由
  → 输出: {kind:"task", task:{...spec...}}
  → 意图精炼 = 同一个调用的 JSON 中的 spec 字段

Go 核心并行:
  ① 查 context_store:        10ms ─┐
  ② 查能力目录:              50ms ─┤ 并行
  ③ 评分 + 路由:              1ms ─┘
WebSocket dispatch:          10ms
目标节点统领:
  agent 选择 (MVP 按配置):    本地开销
  或 native exec:            取决于命令
──────────────────────────────────
到 dispatch 延迟:           ~800ms
执行延迟:                    分钟-小时 (用户预期)
合并门禁 (3 并行 Haiku):    ~500ms
```

**对比之前串行+规则方案**：

| 阶段 | 之前（串行+规则） | 现在（统一入口） |
|---|---|---|
| 短任务 LLM 调用次数 | 2（分类 + 执行） | **1** |
| 短任务延迟 | ~800ms | **~1s**（调用少了，但延迟差异不大——关键差异是 token 消耗和网络往返） |
| 长任务到 dispatch | ~1500ms | **~800ms** |
| 分类能力 | 规则边界脆弱 | **由模型泛化，仍需 schema 校验和失败回退** |
| 维护成本 | 每新任务类型加规则 | **主要维护 schema、工具和能力声明** |

---

## 八、Skill 系统：自进化的流程记忆

### 8.1 设计来源

Skill 系统融合了三个来源的设计思想：

- **Hermes Agent (Nous Research)**：Self-evolving skill——从工具调用历史中自动生成可执行 skill，fuzzy patching 增量更新，skill index 轻量索引（不把全部 skill 塞进 prompt）
- **MUSE-Autoskill (arXiv 2605.27366)**：质量闸门（≥3 次出现，≥70% 成功率）、沙箱验证、skill bank 定期修剪
- **Harness / agentskills.io**：标准化 SKILL.md 格式（YAML frontmatter + Markdown body），跨代理可移植

### 8.2 自动生成触发条件

```
触发条件（满足任一）:
  - 同一类任务成功执行 ≥3 次
  - 执行成功率 ≥70%
  - 用户明确说"记住这个做法"或"以后都这么做"
  - 用户提供的纠正/指导（技能修正）

不触发:
  - 只用了 1 个工具 = 太简单，不值得做成 skill
  - 执行失败次数 > 成功次数
  - 仅在即时任务（短对话）中的操作
```

### 8.3 生成流程

```
触发条件满足
    │
    ▼
1. 收集素材: 相同类型的任务执行历史（tool calls + 结果 + 用户反馈）
    │
    ▼
2. LLM 蒸馏: 调用轻量模型（Haiku），从历史中提取
   - 前置条件（何时用这个 skill）
   - 步骤序列（标准化流程）
   - 注意事项（已知陷阱）
   - 适用场景（能做什么，不能做什么）
    │
    ▼
3. 生成 SKILL.md: 按 agentskills.io 标准格式
   ~~~yaml
   ---
   name: deploy-117club
   description: 部署 117club 网站到生产服务器
   scope: project(117club)
   triggers: ["部署", "上线", "发布 117club"]
   requires: ["ssh-access", "nginx-reload"]
   ---
   ~~~
    │
    ▼
4. 用户审批: PUSH 到手机
   [批准] [拒绝] [修改]
    │
    ├── 批准 → 写入 → 生效
    ├── 拒绝 → 丢弃
    └── 修改 → 用户编辑后批准
```

### 8.4 Skill 的作用域隔离

| 作用域 | 定义 | 示例 |
|---|---|---|
| `scope: global` | 所有项目可用。用户审批通过后标记。 | "所有项目一律使用 ESLint flat config" |
| `scope: project({name})` | 仅指定项目可用 | "117club 项目的部署流程" |
| `scope: device({id})` | 仅指定设备可用 | "香橙派的舵机初始化序列" |

### 8.5 Skill 的渐进加载（Hermes 模式）

所有 skill 文件不在 system prompt 中全量加载。只加载**轻量 skill index**（名称 + 一句话描述 + 触发词），当调度器检测到匹配时才通过 `skill_view` 加载完整内容。这避免了"重量背包"问题——skill 越多 prompt 越大。

### 8.6 Skill 的生命周期维护

```
活跃 skill: 最近 30 天内被触发过
休眠 skill: 30-90 天无触发 → 归档
过期 skill: >90 天无触发 + 利用率 <5% → 建议删除（通知用户）
合并: 内容高度重叠的 skill → 建议合并（通知用户）
```

---

## 九、员工表与设备入职

### 9.1 "公司隐喻"设计

**核心原则：公司员工不需要知道所有人是干什么的，只需要去找部门，从部门里找人就行。**

员工表是一份**集中存储、API 按需查询**的设备名录。入职：节点把自己的能力卡写入员工表。查询：调度器需要找人时通过 API 查表匹配。裁员：主动 leave 或心跳超时标记 offline。

### 9.2 员工表数据模型

```json
{
  "employee_id": "windows-y7000p",
  "name": "拯救者 Y7000P 2024",
  "department": "算力池",
  "chip": "i7-14650HX + RTX 4060 8GB",
  "tier": 2,
  "capabilities": {
    "native": [
      {"id": "build:windows", "command": "msbuild", "args": [...]}
    ],
    "agents": [
      {"id": "claude_code", "capabilities": ["code:modify", "code:review"], "cost_tier": "medium_high"},
      {"id": "opencode", "capabilities": ["code:modify", "web:search"], "cost_tier": "low_medium"}
    ],
    "manual": [
      {"id": "edit:video"}
    ]
  },
  "capacity": {
    "cpu_cores": {"total": 20, "available": 14},
    "ram_gb": {"total": 32, "available": 18},
    "gpu_vram_gb": {"total": 8, "available": 6},
    "max_concurrent_tasks": 5,
    "current_tasks": 2
  },
  "status": "online",
  "last_seen": 1723456789,
  "joined_at": 1723000000
}
```

### 9.3 入职/裁员协议

**入职**：
```
新节点上线 → 跑 join 命令 → 向服务器 POST /api/v1/employees
  {employee_id, name, chip, capabilities, capacity, ...}
→ 服务器写入员工表 → 返回 OK
→ 开始每 30s 发送心跳 (PUT /api/v1/employees/{id}/heartbeat)
→ 调度器以后查表就能找到它

不需要: 通知任何其他节点。不需要全网广播。
```

**裁员（软性下线）**：
```
主动离职: 节点发 DELETE /api/v1/employees/{id} → 从表删除
被动失联: 心跳超时 (3 个周期 ≈ 90s) → status 标记为 "offline"
→ 调度器查询时自动跳过 offline 节点
→ 已委派给该节点的任务触发 transfer
```

### 9.4 查表：部署时固定

员工表主表位置在部署时配置：

```yaml
# /etc/panda/config.yaml
ledger:
  url: "https://panda.xenith.sh"
  # 或开源用户:
  # url: "https://orangepi3b.tailnet-name.ts.net"  (Tailscale Funnel)
  # 或本地开发:
  # url: "http://localhost:7836"
```

所有调度器（无论哪个设备当前是入口）都查同一个 URL。

### 9.5 员工表 API

```
POST   /api/v1/employees             入职（注册能力卡）
GET    /api/v1/employees             列出所有员工（含 offline）
GET    /api/v1/employees?status=online&dept=算力池  筛选查询
GET    /api/v1/employees/{id}        查看单个员工详情
PUT    /api/v1/employees/{id}/heartbeat  更新心跳（status + capacity + last_seen）
PUT    /api/v1/employees/{id}        更新员工信息
DELETE /api/v1/employees/{id}        裁员
```

---

## 十、通信管线：P2P 委派协议

### 10.1 核心原则

> **每节点独立通信。不要一个任务发下去所有都需要中心节点批准。通过设计通信管线，让每一个节点单独通信即可。**

### 10.2 消息类型（完整协议）

```
hello / join         节点 → 服务器      入职（写能力卡）
heartbeat            节点 → 服务器      更新 status/capacity/last_seen

task_delegate        节点 → 节点        直接派活（P2P，不经过中心）
task_accept          节点 → 节点        接受
task_decline         节点 → 节点        拒绝（必须带原因）

task_progress        执行节点 → 上级    进度上报
task_result          执行节点 → 上级    结果回传（沿委派链逐级）

task_retry           上级 → 执行节点    打回修正
task_transfer        上级 → 新节点      跨节点转移

context_fetch        节点 → 节点        按需拉取 full 完整上下文
context_ack          节点 → 节点        上下文已收到

heartbeat_p2p        节点 → 节点        P2P 心跳（可选，探测直连延迟）
```

### 10.3 消息信封

```json
{
  "v": 1,
  "type": "task_delegate",
  "msg_id": "uuid7",
  "from": "orangepi3b",
  "to": "windows-y7000p",
  "ts": 1723456789,
  "payload": {
    "task_id": "uuid7",
    "parent_id": "uuid7-parent",
    "project": "117club",
    "context_type": "file",
    "context_hash": "sha256:abc123...",
    "intent": "重构 Hero.vue 组件，改为响应式布局",
    "spec": {
      "scope": "frontend/src/views/Hero.vue",
      "constraints": ["不修改 App.vue", "保留现有导航项"],
      "success_definition": "viewport < 768px 时导航变为汉堡菜单"
    },
    "requires": {
      "abilities": ["code:modify", "files"],
      "min_ram_gb": 4
    },
    "chain": ["orangepi3b"],
    "timeout_ms": 300000,
    "max_retries": 2,
    "complexity": 0.6,
    "model_tier": 3
  }
}
```

### 10.4 委派链 = 权限追溯 + 结果回传

委派链 `["orangepi3b", "macbook", "windows"]` 有三个功能：
1. **结果回传路径**：Windows 完成 → 报告 Mac → Mac 审核 → 报告香橙派
2. **权限追溯**：Windows 上的高风险操作沿链回传到根（香橙派），再由根推送到用户手机
3. **审计追溯**：每条 task_event 附带当前链的快照，事后可重建完整决策路径

### 10.5 点对点通信 vs 经过中心的区别

```
错误模式（经过中心批准）:
  香橙派 → Mac: "帮我看你能不能干这个"
  Mac → 香橙派: "我干不了，但 Windows 可以"
  香橙派 → Mac: "好，我把它发给 Windows"
  香橙派 → Windows: "你来干这个"
  Windows → 香橙派: "好的"
  香橙派 → Mac: "Windows 接手了"
  一共 6 条消息，4 条经过中心

正确模式（P2P 逐边）:
  香橙派 → Mac: task_delegate
  Mac 本地决策: 我干不了 → 查表 → Windows 匹配 → 直接发
  Mac → Windows: task_delegate
  Windows → Mac: task_accept
  Windows 执行...
  Windows → Mac: task_result
  Mac → 香橙派: task_result
  一共 5 条消息，0 条经过中心的"批准"
```

### 10.6 序列化选择：JSON + MessagePack 混合

- **控制消息**（task_delegate, heartbeat, status_update）：JSON，保持可调试
- **数据消息**（L3 context, task_result 大块）：MessagePack，紧凑二进制（比 JSON 小 30-50%，解析快 2-3x）
- **心跳消息**：精简字段名（`"n":"opi3b","s":"idle","l":0.15,"ts":1723`），单条 < 30 bytes

---

## 十一、任务队列与用户面板

### 11.1 为什么是队列视图而不是任务树

任务树在项目数超过 3 个、任务数超过 15 个时会退化成不可导航的迷宫。十层深的树 = 用户不知道在第几层的哪个子节点下。队列视图只展示状态分组，用户不需要关心任务之间的依赖关系——那是系统内部的事。

### 11.2 用户看到的：按状态分组的队列 + 点进看详情

```
┌──────────────────────────────────────────┐
│  📋 PANDA                                │
│  ────────────────────────────────────── │
│                                          │
│  ▸ 待处理 (SUBMITTED)          3         │
│  ┌──────────────────────────────────┐   │
│  │ 📝 117club 导航栏响应式改造       │   │
│  │    ⏳ 排队中 · 预计 3min 后开始    │   │
│  │ 📝 周报生成                      │   │
│  │    ⏳ 排队中 · 预计 8min 后开始    │   │
│  │ 📝 论文图表优化                   │   │
│  │    ⏳ 排队中 · 等待 GPU 空闲       │   │
│  └──────────────────────────────────┘   │
│                                          │
│  ▸ 进行中 (RUNNING)             2       │
│  ┌──────────────────────────────────┐   │
│  │ 🔄 消融实验训练 · Win/4060 · 67% │   │
│  │ 🔄 SEO meta tags · Mac · running │   │
│  └──────────────────────────────────┘   │
│                                          │
│  ▸ 待审批 (REVIEW)              1       │
│  ┌──────────────────────────────────┐   │
│  │ ✅ 117club 部署脚本    [批准|驳回]│   │
│  └──────────────────────────────────┘   │
│                                          │
│  ▸ 已完成 (DONE) · 24h 内        4      │
│  ┌──────────────────────────────────┐   │
│  │ ✅ 查天气 · 即时 · 2s             │   │
│  │ ✅ 舵机归位 · 即时 · 0.5s         │   │
│  └──────────────────────────────────┘   │
│                                          │
│  [+ 新建任务]              [语音输入...]  │
└──────────────────────────────────────────┘
```

点击"消融实验训练"进入详情：

```
┌──────────────────────────────────────────┐
│  ← 返回队列          消融实验训练         │
│  ────────────────────────────────────── │
│  项目: 论文 ATC-MARL                      │
│  委派链: opi3b → Win/4060                 │
│  状态: 🔄 训练中 (67%, 预计剩余 18min)     │
│                                          │
│  子任务:                                  │
│  ✅ 数据预处理 · Mac · 完成 (3min ago)    │
│  🔄 训练基线模型 · Win/4060 · 67%         │
│  ⏳ 生成对比图表 · 等待上游                │
│  ⏳ 输出 LaTeX 表格 · 等待上游             │
│                                          │
│  [查看完整日志] [取消任务] [调整优先级]    │
└──────────────────────────────────────────┘
```

后续版本可以为即时任务提供短暂通知和快捷返回。MVP 仍统一写入本地任务记录，CLI 可查询其结果；手机通知和是否从历史面板隐藏，属于后续 UI 策略。

### 11.3 任务内部结构（系统的依赖管理和上下文隔离）

系统的任务组织支持树状归属和 DAG 依赖——但这是内部机制，不暴露为 UI 导航。`parent_id` 表示任务属于哪个父任务，`task_dependencies` 表示执行前必须成功完成的依赖。每个根节点是一个项目/专题（自带独立上下文），叶子是实际执行单元。兄弟节点上下文隔离（并行任务互不干扰），父子节点级联（只传声明的产物，不默认传输过程日志）。

链（写代码→审查→修复→合并→部署）是 DAG 的退化特例——每个节点只有一个依赖，通过 `task_dependencies` 表达严格顺序依赖。

---

## 十二、任务上下文系统（多类型 + 分级传输）

### 12.1 为什么不能强制 git 仓库

原始文档和早期讨论曾默认所有任务上下文绑定 git 仓库。经过深入剖析，这个假设不成立：
- 香橙派控制舵机不需要 git
- 语音查天气不需要 git
- 通知提醒不需要 git

**不同类型任务天然需要不同类型的上下文。git 仓库只是其中一种。**

### 12.2 四种上下文类型

| 类型 | 触发条件 | 典型任务 |
|---|---|---|
| **file** | 任务涉及代码/文件 | 改代码、构建、测试、部署 |
| **command** | 纯指令，无文件操作 | 查天气、发通知、编排、对话 |
| **hardware** | 涉及物理外设 | 舵机、蜂鸣器、传感器读取 |
| **stream** | 持续数据流 | 麦克风音频、摄像头、日志流 |

### 12.3 分级上下文传输（受 ALC 启发，后续演进）

```
TaskContext
├── pointer 指针（目标为约 64 bytes）
│   task_id + ctx_hash + ctx_type + repo_ref(可选) + source_node
│   用途: 对方本地已有同一快照 → 用 hash 定位；命中才是零额外传输
│
├── summary 摘要（目标为约 200-500 bytes）
│   intent(一句话) + 关键参数 + 约束列表 + 期望输出格式
│   用途: 常规委派；如果摘要不足，必须拉取完整快照或拒绝，不允许猜测补全
│
└── full 完整（约 1-50KB，可变）
    全量: intent + 参数 + 快照标识 + git commit/patch/文件列表 + 必需环境约束 + sandbox 配置
    用途: 首次委派 / 跨平台构建 → 对方无本地缓存时必须拉
```

### 12.4 传输协议

```
调度器发 task_delegate:
  context_level: pointer（默认）或 summary/full

执行节点收到 pointer:
  1. 查本地 context_store: SELECT * FROM context WHERE ctx_hash=?
  2. Hit → 直接用（零额外传输）→ accept + 开始执行
  3. Miss → 状态变为 waiting_context，向 source_node 发 context_fetch(hash)
           → 校验 SHA-256 → 写入 context_store → accept + 开始执行

执行节点收到 summary:
  1. 解析 intent + 参数 → 如果明确足够执行 → accept
  2. 不够（如缺少 repo 快照）→ context_fetch 或 decline(reason=context_insufficient)

context_store:
  SQLite KV 表, hash → data
  香橙派: LRU 驱逐, 最多 5 条 (~250KB)
  Mac/Win: LRU 驱逐, 最多 50 条
  超算: 不限
```

### 12.5 各类型上下文的 full 结构

```json
// file-context full snapshot
{
  "type": "file", "repo": "github.com/xenith/117club", "branch": "feat/hero", "commit": "<sha>",
  "scope": ["frontend/src/views/Hero.vue"],
  "dependencies": ["frontend/src/App.vue"],
  "env": {"NODE_ENV": "development"}
}

// command-context full
{
  "type": "command", "intent": "查询今天济南的天气",
  "parameters": {"location": "济南", "date": "today"}
}

// hardware-context full
{
  "type": "hardware", "device": "servo_1", "operation": "rotate",
  "parameters": {"angle": 90, "speed": "medium"},
  "pin_config": {"pwm_pin": 12, "frequency": 50}
}

// stream-context full
{
  "type": "stream", "source": "usb_mic_0", "format": "audio/pcm",
  "sample_rate": 16000, "buffer_ms": 200, "processing": ["vad", "asr"]
}
```

---

## 十三、模型性能调度器

### 13.1 问题定义

同一个"写代码"任务——"把变量名从 x 改成 userCount"任何模型都能做；"重构支付模块的状态机，保持 API 兼容"需要最强推理。系统必须在**不事先人工标注**的情况下自动判断任务复杂度并选对应模型。

### 13.2 复杂度评分（后续模型调度能力）

复杂度评分是入口模型输出中的一个字段，但它只表示推理/工作量复杂度，不表示安全风险或资源需求。MVP 记录该字段但不据此自动切换模型；后续版本可结合历史数据和用户覆盖进行模型档位路由。

```json
{
  "kind": "task",
  "task": {
    "complexity": 0.7,
    "risk": "medium",
    "resource_profile": {"cpu": 2, "ram_gb": 4, "gpu_vram_gb": 0, "duration_hint": "long"},
    "spec": {...}
  }
}
```

复杂度维度（模型内部参考，不暴露为规则）：

```
scope:        single_line(0) → single_file(0.2) → multi_file(0.5) → system_wide(0.8)
risk:         read_only(0) → mutable(0.3) → destructive(0.7)
ambiguity:    fully_specified(0) → somewhat_vague(0.4) → open_ended(0.7)
dependencies: standalone(0) → few_deps(0.2) → many_deps(0.5)
novelty:      pattern_match(0) → known_domain(0.2) → novel_domain(0.5)
```

### 13.3 模型档位映射（后续能力）

| 等级 | 分数范围 | 模型（示例） | 每 1M token 成本 | 适用场景 |
|---|---|---|---|---|
| **L1** | 0.0-0.2 | Haiku / GPT-4o-mini | ~$0.25 | 重命名、格式转换、简单查询 |
| **L2** | 0.2-0.45 | Sonnet / GPT-4o | ~$3 | Bug 修复、单文件重构、Review |
| **L3** | 0.45-0.75 | Opus / GPT-4.5 | ~$15 | 多文件架构变更、复杂调试 |
| **L4** | 0.75-0.95 | Opus + extended thinking | ~$30 | 系统设计、安全审计、迁移 |
| **L5** | 0.95-1.0 | 最强模型 + 人工确认 | ~$50+ | 生产部署、数据迁移、付款操作 |

### 13.4 用户覆盖

用户可以通过语音前缀强制覆盖模型选择：

```
"用 opus 帮我看一下这个支付模块的安全问题"
→ 入口模型检测到 "用 opus" → 强制 L4 → 忽略复杂度评分
```

### 13.5 Token 预算系统（基础框架）

```go
type TokenBudget struct {
    ProjectID  string
    DailyLimit float64  // 默认 $5/天/项目
    UsedToday  float64
    AlertAt    float64  // 80% 告警
    AutoDowngrade bool  // 超预算自动降级（默认 true）
}

func (tb *TokenBudget) Allocate(estimatedCost float64) (allowedTier int, warning string) {
    if tb.UsedToday + estimatedCost > tb.DailyLimit {
        if tb.AutoDowngrade {
            return downgradedTier, "预算已达上限 ($%.2f), 已自动降级模型"
        }
        return 0, "预算已超, 需确认继续"
    }
    tb.UsedToday += estimatedCost
    return requestedTier, ""
}
```

**注意：** Token 预算、自动降级和用户强制模型选择属于后续演进。MVP 只记录模型调用和错误，不根据一次不稳定的复杂度评分自动做高风险降级。

---

## 十四、任务循环防御体系

### 14.1 问题根源分析

Agent 陷入循环的三个根因：
1. **需求模糊**："把导航栏改得更好"——没有"好"的定义
2. **代码债**：项目本身模块边界不清，agent 改一处崩多处
3. **上下文不足**：agent 只看到目标文件，不知道依赖关系和全局样式

### 14.2 防御链（当前基线 + 后续增强）

```
Layer 0: 前置审查（后续可选，非 MVP）
     │
     │ 用户勾选"需要深度审查"时才触发
     │
     ▼
对抗性预分析: Agent A(提案) + Agent B(挑战) → 合并方案 + 风险识别
    Token: ~3000, 比盲开循环的潜在消耗便宜

Layer 1: 确定性执行监控（MVP 启用）
     │
     ▼
信号 A: 范围漂移 — 声明 scope 外出现文件变更 → 拦截
信号 B: 超时/退出码/资源超限 → 停止并记录
信号 C: 重复 attempt 或旧租约结果 → 丢弃，不覆盖新状态
     │
任一触发 → 暂停 → 分析
         ↓
Layer 2: 上级管理决断（后续多级委派启用）
     │
     ▼
父调度器收到: 任务历史 + diff + 原始意图 + 执行节点诊断
   判断: 执行问题还是方案问题?
     ├── 执行问题 → 换 agent 或换模型等级 或换节点
     └── 方案问题 → 进入 Layer 3
         ↓
Layer 3: 对抗性剖析（后续增强）
     │
     ▼
Agent A (分析师): 完整失败记录 → 根因分析
Agent B (审查者): 审查 A, 验证遗漏和可行性
合并 → 用户
         ↓
Layer 4: 用户通知（MVP 通过 CLI/本地日志；后续接入手机）
   📋 诊断报告 + 可选择行动方案
   [批准方案 A] [批准方案 B] [取消任务]
```

### 14.3 关于测试的策略

**不要反反复复跑测试。** 测试只在三个关键点：

1. **任务完成时**：agent 自己跑一次**任务相关文件的冒烟测试**——只测改动路径，不是全量。通过→进合并门禁。不通过→计为一次失败。
2. **合并时**：多条任务分支合并到项目主干时，跑一次全量回归。
3. **部署前**：生产环境部署前，跑关键路径的端到端测试。

代码质量不靠模型承诺保证。MVP 先依赖确定性检查：退出码、声明范围、任务相关测试和结果校验；后续再增加模型审查和用户审批。模型门禁不能替代命令级权限检查。

---

## 十五、代码质量与漂移防御（后续增强；MVP 采用确定性检查）

### 15.1 意图精炼（后续增强；统一入口模型的一个输出字段）

意图精炼不是独立的网络步骤；它是 `task` 输出中的 `spec` 字段。入口模型可以同时输出处理类型、结构化 spec、复杂度、风险和资源画像，但 Go 核心必须校验字段完整性，不能把模型推测当成事实。

```
用户: "帮我把导航栏改得更好"
      │
      ▼
入口模型一次调用（后续版本；MVP 可由 CLI 直接提交已结构化任务）:
  判断: 需要路由
  输出: {
    "kind": "task",
    "task": {
      "spec": {
        "scope": "src/components/Navbar.vue",
        "target": "viewport < 768px 时变为汉堡菜单, 不改 ≥768px 现有行为",
        "constraints": ["不修改 App.vue", "不修改全局 CSS", "保留现有导航项和顺序"],
        "success_definition": "移动端导航不再溢出, 桌面端无回归"
      },
      "complexity": 0.3,
      "risk": "low"
    }
  }
```

### 15.2 项目基线上下文自动打包

每个 file-context 任务打包时，自动化包含受影响文件的最小上下文：

```
任务: 改 Navbar.vue
打包上下文:
  ├── Navbar.vue (主文件)
  ├── 引用 Navbar 的所有组件 (组件依赖图，自动提取)
  ├── Navbar 相关的路由配置
  ├── 项目的 eslint/prettier 配置
  └── 项目的架构文档（如有）
```

### 15.3 合并门禁（三道检查，后续模型增强）

```
后续版本可并行调用模型；MVP 先执行确定性检查:
  ① 范围检查: diff 是否在声明的 scope 内？溢出 → 拦截
  ② 测试/构建检查: 任务声明的验证命令是否通过？失败 → 拦截
  ③ 破坏性检查: 是否包含高风险命令或越权路径？→ 拦截

全部通过 → 进 REVIEW
任一不通过 → 计为一次 retry
```

---

## 十六、权限模型（Tier 1/Tier 2，后续增强）

### 16.1 核心原则

**可挽回自动批，不可挽回必须问人。** 不设第三个层级。

### 16.2 Tier 1：可挽回（后续自动审批；MVP 仅确定性检查）

**定义**：操作的结果可以被回滚、恢复、或影响范围受限。

| 操作 | 为何可挽回 |
|---|---|
| 删除日志/临时文件 | 丢失无关紧要 |
| 安装沙箱内包 | 沙箱隔离，不影响系统 |
| 创建/修改项目文件 | git revert 即可恢复 |
| 重启开发服务 | 非生产环境 |
| 运行脚本/测试 | 只读或受限写入 |
| 修改测试环境变量 | 不涉及生产密钥 |
| 发送通知/Webhook | 信息泄露风险低 |

**自审机制**（对抗性双模型，~1000 tokens）：

```
Model A (审查者): 检查变更
  ① 是否删除用户数据？② 是否修改系统配置？
  ③ 是否暴露敏感信息？④ 是否触发外部副作用？
Model B (验证者): 审查 A 的结论有无遗漏？
    │
    ├── 双模型同意"安全" → 自动放行
    └── 任一方有疑虑 → 提升到 Tier 2
```

### 16.3 Tier 2：不可挽回（必须用户决断）

| 操作 | 为何不可挽回 |
|---|---|
| 删除用户文件/数据 | 无 git 追踪的数据不可恢复 |
| 修改生产环境配置 | 可能导致服务中断 |
| `git push --force` 到主分支 | 强制覆盖历史 |
| `sudo` 操作 | 提权后破坏无法限制 |
| 数据库 schema 变更 | 可能丢失数据 |
| API key/secrets 修改 | 密钥泄露后果严重 |
| 支付/计费操作 | 金钱不可逆 |

沿委派链回传到根调度器 → 手机推送。默认超时 30min → 自动拒绝。不允许静默批准。

---

## 十七、记忆系统（Hermes + OpenClaw Dreaming + Harness Auto-Skills 融合，后续演进）

### 17.1 设计来源

| 来源 | 借鉴的机制 |
|---|---|
| **Hermes Agent (Nous Research)** | 四层记忆结构（MEMORY.md 1300 token 硬上限 + FTS5 + 语义压缩 + Skills 层）、Frozen snapshot、Nudge 引擎、后台 fork agent、Skills 渐进加载 |
| **OpenClaw** | 三阶段 dreaming（Light 去重 → REM 提取模式 → Deep 六维评分写入）、Dream Diary、provenance-gated 来源追溯 |
| **Harness / MUSE-Autoskill** | 两层层（daily raw logs + 精选长记忆）、质量闸门（≥3 次 + ≥70%）、Skill bank 修剪 |

### 17.2 双层记忆，物理隔离

```
┌─────────────────────────────────────────────────────────┐
│  Hermes 个人助理记忆                                      │
│  ┌───────────────────────────────────────────────────┐  │
│  │ 热层: MEMORY.md (1300 token 硬上限)                 │  │
│  │   用户偏好 · 行为模式 · 沟通风格 · 重要事实          │  │
│  │   Frozen at session start                        │  │
│  │ 温层: memory/daily/YYYY-MM-DD.md                   │  │
│  │   30 天归档 → 90 天删除                            │  │
│  │ 冷层: Dreaming 引擎 (空闲时自动运行)                │  │
│  │   Light → REM → Deep → MEMORY.md + DREAMS.md      │  │
│  │ 注入: 仅对话/短任务                                 │  │
│  │ 严禁: 进入项目 agent 工作上下文                      │  │
│  └───────────────────────────────────────────────────┘  │
│                                                         │
│  ═══════════════ 隔离墙 ═══════════════                  │
│                                                         │
│  ┌───────────────────────────────────────────────────┐  │
│  │ 项目记忆 (Project Memory)                           │  │
│  │ projects/{name}/MEMORY.md                          │  │
│  │   架构决策 · 技术栈 · 编码规范 · 历史方案             │  │
│  │   注入: 仅同类项目任务                               │  │
│  │   严禁: 从 Hermes 拉取任何数据                       │  │
│  └───────────────────────────────────────────────────┘  │
│                                                         │
│  唯一交叉通道: Skills (用户审批后全局可用的工作规范)        │
└─────────────────────────────────────────────────────────┘
```

### 17.3 OpenClaw 三阶段 Dreaming

| 阶段 | 做什么 | 是否写入 MEMORY.md |
|---|---|---|
| **Light** | 扫描最近 daily 日志、去重去噪、暂存候选 | 否 |
| **REM** | 提取模式、反思信号、构建主题摘要、关联洞察 | 否 |
| **Deep** | 六维加权评分（Relevance 0.30 + Frequency 0.24 + Query Diversity 0.15 + Recency 0.15 + Consolidation 0.10 + Conceptual Richness 0.06）、严格阈值门控 | **是** |

### 17.4 记忆演进速度

| 类型 | 处理策略 | 示例 |
|---|---|---|
| **非常重要**：用户明确要求记住 | **直接写入 MEMORY.md**（不经过 dreaming 阈值） | "117club 项目禁止 TypeScript" |
| **不重要** | 写入 daily 日志 → 30 天归档 → 90 天删除 | "今天天气不错" |
| **模糊重要性** | 留在日志 → dreaming 自动评级 → 超阈值写入 / 随日志过期 | 日常讨论中的技术偏好 |

### 17.5 防污染措施（后续实现时的强制约束）

1. `context_pack()` 只从当前项目打包，Hermes 不可被打包
2. Agent system prompt 硬约束：项目偏好一律忽略
3. 默认禁止项目 Agent 主动查询 Hermes；如未来开放受控查询，必须经过明确工具白名单、字段过滤和审计，标签不能替代访问控制。
4. REVIEW 前审查模型扫 diff，检测"与项目无关的风格化改动"

---

## 十八、Token 经济性分析与优化

### 18.1 行业现状与问题根因

来自调研的硬数据：
- 多 agent 集群的 token 消耗是单 agent 的 **3-10 倍**
- **40-80% 的 token 花在内部摩擦**
- Sub-agent 基础开销：**每次孵化 8-16k tokens**
- Agent Teams 已知 bug：agent 被复制 2 次（2.2× 开销）

**行业问题的根源：agent 之间用自然语言聊天。** O(n²) 通信膨胀。

### 18.2 PANDA 的五道成本防线

**防线一：Go 核心间通信不经过 LLM。** WebSocket 结构化消息，不是"Agent A 对 LLM 说：请告诉 Agent B..."。

**防线二：任务上下文 pointer 命中。** 同一份已校验快照重复委派时可零额外传输；不同快照必须重新获取。

**防线三：统一入口模型。** 一次调用决定 answer/tool_call/task；Go 核心负责工具校验、任务持久化和执行，不让模型直接产生未审计的副作用。

**防线四：Agent adapter 是 subprocess。** 不需要为 agent 间协调孵化 LLM session。

**防线五（后续）：短任务快捷通道。** 纯 `answer` 可直接流式返回；`tool_call` 仍必须经过 Go 核心校验和事件记录。MVP 统一持久化任务，避免丢失可审计记录。

### 18.3 预算估测（保守基准）

重度用户一天：10 个任务（2 高复杂度 + 3 委派跨机 + 5 即时）

| 环节 | Token 消耗 | 备注 |
|---|---|---|
| 入口模型调用（10 次） | ~2,500 | 含 5 短任务(直接回答) + 5 长任务(路由 JSON) |
| Agent 执行（8 个委派） | ~120,000 | 平均 15k/任务，无论用不用 PANDA 都要花 |
| Agent 选择 | 部署相关 | MVP 按用户配置；后续可由轻量模型选择 |
| 对抗性剖析（2 次失败） | ~10,000 | 根因分析 |
| 权限自审（8 次） | ~8,000 | Tier 1 |
| 合并门禁（5 次） | ~4,000 | 3 个 Haiku 并行 × 5 任务 |
| 夜间 dreaming | ~5,000 | 空闲执行 |
| **PANDA 增量总计** | **需实测** | 以上条目是后续容量估算，不能视为已验证的成本承诺 |
| **日均总计（含 agent）** | **~150,000** | |

月均金额和 PANDA 增量都取决于模型、上下文和失败重试次数，当前不作固定价格承诺；上线前应以实际调用日志重新测算。

---

## 十九、安全架构（OpenClaw 教训规避）

### 19.1 OpenClaw 已知漏洞及 PANDA 防御

| OpenClaw 漏洞 | PANDA 防御 |
|---|---|
| **API key 泄露进系统 prompt** (#11202) | Go 核心管理密钥。环境变量注入 agent 进程，进程结束即消亡。绝不序列化到 LLM 上下文。 |
| **凭证渗出链** (#56268) | 沙箱隔离（agent 只读写 task 目录）。网络白名单（只能访问 API endpoint + git remote）。上下文隔离（Hermes 不进项目上下文）。 |
| **子 agent 完成消息泄露** (#22867) | 任务结果通道是内部 WebSocket，不经过外部消息平台。不含 token 统计、session key、推理链。 |
| **Prompt 注入来自 Token 优化** (#72570) | 任务 spec 由 Go 核心生成（受信），非用户原始输入透传。System prompt 由 Go 代码拼接。 |
| **Session 状态消息泄露 token 片段** (#32970) | Go 核心间通信是结构化二进制。状态消息只含 node_id + task_id + state。 |

### 19.2 安全架构的根本原则

> **把"LLM 可见的信息"和"系统内部信息"分成两条完全隔离的通道。Go 核心之间说的话，LLM 永远看不到。**

---

## 二十、服务器策略（公网 + 开源替代方案）

### 20.1 为什么后续可能需要服务器

当设备数量增加、入口需要切换、用户面板需要跨网络访问时，员工表、任务索引和面板数据才适合放到一个始终在线的索引端点。**本地 MVP 不需要服务器**，使用入口节点上的 SQLite 能力目录即可。

### 20.2 服务器只做"轻量索引"，不做"实时通信"

后续版本的任务通信仍可点对点走 WebSocket over Tailscale，不经过服务器。服务器只存元数据，不存任务执行所需的完整上下文：

| 存什么 | 规模 |
|---|---|
| 员工表 | ~500B/员工 |
| 任务索引 | ~300B/任务 |
| 用户配置 | ~1KB |

### 20.3 服务器 API

```
POST /api/v1/employees             入职
GET  /api/v1/employees?status=...  查询
PUT  /api/v1/employees/{id}/heartbeat  心跳
DELETE /api/v1/employees/{id}      裁员
POST /api/v1/tasks                 创建任务索引
PUT  /api/v1/tasks/{id}            更新状态
GET  /api/v1/dashboard             用户面板数据
```

### 20.4 开源用户的三个选项

**选项 A：Tailscale Funnel（零成本，推荐）** — `tailscale funnel 8080` → 公网 HTTPS。

**选项 B：Fly.io / Railway 免费层（零成本，有公网 IP）** — `fly deploy`。

**选项 C：托管服务（你维护）** — 免费个人层，付费商业层。

### 20.5 服务器抗单点故障

服务器挂 → 面板暂时看不到状态。**现有任务继续执行**（P2P 通信不经过服务器）。服务器恢复 → 心跳恢复 → 面板恢复。

---

## 二十一、竞品分析与创新边界

### 21.1 调研覆盖

| 项目 | 定位 | 与 PANDA 的重叠 | 没有做的 |
|---|---|---|---|
| **OpenHive** | 自托管 swarm 协调面 | Tailscale mesh、能力声明 | **硬件感知调度**、异构设备、嵌入式 |
| **NeuroGrid** | 模块化 swarm 协议 | DAG 任务分解 | 过重（AGPL+区块链/PoWD）、非个人向 |
| **DAgR** | NATS serverless agent | 子 agent 孵化 | NATS 绑定、无能力匹配 |
| **orqlaude** | Claude Code 编排 | DAG、工作窃取 | Claude Code 专属 |
| **delegate** | 事件驱动委派 | DAG、SQLite、崩溃恢复 | 单机、无跨设备 |
| **Hermes Agent** | 自进化单体 agent | 四层记忆、Skills、Nudge | **不做分布式调度** |
| **OpenClaw** | AI agent + dreaming | Dreaming、Diary | **严重安全问题**、单 agent |

### 21.2 拟作为差异化方向（需验证）

1. **ATC-MARL 理论 → 异构设备调度工程落地**：计划从仿真启发转为工程实现，创新性需要通过对比实验和现有方案调研验证
2. **双记忆 + 严格上下文隔离**：Hermes 记人，项目记代码，物理隔离
3. **统领三层能力模型（native/agent/manual）**：确定性命令零 token、AI agent、人工操作统一路由
4. **多上下文类型（file/command/hardware/stream）**：不强制 git
5. **统一入口模型（分类即执行）**：一次模型调用完成分类+执行或分类+精炼，不设独立分类器
6. **两级权限模型**：对抗性双模型自审处理低风险操作
7. **可插拔 Agent 适配层**：安装时扫描+询问安装，统领运行时动态选择，新增 Agent 只需 30 行适配器

### 21.3 诚实标注的非创新点

- Go 核心 + subprocess 扩展：标准工程实践
- WebSocket + MessagePack over Tailscale：成熟协议
- 员工表 + 能力卡：类似 K8s node labels
- 熔断器、重试、幂等：分布式系统标准武器
- Dreaming 机制：OpenClaw 首创
- Skills 自进化：Hermes Agent 首创

---

## 二十二、AI 模型替代风险分析

**理由一：基础模型是单会话、无状态的。它们不知道你的设备存在。**
Claude Code 在同一台机器的 session 里跑，不知道 Windows 上有 4060，不知道香橙派连着舵机。

**理由二：调度逻辑不是模型能力的延伸。**
GPT-6 还是需要一个系统告诉它有哪些设备可用。

**理由三：Sub-agent 现状离"分布式调度"差两代。**
5 个 sub-agent 消耗 55k-116k tokens/个，Agent Teams 有复制 2× 的 bug。

**结论**：基础模型是 PANDA 的可替换依赖；模型变强会提升部分能力，但不会自动提供本地设备目录、任务租约、跨节点状态和硬件接入。替代风险需要随着基础模型能力变化持续评估，不能预先宣称极低。

---

## 二十三、可行性综合评估

| 维度 | 评分 | 说明 |
|---|---|---|
| **技术可行性** | ⭐⭐⭐⭐⭐ | Go + WebSocket + SQLite + agent CLI subprocess，组件成熟；跨设备恢复和取消仍需实测。 |
| **竞争壁垒** | ⭐⭐⭐⭐ | 组合方向有差异化潜力，但需要原型和竞品验证，不能预先视为壁垒。 |
| **AI 模型替代风险** | ⭐⭐⭐⭐⭐ | 设备目录、租约和任务状态仍需系统提供；具体替代程度需随模型能力演进复评。 |
| **Token 经济性** | ⭐⭐⭐⭐⭐ | 结构上减少协调开销；节省比例和增量成本需要运行日志验证。 |
| **安全性** | ⭐⭐⭐⭐ | 设计上覆盖已知风险类别；实际部署仍需权限、密钥和网络测试。 |
| **开发复杂度** | ⭐⭐ | 非常高。8-14 周全职 MVP。Phase 0 就有可见价值。 |
| **开源生态兼容** | ⭐⭐⭐ | 开放栈，但 JS/TS 生态整合是空白。 |
| **商业化可扩展性** | ⭐⭐⭐⭐ | 托管服务 + 开源。 |

**结论：可行。值得做。需要正视开发周期和安全测试成本。**

---

## 二十四、面向未来的设计：万物智联

### 24.1 架构不绑定硬件

新设备加入 = 编译 Go 二进制（一行）+ 写能力卡（JSON）+ `panda join`。

### 24.2 启示性应用场景

| 场景 | PANDA 机制 |
|---|---|
| **深空微星通讯** | TMB 缓存（旧数据不丢）+ ALC 启发的极端压缩 + DCPS 动态拓扑；仅作远期设想 |
| **自动驾驶** | DCPS 逐边通信（车车直接握手）+ 熔断器（传感器故障不传播） |
| **救灾无人集群** | 心跳超时 offline + transfer + 加权排队（救生 10 > 物资 7 > 侦察 5） |
| **智能家居+助理** | 双层记忆（家≠工作）+ Tier 1/Tier 2（关灯自动批, 开锁必须问） |

### 24.3 Token 降价不影响价值

五道成本防线是**结构性的**（省延迟），不是"因为 token 贵"——在实时场景中延迟比成本更关键。

### 24.4 用户增长路径（"超级爆款"分析）

**目标定位**：想做类似 OpenClaw 的超级爆款。

**核心用户画像**：
- 拥有 ≥2 台异构设备的人（一台笔记本 + 一台台式机 + 一个开发板 = 3 台设备，已覆盖全球大部分开发者）
- 设备之间有天然的能力差异（Mac 能打 iOS 包但不能 CUDA，Windows 有 4060 但不能做 Xcode）
- 每天在不同设备之间手动切换 ≥3 次

**增长路径分析**：

| 阶段 | 用户规模 | 关键动作 |
|---|---|---|
| **Phase 0: 个人使用** | 1 人 | 你在自己的三台设备上用，打磨核心体验 |
| **Phase 1: 早期开源** | 10-100 人 | GitHub 开源 + Hacker News / Reddit 发布。吸引的典型用户：树莓派爱好者、有多台电脑的开发者 |
| **Phase 2: 社区增长** | 100-1000 人 | Agent 适配器社区贡献（每新增一个 Agent CLI = 30 行 Python、一个 PR）、Skills 社区分享 |
| **Phase 3: 破圈** | 1000-10000 人 | "任何设备，任何算力，一个语音指令"成为 slogan。关键引爆点：有人把它用在非开发场景（智能家居、救灾模拟、教育机器人） |

**与 OpenClaw 的差异化增长路径**：OpenClaw 的增长来自 AI coding agent 的普及。PANDA 的增长来自**人均设备数的增长**——万物互联意味着未来每个人的设备远远不止一台。这是一个增长的宏观趋势，不受 AI 市场波动影响。

**面临的风险**：如果基础模型内建了多设备管理能力（如 Claude Code 原生支持跨设备调度），PANDA 的调度层将被替代。但记忆系统、Skill 系统、三层能力模型仍是独有——**PANDA 的核心壁垒不是调度本身，而是"调度+记忆+Skill"的三位一体。**

---

## 二十五、开源与商业化策略

### 25.1 开源模式

MIT 许可。Go 核心 + PWA 前端 + 扩展框架完全开源。部署门槛：Tailscale Funnel 或 Fly.io 免费层。

### 25.2 商业化模式

托管服务（免费个人 ≤3 设备，付费 Pro 无限设备）。企业版（私有部署、多用户、SSO）。商业模式对标 Tailscale。

---

## 二十六、开发路线图

### Phase 0 · 本地任务闭环（当前 MVP）→ 预计 2-3 周

```
交付:
  · 固定一个入口节点（Mac 或香橙派）
  · Go 核心骨架: recv + router + state + local ledger + context
  · 本地能力目录与节点启动注册
  · 消息协议 v1 (hello/heartbeat/task_delegate/task_accept/task_result/task_cancel)
  · 一个 native adapter + 一个 Agent adapter
  · 文本/CLI 入口；暂不包含语音、PWA、公网服务器和动态入口切换

验收:
  · 两个本地节点上线，能力目录可见
  · 模拟 task_delegate → task_accept → task_result 跑通
  · 重复消息不会重复执行，旧 attempt 结果不会覆盖新 attempt
  · 节点重启后可从 SQLite 恢复任务状态
  · 取消、超时和失败状态有确定结果
```

### Phase 1 · 单级委派与入口模型 → 预计 3-4 周

```
交付:
  · 统一入口模型 (一次调用决定 answer/tool_call/task)
  · 委派协议 v1 + 任务队列 (SUBMITTED/QUEUED/RUNNING/REVIEW/DONE)
  · 统领三层能力路由 (native/agent/manual)
  · 第二个 Agent adapter（在第一个 adapter 稳定后）
  · 基础 Agent 选择（先用能力与用户配置，模型选择后续加入）
  · 本地队列查看与取消
  · 复杂度、风险和资源画像字段；模型档位路由留作后续

验收:
  · 文字/CLI 发任务 → 统一入口 → 路由 → 目标设备执行 → 结果回传
  · answer/tool_call/task 三种输出都能被核心正确处理
  · 统领 native 命令执行 (如 xcodebuild) 不需要模型调用
```

### Phase 2 · 多级委派 + 上下文 → 预计 4-6 周（后续演进）

```
交付:
  · 委派链 + P2P 逐边委派
  · 多类型上下文 (file/command/hardware/stream) + pointer/summary/full 分级（后续演进）
  · 防御层 (执行监控→上级决断→对抗性剖析)
  · 权限 Tier 1/Tier 2 + 对抗性自审
  · 熔断器 + 崩溃恢复 + 幂等检测
  · 香橙派 GPIO 扩展

验收:
  · 香橙派→Mac→Windows 链跑通 (Mac 直接调 Windows)
  · 同一快照第二次委派命中 pointer（零额外传输）
  · 舵机语音指令执行
```

### Phase 3 · 记忆 + 语音 + 安全 → 预计 3-4 周（后续演进）

```
交付:
  · 双层记忆 + 上下文隔离（后续演进）
  · Dreaming 引擎 (Light/REM/Deep) + Dream Diary
  · Skill 系统 (生成/审批/作用域/维护)
  · 语音入口 (Porcupine + 云端 ASR)
  · 安全加固 (沙箱 + 网络白名单 + 密钥隔离)
  · PWA 完善 (Web Push + 历史 + 审批)
  · 响铃通知 (GPIO)

验收:
  · 语音→唤醒→ASR→分类→调度→执行→通知 全链
  · Dreaming 自动运行，DREAMS.md 可读
  · Skill 生成→审批→调用
  · 高风险操作必须手机确认
```

### Phase 4 · 硬件 + 扩展 → 并行，持续（远期愿景）

```
交付:
  · M.2 存储 + swap 分层
  · 3D 外壳
  · 超算/任意设备入职
  · 社区文档 + 贡献指南
  · 托管服务 (可选)
```

---

## 二十七、附录：完整术语表

| 术语 | 定义 |
|---|---|
| **PANDA** | Personal Adaptive Node-based Distributed Assistant |
| **节点 (Node)** | 任何运行 PANDA Go 核心的设备 |
| **入口调度器** | 接收用户请求并创建/委派任务；MVP 固定一个入口，后续通过租约和状态同步支持切换 |
| **子调度器** | 委派链中间节点，被上级委派后进一步编排下级 |
| **执行节点** | 用自己的硬件/agent 执行任务的节点 |
| **统领 (Commander)** | 每节点 Go 核心中的模块，管理三层能力（native/agent/manual） |
| **委派链 (Delegation Chain)** | 任务从根到叶的节点序列——权限追溯+结果回传路径 |
| **能力目录 (Capability Ledger)** | 节点能力和状态名录；MVP 本地 SQLite，后续可由服务器集中存储并按需查询 |
| **能力卡 (Capability Card)** | 节点注册的能力声明（native 命令 + agent 列表 + manual 能力 + 负载） |
| **三层能力 (Three-Tier Capability)** | native(确定性命令, 0 token) / agent(AI 推理) / manual(人工操作) |
| **入职 (Join) / 裁员 (Leave)** | 注册/注销节点 |
| **任务队列** | 用户面板的状态分组（SUBMITTED/QUEUED/RUNNING/REVIEW/DONE），内部还包括 FAILED/CANCELLED/EXPIRED |
| **任务内部结构** | `parent_id` 表示归属，`task_dependencies` 表示 DAG 执行依赖，不暴露为 UI 导航 |
| **即时任务 / 规划任务** | 后续产品概念；MVP 统一持久化任务，是否快捷返回由入口层决定 |
| **统一入口模型** | 一次调用决定 `answer`/`tool_call`/`task`；Go 核心负责校验、持久化和副作用执行 |
| **上下文类型** | file / command / hardware / stream |
| **pointer/summary/full 上下文** | 指针级 / 摘要级 / 已校验完整快照；命中 pointer 才能零额外传输 |
| **ATC-MARL / TMB / ALC / DCPS** | 论文理论基础（见第二章） |
| **Hermes 记忆** | 个人助理记忆——MEMORY.md, 1300 token 硬上限, 仅对话/短任务 |
| **项目记忆** | 项目专属记忆——projects/{name}/MEMORY.md, 严禁混入 Hermes |
| **Dreaming** | 三阶段记忆整合（Light/REM/Deep） |
| **Dream Diary** | 人类可读的记忆整合日志（DREAMS.md） |
| **Skill** | 自动生成的可执行流程，agentskills.io 标准，用户审批后生效 |
| **Tier 1 / Tier 2 权限** | 后续能力：可挽回操作可按策略自动批；不可挽回操作必须用户决断 |
| **防御链** | Layer 0(后续前置审查) → Layer 1(确定性执行监控) → Layer 2(后续上级决断) → Layer 3(后续对抗性剖析) → Layer 4(用户通知) |
| **节点资源等级** | Micro / Standard / Full；不与上下文或模型档位共用 L0/L1/L2 名称 |
| **Tailscale Funnel** | 零成本公网 HTTPS——开源用户替代服务器方案 |

---

*文档版本 v4.6 · 2026-08-12 · 已整合 ATC-MARL 论文完整技术细节与定量数据 · 本地架构基线已收敛 · 远期愿景保留并分阶段实现*

*上一版本: v4.5 (2026-08-12) · 本版大幅深化 §2.2-2.5 的 TMB/ALC/DCPS 工程映射，新增 §2.6 论文额外启发（训练方法论、可扩展性数据、模块协同效应、局限性缓解），补充论文中的命题、公式和消融实验数据作为 PANDA 设计的独立验证。*
