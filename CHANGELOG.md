# Changelog

[English](CHANGELOG.md) · [简体中文](CHANGELOG.zh-CN.md) · [日本語](CHANGELOG.ja.md) · [Español](CHANGELOG.es.md) · [Deutsch](CHANGELOG.de.md)

## Install

**macOS / Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh
```

**Windows (PowerShell)**

```powershell
irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
```

**macOS (Homebrew)**

```sh
brew install Xustalis/openpanda/openpanda
```

After installing, run `panda init` to set the node up, or just type `panda` to drop into the REPL. An older install upgrades in place with the same command — user data is preserved.

## About

OpenPanda (**Open** **P**ersonal **A**daptive **N**ode-based **D**istributed **A**ssistant) is a personal task-orchestration kernel: one `panda` binary runs on each of your devices, nodes find each other over an authenticated WebSocket bus, an entry model turns each request into a direct answer or an executable task spec, and the scheduler routes every task to the device and agent best suited to run it. The CLI is the kernel's primary interface — bare `panda` drops into an interactive REPL — and the web console is a thin shell over the same store and engine.

## Versioning

- Versions follow `MAJOR.MINOR.PATCH`. The project is in initial development (`0.0.x`): a patch release may add features, fix bugs, and — exceptionally — introduce breaking changes, which are always listed under **Breaking changes**.
- A release is cut by tagging `vX.Y.Z`; every commit since the previous tag belongs to the new version's section. `[Unreleased]` collects work since the last tag.
- Each version documents four categories: **Added** (new features), **Fixed** (bug fixes), **Changed** (improvements and refinements), **Breaking changes** (requires action when upgrading).
- Entries name the change and its user-visible effect in one to three lines; the introducing commit is cited where it aids archaeology.
- This English file is canonical. The zh-CN / ja / es / de translations mirror it and may lag briefly around a release.

## [0.0.6] - 2026-08-27

The cross-device compute release takes shape: a request that needs different machines for different steps is now a first-class plan whose stages run where the hardware is, and both surfaces — the CLI and the web console — gained the presentation layer they lacked: live feedback while an ask converges, real Markdown in the browser, and the input editor a daily driver needs.

### Added

- **Plan plane — pipelines whose stages run on different machines** — a stage is an ordinary task (CAS state machine, lease, retry, supervision, review parking), so a pipeline inherits everything a task already has; a finished stage's work dir is packed and chunked over the bus to the machine running the next stage. Two entry paths: `panda plan example > train.yaml`, `panda plan run train.yaml [--dry-run]`, `panda plan show <id>` — or one sentence via `panda ask`, the model emitting a plan precisely when a request must change machines. No stage carries tier-2 consent: an irreversible stage parks in review for a person (c10b8af).
- **Routing by declared hardware** — `resource_profile` is a hard filter (`ledger.Fits`) and the scorer ranks free capacity + queue depth + tier, discounted by heartbeat freshness, so two tasks published at once land on two machines; the entry prompt carries each node's real hardware so the model fills the routing filter sighted, not blind (c10b8af).
- **`panda voice`** — wake word → ASR → the same entry pipeline → TTS: the desk-pet entry for a device with no keyboard (c10b8af).
- **`panda card show | rescan | edit | set`** — one command family over the capability card: print it (and which file it came from), re-scan hardware + installed agent CLIs (`rescan` prints a diff, `--write` applies it and keeps a `.bak`, hand-written decisions preserved), open it in `$EDITOR`, or set fields headlessly. `panda detect`, the card rescan, and the panel now share one detection layer (`internal/hwinfo`) (fdb56b8).
- **A presentation layer for the CLI** — `internal/cliui`: one palette resolved once, and a live status line (spinner, verb, elapsed, token count — both were already recorded, just never shown) that degrades to a static line on pipes. The line editor learns bracketed paste and multi-line input (a pasted multi-line prompt is one ask, and history recalls it as one), Ctrl-R incremental history search, and argument-position completion for the ids nobody retypes. Unknown commands get a did-you-mean; `/help` prints inline grouped by intent; new commands cover what a session reaches for after the first ask works (`/cost`, `/model`, `/status`, `/doctor`, `/export`, `/clear`) plus `@file` attach and `!cmd` passthrough so the prompt is never left (c538ab6).
- **The web chat surface catches up** — a hand-written Markdown renderer (zero `innerHTML`, so no sanitizer dependency; 29 node tests) replaces literal `**bold**` and ``` fences in replies; the composer's primary action while streaming is a stop button (the SSE reader takes an AbortSignal); autoscroll stops yanking the view once the reader scrolls up; Cmd+K palette over the same navigation vocabulary as the sidebar; mobile thread drawer replacing `display:none` (c538ab6).
- **A status page** — `docs/status.md` records what works, what is only built, and what is missing, with the flagship pipeline's verification state (76c5b69).
- **Stale node rows can be removed** — `panda nodes remove <id>` and a Remove button on offline node cards drop a directory row that no live peer backs (a renamed machine, a changed identity, a decommissioned node). The local node's own row and online nodes are refused — both re-register themselves, so removing them would be a no-op wearing a success message.
- **Release-notes tooling** — the release workflow publishes the version's CHANGELOG section plus per-platform install commands as the release body and fails the build when the section is missing; the 0.0.5 release page was rewritten to that standard with an English-only body and language switcher, and every CHANGELOG now leads with one-command install (4e12779, c25a3cb, 98e10df, 600ffb3).

### Fixed

- **Queued tasks and plan stages actually route now** — the queue path kept a local "if I can do it, I do it" short-circuit that the router itself had removed, and it was the path every panel task and plan stage took: the hardware filter never ran where the flagship pipeline actually runs (a GPU stage stayed on the Pi whenever the Pi held the ability; a burst of tasks all stayed on the node that accepted them). The decision belongs to the scheduler; an empty ability list means "no constraint", not "nobody matches"; plan stages get a resource key each, so independent stages fan out instead of queueing behind each other (a5b792e).
- **A finished run's result fits a frame** — `task_result` output is clamped to the bus frame size, so a completed run's result reaches the submitter instead of overflowing the frame and vanishing (c1310da).
- **Memory fences cannot be closed from inside** — the `<memory_data>` fence wrapped the body without touching the body's own tags, so an entry containing the literal closing tag ended the fence early and had its remainder read as instructions — and memory is writable by the model's own tools, the panel, and promoted dream candidates. Tags inside are neutralized; the text stays visible for audit (3f18994).
- **A node no longer describes another machine's hardware** — every one of these was a hardcoded value standing where a probe belonged: the default node name was "macbook", so every node that never ran `panda init` announced itself under the author's laptop name; macOS/Windows had no machine-id source, so renaming the machine looked like a different node; the Windows sandbox stripped PATHEXT/SYSTEMROOT/TEMP — which is why a Windows compute node could not launch an adapter at all; `python3` is not a portable interpreter name (probed now, `py -3` first on Windows); a timed-out task hung forever on Windows because the harness could not kill a process tree (`taskkill /T` now); a card advertising a native ability whose command is absent won the route and died with 127 (pruned at load); a GPU whose size no probe could read wrote 0 and was excluded from exactly the work it exists for (unknown is a third state now); `deploy-pi.sh` defaulted to one developer's LAN address (required now) (fdb56b8).
- **i18n regressions closed** — hardcoded Chinese in the voice path, ask/repl plan output, conversation summaries, uninstall errors, and one hint in `panda help` moved into `internal/i18n` across all five locales — ja/es/de users were seeing Chinese in those surfaces (c538ab6).
- **The REPL opens in under a second, not after the peer dial timeout** — interactive startup dialed every configured peer serially before the banner and then waited for them to settle: an offline peer burned the dialer's full 10s timeout as dead air before the first prompt. The REPL, `panda session`, and `panda voice` now dial peers in the background (an offline peer is routine in a long-lived session, and its failure no longer prints WARN lines mid-keystroke), and one-shot `panda ask` dials peers concurrently so an unreachable peer stops gating a reachable one.

## [0.0.5] - 2026-08-25

The three-device lab patch: the first real macOS + OrangePi + Windows cluster — installed from the public installers, linked over LAN, driven end-to-end — exposed that queued tasks never left their origin node, tier-2 consent died at the delegation boundary, and a locked-out agent CLI could attract routing and hang for minutes. Five commits, all verified on that hardware.

### Added

- **`panda task add --requires`** — declare the abilities a task needs (`--requires gpio:read`, comma-separated); a queued task without a local match is routed to a device that has them, the same root-scheduler policy `panda ask` has always used (c4e1bc7).

### Fixed

- **Queued tasks now route cross-device** — tasks from `panda task add` and the web console were claimed and executed by the origin node only: a task requiring an ability only another device had failed outright (`route: no capability matches` for `pi.uptime` filed on a Mac). On claim the scheduler now consults the root scheduler; with no local match the claim is re-targeted to a capable peer (declined-by loop protection, lease so a dead executor is detected), and the peer's result completes the origin's row. Verified in all three directions on the lab: Mac→OrangePi, OrangePi→Mac, Windows→OrangePi (c4e1bc7).
- **Tier-2 authorization travels with delegation** — `--authorize` consent was local to the submitting node, so an agent task delegated to a peer bounced at the executor's defense layer even though the user had approved it. Consent now propagates on the authenticated bus and the executor honors it: a credential-less OrangePi filing an authorized coding task at the Mac's claude completes instead of dying in review (c4e1bc7).
- **Locked-out agent CLIs no longer attract routing** — a capability card is static, but an installed CLI can be unusable: `claude.exe` on the Windows box with no login state and no model key advertised `agent:*` to the fleet, routing sent it a coding task, and it hung for minutes before failing on a network error. The local fallback chain and the capability summary advertised over hello now gate on viability — CLI on PATH *and* a reachable model (own credentials or injection); the Windows summary now advertises only `win.sysinfo` (2db530f).
- **`panda web` no longer dies on a taken port** — a second `/web` (or a leftover process) errored with `bind: address already in use` and printed a token to copy by hand. The console now falls forward to a nearby port and says so; the browser opens already authenticated (the token is never printed), and `/web` while it runs re-opens the browser logged in. `--no-browser` still prints a token-carrying URL for manual use (c4e1bc7).
- **Peer hello reports the real version** — all three hello paths advertised a hardcoded `0.1.0-dev`, so `panda nodes` showed wrong versions across a mixed-version fleet; they now report `version.Version` (all three lab devices show 0.0.5) (2db530f).
- **The capability card next to the resolved config wins over `./capabilities.yaml`** — starting a daemon from a directory that happens to contain a capabilities.yaml (a repo checkout, another node's card) silently loaded the wrong card; the init-written card next to the config file now takes precedence, `--card` still overrides (2db530f).
- **Windows data directory no longer collides with the install prefix** — the default state dir `%LOCALAPPDATA%\openpanda` and the install prefix `%LOCALAPPDATA%\OpenPanda` are the same directory on case-insensitive NTFS: the SQLite store, memory, and projects lived inside the install prefix and an uninstall swept them away. The data dir is now `%LOCALAPPDATA%\openpanda-data`; Windows nodes coming from 0.0.4 start with a fresh store (fc50721).
- **Installers survive a rate-limited GitHub API and a broken WinPS 5.1 HTTP stack** — `api.github.com` allows 60 unauthenticated requests per IP per hour; when exhausted, both installers now resolve the latest version through the `/releases/latest` 302 redirect instead. `install.ps1` forces TLS 1.2 up front, prefers the bundled `curl.exe` (Windows 10 1803+) with `Invoke-WebRequest` fallback, and adds timeouts so a broken WinINET proxy fails fast instead of hanging. Both behaviors were hit during the real three-device install (109b567).
- **Homebrew tap push authenticated** — the release workflow's tap-update step failed with `could not read Username` when the job token lacked the grant; the push URL now embeds the token (6868a63).

## [0.0.4] - 2026-08-25

The distributed-node release, GA. The engine models physical vs VM nodes, guards
singleton identity per host, ships hardening + contract tests for the adapter
protocol, and exposes a `/api/self` + `/api/nodes` surface with a web Nodes page.
Since the beta snapshot, the follow-up cycle landed the entry-model decision caches,
a layered system prompt, a zero-config web onboarding flow, a shared adapter harness,
tier-2 authorization UX, installer/uninstaller sweeps, updater changelog digests, a
one-question `panda init`, and a scenario-based FAQ. Installs cleanly **from any
working directory** and the first public release with end-to-end docs.

### Added

- **Node kind and stable identity** — `node.kind = physical | vm`. Physical nodes derive
  a stable ID from the host fingerprint (hostname + MAC hash); VM nodes require an
  explicit `node.identity` so you can pin the same identity across reprovisioned
  cloud instances. `panda init` now prompts for kind and (when VM) the stable identity.
  Peer hello protocol v2 carries `node_kind` + `node_identity` and `employee_cache`
  v10 backfills the two columns with `DEFAULT 'physical'` for existing rows.
- **Singleton daemon guard (`nodeidentity` package)** — `Acquire(kind, identity)` takes
  an OS-level file lock in `$USER_DATA_DIR/locks/`, with platform-native `flock(2)` on
  Unix / `LockFileEx` on Windows. Running a second `panda daemon` on the same identity
  exits cleanly with a diagnostic instead of corrupting the shared store.
- **Adapter protocol hardening + contract test** — commander/adapter.go now returns a
  unified `{ok, result, exit_code}` frame with stderr-as-diagnostic capture for
  non-zero exits; adapter/inject logs every injection decision (auto | always | never)
  for operator audit. `tests/adapter_contract_test.py` validates every adapter speaks
  the same frame, and `testdata/scenarios/long_task.py` drives the queue cancel path.
  `adapters/codex.py` argument parsing and stdout framing were fixed as part of the
  hardening work.
- **`/api/self` + `/api/nodes` + web Nodes page** — panel/self.go exposes the local
  node (name, kind, identity, resource class, running state, capabilities) and the
  node directory (local + connected peers with last-seen / running state). The new
  `Nodes` tab (`webui/app/src/views/nodes.tsx`) renders a running/last-seen table
  with kind + resource class chips.
- **Distributed-lab toolkit** — `scripts/lab/generate-three-node.sh` bootstraps three
  isolated configs (physical A + B + a VM node) with distinct identities, shared
  secrets and pre-wired peer lists; `scripts/scenario-model/main.go` scores
  scheduling/routing scenarios against a directory of YAML configs;
  `scripts/task-timeline/main.go` emits an ASCII timeline of task transitions per
  node straight from `openpanda.db`, great for recovery audits.
  `docs/testing/distributed-lab-plan.md` records the three-node scenarios that must
  pass before GA — the release gate.
- **Entry-model decision caches** — intent classification and supervise verdicts hit
  a disk cache (`entry_cache`, migration v11) keyed by prompt + device snapshot, so
  identical inputs skip the LLM call entirely. The system prompt is layered
  pi-style: a compact resident core carries the routing decision, with
  memory-governance and verbose task-JSON layers attached on demand, keeping the
  stable prefix provider-cacheable. The commander model's own token consumption is
  billed into delegation metrics (executor `entry:<model>`).
- **Zero-config web onboarding** — `panda web` boots without any configuration and
  serves queue/projects/nodes immediately; a banner offers one-step model setup
  (API type / base URL / model / key with a live connectivity test), and saving
  hot-loads the engine — the first conversation works without a restart.
- **Updater changelog digest + daemon notice** — the update card in the web console
  shows the latest release's changelog digest (collapsible notes beside the
  download/apply buttons), and the headless daemon logs an update-available notice
  (6h auto-check, once per version) since it cannot apply updates itself.
- **Scenario FAQ** — `docs/faq.md` answers the high-frequency questions by scenario
  (first steps, model-error decoding, agent adapters, tier-2 authorization, review,
  scope drift, multi-device networking, data locations, upgrades); `docs/README.md`
  now indexes the user guides separately from internal plans.

### Fixed

- **Homebrew / any-cwd startup failure (SQLITE_CANTOPEN 14)** — default storage
  paths were `./data/openpanda.db` and friends, so `panda` failed to open the DB
  whenever you launched it from any shell directory other than the project root
  (common with Homebrew installs). The fix is multi-layered:
  1. `config.Default()` now anchors DB/memory/projects/skills/work paths to
     `UserDataDir()` — platform-standard per-user state (`~/Library/Application
     Support/openpanda` on macOS, `${XDG_DATA_HOME:-$HOME/.local/share}/openpanda`
     on Linux, `%LOCALAPPDATA%\openpanda` on Windows).
  2. `config.Load()` runs `resolveRelativePaths()` against the YAML's own directory
     so legacy configs written by pre-v0.0.4 `panda init` keep reading next to the
     YAML file, never next to the shell cwd.
  3. `storage.Open()` MkdirAll's the DB's parent directory even for exotic
     manually-specified paths.
  4. `panelStore()` (used by REPL, `panda web`, panel commands, queue, task, …)
     now creates the full storage directory set just like `runDaemon` already did.
  Smoke-tested: `panda queue` from `/` with a fresh HOME auto-creates the user data
  dir, initializes `openpanda.db`, and prints the empty queue.
- **Missing-key misdiagnosis on the Anthropic path** — a non-streaming call with no
  API key sent the empty-key request anyway and surfaced the provider's 401 as
  "invalid key" instead of "not configured". All call paths now return the
  actionable `panda init` / web-settings hint, the REPL banner marks an
  unkeyed model inline, and the panel reports configuration gaps as 503 (same
  family as "engine not configured") rather than a server-fault 500.
- **Tier-2 authorization UX** — a tier-2 refusal now carries an actionable hint
  (`--authorize` or a `tier: 1` card declaration) and skips the retry budget
  straight to review: retrying cannot produce consent. Registry-driven credential
  probing covers Claude Code's new `~/.claude/config.json` + `settings.json`
  locations. Scope parsing extracts path tokens only, so natural-language
  descriptions like “工作目录下的 haiku.txt” no longer trip drift warnings on
  legitimate file operations.
- **Installer / uninstaller** — `install.sh` resumes partial downloads (`curl -C -`),
  persists PATH with the same marked block `panda install` writes, and its generated
  LaunchAgent/systemd services rely on the daemon's config auto-discovery instead of
  hardcoded `--config`/`--card` paths. `panda uninstall` sweeps the distribution
  prefix (bin/, adapters/, example configs) while refusing Homebrew Cellars (keg
  stays whole, `brew uninstall openpanda` hint) and source checkouts.
- **Any-cwd adapter resolution** — delegation from a temp working directory failed
  with "can't open file …/adapters/claude_code.py"; the resolver now walks up from
  the cwd to locate `adapters/` and falls back to the absolute adapter path.
- **Ask-engine device visibility** — an ask engine built against a fresh database
  self-registers its node under the daemon's stable runtime ID, so the entry model
  no longer sees an empty device list on first use.

### Changed

- `panda nodes` output gained a `Kind` column (physical | vm) so distributed setups
  can tell the host-backed nodes apart from provisioned VM identities at a glance.
- **Shared adapter harness** — the seven agent adapters now share one harness
  library (`adapters/_harness.py`) for the `{ok, result, exit_code}` framing,
  argument parsing, and timeout handling, cutting the per-adapter boilerplate.
- **One-question init** — `panda init` asks a single question (configure a model
  now?) with hardware detection filling in name, resource class, kind, and a VM
  identity when the host probes as a guest; `--defaults` / `--non-interactive`
  drop even that prompt. The example capability cards are rewritten to the real
  `ledger.Card` schema with agent tier semantics.
- **Default model moves to `deepseek-v4-flash`** — the `deepseek-chat`/`reasoner`
  aliases were retired by the provider on 2026-07-24; the pro model is
  deliberately never a default (cost control). Card discovery also looks next to
  the resolved config file, matching the daemon's discovery order.
- README: added identity singleton rule section, kind/identity reference rows in the
  node config table, and a `panda nodes` command mention to surface the new
  visibility surface.

## [0.0.3] - 2026-08-23

### Added

- **Multi-agent adapter registry** — `internal/agents` is the single source of truth for the agent CLIs PANDA delegates to (adapter script, probe binary, install command, docs URL). `panda detect`, `panda agents`, the web settings API, and the commander's availability probe all read from it, so adding an agent is a one-entry change.
- **Four new agent adapters** — Grok Build, DeepSeek Harness (`dsh`), OpenClaw, and Hermes join Codex, Claude Code, and OpenCode: each a small headless Python bridge that runs the CLI and returns `{ok, result, exit_code}`.
- **`panda agents`** — `list` (default) probes every agent on PATH with a best-effort version; `test <name>` runs a connectivity check; `install|update <name>` prints the install command + docs link. When nothing is installed, the output lists every missing agent's install command and download URL.
- **Web settings agent roster** — the settings page's agent list now shows, for each missing agent, its install command and a download link (`/api/agents` returns `install_hint` + `install_url`).
- **Superior task review (`上级完成度判定`)** — after an agent runs, the entry model judges the result against the task's success criteria (`entry.Supervise`, outputs `done`/`continue`). A `continue` verdict re-delegates the follow-up instruction (what remains + next step) to the agent chain, looping until the reviewer accepts the work or a capped round budget (default 5) runs out.
- **Terminal routing by risk** — a completed reversible task lands in **done** (已完成); an accepted irreversible (Tier-2) task — pushes, deletes, irreversible state changes — parks in **review** (待审批) with its result for human sign-off; a task the reviewer keeps rejecting parks in **review** with a `needs_followup` marker. The review events replay in the web task detail.
- **One-line installer** — `scripts/install.sh` (POSIX) and `scripts/install.ps1` (PowerShell) download the matching release archive, verify its SHA-256, unpack the binary plus its agent adapters into a per-user prefix, and link `panda` onto `PATH`, with an optional auto-start service (`panda daemon` at login). A Homebrew tap (`brew tap Xustalis/openpanda && brew install openpanda`) covers macOS.
- **Release packaging** — `scripts/package.sh` (and `make package`) cross-compile every supported platform into `dist/panda-<version>-<os>-<arch>.tar.gz` / `.zip` plus a `checksums.txt`, ready for GitHub Releases.
- **Self-update** — `panda web` and the REPL `/web` check the release channel for a newer CLI while running; the web console downloads and verifies the update and, once the task queue is idle, applies it in one click (atomic binary swap, adapter refresh, restart). Discarding a downloaded update leaves no residue; on Windows the swap's `.old` sidecar is swept on the next start.

### Fixed

- **Multi-line `--version` banner** (e.g. Hermes) no longer pollutes the one-line agent table — version output is truncated to its first line in both the CLI and the web settings API.

## [0.0.2] - 2026-08-22

The CLI-first release: the kernel redesign lands (stages A–C) — every web capability gains a CLI peer, the REPL becomes the product's front door, and the CLI gains conversation memory, live task reporting, and per-sink Markdown rendering.

### Added

- **CLI command families** — every web capability has a CLI peer: `panda session | task | memory | config | agents | project`, all sharing the panel's service layer; `panda ask` gains `--output-format json|stream-json` for headless use (a4cba5f).
- **Resource-aware local task queue** — `core.Submit` becomes async: drag-seq → priority → FIFO ordering, gated by a resource-lock registry plus `MaxConcurrent`, so disjoint-resource tasks run ahead of a blocked queue; tasks gain `priority`/`seq`/`session_id`/`resource_keys` (SQLite v9) (0e8d850).
- **REPL conversation memory** — a 24k-character budget with pair-aligned eviction (a user turn never replays without its answer), persisted to `~/.local/state/openpanda/conversation.json`; `/new`, `/history`, `!!`, and `panda ask --continue` (f0a1b9f).
- **Out-of-band task reporting** — a REPL watcher prints a ✓/✗ line when any task reaches a terminal state (board submissions, web console, peer delegations) without disturbing the input line; inline asks are never double-notified (f0a1b9f).
- **Live task board** — `panda queue --watch` and `/tasks watch` redraw the queue every 2s with state-colored rows; Ctrl-C exits the view, not the process (f0a1b9f).
- **`internal/mdtext`** — per-sink Markdown rendering: ANSI emphasis on color TTYs, plain text for pipes and bare consoles, always stripped before TTS; streaming deltas render line-by-line through the same rules (e94f72f).
- **Live agent progress** — adapters stream NDJSON progress notes on stderr, recorded as throttled `EvProgress` events: `panda task <id>` and the panel timeline show what the agent is doing while it runs (93a453a).
- **Injection policy** — `injection.model: auto|always|never`: agent-native credentials win by default; every credential injection is announced in the task output and audit-logged (852b27e).
- **Cost-aware routing** — agent selection scores capability × cost_tier with a `preferred_agents` bonus, falling back to the next-best matching agent (852b27e).
- **Memory overhaul** — configurable caps (`memory.limits`), multi-file topics with manifest-style selective injection, low-weight dream sedimentation (852b27e).
- **`internal/hwinfo`** — shared hardware probing backing `panda detect` and the new `GET /api/self` device-profile endpoint (852b27e, 1a97fd7).
- **Panel app settings + memory topics** — `GET/PUT /api/settings/app` with validated policy storage; the memory API gains per-file topics; the console memory page is productized and settings grouped, i18n synced across five languages (1a97fd7).
- **`panda init`** — interactive first-run bootstrap writing `config.yaml` + `capabilities.yaml`; `config.ResolvePath` unifies resolution (flag > env > user config > system default) across daemon, web, sidecar, and doctor (f5610fc).
- **Console polish** — shared `PageHeader`/`ErrorState` components, global toasts, and confirmation dialogs on destructive actions (45ee941).
- **REPL ergonomics** — slash-command menu under the prompt, pure-ASCII figlet banner, TERM=linux English/ASCII degradation, and `--card` autodiscovery (`./capabilities.yaml` then `/etc/openpanda/capabilities.yaml`) (f0a1b9f).
- **`scripts/deploy-pi.sh`** — one-command Orange Pi deployment: cross-compile, atomic binary swap, systemd install, health check (d7bc87f).

### Fixed

- **Whole-run adapter timeout** — a CLI wedged mid-stream (pipe open, no output) blocked the adapters' read loop forever; the timeout only covered the tail after stdout EOF. Both adapters now run the CLI in its own process group with a watchdog thread that kills the whole tree at the deadline (332f2d4).
- **Anthropic tools API compatibility** — tool_use blocks now always carry `input` (empty object for no-arg tools); strict Anthropic-compatible providers (DeepSeek /anthropic) previously rejected follow-up turns with a 400. Dotted tool names renamed to underscores to satisfy `^[a-zA-Z0-9_-]+$` (93a453a).
- **codex could not initialize under a non-interactive parent** (EPERM writing its state DB and PATH aliases before the first turn) — runs with `-s danger-full-access`, confined by PANDA's external sandbox (332f2d4).
- **Agent failures recorded an empty reason** — the adapter's diagnosis now mirrors into Stderr, so `store.Fail` and task results carry the actual error (93a453a).
- **Mutual-dial reconnect storm** — the dedup loser's final hello reply went out on the registry conn instead of the arriving conn, so it never bound the peer identity and redialed every second (869 reconnects in 15 minutes on real hardware; now 1) (93a453a).
- **Missing work_path** surfaced as a misleading fork/exec ENOENT blaming the command binary — the daemon mkdirs all storage roots at boot (f0a1b9f).
- **Trailing flags silently swallowed** into positional args (`panda task <id> --config x` lost the config) — every subcommand now hoists flags ahead of positionals (f0a1b9f).
- **Completion loop** — `/e` snapped to `/exit ` and backspacing re-triggered it (f0a1b9f).
- **SQLite v9 migration crashed** on legacy databases created before the `tasks` table existed; the table is now created when missing (0e8d850).
- **API errors arrive as guidance, not transport noise** — 401/403 point at `model.api_key`, 404 at `base_url`/model name, persistent 429/5xx name rate limiting, connection failures suggest a network check (df47725).
- **Gates and hardening** — `make measure` referenced a non-existent config; gofmt drift; README stated the wrong Go version; `.gitignore` missed `.openpanda/`; the example config's phantom peer hot-looped warnings; the panel gained `securityHeaders` middleware (cacde7b).

### Changed

- **Answer discipline** — the entry prompt demands conclusions-first answers (no visible reasoning, minimal structure); agent prompts carry an output rider: the final message reports what was done, execution detail stays in `panda task <id>` events (93a453a).
- **Streaming resilience** — `streamWithRetry` replays transient drops (429/5xx/network) with backoff while nothing was delivered; `deltaGuard` keeps task JSON out of chat bubbles and keeps mid-JSON drops retryable; an exhausted tool loop converges via one final tool-free call instead of erroring; tool execution pins the classification-time registry snapshot, preventing "unknown tool" during MCP hot-switching (df47725).
- **Grouped sidebar navigation** — collapsible sections (Tasks / Devices & agents / Personal / System) with the entry prompt recast as the "conductor" persona (f5610fc).
- **Panel endpoint tests** — seventeen tests close the highest-risk audit gaps: sessions CRUD plus a real git end-to-end (worktree carve over HTTP, diff, merge), model-key masking (the raw secret never leaves), MCP spawn failure as 400, skills lifecycle, reminders CRUD, system endpoints (ad884bf).
- **Quiet interactive config loading** — interactive commands mute the config loader's slog chatter; the daemon keeps full logs (f0a1b9f).

### Breaking changes

- **Bare `panda` now opens the interactive REPL** instead of the headless daemon; the kernel moved to an explicit `panda daemon` subcommand. The systemd unit, LaunchAgent, Windows launchers, and Makefile run targets were updated — deployments invoking `panda` directly must switch to `panda daemon` (f0a1b9f).

## [0.0.1] - 2026-08-19

Initial open-source pre-release: the full kernel feature set (daemon, CLI, P2P delegation, audit chain, migrations, scheduler, SSE panel, embedded web console, interactive REPL, cross-platform install lifecycle) plus the assistant layer (agent senses, reminders, MCP, worktree chat sessions, kanban queue board). All gates green throughout: build / vet / full tests / `-race` / cross-compile.

### Added

- **Kernel foundations** — task state machine with leases and crash recovery, authenticated WebSocket node bus, capability directory, and the local execution pipeline with the OpenCode adapter (Sprint 0–1: 1be8f85..307e13a).
- **P2P delegation** — cross-node task routing with context-tiered transfer, the tiered permission model (Tier 1 auto-run / Tier 2 approval), GPIO access, and DCPS scheduling scores (3040e18, 6324a87).
- **Defense chain** — scope-drift detection, retry-loop detection, and command classification with a destructive-command table (590cacc, c647c96).
- **Hermes memory and skills** — daily notes, dreaming with sedimentation, project memory, and loadable skills; `panda skill` manages skill approvals from the CLI and the console carries a skills view (9a41b3e, c36cad1).
- **Voice sidecar** — wake word, STT, TTS, and VAD (hardware-gated), with `OPENPANDA_WAKE_KEYWORD` / `OPENPANDA_WAKE_MODEL` overrides (84faf08).
- **Real-device bringup** — three-node deployment verified on Mac / Windows / Orange Pi, scope routing, and the headless kernel form (0aa9f73, 7f1f8bd).
- **Audit and migrations** — `prev_hash` audit chains for task events and the global log, PRAGMA `user_version` SQLite migrations, slow-DoS protection (hello timeout + connection limits), MCP client hard timeout (7582754).
- **Scheduler mechanisms** — DCPS weighted scoring (`0.4·resource_efficiency + 0.3·user_priority + 0.2·scheduler_tier + 0.1·wait_time`) discounted by TMB heartbeat freshness (30-minute half-life); capacity-driven accept/decline; decline auto-reroute excluding historical decliners (f454909, 7385a89).
- **One-shot CLI panel commands** — `panda status`, `panda queue`, and `panda task | cancel | approve | reject | logs` inspect the node and manage tasks without entering the REPL (307e13a).
- **Interactive REPL** — slash commands over every panel surface (`/ask`, `/tasks`, `/approve`, `/nodes`, `/web`…), five-language i18n, optional ask engine so panel commands work without a model endpoint (6119493).
- **Embedded web console** — rebuilt on Vite + Preact + TypeScript and folded into the binary via `go:embed`: queue/detail/ask/projects/nodes views, live SSE updates, five UI languages (61cc519, c9768c1).
- **Panel write paths + SSE** — `POST /api/ask` via the shared `askengine` package, projects, nodes, cancel, logs, and the `/api/events` change feed (b4fb9f5).
- **`panda web`** — zero-config loopback console with an ephemeral-token auto-login URL (47517e3).
- **`panda install` / `uninstall` / `doctor`** — cross-platform lifecycle: persistent PATH registration, standalone self-check, whitelist-safe uninstall with confirm + zip backup (86b9b9d).
- **Kanban queue board** — create form, priority cycling, per-column drag reorder, inline approvals (da9c9e1).
- **Chat sessions in git worktrees** — streaming replies, live thinking chain (task_events replay + SSE refetch), exactly-once summary fold-back (c36cad1).
- **MCP integration with hot-reload** — one stdio server, validated by actually spawning it before the swap; tools join the agent toolset without restart (c36cad1).
- **Scheduled reminders** — agent-scheduled via the `reminder.set` tool; Web Push (loopback counts as a secure context) plus SSE countdowns; `panda reminder` CLI (c36cad1).
- **Agent senses** — `time.now` and `weather.get` system tools: the model has no clock or window of its own (c36cad1).
- **codex adapter + agent visibility** — installed-CLI probe with connectivity tests; `panda detect` hardware scan into a capabilities.yaml draft (c36cad1).
- **`panda metrics [--csv]` and `panda audit verify [--task <id>]`** — delegation metrics export and audit-chain verification (7582754).
- **`scripts/smoke-delegate`** — cross-process delegation verifier: exit 0 means a peer-only-capability task reached done on a peer.
- **Open-source docs** — five-language READMEs, CONTRIBUTING with merge gates (`make gate`), and the public desktop & packaging roadmap (51031eb).

### Fixed

- **Mutual-dial connection flap** — two nodes dialing each other produced an endless ~1s connect/disconnect cycle; deterministic tie-break in `ensurePeer` (lexicographically smaller node id wins) leaves exactly one TCP connection (879b42d).
- **Wire-protocol authorization gaps** — result/decline/accept/context-ack verify the sender is the current executor; CAS state guards close TOCTOU races; `waiting_context` always carries a lease; local-exec failures terminalize instead of leaving zombies (9622538).
- **Command-classification bypasses** — `env -S` values recursively classified, `php -r` scanned, `find -exec` / `tar --checkpoint-action` / `git push/commit` fail closed to Tier 2 (f5db449).
- **Process-group management** — whole-tree kill on cancel (Unix `Setpgid`, Windows `taskkill /T`) and a 630s adapter hard timeout, no orphaned grandchildren (f5db449).
- **Memory injection channels** — atomic writes for Hermes/Projects/skills, external input tainted `[ext]` and never promoted by dreaming, memory fenced in `<memory_data>` with a data-not-instructions preamble (a742585).
- **Cancel propagation** — `task_cancel` cascades hop-by-hop to executing nodes along the delegation chain (574632a).
- **Transactional writes** — task state UPDATEs and their audit-event INSERTs commit in one transaction (c5d34d4).
- **Comprehensive sweep (D1–D32)** — delegation orphans terminalized, forward copies leased, hello HMAC bound to a 5-minute window, NetworkGuard pinned to configured endpoints, bounded subprocess output capture, and more (1694b7d).
- **White-screen console on fresh clones** — a git-restored hashed `index.html` pointed at ignored assets; the committed placeholder is now stable and `make web` guards on the real build landing (ab87f90).
- **Unknown subcommands started a resident daemon** (`panda statsu`) — they now exit 2 with usage (a742585).
- **Voice wake defaults** — real built-in keywords per backend (`hey_jarvis` / `porcupine`) instead of the non-existent `hey_panda` (4ea73bf).
- **Pre-release audit fixes** — `panda help` exists; "PANDA" brand residue removed from prompts and examples; `config.example.yaml` documents `mcp:` and `model.api_type`; dead roadmap link fixed (2f001c0).

### Changed

- **Hard memory wall** — personal memory is never injected into workspace (worktree-pinned) conversations, so "user prefers dark themes" cannot leak into a code task; project memory reaches execution only via the executing node's ContextPack (da9c9e1).
- **Agent adapters join the tier model** — undeclared agents default to Tier 2 and are rejected before the adapter subprocess spawns (a4d2d9e).
- **OpenAI wire format alongside Anthropic** — the entry model speaks both `/v1/messages`-compatible and OpenAI-compatible endpoints, with streaming on both (c36cad1).
- **Secret-file hardening** — configs containing `api_key` / `shared_secret` / `panel_token` are auto-chmod'd to 0600 with env-var guidance (6275fd4).
- **Panel defaults to loopback** (`127.0.0.1:7840`); non-loopback binds warn about plain HTTP (a742585).
- **Peer reconnect replaces stale connections** — a new conn from the same identity swaps into the registry; removal matches by conn identity (7911bbe).
- **Interpreter `-c` classification is whitelist-based** — only provably pure-output code stays Tier 1 (f5db449).
- **Deployment baseline documented** — plain `ws://` only over loopback/Tailscale/trusted LAN; TLS reverse proxy + `wss://` across the public Internet (7582754).

### Breaking changes

- **Project renamed to OpenPanda** — module path `github.com/Xustalis/OpenPanda`, env vars prefixed `OPENPANDA_`, units `openpanda.service` / `com.openpanda.node.plist`, default DB `openpanda.db`; the CLI binary keeps the short name `panda` (ac71bb1, 6f2083e).

## Parked follow-ups

Deferred deliberately so they stay visible:

- Keyboard shortcuts for the console (new chat, quick task, view switching).
- Browser companion surface for the assistant.
- First-class git views in the console (branch state, history, remotes).
- Worktree management (list/prune/inspect) from the console.
- User-tunable assistant personality and presentation.
- Web-search caching to cut repeat fetches and latency.
- Per-task reasoning-effort tiers (low/medium/high).
