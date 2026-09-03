# Docs

OpenPanda project docs that are intentionally part of the public source tree.

## User guides

- [`status.md`](status.md) — where the project stands: capability-by-capability status, what is verified vs. only built, the two plan entry points, and the known limits (trust model, i18n, GPIO, multi-hop). (中文)
- [`install.md`](install.md) — install guide: one-line script, Homebrew, Windows, source builds, auto-start services, uninstall/purge, release process, install troubleshooting. (中文)
- [`faq.md`](faq.md) — FAQ by scenario: first steps, model configuration errors, agent adapters, task scheduling (tier-2 authorization, review, scope drift), multi-device networking, data locations, upgrades. (中文)
- [`protocol.md`](protocol.md) — P2P bus protocol distilled from `internal/bus`: message envelope, hello/heartbeat/delegation/result/artifact frames, frame caps, and the HMAC shared-secret auth. (中文)
- [`../SECURITY.md`](../SECURITY.md) — trust model (single shared secret, plain ws://), deployment red lines, and the vulnerability reporting channel.
- [`testing/distributed-lab-plan.md`](testing/distributed-lab-plan.md) — the three-node interop scenarios gating each release.

## Internal documents

- [`plans/roadmap-desktop-and-packaging.md`](plans/roadmap-desktop-and-packaging.md) — high-level roadmap for the desktop client & packaging pipeline.
- [`plans/v0.0.4-followup-optimizations.md`](plans/v0.0.4-followup-optimizations.md) — post-v0.0.4-beta code-audit findings (installer P0 bug, model-call layer, adapter harness) and the v4 improvement plan with evidence.
- [`plans/ux-and-positioning-redesign.md`](plans/ux-and-positioning-redesign.md) — positioning rework ("the conductor") and staged UX fixes; P0/P1 done, P2 pending.
- [`superpowers/specs/2026-08-19-task-panel-redesign-design.md`](superpowers/specs/2026-08-19-task-panel-redesign-design.md) — design spec for the resource-aware local queue scheduler, kanban board, and session integration.
- [`author/reports/Agent调度与上下文管理审查-2026-09-03.md`](author/reports/Agent调度与上下文管理审查-2026-09-03.md) — code review of the agent scheduling chain (route/queue/run/supervise/retry) and subagent context management: verified issue list (P1 plain-task cross-node zero file context, P1 silent context degradation, P2 followup replacing intent, plus P2/P3s) with file:line evidence, what is solid, and the W1-W6 fix plan. (中文)
- [`author/reports/项目与对话关联修复方案-2026-09-03.md`](author/reports/项目与对话关联修复方案-2026-09-03.md) — the broken "enter project" jump, and why sessions have no project association at all: diagnosis with code evidence plus the Web+CLI fix plan (Session.project field, project-scoped routing, REPL conversation sharding, delete semantics). (中文)
- [`author/reports/全面分析与修复方案-2026-08-28.md`](author/reports/全面分析与修复方案-2026-08-28.md) — user-reported pain points (capability editing, device onboarding, scheduling UX) fully diagnosed with code evidence, plus the staged fix plan. (中文)
- [`author/guides/DEVELOPMENT.md`](author/guides/DEVELOPMENT.md) — historical hands-on developer guide (2026-08-18 snapshot; see the banner inside). (中文)
