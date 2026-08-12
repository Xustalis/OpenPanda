# PANDA · 产品开发计划书

> **PANDA**: Personal Adaptive Node-based Distributed Assistant
>
> **版本**: v1.1
> **日期**: 2026-08-12
> **作者**: Xenith
> **状态**: Phase 0 启动前，已整合 ATC-MARL 论文定量参考与验收基准
>
> 本文档是 PANDA 项目的完整产品开发计划，涵盖从立项到商业化的全生命周期。
> 技术架构细节参考 [PANDA-总览设计文档](./PANDA-总览设计文档.md)，本文档聚焦于**如何做**和**何时做**。

---

## 目录

- [一、项目概述](#一项目概述)
- [二、立项分析](#二立项分析)
- [三、产品规划](#三产品规划)
- [四、开发阶段详细计划](#四开发阶段详细计划)
- [五、测试策略与质量保障](#五测试策略与质量保障)
- [六、部署与运维计划](#六部署与运维计划)
- [七、发布与增长策略](#七发布与增长策略)
- [八、风险管理](#八风险管理)
- [九、资源规划](#九资源规划)
- [十、预算规划](#十预算规划)
- [十一、里程碑与时间线](#十一里程碑与时间线)
- [十二、成功度量指标](#十二成功度量指标)
- [十三、附录](#十三附录)

---

## 一、项目概述

### 1.1 产品定义

PANDA 是一个以异构设备为节点的个人任务编排系统。它让用户通过文本、CLI 或语音对任意入口节点下达任务，系统自动根据设备能力将任务委派到最合适的执行节点，并完成结果回传。

**核心价值主张**：任何设备，任何算力，一个指令。

### 1.2 产品愿景

```
当前（MVP）:              未来（愿景）:
一台入口节点               任意设备作为入口
文本/CLI 提交任务          语音/手机/桌宠多入口
本地 SQLite 能力目录        集中式员工表 + P2P 委派
一个 Agent adapter          可插拔多 Agent 生态
单级委派                   多级 P2P 逐边委派
                           记忆系统 + Skill 自进化
                           桌宠硬件载体 + 物理世界延展
```

### 1.3 目标用户

| 阶段 | 用户画像 | 设备特征 | 痛点 |
|---|---|---|---|
| Phase 0 | 作者本人 | 香橙派 + Mac + Windows | 跨设备手动切换，人肉调度器 |
| Phase 1 | 早期开源用户 | ≥2 台异构设备 | 同上，且对开源工具有偏好 |
| Phase 2 | 社区开发者 | 多台电脑 + 开发板 | 需要社区贡献的 adapter 和 skill |
| Phase 3 | 泛开发者/极客 | 任意智能设备 | "万物智联"的入口需求 |

### 1.4 产品定位

```
┌─────────────────────────────────────────────────────┐
│                   AI 编排工具                        │
│  ┌──────────┐  ┌──────────┐  ┌──────────────────┐  │
│  │ Sub-agent │  │ Agent    │  │ PANDA            │  │
│  │ (单机)    │  │ Teams    │  │ ← 我们在这里      │  │
│  │           │  │ (多agent,│  │ 多设备分布式       │  │
│  │ Claude    │  │  单机)   │  │ 异构算力编排       │  │
│  │ Code      │  │          │  │                   │  │
│  └──────────┘  └──────────┘  └──────────────────┘  │
│                                                    │
│  单机多进程 ────────→ 多设备分布式 ──────→ 万物智联  │
└─────────────────────────────────────────────────────┘
```

---

## 二、立项分析

### 2.1 问题陈述

当前 AI Agent 工具的**结构性缺陷**：单机多子代理已经成熟，但**多设备分布式算力没有被充分利用**。

具体表现：
1. **iOS 构建必须手动传到 Mac**：用户在 Windows 上开发，构建 iOS 包时需要手动把代码传到 Mac，跑完再传回来
2. **GPU 训练必须手动提交到 Windows**：Mac 上做开发，需要 GPU 训练时必须 SSH 到 Windows，手动启动训练脚本
3. **嵌入式设备只能单独操作**：香橙派连着舵机，但控制舵机需要单独 SSH 进去执行命令
4. **用户充当人肉调度器**：每台电脑各自为战，用户需要在不同设备之间手动切换、手动传输、手动协调

### 2.2 市场机会

| 趋势 | 影响 | PANDA 的切入点 |
|---|---|---|
| 人均设备数持续增长 | 开发者拥有 Mac + PC + 嵌入式 + 手机 | 设备越多，调度需求越强 |
| AI Agent 生态爆发 | Claude Code/Codex/OpenCode 等 CLI agent 成熟 | adapter 层可插拔，跟随生态增长 |
| 边缘计算兴起 | 树莓派/香橙派等廉价算力普及 | Micro 设备级别原生支持 |
| Tailscale/WireGuard 普及 | P2P 组网零门槛 | 通信层依赖成熟组网方案 |
| 开源 AI 工具链成熟 | OpenCode (MIT, 147K⭐) 等开源 agent | 开源用户零成本接入 |

### 2.3 竞品格局

详见 [设计文档 §21](./PANDA-总览设计文档.md#二十一竞品分析与创新边界)。关键结论：

- **OpenHive**、**NeuroGrid** 做了 swarm 协调，但缺少硬件感知调度和异构设备支持
- **Hermes Agent** 做了记忆和 Skill 自进化，但不做分布式调度
- **OpenClaw** 做了 Dreaming，但有严重安全问题且单 agent
- **delegate**、**orqlaude** 做了 DAG 任务编排，但限于单机或单一 agent

**PANDA 的差异化**：调度 + 记忆 + Skill 三位一体，覆盖异构设备、三层能力、多类型上下文，且架构不绑定任何单一 AI 模型或 Agent CLI。

### 2.4 可行性判断

| 维度 | 评估 | 依据 |
|---|---|---|
| 技术可行 | ✅ 可行 | Go + WebSocket + SQLite + agent CLI subprocess，全部成熟组件 |
| 理论可行 | ✅ 已验证 | ATC-MARL 论文（2026.05）通过 MPE 基准 + 15 种子统计验证了三维通信效率优化的有效性（通信量 ↓60%，延迟 ↓40.9%，得分仅降 3.1%） |
| 资源可行 | ✅ 可行 | Phase 0 单人全职 2-3 周，后续按阶段递进 |
| 市场可行 | ⚠️ 待验证 | 需要 Phase 1 开源后获取真实用户反馈 |
| 商业可行 | ⚠️ 待验证 | 托管服务模式需 Phase 2 后评估 |

**理论可行性说明**：ATC-MARL 论文的三个模块（TMB/ALC/DCPS）与 PANDA 的三个核心设计（异步消费/分级传输/去中心化委派）在结构上同构。论文通过 2³=8 配置的完整消融实验独立验证了每个模块的贡献（TMB 贡献 ~46% 延迟降低、ALC 贡献 ~60% 通信量降低、DCPS 贡献 ~40% 拓扑稀疏化）以及 ~15-20% 的正协同效应。这为 PANDA 的架构选择提供了独立于本项目的量化验证——三层设计不是猜测，而是在相关基准上经过统计显著性检验的有效组合。 |

### 2.5 立项决策

**决定：正式立项，按 Phase 0 → Phase 1 → Phase 2 → Phase 3 → Phase 4 分阶段推进。**

每个 Phase 结束后设**决策门**：评估上一阶段成果、市场反馈和技术可行性，决定是否进入下一阶段。任一门未通过则调整计划或终止。

---

## 三、产品规划

### 3.1 产品架构总览

```
┌──────────────────────────────────────────────────────────────┐
│                        用户触点层                              │
│  Phase 0: CLI/文本          Phase 3: 语音 + PWA + 桌宠表情     │
├──────────────────────────────────────────────────────────────┤
│                      统一入口模型                               │
│  Phase 1: 一次调用 → answer / tool_call / task               │
├──────────────────────────────────────────────────────────────┤
│                      Go 核心守护进程                            │
│  Phase 0: recv + router + state + ledger + context           │
│  Phase 1: + entry/model + commander/capability                │
│  Phase 2: + scheduler/routing + defense + security            │
│  Phase 3: + memory + skills + dreaming                        │
├──────────────────────────────────────────────────────────────┤
│                       统领层（每节点）                           │
│  Phase 1: native + agent(一个 adapter) + manual              │
│  Phase 1+: 多 agent adapter 可插拔                            │
├──────────────────────────────────────────────────────────────┤
│                        通信层                                  │
│  Phase 0: WebSocket + JSON over Tailscale/局域网              │
│  Phase 2: + MessagePack + P2P 逐边委派                        │
├──────────────────────────────────────────────────────────────┤
│                        硬件抽象层                               │
│  Phase 2: GPIO 扩展           Phase 4: 桌宠硬件配套             │
└──────────────────────────────────────────────────────────────┘
```

### 3.2 产品功能矩阵

| 功能模块 | Phase 0 | Phase 1 | Phase 2 | Phase 3 | Phase 4 |
|---|---|---|---|---|---|
| **Go 核心骨架** | ● | ○ | ○ | ○ | ○ |
| **本地能力目录 (SQLite)** | ● | ○ | ○ | ○ | ○ |
| **任务状态机 (8 态)** | ● | ○ | ○ | ○ | ○ |
| **1 个 native + 1 个 agent adapter** | ● | ○ | ○ | ○ | ○ |
| **节点间 WebSocket 通信** | ● | ○ | ○ | ○ | ○ |
| **统一入口模型** | ○ | ● | ○ | ○ | ○ |
| **三层能力路由** | ○ | ● | ○ | ○ | ○ |
| **多 Agent adapter 可插拔** | ○ | ● | ○ | ○ | ○ |
| **任务队列 CLI 面板** | ○ | ● | ○ | ○ | ○ |
| **意图精炼** | ○ | ● | ○ | ○ | ○ |
| **P2P 逐边委派** | ○ | ○ | ● | ○ | ○ |
| **多类型上下文 (4 种)** | ○ | ○ | ● | ○ | ○ |
| **上下文分级传输 (pointer/summary/full)** | ○ | ○ | ● | ○ | ○ |
| **防御链 (Layer 1-4)** | ○ | ○ | ● | ○ | ○ |
| **权限 Tier 1/Tier 2** | ○ | ○ | ● | ○ | ○ |
| **合并门禁** | ○ | ○ | ● | ○ | ○ |
| **熔断器 + 幂等 + 崩溃恢复** | ○ | ○ | ● | ○ | ○ |
| **GPIO/HAL 扩展** | ○ | ○ | ● | ○ | ○ |
| **双层记忆 (Hermes + 项目)** | ○ | ○ | ○ | ● | ○ |
| **Dreaming 引擎** | ○ | ○ | ○ | ● | ○ |
| **Skill 自进化系统** | ○ | ○ | ○ | ● | ○ |
| **语音入口 (Porcupine + ASR)** | ○ | ○ | ○ | ● | ○ |
| **PWA 控制台** | ○ | ○ | ○ | ● | ○ |
| **安全加固 (沙箱/密钥隔离)** | ○ | ○ | ○ | ● | ○ |
| **桌宠硬件** | ○ | ○ | ○ | ○ | ● |
| **3D 外壳** | ○ | ○ | ○ | ○ | ● |
| **托管服务** | ○ | ○ | ○ | ○ | ● |
| **社区文档** | ○ | ○ | ○ | ○ | ● |

> ● = 本期交付　　○ = 本期增强/完善

### 3.3 版本策略

- **Phase 0**：内部开发版，无版本号。代码在本地，不公开发布
- **Phase 1**：`v0.1.0` 内部 alpha。功能可用但接口不稳定，仅供早期试用者
- **Phase 2**：`v0.5.0` 公开 alpha。接口基本稳定，欢迎社区贡献 adapter
- **Phase 3**：`v0.9.0` 公开 beta。功能完整，安全加固，准备正式发布
- **Phase 4**：`v1.0.0` 正式版。API 稳定，文档齐全，托管服务上线

---

## 四、开发阶段详细计划

### Phase 0 · 本地任务闭环（预计 2-3 周）

#### 4.0.1 目标

在一台设备（Mac）和另一台设备（香橙派）之间，完成"任务提交 → 委派 → 执行 → 结果回传 → 取消/失败"的基本闭环。不涉及公网服务器、语音、PWA、动态入口切换或模型调用。

#### 4.0.2 前置条件

- [x] 架构设计文档已完成（PANDA-总览设计文档 v4.6）
- [x] 产品开发计划书已完成（本文档 v1.1）
- [ ] Go 开发环境就绪（Go 1.22+，`go version`）
- [ ] Python 3.10+ 就绪（`python3 --version`）
- [ ] Git 仓库初始化（`git init && git add -A && git commit -m "Initial commit"`）
- [ ] 香橙派环境就绪（Armbian + Go 1.22+ + Python 3.10+）
- [ ] Tailscale 组网就绪（Mac ↔ 香橙派 互通，`tailscale ping orangepi3b` 通过）
- [ ] 至少一个 Agent CLI 在 Mac 上就绪并验证可 headless 运行（Claude Code `claude -p "hello"` 或 OpenCode `opencode run "hello"`）
- [ ] Anthropic API key 可用（如选择 Claude Code 作为首个 adapter）

#### 4.0.3 任务拆解

**Sprint 0.1 · 项目骨架（2-3 天）**

| ID | 任务 | 产出 | 估时 |
|---|---|---|---|
| P0-01 | 初始化 Go module + 目录结构 | `go.mod`, `cmd/panda/main.go`, 骨架目录 | 2h |
| P0-02 | 实现配置加载 | `internal/config/`, 读取 `/etc/panda/config.yaml` | 3h |
| P0-03 | 实现结构化日志 | `internal/log/`, 基于 `slog`, JSON 格式, 含 level 过滤 | 2h |
| P0-04 | 实现 SQLite 初始化和 migrate | `internal/storage/sqlite.go`, `migrate.go`, 建表 | 4h |
| P0-05 | 实现 UUIDv7 生成器 | `internal/util/uuid.go` | 1h |
| P0-06 | 编写 Makefile：build/darwin/arm64, build/linux/arm64 | `Makefile`, 交叉编译两行命令 | 1h |
| P0-07 | 在 Mac 和香橙派上分别编译并启动空 core，验证编译和部署链路 | 两个平台都编译通过，启动后 `panda --version` 输出正常 | 2h |

**Sprint 0.2 · 节点生命周期与控制面（2-3 天）**

| ID | 任务 | 产出 | 估时 |
|---|---|---|---|
| P0-08 | 实现 `capabilities.yaml` 解析 | `internal/ledger/capability.go` | 2h |
| P0-09 | 实现本地节点注册（写入 SQLite employee_cache） | `internal/ledger/join.go` | 3h |
| P0-10 | 实现心跳生成和本地状态更新 | `internal/core/beat.go` | 2h |
| P0-11 | 实现本地能力目录查询（SQLite 查询，含筛选） | `internal/ledger/query.go` | 2h |
| P0-12 | 实现节点优雅下线（信号处理 SIGINT/SIGTERM → 标记 offline） | `internal/core/shutdown.go` | 2h |
| P0-13 | 手写两个节点的 `capabilities.yaml` 并验证注册→查询链路 | 两个节点上线，SQLite 中有记录，查询返回正确能力 | 1h |

**Sprint 0.3 · 任务状态机（3-4 天）**

| ID | 任务 | 产出 | 估时 |
|---|---|---|---|
| P0-14 | 实现任务 CRUD（SQLite tasks 表） | `internal/storage/tasks.go` | 3h |
| P0-15 | 实现任务状态机（10 状态 + 合法转移校验） | `internal/core/state.go` | 4h |
| P0-16 | 实现任务事件记录（task_events 表，可重放审计） | `internal/core/state.go` 内 events 方法 | 2h |
| P0-17 | 实现 attempt_id 管理（retry/transfer 新建 attempt_id） | `internal/core/state.go` 内 attempt 方法 | 2h |
| P0-18 | 实现幂等检测（同一 task_id + attempt_id 的重复事件拒绝） | `internal/core/state.go` 内 idempotent 检查 | 2h |
| P0-19 | 实现任务取消级联（取消父任务 → 标记所有未完成子任务为 cancelled） | `internal/core/state.go` 内 cancel 方法 | 2h |
| P0-20 | 编写状态机单元测试（覆盖所有合法/非法转移、幂等、级联取消） | `internal/core/state_test.go` | 3h |

**Sprint 0.4 · 通信层（3-4 天）**

| ID | 任务 | 产出 | 估时 |
|---|---|---|---|
| P0-21 | 实现 WebSocket server（节点监听，接收入站连接） | `internal/bus/ws.go` 内 server | 4h |
| P0-22 | 实现 WebSocket client（节点出站，连接其他节点） | `internal/bus/ws.go` 内 client | 3h |
| P0-23 | 实现消息序列化/反序列化（JSON 信封 + 类型路由） | `internal/bus/msg.go` | 2h |
| P0-24 | 实现 core router（消息 type → 对应 handler 分发） | `internal/core/router.go` | 3h |
| P0-25 | 实现 recv（WebSocket 消息接收 → JSON 解析 → router 分发） | `internal/core/recv.go` | 3h |
| P0-26 | 实现 hello/join 消息处理 | `internal/core/handlers.go` | 2h |
| P0-27 | 实现 task_delegate/task_accept/task_decline/task_result/task_cancel 消息处理 | `internal/core/handlers.go` | 5h |
| P0-28 | 实现断线重连和心跳超时检测 | `internal/bus/ws.go` 内 reconnect 逻辑 | 3h |

**Sprint 0.5 · 执行层（2-3 天）**

| ID | 任务 | 产出 | 估时 |
|---|---|---|---|
| P0-29 | 实现 native 命令执行器（exec.Command, 超时, 捕获 stdout/stderr/exit_code） | `internal/commander/native.go` | 3h |
| P0-30 | 实现第一个 Agent adapter（Python, ~30 行, 适配 Claude Code 或 OpenCode） | `adapters/claude_code.py` | 2h |
| P0-31 | 实现 Commander 整合（收到 task_delegate → 匹配 native/agent → 执行 → 回传 result） | `internal/commander/commander.go` | 4h |
| P0-32 | 实现任务 context（file 类型，打包 repo + branch + commit → 传输） | `internal/core/context.go` | 3h |
| P0-33 | 实现任务结果格式化（exit_code + stdout + stderr + 产物路径） | `internal/commander/result.go` | 2h |

**Sprint 0.6 · 端到端集成与验收（2-3 天）**

| ID | 任务 | 产出 | 估时 |
|---|---|---|---|
| P0-34 | 端到端集成测试：Mac 发 task_delegate → 香橙派执行 native 命令 → 回传 result → Mac 记录完成 | 测试脚本 + 通过记录 | 4h |
| P0-35 | 端到端集成测试：Mac 发 task_delegate → 香橙派拒绝（能力不匹配）→ 正确 decline | 测试脚本 + 通过记录 | 1h |
| P0-36 | 端到端集成测试：任务执行中超时 → 标记 failed | 测试脚本 + 通过记录 | 1h |
| P0-37 | 端到端集成测试：任务取消 → 目标节点停止执行 | 测试脚本 + 通过记录 | 1h |
| P0-38 | 端到端集成测试：节点断线 → 任务正确标记 failed + 心跳超时 | 测试脚本 + 通过记录 | 2h |
| P0-39 | 端到端集成测试：节点重启 → 从 SQLite 恢复任务状态 | 测试脚本 + 通过记录 | 2h |
| P0-40 | 端到端集成测试：重复消息（同一 task_id + attempt_id）→ 幂等处理 | 测试脚本 + 通过记录 | 1h |
| P0-41 | 编写 Phase 0 完成报告（实测数据：内存、延迟、消息大小） | `docs/phase0-report.md` | 2h |

#### 4.0.4 Phase 0 验收标准

```
□ 固定入口节点（Mac）启动，Go 核心内存 < 8MB
□ 香橙派节点启动，Go 核心内存 < 8MB（空载）
□ 两个节点互相可见（本地能力目录可查询）
□ task_delegate → task_accept → running → task_result → done 全链路通过
□ 重复 UUIDv7 消息不产生重复任务
□ 旧 attempt_id 的结果不覆盖新 attempt
□ task_cancel → 目标节点收到取消 → 子任务级联取消
□ task_decline → 调度器收到拒绝原因
□ 任务超时 → 自动标记 failed
□ 节点断线 → 心跳超时检测生效
□ 节点重启 → 任务状态从 SQLite WAL 正确恢复
□ 一个 native 命令正确执行并回传结果
□ 一个 Agent adapter 正确执行并回传结果
□ native 命令执行不调用任何模型
```

#### 4.0.5 Phase 0 决策门

- [ ] 现有架构是否需要调整？
- [ ] Go 核心内存基线是否达标（< 8MB）？
- [ ] 任务闭环延迟是否在可接受范围（< 100ms 非执行开销）？
- [ ] SQLite WAL 崩溃恢复是否可靠？
- [ ] 是否进入 Phase 1？

---

### Phase 1 · 单级委派与入口模型（预计 3-4 周）

#### 4.1.1 目标

在 Phase 0 闭合的任务管线基础上，加入统一入口模型（一次 API 调用决定 answer/tool_call/task）、三层能力路由（native/agent/manual）、CLI 任务面板、以及第二个 Agent adapter。

#### 4.1.2 前置条件

- [ ] Phase 0 验收通过
- [ ] Anthropic API key（Haiku 用于入口模型）
- [ ] 至少两个 Agent CLI 可用（Claude Code + OpenCode 或 Codex）

#### 4.1.3 任务拆解

**Sprint 1.1 · 统一入口模型（3-4 天）**

| ID | 任务 | 产出 | 估时 |
|---|---|---|---|
| P1-01 | 实现系统提示词构造器（注入设备能力摘要 + Hermes 记忆占位） | `internal/entry/prompt.go` | 3h |
| P1-02 | 实现 Haiku API 调用封装（一次调用，含重试和超时） | `internal/entry/model.go` | 4h |
| P1-03 | 实现输出解析器（answer/tool_call/task 三种 kind） | `internal/entry/router.go` | 3h |
| P1-04 | 实现 tool_call 校验与分发（Go 核心校验工具白名单 → 本地执行或转发） | `internal/entry/tool_dispatch.go` | 3h |
| P1-05 | 实现 task 输出校验（必填字段、complexity/risk 范围）→ 写入任务管线 | `internal/entry/task_create.go` | 3h |
| P1-06 | 实现 answer 流式输出（Go 核心流式转发到 CLI） | `internal/entry/stream.go` | 2h |
| P1-07 | 实现入口模型失败降级（API 错误 → 返回用户友好错误，不静默失败） | `internal/entry/fallback.go` | 2h |
| P1-08 | 编写入口模型单元测试（三种 kind 的输入/输出、错误降级、schema 校验） | `internal/entry/*_test.go` | 3h |

**Sprint 1.2 · 三层能力路由（3-4 天）**

| ID | 任务 | 产出 | 估时 |
|---|---|---|---|
| P1-09 | 实现能力匹配器（任务 requires.abilities → 节点 capability 匹配） | `internal/commander/capability.go` | 3h |
| P1-10 | 实现 native 路由（精确匹配 capability card 中的 native 命令） | `internal/commander/native_router.go` | 2h |
| P1-11 | 实现 agent 路由（按用户配置和 agent best_at/not_for 选择） | `internal/commander/agent_selector.go` | 4h |
| P1-12 | 实现 manual 路由（生成通知消息 → 推送到 CLI/日志） | `internal/commander/manual.go` | 2h |
| P1-13 | 实现 Commander 优先级（native > agent > manual，用户指定优先） | `internal/commander/priority.go` | 1h |
| P1-14 | 实现 routing 打分函数（能力匹配度 + 负载 + 心跳新鲜度） | `internal/scheduler/routing.go` | 3h |
| P1-15 | 编写路由决策单元测试（匹配/不匹配/多候选打分/用户指定 agent） | `internal/commander/*_test.go` | 2h |

**Sprint 1.3 · CLI 面板与队列（3-4 天）**

| ID | 任务 | 产出 | 估时 |
|---|---|---|---|
| P1-16 | 实现 CLI 入口（`panda ask "..."` → 入口模型 → 输出） | `cmd/panda/ask.go` | 2h |
| P1-17 | 实现 CLI 队列查看（`panda queue` → 按状态分组显示任务） | `cmd/panda/queue.go` | 3h |
| P1-18 | 实现 CLI 任务详情（`panda task <id>` → 委派链 + 子任务 + 日志） | `cmd/panda/task.go` | 3h |
| P1-19 | 实现 CLI 任务取消（`panda cancel <id>` → 取消 + 级联） | `cmd/panda/cancel.go` | 2h |
| P1-20 | 实现 CLI 日志查看（`panda logs <id>` → task_events 时间线） | `cmd/panda/logs.go` | 2h |
| P1-21 | 实现 `panda status`（节点状态 + 在线设备 + 当前负载） | `cmd/panda/status.go` | 2h |
| P1-22 | 实现 CLI 帮助系统（`panda help`, `panda <cmd> --help`） | `cmd/panda/help.go` | 2h |

**Sprint 1.4 · 第二个 Agent adapter（2-3 天）**

| ID | 任务 | 产出 | 估时 |
|---|---|---|---|
| P1-23 | 编写 OpenCode adapter（~25 行 Python） | `adapters/opencode.py` | 1h |
| P1-24 | 编写 adapter 测试（给定 prompt → 调用 adapter → 验证 JSON 输出格式） | `adapters/test_adapter.py` | 2h |
| P1-25 | 实现 adapter 注册和发现（Go 核心扫描 adapters/ 目录） | `internal/commander/adapter_registry.go` | 2h |
| P1-26 | 更新 `panda install` 扫描逻辑（增加 OpenCode 检测） | `scripts/install.sh` | 1h |

**Sprint 1.5 · 集成与验证（3-4 天）**

| ID | 任务 | 产出 | 估时 |
|---|---|---|---|
| P1-27 | 端到端测试：answer（"今天天气怎么样" → 入口模型判断 answer → 流式输出） | 测试记录 | 1h |
| P1-28 | 端到端测试：tool_call（"提醒我 5 分钟后开会" → 入口模型判断 tool_call → Go 执行） | 测试记录 | 1h |
| P1-29 | 端到端测试：task（"重构 Navbar" → 入口模型输出 task JSON → 路由 → 执行） | 测试记录 | 2h |
| P1-30 | 端到端测试：native 路由（"build iOS" → 匹配 Mac native → 执行 xcodebuild） | 测试记录 | 1h |
| P1-31 | 端到端测试：agent 选择（用户指定 "用 OpenCode 改" → 跳过选择 → 直接 OpenCode） | 测试记录 | 1h |
| P1-32 | 端到端测试：manual 路由（"设计 Figma" → 推通知 → 用户手动标记完成） | 测试记录 | 1h |
| P1-33 | 端到端测试：CLI 全流程（panda ask → queue → task → cancel → logs） | 测试记录 | 2h |
| P1-34 | 编写 Phase 1 完成报告 | `docs/phase1-report.md` | 2h |

#### 4.1.4 Phase 1 验收标准

```
□ answer/tool_call/task 三种输出全部正确处理
□ 入口模型一次调用延迟 < 1s（含 API 往返）
□ native 命令执行不经过 LLM
□ agent 选择按能力声明和用户配置正确路由
□ CLI 面板可用（ask/queue/task/cancel/logs/status）
□ 两个 Agent adapter 都能正确执行并返回结果
□ 用户可覆盖 agent 选择
```

#### 4.1.5 Phase 1 决策门

- [ ] 入口模型分类准确率是否足够（> 90%，人工抽检 50 条）？
- [ ] 三层路由是否覆盖所有任务类型？
- [ ] CLI 面板是否可用且直觉？
- [ ] 是否需要调整 agent adapter 接口？
- [ ] 是否进入 Phase 2？

---

### Phase 2 · 多级委派 + 上下文 + 防御（预计 4-6 周）

#### 4.2.1 目标

从单入口固定委派升级为 P2P 逐边委派（多级链）。加入多类型上下文、分级传输、防御链、权限模型、合并门禁。加入香橙派 GPIO 硬件扩展。

#### 4.2.2 前置条件

- [ ] Phase 1 验收通过
- [ ] 至少 3 个异构节点（香橙派 + Mac + Windows）
- [ ] 香橙派 GPIO 外设就绪（舵机/蜂鸣器/传感器）

#### 4.2.3 任务拆解

**Sprint 2.1 · P2P 逐边委派（4-5 天）**

| ID | 任务 | 产出 |
|---|---|---|
| P2-01 | 实现委派链管理（chain_json 追加、验证环路检测） | `internal/scheduler/chain.go` |
| P2-02 | 实现逐边路由决策（收到 task_delegate → 本地能执行？→ 执行；不能？→ 查表 → 转发） | `internal/scheduler/delegate.go` |
| P2-03 | 实现子调度器模式（委派链中间节点协调下游） | `internal/scheduler/sub_scheduler.go` |
| P2-04 | 实现任务转移（transfer：原节点失去租约 → 新节点获得租约 → 新 attempt_id） | `internal/scheduler/transfer.go` |
| P2-05 | 实现容量驱动并行（节点 capacity 不足 → queued → 评分排序） | `internal/commander/capacity.go` |
| P2-06 | 实现优先级加权评分（user_priority + scheduler_tier + wait_time + resource_efficiency） | `internal/scheduler/priority.go` |
| P2-07 | 编写委派链集成测试（opi3b→Mac→Windows 全链 + 中间节点下线 + 转移） | 测试 |

**Sprint 2.2 · 多类型上下文 + 分级传输（4-5 天）**

| ID | 任务 | 产出 |
|---|---|---|
| P2-08 | 实现四种上下文类型的打包器（file/command/hardware/stream） | `internal/core/context_pack.go` |
| P2-09 | 实现上下文哈希计算（SHA-256，可复现） | `internal/core/context_hash.go` |
| P2-10 | 实现 pointer 传输（task_delegate 带 ctx_hash → 目标查 context_store） | `internal/core/context_pointer.go` |
| P2-11 | 实现 summary 传输（intent + params + constraints → 目标判断是否够用） | `internal/core/context_summary.go` |
| P2-12 | 实现 full 传输（完整快照 → 目标写入 context_store → 开始执行） | `internal/core/context_full.go` |
| P2-13 | 实现 context_fetch/context_ack 协议 | `internal/bus/context_proto.go` |
| P2-14 | 实现 context_store LRU 驱逐（Micro: 5 条, Standard: 50 条） | `internal/storage/context_lru.go` |
| P2-15 | 实现 waiting_context 状态管理（等待快照时不假装执行） | `internal/core/state.go` 补充 |
| P2-16 | 编写分级传输测试（pointer hit/miss, summary 够用/不够, full 传输, LRU 驱逐） | 测试 |

**Sprint 2.3 · 防御链 + 权限 + 门禁（5-6 天）**

| ID | 任务 | 产出 |
|---|---|---|
| P2-17 | 实现 Layer 1 执行监控：范围漂移检测（scope 外文件变更 → 警告/拦截） | `internal/defense/scope_guard.go` |
| P2-18 | 实现 Layer 1 执行监控：收益递减检测（retry > 3 次 → 暂停） | `internal/defense/loopdetect.go` |
| P2-19 | 实现 Layer 1 执行监控：超时/资源超限检测 | `internal/defense/resource_guard.go` |
| P2-20 | 实现 Layer 2 上级决断（3 次循环 → 委派链上级接管分析） | `internal/defense/escalation.go` |
| P2-21 | 实现 Layer 3 对抗性剖析框架（双模型 A 分析/B 审查 → 合并报告） | `internal/defense/postmortem.go` |
| P2-22 | 实现 Tier 1/Tier 2 权限判定引擎 | `internal/security/permissions.go` |
| P2-23 | 实现 Tier 1 自动审批（确定性检查：操作类型 + 范围 + 影响面） | `internal/security/auto_approve.go` |
| P2-24 | 实现 Tier 2 用户决断（沿委派链回传 → 通知用户 → 等待审批） | `internal/security/require_approval.go` |
| P2-25 | 实现合并门禁：范围检查（diff vs scope） | `internal/defense/merge_gate.go` |
| P2-26 | 实现合并门禁：确定性破坏性检查（高风险命令模式匹配） | `internal/defense/merge_gate.go` |
| P2-27 | 实现熔断器（agent:task_type 维度，3 次失败 → open → cooldown → half_open） | `internal/defense/circuit.go` |
| P2-28 | 实现幂等管理器（全局幂等键、旧 attempt 拒绝、状态版本递增） | `internal/core/idempotent.go` |

**Sprint 2.4 · 硬件扩展（3-4 天）**

| ID | 任务 | 产出 |
|---|---|---|
| P2-29 | 实现 GPIO 扩展进程（Python sidecar, Unix socket 通信） | `extensions/gpio/servo.py`, `buzzer.py` |
| P2-30 | 实现 Go ↔ sidecar 通信框架（spawn + register + dispatch + health_check） | `internal/bus/sidecar.go` |
| P2-31 | 实现 hardware 上下文打包器（pin_config + operation + parameters） | `internal/core/context_hardware.go` |
| P2-32 | 实现 `panda detect` 硬件扫描（CPU/RAM/GPU/Display/Servo/Mic 等） | `cmd/panda/detect.go` |
| P2-33 | 实现心跳中 hw 字段（temp_c/cpu_load/ram_free/battery/display/servo_pos） | `internal/core/beat.go` 补充 |
| P2-34 | 端到端：语音指令 → 舵机转动（"向右转 90 度"） | 集成测试 |

**Sprint 2.5 · 集成与验证（4-5 天）**

| ID | 任务 | 产出 |
|---|---|---|
| P2-35 | 多级委派全链测试（opi3b→Mac→Windows + 中间节点下线 + 转移） | 测试记录 |
| P2-36 | 上下文分级传输测试（pointer 命中/未命中，summary/full，跨平台构建） | 测试记录 |
| P2-37 | 防御链测试（注入范围漂移/循环/超时 → 系统检测并响应） | 测试记录 |
| P2-38 | 权限测试（Tier 1 操作自动批 → Tier 2 操作推审批 → 超时拒绝） | 测试记录 |
| P2-39 | 熔断器测试（连续失败 → open → cooldown → half_open → close） | 测试记录 |
| P2-40 | 崩溃恢复测试（kill -9 node → 重启 → SQLite WAL 恢复 → 任务续接） | 测试记录 |
| P2-41 | 网络分区测试（拔网线 → 心跳超时 → 任务 transfer → 恢复 → 幂等拒绝旧消息） | 测试记录 |
| P2-42 | 编写 Phase 2 完成报告 | `docs/phase2-report.md` |

#### 4.2.4 Phase 2 验收标准

```
□ opi3b→Mac→Windows 委派链跑通，Mac 直接调 Windows
□ 同一快照第二次委派命中 pointer（零额外传输）
□ 舵机通过语音指令执行
□ 范围漂移被检测并拦截
□ Tier 2 高风险操作需要用户确认
□ 熔断器在 3 次连续失败后打开
□ 节点 kill -9 后重启，任务状态正确恢复
□ 网络分区期间任务不丢失，恢复后不双跑
□ 旧 attempt 结果不会覆盖新 attempt
```

**ATC-MARL 论文对齐指标**（Phase 2 实测对比基准）：

| 论文指标（N=4 MPE） | PANDA 对应指标 | 测量方法 |
|---|---|---|
| 通信量 ↓60% vs 全广播 | 上下文传输量对比：pointer hit/miss 率 + 平均 context 大小 | 统计 100 次委派的传输字节数 |
| 延迟 ↓40.9% vs 同步等待 | 端到端委派延迟：从入口提交到执行节点收到的时间 | 在 3 个节点上打时间戳统计 |
| 活动边稀疏度 56.7%（5.2/12） | 委派链长度分布 + 子调度器使用率 | 统计 100 个任务的委派链 |
| 得分仅降 3.1% | 任务成功率（done/总提交）+ 平均完成时间 | 统计任务状态分布 |
| 模块协同效应 15-20% | 同时启用 pointer + DCPS 式路由 vs 单独启用的增益 | A/B 对比测试 |

#### 4.2.5 Phase 2 决策门

- [ ] 多级委派链是否稳定？
- [ ] pointer 命中率是否达到预期（> 60%）？
- [ ] 防御链误报率是否可接受（< 10%）？
- [ ] 权限模型是否覆盖所有高风险场景？
- [ ] 硬件驱动是否稳定（24h 连续运行）？
- [ ] 是否进入 Phase 3？

---

### Phase 3 · 记忆 + 语音 + 安全（预计 3-4 周）

#### 4.3.1 目标

引入双层记忆系统（Hermes + 项目记忆）、Dreaming 引擎、Skill 自进化系统、语音入口、PWA 完善、安全加固。

#### 4.3.2 前置条件

- [ ] Phase 2 验收通过
- [ ] 麦克风硬件就绪（香橙派 USB 麦克风）
- [ ] Porcupine 唤醒词 access key

#### 4.3.3 任务拆解

**Sprint 3.1 · 双层记忆（4-5 天）**

| ID | 任务 | 产出 |
|---|---|---|
| P3-01 | 实现 MEMORY.md 格式规范（YAML frontmatter + Markdown body） | 规范文档 |
| P3-02 | 实现 Hermes 记忆管理器（热层/温层/冷层、1300 token 硬上限） | `internal/memory/hermes.go` |
| P3-03 | 实现项目记忆管理器（projects/{name}/MEMORY.md、隔离保证） | `internal/memory/project.go` |
| P3-04 | 实现语义检索（FTS5 全文索引 + 嵌入向量相似度） | `internal/memory/search.go` |
| P3-05 | 实现记忆注入器（系统提示词组装时注入对应层级的记忆） | `internal/memory/injector.go` |
| P3-06 | 实现上下文隔离墙（Hermes 不进项目 agent context，项目不进 Hermes） | `internal/memory/isolation.go` |
| P3-07 | 实现 daily 日志记录（每天的操作自动写入 memory/daily/YYYY-MM-DD.md） | `internal/memory/daily.go` |

**Sprint 3.2 · Dreaming 引擎（3-4 天）**

| ID | 任务 | 产出 |
|---|---|---|
| P3-08 | 实现 Light 阶段（扫描 daily 日志 → 去重去噪 → 暂存候选） | `extensions/dreamer/light.py` |
| P3-09 | 实现 REM 阶段（提取模式 → 构建主题摘要 → 关联洞察） | `extensions/dreamer/rem.py` |
| P3-10 | 实现 Deep 阶段（六维加权评分 → 阈值门控 → 写入 MEMORY.md） | `extensions/dreamer/deep.py` |
| P3-11 | 实现 Dreaming 调度器（节点空闲时自动触发、每日最多 1 次 Deep） | `internal/memory/dream_scheduler.go` |
| P3-12 | 实现 Dream Diary（DREAMS.md 人类可读的记忆整合日志） | `internal/memory/dream_diary.go` |
| P3-13 | 实现 provenance-gated 来源追溯（每条记忆标注来源） | `internal/memory/provenance.go` |

**Sprint 3.3 · Skill 自进化（3-4 天）**

| ID | 任务 | 产出 |
|---|---|---|
| P3-14 | 实现 Skill 触发检测（≥3 次同类任务 + ≥70% 成功率 → 触发生成） | `internal/skills/trigger.go` |
| P3-15 | 实现 Skill 生成器（收集执行历史 → 轻量模型蒸馏 → SKILL.md） | `internal/skills/generator.go` |
| P3-16 | 实现 Skill 审批流（生成 → PWA 推送 → 用户审批/拒绝/修改） | `internal/skills/approval.go` |
| P3-17 | 实现 Skill 作用域隔离（global/project/device 三级） | `internal/skills/scope.go` |
| P3-18 | 实现 Skill 渐进加载（skill index 轻量索引 → 匹配时加载完整内容） | `internal/skills/loader.go` |
| P3-19 | 实现 Skill 生命周期维护（活跃/休眠/过期/建议合并） | `internal/skills/lifecycle.go` |

**Sprint 3.4 · 语音入口（3-4 天）**

| ID | 任务 | 产出 |
|---|---|---|
| P3-20 | 实现 Porcupine 唤醒词 sidecar（Python 进程，检测 "Hey Panda"） | `extensions/voice/wake.py` |
| P3-21 | 实现云端 ASR sidecar（流式语音 → 文本） | `extensions/voice/stt.py` |
| P3-22 | 实现 TTS sidecar（文本 → 语音输出，可选） | `extensions/voice/tts.py` |
| P3-23 | 实现语音管线管理器（wake → ASR → 入口模型 → TTS/执行） | `internal/entry/voice_pipeline.go` |
| P3-24 | 实现 VAD（语音活动检测，静音切分） | `extensions/voice/vad.py` |

**Sprint 3.5 · PWA + 安全加固（4-5 天）**

| ID | 任务 | 产出 |
|---|---|---|
| P3-25 | 实现 PWA 基础框架（React/Vue + 队列面板 + 任务详情） | `web/pwa/` |
| P3-26 | 实现 Web Push 通知（任务状态变更 → 手机推送） | `web/pwa/sw.js` |
| P3-27 | 实现移动端审批（Tier 2 高风险操作 → 手机通知 → 批准/拒绝） | `web/pwa/approval.html` |
| P3-28 | 实现 PWA 历史记录（按状态/项目/日期筛选） | `web/pwa/history.html` |
| P3-29 | 实现执行沙箱（agent 进程只读写 task 目录） | `internal/security/sandbox.go` |
| P3-30 | 实现网络白名单（agent 进程只能访问 API endpoint + git remote） | `internal/security/network_guard.go` |
| P3-31 | 实现密钥隔离（API key → 环境变量注入 → 进程完即消亡，绝不写入文件） | `internal/security/key_vault.go` |
| P3-32 | 实现审计日志（高风险操作全记录，含谁/何时/什么/结果） | `internal/security/audit.go` |

**Sprint 3.6 · 集成与验证（3-4 天）**

| ID | 任务 | 产出 |
|---|---|---|
| P3-33 | 端到端语音测试（"Hey Panda" 唤醒 → ASR → 分类 → 执行 → TTS 回复） | 测试记录 |
| P3-34 | Dreaming 运行测试（空闲触发 → Light → REM → Deep → MEMORY.md 更新） | 测试记录 |
| P3-35 | Skill 全生命周期测试（触发→生成→审批→调用→维护→过期） | 测试记录 |
| P3-36 | 记忆隔离测试（Hermes 数据不在 agent context 中泄露） | 测试记录 |
| P3-37 | 安全渗透测试（注入攻击/密钥泄露/越权操作/沙箱逃逸） | 安全报告 |
| P3-38 | 编写 Phase 3 完成报告 | `docs/phase3-report.md` |

#### 4.3.4 Phase 3 验收标准

```
□ 语音唤醒→ASR→分类→执行→通知 全链路延迟 < 3s（不含 agent 执行时间）
□ Dreaming 每日自动运行，DREAMS.md 可读
□ Skill 自动生成 → 用户审批 → 下次同类任务自动调用
□ Hermes 记忆绝对不在 agent 项目上下文中出现
□ Tier 2 高风险操作手机推送确认
□ 沙箱内 agent 无法访问系统敏感路径
□ API key 不在任何文件中明文存储
```

#### 4.3.5 Phase 3 决策门

- [ ] 语音识别准确率是否达标（> 90%）？
- [ ] Dreaming 输出质量是否可靠（人工抽检 10 天）？
- [ ] Skill 自动生成质量是否合格（成功率 > 70%）？
- [ ] 安全审计是否通过（无高危漏洞）？
- [ ] 是否进入 Phase 4（正式版发布准备）？

---

### Phase 4 · 硬件 + 扩展 + 发布（持续，远期愿景）

#### 4.4.1 目标

桌宠硬件落地（外壳 + 屏幕 + 舵机）、超算/任意设备入职支持、社区文档完善、托管服务上线、正式版发布。

#### 4.4.2 任务拆解

**Sprint 4.1 · 桌宠硬件（4-6 周，并行）**

| ID | 任务 | 产出 |
|---|---|---|
| P4-01 | 确定屏幕方案（墨水屏 vs IPS LCD vs 无屏，实物对比后定） | 硬件选型记录 |
| P4-02 | 确定舵机自由度（2-3 vs 5+，实物测试后定） | 硬件选型记录 |
| P4-03 | 3D 外壳设计（预留屏幕/舵机/喇叭/散热开孔） | STL 文件 |
| P4-04 | 3D 打印 + 试装 | 实物外壳 |
| P4-05 | 表情渲染引擎（idle/listening/thinking/working/done/error/offline） | `extensions/display/face.py` |
| P4-06 | 舵机动作映射（状态 → 舵机角度序列） | `extensions/gpio/pose.py` |
| P4-07 | M.2 存储迁移 + swap 分层配置 | 部署文档 |
| P4-08 | 桌宠 48h 连续运行稳定性测试 | 测试报告 |

**Sprint 4.2 · 开放与生态（3-4 周）**

| ID | 任务 | 产出 |
|---|---|---|
| P4-09 | 编写 adapter 贡献指南（模板 + 接口规范 + 测试要求 + PR 流程） | `CONTRIBUTING.md` |
| P4-10 | 编写 Skill 贡献指南（格式规范 + 示例 + 审批标准） | `docs/skill-guide.md` |
| P4-11 | 编写部署文档（香橙派/树莓派/Mac/Windows/Linux/超算） | `docs/deployment/` |
| P4-12 | 编写用户手册（入门/任务/设备/记忆/Skill/安全/FAQ） | `docs/manual/` |
| P4-13 | 搭建 GitHub Actions CI（lint + test + build + cross-compile） | `.github/workflows/` |
| P4-14 | 搭建贡献者社区（Discord/Discussion） | 社区链接 |
| P4-15 | 发布 Homebrew formula / apt repo / AUR package | 安装方式 |
| P4-16 | 发布 v1.0.0 GitHub Release + changelog | Release |

**Sprint 4.3 · 托管服务（4-6 周，可选）**

| ID | 任务 | 产出 |
|---|---|---|
| P4-17 | 实现服务器端员工表 API（Go, PostgreSQL） | `server/` |
| P4-18 | 实现服务器端任务索引 API | `server/` |
| P4-19 | 实现服务器端 PWA 托管 | `server/` |
| P4-20 | 实现用户认证（OAuth GitHub/Google） | `server/auth/` |
| P4-21 | 实现多用户隔离 | `server/multi_tenant/` |
| P4-22 | 搭建托管基础设施（Fly.io / Railway） | 部署配置 |
| P4-23 | 实现计费系统（免费 ≤3 设备, Pro 无限设备） | `server/billing/` |
| P4-24 | 托管服务 beta → 正式上线 | 上线 |

---

## 五、测试策略与质量保障

### 5.1 测试金字塔

```
                    ┌──────┐
                    │ E2E  │  少量，关键链路
                    │ ~20  │
                   ┌┴──────┴┐
                   │ 集成测试 │  中量，跨模块交互
                   │  ~50   │
                  ┌┴─────────┴┐
                  │  单元测试   │  大量，每个模块核心逻辑
                  │   ~200+   │
                  └───────────┘
```

### 5.2 各阶段测试重点

| 阶段 | 单元测试 | 集成测试 | E2E 测试 | 专项测试 |
|---|---|---|---|---|
| Phase 0 | 状态机、幂等、消息序列化 | WebSocket 通信、任务闭环 | 2 节点全链路 | 崩溃恢复、消息重放 |
| Phase 1 | 入口模型解析、路由决策 | 入口→路由→执行 | answer/tool_call/task 全链路 | 入口模型准确率人工抽检 |
| Phase 2 | 委派链、上下文、熔断器 | 多级委派、分级传输 | 3 节点全链 | 网络分区、故障注入 |
| Phase 3 | 记忆/Skill/权限逻辑 | Dreaming 管线、语音管线 | 语音全链路 | 安全渗透测试 |
| Phase 4 | 硬件驱动 | 桌宠集成 | 48h 连续运行 | 性能基准、压力测试 |

### 5.3 测试环境矩阵

| 环境 | 用途 | 配置 |
|---|---|---|
| 本地开发 | 开发 + 单元测试 | Mac (ARM64) |
| CI | 自动化测试 | GitHub Actions (ubuntu + macos runner) |
| 集成环境 | 集成 + E2E | Mac + 香橙派 (Tailscale 组网) |
| 预发布 | 安全审计 + 性能测试 | Mac + 香橙派 + Windows (完整集群) |

### 5.4 质量门禁

每个 Phase 的代码合并前必须通过：

```
□ 所有单元测试通过
□ 所有集成测试通过
□ 所有 E2E 测试通过
□ go vet / go staticcheck 无告警
□ 代码覆盖率 > 70%（核心模块 > 85%）
□ 无已知安全漏洞（govulncheck）
□ Phase 验收标准全部达成
```

### 5.5 Bug 管理策略

| 严重度 | 定义 | 响应时间 | 修复策略 |
|---|---|---|---|
| P0 致命 | 数据丢失、安全漏洞、系统崩溃 | 即时 fix | 阻塞发布 |
| P1 严重 | 核心功能不可用 | 24h | 当前 sprint 修复 |
| P2 一般 | 非核心功能异常、体验问题 | 1 周 | 下个 sprint |
| P3 轻微 | 视觉/文案问题 | 2 周 | backlog |

---

## 六、部署与运维计划

### 6.1 部署架构演进

```
Phase 0-2: 纯本地                            Phase 3-4: + 可选服务器
┌─────────────┐                              ┌─────────────┐
│ Mac (入口)   │──── Tailscale ────           │ PWA 面板     │
│ Go Core     │                   │          │ (手机)       │
│ SQLite      │              ┌────┴────┐     └──────┬──────┘
│ 能力目录     │              │ 香橙派    │            │
└─────────────┘              │ Go Core  │     ┌──────┴──────┐
                             │ GPIO     │     │ 员工表服务器  │
                             └─────────┘     │ (可选)       │
                                             │ PostgreSQL  │
                                             └─────────────┘
```

### 6.2 安装方式演进

| Phase | Mac | Linux (ARM64) | Windows | 其他 |
|---|---|---|---|---|
| Phase 0 | `go build` | `GOOS=linux GOARCH=arm64 go build` | 不支持 | 不支持 |
| Phase 1 | shell 安装脚本 `curl ... \| bash` | 同左 | 不支持 | 不支持 |
| Phase 2 | shell 脚本 + binary release | binary release | binary release (amd64) | 不支持 |
| Phase 4 | Homebrew | apt repo + AUR | winget / scoop | Docker |

### 6.3 配置管理

```yaml
# /etc/panda/config.yaml（Phase 0 最小配置）
node:
  name: "macbook-m1"
  resource_class: "Standard"

network:
  listen_addr: ":7836"           # WebSocket 监听
  peers:                          # 手动配置的对等节点
    - "orangepi3b.tailnet-name.ts.net:7836"

storage:
  db_path: "/var/lib/panda/panda.db"
  context_path: "/var/lib/panda/context"

# Phase 3 新增
memory:
  hermes_path: "~/.panda/memory/"
  max_hot_tokens: 1300

skills:
  bank_path: "~/.panda/skills/"
  auto_approve: false

# Phase 4 新增
ledger:
  url: "https://panda.xenith.sh"  # 或 Tailscale Funnel
```

### 6.4 监控与日志

| 层级 | 工具 | 内容 |
|---|---|---|
| Go 核心 | `slog` (结构化 JSON 日志) | 状态变更、消息收发、错误、性能指标 |
| Python 扩展 | Python `logging` → stdout → Go 采集 | adapter 输入/输出、异常 |
| 系统 | journald (Linux) / unified log (macOS) | 进程启动/崩溃、资源使用 |
| 审计 | `task_events` 表 | 完整可重放的操作记录 |

### 6.5 备份与恢复

| 数据 | 备份策略 | 恢复策略 |
|---|---|---|
| SQLite 数据库 | 每日 WAL checkpoint + 文件快照 | 直接恢复 SQLite 文件 |
| 上下文快照 | 不在备份范围（可从源节点重新获取） | context_fetch 重新拉取 |
| MEMORY.md | Git 版本控制（自动 commit） | git revert |
| Skills | Git 版本控制 | git revert |
| 日志 | 30 天滚动，自动清理 | 不需要恢复 |

---

## 七、发布与增长策略

### 7.1 发布节奏

| 阶段 | 发布方式 | 目标受众 | 沟通渠道 |
|---|---|---|---|
| Phase 0 | 无发布（内部开发） | 仅作者 | 无 |
| Phase 1 | GitHub 私有仓库 | 作者 + 2-3 个朋友试用 | 私聊 |
| Phase 2 | GitHub 公开仓库 `v0.5.0-alpha` | 早期开源用户 | GitHub Discussions |
| Phase 3 | `v0.9.0-beta` | 公测用户 | Discord + GitHub |
| Phase 4 | `v1.0.0` 正式版 | 所有用户 | Hacker News, Reddit, 技术博客 |

### 7.2 关键发布物

每次公开发布需准备：

```
□ Release Notes（CHANGELOG.md，按 Added/Changed/Fixed/Removed 分类）
□ 二进制文件（darwin-arm64, linux-arm64, linux-amd64, windows-amd64）
□ 安装指南（更新 README.md）
□ 升级指南（如有破坏性变更）
□ 视频 Demo（Phase 2 起，每次发布 3-5 分钟功能演示）
```

### 7.3 增长里程碑

| 里程碑 | 指标 | 预计时间 |
|---|---|---|
| 个人稳定使用 | 作者本人日常使用 ≥ 2 周无阻塞 bug | Phase 1 结束后 |
| 首个外部用户 | GitHub star ≥ 10，有外部用户成功部署 | Phase 2 发布后 2 周 |
| 社区贡献 | 首个外部 PR（adapter 或 skill）被合并 | Phase 2 发布后 4 周 |
| 100 stars | GitHub 100 stars | Phase 2 发布后 8 周 |
| 1000 stars | GitHub 1000 stars | Phase 3 发布后 |
| 社区自运转 | > 5 个外部 adapter 贡献, > 20 个 skill | Phase 4 |

### 7.4 内容营销计划

| 内容 | 形式 | 发布时机 | 目标 |
|---|---|---|---|
| "为什么我写了 PANDA" | 技术博客 | Phase 2 发布时 | 引发共鸣，吸引早期用户 |
| "一个语音指令控制三台电脑" | 视频 Demo | Phase 3 发布时 | 破圈传播 |
| "构建异构设备集群：PANDA 架构详解" | 深度技术文章 | Phase 4 发布时 | 技术品牌建设 |
| "桌宠：给 AI 一个身体" | 产品故事 | Phase 4 桌宠完成时 | 情感链接 |

---

## 八、风险管理

### 8.1 风险矩阵

| 风险 | 概率 | 影响 | 等级 | 缓解措施 | 触发条件 |
|---|---|---|---|---|---|
| **架构方向错误** | 低 | 高 | 中 | 每 Phase 设决策门，允许调整；Go 核心接口简洁，替换成本可控 | Phase 验收持续不达标 |
| **AI 模型能力跃升** | 中 | 高 | 高 | 架构不绑定模型；调度/记忆/Skill 仍是独有 | 基础模型原生支持多设备调度 |
| **Agent CLI 接口变更** | 中 | 中 | 中 | adapter 层 30 行，快速适配；关注各 CLI changelog | adapter 调用失败 |
| **跨平台兼容性问题** | 中 | 中 | 中 | 早期交叉编译验证（Phase 0 已在 Mac + ARM64 测试）；Windows 在 Phase 0 暂不支持 | 编译或运行时错误 |
| **Token 成本失控** | 中 | 高 | 高 | 五道成本防线；Token 预算系统；本地实测成本基准 | 日 token 消耗 > 预算 2x |
| **安全漏洞** | 高 | 高 | 高 | OpenClaw 教训规避；Phase 3 安全审计；沙箱 + 网络白名单 | 任何密钥泄露或越权操作 |
| **开发时间超期** | 高 | 中 | 中 | 每 Sprint 2-3 天 buffer；Phase 可独立发布，不互相阻塞 | 单 Sprint 延期 > 50% |
| **用户不买账** | 中 | 高 | 高 | 早期开源验证（Phase 2）；快速迭代；社区反馈闭环 | Phase 2 发布后 4 周无外部用户 |
| **竞品抢先发布** | 低 | 中 | 低 | 差异化壁垒（调度+记忆+Skill）；关注竞品动态 | 直接竞品发布 |

### 8.2 技术风险缓解

| 风险 | 具体缓解措施 |
|---|---|
| SQLite 并发写入瓶颈 | WAL 模式 + 单写者设计；监控写入延迟；Phase 3+ 可评估 PostgreSQL 迁移 |
| WebSocket 连接不稳定 | 自动重连 + 指数退避；心跳超时检测；Tailscale 组网降低网络层问题 |
| Python subprocess 开销 | adapter 是按需启动的瞬态进程，用完即回收；常驻扩展（语音/GPIO）走 Unix socket |
| 香橙派性能不足 | zram + 无 swap SD 卡；Micro 扩展用完即卸；Go core ~6MB 基线；监控 CPU/内存 |
| 跨版本协议兼容 | 消息信封 `v` 字段；版本协商在 Phase 2 前定义；向后兼容 1 个大版本 |

### 8.3 应急计划

| 场景 | 响应 |
|---|---|
| Phase 0 核心指标不达标（内存 > 10MB） | 性能分析 → 优化热路径 → 仍不达标则重新评估 Go 方案 |
| Phase 1 入口模型准确率 < 80% | 调整 prompt → 增加 few-shot 示例 → 仍不达标则评估更强模型 |
| Phase 2 委派链稳定性差 | 简化链长度 → 增加重试 → 增加健康检查频率 |
| 关键依赖（如 Claude Code CLI）废弃或大改 | adapter 层隔离 → 30 行更新 → 72h 内适配 |
| 社区完全不感兴趣 | 重新评估产品方向 → 聚焦个人使用价值 → 或 pivot |

---

## 九、资源规划

### 9.1 人力资源

| 阶段 | 角色 | 人数 | 投入程度 | 关键技能 |
|---|---|---|---|---|
| Phase 0-1 | 全栈开发者 | 1（作者） | 全职 | Go, Python, SQLite, WebSocket, Tailscale |
| Phase 2 | 全栈开发者 | 1 | 全职 | + 硬件基础（GPIO/PWM） |
| Phase 3 | 全栈开发者 | 1 | 全职 | + 前端（React/Vue PWA）、安全 |
| Phase 3 | 安全审计（可选） | 0-1 | 按需 | 渗透测试、安全审计 |
| Phase 4 | 全栈开发者 | 1 | 全职 | + 3D 建模（可外包） |
| Phase 4 | 社区运营（可选） | 0-1 | 兼职 | 社区管理、文档 |

### 9.2 硬件资源

| 设备 | 用途 | 已有？ | 配置 |
|---|---|---|---|
| Mac (M1) | 主开发机 + 入口节点 | ✅ | 16GB RAM · 256GB SSD |
| 香橙派 3B | 目标部署节点 + 嵌入式 | ✅ | RK3566 · 2GB RAM · SD + M.2（待购） |
| Windows 台式机 | GPU 执行节点 | ✅ | RTX 4060 8GB |
| 香橙派 M.2 SSD | 存储扩展 | ❌ 待购 | 128-256GB NVMe M-Key 2230/2242 |
| USB 麦克风 | 语音入口 | ❌ 待购 | Phase 3 前 |
| 舵机 + 驱动板 | 桌宠动作 | ❌ 待购 | Phase 4 前 |
| 屏幕（墨水屏/LCD） | 桌宠表情 | ❌ 待购 | Phase 4 前 |
| 3D 打印服务 | 外壳 | ❌ 外包 | Phase 4 |

### 9.3 软件与服务资源

| 服务 | 用途 | 费用 | Phase |
|---|---|---|---|
| Anthropic API | 入口模型（Haiku）+ Agent（Claude Code） | ~$20-100/月 | Phase 1+ |
| OpenAI API（可选） | Codex adapter | ~$10-50/月 | Phase 1+ |
| GitHub | 代码托管 + CI/CD | 免费 | 全程 |
| Tailscale | 组网 | 免费（个人） | 全程 |
| Fly.io / Railway | 托管服务（可选） | 免费层 | Phase 4 |
| Porcupine | 唤醒词 | 免费（个人） | Phase 3 |
| 域名 | panda.xenith.sh | ~$15/年 | Phase 4 |

---

## 十、预算规划

### 10.1 各阶段成本估算

| 阶段 | 时间 | 人力成本 | 硬件/服务 | 总成本 |
|---|---|---|---|---|
| Phase 0 | 2-3 周 | 作者全职（0 额外） | $0（已有设备） | $0 |
| Phase 1 | 3-4 周 | 作者全职 | API ~$20-100 | ~$20-100 |
| Phase 2 | 4-6 周 | 作者全职 | API ~$50-200 + M.2 ~$20 | ~$70-220 |
| Phase 3 | 3-4 周 | 作者全职 | API ~$100-300 + 麦克风 ~$30 | ~$130-330 |
| Phase 4 | 持续 | 作者全职 + 可选外包 | 舵机 ~$20 + 屏幕 ~$15 + 3D 打印 ~$50 + 域名 ~$15/年 | ~$100 + 运营 |
| **Phase 0-4 总计** | **15-20 周** | **1 人全职** | **~$320-750** | **~$320-750 + 人力** |

### 10.2 运营期月度成本预估（Phase 4+）

| 项目 | 月度成本 | 备注 |
|---|---|---|
| API 调用（个人重度使用） | ~$50-200 | 取决于任务量和模型 |
| 托管服务（Fly.io 免费层升级） | ~$0-25 | 用户增加后升级 |
| 域名 | ~$1.25/月 | $15/年 |
| **月度合计** | **~$50-225** | 自用场景 |

---

## 十一、里程碑与时间线

### 11.1 总体时间线

```
2026 Q3                      Q4                        2027 Q1
├─────────┼─────────┼─────────┼─────────┼─────────┼─────────┤
│ Phase 0  │ Phase 1  │  Phase 2              │ Phase 3  │Phase 4...│
│ 2-3 周   │ 3-4 周   │  4-6 周               │ 3-4 周   │ 持续      │
├─────────┼─────────┼─────────┼─────────┼─────────┼─────────┤
8/12      9/2       9/30      11/11              12/9      持续
立项      本地闭环   单级委派   多级委派+防御        记忆+语音   硬件+发布
```

### 11.2 关键日期

| 日期 | 里程碑 | 交付物 |
|---|---|---|
| **2026-08-12** | 立项 + 设计文档完成 | 设计文档 v4.5 + 本计划书 v1.0 |
| **2026-08-15** | Phase 0 Sprint 0.1 开始 | 项目骨架 |
| **2026-08-22** | Phase 0 Sprint 0.3 完成 | 任务状态机可测试 |
| **2026-08-29** | Phase 0 Sprint 0.5 完成 | 通信+执行可集成 |
| **2026-09-02** | **🎯 Phase 0 完成** | 本地任务闭环验收通过 |
| **2026-09-05** | Phase 1 Sprint 1.1 开始 | 统一入口模型 |
| **2026-09-19** | Phase 1 Sprint 1.3 完成 | CLI 面板可用 |
| **2026-09-30** | **🎯 Phase 1 完成** | 单级委派验收通过 |
| **2026-10-07** | Phase 2 Sprint 2.1 开始 | P2P 逐边委派 |
| **2026-10-28** | Phase 2 Sprint 2.3 完成 | 防御+权限可用 |
| **2026-11-11** | **🎯 Phase 2 完成** | 多级委派+防御验收通过 |
| **2026-11-14** | Phase 3 Sprint 3.1 开始 | 双层记忆 |
| **2026-12-02** | Phase 3 Sprint 3.5 完成 | PWA+安全可用 |
| **2026-12-09** | **🎯 Phase 3 完成** | 记忆+语音+安全验收通过 |
| **2026-12-12+** | Phase 4 并行开始 | 硬件+社区+托管 |

### 11.3 Sprint 节奏

- **Sprint 长度**：以任务粒度为准（通常 2-5 天），不使用固定双周 Sprint；这样更灵活
- **每日站会**：无需（单人），但每天结束时更新任务进度
- **回顾**：每个 Phase 结束时进行，记录经验教训到 `docs/retrospectives/`

---

## 十二、成功度量指标

### 12.1 各阶段成功指标

| Phase | 技术指标 | 体验指标 | 增长指标 |
|---|---|---|---|
| Phase 0 | 内存 < 8MB, 延迟 < 100ms, 崩溃恢复 100% | ╳（内部） | ╳ |
| Phase 1 | 入口准确率 > 90%, 端到端成功率 > 95% | 作者本人能日常使用 | ╳ |
| Phase 2 | pointer 命中率 > 60%, 防御误报 < 10% | 3 台设备稳定运行 | GitHub stars > 0 |
| Phase 3 | 语音准确率 > 90%, Dreaming 质量达标 | 语音可用，记忆演进可见 | stars > 100 |
| Phase 4 | 48h 连续运行无 crash, 测试覆盖率 > 80% | "开箱即用"的用户体验 | stars > 1000, > 5 个外部 adapter |

### 12.2 北极星指标

**Phase 1-2**：作者本人每天使用 PANDA 完成 ≥ 3 个跨设备任务，且不需要手动 SSH 到其他设备。

**Phase 3-4**：用户设备上的 PANDA 守护进程连续运行 ≥ 7 天，自动处理 ≥ 10 个任务，无需人工干预.

### 12.3 放弃标准

每个 Phase 的决策门如果出现以下情况，应暂停并重新评估：

- 连续 2 个 Sprint 核心目标未达成
- 技术指标持续低于目标值 30% 以上
- 发现不可克服的技术阻塞点（如协议无法稳定、性能不可优化到目标）
- 外部环境变化导致产品失去价值（如基础模型已完美解决多设备调度）

---

## 十三、附录

### 附录 A · 技术选型决策记录

| 决策 | 选项 | 选择 | 原因 | 日期 |
|---|---|---|---|---|
| 核心语言 | Rust / Go / Python | Go | 交叉编译 + 低内存 + goroutine | 2026-08-12 |
| 扩展语言 | Python / Rust / Node.js | Python (subprocess) | 快速开发 + Go 不支持的动态加载 | 2026-08-12 |
| 数据库 | SQLite / PostgreSQL / BoltDB | SQLite | 零运维 + WAL + 嵌入式友好 | 2026-08-12 |
| 通信协议 | WebSocket / gRPC / NATS | WebSocket + JSON | 简单 + 调试友好 + 浏览器兼容 | 2026-08-12 |
| 组网 | Tailscale / WireGuard / ZeroTier | Tailscale | 零配置 + Funnel + 免费个人版 | 2026-08-12 |
| 序列化 | JSON / MessagePack / Protobuf | JSON + MessagePack 混合 | 控制面可调试 + 数据面紧凑 | 2026-08-12 |
| 入口模型 | Haiku / GPT-4o-mini | Haiku | 低成本 + 低延迟 + 足够分类精度 | 2026-08-12 |
| PWA 框架 | React / Vue / Svelte | 待定 | Phase 3 前对比后决定 | 待定 |

### 附录 B · 术语表

参见 [设计文档 §27](./PANDA-总览设计文档.md#二十七附录完整术语表)

### 附录 C · 参考文档与理论来源

| 文档 | 路径 | 状态 |
|---|---|---|
| 总览设计文档 | `./PANDA-总览设计文档.md` | v4.6, 已完成（已整合 ATC-MARL 论文细节） |
| 产品开发计划书 | `./PANDA-产品开发计划书.md`（本文档） | v1.1, 已完成（已整合论文定量参考） |
| ATC-MARL 论文（中译版） | 微信文件 `ATC_MARL_Paper_Chinese.docx` | 徐浩博, 2026.05 |
| 消息协议规范 | `./docs/PROTOCOL.md` | 待写（Phase 0 产出） |
| API 文档 | `./docs/API.md` | 待写（Phase 1 产出） |
| 部署指南 | `./docs/DEPLOY.md` | 待写（Phase 2 产出） |
| 用户手册 | `./docs/USER_GUIDE.md` | 待写（Phase 3 产出） |
| 贡献指南 | `./CONTRIBUTING.md` | 待写（Phase 4 产出） |

**ATC-MARL 论文关键数据速查**：

| 指标 | 值 | 论文位置 |
|---|---|---|
| vs MAPPO 得分差距 | -3.1%（统计不显著, p=0.082） | Table VI, VII |
| 通信量降低 | 60.0% | Table VI |
| 单步延迟降低 | 40.9% | Table VI |
| 模块协同效应 | 15-20% | §VI-A |
| ALC 压缩分布 | 32dim=38%, 16dim=39%, 64dim=12%, 8dim=11% | Table XIII |
| DCPS 稀疏度 N=4 | 56.7%（5.2/12 边） | §V-F |
| DCPS 稀疏度 N=8 | 73.9%（14.6/56 边） | §V-F |
| 每智能体通信量 N=8 vs N=4 | 82.0 vs 102.5（不升反降） | Table X |
| TMB 延迟贡献 | ≈46% | Table XII |
| ALC 通信量贡献 | ≈60% | Table XII |
| DCPS 拓扑贡献 | ≈40% | Table XII |
| Gumbel 温度退火 | τ₀=1.0, τ←τ·0.995/轮 | Table V |

### 附录 D · 文档更新策略

- **本计划书**：每个 Phase 完成后更新，反映实际进度和下一 Phase 的详细计划
- **设计文档**：架构变更时更新，保持"单一权威参考"地位
- **Release Notes**：每次公开发布时更新
- **CHANGELOG**：每次合并 PR 时更新（使用 conventional commits）

---

*文档版本 v1.1 · 2026-08-12 · 随项目推进持续更新*

*本文档与 [PANDA-总览设计文档](./PANDA-总览设计文档.md)（v4.6，已整合 ATC-MARL 论文细节）配套使用：设计文档定义"是什么"，本计划书定义"怎么做"。*

*v1.1 更新：新增理论可行性评估（ATC-MARL 论文定量验证）、Phase 2 验收标准中的论文对齐指标、附录 C 论文关键数据速查表。*
