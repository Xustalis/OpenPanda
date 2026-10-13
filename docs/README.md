# Docs

OpenPanda project docs that are intentionally part of the public source tree.

## User guides

- [`status.md`](status.md) — where the project stands: capability-by-capability status, what is verified vs. only built, the two plan entry points, and the known limits (trust model, i18n, actuator real-hardware validation, multi-hop). (中文)
- [`install.md`](install.md) — install guide: one-line script, Homebrew, Windows, source builds, auto-start services, uninstall/purge, release process, install troubleshooting. (中文)
- [`faq.md`](faq.md) — FAQ by scenario: first steps, model configuration errors, agent adapters, task scheduling (tier-2 authorization, review, scope drift), multi-device networking, data locations, upgrades. (中文)
- [`protocol.md`](protocol.md) — P2P bus protocol distilled from `internal/bus`: WS + sealed-UDP transports, negotiated per-session AEAD and binary data frames, message envelope, hello/heartbeat/delegation/result/artifact/DTN/punch/pair frames, frame caps, the HMAC-membership + Ed25519-identity hello auth, LAN discovery beacons, and trust-zone clamping. (中文)
- [`../SECURITY.md`](../SECURITY.md) — trust model (single shared secret, negotiated session AEAD + gated plaintext fallback, encrypted UDP/DTN planes), deployment red lines, and the vulnerability reporting channel.
- [`testing/distributed-lab-plan.md`](testing/distributed-lab-plan.md) — the three-node interop scenarios gating each release.

## Internal documents

- [`plans/roadmap-desktop-and-packaging.md`](plans/roadmap-desktop-and-packaging.md) — high-level roadmap for the desktop client & packaging pipeline.
- [`plans/roadmap-v0.0.10-multi-device.md`](plans/roadmap-v0.0.10-multi-device.md) — the v0.0.10 multi-device collaboration roadmap: delegation-parity, joining & identity, actuator scheduling, fleet observability, trust tail, and the validation gates; per-item status tracked inline.
- [`plans/design-trust-zones-agent-economy.md`](plans/design-trust-zones-agent-economy.md) — design draft for per-peer trust tiers (mesh/guarded/open), agent dialogue, and the decentralized work market; partially implemented as an experimental feature. (中文)
- [`plans/design-task-message-board.md`](plans/design-task-message-board.md) — the inter-task message board: sibling tasks under one plan or parent exchange bounded slot-based notes (TMB/ALC/DCPS engineered, no learning component), the cross-node owner fan-out path, and what is deliberately not implemented. (中文)

## Historical reports

Point-in-time investigation and fix records; their items have landed — see
[`status.md`](status.md) for current state.

- [`reports/confirmed-issues-fix-report-2026-09-09.md`](reports/confirmed-issues-fix-report-2026-09-09.md) — confirmed CLI/TUI, approval/cancel, web-session and node-identity issues with root causes and the implemented fix list. (中文)
- [`reports/irreversible-approval-findings-2026-09-09.md`](reports/irreversible-approval-findings-2026-09-09.md) — irreversible-operation approval gate: findings and requirements feeding the approval redesign. (中文)
- [`reports/修复总结_2026-09-20.md`](reports/修复总结_2026-09-20.md) — fix summary for the 2026-09-20 stability/security pass. (中文)
- [`reports/PANDA系统架构优化与CLI交互重构分析汇报.md`](reports/PANDA系统架构优化与CLI交互重构分析汇报.md) — architecture & CLI interaction analysis report. (中文)
