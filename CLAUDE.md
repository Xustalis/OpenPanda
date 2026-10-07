# CLAUDE.md

Working notes for AI agents contributing to this repository.

## What this is

OpenPanda (Open Personal Adaptive Node-based Distributed Assistant) is a
peer-to-peer orchestrator that runs on heterogeneous machines and farms
work out to whichever node is best suited. It is not itself an agent —
it coordinates the agent CLIs already on each device (Claude Code,
Codex, and friends) plus direct shell execution and human-approved
tasks.

Requires Go 1.26+. The build deliberately avoids CGO: even SQLite comes
from `modernc.org/sqlite`, so cross-compilation is just `GOOS/GOARCH`.

## Licensing

Dual-licensed: **AGPL-3.0-or-later** (see `LICENSE`) for open-source use,
plus a commercial license for proprietary embedding/hosting. All external
contributions are governed by `CLA.md` — new PRs require CLA assent
before merge. Keep new files consistent with the existing
`// SPDX-License-Identifier: AGPL-3.0-or-later` headers.

## Build, test, ship

Everything goes through the Makefile:

```bash
make build        # native binary (fmt-check + vet run first as prerequisites)
make test         # unit/integration suite
make race         # race detector, whole tree
make race-focused # race detector on concurrency-heavy pkgs only (bus/core/storage/panel)
make vet          # go vet
make fmt / fmt-check
make gate         # pre-merge bar: fmt + vet + build + adapter-test + test + race-focused
make gate-all     # gate plus web console tests/build and TUI PTY tests
make bench        # routing/codec/dedup microbenchmarks
make dev          # build + launch web console against config.yaml
make run          # run daemon from source
make measure      # steady-state RSS measurement

# release machinery
make build-<os>-<arch>   # darwin/linux/windows x amd64/arm64 (+ linux-armv7 lite)
make build-lite          # reduced-footprint builds for small devices
make package             # release archives into dist/
make release / release-local

# single test
go test -run TestName ./internal/<pkg>/...
```

The web console needs node/npm only when rebuilding it (`make web`,
`make web-test`); the Go binary embeds a prebuilt copy.

## Layout

- `cmd/panda/` — the `panda` CLI. Bare invocation drops into the TUI
  REPL; subcommands cover `daemon`/`serve`, `ask`, `repl`/`chat`, `web`,
  `voice`, `install`/`uninstall`, `doctor`, `status`, `nodes`, `pair`,
  `queue`, `task`, `plan`, `cancel`, `approve`/`reject`, `logs`, `skill`,
  `reminder`, `detect`, `card`, `init`, `metrics`, `audit`, `session`
  (incl. `fork`/`tree` for thread branching), `memory`, `config`, `agents`,
  `project`, `mcp`, `rpc` (NDJSON-over-stdio embedding surface,
  experimental), `auth` (subscription OAuth, experimental), `version`.
- `internal/` — all runtime code (see below).
- `adapters/` — Python scripts, one per agent CLI, plus `_harness.py`
  (the shared stdin/stdout JSON protocol they all speak). Installed next
  to the binary; pure stdlib, no pip deps.
- `webui/app/` — Preact console (vite build, embedded via go:embed into
  `webui/panel/`; `webui/cmd/panel/` is the sidecar binary).
- `config/` + `config.example*.yaml` — config schema and sample node
  cards. `testdata/` holds `node-a.yaml`/`node-b.yaml`/`deploy-opi.yaml`
  fixtures.
- `deploy/`, `drivers/`, `scripts/` — deployment manifests, hardware
  profiles, install scripts.
- `docs/` — design docs and review reports.

### `internal/` by concern

- **Node & task lifecycle**: `core` (the `Core` daemon — wires store,
  transport, execution loop, delegation/retry/judge, metrics), `entry`
  (classifies input into answer/tool_call/task/plan, builds prompts,
  streams output), `scheduler` (queue, node scoring `score.go`, routing
  `route.go`, multi-hop `chain.go`), `commander` (three execution tiers:
  `native` shell via `executil`, `agent` via adapters, `manual` approval
  queue; `inject.go` enriches agent context).
- **Wire & discovery**: `bus` (WebSocket + HMAC auth, envelopes,
  payloads), `ledger` (capability cards — what each node advertises),
  `nodeidentity`, `carddetect`, `cardmut`.
- **Safety**: `defense` (permission tiers, circuit breaker, scope-drift
  and loop detection, snapshots), `security` (sandbox, net allowlists,
  secret redaction, audit log), `guard`.
- **Memory & skills**: `memory` (USER.md/MEMORY.md layers, isolation
  wall, daily logs, Dreaming consolidation engine, context injector),
  `skills` (SKILL.md progressive loading + self-evolution).
- **Data & platform**: `storage` (SQLite WAL + migrations), `ctxstore`,
  `sessions`, `projects`, `config`, `log`, `util` (UUIDv7 etc.),
  `version`, `hwinfo`, `install`, `i18n` (EN/ZH/JA/ES/DE), `reminders`,
  `updater`, `doctor`, `cliui`, `providers`, `pyexec`, `askengine`,
  `artifact`, `mdtext`, `mcp`/`mcpserve`, `agents`, `plan`, `auth`
  (subscription-OAuth token store + PKCE flows).

## Conventions

- Tests sit next to the code (`foo_test.go`); `internal/core/e2e_test.go`
  is the heavyweight integration suite.
- Before opening a PR run `make gate` — it is what CI enforces.
- User-facing strings go through `internal/i18n` and
  `webui/app/src/i18n/` — all five locales are maintained in parallel.
- New agent adapters plug into `adapters/` and speak the `_harness.py`
  wire contract; `make adapter-test` exercises them.
- Never log secrets; `internal/security` owns redaction — route new
  secret-handling through it rather than reinventing.
