# 更新日志

[English](CHANGELOG.md) · [简体中文](CHANGELOG.zh-CN.md) · [日本語](CHANGELOG.ja.md) · [Español](CHANGELOG.es.md) · [Deutsch](CHANGELOG.de.md)

## [Unreleased]

## [0.0.2] - 2026-08-22

CLI 优先的版本：内核重设计落地——每一项 Web 能力都有了对应的 CLI 命令——REPL 成为产品的正门，CLI 获得了对话记忆、实时任务汇报和按输出端渲染的 Markdown。

### CLI 正门与 REPL 改造

- **CLI 正门**——裸 `panda` 现在打开交互式 REPL（此前是无头 daemon）；内核移到显式的 `panda daemon` 子命令。systemd/LaunchAgent/Windows 启动器与 Makefile 运行目标均已改为显式启动 daemon。
- **REPL 多轮上下文**——裸模式提问累积一段对话，以 24k 字符为预算（整轮问答成对地最早优先淘汰，用户发言永远不会脱离它的回答被重放），并持久化到 `~/.local/state/openpanda/conversation.json`：新开的终端从上次结束的地方继续。`/new` 清空、`/history` 查看、`!!` 重复上一条提问，`panda ask --continue` 一次性接续同一线程。
- **带外任务汇报**——交互式 REPL 运行一个任务观察器，轮询存储层的任务状态指纹，任何任务到达终态（看板排队任务、Web 控制台提交、跨节点委派）时打印一行 ✓/✗——插入行编辑器而不打断输入中的缓冲。行内 ask 自行吸收结果，绝不重复通知。
- **实时任务板**——`panda queue --watch`（及 REPL 的 `/tasks watch`）每 2 秒原地重绘队列，状态着色；Ctrl-C 退出视图而非进程。
- **斜杠命令菜单**——REPL 中输入 `/` 前缀即在提示符下方实时列出匹配命令（上限 10 条，附 (+N, Tab) 提示）；补全本身仍由 Tab 触发。修复了 `/e` 自动吸附到 `/exit `、退格又重新触发的补全循环。
- **启动横幅重设计**——经典 figlet 字形以纯 ASCII 拼出 OpenPanda（任何终端都能渲染），附节点/模型/工作目录信息行；仅 TTY 着色。
- **TTY/控制台降级**——裸 Linux 控制台（TERM=linux）下界面回退到英文和 ASCII 分隔符，不再出现菱形乱码；非 UTF-8 终端将 `·` 分隔符换成 `|`。
- **能力卡自动发现**——`--card` 默认依次查找 `./capabilities.yaml` 和 `/etc/openpanda/capabilities.yaml`，已安装节点零参数即可执行任务。
- **flag 重排**——flag 可以写在位置参数之后（`panda task <id> --config x`）：Go flag 包会把尾部 flag 静默吞进位置参数文本；所有子命令现在都会把它们提前。

### 内核重设计——CLI 即内核，Web 为薄壳

- **注入策略（stage A）**——`injection.model: auto|always|never`：默认 agent 原生模型凭据优先；每次凭据注入都在任务输出中声明并记入审计日志。
- **成本感知路由（stage A）**——agent 选择按能力 × cost_tier 评分，带 `preferred_agents` 加成，并回退到次优的匹配 agent。
- **记忆改造（stage A）**——可配置上限（`memory.limits`）、带清单式选择性注入的多文件 topics、低权重的 dream 沉降。
- **硬件探测（stage A）**——共享的 `internal/hwinfo` 包支撑 `panda detect` 与 `/api/self`。
- **CLI 命令族（stage B）**——每项 Web 能力都有 CLI 对应物：`panda session`（worktree 隔离的聊天会话）、`task`（看板/时间线/添加）、`memory`（多文件编辑）、`config`、`agents`（探测+连通测试）、`project`——全部与面板共享服务层。平台终端助手（term_darwin/linux/unix/other）提供 raw 模式 UX。
- **面板：self、应用设置、记忆 topics（stage C）**——`GET /api/self`（设备档案）、`GET/PUT /api/settings/app`（经校验的应用策略存储）、支持按文件读写的记忆 API；控制台记忆页为 topics 产品化、设置分组、五种语言 i18n 同步。
- **面板端点测试覆盖**——十七个测试补齐审计标记的高风险缺口：会话 CRUD 加真实 git 端到端（HTTP 完成 worktree 切刻、diff、merge 落回主检出）、模型设置密钥脱敏（原始密钥绝不外泄）、MCP 拉起失败返回 400、skills 生命周期、提醒 CRUD 和系统端点。

### 输出卫生与适配器

- **Markdown 输出卫生**——新的 `internal/mdtext` 按输出端渲染回答：彩色 TTY 得到 ANSI 强调（青色标题、加粗、暗色代码、对齐表格），管道和裸控制台得到纯文本，语音管线（`Speak`）在 TTS 前总是剥离 Markdown。流式增量按行通过同一套规则渲染，任何表面都不会漏出原始 `**`/`|`/`#` 标记。
- **回答纪律**——入口提示词现在要求结论先行（不展示推理过程、结构极简），agent 提示词附带输出附加条款：最终消息汇报做了什么，而不是探索过程。执行细节留在 `panda task <id>` 事件里——CLI 版的「折叠显示过程」。
- **codex 适配器**——以 `-s danger-full-access` 运行：codex 自带的沙箱在非交互父进程下根本无法初始化（状态库与 PATH 别名创建在第一轮之前就以 EPERM 失败），而 PANDA 已将适配器限制在任务 cwd 内。agent 失败现在也会呈现诊断信息，而不是空的 `result {"failed":""}`。

### 新增

- **资源感知的本地任务队列**——`core.Submit` 的同步模型变为异步队列：`internal/scheduler/queue` 按拖拽序 → 优先级 → FIFO 排序任务，并用资源锁注册表加 `MaxConcurrent` 把关启动，互不冲突资源的任务得以越过阻塞的队列先行。任务新增 `priority`/`seq`/`session_id`/`resource_keys`（SQLite v9），看板任务可跳转进关联会话（0e8d850）。
- **`panda init`——交互式首次引导**——通过一次交互式提问把 `config.yaml` + `capabilities.yaml` 写到用户可写目录：硬件扫描默认值、枚举输入校验（打错重问）、五语言提示。`config.ResolvePath` 提供统一的解析顺序（flag > 环境变量 > 用户配置 > 系统默认），daemon、`panda web`、webui 边车和 doctor 共享，init 写出的配置无需额外参数即可被各处拾取（f5610fc）。
- **控制台 P1 打磨——统一页面、可编辑记忆、toast、确认对话框**——共享的 `PageHeader`/`ErrorState` 组件统一各视图结构与错误处理；记忆页成为产品级页面（条目按 `§` 切分、新条目高亮、字符计数、经新的 `PUT /api/memory/{file}` 原地编辑）；全局 toast 反馈（错误手动关闭、成功/信息自动关闭）取代散落各视图的错误文本；破坏性操作——删除会话、拒绝 skill、取消任务、删除提醒——现在都需要确认对话框（45ee941）。

### 变更

- **分组侧边栏导航**——控制台导航折叠为可展开分组（任务 / 设备与智能体 / 个人 / 系统），当前分组自动展开——渐进式披露取代八个平铺入口；入口提示词重塑为「指挥家」人设：简单提问直接回答，复杂工作派发给设备与智能体（f5610fc）。
- **ask 管线的流式韧性与收敛**——`streamWithRetry` 在用户尚未看到任何增量时对瞬时中断（429/5xx/网络）做退避重试；`deltaGuard` 阻止结构化原始输出（任务 JSON、```json 围栏）流进聊天气泡和终端，被抑制的增量不计为已送达，JSON 中途掉线仍可安全重试；耗尽 maxRounds 的工具循环现在以最后一次无工具调用收敛而非报错；工具执行使用分类时的同一注册表快照，杜绝 MCP 热切换期间的「unknown tool」错误；CLI/REPL 回答在 TTY 上实时流式（工具进度为单行提示），管道输出保持干净（df47725）。

### 修复

- **适配器全时段超时**——codex/claude 适配器先读 agent CLI 的 stdout 到 EOF 再等进程，CLI 中途卡死（管道开着、无输出）会让读循环永久阻塞——请求超时只覆盖 stdout EOF 之后的那一小段。两个适配器现在把 CLI 放进独立进程组，看门狗线程在期限时击杀整棵进程树：子进程继承并持有管道，只杀直接子进程会让读取端继续阻塞。
- **Anthropic 工具 API 兼容**——tool_use 块现在总是携带 `input`（无参工具为空对象）：此前 map 的 omitempty 会把它丢掉，严格的 Anthropic 兼容服务商（DeepSeek /anthropic）会在后续轮次以 400 拒绝。带点工具名（reminder.set、time.now、weather.get）改为下划线以满足 `^[a-zA-Z0-9_-]+$` 模式。
- **work_path 自动创建**——daemon 启动时 mkdir 全部存储根（context/memory/projects/skills/work）；缺失的工作目录此前会表现为误导性的 fork/exec ENOENT，让人错怪命令二进制。
- **互拨重连风暴**——去重输家的最后一声 hello 回复走了注册表连接而非到达连接，输家因此从未绑定对端身份、跳过 MaintainPeer 的边等待、每秒重拨一次；真实硬件上曾观察到 15 分钟 869 次重连，现在只有 1 次然后归于安静。
- **门禁与小加固**——Makefile `measure` 目标引用了不存在的配置（现与 README 片段一致）；六个文件重排格式以满足 gofmt 门禁；README 徽章/前置要求在五个版本中统一为 Go ≥1.26；`.gitignore` 增加 `.openpanda/`；示例配置的幻影 peer 注释掉以免 `make run` 热循环告警；面板增加 `securityHeaders` 中间件作纵深防御（cacde7b）。
- **旧库上的 SQLite v9 迁移**——队列表结构迁移在 `tasks` 表尚不存在的数据库上崩溃；现在缺表时会先建表（0e8d850）。
- **可操作的 API 错误映射**——非 OK 状态码沿流式路径保留，错误以指引而非原始传输噪音到达用户：401/403 指向 `model.api_key`，404 指向 `base_url`/模型名，400 指向请求本身，持续 429/5xx 明示限流或服务不可用，连接失败建议检查网络（df47725）。

## [0.0.1] - 2026-08-19

首次开源预发布。

**项目更名为 OpenPanda**（Open + Personal Adaptive Node-based Distributed Assistant）。Go module 路径改为 `github.com/Xustalis/OpenPanda`；所有环境变量使用 `OPENPANDA_` 前缀；systemd/LaunchAgent 单元为 `openpanda.service` / `com.openpanda.node.plist`；默认数据库文件名为 `openpanda.db`。CLI 二进制保留短名 `panda`。

全程门禁全绿：build / vet / 全量测试 / `-race` / 交叉编译。覆盖完整内核特性集（daemon、CLI、P2P 委派、审计链、迁移、调度器评分+去重、SSE 面板、内嵌 Web 控制台、交互式 REPL、跨平台 install/uninstall/doctor），外加助手层：agent 感官、定时提醒、MCP 集成、worktree 聊天会话和看板任务队列。

### v0.0.1 预发布审计修复

- **Web 控制台嵌入重构**——vite 构建产物现在落在 `dist/app/`，提交在库里的 `dist/index.html` 占位符永不被构建触碰。此前 `make web` + `git add -A` 循环可能提交一个指向被忽略 `/assets/*` 的哈希 index.html，让每个新克隆的控制台白屏。占位符现已稳定，`make web` 以 `dist/app/index.html` 存在为守卫，静态处理器优先真实构建、占位符兜底。
- **`panda help`**——子命令现已存在（含 `-h`/`--help`），打印有导向的总览而非报错；未知子命令打印同样的用法。
- **品牌残留**——入口模型的系统提示词曾以「PANDA」自我介绍；现为「OpenPanda」（每次回复都对用户可见）。`config.example.yaml` 头部亦然。
- **`config.example.yaml`**——补记此前未记载的 `mcp:` 段与 `model.api_type`（anthropic | openai）；push 段注释更新到内嵌控制台时代。
- **死链**——路线图曾引用仅本地的委派报告；现指向可复现该验证的 `scripts/smoke-delegate`。

### 新增

- **`panda install` / `panda uninstall` / `panda doctor`——跨平台全局命令生命周期**——`panda install` 把二进制复制到 `~/.local/bin`（unix）/ `%LOCALAPPDATA%\OpenPanda\bin`（Windows）并持久注册 PATH：unix 上是 shell rc 文件中的标记块（`# >>> openpanda path >>>`，幂等，用户行不动），Windows 上经注册表 API 写 HKCU\Environment 并保留值类型（避开 `setx`——它把 PATH 截断在 1024 字符）外加 WM_SETTINGCHANGE 广播；随后执行已安装副本自校验。`panda doctor` 是独立自检（已安装副本可运行 / PATH 可解析 / 重启后持久 / 配置与数据库可用；任一失败退出码 1）。`panda uninstall` 白名单安全：打印完整计划、要求键入 `confirm`（脚本用 `--yes`，`--dry-run` 预览）、只删除显式推导的目标（二进制、PATH 注册、数据库+wal、context 目录、VAPID 密钥、仅限自有根内的配置），永远保留用户资产（projects/memory/skills/work 目录——与家目录或资产重叠的一律翻转为保留），先在家目录写出已删状态的 zip 备份，并产出删除/保留报告文件。护栏核心在 `internal/install`（单元测试覆盖，含符号链接安全：链接被移除、绝不跟随）。全程五语言 CLI 消息。
- **`panda web`——一条命令开箱即用的控制台**——默认回环绑定加临时随机 token（零配置），浏览器打开 `/?token=…`，应用消费一次后从地址栏剥离：无需改配置、无需粘贴 token。同样的零配置+自动登录行为进入 REPL 的 `/web` 与 `panda-webui` 边车（现在打印可直接打开的 URL）；无 token 的非回环绑定仍然拒绝启动。前端加载时消费 `?token=`（Jupyter 风格）；`make web` 在构建未落地时大声失败（占位符守卫）。
- **交互式 REPL**——`panda repl` 是操作席：裸输入进 ask 引擎，斜杠命令驱动每个面板表面（`/ask`、`/tasks`、`/task`、`/cancel`、`/approve`、`/reject`、`/logs`、`/projects`、`/project`、`/nodes`、`/authorize`、`/lang`），`/web` 一键拉起内嵌控制台。未知命令给出修复指引、绝不退出；ask 引擎可选，无模型端点时 REPL 仍服务面板命令（7a5c2bf）。
- **内嵌 Web 控制台**——控制台以 Vite + Preact + TypeScript 重建（除 Preact 外零运行时依赖），经 `go:embed` 折进二进制：队列/详情/ask/项目/节点/审批视图、实时 SSE 更新、五种界面语言（English、简体中文、日本語、Español、Deutsch）、熊猫品牌 SVG。`make web` 构建；提交的占位符让没有 node 的 `go build` 照常工作（844ccf6、688cc20）。
- **面板写路径 + SSE**——`POST /api/ask`（经共享 `askengine` 包的统一入口模型）、`POST /api/projects` + `GET /api/projects`、`GET /api/nodes`（实时能力目录）、`POST /api/tasks/{id}/cancel`、`GET /api/tasks/{id}/logs`、`GET /api/events`（队列/节点变更的 SSE 流）补齐系统审计发现的只读缺口（b599dc7、6748baa）。
- **CLI i18n**——`internal/i18n`：区域检测、英文回退、`{placeholder}` 插值；CLI 与 REPL 共享同一套五语言消息表，加一个区域条目即可扩展（7a5c2bf）。
- **配置启动校验**——资源等级、peer 与监听地址、监听/面板端口冲突在启动时检查，而非拨号时才失败（b599dc7）。
- **`scripts/smoke-delegate`**——跨进程委派验证器：成为临时调度参与者，提交一个仅 peer 具备能力的任务并汇报它跑在哪；退出码 0 表示往返在 peer 上到达 done（fbb4f9e）。
- **CONTRIBUTING.md**——工程门禁（`make gate`）、代码规范（错误包装、注释政策、并发规则、fail-closed 安全）、提交风格、i18n 规则与 PR 清单；附公开的[桌面与打包路线图](docs/plans/roadmap-desktop-and-packaging.md)（Stage 1 完成 → Stage 2 分发加固 → Stage 3 基于 Wails 的桌面端 → Stage 4 市场/移动/多用户）。
- **Sprint 2 论文机制（ATC-MARL 映射）**——`internal/scheduler/score.go`：DCPS 加权评分（`0.4·resource_efficiency + 0.3·user_priority + 0.2·scheduler_tier + 0.1·wait_time`）按 TMB 心跳新鲜度折扣（`exp(-λ·Δt)`，30 分钟半衰期）；容量驱动的 accept/decline 经 `MaxConcurrent`；心跳携带实时 `CurrentTasks`，闭合两个机制的数据环（543801f）。
- **拒绝自动改派**——任务持久化其 `requires` 能力集合（`requires_json` 列，submit 与 delegate 双路径）；被拒任务重新跑 DCPS 评分并排除历史拒绝者（审计事件 `EvDecline` 中的 `DeclinedBy`），改派到次优节点（dad4f04、P1-5）。
- **`panda metrics [--csv]`**——导出委派指标；**`panda audit verify [--task <id>]`**——校验全局审计日志或单个任务事件时间线的 `prev_hash` 链（6f2c8d5）。
- **PRAGMA `user_version` 驱动的 SQLite 迁移**（6f2c8d5、A1）与 `task_events`、`audit_log` 的 **`prev_hash` 审计链**（6f2c8d5、A3）。
- **`OPENPANDA_WAKE_KEYWORD`** 环境变量覆盖语音唤醒词；openwakeword 仍可经 `OPENPANDA_WAKE_MODEL` 指向自定义 `.tflite`（2e72c8c）。
- **agent 感官**——`time.now` 与 `weather.get` 系统工具：模型自身没有时钟也没有窗户，ask 引擎替它提供（天气经 Open-Meteo 地理编码 + 今明两天）（c36cad1）。
- **定时提醒**——`reminder.set` 工具让 agent 自行排程；SQLite 存储配原子 `ClaimDue` + 扫描器，daemon 与面板共用一个数据库而不重复触发；送达为 Web Push（回环被视为安全上下文，`panda web` 无 TLS 也能推送）与任意打开的控制台上的实时 SSE 倒计时；`panda reminder list/add/rm` 从 CLI 管理（c36cad1）。
- **MCP 集成与热重载**——一个 stdio MCP 服务器，经 `config.yaml`（`mcp.command`，保留注释的更新）或控制台设置卡配置，切换前实际拉起服务器并列出其工具做验证；工具即刻加入 agent 工具集，无需重启（c36cad1）。
- **git worktree 中的聊天会话**——控制台对话在隔离的 git worktree 中运行并流式回复；会话任务卡暴露实时思考链（task_events 回放 + SSE 重取），完成的任务恰好一次地把摘要轮折回会话（c36cad1、0e8d850）。
- **看板任务队列**——Web 控制台中的四列任务板：创建表单、优先级循环、按列拖拽重排与行内审批（da9c9e1）。
- **codex 适配器 + agent 可见性**——`adapters/codex.py` 以同一适配器协议加入 claude/opencode 之列；设置页列出已安装 agent CLI 及连通测试；`panda detect` 把硬件（CPU/RAM/GPU/agent CLI）扫描成 capabilities.yaml 草稿；doctor 现在也检查 python3、`adapters/` 与各 agent CLI（c36cad1、0e8d850）。

### 变更

- **peer 重连替换陈旧连接**——同一已认证身份的新连接换入注册表（旧连接在锁外关闭），`handleHello` 在替换时重新问候；注册表移除按连接指针匹配（befa3bd、P1-7）。
- **agent 路径并入 Tier 授权模型**——`ledger.Agent` 增加 `tier` 字段；未声明的 agent 默认 Tier 2（fail closed），在适配器子进程拉起前被 `defense.Authorize` 拒绝；显式 `tier: 1` 的卡免审批运行（c26b11e、P1-15）。
- **密钥文件加固**——含 `api_key` / `shared_secret` / `panel_token` 的配置自动 chmod 到 0600，启动时告警并建议改用环境变量（`OPENPANDA_SHARED_SECRET` / `OPENPANDA_PANEL_TOKEN` / `OPENPANDA_MODEL_API_KEY`）；chmod 失败仅告警不阻断（e5de650、P1-19）。
- **解释器 `-c` 分类改为白名单制**——只有可证明纯输出的代码（echo/print/console.log…）留在 Tier 1，其余一律 Tier 2（38186af、P1-14）。
- **面板默认回环**——`127.0.0.1:7840`；非回环绑定就明文 HTTP 告警（3c7e8f4、P1-24）。
- **部署基线成文**——明文 `ws://` 仅限回环/Tailscale/可信局域网，公网用 TLS 反代 + `wss://`（6f2c8d5、C1）。
- **个人记忆与工作区会话之间的硬记忆墙**——AskTurns 此前把 Hermes 个人记忆注入每次分类，包括绑定项目 worktree 的会话对话，「用户偏好暗色主题」可能漏进从该对话派生的代码任务。固定的 workDir 现在标记工作区对话且完全不加载个人记忆；面板也把非仓库会话钉在工作路径上，每个会话都算工作区作用域。项目记忆仍经执行节点的 ContextPack 到达执行侧，绝不进入口提示词；回归测试双向钉住（da9c9e1）。
- **Anthropic 之外的 OpenAI 线格式**——入口模型同时说 `/v1/messages` 兼容与 OpenAI 兼容端点，两条路径都支持流式补全（c36cad1）。

### 修复

- **互拨连接抖动**——两节点同时互拨（常见的双侧 `peers:` 部署）时，各自持有一条出站一条入站到同一 peer 的连接；第二次注册关闭第一条，其重连循环一秒后重拨又顶掉对侧连接——无休止的 ~1 秒连接/断开循环，把能力目录搅得反复上下线。修复：`ensurePeer` 确定性决胜——字典序较小节点 id 发起的连接胜出，双方算出同一赢家，恰有一条 TCP 存活；`MaintainPeer` 现在阻塞等到该边消亡而非热重拨（fbb4f9e，回归测试 `TestMutualDialDedup`）。
- **线协议授权缺口**——`handleResult`/`handleDecline`/`handleAccept` 校验发送者是当前执行者（审计事件 `EvDelegate` 中的 `DispatchTarget`；空 `AttemptID` 拒绝）；`handleContextAck` 校验发送者是 `context_fetch` 目标；Accept/Cancel/Approve/Reject 上的 CAS 状态守卫闭合 TOCTOU 竞态；`waiting_context` 总是带租约；本地执行失败终态化而非留僵尸（a6fc1c2、P1-1/2/3/4/6/8/9/11）。
- **命令分类绕过**——`env -S` 值递归分类；`php -r` 扫描；`find -exec`、`tar --checkpoint-action`、`git push/commit` 之流 fail-closed 到 Tier 2；`make`/`ssh` 进入破坏性表（38186af、P1-12/13）。
- **进程组管理 + 适配器硬超时**——Unix `Setpgid` 配取消时全组击杀，Windows `taskkill /T`；适配器包进 630 秒 `context.WithTimeout`（退出码 124），无孤儿孙进程（38186af、P1-17/18）。
- **记忆系统注入通道**——Hermes/Projects/skills/dream-last-deep 原子写（`util.WriteFileAtomic`）；`Projects.Save` 加互斥；外部输入污染 `[ext]` 标记且绝不被 Deep dreaming 提升；记忆注入围进 `<memory_data>` 并冠以「数据非指令」前言；日记条目换行净化（3c7e8f4、P1-20/21/22/23、P2-16）。
- **CLI 陷阱**——未知子命令退出码 2 而非启动 daemon（`panda statsu` 不再拉起常驻 daemon）；手搓 `dirOf` 换成 `filepath.Dir`（3c7e8f4、P1-25/26）。
- **取消传播**——`task_cancel` 现在向下游执行节点转发（分发目标从 `EvDelegate` 恢复），沿委派链逐跳级联；CLI 与线协议路径共享 `Core.CancelTree`/`finishCancel`（66b265d、P2-3/P2-7）。
- **事务化状态写入**——TaskStore 状态 UPDATE 与审计事件 INSERT 在同一事务提交（`applyCAS`、`applyState`、Accept/Decline/Approve/Reject/Cancel/CreateWithID）；ctxstore upsert + 容量淘汰同样事务化，附并发 Put 回归测试（bcbf156、P2-1/14）。
- **语音唤醒默认值**——默认关键词现在是各后端真实的内置项（openwakeword 用 `hey_jarvis`、pvporcupine 用 `porcupine`）；此前默认 `hey_panda` 一启动就抛错（2e72c8c、P2-21）。
- **慢速 DoS 防护**——hello 超时加全局/每 IP 连接上限（`max_connections`、`max_connections_per_ip`）（6f2c8d5、A2）。
- **MCP 客户端硬超时**附进程击杀回退（6f2c8d5、A4）。
- **综合检查清扫（D1–D32 + P1–P3）**——委派孤儿终态化、转发副本带租约、ForceFail/CompleteFromRemote/FailFromRemote 的 CAS 守卫、`PreferredNode` 绑定显式 `spec.node`、hello HMAC 绑定 5 分钟时间窗、NetworkGuard 白名单钉到已配置端点、Redact 覆盖 JSON 引号键、TierFromCommand 规范化路径/`.exe`、子进程输出捕获限界（8MiB）等（75b98c8）。

### 计划中的后续（暂缓）

有意押后到 v0.0.1 之后——记录在此以保持可见：

- **键盘快捷键**——控制台全局热键（新建会话、快速任务、视图切换）。
- **浏览器集成**——助手的伴侣浏览器表面。
- **Git 界面**——控制台内一等公民的 git 视图（分支状态、历史、远端）。
- **Worktree 管理**——从控制台列出/清理/检视聊天 worktree，而非仅随会话删除。
- **个性化**——用户可调的助手性格与呈现偏好。
- **网页搜索缓存**——agent 网页搜索的缓存层，减少重复抓取与延迟。
- **推理力度分层**——把低/中/高推理强度暴露为按任务的设置。
