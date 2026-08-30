# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

OpenPanda (Open Personal Adaptive Node-based Distributed Assistant) is a peer-to-peer task-orchestration assistant that runs across heterogeneous devices. It sits above individual agent CLIs (Claude Code, Codex, etc.) and delegates tasks to the best-suited node in the network.

## Build & Test Commands

```bash
# Build the native binary (release, stripped symbols)
make build

# Build the web console into the binary (requires node/npm)
make web

# Run the full test suite
make test

# Run tests with race detector
make race

# Race detector scoped to the concurrency-sensitive packages (storage, core, bus, panel)
make race-focused

# Static analysis
make vet

# Format check (gofmt) / format all Go code
make fmt-check
make fmt

# Merge gate: fmt-check + vet + build + test + race (must pass before PR lands)
make gate

# The full gate: gate + web-test + web (web console tests and embedded build)
make gate-all

# Web console tests (node --test) / build the web console into the binary
make web-test
make web

# Cross-compile targets
make build-darwin-amd64
make build-darwin-arm64
make build-linux-arm64
make build-linux-amd64
make build-windows-amd64
make build-windows-arm64

# Package release archives (dist/) / local packaging
make package
make release-local

# Run a single test
go test -run TestName ./path/to/package/...

# Quick-start: build and open web console with config.yaml
make dev

# Run the daemon directly
make run

# Measure steady-state RSS
make measure
```

## Architecture

The codebase is a Go 1.26+ monorepo with pure-Go SQLite (no CGO).

### Entry Points
- `cmd/panda/` — CLI entry point with subcommands: `daemon`/`serve`, `ask`, `repl`/`chat`, `web`, `voice`, `install`, `uninstall`, `doctor`, `status`, `nodes` (`add`/`invite`/`disconnect`/`remove`), `pair`, `queue`, `task` (`add`/`priority`/`move`), `plan`, `cancel`, `approve`, `reject`, `logs`, `skill`, `reminder`, `detect`, `card` (`show`/`rescan`/`edit`/`set` + `native`/`agent`/`manual`), `init`, `metrics`, `audit` (`verify`/`entries`), `session`/`sessions`, `memory`, `config`, `agents`, `project`, `version`, `help`
- `webui/cmd/panel/` — Web console sidecar (embeds the Preact app via go:embed)

### Core Packages (`internal/`)

**Node Lifecycle & Task Orchestration:**
- `core/` — Root daemon type (`Core`) wiring node lifecycle, task store, and WebSocket transport. Contains the task execution loop, delegation, retry, supervision (judge → re-delegate), and metrics.
- `entry/` — Unified entry model: classifies user input into `answer` (LLM reply), `tool_call` (tool invocation), `task` (delegated to a node), or `plan` (multi-stage pipeline whose stages run on different machines). Handles prompt building, streaming, tool routing, and output parsing.
- `scheduler/` — Task routing and scoring. `queue/` manages the task queue; `route.go` picks the best node; `score.go` ranks candidates; `chain.go` handles multi-step delegation chains.
- `commander/` — Three-tier execution: `native` (direct shell via `executil`), `agent` (adapter-backed, e.g. Claude Code), `manual` (queued for human approval). `adapter.go` manages agent adapters; `native.go` runs shell commands; `inject.go` injects memory/skills into agent context.

**Transport & Discovery:**
- `bus/` — WebSocket transport with HMAC auth (`auth.go`), message envelope (`msg.go`), and payloads (`payloads.go`).
- `ledger/` — Capability directory: `card.go` defines node capability cards; `capability.go` manages the card registry; `card_load_test.go` tests card parsing.

**Safety & Defense:**
- `defense/` — Permission tiers (`permission.go`), circuit breaker (`circuit.go`), scope-drift detection (`scope.go`), infinite-loop detection (`loop.go`), and state snapshots (`snapshot.go`).
- `security/` — Sandboxing, network allow-lists, secret redaction, audit logging.

**Memory & Skills:**
- `memory/` — Two-layer memory (USER.md/MEMORY.md) with isolation wall (`isolation.go`), daily task logging (`daily.go`), Dreaming engine (`dream.go`, `dream_scheduler.go`, `dream_diary.go`) for consolidating logs into long-term memory, and memory injection into agent context (`injector.go`).
- `skills/` — SKILL.md procedural memory with progressive loading and self-evolution tracking.

**Infrastructure:**
- `storage/` — SQLite with WAL mode (`sqlite.go`) and migrations (`migrate.go`, `migrations.go`).
- `config/` — YAML configuration loading.
- `log/` — Structured JSON logging.
- `util/` — Shared utilities (UUIDv7, etc.).
- `version/` — Build version info.
- `hwinfo/` — Hardware detection for capability cards.
- `i18n/` — Internationalization (5 languages: EN, ZH, JA, ES, DE).
- `mcp/` — Model Context Protocol stdio server integration.
- `reminders/` — Scheduled reminders stored in SQLite, fired by scanner, delivered via Web Push and SSE.
- `updater/` — Self-update mechanism.
- `install/` — Installation logic (path management, service registration).

### Configuration
- `config.example.local.yaml` — Example config file
- `config/capabilities.example-*.yaml` — Example capability cards for different node tiers (desktop, edge)
- Key config sections: `network` (listen addr, shared secret, peers), `model` (base URL, model name, API key)

### Testing
- Tests are co-located with source files (`*_test.go`)
- `testdata/` contains test fixtures (`node-a.yaml`, `node-b.yaml`, `deploy-opi.yaml`)
- Use `make gate` to run the full merge gate before PRs
- The `internal/core/` package has extensive integration tests (`e2e_test.go`)

### Agent Adapters
- `adapters/` — Python scripts for agent adapters (Claude Code, Codex, etc.)
- Adapters are installed alongside the binary and needed for agent-tier execution

### Web Console
- `webui/app/` — Preact frontend (built with npm/vite)
- `webui/panel/` — Go sidecar that embeds the built frontend via go:embed
- `webui/push/` — Web Push notification support
- Build with `make web` (requires node/npm)
