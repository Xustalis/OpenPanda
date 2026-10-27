# CLAUDE.md

Working notes for AI agents contributing to this repository.

## What this is

OpenPanda (Open Personal Adaptive Node-based Distributed Assistant) is a
peer-to-peer orchestrator that runs on heterogeneous machines and farms
work out to whichever node is best suited. It is not itself an agent —
it coordinates the agent CLIs already on each device (Claude Code,
Codex, and friends) plus direct shell execution and human-approved
tasks.

Requires Go 1.26.10+ (the patch level is a security floor: 1.26.10 is where GO-2026-6809, reachable through crypto/x509 in the bus TLS path, is fixed). The build deliberately avoids CGO: even SQLite comes
from `modernc.org/sqlite`, so cross-compilation is just `GOOS/GOARCH`.

## Licensing

Dual-licensed: **AGPL-3.0-or-later** (see `LICENSE`) for open-source use,
plus a commercial license for proprietary embedding/hosting. All external
contributions are governed by `CLA.md` — new PRs require CLA assent
before merge. Use of the software is additionally governed by
`TERMS.md`/`TERMS.zh-CN.md` (warranty disclaimer, liability cap,
indemnity — wired in via AGPL §7 additional terms). Keep new files
consistent with the existing
`// SPDX-License-Identifier: AGPL-3.0-or-later` headers.

## Build, test, ship

Everything goes through the Makefile:

```bash
make build        # native binary (fmt-check + vet run first as prerequisites)
make test         # unit/integration suite (adapter-test runs first as a prerequisite)
make race         # race detector, whole tree (pre-release superset check)
make race-focused # race detector on concurrency-heavy pkgs only (core/commander/bus/storage/askengine/scheduler/webui-panel)
make vet          # go vet
make fmt / fmt-check
make gate         # pre-merge bar: fmt + vet + build + adapter-test + test + race-focused
make gate-all     # gate plus web-gate and the two TUI PTY tests
make adapter-test # adapter wire contract: fake CLIs on PATH assert argv/env/
                  # progress/timeout (python3 tests/adapter_contract_test.py)
make tui-pty-test / tui-pty-exit-test
                  # real-pty TUI contracts: mouse ownership; exit note + failed-turn
                  # persistence (scripts/tui-*-pty-check.py, macOS/Linux only)
make bench        # routing/codec/dedup microbenchmarks
make dev          # build + open the web console against config.yaml (loopback, auto token)
make run          # run daemon from source
make run-local    # daemon + webui sidecar via scripts/run-local.sh
make install-local
                  # sync a fresh build over ~/.local/bin and the release prefix
make measure      # steady-state RSS measurement
make icons        # regenerate the PWA icon set (scripts/genicons)
make clean        # rm -rf bin dist

# release machinery
make build-<os>-<arch>   # darwin/linux/windows x amd64/arm64
make build-lite[-linux-<arch>]
                         # reduced-footprint builds for small devices
                         # (linux amd64/arm64/armv7)
make build-webui         # webui/cmd/panel sidecar binary (runs `make web` first)
make package             # release archives into dist/
make release / release-local

# single test
go test -run TestName ./internal/<pkg>/...
```

The web console needs node/npm only when rebuilding it (`make web`,
`make web-test`; `make web-gate` runs the whole leg as CI does — one
install, then typecheck + unit tests + build). The Go binary embeds a
prebuilt copy; only a placeholder `dist/index.html` is committed.

## Layout

- `cmd/panda/` — the `panda` CLI. Bare invocation drops into the TUI
  REPL; subcommands cover `daemon`/`serve`, `ask`, `repl`/`chat`, `web`,
  `voice`, `install`/`uninstall`, `update`/`upgrade` (self-updater:
  `update check|apply`), `doctor`, `status`, `nodes`, `pair`, `queue`,
  `task` (incl. `priority`/`move`/`artifact`/`board`/`send`
  — queue vs `--inject` live notes to a task), `plan`
  (`run`/`show`/`example`), `cancel`, `approve`/`reject`, `logs`,
  `skill` (`list`/`find`/`hub`/`add`/`reset`), `reminder`, `todo`
  (`list`/`add`/`edit`/`done`/`rm`/`move`/`start` — the human board;
  `start` enqueues a task), `detect`,
  `card` (`show`/`rescan`/`edit`/`set` plus structured
  `native|agent|manual` edits), `init`, `metrics`, `heatmap`, `audit`
  (`verify`/`entries`), `session`/`sessions` (`list`/`show`/`rm` plus
  `fork`/`tree` for thread branching), `memory`, `model`/`models`
  (multi-model registry + active-model switcher), `config` (sections
  `model`/`mcp`/`limits`/`routing`/`injection`/`approval`/`delegation`/
  `self_evolution`, comments preserved), `agents`, `project`, `mcp` (exposes
  the node's self-tools as an MCP stdio server), `rpc` (NDJSON-over-stdio
  embedding surface, experimental), `auth` (subscription OAuth,
  experimental), `self` (`status`/`queue`/`doctor`/`mint`/`propose`/
  `rebase`/`rollback`/`reset` — the opt-in self-evolution patch-set
  pipeline), `read` (markdown viewer; aliases
  `view`/`cat`/`md`/`markdown`), `version`,
  `completion` (bash/zsh/fish script generation). Global flags:
  `--config`, `--card`, `--mcp`, `--json`.
- `internal/` — all runtime code (see below).
- `adapters/` — Python scripts, one per agent CLI (claude_code, codex,
  opencode, generic, hermes, pi, antigravity, deepseek, grok, openclaw…),
  plus `_harness.py` (the shared stdin/stdout JSON protocol they all
  speak). Installed next to the binary; pure stdlib, no pip deps.
- `extensions/voice/` — the voice extension scripts (`stt.py`, `tts.py`,
  `wake.py`) shipped alongside the adapters, consumed by `panda voice`.
- `webui/app/` — Preact console (vite build, embedded via go:embed into
  `webui/panel/`; `webui/cmd/panel/` is the sidecar binary,
  `webui/push/` the Web Push/VAPID service).
- `config.example.yaml` / `config.example.local.yaml` (repo root) —
  config schema samples. `config/` holds the committed
  `capabilities.example-*.yaml` node-card samples; personal
  `capabilities.*.yaml` and `config*.yaml` are gitignored.
- `testdata/` — `node-{a,b,c}.yaml`, `card-{a,b}.yaml`,
  `deploy-opi.yaml` and `scenarios/` fixtures; `run/`, `live/`,
  `artifacts/` and the per-node state dirs are gitignored runtime output.
- `deploy/` — packaging/deploy manifests (homebrew, macos, windows).
- `drivers/` — the reference actuator drivers (`panda-*` scripts the
  card advertises as `hardware:*` abilities — servo, mic, camera,
  notify, MCU, screen capture, GUI input; `.ps1` variants for Windows).
- `scripts/` — installers (`install.sh`/`install.ps1`), release
  packaging/signing/verify and Homebrew-tap tooling, PTY regression
  checks (`tui-*`, `watcher-*`), deploy/lab helpers, `panda_rpc.py`,
  `check-docs.sh`.
- `skills/global/` — the bundled global SKILL.md library.
- `docs/` — user guides and the public protocol spec;
  `docs/README.md` indexes the set (`status.md`, `install.md`,
  `faq.md`, `protocol.md` and their `.en.md` twins).
- Runtime dirs (`data/`, `memory/`, `plans/`, `projects/`, `tasks/`,
  `MEMORY.md`/`USER.md`) appear at the repo root when a dev node runs
  with `work_path="."` — gitignored node state, not source. `site/`
  and `author/` are likewise local-only.

### `internal/` by concern

- **Node & task lifecycle**: `core` (the `Core` daemon — wires store,
  transport, execution loop, delegation/retry/judge, metrics, and the
  inter-task message board in `messages.go`: slot-based sibling notes
  under `plan:<id>` / `task:<parent>` scopes), `entry`
  (classifies input into answer/tool_call/task/plan, builds prompts,
  streams output), `scheduler` (queue, node scoring `score.go`, routing
  `route.go`, multi-hop `chain.go`), `commander` (three execution tiers:
  `native` shell via `executil`, `agent` via adapters, `manual` approval
  queue; `inject.go` enriches agent context).
- **Wire & discovery**: `bus` (WS + sealed-UDP transports, negotiated
  per-session AEAD and binary data frames, HMAC-membership +
  Ed25519-identity hello auth, LAN beacons, DTN/punch — see
  `docs/protocol.md`), `ledger` (capability cards — what each node
  advertises), `nodeidentity`, `carddetect`, `cardmut`.
- **Safety**: `defense` (permission tiers, circuit breaker, scope-drift
  and loop detection, snapshots), `security` (sandbox, net allowlists,
  secret redaction, audit log), `guard`, `trust` (per-peer trust zones —
  mesh/guarded/open levels asserted locally on the peer's Ed25519
  identity — clamping what an outbound delegate may expose and how an
  inbound one is admitted; screen-capture tasks and their output are
  mesh-only).
- **Memory & skills**: `memory` (USER.md/MEMORY.md layers, isolation
  wall, daily logs, Dreaming consolidation engine, context injector),
  `skills` (SKILL.md progressive loading + self-evolution).
- **Data & platform**: `storage` (SQLite WAL + migrations), `ctxstore`,
  `sessions`, `projects`, `config`, `log`, `util` (UUIDv7 etc.),
  `version`, `hwinfo`, `install`, `i18n` (EN/ZH/JA/ES/DE), `reminders`,
  `updater`, `doctor`, `cliui`, `providers`, `pyexec`, `askengine`,
  `artifact`, `mdtext`, `mcp`/`mcpserve`, `agents`, `plan`, `auth`
  (subscription-OAuth token store + PKCE flows), `self` (self-evolution:
  signed patch-set manifests/queue/worktree/archive, release-gate runner,
  staged swap + `panda.release` rollback target).

## Conventions

- Tests sit next to the code (`foo_test.go`); `internal/core/e2e_test.go`
  is the heavyweight integration suite.
- Before opening a PR run `make gate` — it is what CI enforces.
- User-facing strings go through `internal/i18n` and
  `webui/app/src/i18n/` — all five locales are maintained in parallel.
- The marketing site (`site/`) is bilingual, but by a different mechanism:
  Chinese is the markup (every translatable node carries `data-i18n`,
  `data-i18n-aria`, `data-i18n-placeholder` or `data-i18n-content`) and
  English lives in the flat table in `site/js/i18n.js`. New copy needs the
  attribute *and* an English entry — a key without one falls back to the
  Chinese already in the page. Language comes from `?lang=`, then
  `localStorage["openpanda.site.lang"]`, then the browser; there is no build
  step, so Pages ships the files as they are.
- New agent adapters plug into `adapters/` and speak the `_harness.py`
  wire contract; `make adapter-test` (`tests/adapter_contract_test.py`)
  exercises it against fake CLIs on PATH and is a prerequisite of
  `make test`, so an adapter flag change is a deliberate contract edit.
- Top-level docs are localized too — README, CHANGELOG, CONTRIBUTING,
  CLA, CODE_OF_CONDUCT, COMMERCIAL, TERMS and SECURITY each carry
  per-locale variants (`zh-CN`/`ja`/`es`/`de`); keep them in sync.
- Never log secrets; `internal/security` owns redaction — route new
  secret-handling through it rather than reinventing.
