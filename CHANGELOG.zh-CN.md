# 更新日志

[English](CHANGELOG.md) · [简体中文](CHANGELOG.zh-CN.md) · [日本語](CHANGELOG.ja.md) · [Español](CHANGELOG.es.md) · [Deutsch](CHANGELOG.de.md)

## 项目概述

OpenPanda（**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant）是一个个人任务编排内核：每台设备运行一个 `panda` 二进制，节点经带认证的 WebSocket 总线互相发现，入口模型把每个请求变成直接回答或可执行的任务规格，调度器再把任务路由到最适合运行的设备与智能体。CLI 是内核的主接口——裸 `panda` 直接进入交互式 REPL——Web 控制台则是跑在同一套存储与引擎上的薄壳。

## 版本规则

- 版本号遵循 `MAJOR.MINOR.PATCH`。项目处于初始开发期（`0.0.x`）：一个补丁版本可以包含新功能、缺陷修复，以及例外的破坏性变更——后者一律列入**破坏性变更**分类。
- 发布通过打 `vX.Y.Z` 标签完成；上一个标签之后的全部提交归入新版本的段落，`[Unreleased]` 收集最近一次发布之后的内容。
- 每个版本按四个分类记录：**新增功能**、**问题修复**、**优化改进**、**破坏性变更**（升级时需要采取行动的改动）。
- 每条记录以一至三行写明变更内容与用户可见的影响；必要时标注引入该变更的提交，便于追溯。
- 英文版（CHANGELOG.md）为权威版本，zh-CN / ja / es / de 翻译与其镜像，发布前后可能短暂滞后。

## [0.0.5] - 2026-08-25

三设备实验室补丁：首个真实 macOS + 香橙派 + Windows 集群（公开安装器安装、局域网组网、端到端跑任务）暴露出——队列任务走不出发起节点、tier-2 授权在委派边界丢失、被锁死的 agent CLI 会吸引路由并挂起数分钟。共 5 个提交，全部在该硬件上验证。

### 新增

- **`panda task add --requires`** — 声明任务所需能力（`--requires gpio:read`，逗号分隔）；本节点无此能力的队列任务会被路由到拥有该能力的设备，与 `panda ask` 一直采用的根调度策略一致（c4e1bc7）。

### 问题修复

- **队列任务支持跨设备路由** — `panda task add` 与 Web 控制台创建的任务此前只由发起节点认领执行：任务所需能力只在其他设备上时直接失败（在 Mac 上提交 `pi.uptime` 报 `route: no capability matches`）。调度器认领时现在会咨询根调度器：本节点无匹配即把认领改派给有能力的 peer（含已拒绝节点环路保护、lease 检测执行节点死亡），peer 的结果回填发起节点的任务行。实验室三个方向实测：Mac→OrangePi、OrangePi→Mac、Windows→OrangePi 全部完成（c4e1bc7）。
- **Tier-2 授权随委派传递** — `--authorize` 的授权此前只在提交节点本地生效：委派到 peer 的 agent 任务到了执行节点被防御层拒绝，即使用户已批准。授权现在随认证总线传递、执行节点照常放行——无凭证的香橙派向 Mac 的 claude 提交已授权 coding 任务，现在能跑完而不是死在 review（c4e1bc7）。
- **被锁死的 agent CLI 不再吸引路由** — 能力卡是静态的，但已安装的 CLI 可能不可用：Windows 上的 `claude.exe` 无登录态、节点又没配模型 key，却向集群宣告 `agent:*`；路由把 coding 任务派过去，挂起数分钟后才以网络错误失败。本地回退链与 hello 宣告的能力摘要现在都按可用性把关——CLI 在 PATH *且* 模型可达（自带凭证或可注入）；该 Windows 节点的摘要现在只宣告 `win.sysinfo`（2db530f）。
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
- **Anthropic 工具 API 兼容**——tool_use 块现在总是携带 `input`（无参工具为空对象），此前严格的 Anthropic 兼容服务商（DeepSeek /anthropic）会以 400 拒绝后续轮次；带点工具名改为下划线以满足 `^[a-zA-Z0-9_-]+$`（93a453a）。
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
