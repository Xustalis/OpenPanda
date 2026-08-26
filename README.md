# 🐼 OpenPanda

**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant

> Any device, any compute, one command.
> A personal task-orchestration assistant that runs across your heterogeneous
> devices as a peer-to-peer network of nodes.

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.26-blue)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-blue)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)
![Status](https://img.shields.io/badge/status-pre--release-yellow)

---

## Table of contents

- [What is OpenPanda?](#what-is-panda)
- [Key features](#key-features)
- [Architecture](#architecture)
- [Installing](#installing)
- [Getting started](#getting-started)
- [Usage](#usage)
- [CLI reference](#cli-reference)
- [Configuration](#configuration)
- [Documentation](#documentation)
- [Testing](#testing)
- [Deployment](#deployment)
- [Tech stack](#tech-stack)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)
- [Acknowledgements](#acknowledgements)

## What is OpenPanda?

OpenPanda is **not another agent CLI** — it is the layer *above* them: the household
manager for every device and every tool you own.

Claude Code, Codex, OpenCode, Grok Build, DeepSeek Harness, OpenClaw, Hermes … each
is a powerful agent on *one* machine.
OpenPanda doesn't compete with them — it **hires** them. Whatever device you speak to
becomes the commander; it answers directly when it can, and when it can't, it routes
the task over your network to whichever node can actually do the work — handing it to
that node's own agents (Claude Code, Codex, …), or straight to the hardware when a
plain device action is all that's needed (a servo doesn't need an LLM).

```
sub-agents (single machine)   agent teams (single machine)   OpenPanda (many machines)
┌──────────────┐              ┌──────────────┐               ┌──────────────────────┐
│ Claude Code  │              │ multi-agent  │               │ heterogeneous fleet  │
│ Codex …      │              │ orchestration│               │ + their agents       │
│              │              │              │               │ + bare hardware      │
└──────────────┘              └──────────────┘               └──────────────────────┘
      upstream of all of them: OpenPanda delegates, they execute
```

In practice: you ask once, from any device, and OpenPanda delegates the task to the
node best suited to run it, streams back the result, and remembers what it learned
for next time — while keeping project work strictly isolated from personal memory,
so your codebase never drifts because "the assistant knows you prefer dark themes".

It is built from the ground up as a **personal** system: no cloud dependency, your
memory stays on your devices, and every node speaks to its peers over direct
WebSocket links you control.

## Key features

- **Heterogeneous node network** — nodes advertise their real capabilities
  (CPU class, shell, agent adapters) via a capability card; the network routes
  each task to the node that can actually do it. Built for laptops, SBCs,
  desktops, and every platform tier in between.
- **Unified entry model** — one prompt in, three intents out: `answer`
  (pure LLM reply), `tool_call` (your tools), `task` (delegated to a node).
  Automatic intent classification with graceful fallback.
- **Three-tier capability execution** — `native` (direct shell execution),
  `agent` (adapter-backed agent, e.g. Claude Code via an Anthropic-compatible
  endpoint), and `manual` (queued for you to approve/run by hand).
- **Superior task review** — after an agent runs, the entry model judges the
  result against the task's success criteria; incomplete work is re-delegated
  to the agent chain (with what remains and the next step) until it is accepted
  or the round budget runs out. Completed reversible tasks finish into
  **done**, while irreversible side effects (pushes, deletes, …) and
  never-satisfied tasks land in **review** for your explicit approval.
- **P2P delegation protocol** — idempotent `task_id` keys and per-attempt
  `attempt_id`s over WebSocket + JSON, so crashed retries never double-execute.
- **Self-evolving skills** — procedural memory in `SKILL.md` files: a skill
  declares when it applies, how it runs, and can be refined after each use.
- **Daily-assistant tools** — the agent can read the system clock, fetch live
  weather, and set **scheduled reminders** (`reminder.set`): stored in SQLite,
  fired by a scanner, delivered as Web Push notifications and live SSE updates
  to any open console. `panda reminder list/add/rm` manages them from the CLI.
- **MCP integration** — one stdio Model Context Protocol server configurable
  in `config.yaml` (`mcp.command`) or the console's settings page; its tools
  join the agent's toolset live, no daemon restart.
- **Two-layer memory** — per-user and per-project memory (`USER.md` /
  `MEMORY.md` style) kept behind an isolation wall, plus a background
  **Dreaming** engine that consolidates daily logs into long-term memory while
  the node is idle.
- **Voice entry** — optional sidecar pipeline (wake word → STT → LLM → TTS),
  hardware-gated and ready for embedded microphones.
- **Interactive REPL + embedded web console** — `panda repl` is the operator's
  seat: bare input goes to the ask engine, slash commands drive the panel
  surfaces (`/tasks`, `/approve`, `/projects`, `/nodes`, `/lang` …), and `/web`
  boots the embedded console in one click. The console's task queue is a
  **kanban board** (to do / in progress / in review / finished) with inline
  approvals, plus chat sessions, reminders, an editable memory page (USER /
  MEMORY / DREAMS), and a settings page (model endpoint — Anthropic or OpenAI
  compatible — and MCP server). `panda web` is the one-command path: loopback
  bind + ephemeral token by default, browser opens already logged in (no
  config, no token paste). Five UI languages: English, 简体中文, 日本語,
  Español, Deutsch.
- **Self-update** — `panda web` (and `/web`) checks the release channel in the
  background; the console downloads and verifies an available update, then
  installs it in one click once the task queue is idle. Discard a downloaded
  update and nothing is left behind.
- **Defense & safety layers** — permission tiers, a circuit breaker, scope-drift
  and infinite-loop detection, plus execution-side hardening: sandboxing,
  network allow-lists, secret redaction, and audit logging.
- **Slim by design** — steady-state RSS ≈ **13–20 MB**, built to live on
  resource-constrained single-board computers.
- **Cross-compiles cleanly** — one static binary per platform, no CGO needed
  (pure-Go SQLite via `modernc.org/sqlite`).

## Architecture

```
                        ┌───────────────────────────┐
                        │  You: CLI / web / voice  │
                        └─────────────┬─────────────┘
                                      │
                 ┌────────────────────▼────────────────────┐
                 │             entry · panda ask            │
                 │   classify:  answer | tool_call | task   │
                 └────────────────────┬────────────────────┘
                                      │  delegate over WebSocket + JSON
                       ┌──────────────┴───────────────┐
                       │                              │
          ┌────────────▼────────────┐     ┌────────────▼────────────┐
          │        Worker node      │     │        Worker node      │
          │  e.g. Laptop (Standard)  │     │    e.g. SBC (Micro)      │
          └─────────────────────────┘     └─────────────────────────┘
```

Inside each node:

```
┌─────────────────────────────────────────────────────────────┐
│ cmd/panda      daemon + CLI (ask / status / queue / task …)  │
├─────────────────────────────────────────────────────────────┤
│ entry          unified entry model (answer · tool_call · task)│
│ scheduler      delegation & routing decisions                │
│ commander      3-tier execution: native · agent · manual     │
│ defense        permission tiers · circuit · drift · loops    │
│ security       sandbox · allow-lists · redaction · audit     │
│ memory         USER/MEMORY stores + Dreaming engine          │
│ skills         SKILL.md procedural memory                    │
├─────────────────────────────────────────────────────────────┤
│ bus            WebSocket transport + message envelope        │
│ ledger         capability directory (cards, heartbeat)       │
│ storage        SQLite (WAL) + migrations                     │
│ log / util     structured JSON logs, UUIDv7                 │
└─────────────────────────────────────────────────────────────┘
```

## Installing

Get a release binary in one line — macOS, Linux, or Windows; consistent
experience, no root required. The installer downloads the matching release
archive, verifies its SHA-256, unpacks the binary plus its agent adapters
(`adapters/*.py`) into a per-user prefix, and links `panda` onto your `PATH`.

| Platform | Command |
|---|---|
| macOS / Linux | `curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh \| sh` |
| macOS (Homebrew) | `brew tap Xustalis/openpanda && brew install openpanda` |
| Windows (PowerShell) | `Set-ExecutionPolicy -Scope Process Bypass`, then `irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 \| iex` |

Override the defaults with flags:

```bash
sh scripts/install.sh --version 0.0.4           # pin a version (default: latest)
sh scripts/install.sh --prefix /opt/openpanda   # custom install directory
sh scripts/install.sh --yes                     # also register auto-start, no prompt
sh scripts/install.sh --no-service              # skip auto-start entirely
```

On macOS / Linux files land under a per-user prefix following the XDG convention:

```
${XDG_DATA_HOME:-~/.local/share}/openpanda/
├── bin/panda            # the real binary
├── adapters/*.py        # agent adapters (the daemon needs these to delegate)
├── config.example.yaml
└── capabilities.example-*.yaml
```

`~/.local/bin/panda` is a symlink to that binary (already on `PATH`); if your
shell doesn't include `~/.local/bin`, the script prints the `export PATH` line to
add. On Windows the files go to `%LOCALAPPDATA%\OpenPanda\` and its `bin` is added
to your user `PATH`. The installer can also register an auto-start service
(`panda daemon` at login) — say yes only after `panda init`, since the daemon
won't start without a config.

After installing, bootstrap and run:

```bash
panda init      # interactive config.yaml + capabilities card
panda doctor    # self-check: binary / PATH / config / adapters / agents
panda repl      # drop into the interactive REPL
panda web       # open the embedded web console (loopback, auto-login)
```

To remove everything:

- macOS / Linux: `rm -rf ~/.local/share/openpanda ~/.local/bin/panda` (and stop any
  auto-start service first).
- Windows: delete `%LOCALAPPDATA%\OpenPanda` and remove its `bin` from the user `PATH`.

Full guide and troubleshooting: [docs/install.md](docs/install.md).

## Getting started

### Prerequisites

| Tool | Version |
|---|---|
| Go | 1.26.5+ |
| Python | 3.10+ (agent adapters / voice sidecar) |
| make | any recent version |

### Build

```bash
make build          # native binary → bin/panda (release, stripped)
make web            # web console into the binary (needs node/npm; skip = console shows a hint page)
make test           # run the full test suite
make vet            # static analysis
```

Cross-compile for the devices you actually run:

```bash
make build-linux-arm64   # → bin/panda-linux-arm64  (SBCs, embedded boards)
make build-linux-amd64   # → bin/panda-linux-amd64
make build-darwin-arm64  # → bin/panda-darwin-arm64
make build-windows-amd64 # → bin/panda-windows-amd64.exe
```

### Configure

Bootstrap a node interactively — the model endpoint, node name, and a
capabilities card are generated from one prompt:

```bash
./bin/panda init
```

Or copy the example config and edit it for each node:

```bash
cp config.example.yaml /etc/openpanda/config.yaml   # or keep it local and use --config
```

The config is small and self-explanatory. The two things that matter most:

```yaml
network:
  listen_addr: ":7836"        # WebSocket listener
  shared_secret: "..."        # HMAC auth between nodes — all nodes must share the same value
  peers:                      # other nodes in your network
    - "worker-1.your-tailnet.ts.net:7836"
model:
  base_url: "https://api.deepseek.com/anthropic"  # any /v1/messages-compatible endpoint
  model: "deepseek-chat"
  # api_key: ""               # prefer the OPENPANDA_MODEL_API_KEY env var
```

Node identity rules are enforced at daemon startup. A physical host may run
only one `physical` node, regardless of its display name. A virtual machine
may run as an additional `vm` node; set a stable `node.identity` for each VM.
The OS lock is keyed by physical-host identity or VM identity, so an ordinary
second process on the same device is rejected while host plus VM is allowed.

Secrets (model API keys) are read from `OPENPANDA_MODEL_API_KEY` rather than the
config file whenever possible.

### Run the daemon

The fastest way to see the whole system is the one-command web console:
loopback bind, ephemeral token, browser opens already logged in — no config
editing, no token pasting:

```bash
./bin/panda web
```

The console also manages the model endpoint (Anthropic- or OpenAI-compatible)
from its settings page if you haven't configured one yet.

For a resident multi-node setup, run the daemon itself:

```bash
./bin/panda daemon --config config.yaml --card config/capabilities.example-desktop.yaml
```

Each node that can *execute* work should be started with its capability card.
A node without a card still participates in heartbeats, but it won't be routed work.

## Usage

Ask anything — the entry model decides whether to answer, call a tool, or delegate:

```bash
./bin/panda ask --card config/capabilities.example-desktop.yaml "summarize the git log for the last week"
```

> `--card` points at this node's capability card; answer/tool_call work without it, but a delegated-task output is refused without it.

Inspect the network and queue:

```bash
./bin/panda status
./bin/panda queue
```

Drill into or cancel a specific task:

```bash
./bin/panda task <task-id>
./bin/panda cancel <task-id>
./bin/panda logs <task-id>
```

Manage skills:

```bash
./bin/panda skill
```

## CLI reference

| Command | Description |
|---|---|
| `panda` (no args) | Open the interactive REPL (same as `panda repl`); the daemon now runs via the `panda daemon` subcommand |
| `panda daemon [--config PATH] [--card PATH]` | Run the daemon: register node, heartbeat, WS server, peer reconnect |
| `panda ask [--config PATH] [--card PATH] [--authorize] "<question>"` | Unified entry: classify into answer / tool_call / task / plan and execute |
| `panda plan run <file.yaml> \| show <id> \| example` | Multi-stage cross-device pipeline: a stage IS an ordinary task (queues, routes by hardware, retries, parks in review), the plan adds the ordering and hands each stage's output dir to the next machine's stage; `run --dry-run` validates and prints the routing without creating anything |
| `panda voice [--once] [--mute]` | Desk-pet entry: wake word → ASR → the same entry pipeline → TTS, for a device with no keyboard; `--once` handles a single utterance, `--mute` prints instead of speaking |
| `panda repl [--config PATH] [--card PATH]` | Interactive shell: slash commands (tasks/approve/projects/nodes/lang), bare input goes to the ask engine, `/web` boots the embedded console |
| `panda web [--config PATH] [--card PATH] [--no-browser]` | One-command web console: loopback + ephemeral token by default, opens the browser already logged in |
| `panda init` | Interactive first-run setup: generates `config.yaml` + `capabilities.yaml` (model endpoint, node name, hardware-scan defaults) |
| `panda install [--dir PATH] [--no-path]` | Register `panda` globally on PATH (persistent across reboots) and self-verify the installed copy |
| `panda uninstall [--config PATH] [--yes] [--no-backup] [--dry-run]` | Safe removal: full plan first, `confirm` required, whitelist-only deletion, user assets (projects/memory/skills) always kept, zip backup + report |
| `panda doctor [--config PATH]` | Self-check: installed copy runs, PATH resolves, persistence survives reboot, config/database usable |
| `panda status` | Node & task status |
| `panda queue` | List the task queue |
| `panda task [--config PATH] <task-id>` | Task details |
| `panda cancel [--config PATH] <task-id>` | Cancel a task (cascades to the executing node) |
| `panda approve [--config PATH] <task-id>` | Approve a reviewed task (review → done) |
| `panda reject [--config PATH] [--reason s] <task-id>` | Reject a reviewed task |
| `panda logs [--config PATH] <task-id>` | Task execution logs |
| `panda skill` | Skill store management |
| `panda reminder list \| add \| rm` | Scheduled reminders: list / add (`--after 10m` or `--at "2006-01-02 15:04"`) / remove |
| `panda detect [-o PATH]` | Scan this machine's hardware (CPU/RAM/GPU/agent CLIs) into a capabilities.yaml draft |
| `panda card show \| rescan \| edit \| set` | This node's capability card: print it (and which file it came from), re-scan hardware + installed agent CLIs (`rescan` prints a diff, `--write` applies it and keeps a `.bak`), open it in `$EDITOR`, or `set <field>=<value>` headlessly. Probed hardware is overwritten, hand-written decisions (device name, resource_class, max_concurrent_tasks, agent tiers, native/manual abilities) are preserved |
| `panda agents [list]` | Probe the agent CLIs on PATH (Codex, Claude Code, OpenCode, Grok Build, DeepSeek Harness, OpenClaw, Hermes); `test <name>` checks one, `install|update <name>` prints its install command + download link |
| `panda metrics [--csv]` | Export delegation metrics |
| `panda audit [--task <id>]` | Verify the `prev_hash` chain of the audit log or one task's events |
| `panda version` / `panda help` | Print version / command overview |

## Configuration

| Section | Key | Meaning |
|---|---|---|
| `node` | `name` | Unique node ID (used across the network) |
| `node` | `resource_class` | `Micro` \| `Standard` \| `Full` → scheduler tier |
| `node` | `kind` | `physical` (default) or `vm`; controls the local singleton rule |
| `node` | `identity` | Stable VM identity; physical nodes use the host fingerprint and ignore this override |
| `network` | `listen_addr` | WebSocket listener address |
| `network` | `shared_secret` | HMAC secret authenticating node-to-node hellos; the WS listener refuses to start without it (all nodes share one value) |
| `network` | `max_connections` | Global concurrent WS connection limit (0 = unlimited) |
| `network` | `max_connections_per_ip` | Per-remote-IP concurrent WS connection limit (0 = unlimited) |
| `network` | `panel_addr` | Web console HTTP address (`panda web` / `/web`); default `127.0.0.1:7840` |
| `network` | `panel_token` | Bearer token guarding the console's `/api/*` (loopback auto-generates an ephemeral one; prefer `OPENPANDA_PANEL_TOKEN`) |
| `network` | `peers` | Manual peer addresses to dial |

Use `panda nodes` (or `panda status`) to inspect node kind, local/remote
placement and running state. `panda status --running` shows only nodes with a
fresh runtime heartbeat; the Web Nodes page exposes the same data through
`/api/self` and `/api/nodes`. Stale rows (a renamed machine, a peer whose
identity changed, a decommissioned node) can be dropped with `panda nodes
remove <id>` or the console's Remove button — the local node's own row and
online nodes are refused, since both re-register themselves.
| `storage` | `db_path` | SQLite database path |
| `storage` | `context_path` | Context snapshot store |
| `storage` | `memory_path` | Personal memory root |
| `storage` | `projects_path` | Per-project memory root |
| `storage` | `skills_path` | Procedural-memory root |
| `storage` | `work_path` | Where agents execute; scope drift is measured here |
| `log` | `level` | `debug` \| `info` \| `warn` \| `error` |
| `model` | `base_url` | Anthropic- or OpenAI-compatible API base URL |
| `model` | `model` | Model id (e.g. `deepseek-chat`, `deepseek-reasoner`) |
| `model` | `api_type` | `anthropic` \| `openai` (default `anthropic`) |
| `model` | `api_key` | Secret — prefer `OPENPANDA_MODEL_API_KEY` |
| `model` | `max_tokens` | Completion cap (default 4096) |
| `mcp` | `command` | stdio MCP server argv (empty = disabled); tools hot-load into the agent toolset |
| `push` | `enabled` | Serve `/api/push/*` and send Web Push (embedded console + webui sidecar) |
| `push` | `vapid_subject` | VAPID subject (e.g. `mailto:` address) |
| `push` | `vapid_key_path` | VAPID key path (auto-generated on first boot) |

Config load order: `--config` flag > environment > default `/etc/openpanda/config.yaml`.

## Documentation

Full documentation lives in the [`docs/`](docs/) directory:

- [Documentation index](docs/README.md) — entry point for the public docs.
- [Contributing guide](CONTRIBUTING.md) — toolchain, engineering gates,
  code conventions, and the PR checklist (translations available in
  `CONTRIBUTING.zh-CN.md` / `CONTRIBUTING.ja.md` / `CONTRIBUTING.es.md` /
  `CONTRIBUTING.de.md`).
- [Desktop & packaging roadmap](docs/plans/roadmap-desktop-and-packaging.md) —
  staged plan toward a native desktop client, signed installers, notarization,
  and auto-updates.

## Testing

```bash
make test        # full suite
make vet         # go vet
```

Key protocol invariants are covered by real two-node WebSocket tests
(no Tailscale required):

```bash
go test ./internal/core/ -run 'TestTwoNodeProtocol|TestDelegateIdempotent|TestCancelPropagates' -v
```

## Deployment

### Network security baseline

OpenPanda nodes speak plain WebSocket (`ws://`) by default. **Plain WebSocket must
only be used over a trusted private path:**

- Loopback / same-host links (e.g. `127.0.0.1`, `localhost`).
- A private overlay network you control, such as **Tailscale** or a VPN.
- A physically isolated LAN where every device is trusted.

**If any OpenPanda peer crosses the public Internet, terminate TLS in front of the
WebSocket listener** (e.g. nginx, Caddy, Traefik) and configure peers with the
`wss://` URL. The `shared_secret` authenticates node-to-node hellos, but it is
*not* a substitute for transport encryption — do not expose a plain `ws://`
listener on the public Internet.

The `panel_addr` web console serves plain HTTP and carries a Bearer token (an
ephemeral one is auto-generated on loopback). Keep it on loopback, or put it
behind the same TLS reverse proxy.

### Memory footprint

OpenPanda targets low-power devices. Verify your steady-state memory before shipping
to hardware — a single `ps` sample is unreliable due to GC noise; take several:

```bash
make build
for i in 1 2 3 4 5; do
  ./bin/panda daemon --config testdata/node-a.yaml >/dev/null 2>&1 &
  PID=$!; sleep 3
  ps -o rss= -p $PID | awk '{printf "%d MB\n", $1/1024}'
  kill -TERM $PID; wait $PID 2>/dev/null
done
```

## Tech stack

| Layer | Choice |
|---|---|
| Core daemon | Go (modernc.org/sqlite — pure Go, no CGO) |
| Glue / adapters | Python 3.10+ |
| Transport | WebSocket + JSON envelopes |
| State | SQLite in WAL mode |
| Frontend | Web console (Vite + Preact, `go:embed` single binary) via `panda repl` → `/web`, or the standalone `webui/` sidecar |
| LLM access | Anthropic-compatible `/v1/messages` or OpenAI-compatible endpoints (e.g. DeepSeek) |

## Roadmap

Phases 0–3 (entry model · P2P delegation · memory + voice + execution
hardening · kernel/console/REPL rebuild + live two-node verification) are
complete. Phase 4 (desktop client + signed installer pipeline + auto-update
mechanism + release channels) is planned in detail in the
[desktop & packaging roadmap](docs/plans/roadmap-desktop-and-packaging.md).

## Contributing

Contributions are welcome. To keep the codebase consistent, please follow the
engineering gates before opening a pull request:

- `make vet && make test` must pass.
- `gofmt -l internal/ cmd/ adapters/` must be empty.
- Keep core-module test coverage above ~60% where practical.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full conventions: error
wrapping (`%w` / `errors.Is`), complexity limits, no dead code, concurrency
rules, i18n rules, and the commit style. Translated editions are available in
[`CONTRIBUTING.zh-CN.md`](CONTRIBUTING.zh-CN.md),
[`CONTRIBUTING.ja.md`](CONTRIBUTING.ja.md),
[`CONTRIBUTING.es.md`](CONTRIBUTING.es.md), and
[`CONTRIBUTING.de.md`](CONTRIBUTING.de.md).

## License

Released under the [MIT License](LICENSE).

## Acknowledgements

Inspired by distributed multi-agent scheduling theory and by the
memory patterns of Hermes and OpenClaw. Built by Xenith.
