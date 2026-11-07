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
- Parallel writes still require disjoint `paths`, different `workdir` values or `isolate` worktrees. Overlapping/unknown scopes remain serialized.

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
