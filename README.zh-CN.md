# 🐼 OpenPanda

**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant

> 任何设备，任何算力，一个指令。
> 一个把「个人」放在第一位的任务编排助手——以 P2P 节点网络的方式，运行在你的异构设备之间。

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-blue)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-blue)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Status](https://img.shields.io/badge/status-pre--release-yellow)

---

## 目录

- [这是什么](#这是什么)
- [核心特性](#核心特性)
- [架构](#架构)
- [快速开始](#快速开始)
- [使用示例](#使用示例)
- [CLI 参考](#cli-参考)
- [配置](#配置)
- [文档](#文档)
- [测试](#测试)
- [部署](#部署)
- [技术栈](#技术栈)
- [路线图](#路线图)
- [参与贡献](#参与贡献)
- [许可](#许可)
- [致谢](#致谢)

## 这是什么

OpenPanda **不是又一个 Agent CLI**——它是它们**上游**的那一层：你所有设备、所有工具的大管家。

Claude Code、Codex、OpenCode、OpenClaw……每一个都是单机上的强力 Agent。OpenPanda 不与它们竞争，而是**雇佣**它们。你在哪台设备上说话，那台设备就是总指挥：能自己做的直接做；做不了的，就通过网络把任务路由给真正能干的节点——交给那个节点自己的 Agent（Claude Code、Codex……）；而单纯操作设备的任务（比如控制舵机）则直接执行，根本不需要惊动 Agent。

```
子代理（单机）              Agent 编排（单机）            OpenPanda（多设备）
┌──────────────┐           ┌──────────────┐            ┌──────────────────────┐
│ Claude Code  │           │ 多 Agent     │            │ 异构设备集群          │
│ Codex …      │           │ 编排         │            │ + 各设备的 Agent      │
│              │           │              │            │ + 裸硬件             │
└──────────────┘           └──────────────┘            └──────────────────────┘
                它们所有人的上游：OpenPanda 负责委派，它们负责执行
```

实际使用中：你只需在任何一台设备上发出一次指令，OpenPanda 就会把任务委派给最适合执行的节点，回传结果，并把学到的经验记住，留待下次使用——同时项目工作与个人记忆严格隔离，你的代码库绝不会因为「助理知道你喜欢暗色主题」而跑偏。

它从设计之初就是**个人**系统：不依赖云端、记忆只留在你的设备上、每个节点之间通过你能掌控的直连 WebSocket 通信。

## 核心特性

- **异构节点网络**——每个节点通过能力卡（capability card）声明真实能力（CPU 等级、shell、Agent 适配器）；网络把任务路由给真正能干活的节点。为笔记本电脑、开发板、台式机以及各种性能层级的设备而设计。
- **统一入口模型**——一条指令进入，三种意图输出：`answer`（纯 LLM 回答）、`tool_call`（调用你的工具）、`task`（委派到节点执行）。自动意图分类，并带优雅降级。
- **三层能力执行**——`native`（直接执行 shell 命令）、`agent`（基于适配器的 Agent，例如通过 Anthropic 兼容端点调用 Claude Code）、`manual`（进队列，等你人工审批/手动执行）。
- **P2P 委派协议**——基于 WebSocket + JSON 的幂等 `task_id` 与每次执行唯一的 `attempt_id`，崩溃重试绝不会重复执行。
- **自进化 Skill 系统**——程序性记忆以 `SKILL.md` 文件存在：每个 skill 声明适用时机、运行方式，并在每次使用后自我迭代。
- **日常助手工具**——Agent 能读系统时钟、查实时天气，还能**设置定时提醒**（`reminder.set`）：SQLite 存储、扫描器到点触发、Web Push 推送 + SSE 实时刷新到任何打开的控制台；CLI 侧 `panda reminder list/add/rm` 管理。
- **MCP 接入**——通过 config.yaml（`mcp.command`）或控制台设置页配置一个 stdio MCP 服务器；其工具**热加载**进 Agent 工具表，无需重启守护进程。
- **双层记忆**——按用户与按项目隔离的记忆（`USER.md` / `MEMORY.md` 风格），外加隔离墙；后台 **Dreaming** 引擎在节点空闲时把日常日志沉淀为长期记忆。
- **语音入口**——可选的 sidecar 管线（唤醒词 → 语音识别 → LLM → 语音合成），硬件门控，为嵌入式麦克风准备。
- **交互式 REPL + 内嵌 Web 控制台**——`panda repl` 是操作席：裸输入直达 ask 引擎，斜杠命令驱动面板（`/tasks`、`/approve`、`/projects`、`/nodes`、`/lang`……），`/web` 一键启动内嵌控制台（对话、任务看板、审批、提醒、记忆）。`panda web` 一条命令开箱即用：默认回环绑定 + 临时 token，浏览器自动登录。五种界面语言：English、简体中文、日本語、Español、Deutsch。
- **防御与安全层**——权限 Tier、熔断器、范围漂移与死循环检测；执行侧加固：沙箱、网络白名单、密钥脱敏、审计日志。
- **极致轻量**——稳态 RSS 约 **13–20 MB**，为资源受限的单板电脑而生。
- **干净交叉编译**——每个平台一个静态二进制，无需 CGO（纯 Go SQLite，`modernc.org/sqlite`）。

## 架构

```
                        ┌───────────────────────────┐
                        │     你：CLI / 语音       │
                        └─────────────┬─────────────┘
                                      │
                 ┌────────────────────▼────────────────────┐
                 │            entry · panda ask             │
                 │   分类：answer | tool_call | task        │
                 └────────────────────┬────────────────────┘
                                      │  通过 WebSocket + JSON 委派
                       ┌──────────────┴───────────────┐
                       │                              │
          ┌────────────▼────────────┐     ┌────────────▼────────────┐
          │         工作节点         │     │         工作节点         │
          │   如 笔记本（Standard）│     │   如 开发板（Micro）     │
          └─────────────────────────┘     └─────────────────────────┘
```

每个节点内部：

```
┌─────────────────────────────────────────────────────────────┐
│ cmd/panda      守护进程 + CLI（ask / status / queue / task…） │
├─────────────────────────────────────────────────────────────┤
│ entry          统一入口模型（answer · tool_call · task）      │
│ scheduler      委派与路由决策                                │
│ commander      三层执行：native · agent · manual             │
│ defense        权限 Tier · 熔断 · 范围漂移 · 循环检测         │
│ security       沙箱 · 白名单 · 脱敏 · 审计                   │
│ memory         USER/MEMORY 存储 + Dreaming 引擎              │
│ skills         SKILL.md 程序性记忆                           │
├─────────────────────────────────────────────────────────────┤
│ bus            WebSocket 传输 + 消息信封                     │
│ ledger         能力目录（能力卡、心跳）                      │
│ storage        SQLite（WAL）+ 迁移                           │
│ log / util     结构化 JSON 日志，UUIDv7                     │
└─────────────────────────────────────────────────────────────┘
```

## 快速开始

### 前置条件

| 工具 | 版本 |
|---|---|
| Go | 1.26.5+ |
| Python | 3.10+（Agent 适配器 / 语音 sidecar） |
| make | 任意较新版本 |

### 构建

```bash
make build          # 本机二进制 → bin/panda（release，剥离符号）
make web            # 构建内嵌 Web 控制台（需 node/npm；跳过则控制台显示提示页）
make test           # 运行完整测试套件
make vet            # 静态分析
```

为你的实际设备交叉编译：

```bash
make build-linux-arm64   # → bin/panda-linux-arm64  （如香橙派）
make build-linux-amd64   # → bin/panda-linux-amd64
make build-darwin-arm64  # → bin/panda-darwin-arm64
make build-windows-amd64 # → bin/panda-windows-amd64.exe
```

### 配置

复制示例配置，为每个节点修改：

```bash
cp config.example.yaml /etc/openpanda/config.yaml   # 或留在本地，用 --config 指定
```

配置很精简，注释自解释。最核心的两处：

```yaml
network:
  listen_addr: ":7836"        # WebSocket 监听地址
  shared_secret: "..."        # 节点间 HMAC 鉴权——所有节点必须共享同一值
  peers:                      # 网络中的其他节点
    - "worker-1.your-tailnet.ts.net:7836"
model:
  base_url: "https://api.deepseek.com/anthropic"  # 任何兼容 /v1/messages 的端点
  model: "deepseek-chat"
  # api_key: ""               # 优先使用 OPENPANDA_MODEL_API_KEY 环境变量
```

密钥（模型 API key）尽量从 `OPENPANDA_MODEL_API_KEY` 环境变量读取，而非配置文件。

### 运行

最快看到全貌的方式是一条命令起 Web 控制台：回环监听 + 临时 token，浏览器打开即已登录——无需改配置、无需贴 token：

```bash
./bin/panda web
```

如果还没配置模型端点，控制台的设置页可以直接管理（Anthropic / OpenAI 兼容）。

常驻多节点部署则运行守护进程本身：

```bash
./bin/panda --config config.yaml --card config/capabilities.example-desktop.yaml
```

每个**能执行任务**的节点都应带上自己的能力卡启动。没有能力卡的节点仍参与心跳，但不会被委派任务。

## 使用示例

问任何问题——入口模型自动决定是回答、调用工具、还是委派：

```bash
./bin/panda ask --card config/capabilities.example-desktop.yaml "总结一下最近一周的 git log"
```

> `--card` 指向本机能力卡；不带它时 answer/tool_call 照常工作，但委派任务的输出会拒绝执行。

查看网络与队列：

```bash
./bin/panda status
./bin/panda queue
```

查看 / 取消某个任务：

```bash
./bin/panda task <task-id>
./bin/panda cancel <task-id>
./bin/panda logs <task-id>
```

管理 skills：

```bash
./bin/panda skill
```

## CLI 参考

| 命令 | 说明 |
|---|---|
| `panda`（无参数） | 运行守护进程：节点注册、心跳、WS 服务、peer 重连 |
| `panda ask [--config PATH] [--card PATH] [--authorize] "<问题>"` | 统一入口：分类为 answer / tool_call / task 并执行 |
| `panda repl [--config PATH] [--card PATH]` | 交互式 shell：斜杠命令（tasks/approve/projects/nodes/lang），裸输入走提问引擎，`/web` 一键拉起内嵌控制台 |
| `panda web [--config PATH] [--card PATH] [--no-browser]` | 一条命令起 Web 控制台：默认回环监听 + 临时令牌，浏览器打开即已登录 |
| `panda install [--dir PATH] [--no-path]` | 将 `panda` 注册为全局命令（PATH 持久化、重启后仍可用），并自动验证安装副本可运行 |
| `panda uninstall [--config PATH] [--yes] [--no-backup] [--dry-run]` | 安全卸载：先展示完整计划，需输入 `confirm` 二次确认，白名单删除，用户资产（projects/memory/skills）始终保留，生成 zip 备份与清理报告 |
| `panda doctor [--config PATH]` | 自检：安装副本可运行、PATH 解析正常、持久化在重启后有效、配置/数据库可用 |
| `panda status` | 节点与任务状态 |
| `panda queue` | 列出任务队列 |
| `panda task [--config PATH] <task-id>` | 任务详情 |
| `panda cancel [--config PATH] <task-id>` | 取消任务（级联到执行节点） |
| `panda approve [--config PATH] <task-id>` | 批准进入 review 的任务（review → done） |
| `panda reject [--config PATH] [--reason s] <task-id>` | 拒绝进入 review 的任务 |
| `panda logs [--config PATH] <task-id>` | 任务执行日志 |
| `panda skill` | Skill 存储管理 |
| `panda reminder list \| add \| rm` | 定时提醒：列出 / 新增（`--after 10m` 或 `--at "2006-01-02 15:04"`）/ 删除 |
| `panda detect [-o PATH]` | 扫描本机硬件（CPU/RAM/GPU/Agent CLI）生成 capabilities.yaml 草稿 |
| `panda metrics [--csv]` | 导出委派指标 |
| `panda audit [--task <id>]` | 校验审计日志或单任务事件的 `prev_hash` 链 |
| `panda version` / `panda help` | 打印版本号 / 命令总览 |

## 配置

| 段 | 键 | 含义 |
|---|---|---|
| `node` | `name` | 唯一节点 ID（全网使用） |
| `node` | `resource_class` | `Micro` \| `Standard` \| `Full` → 调度器层级 |
| `network` | `listen_addr` | WebSocket 监听地址 |
| `network` | `shared_secret` | 节点间 HMAC 鉴权密钥；WS 监听缺它不启动（所有节点共享同一值） |
| `network` | `max_connections` | 全局并发 WS 连接上限（0 = 不限） |
| `network` | `max_connections_per_ip` | 单远端 IP 并发 WS 连接上限（0 = 不限） |
| `network` | `panel_addr` | Web 控制台 HTTP 地址（`panda web` / `/web`）；默认 `127.0.0.1:7840` |
| `network` | `panel_token` | 控制台 `/api/*` 的 Bearer 令牌（回环监听时自动生成临时令牌；优先用 `OPENPANDA_PANEL_TOKEN`） |
| `network` | `peers` | 要拨号的手动 peer 地址 |
| `storage` | `db_path` | SQLite 数据库路径 |
| `storage` | `context_path` | 上下文快照存储 |
| `storage` | `memory_path` | 个人记忆根目录 |
| `storage` | `projects_path` | 项目记忆根目录 |
| `storage` | `skills_path` | 程序性记忆根目录 |
| `storage` | `work_path` | Agent 执行目录；范围漂移在此测量 |
| `log` | `level` | `debug` \| `info` \| `warn` \| `error` |
| `model` | `base_url` | Anthropic 兼容 Messages API 基地址 |
| `model` | `model` | 模型 id（如 `deepseek-chat`、`deepseek-reasoner`） |
| `model` | `api_key` | 密钥——优先用 `OPENPANDA_MODEL_API_KEY` |
| `model` | `api_type` | `anthropic` \| `openai`（默认 `anthropic`） |
| `model` | `max_tokens` | 补全 token 上限（默认 4096） |
| `mcp` | `command` | stdio MCP 服务器命令行（空 = 禁用）；工具热加载进 Agent 工具表 |
| `push` | `enabled` | 开启 `/api/push/*` 与 Web Push 发送（内嵌控制台 + webui 侧车） |
| `push` | `vapid_subject` | VAPID subject（如 `mailto:` 地址） |
| `push` | `vapid_key_path` | VAPID 密钥路径（首次启动自动生成） |

配置加载优先级：`--config` 参数 > 环境变量 > 默认 `/etc/openpanda/config.yaml`。

## 文档

完整文档位于 [`docs/`](docs/) 目录：

- [文档索引](docs/README.md) —— 公开文档入口。
- [贡献指南](CONTRIBUTING.md) —— 工具链、工程质量门槛、代码规范、PR 清单
  （译版见 `CONTRIBUTING.zh-CN.md` / `CONTRIBUTING.ja.md` / `CONTRIBUTING.es.md` / `CONTRIBUTING.de.md`）。
- [桌面端与分发路线图](docs/plans/roadmap-desktop-and-packaging.md) —— 面向桌面客户端、签名安装包、公证与自动更新管道的分阶段规划。

## 测试

```bash
make test        # 完整套件
make vet         # go vet
```

核心协议不变量由真实双节点 WebSocket 测试覆盖（无需 Tailscale）：

```bash
go test ./internal/core/ -run 'TestTwoNodeProtocol|TestDelegateIdempotent|TestCancelPropagates' -v
```

## 部署

### 网络安全基线

OpenPanda 节点默认使用明文 WebSocket（`ws://`）。**明文 WebSocket 只允许在可信私有路径上使用：**

- 本机回环（如 `127.0.0.1`、`localhost`）。
- 你控制的私有覆盖网络，例如 **Tailscale** 或 VPN。
- 所有设备均受信任的物理隔离局域网。

**只要任一 OpenPanda peer 跨越公网，就必须在 WebSocket 监听器前终结 TLS**（如 nginx、Caddy、Traefik），并在 peer 地址中使用 `wss://`。`shared_secret` 仅用于节点间 hello 的 HMAC 鉴权，**不能替代传输层加密**——请勿将明文 `ws://` 监听直接暴露到公网。

可选的 `panel_addr` 为旧版 PWA 侧车提供纯 HTTP 服务，请保持回环，或同样置于 TLS 反向代理之后。

### 内存占用

OpenPanda 面向低功耗设备。上硬件前请先验证稳态内存——单次 `ps` 采样因 GC 噪声并不可靠，多采几次：

```bash
make build
for i in 1 2 3 4 5; do
  ./bin/panda --config testdata/node-a.yaml >/dev/null 2>&1 &
  PID=$!; sleep 3
  ps -o rss= -p $PID | awk '{printf "%d MB\n", $1/1024}'
  kill -TERM $PID; wait $PID 2>/dev/null
done
```

## 技术栈

| 层 | 选型 |
|---|---|
| 核心守护进程 | Go（modernc.org/sqlite —— 纯 Go，无 CGO） |
| 胶水 / 适配器 | Python 3.10+ |
| 传输 | WebSocket + JSON 信封 |
| 状态 | WAL 模式的 SQLite |
| 前端 | Web 控制台（Vite + Preact，`go:embed` 单二进制）经 `panda repl` → `/web` 或 `panda web` 启动；独立 `webui/` 侧车并存 |
| LLM 访问 | Anthropic 兼容 `/v1/messages` 或 OpenAI 兼容端点（如 DeepSeek） |

## 路线图

Phase 0–3（入口模型 · 双节点委派 · 记忆+语音+执行加固 · 内核/控制台/REPL 重建 + 实机双节点验证）已完成。Phase 4（桌面客户端 + 签名安装流水线 + 自动更新机制 + 发布渠道）详见[桌面与分发路线图](docs/plans/roadmap-desktop-and-packaging.md)。

## 参与贡献

欢迎贡献。为保证代码库一致，请在提交 PR 前通过以下工程门槛：

- `make vet && make test` 必须通过。
- `gofmt -l internal/ cmd/ adapters/` 必须无输出。
- 核心模块测试覆盖尽量保持在 ~60% 以上。

完整工程规范见 [CONTRIBUTING.md](CONTRIBUTING.md)：错误包装（`%w` / `errors.Is`）、复杂度限制、无死代码、并发规则、i18n 规则与提交信息风格。译版见 [`CONTRIBUTING.zh-CN.md`](CONTRIBUTING.zh-CN.md)、[`CONTRIBUTING.ja.md`](CONTRIBUTING.ja.md)、[`CONTRIBUTING.es.md`](CONTRIBUTING.es.md)、[`CONTRIBUTING.de.md`](CONTRIBUTING.de.md)。

## 许可

本项目基于 [MIT 许可](LICENSE) 发布。

## 致谢

灵感来自分布式多 Agent 调度理论（ATC-MARL）以及 Hermes 与 OpenClaw 的记忆模式。由 Xenith 构建。
