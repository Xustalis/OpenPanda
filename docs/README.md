# Docs

OpenPanda project docs that are intentionally part of the public source tree.

## User guides

- [`agent-execution.md`](agent-execution.md) — agent/subagent native model selection, concurrency/delegation limits, hot reload and safe parallel scope rules. (中文 · [English](agent-execution.en.md) · [日本語](agent-execution.ja.md) · [Español](agent-execution.es.md) · [Deutsch](agent-execution.de.md))

- [`status.md`](status.md) — where the project stands: capability-by-capability status, what is verified vs. only built, the two plan entry points, and the known limits (trust model, i18n, actuator real-hardware validation, multi-hop). The 2026-11-02 entry covers the v0.1.4 run loop (workspace scout, evidence-based completion, clarification) and interaction surface (side channel, rewind, task rail, lifecycle hooks). (中文 · [English](status.en.md))
- [`install.md`](install.md) — install guide: one-line script, Homebrew, Windows, source builds, auto-start services, uninstall/purge, release process, install troubleshooting. (中文 · [English](install.en.md))
- [`faq.md`](faq.md) — FAQ by scenario: first steps, model configuration errors, agent adapters, task scheduling (tier-2 authorization, review, scope drift), sessions and interaction (side channel, rewind, file viewer, artifact preview, workspace map, hooks), multi-device networking (incl. the computer-use boundary), data locations, upgrades. (中文 · [English](faq.en.md))
- [`protocol.md`](protocol.md) — P2P bus protocol distilled from `internal/bus`: WS + sealed-UDP transports, negotiated per-session AEAD and binary data frames, message envelope, hello/heartbeat/delegation/result/artifact/DTN/punch/pair frames, frame caps, the HMAC-membership + Ed25519-identity hello auth, LAN discovery beacons, trust-zone clamping, and the mesh-only rule for screen-capture tasks and their output. (中文 · [English](protocol.en.md))
- [`../SECURITY.md`](../SECURITY.md) — trust model (single shared secret, negotiated session AEAD + gated plaintext fallback, encrypted UDP/DTN planes, mesh-exclusive display content), deployment red lines, and the vulnerability reporting channel. (中文 · [English](../SECURITY.en.md) · [日本語](../SECURITY.ja.md) · [Español](../SECURITY.es.md) · [Deutsch](../SECURITY.de.md))

Internal design notes, roadmaps, test runbooks and point-in-time reports are
maintained outside the repository, under the maintainer's local `author/docs/`
tree. They are not tracked by git and are not part of the public source tree.
