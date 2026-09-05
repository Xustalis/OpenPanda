# 🐼 OpenPanda

**开源、本地优先的个人智能体操作系统与多设备分布式编排中枢**

> 将你的所有设备连成私有点对点协同网络，将孤立的终端 AI Agent（Claude Code、Codex、Grok 等）“雇佣”为一支跨设备协同作战的工程团队。

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![Release: v0.0.8-preview](https://img.shields.io/badge/release-v0.0.8--preview-blue.svg)](https://github.com/Xustalis/OpenPanda/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-00ADD8)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-3776AB)
![Platforms](https://img.shields.io/badge/平台-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Memory](https://img.shields.io/badge/内存常驻-~20MB%20RSS-brightgreen)
![Local First](https://img.shields.io/badge/云端依赖-零依赖%20(纯本地私有)-success)

---

## ⚡ 为什么需要 OpenPanda？

如今的终端 AI 编程助手（**Claude Code、OpenAI Codex、Grok Build、OpenCode** 等）十分强大，但它们都有一个共同的物理限制：**被死死困在单台机器的单个终端里**。

而现实中，每位开发者的日常硬件资产往往是多元的：
- 一台随身携带、用来构思需求和审查代码的轻薄 **MacBook**；
- 一台放在家里或机房、拥有强劲 CPU/GPU 与 Docker 环境的 **Linux 编译工作站 / 服务器**；
- 一台 24 小时开机、处理常驻脚本或智能家居硬件控制的 **Raspberry Pi / 树莓派 / 香橙派 SBC**。

**OpenPanda 解决的正是这个断层。** 它不与现有的 Agent 工具竞争，而是**统领并雇佣它们**：

```
┌─────────────────────────────────────────────────────────────┐
│                       你：发送一条指令                      │
│             （终端交互 TUI / 网页控制台 / 语音入口）        │
└──────────────────────────────┬──────────────────────────────┘
                               │
                  ┌────────────▼────────────┐
                  │     🐼 OpenPanda OS     │
                  │   意图解析 · 智能路由   │
                  │   双向监督 · 安全风控   │
                  └────────────┬────────────┘
                               │ 点对点直连 WebSocket (无第三方云端)
     ┌─────────────────────────┼─────────────────────────┐
     │                         │                         │
┌────▼──────────────┐   ┌──────▼────────────┐   ┌────────▼────────────┐
│  MacBook (工作节点)│   │  Linux 编译服务器  │   │  树莓派 / SBC 开发板│
│  - 快速单测/审查  │   │  - 重型构建编译   │   │  - GPIO 引脚/传感器 │
│  - Claude Code    │   │  - Codex / Docker │   │  - 7×24h 自动化守护 │
└───────────────────┘   └───────────────────┘   └─────────────────────┘
```

你只需在**任意一台设备**上说一句话，OpenPanda 会自动分析任务意图，将其派发到算力或工具最匹配的节点上，驱动该节点上的 Agent 展开执行，全程监督执行过程并校验结果，最后将交付物准确送回你面前。

---

## 🌟 核心特性：它能为你做什么？

### 1. 🌐 异构多设备点对点智能协同
- **能力卡自动宣告**：每台设备在加入网络时会自动生成硬件与工具画像（CPU 规格、内存、操作系统、可用 Agent 适配器、物理引脚）。
- **按需精准调度**：重型编译任务自动派往高配服务器，硬件控制任务自动派往开发板，无需手动繁琐切换 SSH。
- **无云端中继的私有直连**：设备间通过安全认证的 WebSocket 点对点直通，所有代码、指令与记忆永不离开你的个人可信网络。

### 2. 🤖 全能 Agent 雇佣与无感故障转移
- **主流智能体即插即用**：开箱支持 Claude Code、OpenAI Codex、Grok Build、DeepSeek Harness、OpenCode 及本地 Shell 命令行。
- **凭据兜底与自动模型注入**：当某个 Agent 遇到 API 余额耗尽或 401 凭据失效时，OpenPanda 自动注入配置的备选模型接力执行，避免半途失败。
- **全透明执行轨迹**：实时在终端或看板中流式呈现 Agent 运行的 Bash 指令、文件修改与工具调用，清楚知道每一步是谁在调用什么模型完成的。

### 3. 🛡️ 自主安全闭环与人机协作（Human-in-the-Loop）
- **风险分级审批门**：可逆的安全操作（阅读代码、本地编译、运行单元测试）全自动执行交付。
- **关键操作人工把关**：不可逆的高风险操作（`git push` 到远程仓库、修改生产数据库、删除关键文件）自动挂起并弹出交互审批卡，支持一键或键盘确认。
- **防发散与熔断机制**：内置死循环侦测与重试熔断器，防止 Agent 在死胡同里无休止消耗你的 Token。

### 4. 🧠 双层隔离记忆与自进化技能库
- **记忆物理隔离墙**：用户个人偏好（`USER.md`）与项目代码事实（`MEMORY.md`）严格分层，保证项目工程严谨性。
- **自进化技能系统（Skills）**：每次成功解决的特定流程都会提炼为结构化的 `SKILL.md`，随着使用次数增加越用越聪明。
- **上下文随任务漫游**：跨设备委派任务时，工作目录摘要与项目专属记忆同步打包跟随，远端执行节点瞬间理解你的工程背景。

### 5. 🖥️ 三大统一入口，随心切换
- **现代终端 TUI**：基于 Bubble Tea 构建，支持上下方向键导航、实时多阶段进度、中途随时打断/转向（Mid-turn Steering）。
- **零配置开箱即用 Web 控制台**：内嵌看板视图（待办 / 进行中 / 待审批 / 已完成），实时 SSE 消息流，自适应移动端抽屉，浏览器自动免密登入。
- **脚本化 CLI 接口**：支持单行命令（如 `panda ask "构建项目并跑测试"`）直接嵌入你的日常自动化脚本。

### 6. 🪶 极致轻量：常驻内存仅 ~20MB
- 纯 Go 编写的单静态二进制文件，无额外动态链接依赖（纯 Go SQLite WAL 模式）。
- 可以在 20 美元的低功耗单板机、旧笔记本乃至高性能集群上长时间静默稳定运行。

---

## 🚀 3 分钟快速上手

### 第 1 步：一键安装

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

### 第 2 步：初始化节点

```bash
panda init
```
*交互式引导将配置你的设备名称、大模型提供商（支持 DeepSeek、Claude、OpenAI、Kimi、通义千问、Ollama 等）并生成本机能力卡。*

### 第 3 步：开始使用

- **启动交互式终端 TUI：**
  ```bash
  panda
  ```
- **启动网页看板控制台（自动调起浏览器）：**
  ```bash
  panda web
  ```
- **或者直接发起一条指令：**
  ```bash
  panda ask "查看当前系统状态并总结待办任务"
  ```

### 30 秒连接第二台设备

将你的 MacBook 与 Linux 编译机互联：
1. 在设备 A（如 Linux 机器）上运行：`panda pair` 获取配对码。
2. 在设备 B（如 MacBook）上运行：`panda nodes add <设备A地址>`。
*现在两台设备已自动组网，随时协同！*

---

## 📖 典型实战场景

### 场景一：轻薄本发起重型远程编译与测试
> “我在咖啡馆用 MacBook Air，要求 OpenPanda 编译庞大的 Rust 项目并运行全量单测。”
- **底层协作**：OpenPanda 识别到本机缺少多核编译资源，自动将编译任务路由到机房的 32 核 Linux 服务器，服务器上的 Codex 或原生 Cargo 极速完成构建，并将单测结果实时流式送回 MacBook 终端。

### 场景二：开发板与智能硬件协同
> “检测家庭服务器的内存，若超过 80% 则安全重启测试容器，并在明天早上提醒我。”
- **底层协作**：任务被分配至目标宿主机执行诊断，并在本地 SQLite 调度器中建立长效定时提醒，到期通过浏览器 Web Push 准时推送。

### 场景三：安全的自主多步编程
> “重构数据库 Schema，执行迁移脚本，并推送到 GitHub 分支。”
- **底层协作**：Agent 自动生成 SQL 迁移并在本地环境测试通过。当准备执行 `git push origin` 时，安全网关自动捕获不可逆动作并弹出交互提示卡：
  ```
  ⚠️  检测到不可逆高危操作：git push origin feature/schema-v2
  [ 拒绝 (Deny) ]   [ 批准 (Approve) ]
  ```
  你在终端或网页看板轻按一次回车或点击，动作才真正提交执行。

---

## 🛠️ 常用命令速查

| 命令 | 用途说明 |
|---|---|
| `panda` | 启动完整的交互式 Bubble Tea 终端控制台 |
| `panda ask "<指令>"` | 快速处理：直接回答、调用本地工具或委派任务 |
| `panda web` | 启动内嵌网页控制台并自动打开浏览器（带临时免密凭据） |
| `panda nodes` | 查看当前 P2P 网络中所有在线设备及其能力 |
| `panda pair` | 生成设备配对码，引导新设备加入网络 |
| `panda queue` | 查看当前正在排队、执行中或待人工审批的任务 |
| `panda approve <id>` | 审批放行特定的二级高危操作 |
| `panda project list` | 管理当前的工作空间项目与工程记忆 |
| `panda doctor` | 全面体检 PATH 路径、配置文件、适配器与数据库状态 |
| `panda version` | 输出当前二进制版本号 |

---

## 🏗️ 架构概览

```
┌─────────────────────────────────────────────────────────────┐
│ cmd/panda      终端交互 TUI · 网页看板 · CLI 快速入口       │
├─────────────────────────────────────────────────────────────┤
│ askengine      意图分类引擎 · 内核管理工具族 · 智能降级     │
│ scheduler      动态多设备算力评分 · P2P 拓扑路由            │
│ commander      三层执行架构：原生 Shell · Agent 适配 · 人工 │
│ defense        三级权限门控 · 熔断器 · 防漂移与防死循环     │
│ memory         双层隔离记忆（用户/项目） · 自学习 Skills    │
├─────────────────────────────────────────────────────────────┤
│ bus            点对点 WebSocket 传输与 HMAC 双向身份认证   │
│ ledger         分布式节点账本与能力卡广播                   │
│ storage        纯 Go 嵌入式 SQLite（WAL 高并发模式）        │
└─────────────────────────────────────────────────────────────┘
```

---

## 🤝 参与贡献

我们极其欢迎社区的参与与贡献！无论是提交新的 AI CLI 适配器、优化调度器评分算法，还是改进 TUI 组件交互：

1. 查阅 [CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md) 了解代码规范与开发流。
2. 查阅 [SECURITY.md](SECURITY.md) 了解安全边界。
3. 遵循 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) 社区行为守则。
4. 提交 PR 前请在本地运行 `make gate` 确保全部校验通过。

---

## 📄 开源许可证

OpenPanda 依据 [MIT License](LICENSE) 协议完全开源。
