# OpenPanda Architecture & Design Notes

This directory holds the public architectural reference for OpenPanda, derived from the current implementation and cross-linked with the user and protocol guides in [`docs/`](../README.md).

## Documents

- [Routing, Scoring & Negotiation](routing-and-scheduling.md) — node capability scoring, heartbeat freshness decay, capacity-driven delegation, multi-hop graph routing, DTN contact plans, and peer scope lock negotiation.
- [Execution, Defense & Skills](execution-and-defense.md) — unified entry classification, three-layer commander dispatch, actuators, token and spawn-depth budgets, clarification loops, defense layers, permission tiers, and progressive skill loading.
- [Self-Evolution Pipeline](self-evolution.md) — opt-in signed patch-set queue, rebase/merge workflow, release-gate checks, staged swap, and rollback guarantees.

## Companion Guides

- [P2P Bus Protocol](../protocol.md) ([English](../protocol.en.md)) — WebSocket and sealed-UDP transports, session AEAD, binary artifact frames, hello authentication, epidemic directory sync, blind transit relay, NAT hole punching, and `panda://join`.
- [Three-Layer Routing Contract](../routing-layers.md) — device pinning (`--nodes` vs `--preferred`), executor adapters vs coordinator models, and queue-forward fallback.
- [Agent Execution](../agent-execution.md) ([English](../agent-execution.en.md)) — native model configuration, per-agent concurrency, stage contracts, and parallel workspace scoping.
- [Memory Lifecycle](../memory-lifecycle.md) ([English](../memory-lifecycle.en.md)) — two-layer memory (`USER.md` / `MEMORY.md`), project isolation wall, daily logs, and Dreaming consolidation.
- [Conversation, Plan & Goal](../conversation-plan-goal.md) ([English](../conversation-plan-goal.en.md)) — persistent read-only planning, version confirmation, and background goals.
- [Security Model](../../SECURITY.md) — trust zones (`mesh` / `guarded` / `open`), credential redaction, audit log signing, and OS sandboxing.
