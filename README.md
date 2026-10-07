# 🐼 OpenPanda

**The Open-Source, Local-First Multi-Agent Orchestration OS**

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![Release](https://img.shields.io/github/v/release/Xustalis/OpenPanda?label=release&color=blue)](https://github.com/Xustalis/OpenPanda/releases)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
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
| **Multi-device collaboration** | Cross-node task routing and scheduling over a P2P mesh | ✅ Shipped in v0.0.10 — LAN discovery, TOFU pinning, delegated worktrees |

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
- **Skills Hub & autonomous discovery** — a curated offline catalog (`panda skill hub`), importing from a path, URL, or archive (`panda skill import`), and an assistant that finds and installs the skill a task needs mid-flight.
- **Traveling context** — delegated tasks carry project memory and a workspace digest to the executing node.

### Multi-Device Collaboration

- **LAN auto-discovery + TOFU pinning** — nodes find each other on the LAN with fingerprint-confirmed admission; `panda nodes verify` pins each peer's Ed25519 key.
- **Capability cards** — each node auto-declares its hardware and tool profile (CPU, RAM, OS, available agents).
- **P2P mesh** — nodes communicate over authenticated, encrypted WebSocket and an encrypted UDP data plane with NAT traversal; data never leaves your private network.
- **Capability-based routing** — the scheduler scores nodes on measured capacity and routes each task to the best match; `panda nodes drain` parks a node for maintenance.
- **Worktree-traveling delegation** — a file task delegated to another node carries its checkout there and brings the result back.
- **Actuator dispatch** — tasks can drive physical actuators (servo, mic, camera, notify, serial MCU) on the node that owns the hardware.

### Sessions & Embedding

- **Session trees** — `panda session fork <id> --at N` branches a conversation at any turn; in a repository the child's worktree branches off the parent's, so it inherits the code the parent produced. `panda session tree` renders the family, `/fork` does it inside the REPL/TUI.
- **Auto-compaction** — overflowing history folds into a model-written running digest instead of being dropped; the stored thread stays whole.
- **`panda rpc`** — NDJSON-over-stdio embedding surface (`status`, streaming `ask`, `session.*`), so other tools can drive OpenPanda; `scripts/panda_rpc.py` is a stdlib-only reference client.
- **Subscription OAuth** — `panda auth login anthropic` signs in a Claude Pro/Max subscription via PKCE with transparent token refresh.

### Interfaces & Runtime

- **Terminal TUI** (Bubble Tea): full-screen alternate-screen mode with keyboard navigation, first-run onboarding wizard, live progress, mid-turn steering.
- **Web console**: zero-config kanban, real-time SSE streaming, skills management, session cancellation, automatic browser login.
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

To connect a second device: run `panda pair` on device A to get a pairing code, then `panda nodes add <device-A-address>` on device B — or let LAN discovery surface it under `panda nodes` and admit it by fingerprint.

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
| `panda session` | List, fork, and resume conversation sessions (`session tree` shows the family) |
| `panda skill` | Browse, import, and install workflow skills (Hub, URL, or file) |
| `panda auth login` | Sign in a model subscription (e.g. `anthropic`) via OAuth |
| `panda rpc` | NDJSON-over-stdio API for embedding OpenPanda |
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
│ sessions       Conversation trees · forking · compaction    │
│ auth           Subscription OAuth (PKCE) · token store      │
│ scheduler      Multi-device scoring & task routing          │
│ bus / ledger   P2P WebSocket + UDP transport · HMAC auth    │
│ storage        Pure Go SQLite (WAL mode)                    │
└─────────────────────────────────────────────────────────────┘
```

---

## 🗺️ Roadmap

| Version | Theme |
|---|---|
| **v0.0.8** (stable baseline) | Single-machine multi-agent orchestration, fully usable: intent classification, dispatch, supervision loop, failover, tiered approval, prompt language policy |
| **v0.0.9** (stable) — "Periapsis" | Hybrid transport/DTN architecture completed: latency-weighted mesh routing, on-wire DTN bundles, token budgets, shadow copies, actuator path, UDP datagram plane with mesh-coordinated NAT hole punching, encrypted payloads, contact plans, Ed25519 node identity, lite build — plus remembered approvals, execution attribution, a web sidebar node chip and everyday CLI polish |
| **v0.0.10** (current) — "Apoapsis" | Reaching outward to the LAN and the edge: discovery with fingerprint-confirmed admission and TOFU key pinning, worktree-traveling delegated file tasks, the clarification loop, actuator dispatch with five reference drivers (serial MCU included), the Pi adapter and a Python-free generic executor — plus session trees with forking, auto-compaction, the `panda rpc` embedding surface, subscription OAuth, signed event/audit chains, OS-level sandboxing, and a steady-state perf round. Relicensed to AGPL-3.0-or-later with dual commercial licensing |
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

OpenPanda is dual-licensed:

- **Community** — [GNU Affero General Public License v3.0 or later](LICENSE) (AGPL-3.0-or-later)
- **Commercial** — proprietary terms for closed-source embedding or hosted use; see [COMMERCIAL.md](COMMERCIAL.md)

Releases published before the license change remain available under the MIT License.
