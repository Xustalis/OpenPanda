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

## [Unreleased]

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
- **调度器论文机制**——DCPS 加权评分（`0.4·resource_efficiency + 0.3·user_priority + 0.2·scheduler_tier + 0.1·wait_time`）按 TMB 心跳新鲜度折扣（30 分钟半衰期）；容量驱动 accept/decline；拒绝后自动改派并排除历史拒绝者（f454909、7385a89）。
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
