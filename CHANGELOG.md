# Changelog

[English](CHANGELOG.md) · [简体中文](CHANGELOG.zh-CN.md) · [日本語](CHANGELOG.ja.md) · [Español](CHANGELOG.es.md) · [Deutsch](CHANGELOG.de.md)

## [Unreleased]

## [0.0.2] - 2026-08-22

The CLI-first release: the kernel redesign lands — every web capability now has a CLI peer — the REPL becomes the product's front door, and the CLI gains conversation memory, live task reporting, and per-sink Markdown rendering.

### CLI front door & REPL overhaul

- **CLI front door** — bare `panda` now opens the interactive REPL (was: the headless daemon); the kernel moved to an explicit `panda daemon` subcommand. The systemd/LaunchAgent/Windows launchers and the Makefile run targets start the daemon explicitly.
- **REPL multi-turn context** — bare-mode asks accumulate a conversation bounded by a 24k-character budget (whole exchanges evicted oldest-first, so a user turn never replays without its answer) and persist it to `~/.local/state/openpanda/conversation.json`: a new terminal resumes where the last one ended. `/new` clears it, `/history` views it, `!!` repeats the last prompt, and `panda ask --continue` picks the thread up one-shot.
- **Out-of-band task reporting** — the interactive REPL runs a task watcher that polls the store's state fingerprint and prints a ✓/✗ line when a task reaches a terminal state (queued board tasks, web-console submissions, peer delegations) — interleaved into the line editor without losing the in-progress buffer. Inline asks absorb their own outcome and are never double-notified.
- **Live task board** — `panda queue --watch` (and `/tasks watch` in the REPL) redraws the queue in place every 2s with state-colored rows; Ctrl-C exits the view, not the process.
- **Slash command menu** — typing a `/` prefix in the REPL live-lists matching commands under the prompt (capped at 10 with a (+N, Tab) hint); completion itself stays on Tab. Fixes the auto-complete loop where `/e` snapped to `/exit ` and backspacing re-triggered it.
- **Startup banner redesign** — classic figlet lettering spells out OpenPanda in pure ASCII (renders on any terminal), with node/model/workdir info lines; colors on TTY only.
- **TTY/console degradation** — on a bare Linux console (TERM=linux) the UI falls back to English and ASCII separators so nothing renders as diamonds; non-UTF-8 terminals swap `·` separators for `|`.
- **Capability card autodiscovery** — `--card` now defaults to `./capabilities.yaml` then `/etc/openpanda/capabilities.yaml`, so installed nodes execute tasks with zero flags.
- **Flag reordering** — flags may sit after positionals (`panda task <id> --config x`): the Go flag package silently swallowed trailing flags into the positional text; every subcommand now hoists them ahead.

### Kernel redesign — CLI is the kernel, web is the thin shell

- **Injection policy (stage A)** — `injection.model: auto|always|never`: agent-native model credentials win by default; every credential injection is announced in the task output and audit-logged.
- **Cost-aware routing (stage A)** — agent selection scores capability × cost_tier with a `preferred_agents` bonus, falling back to the next-best matching agent.
- **Memory overhaul (stage A)** — configurable caps (`memory.limits`), multi-file topics with manifest-style selective injection, and low-weight dream sedimentation.
- **Hardware probing (stage A)** — a shared `internal/hwinfo` package backs `panda detect` and `/api/self`.
- **CLI command families (stage B)** — every web capability has a CLI peer: `panda session` (worktree-isolated chat sessions), `task` (board, timeline, add), `memory` (multi-file editing), `config`, `agents` (probe + connectivity tests), and `project` — all sharing the service layer with the panel. Platform terminal helpers (term_darwin/linux/unix/other) provide raw-mode UX.
- **Panel: self, app settings, memory topics (stage C)** — `GET /api/self` (device profile), `GET/PUT /api/settings/app` (validated app-policy storage), and a memory API with per-file topics; the console memory page is productized for topics, settings are grouped, and i18n is synced across all five languages.
- **Panel endpoint test coverage** — seventeen tests close the highest-risk gaps: sessions CRUD plus a real git end-to-end (worktree carve over HTTP, diff, merge landing in the main checkout), model-settings key masking (the raw secret never leaves), MCP spawn failure as a 400, the skills lifecycle, reminders CRUD, and the system endpoints.

### Output hygiene & adapters

- **Markdown output hygiene** — new `internal/mdtext` renders answers per sink: color TTYs get ANSI emphasis (cyan headings, bold, dim code, aligned tables), pipes and bare consoles get plain text, and the voice pipeline (`Speak`) always strips Markdown before TTS. Streaming deltas render line-by-line through the same rules, so no raw `**`/`|`/`#` markers leak into any surface.
- **Answer discipline** — the entry prompt now demands conclusions-first answers (no visible reasoning, minimal structure), and agent prompts carry an output rider: the final message reports what was done, not the exploration trail. Execution detail stays in `panda task <id>` events — the CLI equivalent of a collapsed "show work" section.
- **codex adapter** — runs with `-s danger-full-access`: codex's own sandbox cannot initialize under a non-interactive parent (its state DB and PATH-alias creation fail with EPERM before the first turn), and PANDA already confines the adapter to the task cwd. Agent failures also now surface their diagnosis instead of an empty `result {"failed":""}`.

### Added

- **Resource-aware local task queue** — `core.Submit`'s synchronous model becomes an async queue: `internal/scheduler/queue` orders tasks by drag-seq → priority → FIFO and gates starts on a resource-lock registry plus `MaxConcurrent`, so disjoint-resource tasks run ahead of a blocked queue. Tasks gain `priority`/`seq`/`session_id`/`resource_keys` (SQLite v9), and board tasks jump into their linked session (0e8d850).
- **`panda init` — interactive first-run bootstrap** — writes `config.yaml` + `capabilities.yaml` to a user-writable dir from one interactive prompt: hardware-scan defaults, validated enum inputs (re-prompt on typos), five-language prompts. `config.ResolvePath` gives a single resolution order (flag > env > user config > system default) shared by the daemon, `panda web`, the webui sidecar, and doctor, so an init-written config is picked up everywhere without extra flags (f5610fc).
- **Console P1 polish — unified pages, editable memory, toasts, confirm dialogs** — shared `PageHeader`/`ErrorState` components standardize every view's structure and error handling; the memory page becomes a product page (entries split on `§`, new-entry highlight, char counters, in-place editing via the new `PUT /api/memory/{file}`); global toast feedback (errors manual-dismiss, success/info auto-dismiss) replaces scattered per-view error text; destructive actions — delete chat, reject skill, cancel task, delete reminder — now require a confirmation dialog (45ee941).

### Changed

- **Grouped sidebar navigation** — the console nav collapses into collapsible sections (Tasks / Devices & agents / Personal / System) with the active group auto-expanded — progressive disclosure instead of eight flat entries; the entry prompt is recast as the "conductor" persona: simple asks answered directly, complex work dispatched to devices and agents (f5610fc).
- **Streaming resilience and convergence for the ask pipeline** — `streamWithRetry` retries transient drops (429/5xx/network) with backoff while no delta has reached the user; `deltaGuard` keeps raw structured output (task JSON, ```json fences) from streaming into chat bubbles and terminals, and suppressed deltas don't count as delivered, keeping mid-JSON drops retryable; a tool loop that exhausts maxRounds now converges via one final tool-free call instead of erroring; tool execution uses the same registry snapshot classification saw, preventing "unknown tool" errors during MCP hot-switching; CLI/REPL answers stream live on a TTY (tool progress as one-line notes), piped output stays clean (df47725).

### Fixed

- **Whole-run adapter timeout** — the codex/claude adapters read the agent CLI's stdout to EOF before waiting on the process, so a CLI wedged mid-stream (pipe open, no output) blocked the read loop forever — the request timeout only covered the tail after stdout EOF. Both adapters now start the CLI in its own process group with a watchdog thread that kills the whole tree at the deadline: children inherit and hold the pipes open, so killing only the direct child left the readers blocked.
- **Anthropic tools API compatibility** — tool_use blocks now always carry `input` (empty object for no-arg tools): map omitempty previously dropped it and strict Anthropic-compatible providers (DeepSeek /anthropic) rejected follow-up turns with a 400. Dotted tool names (reminder.set, time.now, weather.get) renamed to underscores to satisfy the `^[a-zA-Z0-9_-]+$` pattern.
- **work_path auto-creation** — the daemon mkdirs all storage roots (context/memory/projects/skills/work) at boot; a missing work dir used to surface as a misleading fork/exec ENOENT blaming the command binary.
- **Mutual-dial reconnect storm** — the dedup loser's final hello reply went out on the registry conn instead of the arriving conn, so the loser never bound the peer identity, skipped MaintainPeer's edge wait, and redialed every second; observed 869 reconnects in 15 minutes on real hardware, now 1 with silence after.
- **Gates and small hardening** — Makefile `measure` target referenced a non-existent config (now matches the README snippet); six files reformatted to satisfy the gofmt gate; README badge/prerequisites state Go ≥1.26 across all five editions; `.gitignore` gains `.openpanda/`; the example config's phantom peer is commented out so `make run` doesn't hot-loop warnings; the panel gains a `securityHeaders` middleware as defense-in-depth (cacde7b).
- **SQLite v9 migration on legacy DBs** — the queue-schema migration crashed on databases created before the `tasks` table existed; it now creates the table when missing (0e8d850).
- **Actionable API error mapping** — non-OK status codes are preserved through the streaming path so errors reach the user as guidance, not raw transport noise: 401/403 point at `model.api_key`, 404 at `base_url`/model name, 400 at the request, persistent 429/5xx name rate limiting or service unavailability, and connection failures suggest a network check (df47725).

## [0.0.1] - 2026-08-19

Initial open-source pre-release.

**Project renamed to OpenPanda** (Open + Personal Adaptive Node-based Distributed Assistant). Go module path is now `github.com/Xustalis/OpenPanda`; all env vars use the `OPENPANDA_` prefix; systemd/LaunchAgent units are `openpanda.service` / `com.openpanda.node.plist`; default DB filename is `openpanda.db`. The CLI binary keeps the short name `panda`.

All gates green throughout: build / vet / full tests / `-race` / cross-compile. Covers the full kernel feature set (daemon, CLI, P2P delegation, audit chain, migrations, scheduler scoring + dedup, SSE panel, embedded web console, interactive REPL, cross-platform install/uninstall/doctor) plus the assistant layer: agent senses, scheduled reminders, MCP integration, worktree chat sessions, and the kanban queue board.

### v0.0.1 pre-release audit fixes

- **Web console embedding restructured** — the vite build now lands in
  `dist/app/` while the committed `dist/index.html` placeholder is never
  touched by a build. Previously a `make web` + `git add -A` cycle could
  commit a hashed index.html pointing at ignored `/assets/*`, which left
  every fresh clone with a white-screen console. The placeholder is now
  stable, `make web` guards on `dist/app/index.html` existing, and the
  static handler prefers the real build with the placeholder as fallback.
- **`panda help`** — the subcommand now exists (also `-h`/`--help`) and
  prints an oriented overview instead of an error; unknown subcommands
  print the same usage.
- **Brand residue** — the entry model's system prompt introduced the agent
  as "PANDA"; it now says "OpenPanda" (user-visible in every reply).
  `config.example.yaml` header likewise.
- **`config.example.yaml`** — documents the previously undocumented `mcp:`
  section and `model.api_type` (anthropic | openai); push section comments
  updated for the embedded console era.
- **Dead link** — roadmap referenced a local-only delegation report; now
  points at `scripts/smoke-delegate`, which reproduces the verification.

### Added

- **`panda install` / `panda uninstall` / `panda doctor` — global command lifecycle, cross-platform** — `panda install` copies the binary to `~/.local/bin` (unix) / `%LOCALAPPDATA%\OpenPanda\bin` (Windows) and registers it on PATH persistently: a marked block in shell rc files (`# >>> openpanda path >>>`, idempotent, user lines untouched) on unix, HKCU\Environment on Windows via the registry API with the value type preserved (`setx` avoided — it truncates PATH at 1024 chars) plus a WM_SETTINGCHANGE broadcast; it then self-verifies by executing the installed copy. `panda doctor` is the standalone self-check (installed copy runs / PATH resolves / persistence survives reboot / config & DB usable; exit 1 on any failure). `panda uninstall` is whitelist-safe: it prints the full plan, requires typing `confirm` (or `--yes` for scripts, `--dry-run` to preview), deletes only explicitly-derived targets (binary, PATH registration, DB + journals, context dir, VAPID key, config only inside owned roots), always keeps user assets (projects/memory/skills/work dirs — anything overlapping home or an asset is auto-flipped to keep), writes a zip backup of the deleted state to the home dir first, and produces a deleted/kept report file. Guardrail core in `internal/install` (unit-tested, incl. symlink-safety: links are removed, never followed). Five-language CLI messages throughout.
- **`panda web` — one-command console with auto-login** — loopback bind and an ephemeral random token by default (zero config), and the browser opens at `/?token=…` which the app consumes once and strips from the address bar: no config editing, no token paste. The same zero-config + auto-login behavior lands in the REPL's `/web` and the `panda-webui` sidecar (which now prints a ready-to-open URL); non-loopback binds without a configured token still fail closed. Frontend auto-login consumes `?token=` on load (Jupyter-style); `make web` now fails loudly if the build did not land (placeholder guard).
- **Interactive REPL** — `panda repl` is the operator's seat: bare input goes to the ask engine, slash commands drive every panel surface (`/ask`, `/tasks`, `/task`, `/cancel`, `/approve`, `/reject`, `/logs`, `/projects`, `/project`, `/nodes`, `/authorize`, `/lang`), and `/web` boots the embedded console in one click. Unknown commands name the fix, never exit; the ask engine is optional so the REPL still serves panel commands without a model endpoint (7a5c2bf).
- **Embedded web console** — the console is rebuilt on Vite + Preact + TypeScript (zero runtime deps beyond Preact) and folded into the binary via `go:embed`: queue/detail/ask/projects/nodes/approvals views, live SSE updates, five UI languages (English, 简体中文, 日本語, Español, Deutsch), panda brand SVG. `make web` builds it; a committed placeholder keeps `go build` working without node (844ccf6, 688cc20).
- **Panel write paths + SSE** — `POST /api/ask` (unified entry model via the shared `askengine` package), `POST /api/projects` + `GET /api/projects`, `GET /api/nodes` (live capability directory), `POST /api/tasks/{id}/cancel`, `GET /api/tasks/{id}/logs`, and `GET /api/events` (SSE stream of queue/node changes) close the read-only gap found in the system audit (b599dc7, 6748baa).
- **CLI i18n** — `internal/i18n`: locale detection, English fallback, `{placeholder}` interpolation; CLI and REPL share the same five-language message maps, extendable by adding a locale entry (7a5c2bf).
- **Config startup validation** — resource classes, peer and listen addresses, and listen/panel port conflicts are checked at boot instead of failing at dial time (b599dc7).
- **`scripts/smoke-delegate`** — cross-process delegation verifier: becomes an ephemeral scheduler participant, submits a task requiring a peer-only capability, and reports where it ran; exit 0 means the round-trip reached done on a peer (fbb4f9e).
- **CONTRIBUTING.md** — engineering gates (`make gate`), code conventions (error wrapping, comment policy, concurrency rules, fail-closed security), commit style, i18n rules, and the PR checklist; plus the public [desktop & packaging roadmap](docs/plans/roadmap-desktop-and-packaging.md) (Stage 1 done → Stage 2 distribution hardening → Stage 3 desktop on Wails → Stage 4 marketplace/mobile/multi-user).
- **Sprint 2 paper mechanisms (ATC-MARL mapping)** — `internal/scheduler/score.go`: DCPS weighted scoring (`0.4·resource_efficiency + 0.3·user_priority + 0.2·scheduler_tier + 0.1·wait_time`) discounted by TMB heartbeat freshness (`exp(-λ·Δt)`, 30-minute half-life); capacity-driven accept/decline via `MaxConcurrent`; heartbeats now carry live `CurrentTasks`, closing the data loop for both mechanisms (543801f).
- **Decline auto-reroute** — tasks persist their `requires` capability set (`requires_json` column, both submit and delegate paths); a declined task re-runs DCPS scoring excluding historical decliners (`DeclinedBy` from `EvDecline` audit events) and re-dispatches to the next-best node (dad4f04, P1-5).
- **`panda metrics [--csv]`** — export delegation metrics; **`panda audit verify [--task <id>]`** — verify the `prev_hash` chain of the global audit log or one task's event timeline (6f2c8d5).
- **PRAGMA `user_version` driven SQLite migrations** (6f2c8d5, A1) and a **`prev_hash` audit chain** for `task_events` and `audit_log` (6f2c8d5, A3).
- **`OPENPANDA_WAKE_KEYWORD`** env override for the voice wake word; openwakeword can still point at a custom `.tflite` via `OPENPANDA_WAKE_MODEL` (2e72c8c).
- **Agent senses** — `time.now` and `weather.get` system tools: the model has no clock or window of its own, so the ask engine provides them (weather via Open-Meteo geocoding + current/tomorrow) (c36cad1).
- **Scheduled reminders** — `reminder.set` tool lets the agent schedule its own; SQLite store with atomic `ClaimDue` + scanner so daemon and panel share one DB without double-firing; delivery as Web Push (loopback is treated as a secure context, so `panda web` gets push without TLS) and live SSE countdowns on any open console; `panda reminder list/add/rm` manages them from the CLI (c36cad1).
- **MCP integration with hot-reload** — one stdio MCP server, configurable via `config.yaml` (`mcp.command`, comment-preserving update) or the console settings card, which validates by actually spawning the server and listing its tools before the swap; tools join the agent toolset immediately, no restart (c36cad1).
- **Chat sessions in git worktrees** — console conversations run in isolated git worktrees with streaming replies; session task cards expose a live thinking chain (task_events replay + SSE refetch), and finished tasks fold a summary turn back into the session exactly-once (c36cad1, 0e8d850).
- **Kanban queue board** — a four-column task board in the web console: create form, priority cycling, per-column drag reorder, and inline approvals (da9c9e1).
- **Codex adapter + agent visibility** — `adapters/codex.py` joins claude/opencode behind the same adapter protocol; the settings page lists installed agent CLIs with connectivity tests; `panda detect` scans hardware (CPU/RAM/GPU/agent CLIs) into a capabilities.yaml draft; doctor now checks python3, `adapters/`, and the agent CLIs too (c36cad1, 0e8d850).

### Changed

- **Peer reconnect replaces stale connections** — a new conn from the same authenticated identity swaps into the registry (old conn closed outside the lock), and `handleHello` re-greets on replacement; registry removal matches by conn pointer (befa3bd, P1-7).
- **Agent path joins the Tier authorization model** — `ledger.Agent` gains a `tier` field; undeclared agents default to Tier 2 (fail closed) and are rejected by `defense.Authorize` before the adapter subprocess is spawned; explicit `tier: 1` cards run without approval (c26b11e, P1-15).
- **Secret-file hardening** — configs containing `api_key` / `shared_secret` / `panel_token` are auto-chmod'd to 0600 with a startup warning preferring env vars (`OPENPANDA_SHARED_SECRET` / `OPENPANDA_PANEL_TOKEN` / `OPENPANDA_MODEL_API_KEY`); chmod failure warns without blocking (e5de650, P1-19).
- **Interpreter `-c` classification is whitelist-based** — only provably pure-output code (echo/print/console.log…) stays Tier 1, everything else is Tier 2 (38186af, P1-14).
- **Panel defaults to loopback** — `127.0.0.1:7840`; non-loopback binds warn about plain HTTP (3c7e8f4, P1-24).
- **Deployment baseline documented** — plain `ws://` only over loopback/Tailscale/trusted LAN, TLS reverse proxy + `wss://` across the public Internet (6f2c8d5, C1).
- **Hard memory wall between personal memory and workspace sessions** — AskTurns previously injected Hermes personal memory into every classification, including session conversations bound to a project worktree, so "user prefers dark themes" could leak into a code task spawned from that chat. A pinned workDir now marks a workspace conversation and no personal memory is loaded at all; the panel also pins non-repo sessions to the work path, so every session counts as workspace-scoped. Project memory still reaches execution via ContextPack on the executing node, never the entry prompt; a regression test pins both directions (da9c9e1).
- **OpenAI wire format alongside Anthropic** — the entry model speaks both `/v1/messages`-compatible and OpenAI-compatible endpoints, with streaming completions on both paths (c36cad1).

### Fixed

- **Mutual-dial connection flap** — when two nodes dialed each other simultaneously (the common `peers:`-on-both-sides deployment), each side held one outbound and one inbound conn to the same peer; the second registration closed the first, whose reconnect loop redialed a second later and displaced the other side's conn in turn — an endless ~1s connect/disconnect cycle that churned the capability directory offline/online. Fix: deterministic tie-break in `ensurePeer` — the conn initiated by the lexicographically smaller node id wins, both sides compute the same winner, exactly one TCP conn survives; `MaintainPeer` now blocks until the edge dies instead of hot-redialing (fbb4f9e, regression test `TestMutualDialDedup`).
- **Wire-protocol authorization gaps** — `handleResult`/`handleDecline`/`handleAccept` verify the sender is the current executor (`DispatchTarget` from `EvDelegate` audit events; empty `AttemptID` rejected); `handleContextAck` verifies the sender is the `context_fetch` target; CAS state guards on Accept/Cancel/Approve/Reject close TOCTOU races; `waiting_context` always carries a lease; local-exec failures terminalize instead of leaving zombies (a6fc1c2, P1-1/2/3/4/6/8/9/11).
- **Command-classification bypasses** — `env -S` values recursively classified; `php -r` scanned; `find -exec`, `tar --checkpoint-action`, `git push/commit` and friends fail-closed to Tier 2; `make`/`ssh` join the destructive table (38186af, P1-12/13).
- **Process-group management + adapter hard timeout** — Unix `Setpgid` with whole-group kill on cancel, Windows `taskkill /T`; adapters wrapped in a 630s `context.WithTimeout` (exit 124), no orphaned grandchildren (38186af, P1-17/18).
- **Memory-system injection channels** — atomic writes for Hermes/Projects/skills/dream-last-deep (`util.WriteFileAtomic`); `Projects.Save` mutex; external input tainted `[ext]` and never promoted by Deep dreaming; memory injection fenced in `<memory_data>` with a data-not-instructions preamble; daily-entry newline sanitization (3c7e8f4, P1-20/21/22/23, P2-16).
- **CLI foot-guns** — unknown subcommands exit 2 instead of starting the daemon (`panda statsu` no longer launches a resident daemon); hand-rolled `dirOf` replaced by `filepath.Dir` (3c7e8f4, P1-25/26).
- **Cancel propagation** — `task_cancel` now forwards downstream to executing nodes (dispatch target recovered from `EvDelegate`), cascading hop-by-hop along the delegation chain; shared `Core.CancelTree`/`finishCancel` for CLI and wire paths (66b265d, P2-3/P2-7).
- **Transactional state writes** — TaskStore state UPDATEs and audit-event INSERTs commit in one transaction (`applyCAS`, `applyState`, Accept/Decline/Approve/Reject/Cancel/CreateWithID); ctxstore upsert + cap eviction are transactional too, with a concurrent-Put regression test (bcbf156, P2-1/14).
- **Voice wake defaults** — the default keyword is now a real built-in per backend (`hey_jarvis` for openwakeword, `porcupine` for pvporcupine); previously the default `hey_panda` threw on boot (2e72c8c, P2-21).
- **Slow-DoS protection** — hello timeout plus global/per-IP connection limits (`max_connections`, `max_connections_per_ip`) (6f2c8d5, A2).
- **MCP client hard timeout** with process-kill fallback (6f2c8d5, A4).
- **Comprehensive-check sweep (D1–D32 + P1–P3)** — delegation orphans terminalized, forward copies leased, CAS guards on ForceFail/CompleteFromRemote/FailFromRemote, `PreferredNode` bound to explicit `spec.node`, hello HMAC bound to a 5-minute timestamp window, NetworkGuard allowlist pinned to configured endpoints, Redact covers JSON-quoted keys, TierFromCommand normalizes paths/`.exe`, bounded subprocess output capture (8MiB), and more (75b98c8).

### Planned follow-ups (deferred)

Deliberately parked for after v0.0.1 — tracked here so they stay visible:

- **Keyboard shortcuts** — global hotkeys for the console (new chat, quick
  task, view switching).
- **Browser integration** — a companion browser surface for the assistant.
- **Git surface** — first-class git views (branch state, history, remotes)
  in the console.
- **Worktree management** — list/prune/inspect chat worktrees from the
  console instead of only on chat deletion.
- **Personalization** — user-tunable assistant personality & presentation
  preferences.
- **Web search caching** — cache layer for agent web searches to cut repeat
  fetches and latency.
- **Reasoning effort tiers** — expose low/medium/high reasoning strength as
  a per-task setting.
