# Roadmap — v0.0.10 multi-device collaboration

Theme: **多机协作** — delegated work must produce results at least as good as
running the tool natively, the fleet must be easy to assemble and observe,
and scheduling must cover actuators (servos, microphones, compute), not just
agent harnesses. Jarvis is the north star.

Status legend: `[ ]` planned · `[~]` in progress · `[x]` done

## Problem statement (2026-09-28 analysis)

Delegated agent work is structurally weaker than native CLI use. Root causes
found in code:

1. **Context loss** — an anonymous task executes in the node-wide work dir
   (`c.workDir`, an empty sandbox); the remote agent sees no repo, no
   CLAUDE.md. Only project tasks ship a tree. `context_type=file` packs only
   a path *string*, not the bytes. (`internal/core/context.go`,
   `handlers.go:1046`)
2. **Tool-face reduction** — default `tools_policy=minimal` allows only
   `Read,Write,Edit,Bash,Grep,Glob`: no Task sub-agents, WebFetch/WebSearch,
   MCP servers. Native CLI has all of them. (`adapters/claude_code.py:42,65`)
3. **Turn truncation** — `--max-turns 30` default, 600 s adapter timeout;
   real coding tasks get cut mid-work. (`adapters/claude_code.py:58`,
   `internal/commander/adapter.go:468`)
4. **Intent distillation loss** — the wire carries the entry model's
   `Intent`, not the user's words. *Partially mitigated*: the raw prompt is
   already appended to the stored intent at submit (`askengine.go:1784`),
   so it persists and delegates; what remains is that spec.target rewrites
   can still lose nuance upstream of that backstop.
5. **No clarification loop** — `-p` headless mode cannot ask the user; an
   ambiguous delegated task guesses. Native interactive mode asks.
6. **Weak result verification** — the result payload is text-only; no
   files-changed list, no test-run signal, so the judge can only grade prose.
7. **Dishonest routing** — a context-bound task ("fix my repo") can be routed
   to a node that does not have the repo, because scoring has no
   project-residence factor. (`internal/scheduler/score.go`)

## Track 0 — delegation quality parity (release blocker)

Delegated output must be ≥ native output. Acceptance test: a coding task
delegated to a peer produces a patch judged equivalent-or-better to the same
task run locally, measured by the supervise verdict over a fixed task set.

- [x] Q1 **Worktree travels** — a `context_type=file` task with a repo path
      packs its tree (minus derived dirs: .git, node_modules, caches) as an
      artifact input; the executor unpacks into a per-task workspace and runs
      there; the produced tree is packed back as the output artifact.
      Implemented: `attachWorktree` (≤256 MiB, repo-only, skip-list),
      `attachedWorkDir` (traversal-checked per-task dir), `adoptWorktreeOutput`
      (fetch + extract back into the origin checkout). E2E test:
      `TestFileTaskWorktreeTravelsRoundTrip`.
- [x] Q2 **Prompt fidelity** — raw prompt rides in intent at submit
      (`askengine.go:1791`); the acceptance side of the spec now reaches the
      executor too: `taskSpecEnvelope` folds `success_definition` +
      `constraints` into `currentIntent`, so the agent prompt states "done"
      and the supervise judge reads the same criteria. Test:
      `TestAcceptanceEnvelopeReachesPrompt`.
- [x] Q3 **Budget parity** — `tools_policy` / `max_turns` now live on
      `entry.TaskSpecDetail` so they survive `spec_json` marshaling and the
      wire; run() applies them via `commander.WithToolsPolicy` /
      `WithMaxTurns`, and `_harness.read_request` parses `max_turns`.
      Tests: `TestToTaskInputCarriesExecutionOverrides` (+ claude_code.py
      honors `req.max_turns`).
- [x] Q4 **Clarification relay** — implemented as §4.3 on the existing
      review/resume wire instead of a new channel: the agent ends a blocked
      turn with a `PANDA_QUESTION:` line (`prompt.question.hint`, 5 locales)
      → `parseQuestionRequest` strips the marker → `PauseForAnswer` parks the
      task in review (`resume_execution`) → `TaskResultPayload.question`
      carries it to the origin → `panda approve <id> -m "answer"` sends
      `task_resume.answer` → `ResumeApproved` folds the reply into the
      re-run's intent (agent session resumes, so the question's context is
      intact). Tests: `TestClarificationParkAndAnswerResume`,
      `TestClarificationCrossDevice`, `TestParseQuestionRequest`.
- [x] Q5 **Result contract** — `files_changed` (capped 200) rides
      `TaskResultPayload` from the before/after workdir diff; the judge gets
      the same footprint as evidence; `panda task` lists changed paths.
- [x] Q6 **Honest routing** — nodes advertise their project checkouts
      (`CardSummary.projects` → ledger `employee_cache.projects` →
      heartbeats); `scheduler.RouteP`/`RouteAtP` add a residence score
      component; `handleDelegate` + plan dispatch route through the
      project-aware variants. Tests: `residence_test.go`,
      `TestProjectsRoundTrip` (ledger).

**Post-Track-0 review pass** — a full read of the landed code found and
fixed seven issues before they reached a release:

- `parseQuestionRequest` required a word boundary after the marker —
  `PANDA_QUESTIONABLE:` prose was being eaten as a protocol question.
- `attachWorktree` pins the origin row's `work_dir` to the packed dir, so
  `adoptWorktreeOutput`'s return leg cannot land on a divergent
  WorkDir/RepoPath pair.
- `forwardScheduled` + `rerouteDeclined` now call the row-based
  `attachWorktreeFrom`: queued and re-routed file tasks ship their tree
  (the old code pinned them local or forwarded them blind). New tests:
  `TestScheduledFileTaskShipsWorktree`, `TestScheduledFileTaskNonRepoStaysLocal`.
- `filterHostDrift` takes the run's actual workDir — attached/stage dirs
  are deeper than the node root the old signature assumed.
- `_harness.py` clamps `max_turns` at zero — a malformed request can no
  longer pass `--max-turns -1` to the CLI.
- `askengine.Result` now carries `Question` + `FilesChanged` (both were
  computed on the executor and stored on the row but dropped at the
  inline-ask boundary); `panda ask` prints the parked question and the
  `approve -m` answer hint (`cli.ask.question*`, 5 locales).

## Track 1 — joining & identity

- [x] LAN auto-discovery — `internal/core/discovery.go`: a UDP broadcast
      beacon (`panda-beacon/1`, ~150 B, 15 s cadence) on `network.discovery_addr`
      (default `:7837`, `"off"` disables). Datagrams carry id/addr/ver/pubkey —
      no credentials; the pubkey is only a display hint for fingerprint
      comparison, admission still requires the shared secret + signed Ed25519
      hello. `parseBeacon` is the entire trust boundary: magic, size cap,
      host:port shape, hex pubkey. `resolveBeaconAddr` substitutes the source
      IP for unspecified/loopback advertise hosts. Beacons feed
      `pending_nodes` (migration v32), never `employee_cache`: 60 s TTL sweep
      + 64-row cap (stalest evicted — bounded noise under a forged-beacon
      flood). Self-beacons and already-joined ids are skipped; a loopback
      `listen_addr` or an empty shared secret suppresses announcing (a beacon
      from either advertises a join that cannot work) while the listener
      keeps running. `panda nodes` renders the pending section with
      fingerprints; `panda nodes admit <id>` converts a pending row into a
      configured peer (the `/nodes admit` REPL twin live-dials). Discovery
      runs on the daemon AND on the REPL's embedded sched core, so a
      daemon-less seat still hears beacons.
- [x] `panda nodes` shows key fingerprints + verified ✓; `nodes verify <id>`
      stamps human-compared (TOFU) — migration v31 `employee_cache.key_verified`.
      A changed key under an existing row clears the stamp and logs a warning
      (`recordPeerPubKey`): reinstall, rotation and impersonation look alike
      at this layer and none may inherit trust. The self row verifies by
      construction via `recordSelfPubKey`; `EnsureNodeKey` at daemon/engine
      start materializes the identity so a peerless node's own fingerprint is
      visible (it is the value the operator compares against).

### Post-Track-1 review pass (findings fixed this increment)

- `runStatus` returned early on an empty fleet — a fresh node with nothing
  paired would never show the pending section (the one case it exists for).
- `RunDiscovery` blocked on `ReadFromUDP` with no shutdown release — a ctx
  watcher now closes the socket so the goroutine cannot outlive the daemon.
- `pending_nodes` had no bound — forged beacons could grow the table without
  limit; capped at 64 with stalest-evict.
- Self fingerprint could be absent forever on a peerless node (keypair only
  materialized on first hello) — `EnsureNodeKey` runs at startup.
- "LAN node seen" logged on every 15 s re-beacon — now only on first insert.
- `UpsertPending` reset `first_seen`… no — verified it only updates
  addr/pub/ver/last_seen on conflict, preserving first_seen.

## Track 2 — scheduling beyond harnesses

- [x] Actuator drivers shipped — `drivers/` carries the four reference
      drivers: `panda-servo` (gpiozero/AngularServo, BCM pin, 0-180°),
      `panda-mic` (arecord→WAV, 1-300 s cap), `panda-camera` (fswebcam /
      imagesnap / ffmpeg fallback chain), `panda-notify` (notify-send /
      osascript / powershell toast). Each exits 3 on a missing backend so
      `PruneUnavailableActuators` keeps the card honest. `package.sh` ships
      `drivers/` in every archive (full + lite) and `install.distributionEntries`
      sweeps it on uninstall.
- [x] action_spec end to end — `TaskSpecDetail.action_spec` rides spec_json
      (`ParseActionSpec`'s existing reader); `ValidateTaskSpec` vets target/
      action/param-name/scalar-value at the boundary (32-param cap); the entry
      prompt teaches the `hardware:*` convention + emit shape; `task_submit`
      gains an `action_spec` argument (context flips to `hardware`);
      `panda task add --action-spec '<json>'` and `/task add --action-spec`
      give the same dispatch without the model. `{action}` now fails closed
      like `{param:<name>}` — a template naming it rejects an action-less
      spec instead of substituting "". Tests: `TestActuatorTaskExecutesDriver
      Locally`, `TestActuatorTaskCrossDevice` (delegate → remote /bin/echo
      driver → result), `TestActuatorTaskSpecMismatchRefuses`,
      `TestActuatorTaskMissingParamFails`, `TestValidateTaskSpecActionSpec`,
      `TestParseOutputCarriesActionSpec`, `TestToTaskInputCarriesActionSpec`,
      `TestSubstituteActionSpecRejectsMissingAction`.
- [x] Actuator-only nodes route — `NewCore`/`ReloadCard` created the
      commander router only when Native/Agents/Manual were non-empty, so a
      hardware-only edge card (just a servo) declined every actuator task at
      `localMatch`. Actuators now count. `capabilities.example-edge.yaml`
      declares all four drivers; desktop example shows `hardware:notify`.

### Post-Track-2 review pass (findings fixed this increment)

- action_spec on a NON-actuator plan was silently dropped — a task asking
  for hardware whose requires resolved a plain ability ran the intent text
  as a shell command. `run()` now fails the dispatch when a spec naming a
  target meets a non-actuator plan (`TestActuatorSpecOnNonActuatorPlanFailsClosed`).
- `requires` carried no ordering guarantee — a vague token ("servo") ahead
  of the spec's exact id let MatchActuator resolve a sibling actuator, then
  the substitution gate refused and the task churned declines. Submission
  boundaries now lead requires with `target_actuator` via
  `ledger.RequiresForActionSpec` (model path in `toTaskInput`, plus
  `task add`/`/task add`), and `toTaskInput` forces `context_type:
  hardware` when a spec is present.
- `RunDiscovery` parsed its bind port with Atoi-then-0 — a non-numeric
  `discovery_addr` port passed config validation (SplitHostPort accepts
  names) then bound an ephemeral socket announcing to port 0. Now
  `net.ResolveUDPAddr` vets it; `TestDiscoveryBadBindAddr` pins the
  warn-and-return. The `":7837"` default literal was duplicated between
  daemon and embedded-engine wiring — one `DiscoveryAddrOrDefault()`.
- `splitArgs` (REPL tokenizer) honored only double quotes, so
  `/task add --action-spec '<json>'` was unreachable: JSON's `"` toggled the
  parser and got stripped. Single quotes group now (`TestSplitArgsQuotes`).
- `--agents a,b` combined with `--action-spec` silently discarded the spec
  (the multi-harness plan path returned early) — refused loudly instead.
- `recordSelfPubKey` inserted `last_seen=0` on a fresh row — the self row
  showed "never" until the first heartbeat; now stamps the materialize time.

- [ ] Live compute metrics — `capacity_json` gains gpu_util / mem_free /
      disk_free; scoring consumes them.
- [ ] `task add --nodes a,b` fan-out with aggregated results (distinct from
      `--agents`, which is same-node harness parallelism).
- [ ] `panda nodes drain <id>` — maintenance mode: heartbeat status
      `draining`, scheduler skips it, in-flight finishes; pairs with the
      updater's idle-queue gate for rolling upgrades.

## Track 3 — fleet observability

- [ ] Fleet panel: RTT, transport (ws/udp/punch/dtn), version skew warning,
      in-flight task count per node (data already in `employee_cache`).
- [ ] `panda task <id> --trace` + web timeline — the cross-node hop trail
      (delegate → relay → accept → result) without lab scripts.
- [ ] Fleet queue snapshot — heartbeats carry queued counts.

## Track 4 — trust & protocol tail

- [ ] Audit chain signing (P2-9): task_events hash chain → Ed25519/HMAC.
- [ ] Link metrics beyond RTT: bandwidth/loss samples.
- [ ] TURN-style relay fallback for symmetric NAT (large; may slip).
- [ ] Command-taint tracking for approval-gate residual vectors (documented;
      design change, likely out of scope).

## Track 5 — validation gates

- [ ] T2 fault-injection scripted — disconnect/restart/replay matrix runs in
      the loopback lab unattended.
- [ ] T3 real three-device lab (Mac / Orange Pi / Windows): discovery →
      pairing → delegation → plan → drain → upgrade; record latency,
      reconnect time, completion rate.

## Non-goals this release

- TURN relay, signed topology ads, source routing (post-v0.0.10).
- Multi-user households, mobile companion (roadmap Stage 4).
