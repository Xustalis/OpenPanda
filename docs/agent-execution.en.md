# Agent and subagent models and concurrency

Open **Settings → Agents** to edit root/child CLI models, per-agent concurrency, worker concurrency and delegation limits. Replace the illustrative model IDs below with models actually available to each CLI.

```yaml
injection:
  model: never
routing:
  max_concurrent: 6
  agent_concurrency:
    codex: 2
    claude_code: 2
  agent_models:
    codex: [my-root-model]
    claude_code: [my-claude-model]
  subagent_models:
    codex: [my-child-model]
    claude_code: [my-child-claude-model]
delegation:
  enabled: true
  max_concurrent: 3
  fanout: 3
  max_children: 12
  max_depth: 2
```

- `routing.max_concurrent` caps active agent calls in this worker and updates its queue admission. Zero inherits capability-card capacity, not unlimited execution.
- `routing.agent_concurrency` adds per-agent caps; omit an entry for no extra cap. Keys are card names or adapter filenames. Adapter-key caps aggregate aliases using that adapter. When both name and adapter caps exist, both apply.
- `routing.subagent_models` applies to OpenPanda-created children. A missing entry inherits the root selection; explicit `[]` restores the child CLI default. Clearing a UI field restores inheritance.
- `delegation.max_concurrent` caps active child calls across all parents; zero uses only the shared worker/agent caps.
- `fanout` bounds children spawned-and-awaited by one parent, not their whole execution lifetime. Children still running after an await timeout continue to consume execution capacity.
- `max_children` caps cumulative children per parent. `max_depth` caps spawn generations; roots have depth zero.
- Lowering a cap drains existing calls rather than killing them. A parent waiting for children occupies no agent-call slot.
- These are active adapter-call limits in one worker, not cluster-wide quotas or controls on vendor-internal agents/idle persistent sessions. Normal deployments retain one queue owner; independent workers do not share a hard process-wide semaphore.
- In a project-backed multi-child batch with effective `fanout` above one, siblings that omit `paths`, `isolate` and `workdir` are automatically isolated and become eligible to run concurrently, subject to worker/agent/delegation limits; their changes use the existing conflict-aware merge. A lone unscoped task still locks the whole project. Explicit disjoint `paths` avoid the copy; overlapping paths in a shared worktree remain serialized.

These are native CLI model choices, not new endpoint/key injection. Existing precedence is preserved: injected models and explicit CLI model environment pins outrank the candidate list. The example opts into native credentials with `injection.model: never`; do not copy that setting onto a node that requires Panda-supplied credentials. Keep using `injection.agents` for endpoint/credential overrides. Single-model CLIs use the first candidate; OpenCode resolves candidates against its live catalogue. ZCode accepts `provider/model`; an unqualified model needs an existing provider selection. Custom generic command templates need a `{model}` placeholder.

Saving updates this embedded worker immediately. Other local workers sharing the configuration refresh approximately every second and before execution. In-flight calls keep their model snapshots.

```sh
panda config routing set agent_models codex my-root-model
panda config routing set subagent_models codex my-child-model
panda config routing set max_concurrent 6
panda config routing set agent_concurrency codex 2
panda config delegation set max_concurrent 3
panda config delegation set fanout 3
panda config routing get --json
```

API: `GET /api/settings/execution`, `PUT /api/settings/execution` (Bearer token).

## Execution deadlines and context

- Candidate failover, transient retries and credential rescue share one round deadline (task override or global timeout, plus shutdown grace). Cancellation stops new candidates. A panic settles its own attempt and releases execution resources.
- OpenCode provider/session failures end the call immediately; terminal idle after an answer also finishes a CLI that keeps its pipe open. Initial idle, tool failures and foreign-session events do not complete this task.
- Managed Codex tasks retain sessions on disk for interruption and supervision resume; standalone diagnostics keep their ephemeral default. Both `thread.started` and legacy session events are supported.
- Agent prompts have 12,000 / 32,000 / 64,000-byte soft budgets by complexity; unknown complexity uses 32,000. User requirements, acceptance criteria, scope and protocol constraints survive intact. Skill bodies and degraded indexes share the remaining budget. Personal memory indexes select at most six relevant topics within 4,000 bytes, preserving project/personal isolation. Command references are task-selective; `panda help` retains the full catalog.
- Overflow retries halve the original budget, capped at 32,000 bytes. Run notes can shrink to 12,000 bytes with explicit omission markers and valid UTF-8. Resume retains full task requirements for cold fallback. `exec_agent_start` records `prompt_bytes` and `context_budget_bytes`; these are bytes, not tokens.
- Verdict/digest calls use a separate short-output configuration of the same model: at most 2,048 output tokens, one transport retry and a request to disable extended thinking (provider behavior varies). The judge has a 45-second deadline; failures still park for human review. Conversational and agent model settings are unchanged. Read-only `model_list` serves configured/active model queries without an agent task.

Deploy the updated binary and adapters to activate these changes; config hot reload does not replace running program code.
