# [](https://)更新日志

[English](CHANGELOG.md) · [简体中文](CHANGELOG.zh-CN.md) · [日本語](CHANGELOG.ja.md) · [Español](CHANGELOG.es.md) · [Deutsch](CHANGELOG.de.md)

## 一键安装

**macOS / Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

**Windows（PowerShell）**

```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

**macOS（Homebrew）**

```sh
brew install Xustalis/openpanda/openpanda
```

安装后运行 `panda init` 初始化，或直接输入 `panda` 进入 REPL。已在运行的旧版本用上面的同条命令即可原地升级（覆盖安装，用户数据保留）。

## 项目概述

OpenPanda（**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant）是一个个人任务编排内核：每台设备运行一个 `panda` 二进制，节点经带认证的 WebSocket 总线互相发现，入口模型把每个请求变成直接回答或可执行的任务规格，调度器再把任务路由到最适合运行的设备与智能体。CLI 是内核的主接口——裸 `panda` 直接进入交互式 REPL——Web 控制台则是跑在同一套存储与引擎上的薄壳。

## 版本规则

- 版本号遵循 `MAJOR.MINOR.PATCH`。项目处于初始开发期（`0.0.x`）：一个补丁版本可以包含新功能、缺陷修复，以及例外的破坏性变更——后者一律列入**破坏性变更**分类。
- 发布通过打 `vX.Y.Z` 标签完成；上一个标签之后的全部提交归入新版本的段落，`[Unreleased]` 收集最近一次发布之后的内容。
- 每个版本按四个分类记录：**新增功能**、**问题修复**、**优化改进**、**破坏性变更**（升级时需要采取行动的改动）。
- 每条记录以一至三行写明变更内容与用户可见的影响；必要时标注引入该变更的提交，便于追溯。
- 英文版（CHANGELOG.md）为权威版本，zh-CN / ja / es / de 翻译与其镜像，发布前后可能短暂滞后。

## [0.0.8-preview] - 2026-09-05

向正式开源项目转变的里程碑版本：OpenPanda 从实验性任务路由内核全面蜕变为正式、生产可用的分布式个人智能体编排操作系统。本版本带来完整的集群管理工具族（Ask 引擎实时查探集群与能力）、交互式 TUI 增强与运行中转向（mid-turn steering）、Agent 主动故障转移与凭证兜底模型注入、多模型注册中心、执行全透明化跟踪，以及项目上下文感知委派。

### 新增

- **内核管理工具族（Management Tools）** —— Ask 引擎现已接入一级内省工具（`system_status`、`card_list`、`card_show` 及队列查询）。用户只需自然语言询问“现在集群什么状态”、“香橙派上有哪些能力”，内核即可通过 live 数据精准回答。
- **TUI 运行中转向与即时中断（Mid-turn Steering & Real Stop）** —— 支持在任务执行过程中随时取消或纠偏转向，支持鼠标交互、实时任务动态可视化渲染及高响应度的 Bubble Tea 状态展示。
- **主动故障转移与凭据兜底模型注入** —— 当终端 Agent CLI 遇到额度耗尽或凭证失效（401/403）时，OpenPanda 自动注入配置的备选模型接管执行，无需人工介入中断。
- **执行过程与模型调度全透明展示** —— 彻底打开黑盒：Agent 运行时执行的各项操作（如 `Bash: curl ...`、文件读写、工具调用等）现在通过 `EvProgress` 实时同步推送到 TUI 运行卡片与足迹跟踪中；任务完成后清晰标注执行 Agent 与底层模型归属。
- **多模型注册中心（`/model`）** —— 模型配置不再是单一字段。支持通过 `models:` 列表管理多个模型，并在运行期通过 `/model <alias>` 无缝热切换；内置支持 DeepSeek、Claude (Anthropic)、ChatGPT (OpenAI)、Kimi (月之暗面)、火山引擎 (Ark/豆包)、智谱、通义千问、硅基流动、OpenRouter 及本地 Ollama。
- **斜杠菜单的参数候选** —— 带枚举参数的命令（`/lang`、`/resume`、`/config set` 等）在命令名后输入空格即弹出与命令列表相同的方向键菜单：↑↓ 移动、Tab 补全、Enter 选中并执行、Esc 关闭。
- **审批卡方向键选择** —— 二级审批提示现在可用 ↑↓/←→ 加 Enter 回答（焦点默认落在"拒绝"，与原有默认一致），原 y/n/Esc 热键保持不变。

### 修复

- **`/lang` 现在真正切换界面语言** —— 所选语言会即时同步到 TUI 菜单、状态行与帮助文本，并持久化至 `config.yaml`。
- **凭据兜底注入后的元数据丢失** —— 修复了自动注入配置模型兜底运行后，执行结果未能正确向上传递 `Injected` 标记及模型名称的问题。

## [0.0.8-alpha] - 2026-09-03

项目（project）版本：项目不再只是任务上的一个名字——它有工作目录、描述和持久化的"当前项目"指针，从项目里发起的任务自动继承它，被委派的任务把整个项目带到执行节点，执行机器因此知道自己在做的是什么。审批门重新定界为"仅不可逆操作"（审查后把下载落盘这一向量重新纳入门控），控制台补上了跟踪 Plan 与修改设置的界面，并换上了「Panda Paper」新皮肤。以 alpha 切出：本线的功能范围已完整，但浸泡时间还比不上正式号版本。

### 新增

- **项目成为一等公民** — projects 表（工作目录、描述、时间戳）加 settings 表支撑的当前项目指针，可在一次性进程中存活（`panda ask` 不是常驻进程）；完整 CLI 命令族 `panda project list | new --dir --desc | show | rename | remove | enter | exit`，`list` 标记当前项目（a9fa471, 1260cb9）。
- **任务继承所在项目** — 引擎携带一个环境项目（ambient project），补全分类器没有指明的部分；项目中的任务知道自己的工作目录和描述——此前它知道的反而不如不属于任何项目的任务多（8c82c5e）。
- **被委派的任务携带项目** — 项目记忆打包进委派载荷（有大小上限），工作树作为分块 artifact 引用传输（复用 plan 平面的既有机制），执行端在自己的根目录下重新派生工作目录，拒绝带路径字符的项目名（总线上的名字属不可信输入）。完成后的产出覆盖式收回本机项目目录——两台机器改一个项目是真冲突，绝不静默合并（a1f1d19）。
- **控制台：项目与设置** — 项目 CRUD、进入/退出和元数据进控制台 API（7cda886）；项目行携带工作目录、当前项目状态与改变它们的操作（146c1ba）；审批、路由、记忆上限与注入设置——审批门是用户盯着队列看了一下午之后最想改的设置，现在不必离开控制台去改。四项全部并入控制台已有的 settings API，而不是另开一个（7a60a80, 0736c2f）。
- **Plan 看板端点** — `GET /api/plans`（有哪些 plan、进行到哪）与 `GET /api/plans/{id}`（单个 plan 的各阶段及阶段间的 artifact 接线——回答"训练阶段真的拿到脚本了吗"的视图）。发起 plan 仍走 `/api/ask`，那里本来就工作正常（932442a）。
- **多模型支持** — 智能 base_url 归一化（尾斜杠、缺失版本前缀、各家供应商的怪癖）、思考模式模型的 reasoning 字段、OpenAI 供应商预设（08ede13）。

### 问题修复

- **队列任务重新可以跨设备路由（CLI 与看板）** — `panda task add` 和看板的 POST 曾把每个队列任务钉在节点级工作目录上，而 0.0.6 起 forwardScheduled 把"被钉住的任务"定义为纯本地工作，于是无该能力的节点上 `--requires pi.uptime` 直接报 `route: no capability matches`，而不是到达持有该能力的 peer——0.0.5 修好的正是这个问题，一个版本之后又被悄悄撤销。这个钉定本身是冗余的（执行侧本就回落到同一个节点级默认目录），现已移除；只有带自有目录的任务（面板会话的 worktree）才留在本地（本次发布）。
- **SSE 指纹缓存传播加载失败** — 堆在失败的存储扫描后面的调用者现在拿到同一个 error，而不是空值加 nil error；存储持续报错时不再向每条连接的流误发一次变更事件、只剩加载者自己的流断开（2026-09-02 审查 P2）。
- **TUI：一条斜杠命令只留一个输入框** — exec 路径现在在排队命令的同一个事件循环回合里清空帧，状态行和圆角框不再留在回滚区里形成第二个输入条（6a77bf7）。
- **TUI 阶段计时、任务卡格式、DeepSeek thinking 回传** — 判定耗时不再记到执行阶段头上，任务卡渲染统一，思考模式对话回传不再 400（6d6e2e4）。
- **CLI 表格按显示宽度对齐** — `%-Ns` 按字节填充，CJK 标题（每字两列）或着色状态格会把后面所有列挤出对齐线；表格现在按显示宽度填充，task id 无歧义时打印短形式，同批附带一轮 TUI 布局修复（8740c04）。
- **Darwin 构建自动签名** — 构建时自动 ad-hoc 签名，macOS 不再在首次运行时 SIGKILL 新构建的二进制（3b86987）。
- **文档：OpenAI 兼容示例弃用已退役的 DeepSeek 对话模型别名**（cbe52cb）。

### 变更

- **审批门只拦不可逆操作** — Tier 2 现在意味着"后续命令无法恢复"：删除、磁盘/分区/固件状态、电源状态、提权，以及丢工作的参数形式（`git push --force`、`rsync --delete`、`sed -i`、`find -delete`）。curl、wget、make、ssh、systemctl、mount、docker、kubectl、terraform、各包管理器、chmod/chown/mv/cp/tee 之类不再需要审批即可运行，`bash scripts/build.sh` 也不再弹审批——一个跑不了自己 build 的节点做不了它存在的意义（e593470）。
- **下载落盘的抓取仍需审批** — 把字节保存到路径的 curl/wget（`-o`、`-O`、`--output`）是 Tier 2：字节对分类器不可见，下一步通常是执行它，而此前 `curl -o x …; bash x` 从头到尾都判 Tier 1。抓到 stdout 或 `/dev/null`——可达性探测的写法——不受影响（2026-09-02 审查 P1）。
- **控制台：「Panda Paper」视觉重构** — 在控制台既有样式之上整层追加的重构层：暖纸浅色主题与暖墨深色主题（拒绝纯黑与冷灰）、竹绿品牌色加深一档收沉稳、蓝色仅留给决策链路（Orbit）、衬线展示字体只用于标题层、圆角整体放大一档、更浅更软的暖棕阴影，并移除"AI 感"装饰（标题渐变文字、渐变下划线、按钮高光覆层）。该层通过同名 token 重定义 + 级联覆盖生效——组件结构、类名与逻辑零改动（本次发布）。

## [0.0.7] - 2026-08-31

可用性版本：能力卡——告诉调度器本节点能做什么的文件——现在可以从所有界面（CLI、REPL、TUI、Web 控制台）编辑，无需重启 daemon；添加第二台设备变成了产品流程而非配置文件难题；每个任务结果现在都会获得一份人类可读的汇报，用户看到的是发生了什么，而不是一墙原始 stdout。

### 新增

- **全界面结构化卡片编辑** — `panda card native add|remove`、`panda card agent add|remove|set`、`panda card manual add|remove`（结构化子命令，不再只能开编辑器）；REPL 和 TUI 里用 `/card` 做同样操作；Web 控制台完整卡片编辑器（`/api/card` 加 agent/native/manual 端点）。所有写路径走同一校验器 + `.bak` + 原子写管线，坏编辑不会污染卡片（1b8e2b7）。
- **设备配对** — `panda pair` 生成共享密钥、打印新设备的接入指引、写入双方配置；`panda nodes add <addr>` 追加 peer 并现场拨号、无需重启；Web 控制台"邀请设备"CTA 现在接到节点页的真实配对流程（763bff6, 5748cec）。
- **卡片热重载** — 编辑卡片（从任何界面）触发 `ReloadCard`：调度器重读、重注册能力、向所有已连接 peer 广播带新卡片的心跳，变更无需重启即可传播（3d6feeb）。
- **Bubble Tea TUI** — `panda` 现在进入 Bubble Tea 前端，带可用的 tier-2 审批路径（内联审批卡、`y` 批准、`n` 留待 `/approve`）；经典 REPL 仍可通过 `PANDA_CLASSIC_REPL=1` 使用（06cca6a）。
- **LLM 任务汇报** — 每个内联任务（成功或失败）完成后，引擎调用入口模型生成人类可读的结果汇报；汇报在 REPL、TUI、Web 控制台的原始输出之前渲染，用户看到"做了什么 + 关键输出"（成功）或"为什么失败 + 下一步怎么办"（失败），而不是原始 stdout/stderr。模型失败时优雅降级——跳过汇报、显示原始输出（本版本）。
- **Web：思维流式与任务进度** — 模型推理以可折叠思维块流式进入聊天（03a4301）；任务消息显示进度和结果，而非仅显示载荷（4ba931f）。
- **远端 tier-2 恢复** — 被委派到远端节点的 tier-2 任务被批准后，重跑发生在执行者（工作所属的机器），而非批准者的机器（3d6feeb）。
- **常驻协程 recover 守护** — 新增 `internal/guard` 包裹长驻协程：panic 时记录完整堆栈并触发受控关停，而不是留下半死进程继续跑；单条总线连接的读循环 panic 只关闭该连接。
- **Windows 优雅关停** — CTRL_CLOSE/LOGOFF/SHUTDOWN 控制台事件现在触发与 unix SIGTERM 相同的有序关停路径（`SetConsoleCtrlHandler`，短清理窗口）。
- **Windows 控制台颜色** — TERM 未设置时（Windows 控制台），TUI 调色板在 TTY 输出上启用颜色；`dumb` 与 `NO_COLOR` 仍优先。
- **`make build-darwin-amd64`** — Intel Mac 构建目标，与其他按平台划分的目标并列。
- **Agent 能力面与每任务工具策略** — Agent 注册表现在声明每个 CLI 的原生能力（skills、MCP、子代理），不再逐适配器硬编码；入口模型可为单个任务请求工具策略（`minimal` / `extended`）覆盖全局路由策略，高复杂度任务可以只为该任务解锁 Agent 的完整能力面。Claude Code 的子代理派生（Task 工具）以类型化 `subagent` 进度事件呈现，不限流地进入任务时间线（本版本）。
- **按任务类型的 Agent 超时** — `timeouts.agent_by_kind` 按任务类型覆盖 Agent 墙钟预算（训练任务可以比快速修复跑得久）；未列出的类型沿用 `timeouts.agent_s`，任务租约始终保持在实际生效的预算之上（bcbe1d2, e573c2e, 9fc2d04）。
- **心跳携带熔断状态** — Agent CLI 持续失败的节点会在心跳中声明熔断状态，peer 在撞上坏 Agent 之前就不再向其路由任务（bcbe1d2）。
- **Agent 会话续接与用量记账** — 监督轮次续接 Agent 自己的会话而不再每轮冷启动（适配器线协议新增 `session_id` + `resume`），适配器上报结构化 token 用量明细并记为 `agent_usage` 事件（e8dc68b, 183bf6f, 1722144）。
- **委派深度上限** — 同意信息随线路携带跳数上限：任务最多只能被再委派有限的跳数，委派链不再可能无限增长（ca5770e）。
- **取消可靠送达** — `task_cancel` 现在与结果走同一条 outbox：执行者断连时发出的取消会被持久化，重连后重投，不再丢失（dc4412a）。

### 问题修复

- **P0 安全发现关闭** — plan_id/stage_id 路径穿越（通过阶段工作目录中的 `../../../../` 任意目录读取+外泄）现被 ID 校验 + 根前缀断言阻断；peer 断连后的结果送达被持久化到 outbox、重连时重投（审查 P0-2）；TUI 中断/退出语义修复，Ctrl+C 现在真正退出（763bff6, 5129461）。
- **P1 安全加固** — 默认监听地址从 `0.0.0.0:7836` 改为 `127.0.0.1:7836`（daemon 不再默认绑定所有接口）；`context_fetch` 现要求 peer 在任务的委派链中；supervisor 不可达时将任务停泊到 review，而非静默接受未验证结果（763bff6）。
- **入口模型：不再重放翻倍的用户轮次** — 严格提供商（Anthropic 兼容）在会话重放中翻倍或悬挂用户轮次时返回 400；规范化步骤现在合并同角色的连续纯文本轮次（8174e78）。
- **编排时序与 Web 消息竞态** — judge 运行时不再计入执行阶段（单独的 `judge_start` 追踪标记）；监督循环在轮次结果之前追踪执行，因此 continue→continue 路径不再隐藏重执行；Web 乐观轮次状态提取到 `chatstate.ts`，出错时移除乐观气泡，助手回复不再落入用户消息内（97d5c62）。
- **取消与执行者接受的竞态** — 到达执行者接受窗口的取消被丢弃；取消现被排队，接受完成后应用（a19b33b）。
- **Windows 门禁与双向拨号握手确定性** — 跨平台 CI 门禁现在在 Windows 上通过；双向拨号 tie-break 无论到达顺序如何都确定（526c731）。
- **CI：并行门禁任务** — 门禁工作流现在将 build/vet/test/typecheck 作为并行任务运行，race 检测器限定在需要的包，并门禁 Web 控制台 typecheck（3f302f1）。
- **迁移互斥** — schema 迁移在 `BEGIN IMMEDIATE` 下执行，并在事务内复查 `user_version`，两个进程打开同一数据库时每个版本只应用一次；比数据库 schema 更旧的二进制现在会明确报错，而不是静默继续。
- **Web：单一事件总线** — 控制台现在只保持一条引用计数的 SSE 连接，用 `Authorization` 头认证（URL 不再携带 token），指数退避自动重连，change 与 trace 事件向所有订阅者扇出。
- **Web：会话流竞态** — 流式写入仅在会话处于活动状态时生效；流式中切换会话不再把气泡混入其他会话，切换时中止过期的转录加载。
- **Web：健壮性与可访问性** — 顶层错误边界带重试；命令面板与确认对话框焦点圈闭；看板卡片可键盘操作（Enter/Space，焦点可见）；标签页隐藏时暂停系统轮询并跳过未完成的轮询；稳定的列表 key。
- **`panda skill --help` / `panda reminder --help`** — 打印用法并以 0 退出，不再把 `--help` 当作未知动词。
- **CI：门禁与安装器流水线修复** — 修复全部四条失败的门禁腿与安装器流水线（7c418b0）。
- **CLI：折叠的思维块不再展示无法展开它的密钥**（e772598）。
- **孤儿转发与陈旧目录行** — 死掉的 peer 留下的悬空转发会被救回并完成，不再有任何 peer 背书的目录行会被清扫，不再永久滞留（32e4489）。
- **push 超时级联取消下游** — 超时的 push 会取消其下游工作而不是任其继续跑，重试预算可跨重启存活（f7efb70）。

### 变更

- **默认监听地址** — daemon 现在默认绑定 `127.0.0.1:7836` 而非 `0.0.0.0:7836`。依赖旧默认的现有部署必须在 `config.yaml` 中显式设置 `network.listen_addr` 或通过 `OPENPANDA_LISTEN_ADDR` 设置。
- **平台化系统配置目录** — 系统级备用配置目录在 unix 上仍是 `/etc/openpanda`，在 Windows 上为 `%ProgramData%\OpenPanda`。
- **存储初始化收敛为一条路径** — daemon 与 Web 面板经同一函数打开存储（`cmd/panda/store.go`）；面板不再遗漏产物池目录。
- **Web 面板：事件扫描与连接数解耦** — 任务/节点/提醒指纹缓存一个轮询周期，订阅者增长时扫描负载基本恒定。
- **逐适配器调优** — 其余 Agent 适配器各自获得了针对其 CLI 的调用处理，不再是一条通用路径（24df1c1）。

## [0.0.6] - 2026-08-27

跨设备算力调度版本成形：需要不同机器完成不同步骤的请求，如今是一等公民的 plan——各阶段在硬件所在的机器上运行；CLI 与 Web 控制台两个界面也补上了各自缺失的展示层——ask 收敛期间的实时反馈、浏览器里真正的 Markdown 渲染，以及日常重度使用所需的输入编辑器。

### 新增

- **Plan plane——阶段运行在不同机器上的流水线** — 一个阶段就是一个普通任务（CAS 状态机、lease、重试、监督、审批停泊），因此流水线继承任务已有的一切；完成阶段的工作目录被打包分块、经总线搬运到运行下一阶段的机器。两条入口：`panda plan example > train.yaml`、`panda plan run train.yaml [--dry-run]`、`panda plan show <id>`——或者一句话经 `panda ask`，模型仅在请求必须换机器时才输出 plan。任何阶段不携带 tier-2 同意：不可逆阶段停在 review 等人批（c10b8af）。
- **按声明的硬件路由** — `resource_profile` 是硬过滤条件（`ledger.Fits`），打分器按空闲容量 + 队列深度 + 等级排序、按心跳新鲜度折减——两个同时发布的任务会落在两台机器上；入口提示词携带每个节点的真实硬件，模型填路由过滤时看得见而非盲填（c10b8af）。
- **`panda voice`** — 唤醒词 → ASR → 同一条入口管线 → TTS：无键盘设备的桌宠入口（c10b8af）。
- **`panda card show | rescan | edit | set`** — 一个命令族管理能力卡：打印（及来源文件）、重扫硬件与已装 agent CLI（`rescan` 打印差异，`--write` 应用并保留 `.bak`，手写决策不被覆盖）、用 `$EDITOR` 编辑、或无头地设置字段。`panda detect`、卡的 rescan 与面板共用同一检测层（`internal/hwinfo`）（fdb56b8）。
- **CLI 的展示层** — `internal/cliui`：一次性解析的统一调色板，以及实时状态行（spinner、动词、耗时、token 数——后两者早已记录、只是从未展示），管道输出时退化为静态行。行编辑器学会括号粘贴与多行输入（粘贴多行提示词只触发一次 ask，历史也按单条回放）、Ctrl-R 增量历史搜索、以及没人愿意重打的 id 的参数位补全。未知命令给出 did-you-mean；`/help` 按意图分组内联打印；新命令覆盖第一次 ask 跑通后自然会需要的东西（`/cost`、`/model`、`/status`、`/doctor`、`/export`、`/clear`），外加 `@file` 附件与 `!cmd` 直通，让人不必离开提示符（c538ab6）。
- **Web 聊天界面补课** — 手写 Markdown 渲染器（零 `innerHTML`，因此无需 sanitizer 依赖；29 个 node 测试）替换回复里的字面 `**bold**` 与 ```围栏；流式期间输入框主按钮变为停止按钮（SSE 读取器接受 AbortSignal）；读者上滚后自动滚动不再拉扯视图；Cmd+K 命令面板与侧边栏共用同一导航词汇；移动端会话抽屉替代`display:none`（c538ab6）。
- **状态页** — `docs/status.md` 记录哪些能用、哪些只是构建了、哪些缺失，含旗舰流水线的验证状态（76c5b69）。
- **过期节点行可移除** — `panda nodes remove <id>` 与离线节点卡上的移除按钮，删除已无活跃 peer 支撑的目录行（改过名的机器、换了身份的 peer、退役节点）。本机自己的行与在线节点会被拒绝——它们会自行重新注册，"移除"只会是一次穿着成功消息的无操作。
- **Release 说明工具链** — release 工作流把版本对应的 CHANGELOG 段落加各平台安装命令发布为 release 正文，段落缺失时构建失败；0.0.5 release 页按该标准重写为纯英文正文加语言切换器；每个 CHANGELOG 顶部加一键安装（4e12779, c25a3cb, 98e10df, 600ffb3）。

### 问题修复

- **队列任务与计划阶段真正走路由了** — 队列路径保留着一份"我能做就我做"的本地短路——路由器本体早已移除它，而这条路径恰是所有面板任务与计划阶段的必经之路：硬件过滤从未在旗舰流水线实际运行的地方运行过（GPU 阶段只要派声明了该能力就留在派上；一批任务全部堆在接收节点）。决定权归还调度器；空 ability 列表意为"无约束"而非"没人匹配"；计划阶段各持资源键，独立阶段扇出并行而非排队串行（a5b792e）。
- **跑完的结果塞得进一帧** — `task_result` 输出被钳制到总线帧大小，完成任务的结果能到达提交者，而不是溢出帧后消失（c1310da）。
- **记忆围栏无法从内部关闭** — `<memory_data>` 围栏包裹正文时不动正文自身的标签：含字面闭合标签的条目会提前结束围栏、剩余部分被当作指令读取——而记忆可由模型自己的工具、面板、梦境晋升写入。内部标签被中和，文本保持可见以便审计（3f18994）。
- **节点不再描述别的机器的硬件** — 每一处都是"硬编码值站在本该探针的位置"：默认节点名是 "macbook"，没跑过 `panda init` 的节点都以作者的笔记本名自报家门；macOS/Windows 没有机器 ID 来源，改个机器名就成了另一个节点；Windows 沙箱剥掉 PATHEXT/SYSTEMROOT/TEMP——这正是 Windows 计算节点根本起不了 adapter 的原因；`python3` 不是可移植的解释器名（现在探测，Windows 先试 `py -3`）；超时任务在 Windows 上永久挂起，因为 harness 杀不掉进程树（现在 `taskkill /T`）；宣告了命令不存在的原生能力的能力卡会赢得路由然后死于 127（加载时修剪）；读不出大小的 GPU 写 0 会被排除在它存在意义所在的工作之外（现在"未知"是第三种状态）；`deploy-pi.sh` 默认用某位开发者的局域网地址（现在必填）（fdb56b8）。
- **i18n 回归收口** — 语音路径、ask/repl 计划输出、会话摘要、卸载报错与 `panda help` 里一处提示的硬编码中文迁入 `internal/i18n` 五语言——ja/es/de 用户此前在这些界面看到的是中文（c538ab6）。
- **REPL 一秒内开打，不再等 peer 拨号超时** — 交互式启动此前在横幅之前**串行**拨号每个配置的 peer、再等它们落定：离线 peer 会把拨号器整整 10 秒的超时烧成横幅前的死寂。REPL、`panda session` 与 `panda voice` 现在改为后台拨号（长会话里离线 peer 属常态，其失败也不再在输入中途打印 WARN 行），一次性的 `panda ask` 改为并发拨号——不可达的 peer 不再拖住可达的。

## [0.0.5] - 2026-08-25

三设备实验室补丁：首个真实 macOS + 香橙派 + Windows 集群（公开安装器安装、局域网组网、端到端跑任务）暴露出——队列任务走不出发起节点、tier-2 授权在委派边界丢失、被锁死的 agent CLI 会吸引路由并挂起数分钟。共 5 个提交，全部在该硬件上验证。

### 新增

- **`panda task add --requires`** — 声明任务所需能力（`--requires gpio:read`，逗号分隔）；本节点无此能力的队列任务会被路由到拥有该能力的设备，与 `panda ask` 一直采用的根调度策略一致（c4e1bc7）。

### 问题修复

- **队列任务支持跨设备路由** — `panda task add` 与 Web 控制台创建的任务此前只由发起节点认领执行：任务所需能力只在其他设备上时直接失败（在 Mac 上提交 `pi.uptime` 报 `route: no capability matches`）。调度器认领时现在会咨询根调度器：本节点无匹配即把认领改派给有能力的 peer（含已拒绝节点环路保护、lease 检测执行节点死亡），peer 的结果回填发起节点的任务行。实验室三个方向实测：Mac→OrangePi、OrangePi→Mac、Windows→OrangePi 全部完成（c4e1bc7）。
- **Tier-2 授权随委派传递** — `--authorize` 的授权此前只在提交节点本地生效：委派到 peer 的 agent 任务到了执行节点被防御层拒绝，即使用户已批准。授权现在随认证总线传递、执行节点照常放行——无凭证的香橙派向 Mac 的 claude 提交已授权 coding 任务，现在能跑完而不是死在 review（c4e1bc7）。
- **被锁死的 agent CLI 不再吸引路由** — 能力卡是静态的，但已安装的 CLI 可能不可用：Windows 上的 `claude.exe` 无登录态、节点又没配模型 key，却向集群宣告 `agent:*`；路由把 coding 任务派过去，挂起数分钟后才以网络错误失败。本地回退链与 hello 宣告的能力摘要现在都按可用性把关——CLI 在 PATH _且_ 模型可达（自带凭证或可注入）；该 Windows 节点的摘要现在只宣告 `win.sysinfo`（2db530f）。
- **`panda web` 端口被占不再报错退出** — 第二次 `/web`（或残留进程）此前报 `bind: address already in use`，还要用户手动复制 token。控制台现在自动换到相邻端口并明确提示；浏览器直接带凭证打开（token 不再打印），`/web` 已在运行时会重新打开已登录的浏览器。`--no-browser` 仍打印带 token 的 URL 供手动使用（c4e1bc7）。
- **Peer hello 上报真实版本号** — 三条 hello 路径此前广播硬编码的 `0.1.0-dev`，混合版本集群里 `panda nodes` 显示的版本全是错的；现在统一上报 `version.Version`（实验室三台设备均显示 0.0.5）（2db530f）。
- **配置文件旁的能力卡优先于 `./capabilities.yaml`** — 在恰好含有 capabilities.yaml 的目录（仓库检出、其他节点的卡）启动 daemon 会静默加载错误的卡；现在优先加载 init 写在配置文件旁边的卡，`--card` 显式指定仍然最高（2db530f）。
- **Windows 数据目录不再与安装目录冲突** — 默认数据目录 `%LOCALAPPDATA%\openpanda` 与安装前缀 `%LOCALAPPDATA%\OpenPanda` 在大小写不敏感的 NTFS 上是同一目录：数据库、记忆与项目全部落在安装前缀内部，卸载时会一并清扫。数据目录现改为 `%LOCALAPPDATA%\openpanda-data`；从 0.0.4 升级的 Windows 节点以全新存储启动（fc50721）。
- **安装器可在 GitHub API 限流与 WinPS 5.1 HTTP 栈损坏的机器上工作** — `api.github.com` 对未认证请求限额为每 IP 每小时 60 次，耗尽时两个安装器都改走 `/releases/latest` 的 302 重定向解析最新版本；`install.ps1` 开头强制 TLS 1.2，优先使用系统自带 `curl.exe`（Windows 10 1803+）并以 `Invoke-WebRequest` 兜底，补充超时让损坏的 WinINET 代理快速失败而不是挂起。两者均在真实三设备安装中命中过（109b567）。
- **Homebrew tap 推送认证修复** — release 工作流的 tap 更新步骤此前因 job token 权限不足报 `could not read Username`；推送 URL 现在内嵌 token（6868a63）。

## [0.0.4] - 2026-08-25

分布式节点正式版（GA）。引擎区分物理节点与虚拟机节点、守护同节点身份的单例、为适配器协议补齐加固与契约测试、暴露带 Nodes 页的 `/api/self` + `/api/nodes` 接口。beta 之后跟进落地：入口模型决策缓存、分层 system prompt、零配置 Web 引导、共享适配器 harness、tier-2 授权体验、安装/卸载清扫、更新器 changelog 摘要、一问式 `panda init` 与场景化 FAQ。Homebrew 安装后从**任意工作目录**都能干净启动，首个带完整上手文档的公开发布。

### 新增功能

- **节点类型 + 稳定身份**——`node.kind = physical | vm`。物理节点用主机指纹（主机名 + MAC 哈希）推导稳定 ID；虚拟机节点要求显式 `node.identity`，以便在重建的云实例上保持同一身份。`panda init` 现在会询问类型和（若为 VM）稳定身份。Peer hello 协议 v2 携带 `node_kind` + `node_identity`；`employee_cache` v10 迁移为两列补 `DEFAULT 'physical'`。
- **单例守护锁（`nodeidentity` 包）**——`Acquire(kind, identity)` 在 `$USER_DATA_DIR/locks/` 下取 OS 级文件锁：Unix 用 `flock(2)`，Windows 用 `LockFileEx`。对同一身份再跑一次 `panda daemon` 会打印诊断后干净退出，避免破坏共享存储。
- **适配器协议加固 + 契约测试**——`internal/commander/adapter.go` 返回统一 `{ok, result, exit_code}` 帧，非零退出时把 stderr 作为诊断保留；`inject` 对每次凭据注入决策（auto | always | never）写入日志便于操作者审计。`tests/adapter_contract_test.py` 验证每个适配器都讲同一套帧；`testdata/scenarios/long_task.py` 用于压测队列取消路径。`adapters/codex.py` 的参数解析与 stdout framing 同步修复。
- **`/api/self` + `/api/nodes` + 网页 Nodes 面板**——`panel/self.go` 暴露本地节点（name / kind / identity / resource class / running state / capabilities）与节点目录（本地 + 已连接 peer，含最后一次可见时间/运行态）。新增 Nodes 页（`webui/app/src/views/nodes.tsx`）以 running/last-seen 表格渲染 kind + 资源等级 chip。
- **分布式实验室工具箱**——`scripts/lab/generate-three-node.sh` 生成三个互相隔离的配置（物理 A/B + 一个 VM 节点），带独立身份、共享密钥与已预装 peer 列表；`scripts/scenario-model/main.go` 读取 YAML 目录给出调度/路由预测评分；`scripts/task-timeline/main.go` 直接从 `openpanda.db` 输出每个节点的任务迁移 ASCII 时间线，适合恢复审计。`docs/testing/distributed-lab-plan.md` 记录 GA 前必须通过的三节点场景用例（发布门禁）。
- **入口模型决策缓存**——意图分类与监督判定命中磁盘缓存（`entry_cache`，迁移 v11，键为 prompt + 设备快照），相同输入直接跳过 LLM 调用。system prompt 分层（pi 风格）：精简常驻核心承载路由决策，记忆治理与详尽任务 JSON 层按需附加，稳定前缀可被服务商缓存。指挥官模型自身的 token 消耗计入委派指标（executor `entry:<model>`）。
- **零配置 Web 引导**——`panda web` 无任何配置也能启动，queue/projects/nodes 立即可用；横幅提供一步式模型设置（API 类型 / base URL / 模型 / key，带实时连通性测试），保存即热加载引擎——无需重启就能开始第一次对话。
- **更新器 changelog 摘要 + daemon 通知**——Web 控制台的更新卡片展示最新 release 的 changelog 摘要（下载/应用按钮旁的折叠笔记），无头 daemon 每 6 小时自动检查并在日志里提示新版本（每版本只提示一次，因为它无法自我应用更新）。
- **场景化 FAQ**——`docs/faq.md` 按场景回答高频问题（入门步骤、模型报错解读、agent 适配器、tier-2 授权、review、scope drift、多设备组网、数据位置、升级）；`docs/README.md` 将用户指南与内部计划分开索引。

### 问题修复

- **Homebrew / 任意 cwd 启动失败（SQLITE_CANTOPEN 错误 14）**——默认存储路径原来是 `./data/openpanda.db`，在非项目目录下（Homebrew 安装的常态）打开 DB 失败。多层修复：
  1. `config.Default()` 现在把 DB/memory/projects/skills/work 全部锚定到 `UserDataDir()`（按平台的用户态目录：macOS `~/Library/Application Support/openpanda`；Linux `${XDG_DATA_HOME:-$HOME/.local/share}/openpanda`；Windows `%LOCALAPPDATA%\openpanda`）。
  2. `config.Load()` 运行 `resolveRelativePaths()`，把 YAML 里遗留的相对路径按「YAML 自己所在目录」重定位，保证 pre-v0.0.4 的 `panda init` 写出来的旧配置读的还是 YAML 旁边的 data 目录，不是 shell cwd。
  3. `storage.Open()` 无论手工指定什么怪路径都会 `MkdirAll` 数据库的父目录。
  4. `panelStore()`（REPL、`panda web`、面板命令、queue/task 等入口共用）现在像 `runDaemon` 一样一次性创建完整存储目录。
     冒烟验证：用全新 HOME 从 `/` 下 `panda queue` → 自动创建用户数据目录并初始化 DB，输出队列为空。
- **Anthropic 路径空 key 误诊**——非流式调用在未配置 key 时照样发出空 key 请求，把服务商的 401 报成「key 无效」而不是「未配置」。现在所有调用路径返回可操作的 `panda init` / Web 设置页提示；REPL 横幅内联标注未配 key 的模型；面板把配置缺口报为 503（与「引擎未配置」同类）而非服务器错误 500。
- **tier-2 授权体验**——tier-2 拒绝现在附带可操作提示（`--authorize` 或能力卡 `tier: 1` 声明），并跳过重试预算直接进入 review：重试不可能产生授权。注册表驱动的凭证探测覆盖 Claude Code 新版 `~/.claude/config.json` + `settings.json` 位置。scope 解析只提取路径 token，自然语言描述（如「工作目录下的 haiku.txt」）不再对合法文件操作误报 drift。
- **安装器 / 卸载器**——`install.sh` 支持断点续传（`curl -C -`），PATH 持久化与 `panda install` 写同一标记块，生成的 LaunchAgent/systemd 服务依赖 daemon 的配置自动发现而非硬编码 `--config`/`--card` 路径。`panda uninstall` 清扫发行前缀（bin/、adapters/、示例配置），拒绝动 Homebrew Cellar（keg 保持完整，提示 `brew uninstall openpanda`）与源码 checkout。
- **任意 cwd 适配器解析**——从临时工作目录委派任务曾失败（"can't open file …/adapters/claude_code.py"）；解析器现在从 cwd 向上逐级定位 `adapters/` 并回退到绝对适配器路径。
- **ask engine 设备可见性**——基于全新数据库构建的 ask engine 用 daemon 的稳定运行时 ID 自注册节点，入口模型不再在首次使用时看到空设备列表。

### 优化改进

- `panda nodes` 输出新增 `Kind` 列（physical | vm），分布式部署一眼就能区分宿主机器节点与置备的 VM 身份。
- **共享适配器 harness**——七个 agent 适配器共用一个 harness 库（`adapters/_harness.py`）处理 `{ok, result, exit_code}` 帧、参数解析与超时，削减每个适配器的样板代码。
- **一问式 init**——`panda init` 只问一个问题（现在配置模型吗？），硬件探测自动填名称、资源等级、类型与 VM 身份（宿主探测为 guest 时）；`--defaults` / `--non-interactive` 连这一问都跳过。示例能力卡重写为真实 `ledger.Card` schema 并说明 agent tier 语义。
- **默认模型切换为 `deepseek-v4-flash`**——`deepseek-chat`/`reasoner` 别名已于 2026-07-24 被服务商退役；pro 模型绝不作为默认（成本控制）。能力卡发现同时查找已解析配置文件旁边，与 daemon 的发现顺序一致。
- README：新增「身份单例规则」小节，节点配置表补上 kind/identity 参考行，并首次在命令总览里带出 `panda nodes`。

## [0.0.3] - 2026-08-23

### 新增功能

- **多智能体适配器注册表**——`internal/agents` 是 PANDA 委派的所有 agent CLI 的唯一事实来源（适配器脚本、探测二进制、安装命令、文档 URL）。`panda detect`、`panda agents`、Web 设置 API 与 commander 的可用性探测都从此读取，新增一个 agent 只需改一处。
- **四个新 agent 适配器**——Grok Build、DeepSeek Harness（`dsh`）、OpenClaw 与 Hermes 加入 Codex、Claude Code、OpenCode 行列：各自是一个小巧的无头 Python 桥接，运行 CLI 并返回 `{ok, result, exit_code}`。
- **`panda agents`**——`list`（默认）尽力探测 PATH 上每个 agent 的版本；`test <name>` 跑连通性检查；`install|update <name>` 打印安装命令 + 文档链接。若什么都没装，输出列出每个缺失 agent 的安装命令与下载 URL。
- **Web 设置 agent 名册**——设置页的 agent 列表现在为每个缺失 agent 展示安装命令与下载链接（`/api/agents` 返回 `install_hint` + `install_url`）。
- **上级完成度判定（`superior task review`）**——agent 运行后，入口模型对照任务成功标准判定结果（`entry.Supervise`，输出 `done`/`continue`）。`continue` 判定会把后续指令（还剩什么 + 下一步）重新委派给 agent 链，循环直到评审通过或达到轮次预算上限（默认 5）。
- **按风险分流的终态**——可逆任务完成进入 **done**（已完成）；已接受但不可逆的（Tier-2）任务——推送、删除、不可逆状态变更——停在 **review**（待审批）等待人工签字；评审反复拒绝的任务以 `needs_followup` 标记停在 **review**。评审判定事件在 Web 任务详情中回放。
- **一键安装器**——`scripts/install.sh`（POSIX）与 `scripts/install.ps1`（PowerShell）下载对应平台的发布包，校验 SHA-256，把二进制连同 agent 适配器解压到用户前缀，并将 `panda` 链接到 `PATH`，可选注册开机自启服务（登录时运行 `panda daemon`）。macOS 另提供 Homebrew tap（`brew tap Xustalis/openpanda && brew install openpanda`）。
- **发布打包**——`scripts/package.sh`（及 `make package`）交叉编译所有支持平台，产出 `dist/panda-<version>-<os>-<arch>.tar.gz` / `.zip` 与 `checksums.txt`，可直接用于 GitHub Releases。
- **自更新**——`panda web` 与 REPL `/web` 运行期间在后台检查是否有新版 CLI；Web 控制台下载并校验更新，待任务队列空闲后一键应用（原子替换二进制、刷新适配器、重启）。放弃已下载的更新不产生残留；Windows 上替换产生的 `.old` 副产物在下次启动时清理。

### 问题修复

- **多行 `--version` 横幅**（如 Hermes）不再污染单行 agent 表——版本输出在 CLI 与 Web 设置 API 中都截断为首行。

## [0.0.2] - 2026-08-22

CLI 优先的版本：内核重设计（stage A–C）落地——每项 Web 能力都有了 CLI 对应物，REPL 成为产品正门，CLI 获得对话记忆、实时任务汇报与按输出端渲染的 Markdown。

### 新增功能

- **CLI 命令族**——每项 Web 能力都有 CLI 对应物：`panda session | task | memory | config | agents | project`，全部与面板共享服务层；`panda ask` 新增 `--output-format json|stream-json` 供无头调用（a4cba5f）。
- **资源感知本地任务队列**——`core.Submit` 异步化：拖拽序 → 优先级 → FIFO 排序，资源锁注册表加 `MaxConcurrent` 把关，互不冲突的任务可越过阻塞的队列先行；任务新增 `priority`/`seq`/`session_id`/`resource_keys` 字段（SQLite v9）（0e8d850）。
- **REPL 对话记忆**——24k 字符预算、成对淘汰（用户发言永不脱离其回答被重放），持久化到 `~/.local/state/openpanda/conversation.json`；支持 `/new`、`/history`、`!!` 与 `panda ask --continue`（f0a1b9f）。
- **带外任务汇报**——REPL 观察器在任何任务到达终态时（看板提交、Web 控制台、跨节点委派）打印 ✓/✗ 一行，不打断输入行；行内 ask 绝不重复通知（f0a1b9f）。
- **实时任务板**——`panda queue --watch` 与 `/tasks watch` 每 2 秒重绘队列、状态着色；Ctrl-C 退出视图而非进程（f0a1b9f）。
- **`internal/mdtext`**——按输出端渲染 Markdown：彩色 TTY 得 ANSI 强调，管道与裸控制台得纯文本，TTS 前总是剥离；流式增量按行走同一套规则（e94f72f）。
- **智能体实时进度**——适配器在 stderr 输出 NDJSON 进度注记，节流后记为 `EvProgress` 事件：`panda task <id>` 与面板时间线可看到 agent 正在做什么（93a453a）。
- **注入策略**——`injection.model: auto|always|never`：默认 agent 原生凭据优先；每次凭据注入都在任务输出中声明并记入审计日志（852b27e）。
- **成本感知路由**——agent 选择按能力 × cost_tier 评分并带 `preferred_agents` 加成，回退到次优匹配（852b27e）。
- **记忆改造**——可配置上限（`memory.limits`）、清单式选择性注入的多文件 topics、低权重 dream 沉降（852b27e）。
- **`internal/hwinfo`**——共享硬件探测包，支撑 `panda detect` 与新的 `GET /api/self` 设备档案端点（852b27e、1a97fd7）。
- **面板应用设置与记忆 topics**——`GET/PUT /api/settings/app` 带校验的策略存储；记忆 API 支持按文件 topics；控制台记忆页产品化、设置分组、五语言 i18n 同步（1a97fd7）。
- **`panda init`**——交互式首次引导，写入 `config.yaml` + `capabilities.yaml`；`config.ResolvePath` 统一解析顺序（flag > 环境变量 > 用户配置 > 系统默认），daemon/web/边车/doctor 共享（f5610fc）。
- **控制台打磨**——共享 `PageHeader`/`ErrorState` 组件、全局 toast、破坏性操作确认对话框（45ee941）。
- **REPL 人体工学**——提示符下方的斜杠命令菜单、纯 ASCII figlet 横幅、TERM=linux 英文/ASCII 降级、`--card` 自动发现（`./capabilities.yaml` → `/etc/openpanda/capabilities.yaml`）（f0a1b9f）。
- **`scripts/deploy-pi.sh`**——一条命令部署香橙派：交叉编译、原子替换二进制、systemd 安装、健康检查（d7bc87f）。

### 问题修复

- **适配器全时段超时**——CLI 中途卡死（管道开着、无输出）会让读取循环永久阻塞，超时只覆盖 stdout EOF 之后；现在 CLI 跑在独立进程组，看门狗线程到期限击杀整棵进程树（332f2d4）。
- **Anthropic 工具 API 兼容**——tool*use 块现在总是携带 `input`（无参工具为空对象），此前严格的 Anthropic 兼容服务商（DeepSeek /anthropic）会以 400 拒绝后续轮次；带点工具名改为下划线以满足 `^[a-zA-Z0-9*-]+$`（93a453a）。
- **codex 在非交互父进程下无法初始化**（首轮前写状态库与 PATH 别名即 EPERM）——改以 `-s danger-full-access` 运行，由 PANDA 外层沙箱约束（332f2d4）。
- **agent 失败原因为空**——适配器诊断信息现在镜像进 Stderr，`store.Fail` 与任务结果携带真实错误（93a453a）。
- **互拨重连风暴**——去重输家的最后一声 hello 走了注册表连接而非到达连接，导致其对端身份从未绑定、每秒重拨（实测 15 分钟 869 次，现仅 1 次）（93a453a）。
- **缺失 work_path** 曾表现为误导性的 fork/exec ENOENT（错怪命令二进制）——daemon 启动时创建全部存储根目录（f0a1b9f）。
- **尾部 flag 被静默吞入位置参数**（`panda task <id> --config x` 丢失配置）——所有子命令现在把 flag 提到位置参数之前（f0a1b9f）。
- **补全循环**——`/e` 吸附到 `/exit `、退格又重新触发（f0a1b9f）。
- **SQLite v9 迁移在旧库上崩溃**——旧库无 `tasks` 表；现在缺表时先建表（0e8d850）。
- **API 错误以指引呈现而非传输噪音**——401/403 指向 `model.api_key`，404 指向 `base_url`/模型名，持续 429/5xx 明示限流，连接失败建议查网络（df47725）。
- **门禁与加固**——`make measure` 引用不存在的配置；gofmt 漂移；README Go 版本写错；`.gitignore` 漏掉 `.openpanda/`；示例配置幻影 peer 热循环告警；面板补 `securityHeaders` 中间件（cacde7b）。

### 优化改进

- **回答纪律**——入口提示词要求结论先行（不展示推理、结构极简）；agent 提示词带输出附加条款：最终消息汇报做了什么，执行细节留在 `panda task <id>` 事件（93a453a）。
- **流式韧性**——`streamWithRetry` 在用户未见任何增量时对瞬时中断（429/5xx/网络）退避重放；`deltaGuard` 阻止任务 JSON 流进气泡并让 JSON 中途掉线可重试；耗尽的工具循环以最后一次无工具调用收敛；工具执行钉住分类时的注册表快照，杜绝 MCP 热切换期间的「unknown tool」（df47725）。
- **分组侧边栏导航**——可折叠分组（任务 / 设备与智能体 / 个人 / 系统），入口提示词重塑为「指挥家」人设（f5610fc）。
- **面板端点测试**——十七个测试补齐审计高风险缺口：会话 CRUD 加真实 git 端到端（HTTP 切刻 worktree、diff、merge）、模型密钥脱敏（原始密钥不出门）、MCP 拉起失败 400、skills 生命周期、提醒 CRUD、系统端点（ad884bf）。
- **交互命令静默加载配置**——交互面不再输出 slog 噪音；daemon 保留完整日志（f0a1b9f）。

### 破坏性变更

- **裸 `panda` 现在打开交互式 REPL** 而非无头 daemon；内核移到显式 `panda daemon` 子命令。systemd 单元、LaunchAgent、Windows 启动器与 Makefile 运行目标均已更新——直接调用 `panda` 的部署必须改为 `panda daemon`（f0a1b9f）。

## [0.0.1] - 2026-08-19

首次开源预发布：完整内核特性集（daemon、CLI、P2P 委派、审计链、迁移、调度器、SSE 面板、内嵌 Web 控制台、交互式 REPL、跨平台安装生命周期），外加助手层（agent 感官、定时提醒、MCP、worktree 聊天会话、看板任务队列）。全程门禁全绿：build / vet / 全量测试 / `-race` / 交叉编译。

### 新增功能

- **内核地基**——带租约与崩溃恢复的任务状态机、带认证的 WebSocket 节点总线、能力目录、本地执行管线与 OpenCode 适配器（Sprint 0–1：1be8f85..307e13a）。
- **P2P 委派**——跨节点任务路由、上下文分层传输、分级权限模型（Tier 1 自动 / Tier 2 审批）、GPIO 访问、DCPS 调度评分（3040e18、6324a87）。
- **防御链**——scope 漂移检测、重试循环检测、带破坏性命令表的命令分类（590cacc、c647c96）。
- **Hermes 记忆与技能**——每日笔记、带沉降的 dreaming、项目记忆、可加载技能；`panda skill` 从 CLI 管理技能审批，控制台配有 skills 视图（9a41b3e、c36cad1）。
- **语音边车**——唤醒词、STT、TTS、VAD（硬件门控），支持 `OPENPANDA_WAKE_KEYWORD` / `OPENPANDA_WAKE_MODEL` 覆盖（84faf08）。
- **真机部署**——Mac / Windows / 香橙派三节点部署验证、scope 路由、无头内核形态（0aa9f73、7f1f8bd）。
- **审计与迁移**——任务事件与全局日志的 `prev_hash` 审计链、PRAGMA `user_version` SQLite 迁移、慢速 DoS 防护（hello 超时 + 连接数限制）、MCP 客户端硬超时（7582754）。
- **调度器机制**——DCPS 加权评分（`0.4·resource_efficiency + 0.3·user_priority + 0.2·scheduler_tier + 0.1·wait_time`）按 TMB 心跳新鲜度折扣（30 分钟半衰期）；容量驱动 accept/decline；拒绝后自动改派并排除历史拒绝者（f454909、7385a89）。
- **单发 CLI 面板命令**——`panda status`、`panda queue` 与 `panda task | cancel | approve | reject | logs` 无需进入 REPL 即可检视节点并管理任务（307e13a）。
- **交互式 REPL**——斜杠命令覆盖全部面板表面（`/ask`、`/tasks`、`/approve`、`/nodes`、`/web`……）、五语言 i18n、ask 引擎可选（无模型端点也能用面板命令）（6119493）。
- **内嵌 Web 控制台**——Vite + Preact + TypeScript 重建并经 `go:embed` 折入二进制：队列/详情/ask/项目/节点视图、实时 SSE、五种界面语言（61cc519、c9768c1）。
- **面板写路径 + SSE**——经共享 `askengine` 的 `POST /api/ask`、项目、节点、取消、日志与 `/api/events` 变更流（b4fb9f5）。
- **`panda web`**——零配置回环控制台，临时 token 自动登录 URL（47517e3）。
- **`panda install` / `uninstall` / `doctor`**——跨平台生命周期：持久 PATH 注册、独立自检、白名单安全卸载（confirm + zip 备份）（86b9b9d）。
- **看板任务队列**——创建表单、优先级循环、按列拖拽重排、行内审批（da9c9e1）。
- **git worktree 聊天会话**——流式回复、实时思考链（task_events 回放 + SSE 重取）、恰好一次的摘要折回（c36cad1）。
- **MCP 集成与热重载**——一个 stdio 服务器，切换前实际拉起验证；工具即刻加入 agent 工具集，无需重启（c36cad1）。
- **定时提醒**——agent 经 `reminder.set` 工具自行排程；Web Push（回环视为安全上下文）加 SSE 倒计时；`panda reminder` CLI（c36cad1）。
- **agent 感官**——`time.now` 与 `weather.get` 系统工具：模型自身没有时钟也没有窗户（c36cad1）。
- **codex 适配器与 agent 可见性**——已安装 CLI 探测加连通测试；`panda detect` 硬件扫描生成 capabilities.yaml 草稿（c36cad1）。
- **`panda metrics [--csv]` 与 `panda audit verify [--task <id>]`**——委派指标导出与审计链校验（7582754）。
- **`scripts/smoke-delegate`**——跨进程委派验证器：退出码 0 表示仅 peer 具备能力的任务在 peer 上到达 done。
- **开源文档**——五语言 README、含合并门禁（`make gate`）的 CONTRIBUTING、公开的桌面与打包路线图（51031eb）。

### 问题修复

- **互拨连接抖动**——两节点互拨产生无休止的 ~1 秒连接/断开循环；`ensurePeer` 确定性决胜（字典序较小节点 id 胜出）后恰存一条 TCP（879b42d）。
- **线协议授权缺口**——result/decline/accept/context-ack 校验发送者为当前执行者；CAS 状态守卫闭合 TOCTOU；`waiting_context` 总是带租约；本地执行失败终态化不留僵尸（9622538）。
- **命令分类绕过**——`env -S` 值递归分类、`php -r` 扫描、`find -exec` / `tar --checkpoint-action` / `git push/commit` fail-closed 到 Tier 2（f5db449）。
- **进程组管理**——取消时整树击杀（Unix `Setpgid`、Windows `taskkill /T`）加 630 秒适配器硬超时，无孤儿孙进程（f5db449）。
- **记忆注入通道**——Hermes/Projects/skills 原子写、外部输入污染 `[ext]` 且绝不被 dreaming 提升、记忆围入 `<memory_data>` 并冠「数据非指令」前言（a742585）。
- **取消传播**——`task_cancel` 沿委派链逐跳级联到执行节点（574632a）。
- **事务化写入**——任务状态 UPDATE 与审计事件 INSERT 同一事务提交（c5d34d4）。
- **综合清扫（D1–D32）**——委派孤儿终态化、转发副本带租约、hello HMAC 绑定 5 分钟窗口、NetworkGuard 钉到已配置端点、子进程输出限界等（1694b7d）。
- **新克隆控制台白屏**——git 恢复的哈希 `index.html` 指向被忽略资源；提交的占位符现已稳定，`make web` 以真实构建落地为守卫（ab87f90）。
- **未知子命令启动常驻 daemon**（`panda statsu`）——现退出码 2 并打印用法（a742585）。
- **语音唤醒默认值**——各后端真实的内置关键词（`hey_jarvis` / `porcupine`），替代不存在的 `hey_panda`（4ea73bf）。
- **预发布审计修复**——`panda help` 存在；提示词与示例中的「PANDA」品牌残留清除；`config.example.yaml` 记载 `mcp:` 与 `model.api_type`；路线图死链修复（2f001c0）。

### 优化改进

- **硬记忆墙**——个人记忆绝不注入工作区（worktree 钉定）对话，「用户偏好暗色主题」不会漏进代码任务；项目记忆仅经执行节点 ContextPack 到达执行侧（da9c9e1）。
- **agent 适配器并入分级模型**——未声明 agent 默认 Tier 2，在适配器子进程拉起前被拒（a4d2d9e）。
- **Anthropic 之外的 OpenAI 线格式**——入口模型同时说 `/v1/messages` 兼容与 OpenAI 兼容端点，两条路径都支持流式（c36cad1）。
- **密钥文件加固**——含 `api_key` / `shared_secret` / `panel_token` 的配置自动 chmod 0600 并给出环境变量指引（6275fd4）。
- **面板默认回环**（`127.0.0.1:7840`）；非回环绑定就明文 HTTP 告警（a742585）。
- **peer 重连替换陈旧连接**——同一身份的新连接换入注册表；移除按连接身份匹配（7911bbe）。
- **解释器 `-c` 分类白名单制**——只有可证明纯输出的代码留在 Tier 1（f5db449）。
- **部署基线成文**——明文 `ws://` 仅限回环/Tailscale/可信局域网；公网用 TLS 反代 + `wss://`（7582754）。

### 破坏性变更

- **项目更名为 OpenPanda**——module 路径 `github.com/Xustalis/OpenPanda`、环境变量加 `OPENPANDA_` 前缀、单元 `openpanda.service` / `com.openpanda.node.plist`、默认数据库 `openpanda.db`；CLI 二进制保留短名 `panda`（ac71bb1、6f2083e）。

## 暂缓事项

有意押后、记录以保持可见：

- 控制台键盘快捷键（新建会话、快速任务、视图切换）。
- 助手的浏览器伴侣界面。
- 控制台内一等公民的 git 视图（分支状态、历史、远端）。
- 从控制台管理 worktree（列出/清理/检视）。
- 用户可调的助手性格与呈现。
- 网页搜索缓存，减少重复抓取与延迟。
- 按任务的推理力度分层（低/中/高）。
