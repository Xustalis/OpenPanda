# CLAUDE.md

Working notes for AI agents contributing to this repository.

## How to find things

Search the leading keywords, then open the named file or section. Code is
authoritative when prose and source disagree. Section links point inside this
file; each row is self-contained so a retrieved fragment is enough to act on.

- **Build, test, lint, race, package, `make gate`, `make site-check`** →
  [Build, test, ship](#build-test-ship); commands live in `Makefile` and
  `.github/workflows/gate.yml`.
- **CI checks, PR merge, `gh pr merge`, release tag, commit author, CLA** →
  [Repository gates, PRs and releases](#repository-gates-prs-and-releases);
  `gate.yml`, `installers.yml`, `security.yml`, `release.yml`, `pages.yml`
  in `.github/workflows/`.
- **AGPL, commercial license, SPDX header, `CLA.md`, `TERMS.md`** →
  [Licensing](#licensing); `LICENSE`, `CLA.md`, `TERMS.md`.
- **Duplicated identifiers, Windows scheduled task, PATH marker, launchd,
  systemd, `TermsVersionCurrent`, `ProgressKind`, `EvUserMsg`,
  `consumed_at`, `EffectiveBaseURL`, `go.mod` toolchain** →
  [Cross-artifact contracts](#cross-artifact-contracts).
- **Add or change a `panda` subcommand, `panda help`, shell completion** →
  `cmd/panda/dispatch.go` and `internal/nodecli/` (the command catalog);
  a test fails a switch case that has no catalog row.
- **Adapter, agent CLI, model preference, `_harness.py` wire protocol** →
  `adapters/_harness.py`, `adapters/*.py`,
  `tests/adapter_contract_test.py`, `docs/agent-execution.md`.
- **TUI keys, mouse, `/queue`, `/inject`, re-authorization card** →
  `cmd/panda/tui_update.go`, `cmd/panda/tui_turn.go`,
  `scripts/tui-*-pty-check.py`; the cross-artifact contracts name both
  slash-command dispatch points.
- **Config keys, per-agent models, concurrency, hot reload** →
  `internal/config/`, `internal/configsync/`,
  `internal/core/execution_config.go`,
  `webui/app/src/views/execution-settings.tsx`, `docs/agent-execution.md`.
- **Database schema, migration, `ask_rounds`, `user_version`** →
  `internal/storage/migrations.go`, `internal/core/rounds.go`.
- **UI strings, five locales, raw key rendered instead of text** →
  `internal/i18n/`, `webui/app/src/i18n/` (`parity.test.ts`),
  `site/js/i18n.js` for the marketing site.
- **Marketing site page, nav, footer, canonical, sitemap, JSON-LD,
  `data-i18n`, escaped CLI placeholders, image credit, `site/media`** →
  [The marketing site](#the-marketing-site-site), `site/`,
  `scripts/check-site.py`, `make site-check`.
- **Installer, auto-start, Windows task name, PATH block** →
  `scripts/install.sh`, `scripts/install.ps1`, `internal/install/`,
  `docs/install.md`.
- **SSE trace events, decision orbit, `steer` acknowledgment** →
  `webui/panel/events.go`, `webui/panel/panel.go`,
  `webui/panel/sessions.go`,
  `webui/app/src/components/event-parser.ts`,
  `webui/app/src/views/sessions.tsx`.
- **Memory graph, graph layout, label overlap** →
  `webui/panel/graph.go`, `webui/app/src/components/graph-layout.ts`,
  `webui/app/src/components/memory-graph.tsx`.
- **"Model configured but nothing runs", injection rejected, endpoint** →
  `internal/commander/inject.go` (`EffectiveBaseURL`), `internal/doctor/`.
- **Localized docs, `CHANGELOG.md`, version examples, release list** →
  `docs/README.md`, `scripts/check-docs.sh`, root `*.md` locale twins,
  `site/changelog.html`, `site/download.html`.
- **Secrets, redaction, sandbox, network allowlist** → `internal/security/`.
- **Repository directory map** → [Layout](#layout) and
  [internal/ by concern](#internal-by-concern).

## What this is

OpenPanda (Open Personal Adaptive Node-based Distributed Assistant) is a
peer-to-peer orchestrator that runs on heterogeneous machines and farms
work out to whichever node is best suited. It is not itself an agent —
it coordinates the agent CLIs already on each device (Claude Code,
Codex, and friends) plus direct shell execution and human-approved
tasks.

Requires Go 1.26.11+ (the security floor in go.mod includes the
GO-2026-6809 crypto/x509 fix and GO-2026-6859 HTTP fix). The build deliberately
avoids CGO: even SQLite comes from
`modernc.org/sqlite`, so cross-compilation is just `GOOS/GOARCH`.

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

Commands for native builds, the unit/integration suite, lint, the race
detector, TUI PTY checks, site validation, cross-compilation and release
packaging. Everything goes through the Makefile:

```bash
make build        # native binary (fmt-check + vet run first as prerequisites)
make test         # unit/integration suite (adapter-test runs first as a prerequisite)
make race         # race detector, whole tree (pre-release superset check)
make race-focused # race detector on concurrency-heavy packages only:
                  # core/commander/bus/storage/askengine/scheduler/webui-panel
make vet          # go vet
make fmt / fmt-check
make gate         # pre-merge bar: fmt-check + vet + staticcheck + vuln +
                  # build + adapter-test + test + race-focused
make gate-all     # gate + web-gate + the four TUI PTY tests (CI's local equivalent)
make staticcheck  # honnef.co/go/tools/cmd/staticcheck across ./...
make vuln         # govulncheck over the reachable import graph
make adapter-test # adapter wire contract: fake CLIs on PATH assert argv/env/
                  # progress/timeout (python3 tests/adapter_contract_test.py)
make site-check   # static invariants for site/ (i18n keys, anchors, assets,
                  # JSON-LD, version, canonical host); also a Pages pre-publish step
make tui-pty-test / tui-pty-exit-test
                  # real-pty TUI contracts: mouse ownership; exit note + failed-turn
                  # persistence (scripts/tui-*-pty-check.py, macOS/Linux only)
make tui-pty-side-test / tui-pty-sidebar-test
                  # side-channel and sidebar PTY contracts (macOS/Linux only)
make bench        # routing/codec/dedup microbenchmarks
make dev          # build + open the web console against config.yaml (loopback, auto token)
make run          # run daemon from source
make run-local    # daemon + webui sidecar via scripts/run-local.sh
make install-local
                  # rebuild Web then Go; sync binary, adapters, voice helpers and drivers
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

Application tests can use `internal/testdb.Open(t)` for a private in-memory
database cloned from the current migrations. It migrates once per test process;
the storage suite still tests migration history directly. CI runs the four PTY
targets in one make invocation so they share one binary build.

## Layout

Directory map: CLI, runtime packages, adapters, console, marketing site,
installers, fixtures and gitignored runtime state.
[internal/ by concern](#internal-by-concern) below groups the `internal/`
packages by subsystem.

- `cmd/panda/` — the `panda` CLI. Bare invocation drops into the TUI
  REPL; subcommands cover `daemon`/`serve`, `ask`, `repl`/`chat`, `web`,
  `service` (`install`/`status`/`restart`/`uninstall` for the console's
  user-level auto-start unit), `voice`, `install`/`uninstall`, `update`/`upgrade` (self-updater:
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
  `model`/`mcp`/`limits`/`routing`/`injection`/`approval`/`sandbox`/
  `delegation`/`self_evolution`, comments preserved; `routing set` also edits the
  per-agent model and concurrency maps — see `docs/agent-execution.md`),
  `agents`, `project`, `mcp` (exposes
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
  `webui/push/` the Web Push/VAPID service). `views/execution-settings.tsx`
  edits per-agent models and concurrency; `components/graph-layout.ts` and
  `memory-graph.tsx` render the memory graph.
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
  `docs/README.md` indexes the set (`agent-execution.md` in five languages,
  plus `status.md`, `install.md`, `faq.md`, `protocol.md` and their
  `.en.md` twins).
- Runtime dirs (`data/`, `memory/`, `plans/`, `projects/`, `tasks/`,
  `MEMORY.md`/`USER.md`) appear at the repo root when a dev node runs
  with `work_path="."` — gitignored node state, not source. `author/`
  and `docs/author/` are local-only for the same reason. `site/` is
  not: it is tracked and deployed by `.github/workflows/pages.yml` (see
  [The marketing site](#the-marketing-site-site)); `site/media/` holds the
  real console captures and demo recording shared with the README demo blocks.

### `internal/` by concern

- **Node & task lifecycle**: `core` (the `Core` daemon — wires store,
  transport, execution loop, delegation/retry/judge, metrics, and the
  inter-task message board in `messages.go`: slot-based sibling notes
  under `plan:<id>` / `task:<parent>` scopes; `rounds.go` persists ask
  rounds, `recovery.go` assesses whether a failed result can be replayed,
  `execution_config.go` applies live execution config), `entry`
  (classifies input into answer/tool_call/task/plan, builds prompts,
  streams output), `scheduler` (queue, node scoring `score.go`, routing
  `route.go`, multi-hop `chain.go`), `commander` (three execution tiers:
  `native` shell via `executil`, `agent` via adapters, `manual` approval
  queue; `inject.go` enriches agent context, `concurrency.go` bounds
  per-agent parallelism).
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
- **Data & platform**: `storage` (SQLite WAL + migrations; schema 68 adds
  attempt-scoped child waits and durable artifact merge baselines), `ctxstore`,
  `sessions`, `projects`, `config` (plus `execution.go` for per-agent
  model and concurrency defaults), `configsync` (polls the shared config
  and database so a model change in one process reaches the others —
  works on Windows and across atomic file replacement), `log`, `util`
  (UUIDv7 etc.), `version`, `hwinfo`, `install`, `i18n` (EN/ZH/JA/ES/DE),
  `reminders`,
  `nodecli` (the `panda` command registry: the agent prompt reference, the
  `panda help` page's rows and order, and the completion vocabulary all
  come from here; a test parses `cmd/panda/dispatch.go` and fails a switch
  case without a catalog row),
  `updater`, `doctor`, `cliui`, `providers`, `pyexec`, `askengine`
  (hot-swaps the active model without restarting a turn),
  `artifact`, `mdtext`, `mcp`/`mcpserve`, `agents`, `plan`, `auth`
  (subscription-OAuth token store + PKCE flows), `self` (self-evolution:
  signed patch-set manifests/queue/worktree/archive, release-gate runner,
  staged swap + `panda.release` rollback target).

## Repository gates, PRs and releases

Read this before merging a PR, pushing `main`, cutting a `v*` tag, or
choosing a commit author. It records the branch ruleset, which workflow runs
on which event, the release order and the identity CLA checks.

- `main` is protected by a GitHub ruleset: deletion and force-push are
  disabled, and every change lands through a PR with all required checks
  green. Merge with `gh pr merge <n> --rebase`; the project does not keep
  merge commits.
- Workflow triggers are not interchangeable, so a green PR is not proof that
  every leg ran: `gate.yml` runs on PRs, pushes to `main` and manual dispatch;
  `installers.yml` only on pushes to `main` (skipping Markdown and `docs/**`);
  `security.yml` weekly and on demand; `release.yml` on `v*` tags; `pages.yml`
  on pushes to `main` touching `site/**`, `CHANGELOG.md` or
  `scripts/check-site.py`, validating the site before it uploads the artifact.
  A PR cannot surface a `lite-linux-*` packaging failure — the `lint` job
  compiles that variant with `go build -tags lite ./...` to close the gap.
- A release is ordered: push `main` → poll the Gate for that exact SHA until
  it succeeds → then push the `v*` tag. Tagging first publishes a build the
  gate never examined.
- Local and PR commits are authored as `Xustalis <fing2024@outlook.com>`;
  CLA assent is checked against the commit author. The
  `OpenPanda <openpanda@localhost>` identity exists only for git calls made
  *inside repository code* on CI runners with no configured identity — never
  use it for a local commit.

## Cross-artifact contracts

Read this before editing an installer identifier, an auto-start unit, a
terms-version fixture, a progress kind, a trace event, a user-message
delivery path, the `go` directive or a doctor check. These values are
duplicated deliberately; changing one copy alone breaks a runtime path that
a unit test may not cover.

- `OpenPandaNode` — the Windows logon task name, shared by
  `scripts/install.ps1`, `internal/install/services_windows.go`
  (`nodeTaskName`) and `docs/install.md`.
- The PATH marker block `# >>> openpanda path >>>` /
  `# <<< openpanda path <<<` is written and stripped by both the installers
  and `internal/install/path_unix.go`.
- Auto-start unit names: `com.openpanda.node` (macOS launchd label) and
  `openpanda.service` (systemd user unit).
- `config.TermsVersionCurrent` — the PTY fixtures must be at least this
  version. Derive it from `config.go` instead of hard-coding a number: a
  stale fixture makes the re-authorization card swallow keystrokes, which
  looks like a scroll regression.
- `askengine.ProgressKind` (`internal/askengine/askengine.go`) — adding a
  kind means checking its rendering in `cmd/panda/ui.go` (`progressNote`,
  plus five locale strings), `cmd/panda/tui_turn.go` (`onProgress`),
  `webui/panel/sessions.go` (`statusEventOf`, SSE) and
  `webui/app/src/views/sessions.tsx` (`statusNote`, plus web locale strings
  for kinds the console phrases locally instead of passing through).
- Delivering a user message is a three-sided contract:
  `task_messages.consumed_at` (migration 58); `EvUserMsg` /
  `EvUserMsgConsumed` from `internal/core/trace.go` in **both**
  `visibleTraceTypes` (`webui/panel/panel.go`) and `traceEventTypes`
  (`webui/panel/events.go`), with a case and five `events.*` strings in web
  `components/event-parser.ts`; and `StreamCallbacks.OnSteerConsumed`
  (`internal/askengine/askengine.go`), which `webui/panel/sessions.go`
  turns into the SSE `steer` event that upgrades a "queued" note. A
  successful channel write is not a delivery acknowledgment. The TUI leaves
  `GetSteer` and `OnSteerConsumed` nil on purpose (`cmd/panda/tui_msgs.go`):
  mid-turn input is the next turn, and the durable `/queue` and `/inject`
  commands cover cross-process steering. Those two commands need a case at
  both dispatch points in `cmd/panda/tui_update.go` (`submitSlash` and
  `submitDuringTurn`), or they fall through to a nonexistent
  `panda queue` verb.
- The `go` directive in `go.mod` is the CI toolchain version. A stdlib
  vulnerability is fixed by raising that directive; editing call sites does
  not change the compiler.
- The diagnostic surface and the execution surface share one source of
  truth: `commander.EffectiveBaseURL` decides whether a model is usable.
  Every branch that refuses to run needs a matching explanation in
  `internal/doctor` or the logs, or users only see the symptom.

## Conventions

Rules by subject: tests and pre-merge checks, localization, the marketing
site, adapters, localized docs, commits and secrets.

### Tests and pre-merge checks

- Tests sit next to the code (`foo_test.go`); `internal/core/e2e_test.go`
  is the heavyweight integration suite.
- Before opening a PR run `make gate` — it is what CI enforces — plus
  `scripts/check-docs.sh` and the TUI PTY checks. A `site/` edit also needs
  `make site-check`, and a dependency change needs `go mod tidy`. Keep both
  i18n key-parity guards green: `internal/i18n`'s `TestKeyParity` /
  `TestKeyParityReverse` and `webui/app/src/i18n/parity.test.ts` — a missing
  key renders as the raw key.

### Localization: Go app and web console

- User-facing strings go through `internal/i18n` and
  `webui/app/src/i18n/` — all five locales are maintained in parallel.

### The marketing site (site/)

- The marketing site (`site/`) is a nine-page site (`index.html`, plus
  `features.html`, `docs.html`, `download.html`, `changelog.html`,
  `status.html`, `security.html`, `community.html`, `faq.html`) plus a
  self-contained `404.html`. Seven primary pages sit in the nav; security and
  community are reached from the eight-card site map and the footer. The
  eleven chapters of the landing page are reached through `hero-index` and ⌘K,
  not the nav. Content for the standalone pages comes from `README.md`,
  `SECURITY.md`, `CONTRIBUTING.md`, `docs/faq.md`, `docs/status.md` and the
  installer scripts — do not invent capabilities or numbers. Adding a page
  means adding its nav link (or site-map card), sitemap entry, canonical /
  og:url, English keys and TOC anchors — `scripts/check-site.py` asserts all
  of them, along with content floors for the capability, security, FAQ,
  community and adapter pages.
- It is bilingual by a different mechanism than the Go app: Chinese is the
  markup (every translatable node carries `data-i18n`, `data-i18n-aria`,
  `data-i18n-placeholder` or `data-i18n-content`) and English lives in the
  flat table in `site/js/i18n.js`. New copy needs the attribute *and* an
  English entry — a key without one falls back to the Chinese already in the
  page. Language comes from `?lang=`, then
  `localStorage["openpanda.site.lang"]`, then the browser; there is no build
  step, so Pages ships the files as they are. The nav, footer and head
  language bootstrap are therefore **copied verbatim into each page**;
  `check-site.py` compares them byte for byte, so a one-page edit fails.
  `js/main.js` belongs to the landing page, `js/page.js` to the eight content
  pages.
  Chinese is the only canonical edition: no hreflang or alternate locale is
  declared, JSON-LD lives on the landing page only and carries
  language-neutral facts (no FAQPage — it would disagree with the English
  view), and social metadata stays zh_CN in both views.
  `scripts/check-site.py` guards i18n key sets in both directions, in-page
  and cross-page anchors, local assets, tab ARIA, per-page canonicals and
  shared chrome, versions against `CHANGELOG.md`, the release list in
  `changelog.html`, the target matrix in `download.html`, the sitemap and
  link safety. A new release must update `changelog.html`, `download.html`
  and every version literal, or the check goes red. Run `make site-check`
  after any `site/` edit; it also runs before the artifact upload in
  `.github/workflows/pages.yml`.
- The landing page's product section is evidence, not a mock: `site/media/`
  holds real console captures, terminal stills and a short screen recording,
  shared with the demo blocks in the five-language README set. Markup, the
  `s3.rec.*` / `s3.shot.*` / `s3.demo.*` keys in the site dictionary and
  the media files move together; `check-site.py` asserts every local asset
  exists. CLI placeholders such as `<id>` or `<version>` must be written
  `&lt;…&gt;` in both the HTML and the English dictionary — an unescaped one
  becomes an unknown element and swallows the rest of the page. The
  ESO/S. Brunier `eso0932a` backdrop is CC BY 4.0, so the shared footer keeps
  the source link, the licence link and an adaptation note.

### Adapters, localized docs, commits and secrets

- New agent adapters plug into `adapters/` and speak the `_harness.py`
  wire contract; `make adapter-test` (`tests/adapter_contract_test.py`)
  exercises it against fake CLIs on PATH and is a prerequisite of
  `make test`, so an adapter flag change is a deliberate contract edit.
  Adapters honour each CLI's native model preference; the per-agent model
  and concurrency maps are documented in `docs/agent-execution.md`.
- Top-level docs are localized too — README, CHANGELOG, CONTRIBUTING,
  CLA, CODE_OF_CONDUCT, COMMERCIAL, TERMS and SECURITY each carry
  per-locale variants (`zh-CN`/`ja`/`es`/`de`); keep them in sync.
- Commits and PR bodies carry no AI attribution: no `Co-Authored-By` bot
  trailers, no "Generated with" footers, and branch names should name the
  change. The public history is part of the project's record.
- Never log secrets; `internal/security` owns redaction — route new
  secret-handling through it rather than reinventing.
