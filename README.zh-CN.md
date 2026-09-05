# 🐼 OpenPanda

**开源、本地优先的多 Agent 协同操作系统**

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![Release: v0.0.8-preview](https://img.shields.io/badge/release-v0.0.8--preview-blue.svg)](https://github.com/Xustalis/OpenPanda/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-00ADD8)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-3776AB)
![Platforms](https://img.shields.io/badge/%E5%B9%B3%E5%8F%B0-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Memory](https://img.shields.io/badge/%E5%86%85%E5%AD%98%E5%B8%B8%E9%A9%BB-~20MB%20RSS-brightgreen)
![Local First](https://img.shields.io/badge/%E4%BA%91%E7%AB%AF%E4%BE%9D%E8%B5%96-%E9%9B%B6%E4%BE%9D%E8%B5%96-success)

***

## 简介

OpenPanda 将终端 AI 编程助手（Claude Code、OpenAI Codex、Grok Build、DeepSeek Harness、OpenCode 等）与你的设备组织为一支统一的执行队伍。你发出一条指令，它负责意图理解、任务拆解、调度派发、执行监督与结果校验——全程本地运行，不依赖任何云端服务。

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
                               │ 点对点直连 WebSocket（无云端中转）
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
| **多设备协同控制**      | P2P 网络内跨节点任务路由与调度               | 🔶 预览能力，尚未重点调试，v0.0.10 主题 |

***

## ✨ 核心特性

### Agent 编排

- **意图分类与任务拆解** —— 意图分类引擎区分闲聊、管理操作与可执行任务；多阶段任务先拆解为带产物接线的 Plan，再逐阶段推进。

- **监督循环（Supervision Loop）** —— 入口模型持续判定任务完成度，不合格则携带失败原因重新派发，直到完成或超出轮次预算（默认 5 轮）。

- **无感故障转移** —— Agent 遇到配额耗尽或凭据失效（401/403）时，自动注入配置的备用模型接力执行，任务不中断。

- **透明执行** —— 每条 Bash 命令、每次文件修改、每次工具调用及其背后的 Agent 与模型，实时流式回传终端与控制台。

- **分级审批** —— 可逆操作全自动执行；`git push`、修改生产数据库等不可逆操作挂起等待人工确认。

- **熔断保护** —— 死循环侦测与重试熔断器，防止 Agent 失控消耗 Token。

### 记忆与技能

- **双层记忆** —— 用户偏好（`USER.md`）与项目事实（`MEMORY.md`）严格分层。

- **自进化技能** —— 成功流程沉淀为 `SKILL.md`，随使用积累。

- **上下文漫游** —— 跨设备委派任务时，项目记忆与工作目录摘要随任务同行。

### 多设备协同（预览）

- **能力卡宣告** —— 每台设备自动生成硬件与工具画像（CPU、内存、系统、可用 Agent）。

- **P2P 直连** —— 节点间通过认证加密的 WebSocket 点对点通信，数据不离开私有网络。

- **按需路由** —— 调度器依据能力评分将任务派往最匹配的节点。

### 接口与运行时

- **终端 TUI**（Bubble Tea）：方向键导航、实时进度、中途打断与转向。

- **Web 控制台**：零配置、任务看板、实时 SSE 消息流、浏览器自动登录。

- **脚本化 CLI**：`panda ask` 单行命令嵌入自动化脚本。

- **极致轻量**：纯 Go 单静态二进制，常驻内存约 20MB，无外部运行时依赖。

***

## � 安装

**macOS / Linux：**

```bash
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

**macOS (Homebrew)：**

```bash
brew tap Xustalis/openpanda
brew install openpanda
```

**Windows (PowerShell)：**

```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

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

连接第二台设备（预览能力）：在设备 A 上运行 `panda pair` 获取配对码，在设备 B 上运行 `panda nodes add <设备A地址>`。

***

## 🛠️ 常用命令

| 命令                   | 用途                               |
| -------------------- | -------------------------------- |
| `panda`              | 启动交互式终端控制台                       |
| `panda ask "<指令>"`   | 单次执行：直接回答、调用工具或委派任务              |
| `panda web`          | 启动网页控制台并自动打开浏览器                  |
| `panda nodes`        | 查看 P2P 网络中的在线设备及其能力              |
| `panda pair`         | 生成设备配对码                          |
| `panda queue`        | 查看排队中、执行中或待审批的任务（`--watch` 实时刷新） |
| `panda approve <id>` | 审批放行二级高危操作                       |
| `panda project list` | 管理工作空间项目与工程记忆                    |
| `panda doctor`       | 体检 PATH、配置、适配器与数据库               |
| `panda version`      | 输出版本号                            |

***

## 🏗️ 架构

```
┌─────────────────────────────────────────────────────────────┐
│ cmd/panda      终端交互 TUI · 网页看板 · CLI 快速入口       │
├─────────────────────────────────────────────────────────────┤
│ askengine      意图分类引擎 · 管理工具族 · 智能降级         │
│ plan           多阶段任务拆解与阶段间产物接线               │
│ commander      三层执行架构：原生 Shell · Agent 适配 · 人工 │
│ defense        分级权限门控 · 熔断器 · 防漂移与防死循环     │
│ memory         双层隔离记忆（用户/项目） · 自学习 Skills    │
├─────────────────────────────────────────────────────────────┤
│ scheduler      多设备能力评分与任务路由                     │
│ bus / ledger   P2P WebSocket 传输 · HMAC 认证 · 节点账本    │
│ storage        纯 Go 嵌入式 SQLite（WAL 模式）             │
└─────────────────────────────────────────────────────────────┘
```

***

## 🗺️ 路线图

| 版本               | 主题                                        |
| ---------------- | ----------------------------------------- |
| **v0.0.8**（当前基线） | 单机多 Agent 协同完整可用：意图分类、任务派发、监督循环、故障转移、分级审批 |
| **v0.0.9**       | 调优 Agent 控制能力：监督判定、Agent 路由稳定性、执行轨迹透明度    |
| **v0.0.10**      | 重点完成多设备协同控制：跨节点委派、租约保护、断点续跑的全面打磨与实测       |
| **v0.0.x（后续）**   | 稳定性、性能与边缘场景调优                             |
| **v0.1.0**       | 桌面端（Desktop）能力与更强的操控管理能力，达到可商用化标准         |

***

## 🔭 愿景

OpenPanda 的架构为大规模异构集群而设计：无人机集群控制、深空卫星星座通信调度、大规模自动驾驶车队协同。这些场景的本质问题是一致的——**每个节点的算力不同、功能不同、执行的任务不同，需要被统一协调、调度与监督。**

当前版本面向个人开发者的设备与 Agent。长期目标是将同一套编排内核延伸至集群规模的自治节点。

***

## 🤝 参与贡献

1. 查阅 [CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md) 了解代码规范与开发流。
2. 查阅 [SECURITY.md](SECURITY.md) 了解安全边界。
3. 遵循 [CODE\_OF\_CONDUCT.md](CODE_OF_CONDUCT.md) 社区行为守则。
4. 提交 PR 前在本地运行 `make gate` 确保全部校验通过。

***

## 📄 许可证

[MIT License](LICENSE)
