# 🐼 OpenPanda

**开源、本地优先的多 Agent 协同操作系统**

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![Release](https://img.shields.io/github/v/release/Xustalis/OpenPanda?label=release&color=blue)](https://github.com/Xustalis/OpenPanda/releases)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-00ADD8)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-3776AB)
![Platforms](https://img.shields.io/badge/%E5%B9%B3%E5%8F%B0-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Memory](https://img.shields.io/badge/%E5%86%85%E5%AD%98%E5%B8%B8%E9%A9%BB-~20MB%20RSS-brightgreen)
![Local First](https://img.shields.io/badge/%E4%BA%91%E7%AB%AF%E4%BE%9D%E8%B5%96-%E9%9B%B6%E4%BE%9D%E8%B5%96-success)

***

## 简介

OpenPanda 将你机器上已有的终端 AI 编程助手——内置十个适配器（Claude Code、OpenAI Codex、OpenCode、Grok Build、DeepSeek Harness、Antigravity、Hermes、Pi、ZCode、OpenClaw），外加一个兜底其余一切的 generic 桥——与你的设备组织为一支统一的执行队伍。它本身不是 Agent：你发出一条指令，它负责意图理解、任务拆解、调度派发、执行监督与结果校验——全程本地运行，不依赖任何云端服务。

```
┌─────────────────────────────────────────────────────────────┐
│                       你：发送一条指令                       │
│             （终端 TUI / 网页控制台 / CLI 脚本）            │
└──────────────────────────────┬──────────────────────────────┘
                               │
                  ┌────────────▼────────────┐
                  │     🐼 OpenPanda OS     │
                  │   意图解析 · 智能路由   │
                  │   双向监督 · 安全风控   │
                  └────────────┬────────────┘
                               │ 点对点直连（WS + 密封 UDP，无云端中转）
     ┌─────────────────────────┼─────────────────────────┐
     │                         │                         │
┌────▼──────────────┐   ┌──────▼────────────┐   ┌────────▼────────────┐
│  MacBook（工作节点）│   │  Linux 编译服务器  │   │  树莓派 / SBC 开发板│
│  - Claude Code    │   │  - Codex / Docker │   │  - 7×24h 自动化守护 │
└───────────────────┘   └───────────────────┘   └─────────────────────┘
```

项目围绕两条主线演进：

| 主线               | 说明                              | 状态                        |
| ---------------- | ------------------------------- | ------------------------- |
| **多 Agent 协同控制** | 雇佣并统一指挥多个终端 Agent：派发、监督、故障转移、审批 | ✅ 当前工作重点                  |
| **多设备协同控制**      | P2P 网络内跨节点任务路由与调度               | ✅ v0.0.10 已发布；v0.0.11 追加逐连接 AEAD、蓝牙式配对、信任分档与多跳中继；v0.0.12 追加同项目并行执行（路径作用域泳道与隔离 worktree） |

***

## ✨ 核心特性

### Agent 编排

- **意图分类与任务拆解** —— 意图分类引擎区分闲聊、管理操作与可执行任务；多阶段任务先拆解为带产物接线的 Plan，再逐阶段推进。

- **监督循环（Supervision Loop）** —— 入口模型持续判定任务完成度，不合格则携带失败原因重新派发，直到完成或超出轮次预算（默认 5 轮）。

- **多模型注册中心与无感故障转移** —— `panda model add|switch|test` 为每个节点管理多个模型供应商并带健康熔断；配额耗尽或凭据失效（401/403）时自动注入备用模型接力执行，任务不中断。

- **多 Harness 并行** —— `panda ask --agents a,b --mode parallel` 把一条指令作为 plan 同时派给多个 Agent 并回收各自结论。

- **透明执行** —— 每条 Bash 命令、每次文件修改、每次工具调用及其背后的 Agent 与模型，实时流式回传终端与控制台。

- **分级审批** —— 可逆操作全自动执行；`git push`、修改生产数据库等不可逆操作挂起等待人工确认。

- **熔断保护** —— 死循环侦测与重试熔断器，防止 Agent 失控消耗 Token。

- **任务间消息板**（实验性）—— 同属一个 plan 或同一父任务的兄弟任务通过有界槽位板交换短消息（一行状态、一条发现、一个产出文件指针）；每个 Agent 在自己的下一轮读到同伴的留言，而不是阻塞等待对方的结果，按周期计的预算避免信道沦为闲聊。

### 记忆与技能

- **双层记忆** —— 用户偏好（`USER.md`）与项目事实（`MEMORY.md`）严格分层。

- **自进化技能** —— 成功流程沉淀为 `SKILL.md`，随使用积累。

- **技能中心与自主发现** —— 离线精选技能目录（`panda skill hub`）、从路径/URL/压缩包导入（`panda skill import`），助手还能在任务执行中自主找到并安装所需技能。

- **上下文漫游** —— 跨设备委派任务时，项目记忆与工作目录摘要随任务同行。

### 多设备协同

- **LAN 自动发现 + 蓝牙式配对** —— 节点在局域网内自动互见，凭指纹确认准入；`panda pair` 通过两端核对同一短码完成入网，`panda nodes verify` 钉住每个对端的 Ed25519 密钥。

- **能力卡宣告** —— 每台设备自动生成硬件与工具画像（CPU、内存、系统、可用 Agent）。

- **P2P 直连** —— 节点间通过认证 WebSocket 通信（握手内协商逐连接 AEAD 加密）加加密 UDP 数据面（支持 NAT 穿透），数据不离开私有网络。

- **信任分区** —— 每个对端带一个运营者设定的信任档（`panda nodes trust <id> mesh|guarded|open`），钳制委派出站可暴露给它的内容以及来自它的入站任务如何准入；显示内容端到端仅限 mesh 档——信任降级会中途掐断截屏传输。

- **按需路由** —— 调度器按实测容量评分将任务派往最匹配的节点；`panda nodes drain` 可将节点下线维护。

- **工作树随委派同行** —— 委派到其他节点的文件任务会携带工作目录过去并把结果带回。

- **多跳 mesh** —— 硬钉选（`--preferred`、阶段 `node:`）与 context/artifact 数据面在部分网状拓扑上跨中继路由：两跳外的节点沿 gossip 链路状态寻址，够不到源节点的执行者经交付它的那一跳逐段上溯拉取快照与输入。

- **Actuator 派发** —— 任务可驱动持有硬件的节点上的物理执行器；`drivers/` 随发行包附带七个参考实现（舵机、麦克风、摄像头、通知、串口 MCU、截屏、GUI 输入），桌面三件套另有 `.ps1` 变体，原生 Windows 节点无需 git-bash。
- **Computer-use 组合** —— 有桌面会话的节点会广告 `hardware:screen_capture`（截屏）与 `hardware:gui_input`（点击、输入、按键、滚动——点击不可撤销故为 tier 2），该机器上的 agent 由此看见并操作自己的 GUI；无头守护进程不虚报看不见的屏幕。屏幕内容只在 mesh 信任档节点之间流动。
- **产物可取回** —— 任务产出的东西（截图、报告文件）会被打包拉回：`panda task artifact <id>` 列清单并解出，控制台可内联预览图片与文本。

### 会话与嵌入

- **会话树** —— `panda session fork <id> --at N` 在任意轮次边界分叉对话；仓库内子会话的 worktree 从父分支切出，继承父会话产出的代码。`panda session tree` 渲染家族树，REPL/TUI 内 `/fork` 同样可用。

- **自动压缩** —— 溢出的历史折叠为模型撰写的滚动摘要而非丢弃，存储的线程保持完整。

- **`panda mcp`** —— 把节点自己的工具（任务、记忆、技能……）暴露为 MCP stdio 服务，任何讲 MCP 协议的 Agent 都能经标准协议驱动这个节点。

- **`panda rpc`**（实验性）—— NDJSON-over-stdio 嵌入面（`status`、流式 `ask`、`session.*`），其他工具可驱动 OpenPanda；`scripts/panda_rpc.py` 为纯 stdlib 参考客户端。

- **订阅 OAuth**（实验性）—— `panda auth login anthropic` 通过 PKCE 登录 Claude Pro/Max 订阅，token 自动刷新。

### 接口与运行时

- **终端 TUI**（Bubble Tea）：全屏 alternate-screen 模式与键盘导航、首次运行引导向导、实时进度、中途打断与转向；`PANDA_CLASSIC_REPL=1` 可回到经典行式 REPL。

- **Web 控制台**：零配置、任务看板、实时 SSE 消息流、技能管理、会话取消、浏览器自动登录；`panda web --lan` 以临时令牌把控制台分享到局域网，并支持 Web Push 通知。

- **语音入口**（实验性）：`panda voice` 通过随附的 `extensions/voice` sidecar 脚本跑通「唤醒 → 转写 → 派任务 → 朗读」回路（需自备 Python 依赖）。

- **脚本化 CLI**：`panda ask` 单行命令嵌入自动化脚本。

- **自更新**：`panda update check|apply`（或 `panda upgrade`）先探测暂存的新二进制再原地替换。

- **极致轻量**：纯 Go 单静态二进制，常驻内存约 20MB，无外部运行时依赖。

***

## 📦 安装

**macOS / Linux：**

```bash
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

**macOS (Homebrew)：**

```bash
brew install Xustalis/openpanda/openpanda
```

**Windows (PowerShell)：**

```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

**从源码构建**（Go ≥ 1.26，无 CGO，交叉编译干净）：

```bash
git clone https://github.com/Xustalis/OpenPanda.git && cd OpenPanda
make build && ./bin/panda init
```

已安装？`panda update` 原地升级，用户数据保留。

***

## 🚀 快速开始

初始化节点（交互式配置设备名、模型提供商与本机能力卡）：

```bash
panda init
```

任选一种方式开始：

```bash
panda                                  # 交互式终端 TUI
panda web                              # 网页控制台，自动调起浏览器
panda ask "查看系统状态并总结待办任务"  # 直接发一条指令
```

连接第二台设备：在设备 A 上运行 `panda pair <设备B地址>` 发起配对并显示短码，在设备 B 上用 `panda pair confirm` 核对同一码确认 —— 或让 LAN 发现把它列到 `panda nodes` 里，用 `panda nodes admit` 按指纹准入。

***

## 🛠️ 常用命令

| 命令                   | 用途                               |
| -------------------- | -------------------------------- |
| `panda`              | 启动交互式终端 TUI                       |
| `panda ask "<指令>"`   | 单次执行：直接回答、调用工具或委派任务（`--agents a,b --mode parallel` 多 Agent 并行） |
| `panda web`          | 启动网页控制台并自动打开浏览器（`--lan` 分享到局域网） |
| `panda voice`        | 用说话代替打字（实验性）                  |
| `panda model`        | 多模型注册中心：`status`、`add`、`switch`、`test` |
| `panda nodes`        | 查看在线设备及能力；`nodes trust <id> mesh\|guarded\|open` 钉住对端信任档 |
| `panda pair`         | LAN 配对一台新设备（两端核对同一短码）              |
| `panda queue`        | 查看排队中、执行中或待审批的任务（`--watch` 实时刷新） |
| `panda approve <id>` | 审批放行二级高危操作                       |
| `panda plan run <文件.yaml>` | 多阶段、多设备流水线（`plan example` 生成起步样例） |
| `panda task board <id>` | 查看任务可读的兄弟消息板（plan/父作用域及其子任务）  |
| `panda task artifact <id>` | 列出或解出任务的输出工件（`--list`、`--file <路径>`、`--out <目录>`） |
| `panda session`      | 列出、分叉并恢复会话（`session tree` 查看家族树）       |
| `panda project list` | 管理工作空间项目与工程记忆                    |
| `panda card`         | 查看、重扫并编辑本机能力卡                   |
| `panda skill`        | 浏览、导入并安装工作流技能（技能中心/URL/文件）        |
| `panda auth login`   | 通过 OAuth 登录模型订阅（如 `anthropic`）       |
| `panda mcp` · `panda rpc` | 把节点自工具暴露为 MCP stdio 服务 · NDJSON 嵌入面 |
| `panda update`       | 检查更新或原地自更新（`upgrade` 直接应用）     |
| `panda doctor`       | 体检 PATH、配置、适配器与数据库               |
| `panda version`      | 输出版本号                            |

***

## 🏗️ 架构

```
┌─────────────────────────────────────────────────────────────┐
│ cmd/panda      终端 TUI · 网页控制台 · CLI · 语音           │
├─────────────────────────────────────────────────────────────┤
│ askengine      意图分类引擎 · 管理工具族                    │
│ entry/plan     提示词构建 · 多阶段拆解与产物接线            │
│ commander      三层执行架构：原生 Shell · Agent 适配 · 人工 │
│ defense        分级权限门控 · 熔断器 · 防漂移与防死循环     │
│ memory/skills  双层隔离记忆（用户/项目） · 自学习 SKILL.md  │
├─────────────────────────────────────────────────────────────┤
│ sessions       会话树 · 分叉 · 自动压缩                     │
│ auth/updater   订阅 OAuth（PKCE）· 自更新                  │
│ scheduler      能力评分路由 · 延迟加权最短路                │
│ trust          按对端信任分区（mesh/guarded/open）          │
│ bus/ledger     WS + 密封 UDP 传输 · 会话 AEAD ·            │
│                Ed25519 节点身份 · 能力卡                    │
│ artifact       分块可续传的产出打包                         │
│ security       沙箱 · 网络白名单 · 凭据脱敏                 │
│ storage        纯 Go 嵌入式 SQLite（WAL 模式）             │
└─────────────────────────────────────────────────────────────┘
```

***

## 🗺️ 路线图

| 版本               | 主题                                        |
| ---------------- | ----------------------------------------- |
| **v0.0.8**（稳定基线） | 单机多 Agent 协同完整可用：意图分类、任务派发、监督循环、故障转移、分级审批、多语言提示词策略 |
| **v0.0.9**（稳定版）— "Periapsis" | 混合传输/DTN 架构完成：延迟加权 mesh 路由、DTN bundle 上线、令牌预算、影子副本、执行器通路、UDP 数据报平面与经 mesh 协调的 NAT 打洞、DTN 载荷加密、接触计划、Ed25519 节点身份、lite 构建——以及可记忆的审批、执行归属标注、Web 侧栏节点徽标与日常 CLI 打磨 |
| **v0.0.10** — "Apoapsis" | 伸向局域网与边缘硬件：指纹确认准入的 LAN 自动发现与 TOFU 密钥钉住、工作树随委派文件任务同行、澄清回路、带五个参考驱动（含串口 MCU）的 actuator 派发、Pi adapter 与免 Python 的内建 generic 执行器——以及可分叉的会话树、自动压缩、`panda rpc` 嵌入面、订阅 OAuth、签名事件/审计链、OS 级沙箱与一轮稳态性能优化。许可证迁移至 AGPL-3.0-or-later 并引入商业双授权 |
| **v0.0.11** — "Rendezvous" | 收紧 mesh 本身：签名 hello 内协商的逐连接 AEAD 加密、蓝牙式 `panda pair`、签名发现 beacon 与稳定的 `k:<pub>` 身份、跨多跳路由不丢目的地身份的硬节点钉住、跨断链重投裁决的 outbox 托管、daemon 权威仲裁、240 秒静默看门狗、协商二进制数据帧——以及沿委派链逐跳中继 context 与产物的部分网状数据面。本版同时带来 computer-use 眼手对（`hardware:screen_capture`/`hardware:gui_input`，POSIX 与 Windows `.ps1` 驱动齐备，只在确有 GUI 会话时广告）、可取回的任务产物（`panda task artifact`、控制台内联预览），以及显示内容的 mesh 独占信任规则——随字节贯穿发送、拉取与阶段间输入交接 |
| **v0.0.12**（预览版）— "Constellation" | 把机队画成一片天空、让 agent 编队飞行：同项目任务经声明的路径作用域与隔离 worktree 并行，委派边界端到端收紧；运行中的任务可排队递便签、或直接扭转当前一轮，轮预算耗尽停驻的任务可以「继续」而不只是批准/拒绝。Web 控制台收拢成一个干活的地方——队列/计划/项目合并为「总控」页，工作空间页把整片机队画成实时拓扑图并直接把任务发进选中的对话，个人资料页回答这台节点是谁、最近有多忙。底层：跨进程执行租约、上下文溢出恢复、按模型 token 策略下的模型目录换代、PowerShell 补全与 daemon 上的 pprof——外加按 dispensation-attempt 约束的全生命周期栅栏：过期执行权改派、断连期 outbox 按序投递、过期裁决打不进已轮转的行 |
| **v0.1.1**（当前版本）— "Constellation" | 发布级版本线上的补丁版：入口客户端原生支持 OpenAI Responses 与 Google Gemini，OpenCode Go / Console 成为内置供应商，一条对话的名册与节点 pin 进了终端（`/team`、`/node`、`panda ask --team/--node`、待办板），派发预算按你选的团队算而不是写死的 3，跨进程写入在文件锁后成为原子操作，适配器探测不再重复、模型熔断逐级拉长，任务还能钉自己的工作目录。安全一侧，项目不再能把一次已认证请求变成整盘浏览，`sandbox.mode: strict` 在后端缺失时拒绝执行，控制台给请求体、事件流与 agent 探测都加了上限。配置、存储与协议都没变——无 schema 迁移、无新帧、无改名的键。 |
| **v0.1.0**（上一稳定版）— "Constellation" | 发布级版本线的第一个稳定版，也是第一批安装器加固过的版本（每条安装路径都有降级守卫、校验发布签名）：Constellation 的全部功能，加上发版审计的correctness与安全修复——多跳任务经中继拉取产物、模型自报失败不再记成完成、对端拒绝后回到有能力的发起节点、hello 身份钉住、项目包准入与解压上限、UDP 通道崩溃修复、strict 模式机密读拒绝，以及独立控制台的会话/技能 API。已在 Mac → 中继 → 云服务器的跨网链路上实测，包含 3 MiB 项目树的端到端拉取；首个切点之后又补上控制台语音输入（`POST /api/voice/transcribe`）、每 agent 模型 pin（`injection.agents`）与工作空间设备面板。 |
| **v0.0.x** — 内核打磨 | **打磨内核，确保每一项已交付功能真正可用。** 约十条 Experimental 轨道经真机舰队验证转正——LAN 自动发现与指纹准入、配对与会话 AEAD、信任分区、任务间消息板、会话树/RPC/OAuth、工作树随行委派、澄清回路、actuator 驱动族、computer-use、语音入口——并完成参考驱动首次真机点火；全程持续稳定性、性能与边缘场景调优 |
| **v0.1.x** — 助理特化 | **特化为真正替你做事的助理。** 内核之上的桌面薄壳（托盘常驻、全局热键、原生通知、自更新）；织密网一层——sub-main scout→plan 运行中动态拆解、对称 NAT 的 TURN 式中继兜底、消息板欠账（多机 e2e、MCP 拉板、板视图）；以及最小诚实版「在场」——挂在 dream 周期上的未解决清单碰撞机，有值得说的事才开口。可商用质量，边界如实声明 |
| **v0.2.x** — 自进化 | **OpenPanda 自我迭代。** 自修改以 agent 产出的 patch 集落地，经 build → gate → 人工合并管线，与签名发布通道共存；节点自签 self-build 成为信任分区下的第三类 provenance；`self_evolution.mode`（off → propose → auto），最近一份签名 release 永远留作兜底——完整的 Agent 自进化范式 |

***

## 🔭 愿景

OpenPanda 的架构为大规模异构集群而设计：无人机集群控制、深空卫星星座通信调度、大规模自动驾驶车队协同。这些场景的本质问题是一致的——**每个节点的算力不同、功能不同、执行的任务不同，需要被统一协调、调度与监督。**

当前版本面向个人开发者的设备与 Agent。长期目标是将同一套编排内核延伸至集群规模的自治节点。

***

## 📚 文档

完整文档集在 [`docs/`](docs/README.md) 下：[项目现状](docs/status.md)（哪些已验证、哪些仅构建通过）、[安装指南](docs/install.md)、[FAQ](docs/faq.md)、[总线协议](docs/protocol.md) 参考，以及设计稿与历史报告。内部文档大多为中文。

***

## 🤝 参与贡献

1. 查阅 [CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md) 了解代码规范与开发流。
2. 查阅 [SECURITY.md](SECURITY.md) 了解安全边界。
3. 遵循 [CODE\_OF\_CONDUCT.zh-CN.md](CODE_OF_CONDUCT.zh-CN.md) 社区行为守则。
4. 提交 PR 前在本地运行 `make gate` 确保全部校验通过。
5. 非琐碎贡献需签署 CLA——见 [CLA.zh-CN.md](CLA.zh-CN.md)；typo/文档类修改与 `adapters/` 下的改动豁免。

***

## 📄 许可证

OpenPanda 采用双许可证模式：

- **社区版** —— [GNU Affero 通用公共许可证 3.0 或更高版本](LICENSE)（AGPL-3.0-or-later）
- **商业版** —— 面向闭源集成或托管服务的专有授权，详见 [COMMERCIAL.zh-CN.md](COMMERCIAL.zh-CN.md)

许可证变更前发布的历史版本仍按 MIT 许可证提供。

使用 OpenPanda 须遵守[使用条款与免责声明](TERMS.zh-CN.md)（[English](TERMS.md)）。
