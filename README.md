# 🐼 OpenPanda

**The Open-Source, Local-First Multi-Agent Orchestration OS**

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![Release: v0.0.8-preview](https://img.shields.io/badge/release-v0.0.8--preview-blue.svg)](https://github.com/Xustalis/OpenPanda/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-00ADD8)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-3776AB)
![Platforms](https://img.shields.io/badge/platforms-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Memory](https://img.shields.io/badge/Memory%20Footprint-~20MB%20RSS-brightgreen)
![Local First](https://img.shields.io/badge/Cloud%20Dependency-Zero-success)

---

## About

OpenPanda organizes terminal AI coding agents (Claude Code, OpenAI Codex, Grok Build, DeepSeek Harness, OpenCode, and more) together with your devices into a single execution crew. You issue one instruction; it handles intent classification, task decomposition, dispatch, execution supervision, and result verification — all locally, with zero cloud dependency.

```
┌─────────────────────────────────────────────────────────────┐
│                      You: One Command                       │
│           (Terminal TUI / Web Console / CLI Script)         │
└──────────────────────────────┬──────────────────────────────┘
                               │
                  ┌────────────▼────────────┐
                  │     🐼 OpenPanda OS     │
                  │   Route, Orchestrate,   │
                  │     Verify & Secure     │
                  └────────────┬────────────┘
                               │ Direct P2P WebSocket (no cloud relay)
     ┌─────────────────────────┼─────────────────────────┐
     │                         │                         │
┌────▼──────────────┐   ┌──────▼────────────┐   ┌────────▼────────────┐
│  MacBook (Worker) │   │  Linux Build Box  │   │  Raspberry Pi / SBC │
│  - Claude Code    │   │  - Codex / Docker │   │  - 24/7 Daemons     │
└───────────────────┘   └───────────────────┘   └─────────────────────┘
```

The project evolves along two tracks:

| Track | Description | Status |
|---|---|---|
| **Multi-Agent orchestration** | Hire and command multiple terminal agents: dispatch, supervision, failover, approval | ✅ Current focus |
| **Multi-device collaboration** | Cross-node task routing and scheduling over a P2P mesh | 🔶 Preview — not yet hardened; the theme of v0.0.10 |

---

## ✨ Features

### Agent Orchestration

- **Intent classification & task decomposition** — the engine distinguishes chat, management queries, and executable tasks; multi-stage work is first split into a plan with artifact wiring between stages.
- **Supervision loop** — the entry model continuously judges task completion and re-dispatches with the failure reason attached, until done or the round budget (5 rounds by default) is spent.
- **Transparent failover** — quota exhaustion or dead credentials (401/403) trigger automatic fallback-model injection; the task continues instead of dying halfway.
- **Transparent execution** — every Bash command, file edit, and tool call, along with the responsible agent and underlying model, streams live to your terminal and console.
- **Tiered approval** — reversible operations run autonomously; irreversible actions (`git push`, production database changes) suspend for explicit human confirmation.
- **Circuit breakers** — loop detection and retry breakers stop runaway agents from burning tokens.

### Memory & Skills

- **Dual-layer memory** — user preferences (`USER.md`) and project facts (`MEMORY.md`) are strictly separated.
- **Self-evolving skills** — successful workflows are distilled into `SKILL.md` playbooks that accumulate over time.
- **Traveling context** — delegated tasks carry project memory and a workspace digest to the executing node.

### Multi-Device Collaboration (Preview)

- **Capability cards** — each node auto-declares its hardware and tool profile (CPU, RAM, OS, available agents).
- **P2P mesh** — nodes communicate over authenticated, encrypted WebSocket; data never leaves your private network.
- **Capability-based routing** — the scheduler scores nodes and routes each task to the best match.

### Interfaces & Runtime

- **Terminal TUI** (Bubble Tea): arrow-key navigation, live progress, mid-turn steering.
- **Web console**: zero-config kanban, real-time SSE streaming, automatic browser login.
- **Scriptable CLI**: `panda ask` drops straight into automation scripts.
- **Featherweight**: single static Go binary, ~20MB RSS, no external runtime dependencies.

---

## 📦 Installation

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

---

## 🚀 Quick Start

Initialize your node (interactive setup for device name, model providers, and the local capability card):

```bash
panda init
```

Then pick an entry point:

```bash
panda                                                # interactive terminal TUI
panda web                                            # web console, opens browser automatically
panda ask "check system status and summarize tasks"  # one-shot command
```

To connect a second device (preview capability): run `panda pair` on device A to get a pairing code, then `panda nodes add <device-A-address>` on device B.

---

## 🛠️ CLI Cheat Sheet

| Command | Description |
|---|---|
| `panda` | Launch the interactive terminal console |
| `panda ask "<query>"` | One-shot: direct answer, tool call, or task dispatch |
| `panda web` | Start the web console and open the browser automatically |
| `panda nodes` | List online devices and their capabilities |
| `panda pair` | Display a pairing code for a new node |
| `panda queue` | Inspect pending, running, and review tasks (`--watch` for live updates) |
| `panda approve <id>` | Approve a pending Tier-2 task |
| `panda project list` | Manage workspace projects and context |
| `panda doctor` | Diagnose PATH, config, adapters, and database health |
| `panda version` | Print the current version |

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ cmd/panda      Interactive TUI · Web Console · CLI          │
├─────────────────────────────────────────────────────────────┤
│ askengine      Intent classification · management tools     │
│ plan           Multi-stage decomposition & artifact wiring  │
│ commander      3-Tier execution: Native · Agent · Manual    │
│ defense        Tiered gating · circuit breakers · anti-loop │
│ memory         Dual-layer memory (User/Project) + Skills    │
├─────────────────────────────────────────────────────────────┤
│ scheduler      Multi-device scoring & task routing          │
│ bus / ledger   P2P WebSocket transport · HMAC auth · ledger │
│ storage        Pure Go SQLite (WAL mode)                    │
└─────────────────────────────────────────────────────────────┘
```

---

## 🗺️ Roadmap

| Version | Theme |
|---|---|
| **v0.0.8** (current baseline) | Single-machine multi-agent orchestration, fully usable: intent classification, dispatch, supervision loop, failover, tiered approval |
| **v0.0.9** | Sharper agent control: supervision verdicts, agent routing stability, execution transparency |
| **v0.0.10** | Multi-device collaboration as the headline: cross-node delegation, lease protection, resumable execution — hardened and field-tested |
| **v0.0.x (beyond)** | Stability, performance, and edge-case tuning |
| **v0.1.0** | Desktop capabilities and stronger control & management — commercial-grade quality |

---

## 🔭 Vision

OpenPanda's architecture is designed for large-scale heterogeneous clusters: drone swarm control, communication scheduling for deep-space satellite constellations, and coordinated autonomous-vehicle fleets. These scenarios share one fundamental problem — **every node has different compute, different capabilities, and different jobs, yet all need to be coordinated, scheduled, and supervised together.**

Today, OpenPanda serves a developer's devices and agents. The long-term goal is to extend the same orchestration kernel to autonomous nodes at cluster scale.

---

## 🤝 Contributing

1. Read [CONTRIBUTING.md](CONTRIBUTING.md) for code standards and workflow.
2. Check [SECURITY.md](SECURITY.md) for security guidelines.
3. Abide by the [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
4. Run `make gate` locally before submitting a PR.

---

## 📄 License

[MIT License](LICENSE)
