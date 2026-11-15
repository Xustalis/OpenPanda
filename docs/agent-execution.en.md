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

## Investigation tasks and execution evidence

- Task specs may carry `task_kind`: `code_question`, `investigation`, `edit`, or `operations`. Omission preserves legacy tasks; the label grants no permission. Investigation/code questions use read-only `plan`; edits still obey conversation permissions. Deep repository investigations belong in a coding harness; a failed dispatch must not turn entry-level directory orientation into a completed review.
- Investigations require implementation/caller/relevant-test evidence, coverage limits and unverified items, not mandatory builds/tests for static analysis. Bounded/truncated searches cannot prove repository-wide absence. Edits report relevant verification results; operations confirm the requested postcondition. Read-only tasks do not automatically run acceptance commands.
- Each agent attempt records a worker-local `execution_environment`: cwd, git commit/branch/dirty state (including untracked files), and bounded content fingerprints of root `AGENTS.md`/`CLAUDE.md`. This is not a full rules index or dependency preflight; rule bodies are not logged and project commands are not run. Failed git probes remain unknown.
- A trusted process boundary records `adapter_execution_profile`: requested model preference, injection/tools policy, restriction/consent, time limits, resume request and registry-declared capabilities. Later persistent-session turns retain launch settings. This is not native CLI configuration confirmation: unconfirmed native model/effort/version remain unknown. Profiles exclude prompts, credentials, provider URLs, MCP bodies and command templates.
- These are worker-local task events, without a dedicated new UI or cross-node environment sync. They do not imply a complete claim-evidence ledger or completion verifier. Deploy the new binary for these changes to take effect.

## Routing, permissions and plan boundaries

- Conceptual questions can still use the fast path. Concrete code symbols/paths bypass tool-free answers and require actual source reads. Deep repository investigations and in-context deep follow-ups (including Side) require a coding harness; missing dispatch, queued/approval-pending receipts and failures cannot become completed work through convergence summaries. Routing does not switch native CLI reasoning effort or verify each claim against evidence.
- Global tools policy is a ceiling; tasks may only narrow it. `extended` grants no additional OS permissions. Codex uses `workspace-write` normally and `read-only` for restricted work, not `danger-full-access` based on tools policy. Typed investigation/code questions are restricted even with persisted consent; native commands without a read-only sandbox cannot serve these tasks.
- Entry tools in a `plan` conversation need an explicit read-only declaration; local shortcuts obey the same boundary. `task_submit`/`task_budget` retain restricted delegation. Unknown MCP tools lack the declaration and are denied by default; trusted MCP read-only capability configuration is not implemented. Reading skills observes existing content without implicitly installing built-ins.
- Multi-stage plans preserve stage `task_kind`, conversation permissions and agent roster, using a bounded source-workspace snapshot as stage input. Missing artifact storage, packing failures or omitted files fail before task creation. Remote stages use the receiving node's own directory, never the origin's absolute cwd. Outputs retain artifact handoff semantics and **are not automatically written or merged back into the original checkout**.
- Workspace-aware classification (directory map or read tools) bypasses path-only persistent caches so uncommitted edits cannot reuse stale judgments. Verification observations require completed, non-error command-tool results and exact program/argument matches, not `echo`, substrings, unfinished or failed commands. Complex shell compositions are not inferred. These observations are not an independent acceptance pass.
- Side is a separately recorded conversation with the same tool surface (dispatch included) and permission/approval gates as the main thread. Main-thread history is reference only; side turns neither append to nor compact it. Fast/deep routing and model reasoning configuration are separate layers, not a guarantee of native-tool reasoning parity.
- Remote plans still carry project memory without attaching another project checkout; source snapshots and predecessor-input ordering remain intact.

## Stage acceptance, blockers and scheduling

- Generated and YAML stages may specify `acceptance`, `success_definition`, `tools_policy` and `max_turns`; paths, policies and the 0..200 turn limit are validated. Tool policy never grants authorization or widens a restricted run.
- A supervisor `blocked` verdict means external conditions or input must change; the task parks for changed input and approval cannot make known unfinished work complete. Continue or reject it.
- Independent same-machine branches may run concurrently subject to dependencies, capacity and resource locks. Within a plan and priority band the remaining weighted critical path determines order; manual queue order wins.

## Conversation recovery and execution feedback

The console preserves provider-supplied reasoning summaries separately from answer text. They are display metadata, never fabricated reasoning and never replayed to the model. Active answer text, reasoning and progress are checkpointed for refresh/reconnect; Stop cancels the exact operation ID, including a recovered or orphaned operation. A stopped turn retains the partial answer and summary already received.

Context compaction keeps the original transcript and records a summary-prefix cursor for bounded model replay. Older installations may already have removed compacted turns or never stored reasoning; this update cannot reconstruct those missing records. Native CLI credentials/endpoints remain unchanged. An injected Codex provider uses the Responses wire contract. Provider billing failures require changed credentials or balance after permitted rescue, not repeated task replay.

After completion, the original conversation keeps a separate delivery report (up to 24,000 characters, with an explicit link to original task output when shortened); model replay uses only the short digest. Real action previews, changed paths, independent verification and remaining-work caveats are visible by default. Full reports and timelines expand on demand; `done` never implies verified when checks were not recorded. Legacy reports recover output only while it still belongs to their attempt, so a retry cannot rewrite an older handoff. TUI foreground results and background completions both expose outcome, steps, verification and attention sections.

- Parents waiting for children release queue execution capacity and workspace locks while retaining heartbeat, cancellation and device exclusion. They reacquire capacity and locks before resuming. The durable wait marker belongs to one attempt and is cleared by retry, cancellation and restart recovery.
- Isolated merge failures, missing baselines and file conflicts block in review requiring changed input, preserving the workspace. Approval alone cannot certify delivery. Siblings share one immutable source snapshot per batch; explicit work directories keep their meaning.
- A plan publishes its complete graph in one transaction. Stage artifacts carry versioned kernel-generated causal metadata: edits and deletions survive joins, descendants supersede ancestors, divergent concurrent edits conflict, and equal results merge. New plans only dispatch to nodes advertising protocol support; multiple legacy snapshots without trustworthy baselines block explicitly. Plan outputs do not automatically write back to the original checkout.
