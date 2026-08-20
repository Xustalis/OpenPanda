# OpenPanda CLI 内核重构设计文档

- 日期：2026-08-20
- 状态：已批准，进入执行阶段
- 范围：后端行为改造（Go `internal/`、`cmd/`）、CLI 内核重构（`cmd/panda/`）、Web 薄壳（`webui/`）

---

## 1. 背景与目标

### 1.1 项目背景

OpenPanda 是一个基于 **Go 1.26** 的个人分布式助手：后端单二进制内置 daemon、任务调度、记忆系统与 agent 编排能力；前端为 Vite + Preact + TypeScript，经 `go:embed` 嵌入二进制，通过 `panda serve` / `panda web` 提供 Web 面板。当前形态中，Web 面板功能较全，而 CLI 仅覆盖部分能力，两者行为存在割裂。

### 1.2 核心原则

本次重构的第一原则是：**CLI 是内核、Web 是薄壳**。

- 所有 Web 能力，CLI 必须有对等实现；
- CLI 与 Web 共用同一服务层，行为保持一致；
- CLI 参照 **Claude Code / Kimi Code / Codex CLI** 的设计状态（子命令分层、REPL 斜杠命令、无头输出、启动 banner 与状态栏等 UX 基线）。

### 1.3 关键设计方向

1. **模型注入反转**：默认使用 agent 工具自带模型，不覆盖；仅当 agent 无自有模型时才注入 panda 的模型，且必须显式提醒用户。
2. **记忆多文件选择性加载**：记忆拆为多文件，注入"清单+索引"由 agent 按需自读；上限改为 config 可配置；所有记忆均可修正（Web/CLI 可编辑）。
3. **日志 Prune 接入与梦境低权重沉淀**：`daily.Prune` 接入 daemon 定时（现状仅测试代码调用，从未生效）；日志进梦境但低权重；用户重复强调达阈值自动晋升主记忆，并支持事后修正。
4. **路由性价比化**：能力卡匹配 + cost_tier 性价比权重 + 用户可配置优先 agent。
5. **Web 设置分层**：照 Codex 思路尽力做——简单的直接做，过于复杂的暂缓但写入后续更新清单。

### 1.4 目标

- 后端行为对齐已确认的注入/路由/记忆决策；
- CLI 补齐 Web 有而 CLI 无的全部能力缺口（G1-G10），REPL/TUI 达到对标产品的设计状态；
- Web 收敛为薄壳：设置分组化、记忆管理多文件化、设备与委派链可见，业务逻辑下沉共用。

---

## 2. 已确认决策基线

以下决策已与用户确认，为本设计的不可动摇基线：

| # | 决策 |
|---|------|
| D1 | 注入指"模型注入"：默认使用 agent 工具自带模型，不覆盖；仅当 agent 无自有模型时才注入 panda 的模型，且必须显式提醒用户 |
| D2 | panda 自身具备 agent 能力：简单任务自己做，解决不了才调度外部 agent 工具 |
| D3 | 项目执行时一律不注入记忆 |
| D4 | 路由：能力卡匹配 + 性价比权重（cost_tier）+ 用户可配置优先 agent |
| D5 | 记忆上限改为 config 可配置；记忆拆多文件，注入"清单+索引"由 agent 自读（方案 A）；所有记忆错了都可修正（Web/CLI 可编辑） |
| D6 | `daily.Prune` 接入 daemon 定时；日志进梦境但低权重；用户重复强调达阈值自动晋升主记忆（A+B 结合），并支持事后修正 |
| D7 | CLI 是内核、Web 是薄壳；所有 Web 能力 CLI 必须有；CLI 参照 Claude Code / Kimi Code / Codex CLI 设计状态 |
| D8 | Web 设置分层照 codex 思路尽力做：简单的直接做，过于复杂的暂缓但写入后续更新清单 |

---

## 3. 三大阶段设计

### 阶段 A：后端行为改造（Go，`internal/` 与 `cmd/`）

#### A1 模型注入策略反转（`internal/commander`、`internal/config`）

- `internal/commander/adapter.go` 的 `modelEnv`（L42-61）默认不再注入。新增 config 字段 `injection.model: auto|always|never`，默认 `auto`。
- `auto` 判定：探测目标 agent 是否自带模型凭证（复用 `webui/panel/agents.go` 的 probeAgent 思路，检查环境变量与 CLI 登录态）；自带则原样透传；不自带且 panda 有模型配置才注入。
- 注入时显式提醒：任务输出首行打印注入声明（注入的模型/端点、原因），同时写 audit 事件与任务事件流（Web 任务详情可见）。
- `internal/core/handlers.go` `withProjectMemory` / `buildAgentPrompt`：项目执行路径移除记忆注入（技能注入保留），落实决策 D3。

#### A2 路由与性价比权重（`internal/commander/commander.go`）

- `MatchAgent` 增加评分：能力匹配度 × cost_tier 折扣，取最高分；支持 config `routing.preferred_agents` 用户优先级加权。
- fallback 链：首选 agent 不可用 → 下一个匹配 agent → panda 内置能力兜底（兜底时按 A1 走注入+提醒）。现有熔断器逻辑保留。
- 入口分类为"简单"的任务由 panda 内置 askengine 直接处理（现状已如此，补充路由日志说明）。

#### A3 记忆系统改造（`internal/memory`、`internal/askengine`、`internal/config`）

- **上限可配置**：`internal/memory/format.go` 三个硬常量（USER.md=1375、MEMORY.md=2200、项目记忆=8000）改为 config `memory.limits.{user,memory,project}`，默认 5000/10000/30000；`hermes.go`、`webui/panel/memory.go` 联动。
- **多文件记忆**：`memory/topics/*.md` 目录式扩展，Hermes 提供文件清单+首行摘要索引；每个 topic 文件沿用 `§` 条目格式与上限校验。
- **选择性加载**：`Injector.Conversation` 对 agent 路径只注入"记忆文件清单+简短索引"；`AdapterRequest`（`adapter.go` L36-40）新增记忆文件路径清单字段，agent 用自身文件读取能力按需读取。panda 内置问答（askengine）保留检索注入并扩展到多文件打分（Retriever top-K）。

#### A4 日志 Prune 与梦境低权重沉淀（`internal/memory/daily.go`、`dream_scheduler.go`、`dream.go`）

- 把 `*Daily` 注入 memory.Scheduler，在 `tick`（`dream_scheduler.go` L58）内按天调用一次 `daily.Prune(now)`（与 dreamSched 同 tick，独立 24h 门槛）。
- 日志候选低权重：`deep()`（`dream.go` L162-209）对 daily 来源候选乘折扣系数（如 0.5），写入 `signals.go` 权重体系旁注。
- 重复强调晋升（A+B 结合）：daily 候选 frequency 达阈值（如 7 天内 ≥3 次）时，在 provenance 门禁上开白名单通道，自动写入 MEMORY.md 并标注来源为日志；同时产生事件供 Web 展示；用户可在 Web/CLI 修正或删除。

### 阶段 B：CLI 内核重构（`cmd/panda/`，对标 claude/kimi/codex）

#### B1 子命令分层与能力补齐（Web 有 CLI 无的 G1-G10 缺口）

资源命令族（沿用 reminder/skill 二级分发模式，不引入 cobra）：

- `panda session list|new|show|rm|ask|diff|merge`（复用 `internal/sessions`，与 sessions.go 逐端点对应）
- `panda task add|priority|move`（补齐看板创建/优先级/排序；queue 加 `--project` 过滤）
- `panda memory list|get|set|correct`（走 Hermes 校验与原子写；支持多文件/项目记忆/daily 查看）
- `panda config model|mcp|limits|routing get|set|test`（复用 UpdateModelSection 等；输出明示"重启后生效"）
- `panda agents [test <name>]`、`panda audit entries`、`panda project list|create`

全局能力：

- 全局 `--json` flag；panel 一次性命令族接入 `internal/i18n`；消除 `skill.go` 硬编码中文。
- `panda ask` 补 `--output-format json|stream-json` 无头模式。

#### B2 REPL/TUI 升级（对标 codex 设计状态）

- **斜杠命令扩编**：`/sessions /resume /memory /context /config /agents /policy /nodes` 等补齐至覆盖全部 Web 能力；`/help` 改全屏分页器。
- **UX 基线**：启动 banner（版本/模型/目录/推荐命令）、footer 状态栏（节点名/审批模式/上下文占用）、快捷键共识（Esc 中断、Shift+Tab 切模式、双 Ctrl-C 退出）。
- **TUI 框架决策**：采用 Bubble Tea（Charm，纯 Go 编译进二进制，不违反零外部运行时依赖原则）实现状态栏/分页器/补全；若实现成本过高则降级为手写渲染，但状态栏+分页器+补全三项必须落地。
- 修复 REPL `/web` 的 Deps 缺项（`cmd/panda/repl.go` L439-445），与 `panda web` 对齐。

### 阶段 C：Web 薄壳与分层设置（`webui/`）

#### C1 设置页分层（参考 codex 分组）

- settings 页重组为分组导航：常规（语言、外观主题）/ 配置（模型、MCP、注入策略开关、路由优先级、记忆上限、批准策略）/ 记忆管理 / 设备与节点 / Agents。
- 主题：基于现有 CSS 变量体系加亮/暗/跟随系统三档切换（localStorage + 设置页）。
- 批准策略二维化：config 新增 `approval.mode`（always|on-request|never）与执行范围展示（只读展示现有沙盒状态），Web/CLI 双端可读写。
- 暂缓项写入 CHANGELOG 后续更新清单：快捷键、浏览器、git、worktree 管理页、个性化、网页搜索缓存（该功能尚不存在）、推理强度档位。

#### C2 记忆管理页扩展

- `GET /api/memory` 扩展为多文件清单（含 topics/、daily/、项目记忆）；`PUT /api/memory/files/{name}` 通用编辑端点（保留 Hermes 校验）；新增 `PUT /api/projects/{name}/memory`；DREAMS.md 开放编辑（带确认对话框）；展示晋升来源标注，支持修正/删除。

#### C3 设备与设备链可见性

- 新增 `GET /api/self`（本机设备画像：hostname/OS/芯片/内存/能力卡）。
- nodes 页增强：本机 self 标识、能力卡详情展开、委派链（task chain）可视化、可控 agent 列表（复用 /api/agents）。

#### C4 架构对齐（Web 基于 CLI 内核）

- 将 `webui/panel` 各 handler 的业务逻辑下沉到 internal 服务层（或明确复用现有 internal 包），CLI 命令与 panel API 共用同一服务层，保证行为一致；HTTP+SSE 通信模式保留。

---

## 4. 测试计划

- **Go**：`go build ./...`、`go test ./...`（重点：commander 路由权重/注入开关、memory 上限配置化、dream 晋升门禁、daily prune 定时）。
- **前端**：`npm run typecheck`、`make web` 构建嵌入；5 语言 i18n key 同步。
- **Browser E2E**：设置分组页、记忆多文件编辑、设备页、注入提醒在任务详情可见。
- **CLI 冒烟**：session/task/memory/config 命令族逐一验证与 Web 行为一致。

---

## 5. 假设与暂缓项

- 网页搜索缓存在项目中不存在，不在本期范围。
- 会话 fork/rewind/checkpoint 属二期体验增强，不在本期。
- 记忆跨节点同步（bus 新消息类型）不在本期。
- Web 设置暂缓项（写入 CHANGELOG 后续更新清单）：快捷键、浏览器、git、worktree 管理页、个性化、网页搜索缓存、推理强度档位。
- 执行阶段第一步：将本设计写入 `docs/superpowers/specs/` 设计文档后再开始编码。
