# 🐼 OpenPanda

**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant

> Any device, any compute, one command.
> A personal task-orchestration assistant that runs across your heterogeneous
> devices as a peer-to-peer network of nodes.

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
![Go](https://img.shields.io/badge/Go-%E2%89%A51.22-blue)
![Python](https://img.shields.io/badge/Python-%E2%89%A53.10-blue)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)

---

## Table of contents

- [What is OpenPanda?](#what-is-panda)
- [Key features](#key-features)
- [Architecture](#architecture)
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

OpenPanda turns every device you own — a laptop, a single-board computer, a desktop —
into a *node* in a personal task network. You ask once, from any device, and OpenPanda
delegates the task to the node best suited to run it, streams back the result, and
remembers what it learned for next time.

It is built from the ground up as a **personal** system: no cloud dependency, your
memory stays on your devices, and every node speaks to its peers over direct
WebSocket links you control.

## Key features

- **Heterogeneous node network** — nodes advertise their real capabilities
  (CPU class, shell, agent adapters) via a capability card; the network routes
  each task to the node that can actually do it. Built for MacBook ↔ Orange Pi 3B
  and everything in between.
- **Unified entry model** — one prompt in, three intents out: `answer`
  (pure LLM reply), `tool_call` (your tools), `task` (delegated to a node).
  Automatic intent classification with graceful fallback.
- **Three-tier capability execution** — `native` (direct shell execution),
  `agent` (adapter-backed agent, e.g. Claude Code via an Anthropic-compatible
  endpoint), and `manual` (queued for you to approve/run by hand).
- **P2P delegation protocol** — idempotent `task_id` keys and per-attempt
  `attempt_id`s over WebSocket + JSON, so crashed retries never double-execute.
- **Self-evolving skills** — procedural memory in `SKILL.md` files: a skill
  declares when it applies, how it runs, and can be refined after each use.
- **Two-layer memory** — per-user and per-project memory (`USER.md` /
  `MEMORY.md` style) kept behind an isolation wall, plus a background
  **Dreaming** engine that consolidates daily logs into long-term memory while
  the node is idle.
- **Voice entry** — optional sidecar pipeline (wake word → STT → LLM → TTS),
  hardware-gated and ready for embedded microphones.
- **Interactive REPL + embedded web console** — `panda repl` is the operator's
  seat: bare input goes to the ask engine, slash commands drive the panel
  surfaces (`/tasks`, `/approve`, `/projects`, `/nodes`, `/lang` …), and `/web`
  boots the embedded console (queue, ask, projects, nodes, approvals) in one
  click. `panda web` is the one-command path: loopback bind + ephemeral token
  by default, browser opens already logged in (no config, no token paste).
  Five UI languages: English, 简体中文, 日本語, Español, Deutsch. The same
  console also ships as a standalone `webui/` sidecar.
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
                        │     You: CLI / voice     │
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
          │   e.g. MacBook (Full)   │     │   e.g. Orange Pi (Micro) │
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

## Getting started

### Prerequisites

| Tool | Version |
|---|---|
| Go | 1.22+ (module targets 1.26.5) |
| Python | 3.10+ (agent adapters / voice sidecar) |
| make | any recent version |

### Build

```bash
make build          # native binary → bin/panda (release, stripped)
make test           # run the full test suite
make vet            # static analysis
```

Cross-compile for the devices you actually run:

```bash
make build-linux-arm64   # → bin/panda-linux-arm64  (e.g. Orange Pi)
make build-linux-amd64   # → bin/panda-linux-amd64
make build-darwin-arm64  # → bin/panda-darwin-arm64
make build-windows-amd64 # → bin/panda-windows-amd64.exe
```

### Configure

Copy the example config and edit it for each node:

```bash
cp config.example.yaml /etc/openpanda/config.yaml   # or keep it local and use --config
```

The config is small and self-explanatory. The two things that matter most:

```yaml
network:
  listen_addr: ":7836"        # WebSocket listener
  shared_secret: "..."        # HMAC auth between nodes — all nodes must share the same value
  peers:                      # other nodes in your network
    - "orangepi3b.tailnet.ts.net:7836"
model:
  base_url: "https://api.deepseek.com/anthropic"  # any /v1/messages-compatible endpoint
  model: "deepseek-chat"
  # api_key: ""               # prefer the OPENPANDA_MODEL_API_KEY env var
```

Secrets (model API keys) are read from `OPENPANDA_MODEL_API_KEY` rather than the
config file whenever possible.

### Run the daemon

For local development, the fastest way is the provided helper, which builds
both binaries if needed, generates local tokens, and starts the daemon plus
the optional webui sidecar:

```bash
make run-local
```

Then open `http://127.0.0.1:7840` and use the printed Bearer token for the
`/api/*` endpoints.

For manual or multi-node deployment:

```bash
./bin/panda --config config.yaml --card config/capabilities.macbook.yaml
```

Each node that can *execute* work should be started with its capability card.
A node without a card still participates in heartbeats, but it won't be routed work.

## Usage

Ask anything — the entry model decides whether to answer, call a tool, or delegate:

```bash
./bin/panda ask --card config/capabilities.macbook.yaml "summarize the git log for the last week"
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
| `panda` (no args) | Run the daemon: register node, heartbeat, WS server, peer reconnect |
| `panda ask [--config PATH] [--card PATH] [--authorize] "<question>"` | Unified entry: classify into answer / tool_call / task and execute |
| `panda repl [--config PATH] [--card PATH]` | Interactive shell: slash commands (tasks/approve/projects/nodes/lang), bare input goes to the ask engine, `/web` boots the embedded console |
| `panda web [--config PATH] [--card PATH] [--no-browser]` | One-command web console: loopback + ephemeral token by default, opens the browser already logged in |
| `panda status` | Node & task status |
| `panda queue` | List the task queue |
| `panda task [--config PATH] <task-id>` | Task details |
| `panda cancel [--config PATH] <task-id>` | Cancel a task (cascades to the executing node) |
| `panda approve [--config PATH] <task-id>` | Approve a reviewed task (review → done) |
| `panda reject [--config PATH] [--reason s] <task-id>` | Reject a reviewed task |
| `panda logs [--config PATH] <task-id>` | Task execution logs |
| `panda skill` | Skill store management |
| `panda metrics [--csv]` | Export delegation metrics |
| `panda audit [--task <id>]` | Verify the `prev_hash` chain of the audit log or one task's events |
| `panda version` | Print version |

## Configuration

| Section | Key | Meaning |
|---|---|---|
| `node` | `name` | Unique node ID (used across the network) |
| `node` | `resource_class` | `Micro` \| `Standard` \| `Full` → scheduler tier |
| `network` | `listen_addr` | WebSocket listener address |
| `network` | `shared_secret` | HMAC secret authenticating node-to-node hellos; the WS listener refuses to start without it (all nodes share one value) |
| `network` | `max_connections` | Global concurrent WS connection limit (0 = unlimited) |
| `network` | `max_connections_per_ip` | Per-remote-IP concurrent WS connection limit (0 = unlimited) |
| `network` | `panel_addr` | Optional webui sidecar HTTP address (kernel ignores it) |
| `network` | `panel_token` | Bearer token guarding the sidecar's `/api/*` (prefer `OPENPANDA_PANEL_TOKEN`) |
| `network` | `peers` | Manual peer addresses to dial |
| `storage` | `db_path` | SQLite database path |
| `storage` | `context_path` | Context snapshot store |
| `storage` | `memory_path` | Personal memory root |
| `storage` | `projects_path` | Per-project memory root |
| `storage` | `skills_path` | Procedural-memory root |
| `storage` | `work_path` | Where agents execute; scope drift is measured here |
| `log` | `level` | `debug` \| `info` \| `warn` \| `error` |
| `model` | `base_url` | Anthropic-compatible Messages API base URL |
| `model` | `model` | Model id (e.g. `deepseek-chat`, `deepseek-reasoner`) |
| `model` | `api_key` | Secret — prefer `OPENPANDA_MODEL_API_KEY` |
| `model` | `max_tokens` | Completion cap (default 4096) |
| `push` | `enabled` | Serve `/api/push/*` and send Web Push (webui sidecar only) |
| `push` | `vapid_subject` | VAPID subject (e.g. `mailto:` address) |
| `push` | `vapid_key_path` | VAPID key path (auto-generated on first boot) |

Config load order: `--config` flag > environment > default `/etc/openpanda/config.yaml`.

## Documentation

Full documentation lives in the [`docs/`](docs/) directory, which is split into
public and internal parts:

- [Documentation index](docs/README.md) — entry point for all documents.
- [Contributing guide](CONTRIBUTING.md) — toolchain, engineering gates,
  code conventions, and the PR checklist.
- [Desktop & packaging roadmap](docs/plans/roadmap-desktop-and-packaging.md) —
  the staged plan toward the desktop client.
- [Phase reports](docs/reports/) — progress reports for each phase and sprint.

Internal planning, design, and audit documents are kept out of the public
repository.

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

The optional `panel_addr` serves plain HTTP for the legacy PWA sidecar. Keep it
on loopback, or put it behind the same TLS reverse proxy.

### Memory footprint

OpenPanda targets low-power devices. Verify your steady-state memory before shipping
to hardware — a single `ps` sample is unreliable due to GC noise; take several:

```bash
make build
for i in 1 2 3 4 5; do
  ./bin/panda --config testdata/mac-config.yaml >/dev/null 2>&1 &
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
| LLM access | Anthropic-compatible `/v1/messages` endpoint (e.g. DeepSeek) |

## Roadmap

Phase 3 (memory + voice + safety) is complete. The memory layer, Dreaming
engine, skills system, and execution hardening are implemented; voice entry is
code-complete and waiting on microphone-hardware validation. The web console
has been rebuilt (Vite + Preact, embedded in the binary) and the interactive
REPL landed with it — two-node delegation is verified live
([report](docs/reports/delegation-loopback-2026-08-18.md)). Phase 4 (desktop
client and beyond) is planned in the
[desktop & packaging roadmap](docs/plans/roadmap-desktop-and-packaging.md).

## Contributing

Contributions are welcome. To keep the codebase consistent, please follow the
engineering gates before opening a pull request:

- `make vet && make test` must pass.
- `gofmt -l internal/ cmd/ adapters/` must be empty.
- Keep core-module test coverage above ~60% where practical.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full conventions: error
wrapping (`%w` / `errors.Is`), complexity limits, no dead code, concurrency
rules, i18n rules, and the commit style.

## License

Released under the [MIT License](LICENSE).

## Acknowledgements

Inspired by distributed multi-agent scheduling theory (ATC-MARL) and by the
memory patterns of Hermes and OpenClaw. Built by Xenith.
