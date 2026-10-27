# Project status

English translation of [status.md](status.md)（中文版为准）· English

> Recorded 2026-08-30 (`main`, v0.0.7 released); updated 2026-08-31 (a
> full round of review fixes, merged into v0.0.7); 2026-09-02
> (v0.0.8-preview: projects as first-class citizens, approval gate
> rewrite, plan board endpoints, and that day's P1/P2 review fixes);
> 2026-09-05 (v0.0.8-preview: the formal open-source wrap-up release —
> admin tool family, TUI mid-run steering, active model failover and
> injection, multi-model registry fully landed); 2026-09-06 (skill hub
> and autonomous discovery/install, TUI fullscreen + first-run wizard,
> Web skill panel and session cancel); 2026-09-19 (multiple validation
> rounds on `v0.0.8-preview`); 2026-09-20 (`v0.0.8` stable: end-to-end
> prompt multi-language policy, provider geography classification,
> automatic secret redaction, SQLite WAL concurrency tuning, release
> gate wired through); 2026-09-23 (`v0.0.9-beta`: hybrid transport/DTN
> architecture complete — latency-weighted mesh routing, DTN bundles
> live, token budgets, shadow copies, actuator path, agent
> self-management surface `panda mcp`, chunked resumable artifact push,
> DTN multi-hop relay and session checkpoints; model configuration
> surface fully landed, model-management TUI redesigned as full-width
> registry + single-screen form); 2026-09-25 (`v0.0.9` "Periapsis"
> stable: UDP datagram plane and mesh-coordinated NAT punching, DTN
> bundle payload encryption v2, contact-plan routing, Ed25519 node
> identity and signed authorization, lite builds for constrained
> devices, approval memory and per-project approval policy, execution
> attribution, Web sidebar node-state badges, a round of task-pipeline
> and TUI reliability fixes — batched tool calls, DSML compatibility,
> approval-disposition semantics, the queue admin tool family; security
> tightening: SSRF defense on skill URL imports, `/api/*` query-token
> auth removed, per-source STUN rate limiting; breaking change: builtin
> model-vendor defaults removed, `panda init`/`panda model add` are the
> config entry; pre-release additions: `panda web --lan` LAN sharing by
> default (auto temp token + shareable link), `panda task add --agents
> a,b --mode parallel|serial` single-task multi-harness orchestration
> (reuses plan/DAG), cross-process queue-claim polling 2s→400ms);
> 2026-10-06 (a security-hardening round: `task_events`/`audit_log`
> signature verification binds to the node's own key and refuses
> signature tail-stripping, `audit_log` carries per-row Ed25519
> signatures (migration v37); the updater supports out-of-band Ed25519
> verification via `OPENPANDA_UPDATE_PUBKEY` and a self-hosted channel
> via `OPENPANDA_UPDATE_REPO`; the message-dedup table now evicts
> oldest; `SECURITY.md`/config examples realigned with the code);
> 2026-10-06 performance round (capability matching moved to
> zero-allocation on-the-fly tokenization — 2709ns/40allocs →
> 1619ns/0allocs; the artifact data plane pools its 1 MiB read buffers
> and drops a useless per-chunk DB write; the panel's session-summary
> sweep went from "full task table + per-task event timelines" to a
> single NOT EXISTS query; new `make bench` benchmarks: route
> match/decision, control and 1 MiB data-frame codecs, message dedup;
> measured receive-side codec ~86 MB/s, send-side ~184 MB/s —
> structural gains need a binary data lane, recorded for design);
> 2026-10-06 (`v0.0.10-preview` "Apoapsis": LAN auto-discovery with
> fingerprint-confirmed admission, node TOFU key pinning, worktree
> travels with delegated file tasks, the clarification loop, actuator
> dispatch and five reference drivers (incl. serial MCU), the Pi
> adapter and a Python-free builtin generic executor, launch directory
> as project space — alongside that day's hardening and performance
> rounds; breaking: cleartext `ws://` dials on unencrypted underlays
> are refused); 2026-10-07 (license switched to AGPL-3.0-or-later +
> commercial dual licensing; existing MIT-era consents re-prompted once
> via `terms_version`; unpublished `feat/session-tree-rpc-auth` branch
> work: session tree `panda session fork`/`tree` + `/fork` + `POST
> /api/sessions/{id}/fork`, overflowing history auto-compressed into a
> running digest, `panda rpc` NDJSON-over-stdio embedding surface,
> `panda auth login anthropic` subscription OAuth — all marked
> Experimental; fixed two PTY contract-test fixtures missing
> `terms_version` getting caught by the re-consent gate, `make
> gate-all` green); 2026-10-07 (`v0.0.10` "Apoapsis" stable: session
> tree / auto-compression / `panda rpc` / subscription OAuth merged to
> main with stable, release pipeline green, 11 artifacts published,
> Homebrew tap synced; release-day fixes — `session fork`'s `--at`
> value flag parsing, the Windows auth 0600 assertion, three CI timing
> flakes relaxed; five-language README and preview-semantics cleanup in
> scripts/docs); 2026-10-10 (`v0.0.11-preview` "Rendezvous" line
> 10-10: per-connection session AEAD negotiated inside hello,
> Bluetooth-style `panda pair`, signed discovery beacons and `k:<pub>`
> stable identity, hard-pinned routing, outbox-custodied re-send of
> final dispositions on link loss, daemon authoritative-bit
> arbitration, silent watchdog killing stalled agents at a default
> 240s, binary data-frame negotiation; `timeouts.silence_s` semantics
> changed — 0/unset = default 240s kill rather than disabled);
> 2026-10-11 (`v0.0.11-preview` line 10-11: console live execution
> narrative, task briefing cards, CLI streaming reconciliation,
> migration connection-pool pinning, Windows adapter encoding fixes;
> unmerged experiments: trust zones mesh/guarded/open with send-side
> clamping, signed `task_accept`, `routing.reputation` local reputation
> into the route score); 2026-10-12 (`v0.0.11-preview` line 10-12:
> multi-hop mesh hardening verified on a real three-device fleet —
> hard pinning resolves nodes two hops out via gossip topology while
> preserving the final destination's identity on the wire,
> context/artifact relayed hop by hop along the delegation chain
> (partial mesh data plane), `timeouts.native_s` native-command budget,
> plan stages inheriting approval dispositions, askengine parked
> artifact pools fixing silent artifact breakage, parent/child
> resource-key deadlocks and mid-run spawned child folding, complete
> decline trails and restart-interruption causes, artifact
> out-of-range reads returning EOF, `$HOME` mis-pack refusal +
> credential pruning on source packing, explicit agent-replacement
> marking, unexecuted tasks rendering `✗ failed`, the approval gate
> catching redirect/pipe/process-substitution/PowerShell download
> spellings); 2026-10-12 (release-day review fixes: guarded/open-origin
> tasks withhold node model credentials by default — injection,
> credential pass-through and auth-failure fallback all closed,
> `network.trust.accept.guarded.model_creds` opts back in; every bare
> goroutine in `internal/core` moved into the `guard` recovery surface,
> server connection callbacks through `guard.Call` (blocking-counting
> semantics unchanged); self-update fails closed without a
> `ReleasePubKey`, `OPENPANDA_UPDATE_UNSIGNED=1` explicitly permits the
> checksum-only channel; `handlers.go`'s 4338 lines split by concern
> into handlers/run/prompt/wire; cardmut/providers/nodeidentity test
> coverage 39→72%, 42→94%, 59→73%); 2026-10-13 (inter-task message
> board landed: bounded short messages between sibling stages of one
> plan / the child set of one parent — slotted (one slot per sender per
> recipient, newer overwrites older, late copies refused by the seq
> guard), three granularity tiers (status ≤400B / report ≤4096B /
> artifact_ref pointer), a per-episode send budget (`messages.budget`,
> default 8, over-budget sends refused and folded back as a hint),
> cross-node owner fan-out (plan boards owned by the plan origin,
> children boards by the parent's executing node — same rule as result
> reporting), reads at supervision-tick boundaries never blocking a
> running agent; `panda task board <id>` is the viewing entry, design
> in the message-board notes; unit tests and a
> three-node in-process e2e all green, real multi-device verification
> pending); 2026-10-15 (a computer-use and security wrap-up on main
> after v0.0.11-preview: computer-use "eye/hand" landed —
> `drivers/panda-screen` (screencapture/grim/scrot/PowerShell capture)
> and `drivers/panda-input` (cliclick+osascript / xdotool / ydotool /
> PowerShell click·type·key·scroll, tier 2), `DetectActuators`
> auto-detects bundled `drivers/` and `Interface:"desktop"` actuators
> are only advertised when a GUI session is likely (macOS checks the
> console owner, Linux checks DISPLAY/WAYLAND_DISPLAY,
> `OPENPANDA_GUI_SESSION` overrides); artifact return path completed —
> bare-task private workdir outputs auto-pack and return, three
> authenticated panel endpoints + frontend inline preview, `panda task
> artifact` CLI; screen capture promoted to a first-class sensitive
> category — `requires`/`action_spec`/wide-token hits mark it,
> `send.guarded.screen` refuses by default, three gates at
> admission/send/artifact serving, the executor's runtime
> `screen_output` stamp rides `task_result` hop by hop (migration v45),
> and the authorization cache re-checks the peer's trust tier on every
> screen hit; self-update channel wrap-up — official verification key
> compiled in, the `OPENPANDA_UPDATE_UNSIGNED` escape hatch, the
> `install-local` symlink layout); 2026-10-15 (`v0.0.11` "Rendezvous"
> release preparation: a release-review round and fixes — mesh-only
> provenance for screen artifacts handed to downstream tasks as input
> (`ArtifactRef.Screen` stamped, read on the routing/admission/serving
> sides, display-byte consumers judged display-bound cascading stage by
> stage), empty artifact lists serialize as `[]` not `null`,
> panda-input key aliases completed and real macOS double-click,
> panda-screen missing-value guards, shell completions for `task
> board`/`task artifact`; version turned `0.0.11`, `make gate` green).

> 2026-10-22 (`v0.1.0-preview` "Constellation", the first cut of the
> release-grade line. The installers were hardened in the same pass: every
> path now refuses a downgrade (Windows gained the schema guard and
> `-Force`), `install.sh` verifies the release signature where the local
> openssl supports Ed25519 and aborts on a mismatch, and `install.ps1`
> resolves versions from the repository it actually downloads from.
> Installer coverage grew to 23 checks in `scripts/test-installers.sh`,
> adding downgrade refusal and the `--force` override, a foreign signature, an
> unsigned release that must warn (and must be refused under
> `OPENPANDA_REQUIRE_SIG`), and `--lite`. The
> cross-network mesh P1–P5 landed whole (dual-key identity and reachability
> cards, transitive blind relay, epidemic directory sync, K=2 rendezvous edges
> and the reachability ladder, the policy surface and relay budgets,
> `panda://join`, pair over transit, `dial_check`), and the release audit plus a
> second hardening pass closed the rest: multi-hop artifact pulls through
> relays, a model-reported failure no longer settling as done, a declined task
> falling back to a capable origin, hello identity pinning, a bounded
> project-pack admission, the UDP-plane crash, strict-mode secret read denial,
> the standalone console's thread and skills APIs; then the directory share cap
> bound to the row's signer (phantom rows could fill the directory and starve
> honest peers), the dial budget no longer owning the connection, trust
> assertions failing closed, the pairing sweep's nil dereference (it could stop
> the whole daemon), the transit SealNone guard's regression test, and the
> relayed punch request keeping its origin. The live cross-network multi-hop
> was verified (Mac → relay → cloud, a 3 MiB project tree hashing the same).
> See `CHANGELOG.md`; not done: UPnP/NAT-PMP,
> punch/dir source-signature convergence, a daemon→panel mesh-metrics IPC path,
> and hello replay across peers (a protocol-level challenge-response,
> still open).
>
> 2026-10-23 (`v0.1.0` "Constellation" release preparation: the default
> channels move from `v0.0.11` to `v0.1.0`; `v0.1.0-preview` and the earlier
> preview tags/releases were restored and stay published for anyone pinning a
> version. The public surfaces were brought in line before the cut — the site,
> README and this document point at `v0.1.0`, the `0.1.0` changelog section
> now carries the #104 work (console dictation, a per-agent model pin, device
> panels), and the site no longer commits to a next version number. The 23-check installer
> suite passes and `make gate` is green.)
>
> 2026-10-27 (`v0.1.1` "Constellation" release preparation: the patch on the
> release-grade line. Configuration and stored data are unchanged — no schema
> migration, no renamed key — and the wire adds no new frame. The one format
> change is the UDP datagram's peer-keyed seal, which does not interoperate
> with a 0.1.0 node on that plane, so a mesh upgrades together. The entry
> client gained two native dialects (OpenAI Responses and Google Gemini, with
> streaming, tool calls and usage metering; refusal text and the oversized-event
> boundary closed in the same pass), OpenCode Go / Console joined the built-in
> providers, a thread's roster and node pin reached the terminal (`/team`,
> `/node`, `panda ask --team/--node`, the to-do board), the dispatch budget
> now derives from the roster (ceiling 8), cross-process read-modify-write is
> atomic behind a file lock (`internal/fslock`), a task can pin a work
> directory of its own, a project's work directory can no longer be a
> filesystem root or the home directory, `sandbox.mode: strict` refuses to run
> without its backend, and the console caps request bodies, event streams and
> agent probes. The audit closed a batch of false greens: a pinned agent the
> roster excludes is refused rather than re-staffed, the round roll-up waits
> for employees that actually ran, a substitute names both sides, and the
> injection notice is one line. On CI, every workflow action is pinned to a
> commit sha with a guard script, and the adapter contract plus both TUI pty
> contracts run on Windows and macOS too. The gate's race leg also caught and
> closed a data race between the queue's shutdown and its dispatch: Go models
> a WaitGroup's first increment and its first waiter as a read/write pair, and
> the race detector reports the unordered pair. A runner slot is now taken
> under a mutex the wait also holds, and refused once the shutdown has begun.
> Before the cut, three more items closed: the console re-applies the work-dir
> breadth rule before admitting a project into its read roots (a row written by
> an older build can no longer serve the disk), both project endpoints answer
> 400 with the rule that refused the path, and `defense.HashDir` allocates
> ~4 MB per supervision round where it allocated ~165 MB, with a byte-identical
> digest. One security fix landed as well: UDP datagrams now seal to the peer
> pair under an X25519 key derived from both node identities, with the sender's
> id in the clear as the key selector and the envelope's `From` checked against
> it; a peer that publishes no identity key keeps the old mesh-key path, but
> 0.1.0 nodes all publish one, so that plane needs the whole mesh upgraded
> together (the WebSocket track is unaffected and punch frames keep their old
> form).
> Not done: UPnP/NAT-PMP, punch/dir
> source-signature convergence, the daemon→panel mesh-metrics IPC, cross-node
> hello replay.)

> 2026-10-21 (cross-network mesh P1–P5: dual-key identity and reachability cards,
> transitive blind relay, epidemic directory sync, K=2 rendezvous edges and the
> reachability ladder, policy surface and relay budgets, `panda://join`, pair over
> transit, `dial_check`, and §8 counters. Remaining: UPnP/NAT-PMP, punch/dir
> source-signature convergence, and a daemon→panel IPC path for mesh metrics).
>
> 2026-10-21 (self-evolution phases 1–4 main body, opt-in and off by default:
> signed patch-set manifests with per-patch sha256, `<data>/self` queue + merge
> worktree, a merge proposal as a review task, approve → apply → `make gate` →
> build → `StageLocal` → swap, `panda.release` rollback, cross-process worktree
> lock, base-freshness check, `+self.N` allocation at merge, and a service restart
> deferred until the review row settles; web patch-set/diff review card and
> read-only `/api/selfevo`. Remaining: mesh transport, auto mode, producer-side
> gate digest, `panda update --no-rebase`, macOS stable signing).
>
> 2026-10-21 (a review pass over the P0–P2 findings — self-evolution side: a
> merge proposal pins the digest of the manifest the reviewer saw, and an
> approve whose set changed since (the boot-time rebase sweep rewrites a
> queued set in place) asks for a re-review instead of merging unseen bytes;
> a rebase no longer rewrites a terminal (rejected/merged) set and no longer
> erases a "rejected" marker (one rebase used to turn the human's "no" back
> into a pending set); `panda self rebase` now carries the same admission
> rule as the boot sweep (under producers=self a foreign set is no longer
> re-signed into "this node's"); a manifest's base must be a pinned commit
> sha; set ids are validated as plain directory names (no escaping the
> queue root); an import no longer carries the source directory's `.state`
> marker; the `+self.N` counter is persisted so `reset --purge` cannot make
> the version regress; the rollback's schema guard fails closed and uses the
> live data directory's floor. Cross-network side: a transit destination
> checks that the inner frame is addressed to it (otherwise it executes
> somebody else's task and answers for them); the relay bandwidth budget
> charges the bytes that actually leave the node; an unsigned directory row
> may create a row but never displace one (otherwise any member could
> rewrite a peer's card and addrs and knock it out of the dial path); the
> per-sender new-id budget is reserved under the lock (no TOCTOU) and each
> sender's share of the directory is capped (rotating session ids does not
> help); the want list is sorted before the page cut (it used to be cut in
> map order, so the two sides never covered the same ids); the directory
> digest ignores the measured RTT (it used to move with every ping, so the
> two sides never converged and re-pulled every 30s) and is cached for 2s
> (each inbound heartbeat used to cost a full table scan and hash); the
> rendezvous election treats the failure domain as a preference rather than
> a filter (every LAN peer shares one domain, so only one was elected)).
>
> This document answers one question: **where the project stands now —
> what works and what does not.**

## One-line conclusion

Single machine and trusted LAN (currently three devices: Orange Pi /
MacBook / Windows) are **usable as designed**: one sentence in gets
classified as "answer locally / dispatch a task / split into a
multi-machine staged plan", and irreversible operations park in the
approval queue for a human. v0.0.7 filled in the usability layer: the
capability card is editable from every surface and hot-reloads, device
pairing is productized, and every task result gets an LLM briefing.

v0.0.8-preview's main increments: **projects became first-class
citizens** — a metadata table and an on-disk "current project" pointer,
tasks inherit automatically, delegated tasks carry project memory and
the worktree to the executing node, and outputs are collected back by
overwrite; **the approval gate was rewritten to "irreversible only"**
(the download-to-disk vector was brought back under gating after the
9/2 review); the console gained the plan board endpoint plus
approval/routing/memory-cap/injection settings.

2026-09-06 v0.0.8-preview additions: **the skill system learned to
serve itself** — an offline curated skill hub (`panda skill hub`),
imports from path/URL/archive (`panda skill import`), and the Ask
engine autonomously discovering and installing skills it needs
mid-task; the `panda skill` command family, the `/skill` REPL command,
and the Web skill-management panel landed together. The TUI moved to
alternate-screen fullscreen with a first-run wizard; the Web console
gained session cancel, a keepalive-retrying client, a directory picker,
and an execution-event timeline. Engine side: model health circuit
breaking with fallback routing, noauth model support; SQLite now opens
with foreign keys on.

P0 security findings (path traversal, result delivery, TUI exit) are
closed; P1 hardening (loopback listen by default, `context_fetch`
authorization, supervisor-unreachable parking) landed. Known gaps
remain in the trust model (P2-8 under "Known limits"), but the security
posture is much improved.

The 2026-08-31 review fix round has landed (merged into v0.0.7):
- **Backend**: resident-goroutine recover guard (`internal/guard` —
  panic logs + controlled shutdown); storage initialization converged
  (`cmd/panda/store.go` — one path for daemon and panel); migration
  mutex (`BEGIN IMMEDIATE` + `user_version` re-check inside the
  transaction — an old binary errors clearly on a newer database);
  platformed system config dir (unix keeps `/etc/openpanda`, Windows
  `%ProgramData%\OpenPanda`); Windows graceful shutdown
  (`SetConsoleCtrlHandler`) and Windows console colors;
  `make build-darwin-amd64`; `skill`/`reminder --help` exit-code fix.
- **Frontend**: a single shared event-bus instance (one SSE +
  `Authorization` header + exponential-backoff reconnect); session-
  stream race guard (switching sessions mid-stream no longer crosses
  bubbles); a top-level error boundary; focus trapping in the command
  palette and confirm dialogs; keyboard operation on board cards;
  system polling paused on hidden tabs; the panel event-fingerprint
  cache (scan cost decoupled from connection count).

## Capability reconciliation

| Design goal | Status | Landed at |
|---|---|---|
| Voice-dispatched tasks (desk-pet entry) | 🟡 Wired, unverified on real hardware | `cmd/panda/voice.go` (`panda voice`) + `extensions/voice/*.py` |
| Simple questions answered locally, complex ones dispatched | ✅ | `internal/entry` four-way classification: `answer` / `tool_call` / `task` / `plan` |
| Load-balance several tasks across machines in parallel | ✅ | `internal/scheduler/score.go` (resource efficiency 0.4 + user priority 0.3 + node tier 0.2 + wait time 0.1, plus three extras — project residency 0.2, link latency 0.15, opt-in reputation 0.1 — then multiplied by heartbeat-freshness decay, half-life 30 min) |
| Per-adapter independent scheduling | ✅ | `internal/agents/registry.go` registers 10 adapters (plus the generic.py universal bridge); `requires: ["agent:codex"]` is a hard filter |
| Hardware-aware routing (GPU tasks never land on a Pi) | ✅ | `resource_profile` → `ledger.Fits`; legacy cards without a hardware declaration pass under "silence ≠ 0" (`Declared()`) |
| Multi-machine staged pipelines | ✅ | `internal/plan` + `internal/core/plan.go`; stage artifacts moved chunked via `internal/artifact` |
| Report on completion, queue shows done | ✅ | `panda queue` / `panda task <id>` / `panda plan show <id>` |
| Irreversible operations into pending approval | ✅ | tier judged by `internal/defense/permission.go` (incl. the Windows verb/interpreter tables) → `review` state → `panda approve` / `reject` |
| Chief delegates down to claude code / codex | ✅ | `internal/commander` three tiers: `native` / `agent` / `manual` |
| Decentralized, every node can be root | ✅ | edge-by-edge routing + `AppendChain` / `Predecessor` anti-loop, no master root |
| **Card editable from every surface + hot reload** | ✅ | `panda card native/agent/manual` subcommands, `/card` in REPL/TUI, Web `/api/card`; `ReloadCard` hot reload + card-bearing heartbeat broadcast |
| **Device pairing productized** | ✅ | `panda pair`, `panda nodes add <addr>`, Web "Invite a device" CTA |
| **LLM task briefing** | ✅ | `entry.SummarizeResult` runs after every inline task; REPL/TUI/Web render the briefing |
| **Bubble Tea TUI** | ✅ | `panda` defaults into the TUI with the tier-2 approval path; the classic REPL remains via `PANDA_CLASSIC_REPL=1` |
| **Projects travel with tasks across devices** (v0.0.8-preview) | ✅ code landed | `panda project` command family + settings pointer; delegation payload inlines project memory, worktree rides chunked artifacts, outputs collected back by overwrite (`internal/projects` / `internal/core/project.go`) |
| **Plan board endpoints** (v0.0.8-preview) | ✅ | `GET /api/plans`, `GET /api/plans/{id}` (stages + artifact wiring); Web board view wired (`views/plans.tsx`) |
| **LAN auto-discovery + fingerprint admission** (v0.0.10) | 🟡 Experimental | UDP beacon (`network.discovery_addr` :7837) → `pending_nodes` (60s TTL + 64-row cap) → `panda nodes admit`; `nodes verify` pins Ed25519 fingerprints |
| **Worktree travels with delegation** (v0.0.10) | 🟡 Experimental | `context_type=file` repository tasks pack a `__worktree__` artifact (≤256 MiB); the executor runs in a private directory and packs back |
| **Clarification loop** (v0.0.10) | 🟡 Experimental | agent parks on a `PANDA_QUESTION:` line → `panda approve <id> -m "answer"` resumes with the answer |
| **Inter-task message board** (v0.0.11) | 🟡 Experimental | `internal/core/messages.go`: slotted boards under `plan:<id>` / `task:<parent>` scopes (one slot per sender per recipient, newer overwrites older); agents send via the structured `messages` field or `PANDA_MSG` lines, folded into `[sibling board]` at tick boundaries; `bus.MsgTaskMessage` cross-node owner fan-out, receivers verify membership; `panda task board <id>` to view |
| **Session tree / auto-compression / RPC / subscription OAuth** (v0.0.10) | 🟡 Experimental | `panda session fork`/`tree`/`/fork`, overflowing history folded into a running digest, `panda rpc` NDJSON-stdio, `panda auth login anthropic` |
| **Session AEAD + Bluetooth-style pairing** (v0.0.11) | 🟡 Experimental | hello `caps` negotiate `sessaead` per-connection AES-256-GCM (HKDF bound to both nonces/identities); `panda pair` X25519 + short-code confirmation + sealed shared-secret delivery |
| **Trust zones** (v0.0.11) | 🟡 Experimental | `network.peers[].trust`: mesh/guarded/open tiers (`panda nodes trust`); guarded clamps delegation exposure + admission policy (`network.trust.accept.guarded`, optional policy_llm); non-mesh-origin tasks withhold model credentials by default (`model_creds`); signed `task_accept`; `routing.reputation` local reputation; screen-capture tasks mesh-exclusive (`send.guarded.screen`, three gates at admission/send/artifact pull + executor-side `screen_output` runtime stamping propagated hop by hop + per-hit trust re-check on authorization-cache hits — demotion cuts flow immediately) |
| GPIO-driven servo (Orange Pi) | 🟡 Path landed, unverified on real hardware | v0.0.10 actuator dispatch: cards declare `actuators` + argv templates, `spec.action_spec` travels across delegation, the executor substitutes placeholders and runs the driver; `drivers/` ships seven reference implementations — servo/mic/camera/notify/mcu/screen/input |
| **Computer-use eye/hand** (v0.0.11) | 🟡 Path landed | `hardware:screen_capture` (screenshot, tier 1) + `hardware:gui_input` (click/type/key/scroll, tier 2 as irreversible) reference drivers; `DetectActuators` auto-detects bundled `drivers/` (`OPENPANDA_DRIVERS_DIR` overrides exclusively), Windows resolves `.ps1` entries and runs them via `pwsh -File` (.cmd/.bat deliberately not resolved: cmd.exe parsing rules are incompatible with Go argv escaping); `Interface:"desktop"` actuators are advertised only when a GUI session is likely (console owner / DISPLAY·WAYLAND_DISPLAY / non-Session 0, `OPENPANDA_GUI_SESSION` overrides) — a headless daemon never fibs a screen; the routing prompt teaches the capture→reason→actuate chain |
| **Task artifact retrieval** (v0.0.11) | ✅ | `panda task artifact <id> [--list|--file|--out]`; panel `GET /api/tasks/{id}/artifact`, `/files`, `/file?path=` (inside Bearer); console ArtifactSection lists + inline image/text preview + download; bare-task private-directory outputs auto-pack (empty dirs produce no empty pack, fixed workdirs are never packed whole) |
| **Console dictation** (v0.1.0) | ✅ | `webui/panel/voice.go` + `POST /api/voice/transcribe`: the page records 16 kHz mono WAV and relays it to an OpenAI-compatible `/audio/transcriptions`; `asr` is optional and inherits the active model; vendor refusals are returned verbatim |
| **Per-agent model pin** (v0.1.0) | ✅ | `injection.agents.<agent\|adapter>` pins a model for that agent alone; unset fields inherit the node model, stopping before `params`/`headers`/`thinking` so one dialect's body cannot leak into another |
| **Workspace device panels** (v0.1.0) | ✅ | `webui/app/src/views/workspace-panels.tsx`: the panel is the machine, the tile is the agent and its live busy state; a round no longer closes on a batch's first report (`roundStillOpen()` reads the task board) |
| **Native dialects: OpenAI Responses + Google Gemini** (v0.1.1) | ✅ | `internal/entry/responses.go` + `google.go`: the entry client drives both surfaces natively, with streaming, tool calls and usage metering; refusal text streams into the answer, arguments that arrive without their `output_item.added` fail with a diagnosis instead of an empty answer, the SSE event ceiling is 4 MiB and `bufio.ErrTooLong` is final instead of a replayed, re-billed request |
| **OpenCode Go / Console built-in provider** (v0.1.1) | ✅ | `panda model add opencode-go oc_sk_…` — one key covers the pay-as-you-go catalogue and the Go/Go Plus subscription; each model routes to OpenAI Chat, Anthropic Messages, OpenAI Responses or Google Generative AI; only SystemOne (Jev) is unsupported, and it says so at setup |
| **Terminal roster and node pin** (v0.1.1) | ✅ | `/team` (a space-toggled picker), `/node`, `panda ask --team/--node`, `panda session ask --node` and the RPC `AskScope` all carry the roster and the node; `panda todo …` / `/todo` bring the to-do board to the terminal; the dispatch budget derives from the roster (`task_budget` declares a wider fan-out, ceiling 8) |
| **Atomic cross-process storage writes** (v0.1.1) | ✅ | new `internal/fslock`: sessions and memory hold the lock across the whole read-modify-write (the file lock ahead of the in-process mutex, one order everywhere), so two writers no longer overwrite each other; renaming a project moves its memory in one step, a Dreaming promotion is recorded after it is written, and a sweep that changes nothing rewrites nothing |
| **Per-task work directory** (v0.1.1) | ✅ | a task that names a work directory runs there and holds that directory's lock instead of the project's, so two business domains in separate trees proceed side by side rather than queueing behind each other; a pinned task stays on this node; inside the project's own tree the pin degrades to a scope leaf. The task's pin answers to the same breadth rule as the project: a volume root, the home directory and the OS trees are refused |
| **Queue shutdown ordered against dispatch** (v0.1.1) | ✅ | `internal/scheduler/queue`: a runner slot is taken under a mutex the wait also holds, and refused once the shutdown has begun — a slot taken before the wait starts is awaited by it, one taken after is refused and the task stays queued; the dispatch loop no longer races the engine's close inside `wg.Wait()` (Go models a WaitGroup's first increment and its first waiter as a read/write pair, and the race detector reports the unordered pair) |
| Shortest-path multi-hop relay | ✅ graph routing landed | neighbor advertisements build the link-state graph, edge weights take `LinkMetrics` RTT samples (`unknownLinkCost` when unmeasured), `graphFirstHop` runs Dijkstra for the first hop of the globally cheapest path (`internal/scheduler/route.go`) |

## Both entry points reach it

A multi-machine pipeline has two entries, both leading to the same
`core.StartPlan`:

```bash
# One: a file-based pipeline (the kind you run weekly — readable, diffable)
panda plan example > train.yaml
panda plan run train.yaml --dry-run     # preview the routing first
panda plan run train.yaml
panda plan show <plan-id>

# Two: one sentence (the desk-pet scenario — no file needed)
panda ask "write an image-classification training script, run it on the machine with a GPU, send the conclusion back"
panda voice                              # same path, spoken instead
```

There is exactly one criterion for the model emitting `plan` instead of
`task`: **whether a machine switch is required**. Several steps on the
same machine are still one task.

## Stage approval semantics

`core.StartPlan` **gives no stage tier-2 consent**, and `panda voice`
has no `--authorize`. The consequence is deliberate: when an
irreversible action appears in a pipeline, that stage parks at `review`
for a human instead of inheriting "your one authorization of the whole
sentence". Consent to a shell command is not consent to a three-machine
pipeline — and something a microphone heard is even less.

## Verified to what degree

| Check | Result |
|---|---|
| `make gate` (fmt-check + vet + build + adapter-test + test + race-focused) | exit 0 (re-verified 2026-10-15, incl. computer-use drivers/artifact return/screen trust gates; race-focused covers core/trust/bus/storage) |
| `make gate-all` (+ web-gate + the two TUI PTY contracts) | exit 0 (re-verified 2026-10-07; fixed two PTY fixtures missing `terms_version` getting caught by the re-consent gate) |
| Web typecheck + unit tests + vite build | exit 0 (79 tests, 2026-10-07) |
| P0 security findings (path traversal, result delivery, TUI exit) | ✅ closed |
| P1 hardening (loopback by default, `context_fetch` authorization, supervisor unreachable) | ✅ landed |
| 2026-08-31 review fixes (backend stability + frontend races/accessibility) | ✅ code landed, unit tests green; the backend-stability surface is covered by the v0.0.11-preview three-device live regression (delegation/kill/reconnect/replay), the frontend race fixes are single-device surface |
| 2026-09-02 review (P0 build error, P1 approval-gate bypass, P2 SSE error semantics) | ✅ fixed: `priorityName`→`priorityLabel` merged into 932442a; curl/wget download-to-disk re-gated; the fingerprint cache waiter now returns error; `go build`/`vet`/related tests green |
| Inter-task message board (slots/addressing/budget/drain/forgery + three-node wire e2e) | ✅ unit tests and in-process three-node e2e green (2026-10-13); real multi-device exchange unverified |
| Flagship pipeline live on three devices (pi → mac → win → pi) | ✅ v0.0.11-preview ran live on a real three-device fleet (macOS ↔ Windows ↔ Linux SBC): multi-hop delegation and hard pinning (two-hops-out nodes addressed via gossip topology), two-hop pipelines (context/artifact relayed hop by hop along the delegation chain), killing a node mid-execution, disconnect-reconnect and message replay; the project-pack cross-device chain (memory inlining + output collection) still awaits dedicated live verification |
| Voice path live (wake → transcribe → dispatch → read aloud) | ❌ only the degraded path verified (missing drivers error and exit correctly) |

The voice sidecar's default backend needs no key but does need local
dependencies:
`pip install openwakeword faster-whisper SpeechRecognition pyaudio
numpy`. With dependencies missing, `panda voice` prints the sidecar's
own reason and exits 1 — it does not silently restart python.

## Known limits

**P2-8 One shared secret for the whole network (partially mitigated).**
The network shared secret remains the membership credential: a node
holding it can complete mesh join. On top of it, Ed25519 node identity
and signed consent have landed — tier-2 `Authorized` travels with the
task as `Ed25519(taskID, authorized, ts, ConsentDigest)`, the executor
verifies it against the origin public key recorded in its directory,
and stripped/tampered/re-keyed signatures are refused outright;
`panda nodes verify` pins fingerprints and a re-keyed hello from a
pinned node is refused. **Remaining gap**: a shared-secret holder can
still forge consent "from a node whose public key the receiver has
never seen" (an inherent boundary of multi-hop relay + TOFU — that path
now prints a warning); unpinned identities are still trusted on first
use. Therefore "irreversible operations need my approval" holds on a
single machine and a trusted LAN, and stops holding once the network
contains a node you do not fully trust. The real fix is per-node key
distribution/admission — a trust-model change, not a patch.

**P2-9 The audit chain is signed, but anchored inside the database.**
`task_events` and `audit_log` hash chains now carry per-row Ed25519
signatures (migrations v35/v37); verification binds to the node's own
public key (`VerifyTaskEventChain` / `VerifyChain`: re-signing under a
new key is refused, and a signed chain may not show unsigned rows —
anti-truncation). But the signature anchor lives in the database
itself: a never-signed history prefix cannot be distinguished from one
wholesale rewritten; tightening further needs external attestation
(exporting/notarizing the chain head) and remains on the roadmap.

**Remaining opaque vectors in the approval gate.** After the gate was
rewritten to "irreversible only", `curl -o`/`wget -O` download-to-disk
was brought back under Tier 2 (downloaded bytes are invisible to the
classifier); adjacent spellings were subsequently folded in: `curl url
> /tmp/x` redirect-to-disk, `curl url | tee /tmp/x` pipe-to-disk, `bash
<(curl …)` process substitution (incl. nested shells inside quotes, and
PowerShell spellings like `iwr -OutFile`) now judge Tier 2 the same as
`-o`. What remains is a deliberate relaxation: executing a script
already on the machine (`bash script.sh`) does not scan its contents
(otherwise a node cannot run its own build), so "write a script by any
means, then execute" stays outside the gate; the real fix is
command-level taint tracking (executing a file written in the session
escalates it), a larger change. Defense in depth exists: explicitly
declaring `tier: 2` on a native capability in the card always beats
inference.

**i18n took two rounds; the model-facing surface is basically
aligned.** v0.0.6 moved the previously listed surfaces (voice path,
ask/repl plan output, session summaries, uninstall errors, one help
hint) into `internal/i18n` five languages; this round cleared more:
ask-engine edge replies ("tool calls failed to converge in N rounds",
"plan cannot execute") go through `ask.loop.*`/`ask.plan.invalid`
five-language keys; all ask tools' descriptions and schema property
descriptions are completed as "EN / 中文" bilingual inline (per the
`tools.go` convention; `task_submit` long picked by session locale, the
MCP surface stays English-only); injection notices to agents, memory
manifest/snapshot fences, and dream journal entries are all
bilingualized. Tool execution results and error bodies remain Chinese
(model-consumed intermediate artifacts, pinned by tests; the model
rephrases in the user's language), and the `supervise` upper-judge
prompt keeps its tuned Chinese original — making those two five-language
is a separate design round.

**GPIO / servo / computer-use have the execution path but no real-
hardware verification.** Since v0.0.10 actuator dispatch landed
(`action_spec` → card `actuators` argv-template substitution → driver
process), `drivers/` ships seven reference drivers and release
archives bundle them; the screen/input capture and GUI-input chain new
on main has likewise only passed unit tests — servo/mic/camera/serial
have never been fired on any real hardware, and the screenshot
"task → driver → artifact → panel preview" chain has only been
spot-checked on a dev machine (macOS `screencapture` capture works; the
TCC grant can only be given by the user inside a GUI session);
`panda ask "turn the servo to 90 degrees"` end-to-end verification is
still owed.

**Computer-use cognition lives in the agent, not the orchestrating
model.** Screenshots return as artifacts and the user can view/download
them in the CLI/panel, but the orchestrating-model/provider chain stays
text-only — the "look at the screen, then decide what to click" visual
loop happens inside the executing node's agent (the agent reads the PNG
itself); the orchestrating model never sees screen pixels. This is a
deliberate current boundary: multimodal into the orchestration layer is
a separate design round. Separately, `guiSessionLikely` is a session
probe, not a permission probe — macOS screen-recording/accessibility
grants fail honestly at driver execution (exit 3/1), granted by the
user in System Settings.

**The inter-task message board landed, but real multi-device is
unverified, and it is a collaboration channel, not a confidential
one.** Slot semantics (one slot per sender per recipient, newer
overwrites older), addressing (stage id / parent / children / member
task id and unique prefixes), owner resolution, budget accounting, and
receiver-side membership verification all have unit tests and an
in-process three-node wire e2e; real three-device exchange and
cross-node `stage:<id>` targeting still owe a verification round. Two
deliberate boundaries: first, a directed message's local copy on the
**sender's node** is visible to members of the same scope (that node
holds no roster and can only resolve `parent` and concrete task ids
locally; a `stage:<id>` address is recorded on the sending node in
broadcast form) — same-scope members are collaborators anyway, but it
is not a private chat; second, delivery is best-effort with no
dedicated outbox — lost frames self-heal on the next send (a
consequence of the slot model). MCP active board-pulling (not waiting
for tick boundaries under the extended policy), a webui board view, and
communication-efficiency telemetry are unbuilt.

**Multi-hop is now weighted shortest path.** Neighbor advertisements
propagate topology, `LinkMetrics` carries link RTT, and
`graphFirstHop`/`DTNNextHop` run Dijkstra over it. Still-missing deep-
space capabilities: link metrics carry only RTT (no bandwidth/loss
dimensions), topology advertisements are unsigned, and the path is
recomputed at every hop rather than fixed at the source.

**The UDP datagram plane and NAT punching are live (Periapsis).**
`internal/bus/udp.go` provides a second transport of AES-256-GCM sealed
envelopes + HMAC-signed punch frames; `punch:<node-id>` entries in
`network.peers` trigger mesh-coordinated punching
(`punch_offer`/`punch_ready` relayed along the link-state graph,
TTL-bounded), and a punched endpoint becomes a fallback route for
`sendTo`. Reflected-address discovery has zero external dependencies:
every node answers STUN binding requests, and the hello reply throws in
the observed IP for free. DTN bundle payloads are encrypted (v2, with
v1-cleartext verification kept for compatibility). Still missing: when
symmetric NAT (the rare enterprise-grade kind) cannot be punched, a
TURN-style relay fallback; contact-plan scheduled-window delivery is
unbuilt.

## Next steps

1. **The first real three-device lab round is repaid** (v0.0.11-preview:
   macOS ↔ Windows ↔ Linux SBC live delegation, mid-execution node
   kill, disconnect-reconnect, message replay, two-hop pipelines);
   remaining debt: the project-pack cross-device chain (memory inlining
   + output collection), and a cold-start round of LAN discovery →
   fingerprint admission → pinning on a brand-new device.
2. **Real-hardware actuator firing**: servo (gpiozero), mic (arecord),
   camera, serial MCU — each through one real-hardware `panda ask` →
   `action_spec` → driver run; the computer-use chain through
   "screenshot → agent reads it → `gui_input` click → artifact returns
   to preview" on a machine with a real GUI session.
3. **The voice entry end-to-end** on real hardware (wake → transcribe →
   dispatch → read aloud), and the dedicated project-cross-device run —
   the worktree/context relay layer already went through real hardware
   with v0.0.11-preview, but the project pack end-to-end is still
   unverified on its own.
4. Trust-surface wrap-up: P2-8 per-node key distribution/admission
   (closing the "consent from an unknown origin" path); P2-9 external
   attestation of the audit chain head.
5. Trust-zone wrap-up (merged with v0.0.11-preview, Experimental):
   guarded/open-tier behavior, signed `task_accept`, and reputation
   routing need a real multi-device round before promotion; session
   tree/auto-compression/`panda rpc`/subscription OAuth merged with
   v0.0.10 (Experimental) and still owe a round against real models and
   a real subscription.
6. i18n wrap-up, bandwidth/loss link-metric dimensions, TURN-style
   relay fallback for symmetric NAT — scheduled as needed.
7. Inter-task message board wrap-up: real three-device exchange;
   MCP active board-pull and a webui board view; communication-
   efficiency telemetry (message counts/staleness/budget hits). Beyond
   that is the **scout→plan two-phase protocol** (probe the codebase
   first, then split and dispatch dynamically on the report) — the
   message board is its communication substrate; the splitting protocol
   itself is still to be designed (see §8 of the message-board notes).
