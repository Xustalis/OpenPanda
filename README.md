# 🐼 OpenPanda

**The Open-Source, Local-First Personal Agent Operating System & Multi-Device Orchestrator**

> Connect all your devices into a private, peer-to-peer mesh — and "hire" terminal AI coding agents (Claude Code, Codex, Grok, etc.) to collaborate across machines as a unified fleet.

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![Release: v0.0.8-preview](https://img.shields.io/badge/release-v0.0.8--preview-blue.svg)](https://github.com/Xustalis/OpenPanda/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-00ADD8)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-3776AB)
![Platforms](https://img.shields.io/badge/platforms-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Memory](https://img.shields.io/badge/Memory%20Footprint-~20MB%20RSS-brightgreen)
![Local First](https://img.shields.io/badge/Cloud%20Dependency-Zero%20(Local--First)-success)

---

## ⚡ Why OpenPanda?

Today's AI coding assistants (**Claude Code, OpenAI Codex, Grok Build, OpenCode**) are remarkably powerful, but they are **trapped inside a single terminal on a single machine**.

Meanwhile, your everyday workflow spans heterogeneous machines:
- A lightweight **laptop** where you type ideas and review PRs.
- A heavy **workstation or Linux homelab** with GPUs and fast CPUs for builds, Docker, and training.
- A **Raspberry Pi or SBC** handling 24/7 daemons, cron jobs, and IoT hardware.

**OpenPanda is the missing orchestrator.** It does not replace your favorite CLI agents — it **hires** them:

```
┌─────────────────────────────────────────────────────────────┐
│                      You: One Command                       │
│              (Terminal TUI / Web Console / Voice)           │
└──────────────────────────────┬──────────────────────────────┘
                               │
                  ┌────────────▼────────────┐
                  │     🐼 OpenPanda OS     │
                  │   Route, Orchestrate,   │
                  │     Verify & Secure     │
                  └────────────┬────────────┘
                               │ Direct P2P WebSocket (Zero Cloud)
     ┌─────────────────────────┼─────────────────────────┐
     │                         │                         │
┌────▼──────────────┐   ┌──────▼────────────┐   ┌────────▼────────────┐
│  MacBook (Worker) │   │  Linux Build Box  │   │  Raspberry Pi / SBC │
│  - Fast testing   │   │  - Heavy builds   │   │  - GPIO / Sensors   │
│  - Claude Code    │   │  - Codex / Docker │   │  - 24/7 Daemons     │
└───────────────────┘   └───────────────────┘   └─────────────────────┘
```

You give an instruction once from **any** device. OpenPanda analyzes the task, delegates it to the machine with the right tools and compute, supervises the executing agent, verifies results, and streams the finished outcome back to you.

---

## 🌟 What Can OpenPanda Do?

### 1. 🌐 Heterogeneous Multi-Device Collaboration
- **Dynamic Capability Cards**: Every node declares its hardware profile (CPU, GPU, RAM, OS) and available tools.
- **Intelligent Task Routing**: The scheduler automatically routes compile-heavy jobs to high-spec machines and sensor/script jobs to low-power edge nodes.
- **P2P Mesh without Cloud Relays**: Nodes communicate directly over authenticated, encrypted WebSocket connections. Your data and codebase never leave your private devices.

### 2. 🤖 Universal Agent Orchestration ("Hiring" Agents)
- **Seamless Adapter Fleet**: Works out-of-the-box with Claude Code, OpenAI Codex, Grok Build, DeepSeek Harness, OpenCode, or bare shell commands.
- **Active Failover & Model Injection**: If an agent hits a token quota or invalid API key (401/403), OpenPanda automatically injects configured fallback models without breaking your workflow.
- **Transparent Execution Tracking**: See real-time Bash commands, file edits, and tool invocations streamed directly to your terminal or browser.

### 3. 🛡️ Autonomous Safety & Human-in-the-Loop
- **Tiered Risk Assessment**: Non-destructive, reversible tasks (reading logs, compiling, running test suites) complete autonomously.
- **Interactive Approval Gates**: Irreversible actions (pushing code to remote repositories, deleting databases, publishing artifacts) pause safely for your explicit one-click or keyboard approval.
- **Loop & Drift Prevention**: Active circuit breakers prevent infinite agent retry loops and runaway token consumption.

### 4. 🧠 Dual-Layer Memory & Self-Evolving Skills
- **Isolated Memory Wall**: Global user preferences (`USER.md`) are strictly separated from project-specific code context (`MEMORY.md`).
- **Self-Refining Procedural Skills**: OpenPanda creates and updates reusable `SKILL.md` playbooks that get smarter each time a workflow succeeds.
- **Project-Aware Delegation**: When a task moves between machines, project memory and workspace context travel with it, ensuring the remote executor understands your architecture.

### 5. 🖥️ Three Unified Interfaces
- **Interactive Terminal TUI**: Powered by Bubble Tea. Includes arrow-key navigation, real-time progress indicators, and inline approval cards.
- **Embedded Web Console & Kanban**: Zero-config web dashboard with real-time SSE streaming, task kanban boards, mobile drawer, and one-click update management.
- **Direct CLI & Scripting**: Integrate OpenPanda into custom scripts and shell pipelines with single-shot commands like `panda ask` or `panda task`.

### 6. 🪶 Ultra-Lightweight (~20MB Footprint)
- Single static Go binary with zero external runtime dependencies (pure Go SQLite via WAL mode).
- Runs effortlessly on a \$20 SBC / Raspberry Pi, older laptops, or enterprise servers.

---

## 🚀 Quick Start (3 Minutes)

### Step 1: Install OpenPanda

**macOS / Linux:**
```bash
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

**macOS (Homebrew):**
```bash
brew tap Xustalis/openpanda
brew install openpanda
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

### Step 2: Initialize Your Node

```bash
panda init
```
*The interactive wizard configures your node name, model endpoints (Anthropic, OpenAI, DeepSeek, Ollama, etc.), and registers your machine's hardware capability card.*

### Step 3: Run OpenPanda

- **Launch Terminal TUI:**
  ```bash
  panda
  ```
- **Launch Web Console (with auto-login):**
  ```bash
  panda web
  ```
- **Or ask a direct question:**
  ```bash
  panda ask "Check system status and summarize open tasks"
  ```

### Adding a Second Device in 30 Seconds

To connect your MacBook and Linux workstation:
1. On Node A (e.g. Workstation): run `panda pair` to get a pairing code.
2. On Node B (e.g. Laptop): run `panda nodes add <Node-A-Address>`.
*Both devices are now part of your private agent mesh!*

---

## 📖 Real-World Scenarios

### Scenario A: Remote Heavy Compilation & Testing
> *"I'm on a fanless MacBook Air. I ask OpenPanda to build our Rust workspace and run end-to-end tests."*
- **What happens**: OpenPanda routes the compilation task to your Linux server with 32 CPU cores. Claude Code or native Cargo compiles the project remotely, runs tests, and streams output back to your MacBook's TUI in seconds.

### Scenario B: IoT & Homelab Maintenance
> *"Turn off my Homelab test container if memory exceeds 80% and remind me tomorrow morning."*
- **What happens**: OpenPanda routes the docker command to the Homelab server, executes the inspection, sets a persistent reminder in SQLite, and fires a Web Push notification to your browser.

### Scenario C: Safe Multi-Stage Coding Workflow
> *"Refactor our database schema, run migrations, and open a pull request."*
- **What happens**: The agent safely writes migration files and tests them locally. When it attempts `git push origin`, OpenPanda intercepts the command and presents an interactive approval card:
  ```
  ⚠️  Action Requires Approval: git push origin feature/schema-v2
  [ Deny ]   [ Approve ]
  ```
  You review the changes and approve with one keystroke.

---

## 🛠️ CLI Cheat Sheet

| Command | Description |
|---|---|
| `panda` | Launch the full interactive Bubble Tea TUI |
| `panda ask "<query>"` | Direct command: answer, run tool, or delegate task |
| `panda web` | Start embedded web console & open browser automatically |
| `panda nodes` | List connected devices in your P2P mesh |
| `panda pair` | Display pairing code to invite a new node |
| `panda queue` | Inspect pending, running, and review tasks |
| `panda approve <id>` | Approve a pending Tier-2 task |
| `panda project list` | Manage workspace projects and context |
| `panda doctor` | Diagnose PATH, config, adapters, and database health |
| `panda version` | Print current binary version |

---

## 🏗️ Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│ cmd/panda      Interactive TUI + Web Console + CLI          │
├─────────────────────────────────────────────────────────────┤
│ askengine      Intent classification & management toolchain │
│ scheduler      Dynamic multi-device scoring & P2P routing   │
│ commander      3-Tier execution: Native · Agent · Manual    │
│ defense        Risk classification, circuit breaker, loops  │
│ memory         Dual-layer memory (User / Project) + Skills  │
├─────────────────────────────────────────────────────────────┤
│ bus            P2P WebSocket transport & mutual auth        │
│ ledger         Live node directory & capability cards       │
│ storage        Pure Go SQLite (WAL mode)                    │
└─────────────────────────────────────────────────────────────┘
```

---

## 🤝 Contributing

We welcome contributions from the community! Whether you want to add an adapter for a new AI CLI, improve our TUI components, or optimize scheduler heuristics:

1. Read [CONTRIBUTING.md](CONTRIBUTING.md) for code standards and workflow.
2. Check [SECURITY.md](SECURITY.md) for security guidelines.
3. Abide by the [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
4. Run `make gate` locally before submitting a PR.

---

## 📄 License

OpenPanda is open-source software licensed under the [MIT License](LICENSE).
