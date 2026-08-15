# Changelog

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
