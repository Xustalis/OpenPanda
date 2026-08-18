# Changelog

## [Unreleased]

Thirteen commits since v0.0.1 (Sprint 1 batches 1–3, Sprint 2 paper mechanisms, P1/P2 hardening, Sprint 3 batch). All gates green throughout: build / vet / full tests / `-race` / cross-compile.

### Added

- **Sprint 2 paper mechanisms (ATC-MARL mapping)** — `internal/scheduler/score.go`: DCPS weighted scoring (`0.4·resource_efficiency + 0.3·user_priority + 0.2·scheduler_tier + 0.1·wait_time`) discounted by TMB heartbeat freshness (`exp(-λ·Δt)`, 30-minute half-life); capacity-driven accept/decline via `MaxConcurrent`; heartbeats now carry live `CurrentTasks`, closing the data loop for both mechanisms (543801f).
- **Decline auto-reroute** — tasks persist their `requires` capability set (`requires_json` column, both submit and delegate paths); a declined task re-runs DCPS scoring excluding historical decliners (`DeclinedBy` from `EvDecline` audit events) and re-dispatches to the next-best node (dad4f04, P1-5).
- **`panda metrics [--csv]`** — export delegation metrics; **`panda audit verify [--task <id>]`** — verify the `prev_hash` chain of the global audit log or one task's event timeline (6f2c8d5).
- **PRAGMA `user_version` driven SQLite migrations** (6f2c8d5, A1) and a **`prev_hash` audit chain** for `task_events` and `audit_log` (6f2c8d5, A3).
- **`PANDA_WAKE_KEYWORD`** env override for the voice wake word; openwakeword can still point at a custom `.tflite` via `PANDA_WAKE_MODEL` (2e72c8c).

### Changed

- **Peer reconnect replaces stale connections** — a new conn from the same authenticated identity swaps into the registry (old conn closed outside the lock), and `handleHello` re-greets on replacement; registry removal matches by conn pointer (befa3bd, P1-7).
- **Agent path joins the Tier authorization model** — `ledger.Agent` gains a `tier` field; undeclared agents default to Tier 2 (fail closed) and are rejected by `defense.Authorize` before the adapter subprocess is spawned; explicit `tier: 1` cards run without approval (c26b11e, P1-15).
- **Secret-file hardening** — configs containing `api_key` / `shared_secret` / `panel_token` are auto-chmod'd to 0600 with a startup warning preferring env vars (`PANDA_SHARED_SECRET` / `PANDA_PANEL_TOKEN` / `PANDA_MODEL_API_KEY`); chmod failure warns without blocking (e5de650, P1-19).
- **Interpreter `-c` classification is whitelist-based** — only provably pure-output code (echo/print/console.log…) stays Tier 1, everything else is Tier 2 (38186af, P1-14).
- **Panel defaults to loopback** — `127.0.0.1:7840`; non-loopback binds warn about plain HTTP (3c7e8f4, P1-24).
- **Deployment baseline documented** — plain `ws://` only over loopback/Tailscale/trusted LAN, TLS reverse proxy + `wss://` across the public Internet (6f2c8d5, C1).

### Fixed

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

## v0.0.1 — 2026-08-15

First release. Local git tag + build artifacts (`dist/`, `make release`); no remote host yet.

### Highlights

- **Headless kernel daemon + CLI panel** — `panda status/queue/task/approve/reject/cancel/logs/skill`. The legacy PWA panel is a frozen `webui/` sidecar.
- **Transport auth (P0-1)** — shared-secret HMAC handshake, Origin check, server-side approval records, and hello `NodeID==From` validation. The WS listener refuses to start without `network.shared_secret`.
- **Multi-node P2P delegation** — idempotent `task_id` / unique `attempt_id`, context-tiered transfer (pointer / summary / full), and cascade cancel.

### Added

- `make release` — version-tagged binaries for darwin-arm64 / linux-arm64 / linux-amd64 / windows-amd64 into `dist/`, version injected via `-X main.version`.
- `LICENSE` (MIT).
- **Scope-drift host-state ignore** — drift detection now ignores the node's own bookkeeping paths (SQLite `-wal`/`-shm` journals, memory/skills trees, the agent CLI's own config dir), so a task that only writes host-side effects completes instead of pausing for scope drift.
- **OpenCode adapter free-model default** — runs opencode's built-in free model when no provider/model id is set; no API key required.

### Fixed

- CancelCascade could recurse forever on a `parent_id` cycle; now guarded by a visited set (P2-8).
- A task cancelled or lease-expired while paused in `waiting_context` leaked its pending-context entry (P2-7).
- The entry model's multi-`tool_use` responses dropped accompanying text and any `tool_use` after the first; they are now surfaced and replayed to the model (P2-21).

### Docs

- Documented `network.shared_secret` in `config.example.yaml` and all five READMEs; loopback test configs (`testdata/mac-config.yaml`, `testdata/opi-config.yaml`) now form a real P2P network.
