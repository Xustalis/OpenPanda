# Docs

OpenPanda project docs that are intentionally part of the public source tree.

## User guides

- [`status.md`](status.md) — where the project stands: capability-by-capability status, what is verified vs. only built, the two plan entry points, and the known limits (trust model, i18n, GPIO, multi-hop). (中文)
- [`install.md`](install.md) — install guide: one-line script, Homebrew, Windows, source builds, auto-start services, uninstall/purge, release process, install troubleshooting. (中文)
- [`faq.md`](faq.md) — FAQ by scenario: first steps, model configuration errors, agent adapters, task scheduling (tier-2 authorization, review, scope drift), multi-device networking, data locations, upgrades. (中文)
- [`testing/distributed-lab-plan.md`](testing/distributed-lab-plan.md) — the three-node interop scenarios gating each release.

## Internal documents

- [`plans/roadmap-desktop-and-packaging.md`](plans/roadmap-desktop-and-packaging.md) — high-level roadmap for the desktop client & packaging pipeline.
- [`plans/v0.0.4-followup-optimizations.md`](plans/v0.0.4-followup-optimizations.md) — post-v0.0.4-beta code-audit findings (installer P0 bug, model-call layer, adapter harness) and the v4 improvement plan with evidence.
- [`plans/ux-and-positioning-redesign.md`](plans/ux-and-positioning-redesign.md) — positioning rework ("the conductor") and staged UX fixes; P0/P1 done, P2 pending.
- [`superpowers/specs/2026-08-19-task-panel-redesign-design.md`](superpowers/specs/2026-08-19-task-panel-redesign-design.md) — design spec for the resource-aware local queue scheduler, kanban board, and session integration.
