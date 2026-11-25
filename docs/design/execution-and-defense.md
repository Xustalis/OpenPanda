# Execution, Defense & Skill Lifecycle

Public architectural summary of [`internal/entry/`](../../internal/entry/), [`internal/commander/`](../../internal/commander/), [`internal/defense/`](../../internal/defense/), [`internal/security/`](../../internal/security/), and [`internal/skills/`](../../internal/skills/). See also [Agent Execution](../agent-execution.md) and [Security Model](../../SECURITY.md).

## Unified Entry Classification & Context Packing

[`internal/entry/`](../../internal/entry/) classifies each user turn into one of five structured outcomes (`answer`, `tool_call`, `task`, `plan`, `clarify`) without performing direct side effects in the model:

- **Directive robustness (`internal/entry/router.go`)**: prose that announces a structured directive JSON envelope but fails validation returns `ErrDirectiveProse` rather than leaking raw JSON as an answer.
- **Controlled system tools (`internal/askengine/systemtools.go`, `tools.go`)**: read-only host/context probes (`time_now`, `weather`, `task_submit`, etc.) execute inside Go core under strict argument validation.
- **Clarification loop (`PANDA_QUESTION` / `KindClarify`)**: when a large or ambiguous task requires a user choice, execution suspends with bounded multiple-choice options and resumes once answered (`internal/askengine/askengine.go`, `internal/core/run.go`).
- **Context transfer (`internal/entry/spec.go`, `internal/ctxstore/store.go`)**: `context_type` distinguishes `none`, `command`, and `file`. Workspace snapshots and context blobs are content-addressed in `ctxstore` (bounded by node tier LRU limits) and carried across peer delegations.

## Three-Layer Commander & Actuator Dispatch

[`internal/commander/`](../../internal/commander/) resolves task execution across three tiers (`native > agent > manual` unless an explicit executor is pinned):

1. **Native (`native.go`)**: direct shell or built-in driver execution via `executil`, gated by the defense permission tier.
2. **Agent (`adapter.go`)**: bundled Python adapters speaking the `_harness.py` JSON line protocol.
3. **Manual (`commander.go`)**: human-in-the-loop review queue (`StateReview`) for tasks requiring operator action.

Software harnesses and hardware peripherals are unified through `ActuatorProfile` (`internal/ledger/capability.go`) and `ActionSpec` (`internal/commander/actuator.go`): capability cards declare `actuators` with parameterized `argv` templates, and the executor substitutes and re-validates arguments locally before invoking the driver binary.

## Defense Chain & Permission Model

[`internal/defense/`](../../internal/defense/) and [`internal/security/`](../../internal/security/) enforce layered safeguards around agent loops:

- **Deterministic Tier Gate (`permission.go`)**:
  - `TierReversible` (Tier 1): reversible reads/edits allowed under `auto` mode.
  - `TierIrreversible` (Tier 2): irreversible actions (e.g., `git push`, destructive ops, download-to-disk pipelines, GUI input) require explicit user authorization unless `bypass` mode is active.
  - Five conversation permission modes (`auto`, `manual`, `edits`, `plan`, `bypass`) govern execution per thread and travel with every task spec.
- **Circuit Breaker (`circuit.go`)**: tracks consecutive failures per dependency key (default threshold `3`, cooldown `30s`) with `closed -> open -> half_open` transitions so failing dependencies stop burning tokens.
- **Scope Drift Detection (`scope.go`, `snapshot.go`)**: captures before/after workspace snapshots around agent turns and flags edits outside the declared `scope` roots.
- **Monotonic Progress & Loop Breaker (`loop.go`, `statehash.go`, `peer_dispatch.go`)**: hashes worktree state across rounds (`GitFingerprint` / content hashes) to detect logical oscillation or zero-progress loops and stop runaway retries.
- **OS Sandboxing (`internal/security/sandbox.go`)**: applies OS isolation (`bubblewrap` on Linux, AppContainer + restricted token on Windows; see [Windows Sandbox](../windows-sandbox.md)) and refuses unconfined execution when `standard` or `strict` isolation is requested without a working backend.

## Progressive Skill Lifecycle

[`internal/skills/`](../../internal/skills/) distills repeatable workflows into `SKILL.md` playbooks:

- **Trigger & Quality Gate (`trigger.go`, `tracker.go`, `generator.go`)**: tracks task outcomes per class and proposes a `StatusPending` skill after clearing the quality threshold (`>= 3` runs with `>= 70%` success).
- **Scoped Isolation (`skill.go`, `lifecycle.go`)**: project-scoped and device-scoped skills stay segregated so one project's skills cannot leak into another; pending skills require explicit approval before becoming active, and dormant skills decay in ranking (`loader.go`).
- **Progressive Loading (`internal/core/prompt.go`)**: prompts receive a compact skill index and load full `SKILL.md` bodies only for matched skills.
