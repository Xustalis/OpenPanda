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

OpenPanda organizes the terminal AI coding agents already on your machines — ten bundled adapters (Claude Code, OpenAI Codex, OpenCode, Grok Build, DeepSeek Harness, Antigravity, Hermes, Pi, ZCode, OpenClaw) plus a generic bridge for anything else — together with your devices into a single execution crew. It is not itself an agent: you issue one instruction; it handles intent classification, task decomposition, dispatch, execution supervision, and result verification — all locally, with zero cloud dependency.

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
                               │ Direct P2P links (WS + sealed UDP, no cloud relay)
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
| **Multi-device collaboration** | Cross-node task routing and scheduling over a P2P mesh | ✅ Shipped in v0.0.10; v0.0.11 adds per-session AEAD, Bluetooth-style pairing, trust tiers, multi-hop relays; v0.0.12 adds same-project parallel execution via path-scoped lanes and isolated worktrees |

---

## ✨ Features

### Agent Orchestration

- **Intent classification & task decomposition** — the engine distinguishes chat, management queries, and executable tasks; multi-stage work is first split into a plan with artifact wiring between stages.
- **Supervision loop** — the entry model continuously judges task completion and re-dispatches with the failure reason attached, until done or the round budget (5 rounds by default) is spent.
- **Multi-model registry & transparent failover** — `panda model add|switch|test` manages several providers per node behind a health breaker; quota exhaustion or dead credentials (401/403) trigger automatic fallback-model injection, and the task continues instead of dying halfway.
- **Multi-harness runs** — `panda ask --agents a,b --mode parallel` fans one instruction out to several agents as a plan and collects the verdicts.
- **Transparent execution** — every Bash command, file edit, and tool call, along with the responsible agent and underlying model, streams live to your terminal and console.
- **Tiered approval** — reversible operations run autonomously; irreversible actions (`git push`, production database changes) suspend for explicit human confirmation.
- **Circuit breakers** — loop detection and retry breakers stop runaway agents from burning tokens.
- **Sibling message board** (experimental) — tasks sharing a plan or a parent exchange short notes (a status line, a finding, a pointer to produced files) through a bounded slot board; each agent reads what its peers posted at its next round instead of blocking on their results, and a per-episode budget keeps the channel from becoming chatter.

### Memory & Skills

- **Dual-layer memory** — user preferences (`USER.md`) and project facts (`MEMORY.md`) are strictly separated.
- **Self-evolving skills** — successful workflows are distilled into `SKILL.md` playbooks that accumulate over time.
- **Skills Hub & autonomous discovery** — a curated offline catalog (`panda skill hub`), importing from a path, URL, or archive (`panda skill import`), and an assistant that finds and installs the skill a task needs mid-flight.
- **Traveling context** — delegated tasks carry project memory and a workspace digest to the executing node.

### Multi-Device Collaboration

- **LAN auto-discovery + Bluetooth-style pairing** — nodes find each other on the LAN with fingerprint-confirmed admission; `panda pair` joins a device via a short code confirmed on both ends, and `panda nodes verify` pins each peer's Ed25519 key.
- **Capability cards** — each node auto-declares its hardware and tool profile (CPU, RAM, OS, available agents).
- **P2P mesh** — nodes communicate over an authenticated WebSocket with negotiated per-session AEAD encryption, plus an encrypted UDP data plane with NAT traversal; data never leaves your private network.
- **Trust zones** — every peer carries an operator-set tier (`panda nodes trust <id> mesh|guarded|open`) that clamps what a delegated task may expose to that peer and how work inbound from it is admitted; display content is mesh-only end to end, so a trust demotion cuts a screen transfer mid-flight.
- **Capability-based routing** — the scheduler scores nodes on measured capacity and routes each task to the best match; `panda nodes drain` parks a node for maintenance.
- **Worktree-traveling delegation** — a file task delegated to another node carries its checkout there and brings the result back.
- **Multi-hop mesh** — hard pins (`--preferred`, stage `node:`) and the context/artifact data plane route over multiple relays on a partial mesh: a node two hops out is addressed through the gossiped link-state, and an executor that cannot reach the origin pulls its snapshot and inputs through the hop that handed it the task.
- **Actuator dispatch** — tasks can drive physical actuators on the node that owns the hardware; seven reference drivers ship in `drivers/` (servo, mic, camera, notify, serial MCU, screen capture, GUI input), with `.ps1` variants so stock Windows nodes need no git-bash.
- **Computer-use pair** — a node with a desktop session advertises `hardware:screen_capture` (screenshot the display) and `hardware:gui_input` (click, type, key, scroll — tier 2 because a click cannot be unsent), so the agent on that machine can see and drive its GUI; headless daemons never claim a screen they cannot see. Screen output only travels between mesh-trusted nodes.
- **Retrievable artifacts** — what a task produces (a screenshot, a report file) is packed and pulled back to you: `panda task artifact <id>` lists and extracts it, and the console previews images and text inline.

### Sessions & Embedding

- **Session trees** — `panda session fork <id> --at N` branches a conversation at any turn; in a repository the child's worktree branches off the parent's, so it inherits the code the parent produced. `panda session tree` renders the family, `/fork` does it inside the REPL/TUI.
- **Auto-compaction** — overflowing history folds into a model-written running digest instead of being dropped; the stored thread stays whole.
- **`panda mcp`** — exposes the node's own tools (tasks, memory, skills…) as an MCP stdio server, so any MCP-speaking agent can drive the node through the standard protocol.
- **`panda rpc`** (experimental) — NDJSON-over-stdio embedding surface (`status`, streaming `ask`, `session.*`), so other tools can drive OpenPanda; `scripts/panda_rpc.py` is a stdlib-only reference client.
- **Subscription OAuth** (experimental) — `panda auth login anthropic` signs in a Claude Pro/Max subscription via PKCE with transparent token refresh.

### Interfaces & Runtime

- **Terminal TUI** (Bubble Tea): full-screen alternate-screen mode with keyboard navigation, first-run onboarding wizard, live progress, mid-turn steering; `PANDA_CLASSIC_REPL=1` keeps the classic line REPL around.
- **Web console**: zero-config kanban, real-time SSE streaming, skills management, session cancellation, automatic browser login; `panda web --lan` shares it on the LAN behind an ephemeral token, with Web Push notifications.
- **Voice entry** (experimental): `panda voice` runs the wake → transcribe → dispatch → speak loop through the bundled `extensions/voice` sidecar scripts (needs its own Python deps).
- **Scriptable CLI**: `panda ask` drops straight into automation scripts.
- **Self-updating**: `panda update check|apply` (or `panda upgrade`) probes a staged binary and swaps it in place.
- **Featherweight**: single static Go binary, ~20MB RSS, no external runtime dependencies.

---

## 📦 Installation

**macOS / Linux:**
```bash
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

**macOS (Homebrew):**
```bash
brew install Xustalis/openpanda/openpanda
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

**From source** (Go ≥ 1.26, no CGO — cross-compiles cleanly):
```bash
git clone https://github.com/Xustalis/OpenPanda.git && cd OpenPanda
make build && ./bin/panda init
```

Already installed? `panda update` upgrades in place; user data is preserved.

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

To connect a second device: run `panda pair <device-B-address>` on device A and confirm the same short code with `panda pair confirm` on device B — or let LAN discovery surface it under `panda nodes` and admit it by fingerprint (`panda nodes admit`).

---

## 🛠️ CLI Cheat Sheet

| Command | Description |
|---|---|
| `panda` | Launch the interactive terminal TUI |
| `panda ask "<query>"` | One-shot: direct answer, tool call, or task dispatch (`--agents a,b --mode parallel` for a multi-agent run) |
| `panda web` | Start the web console and open the browser automatically (`--lan` to share) |
| `panda voice` | Talk to the node instead of typing (experimental) |
| `panda model` | Multi-model registry: `status`, `add`, `switch`, `test` |
| `panda nodes` | List online devices and capabilities; `nodes trust <id> mesh\|guarded\|open` pins a peer's tier |
| `panda pair` | Pair a new device over the LAN (short code confirmed on both ends) |
| `panda queue` | Inspect pending, running, and review tasks (`--watch` for live updates) |
| `panda approve <id>` | Approve a pending Tier-2 task |
| `panda plan run <file.yaml>` | Multi-stage, multi-device pipelines (`plan example` writes a starter) |
| `panda task board <id>` | Show the sibling-message boards a task reads (plan/parent scope + its children) |
| `panda task artifact <id>` | List or extract a task's output artifact (`--list`, `--file <path>`, `--out <dir>`) |
| `panda session` | List, fork, and resume conversation sessions (`session tree` shows the family) |
| `panda project list` | Manage workspace projects and context |
| `panda card` | Show, rescan, and edit this node's capability card |
| `panda skill` | Browse, import, and install workflow skills (Hub, URL, or file) |
| `panda auth login` | Sign in a model subscription (e.g. `anthropic`) via OAuth |
| `panda mcp` · `panda rpc` | Serve the node's own tools over MCP stdio · NDJSON embedding surface |
| `panda update` | Check for updates or self-update in place (`upgrade` applies directly) |
| `panda doctor` | Diagnose PATH, config, adapters, and database health |
| `panda version` | Print the current version |

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ cmd/panda      Terminal TUI · Web Console · CLI · Voice     │
├─────────────────────────────────────────────────────────────┤
│ askengine      Intent classification · management tools     │
│ entry/plan     Prompt build · multi-stage decomposition     │
│ commander      3-Tier execution: Native · Agent · Manual    │
│ defense        Tiered gating · circuit breakers · anti-loop │
│ memory/skills  Dual-layer memory + self-evolving SKILL.md   │
├─────────────────────────────────────────────────────────────┤
│ sessions       Conversation trees · forking · compaction    │
│ auth/updater   Subscription OAuth (PKCE) · self-update      │
│ scheduler      Capability scoring · latency-weighted routes │
│ trust          Per-peer trust zones (mesh/guarded/open)     │
│ bus/ledger     WS + sealed-UDP transport · session AEAD ·   │
│                Ed25519 identity · capability cards          │
│ artifact       Chunked, resumable output bundles            │
│ security       Sandbox · net allowlists · secret redaction  │
│ storage        Pure Go SQLite (WAL mode)                    │
└─────────────────────────────────────────────────────────────┘
```

---

## 🗺️ Roadmap

| Version | Theme |
|---|---|
| **v0.0.8** (stable baseline) | Single-machine multi-agent orchestration, fully usable: intent classification, dispatch, supervision loop, failover, tiered approval, prompt language policy |
| **v0.0.9** (stable) — "Periapsis" | Hybrid transport/DTN architecture completed: latency-weighted mesh routing, on-wire DTN bundles, token budgets, shadow copies, actuator path, UDP datagram plane with mesh-coordinated NAT hole punching, encrypted payloads, contact plans, Ed25519 node identity, lite build — plus remembered approvals, execution attribution, a web sidebar node chip and everyday CLI polish |
| **v0.0.10** — "Apoapsis" | Reaching outward to the LAN and the edge: discovery with fingerprint-confirmed admission and TOFU key pinning, worktree-traveling delegated file tasks, the clarification loop, actuator dispatch with five reference drivers (serial MCU included), the Pi adapter and a Python-free generic executor — plus session trees with forking, auto-compaction, the `panda rpc` embedding surface, subscription OAuth, signed event/audit chains, OS-level sandboxing, and a steady-state perf round. Relicensed to AGPL-3.0-or-later with dual commercial licensing |
| **v0.0.11** (current stable) — "Rendezvous" | Tightening the mesh itself: negotiated per-session AEAD encryption inside the signed hello, Bluetooth-style `panda pair`, signed discovery beacons and stable `k:<pub>` identities, hard node pins that hold their destination across multi-hop routes, outbox custody that re-delivers verdicts across link flaps, daemon-authority arbitration, a 240 s silence watchdog, negotiated binary data frames — and a partial-mesh data plane that relays context and artifacts along the delegation chain. It also ships the computer-use pair (`hardware:screen_capture`, `hardware:gui_input`; POSIX and Windows `.ps1` drivers, advertised only under a real GUI session), retrievable task artifacts (`panda task artifact`, console preview), and a mesh-only trust rule for display content that follows the bytes through sends, pulls and stage-to-stage input handoffs |
| **v0.0.12** (preview) — "Constellation" | The fleet drawn as one sky, and agents flying in formation: same-project tasks run in parallel through declared path scopes and isolated worktrees under end-to-end delegation bounds; a running task takes a queued note or a live steer, and a round-budgeted park can be continued instead of only accepted or rejected. The web console consolidates into one place to work — queue, plans and projects merge into the Control page, a workspace page draws the whole fleet as a live topology map and dispatches turns straight into a picked thread, and a profile page answers who this node is and how busy it has been. Underneath: cross-process execution leases, context-overflow recovery, a per-model token policy over a catalogue moved to the current generation, PowerShell completion, pprof on the daemon — and a dispensation-attempt lifecycle fence that reroutes lapsed executor custody, orders outbox delivery across link flaps, and keeps stale verdicts from landing on rotated attempts |
| **v0.0.x** — the kernel | **Polish the kernel until every shipped feature is proven.** The Experimental tracks graduate to stable through real-device fleet validation — LAN discovery & fingerprint admission, pairing + per-session AEAD, trust zones, the inter-task message board, session trees/RPC/OAuth, worktree-traveling delegation, the clarification loop, actuator drivers, computer-use, and the voice entry — plus first hardware ignition of the reference drivers, and steady stability, performance and edge-case tuning throughout |
| **v0.1.x** — the assistant | **Specialize into an assistant that does things for you.** A thin desktop shell over the kernel (tray-resident, global hotkey, native notifications, self-update); the dense-mesh layer — sub-main scout→plan in-flight plan recomposition, TURN-style relay fallback for symmetric NAT, message-board debts (multi-device e2e, MCP pull, board view); and a minimal honest "presence" — an unresolved-list collision engine riding the dream cycle that speaks only when it has something worth saying. Commercial-grade quality, boundaries honestly declared |
| **v0.2.x** — self-evolution | **OpenPanda iterates on itself.** Self-modification lands as agent-produced patch sets through a build → gate → human-merge pipeline coexisting with the signed release channel; node-signed self-builds become a third provenance class under the trust zones; `self_evolution.mode` (off → propose → auto) with the last signed release always kept as fallback — the complete agent self-evolution paradigm |

---

## 🔭 Vision

OpenPanda's architecture is designed for large-scale heterogeneous clusters: drone swarm control, communication scheduling for deep-space satellite constellations, and coordinated autonomous-vehicle fleets. These scenarios share one fundamental problem — **every node has different compute, different capabilities, and different jobs, yet all need to be coordinated, scheduled, and supervised together.**

Today, OpenPanda serves a developer's devices and agents. The long-term goal is to extend the same orchestration kernel to autonomous nodes at cluster scale.

---

## 📚 Documentation

The doc set lives under [`docs/`](docs/README.md): [project status](docs/status.md) (what is verified vs. only built), the [install guide](docs/install.md), the [FAQ](docs/faq.md), the [bus protocol](docs/protocol.md) reference, plus design drafts and historical reports. Note that most internals docs are written in Chinese.

---

## 🤝 Contributing

1. Read [CONTRIBUTING.md](CONTRIBUTING.md) for code standards and workflow.
2. Check [SECURITY.en.md](SECURITY.en.md) for security guidelines (the canonical text is the Chinese [SECURITY.md](SECURITY.md)).
3. Abide by the [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
4. Run `make gate` locally before submitting a PR.
5. Non-trivial contributions require CLA assent — see [CLA.md](CLA.md); typo/docs fixes and `adapters/` changes are exempt.

---

## 📄 License

OpenPanda is dual-licensed:

- **Community** — [GNU Affero General Public License v3.0 or later](LICENSE) (AGPL-3.0-or-later)
- **Commercial** — proprietary terms for closed-source embedding or hosted use; see [COMMERCIAL.md](COMMERCIAL.md)

Releases published before the license change remain available under the MIT License.

Use of OpenPanda is subject to the [Terms of Use & Disclaimer](TERMS.md) ([中文](TERMS.zh-CN.md) · [日本語](TERMS.ja.md) · [Español](TERMS.es.md) · [Deutsch](TERMS.de.md)).
