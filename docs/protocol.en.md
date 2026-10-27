# P2P Bus Protocol (internal/bus)

English translation of [protocol.md](protocol.md)（中文版为准）· English

> Distilled from the `internal/bus` package; describes the transport and
> wire protocol for inter-node task delegation.
> The code is authoritative (`msg.go` / `payloads.go` / `ws.go` /
> `udp.go` / `auth.go` / `ed25519.go` / `sessenc.go` / `bundle.go`); on
> any conflict with this document, the code wins.

## Transports

There are two data planes, both carrying **the same envelope** (below):

- **WebSocket** (`gorilla/websocket`), endpoint path `/ws`. `ws://` at
  the TCP layer is unencrypted, but once the hello exchange completes a
  connection may arm **session encryption** (see "Session AEAD" below);
  only links that fail to negotiate encryption face the cleartext gate
  (`cleartextOK`, evaluated post-handshake): `ws://` may only dial
  loopback / Tailscale-shaped destinations (`100.64.0.0/10`,
  `fd7a:115e:a214::/48`, `*.ts.net` — links already encrypted
  underneath or unable to leave the machine) or ranges/hosts listed in
  `network.allow_cleartext_for`; every other destination must be
  `wss://` or go through a `punch:` UDP endpoint;
  `network.allow_cleartext` is the operator's explicit opt-out.
  Inbound is symmetric: if a peer that passed hello cannot arm
  sessaead and its source IP is not in the safe/allowed set, the
  connection is dropped.
- **UDP datagram plane** (`udp.go`, for NAT scenarios). It punches and
  backs up links where a TCP/WebSocket session cannot be established.
  Each datagram is one of four frames (distinguished by a 4-byte kind
  header):
  - `kind 1 punch` / `kind 2 punch_ack`: a JSON `PunchFrame`, HMAC
    signed (`punch|<nonce>|<from>|<ts>`) proving mesh membership; opens
    the NAT hole;
  - `kind 3 data`: `nonce(12) || AES-256-GCM(JSON Envelope)` with the
    frame header as AAD — keys are domain-separated derivations of the
    shared secret, so envelopes on the UDP plane are **encrypted end to
    end**;
  - `kind 4 keepalive`: a sealed empty body keeping the NAT mapping
    alive.
  The same socket also answers STUN binding requests (RFC 5389,
  recognized by magic cookie): reflected-address discovery with zero
  external dependencies.
- The server only accepts handshakes **without an `Origin` header**
  (the inter-node Go client sends none; browsers send one), so a
  cross-site page cannot reach a node's control channel (the PWA talks
  HTTP on the panel port, not here).
- The dialing side sets `TLSClientConfig.InsecureSkipVerify = false`:
  `wss://` requires a valid certificate.
- Connection caps: global concurrent connections `max_connections` and
  per-IP concurrent connections `max_connections_per_ip` (0 =
  unlimited); exceeding either returns `503` — the first gate against
  slow-connection / handshake DoS.
- Inbound connections get a **bounded handshake window**
  `defaultHelloTimeout = 10s`: the first message must be a valid
  `hello`, otherwise the connection drops. The only frame accepted on
  an unauthenticated connection besides `hello` is `pair_hello` (the
  LAN pairing entry, below).

## Session AEAD (sessaead)

Frame-level encryption on top of `ws://`, negotiated by capability
since v0.0.11 (hello `caps` advertises `sessaead`; no version parsing):
when both hellos carry a nonce and a shared secret exists,
`SessionAEAD` derives a per-connection AES-256-GCM via
`HKDF-SHA256(secret, info=domain‖dialerID‖listenerID‖dialerNonce‖
listenerNonce)`. From then on every application frame is sealed as
`nonce(12) ‖ AEAD(tag ‖ payload)` and always travels as a
BinaryMessage; `tag` distinguishes a sealed JSON text frame (0x01)
from a data frame (0x02). The nonce is random per dial and the key is
bound to both identities — sniffing and replaying a hello yields no
session key, and stripping `caps` to downgrade to cleartext just hits
the cleartext gate above (fail closed). When the peer does not support
`sessaead`, the link stays cleartext and is left to the cleartext gate;
interoperation with older versions is unaffected.

## Binary data frames (binart)

Between peers advertising `binart` in hello `caps`, envelopes carrying
bulk bytes (`artifact_chunk` / `artifact_push`) may travel as WS binary
frames: a 2-byte header length + JSON header + raw bytes, avoiding
base64's 4/3 inflation. The sender consults `caps` before choosing the
encoding, so a mixed-version link falls back to the JSON form; binary
frames are only used on direct links (relays/delegation re-pack as
JSON).

## Frames and sizes

| Constant | Value | Meaning |
|---|---|---|
| `readLimit` | `4 << 20` = 4 MiB | per-message cap; the receiver closes the connection on overflow |
| `pongWait` | 60 s | pong wait timeout (peer deemed dead) |
| `pingPeriod` | 30 s | ping interval; must be less than `pongWait` |
| `writeWait` | 10 s | max block duration for a single write (data or ping) |
| `ArtifactChunkBytes` | `1 << 20` = 1 MiB | payload size carried by one `artifact_chunk` |
| `maxWireText` | `512 << 10` = 512 KiB | cap on a single `stdout`/`stderr` field in a result frame |

A 1 MiB `[]byte` is base64-encoded in JSON (~4/3 inflation), about
1.4 MiB on the wire — well below the 4 MiB frame cap even with the
envelope.

## Message envelope

Every message is a JSON envelope (`Envelope`, design doc §10.3):

```json
{
  "v": 1,
  "type": "task_delegate",
  "msg_id": "<uuidv7>",
  "from": "<node-id>",
  "to": "<node-id>",
  "ts": 1710000000,
  "payload": { }
}
```

- `v`: protocol version, currently always `1`.
- `type`: routing key — one of "Message types" below.
- `msg_id`: UUIDv7; the receiver deduplicates on it (idempotence). Must
  be non-empty when building an envelope.
- `from` / `to`: sender / target node; an omitted `to` means broadcast /
  edge-by-edge routing.
- `ts`: Unix seconds.
- `payload`: type-specific payload (raw JSON, decoded by the handling
  layer per type).

When building an envelope (`NewEnvelope`), payloads implementing the
`wireClamper` interface are first `clampForWire()`-trimmed for oversize
fields (see the result frame below). Trimming is unified **at build
time** because — a task that ran for an hour must not blow the frame
cap on long logs, get disconnected by the receiver, and lose the result
along with the link.

## Authentication: hello's two signatures

`hello` carries two identity layers:

- **Membership (HMAC)**: `sig` is the hex of
  `HMAC-SHA256(secret, nodeID + ":" + ts + ":" + nonce)` (`HelloSigN`).
  `ts` binds a timestamp to bound the replay window; `nonce` is a
  per-dial random value so two legitimate reconnects within the same
  second are not misjudged by the receiver's once-only replay cache.
  Older peers without a nonce are checked under the two-field form
  (`HelloSig`, compatibility only).
- **Cryptographic identity (Ed25519)**: `pub_key` is the node's
  persistent identity public key (hex), `ed_sig` an Ed25519 signature
  over `NodeID:Ts:Nonce`. The HMAC proves "you hold the mesh secret";
  the Ed25519 proves "you are the key owner of this node id" — both are
  required: a valid EdSig without the shared secret is refused outright,
  and presenting `pub_key` while unable to sign an EdSig is refused too
  (anti-impersonation).

Verification rules (`VerifyHelloP`):

- `ts` more than `maxHelloAge = 5 * time.Minute` from now (past or
  future) → failure.
- Empty shared secret or empty signature → **always fails
  (fail-closed)**: a node without the shared secret may vouch for no
  peer.
- `sig` is compared constant-time under the nonce form (`HelloSigN` when
  `nonce` is present, else the legacy two-field `HelloSig`); mismatch →
  failure. The receiver additionally builds a once-only replay cache
  over the fields the signature covers.
- Neither `pub_key` nor `ed_sig` present → admitted as a legacy
  identity-less peer (membership only); only half present, or a
  signature that does not verify → treated as forged/stripped, failing
  rather than degrading.

The 5-minute window both tolerates P2P clock drift and expires sniffed
old `hello`s. An authenticated Ed25519 identity then becomes the outbox's
stable custody key (`k:<pub>`) and the object `panda nodes verify` pins
fingerprints to.

## Message types

| `type` | Payload | Notes |
|---|---|---|
| `hello` | `HelloPayload` | Identity declaration at connect: `card` summary + HMAC `sig` (binding `ts`/`nonce`) + Ed25519 `pub_key`/`ed_sig` + `udp_port` + `caps` + `authoritative` |
| `hello_reject` | `HelloRejectPayload` | The only receipt the server gives before disconnecting on handshake failure: `reason` ∈ `auth`/`identity`/`replay` |
| `join` | — | Join (constant defined in `bus`, handled at the core layer) |
| `heartbeat` | `HeartbeatPayload` | status + capacity; may carry the latest capability card `card` |
| `task_delegate` | `TaskDelegatePayload` | Task handover (the core frame, below) |
| `task_accept` | `TaskAcceptPayload` | Task acceptance; newer nodes attach an Ed25519 signature `sig`/`pub`/`ts` |
| `task_decline` | `TaskDeclinePayload` | Task refusal with `reason`; optional machine-readable `reason_code` |
| `task_progress` | `TaskProgressPayload` | Executor heartbeat, sent upstream on the lease cadence |
| `task_result` | `TaskResultPayload` | Completion result (below) |
| `task_retry` | — | Retry (constant in `bus`) |
| `task_transfer` | — | Task transfer (constant in `bus`) |
| `task_cancel` | `TaskCancelPayload` | Cancellation with `reason` |
| `task_resume` | `TaskResumePayload` | Re-release of a task "parked at approval" (may carry a clarifying `answer`; with `accept` set it mirrors an accepted disposition to the parked copy on the other side, review→done, carrying no re-execution consent) |
| `task_message` | `TaskMessagePayload` | One short message on the inter-task message board (sibling stages of one plan, or the child set of one parent — below) |
| `context_fetch` | `ContextFetchPayload` | Ask the source node for the full context snapshot |
| `context_ack` | `ContextAckPayload` | Reply to `context_fetch` |
| `artifact_fetch` | `ArtifactFetchPayload` | Pull one range of an artifact by offset |
| `artifact_chunk` | `ArtifactChunkPayload` | Reply to `artifact_fetch` (one data range) |
| `artifact_push` | `ArtifactPushPayload` | Proactively push one range of an artifact (DTN/disconnected scenarios, resumable) |
| `artifact_push_status` | `ArtifactPushStatusPayload` | Receiver's custody report: `received_through` consecutive bytes |
| `artifact_push_done` | `ArtifactPushDonePayload` | Push finale: pooled after hash verification (`ok`) or failure stops the push |
| `agent_negotiate` | `AgentNegotiatePayload` | Peer conflict-negotiation signal (§5.1, TargetScope + weights) |
| `agent_grant` | `AgentGrantPayload` | Lease-lock grant/deny (§5.2, `denied` distinguishes explicit refusal from no answer) |
| `agent_yield` | `AgentYieldPayload` | Agent yields execution at a checkpoint |
| `dtn_bundle` | `DTNBundlePayload` | A CBOR-encoded DTN bundle passed through verbatim (below) |
| `punch_offer` | `PunchOfferPayload` | NAT punch coordination: session nonce + candidate endpoints + TTL |
| `punch_ready` | `PunchOfferPayload` | Answer to an offer: echoes the nonce, attaches own candidate endpoints |
| `pair_hello` | `PairHelloPayload` | LAN pairing initiation (the only frame accepted besides hello on an unauthenticated connection): session id + ephemeral X25519 public key + Ed25519 identity + nonce |
| `pair_ready` | `PairReadyPayload` | Pairing reply: the peer's X25519/nonce/identity, from which both sides derive the same short verification code |
| `pair_secret` | `PairSecretPayload` | Shared-secret delivery after pairing confirmation: AES-256-GCM sealed (HKDF(dh, nonces)), responder→initiator only |
| `pair_reject` | `PairRejectPayload` | Pairing termination: `rejected`/`expired`/`busy` |

## Key frame details

### `hello`
`node_id`, `ver` (version), `card` (compact JSON of the capability
summary, raw payload), `ts`, `nonce`, `sig`, `pub_key`, `ed_sig` (the
Ed25519 identity pair — see the authentication section), `udp_port`
(datagram-plane listen port, 0 = no UDP plane), `caps` (optional wire
capability list: `sessaead` session encryption, `binart` binary data
frames — a peer that does not know a capability simply does not use it),
`authoritative` (declares the sending process holds the node-identity
lock — same-id arbitration lets the real daemon reclaim the peer edge
from a sibling process borrowing the engine). The capability summary is
deliberately passed through as raw JSON, decoupling the transport from
the ledger package that owns `CapabilitySummary`. A hello **reply**
additionally carries `you` — the source IP the peer observed for this
connection, free reflected-address discovery (unsigned: tampering only
buys a wrong candidate list, which the punch handshake itself sees
through).

When hello verification fails the server replies `hello_reject`
(`reason`: `auth` signature/key mismatch, `identity` id conflict or a
pinned identity re-keyed, `replay` signature already consumed) before
disconnecting — the only receipt frame on the unauthenticated channel,
carrying nothing derived from the secret.

### `heartbeat`
`status` (`online`/`busy`/`offline`/`draining` — the maintenance drain
state; inbound delegations are declined), `load` (0.0–1.0), `capacity`
(the card's raw JSON carrying measured live values `live`:
`mem_free`/`disk_free`/`gpu_util`, plus `queued_tasks` queue depth),
optional `card`. Heartbeats fire every few seconds while `hello` only
fires at dial — after a node hot-reloads its capability card, the `card`
in the heartbeat lets peers learn the new abilities immediately instead
of waiting for a reconnect. Old nodes ignore the unknown field; a new
node that never received it falls back to the card seen at `hello`. Each
tick additionally carries:

- `blocked_agents`: the list of agents currently circuit-broken on this
  machine, which peers exclude from the capability set when routing;
- `neighbors` + `links`: adjacency and link metrics (`peer`/`rtt_ms`/
  `kind` transport type), so the link-state graph's edges and weights
  follow the live topology — weighted shortest path (Dijkstra, first
  hop) is fed by it;
- `contacts`: the contact plan (scheduled transfer windows) for DTN
  custody routing; new nodes always send it (empty array = no plan), an
  absent field marks an old node;
- `projects`: project names this machine holds checkouts of (input to
  residency routing scores);
- `ver`: this machine's version, refreshing peers' directory rows
  mid-release without waiting for reconnects (the fleet panel's
  version-skew warning compares it against its own build).

### `task_delegate`
Task handover (§10.3 example). Besides `task_id` / `parent_id` /
`project` / `title` / `context_type` / `intent` / `spec_json` /
`requires` / `preferred_node` (a soft hint) / `target_node` (a hard pin:
executes or forwards to it when resolvable, honestly declines when
not) / `chain` (the anti-loop chain) / `timeout_ms` / `max_retries` /
`complexity` / `risk` / `attempt_id` / `resume_session_id` /
`user_locale`, several points matter:

- **Context transfer** (§12.4) is decided by `context_level`, which
  controls how the executor obtains the full context:
  - `pointer`: `context_hash` points at a snapshot the executor may
    already hold; on a miss it fetches from the source node.
  - `summary`: the intent/spec on the wire is the whole context; no
    snapshot travels.
  - `full`: `context_data` inlines the complete snapshot (base64).
- **Hardware requirement** `resource_json`: the task's declared
  `entry.ResourceProfile`. It travels with the handover because the
  hardware requirement is a property of the *work*, not of the *first
  node* — when a relay re-routes, it must still be able to keep a
  training task off nodes without VRAM; without this field the
  constraint is lost at the first hop.
- **Authorization** `authorized` + `auth_sig`/`auth_pub`/`auth_ts`: the
  original user's tier-2 consent (§16) travels with the task as an
  Ed25519 signature — `auth_sig` signs
  `TaskID:Authorized:TS:ConsentDigest`, the executing side verifies it
  against the origin public key registered in its directory, and
  stripping/tampering/re-signing under another key is refused outright;
  `auth_hops` records the hops the consent has ridden across relays.
  Remote agent runs without a valid consent clamp are compressed to a
  read-only tool surface (an adapter that cannot express that just gets
  `ErrNotAuthorized`). A bare `authorized` boolean is only the
  compatibility form for key-less old nodes.
- **Project travels along**: `project`/`title` identify the owning
  project; `project_pack`/`project_dir` inline project memory and
  metadata; a `context_type=file` repository task additionally packs its
  worktree as a `__worktree__` artifact input (≤256 MiB, skipping
  `.git`/`node_modules`/cache directories). The executor unpacks it in a
  private per-task directory and packs the result back — delegated file
  changes land back on the originating node's checkout instead of dying
  on a remote.
- **Plan plane** (v0.0.6): `plan_id` / `stage_id` identify the stage for
  the orchestrator's audit; `inputs` (an `ArtifactRef` list) declares
  each antecedent stage's packed artifact and the node holding it, from
  which the executor pulls its starting tree. Standalone tasks leave it
  empty.
- **Transport and budgets**: `transport` (`live` | `dtn`) picks the
  real-time link versus store-and-forward; `deadline_unix` is a DTN
  task's absolute deadline (expiry voids it rather than lease-killing
  it); `depth`/`delegation_budget`/`token_budget` are causal depth and
  the per-hop-decremented mesh quotas — a task at zero may only execute
  or decline, never re-route. `bundled_artifacts` carries the input
  artifacts a push-form delegation brings along (§8.3 fat-bundle).
- **Trust clamping** (trust zones, experimental): a delegation toward a
  `guarded`-tier peer is trimmed per `network.trust.send.guarded` —
  `context_level` capped at `pointer` (snapshots leave the node only in
  the audit-pulled `context_fetch`), `project_pack`/`inputs`/consent
  signatures stripped; a task that would lose required inputs to the
  clamp is re-routed rather than degraded-delivered. `open`-tier peers
  neither send nor receive push-mode delegations (the offer/bid market
  is unbuilt). **Screen-capture tasks are a first-class sensitive
  category**: tasks whose `requires`/`action_spec` resolve to
  `hardware:screen_capture` (including wide tokens like `hardware`/
  `hardware:*` that resolve to it) only move on mesh links — non-mesh
  origins are refused at admission (`trust_policy_refused`), the
  send-side clamp refuses rather than degrades, and their output
  artifacts are likewise released only to mesh peers on the
  artifact_fetch/push serving side (neither participation nor signed
  grants can relax this); `screen: true` is the explicit same-owner
  override. Operator-custom actuator ids in the `screen_sensing` class
  cannot be recognized by the static predicate — the executor's
  claim-time `screen_output` runtime marking takes over (re-stamped hop
  by hop on `task_result`, see the `task_result` section). The binding
  also holds when a screen stage's artifact is handed downstream as an
  input: `ArtifactRef.screen` is stamped by the orchestrator from the
  producer row's mark and read on all three sides — routing
  (Needs.Screen), admission, and serving; any task consuming display
  bytes is itself display-bound (its worktree and outputs may embed
  pixels), and the mark cascades stage by stage. A forged mark can only
  narrow the audience — it fails safe in the same direction as the
  result flag.

### `task_accept` / `task_decline`

`task_accept` is the acceptance receipt; newer nodes attach
`sig`/`pub`/`ts` — the executor's Ed25519 signature over (task,
attempt, signer, ts), verified by the delegator and each relay alike
(>5min skew, wrong key, or malformed → dropped). Consent says "what may
run"; signed acceptance says "who agreed to run" — forwarding either
side cannot forge the other; signature-less frames from old nodes stay
compatible.

`task_decline` may carry a machine-stable `reason_code` besides the
human-readable `reason`: `trust_open_origin` (origin tier is open; the
push-mode origination is refused), `trust_policy_refused` (the
receiver's admission policy refused), `trust_send_ceiling` (send-side
clamping stripped a dependency the task cannot run without — the right
response is re-routing, not retrying). An empty code is an ordinary
pre-trust-zone refusal (drain, capacity, anti-loop), handled by
re-routing as before.

Both frames carry `attempt_id` and land under the same **directional
gate**: a wire attempt strictly older than the row's attempt is the
late echo of a dispensation already replaced — dropped, since a
decline/accept landing after rotation would bounce a live dispensation
back to queued or mark it running on a dead run's word. Equal or newer
wire attempts pass — sibling copies legitimately diverge in attempt
order (each side rotates independently and adopts lazily), so a verdict
tracks the work, not one particular execution. The write behind the
gate is attempt-CAS'd too (`Accept`/`Decline` expected-attempt
predicates), closing the read-check-to-store TOCTOU; an accept whose
CAS fails is not relayed upstream as though our copy accepted it.

### `task_cancel`

`TaskCancelPayload.attempt_id` splits cancellation into two semantics
matching its two classes of sender:

- **Tree cancel** (`attempt_id` empty): a human decision — `panda
  cancel`, console reject, approval denial. Unconditionally cascades
  the whole task tree: the local cascade kills the row and all its
  descendants, `cancel_outbox` carries the order to downstream
  executors under store-and-forward custody, and the receiver's
  `handleCancel` re-cascades hop by hop to the leaves.
- **Dispensation cancel** (`attempt_id` set): the lifecycle
  subsystem's stand-down order for **one specific dispensation** —
  lease-expiry reroute, supersede replacement, absolute-deadline
  verdict. It may only land on the dispensation it named: a receiver
  whose row already moved strictly newer (a fresh delegate arrived
  first, or a rotation was adopted) drops the frame outright and
  writes an audit line instead of killing the live attempt.
  Authorization precedes every task-level verdict: the sender must be
  the row's owner or its immediate chain predecessor, compared by
  runtime identity so a bounced daemon's fresh instance id still
  binds — a peer outside the task's path cannot even write its audit
  trail.

Parked `cancel_outbox` rows follow the same scope rules: parking a
fresh delegate clears only dispensation-scoped cancels for the task
(the stand-down of the dispensation the new delivery replaces), while
a human tree cancel is never lost to a park; on flush cancels are
delivered before delegates, and a live-delivered dispensation cancel
also clears only its own kind.

### `task_delegate` supersede semantics

A re-delivered `task_delegate` is not inherently idempotent: when the
row still exists and the wire attempt is strictly newer, the frame is
a re-route's replacement order. But the right to replace is not proven
by the chain-tail signature alone — the sender must be **a node that
legitimately held the task upstream of us in the stored chain** (when
a mid-chain link lapses, an ancestor may re-route straight past it).
A delegate from outside the path minting a fresh attempt id cannot
delete or replace any row — without the bind, the same frame is a
remote task-kill primitive. Before replacement lands, a live copy is
cascaded down first (`CancelCascade` + `finishCancel`: kill the
runner, free its context reservation, forward the stand-down
downstream); a terminal/failed copy is deleted and recreated; and a
stale delegate carrying an older attempt is always dropped, never
allowed to resurrect a settled copy.

### `task_result`
`task_id` / `attempt_id` / `chain` (the executor's real delegation
chain, letting a receiver that lost its local row rebuild the upstream
path) / `state` (the executor's persisted state — old nodes may omit
it; when absent, done/failed is derived from `ok`; new nodes must keep
`review` so a parent does not mis-upgrade it to done) /
`approval_disposition` (what this review approval means: resume
execution, accept finished work, or trust-admission review — defaults
classified by conservative rule) / `deferred` (the result reports
custody receipt, not execution: the approval parked the run into the
outbox to be delivered when the link recovers — UIs should show "queued
for delivery", not a failed exit code) / `ok` / `exit_code` / `stdout` /
`stderr` / `artifacts` / `tokens` / `cost` / `output_artifact` (the hash
of the stage's packed artifact, recorded by the orchestrator to hand to
successor stages as input) / `screen_output` (when true, the task's
output artifacts are marked display content — the executor stamps it at
claim when it resolves a `screen_sensing`-class actuator, and every
delegator hop receiving the result re-stamps its own task row;
one-directional tightening semantics: a forged true only narrows the
audience, it cannot forge a relaxation) / `files_changed` (worktree
relative-path list, capped at 200, letting the delegator see what the
task actually touched rather than trusting the agent's narration) /
`question` (a clarifying question the agent parked, carried back to the
origin with the review state, answered by `task_resume.answer`) /
`executor` / `agent` / `model` / `work_dir` / `duration_ms` /
`session_id` / `agent_session_id` (attribution: who ran what, where,
for how long).

`stdout`/`stderr` are trimmed by `maxWireText` (512 KiB): a child
process can produce up to 8 MiB per stream while the frame cap is
4 MiB — untrimmed, the result could never land. Trimming keeps **head
and tail** (the beginning holds intent, the end conclusions), splits at
rune boundaries to keep valid UTF-8, and inserts a line "middle
truncated, full output remains on the executing node". The full output
stays on the executing node, fetchable as an artifact when needed.

### The inter-task message board: `task_message`
A bounded short-message channel between sibling tasks (implementation:
`internal/core/messages.go`), riding the QoSSignal lane. Only
two scopes: `plan:<plan-id>` (stages of one plan) and
`task:<parent-id>` (a parent and its child set — the parent itself is a
member).

- **member → owner leg**: `scope` / `sender` (sending task id) / `to`
  (an unresolved address: `stage:<id>`, `parent`, `siblings`,
  `children`, or a member task id) / `grade` (`status` ≤400B / `report`
  ≤4096B / `artifact_ref`) / `body` / `seq` (sender's monotone counter —
  the receiver uses it to refuse late old copies) / `sent_at` /
  `sender_label` (the sender's display name — stage id or title).
- **owner → member fan-out leg**: `fanout=true`, `rcpt` is the recipient
  task id the owner resolved (`""` = broadcast), `member` names which
  member row this copy lands under — the receiver uses it to verify
  "this node really holds a member row for that scope", dropping on
  failure (forged/misdirected).
- **Who is the owner**: a plan board's owner is the plan origin
  (`chain[0]`, holding all stage rows); a children board's owner is the
  parent's executing node (a locally spawned row is this node, a row
  arriving over the wire is taken from `Predecessor` — the same rule as
  result reporting).
- **Delivery semantics**: best-effort, no dedicated outbox — the slot
  model (one slot per sender per recipient, newer overwrites older) lets
  lost frames self-heal on the next send; reads happen at supervision-tick
  boundaries and never block a running agent. TTL 24h; per-episode send
  budget `messages.budget` (default 8).

### Artifact frames: `artifact_fetch` / `artifact_chunk`
Fixed-size chunked **pull**: the side needing an artifact asks for one
range at a time, requesting the next only after the previous landed;
lost/corrupt chunks are "ask again", not a failed whole.

- `artifact_fetch`: `task_id` / `hash` / `offset`.
- `artifact_chunk`: `offset` / `data` (base64) / `total` (full size —
  provides progress and refuses a stream that changes length mid-way) /
  `eof` (the last range; pooling is allowed only after the whole passes
  the `hash` check) / `ok` / `reason` (when the peer does not hold or
  declines to serve, `ok = false` — the requester asks another node
  instead of retrying forever).

Serving-side authorization (`artifactTransferAuthorized`) adds a
display-content rule on top of the participation check: artifacts
produced by tasks marked `screen_output` are released only to mesh-tier
peers — participation, upstream identity, and signed grants cannot
relax it; the authorization-decision cache (1-min TTL) remembers the
screen mark and re-checks the peer's tier on every hit, so a trust
demotion cuts the transfer immediately rather than waiting out the TTL.
The same rule constrains `artifact_push` receivers.

### Artifact push: the `artifact_push` three-frame set
The mirror of pull: **proactive push**, for DTN/disconnected scenarios —
the peer may be unable to initiate `artifact_fetch`. `artifact_push`
carries `offset`/`data`/`total` per range; the receiver answers
`artifact_push_status` reporting `received_through` (consecutive bytes
from 0), and the sender's outbox retires rows only by that coverage
count — a crash right after sending still retransmits unacknowledged
ranges on the next flush, leaving no permanent gap;
`artifact_push_done` closes it out: pooled after the content hash checks
out (`ok`), or the peer declined/already holds it (`reason`).

### DTN bundle: `dtn_bundle`
`blob` is the verbatim bytes of a CBOR-encoded bundle (§8.3). The
receiver unpacks, verifies the signature, checks TTL, then feeds the
inner payload back into the normal message path — a bundle is a signed
transport container, not a second task protocol. v1 cleartext payloads
are verified for compatibility only; v2+ payloads are AES-256-GCM
sealed; `DestEID` uses the stable identity key (`k:<pub>`), resolved
through the directory to the current instance.

### NAT punch coordination: `punch_offer` / `punch_ready`
Rides the envelope (direct link when one exists, otherwise relayed
across the mesh along the link-state graph, TTL decrementing per hop
from `PunchMaxTTL=8`), coordinating a UDP punch session between two
NATed nodes: `nonce` is the session credential in the punch datagrams,
`src` is the true source node id (relays re-pack the envelope as
themselves — `env.From` is only the previous hop), `hosts` is the
initiator's full candidate list (local interfaces + the reflected IP
observed via `hello.you` + STUN results, all on the UDP listen port —
punch traffic always sprays from the shared socket, so the hole the NAT
opens is the listen port). `punch_ready` echoes the nonce and attaches
its own candidates; both sides spray simultaneously. `punch:<node-id>`
entries in `network.peers` trigger this path; a punched endpoint becomes
a fallback route for `sendTo`.

## LAN discovery beacons and pairing

The discovery surface is outside the envelope system — a separate UDP
broadcast (`internal/core/discovery.go`, `network.discovery_addr`,
default `:7837`, `"off"` disables): every 15s a `panda-beacon/1`
datagram (≤1 KiB) carrying id/addr/ver/pubkey/ts and an Ed25519
signature. A beacon is a **hint**, not membership proof — the signature
pins "this address claims this fingerprint", stopping a LAN stranger
from harvesting signed hellos under someone else's node id + public
key; signature-verified rows enter `pending_nodes` (60s TTL, 64-row
cap, verified rows evict unverified ones first), and admission still
needs a normal hello handshake after `panda nodes admit`/`nodes add`.
Own beacons and already-admitted ids are ignored; loopback listeners
and nodes without a shared secret do not broadcast.

Pairing (`panda pair`) rides the four `pair_*` frames above: ephemeral
X25519 exchange + human confirmation of the short verification code on
both sides + AES-256-GCM sealed delivery of the shared secret — the
shared secret never crosses the network in cleartext, and comparing the
pairing code blocks a MITM relay (different DH on the two ends yields
different codes).
