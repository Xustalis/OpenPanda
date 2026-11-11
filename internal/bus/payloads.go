// SPDX-License-Identifier: AGPL-3.0-or-later

package bus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
	"unicode/utf8"
)

func unixNow() int64 { return time.Now().Unix() }

// Message payloads. Each type has a Go struct used for typed (de)serialization
// at the handler layer.

// HelloPayload is sent when a node connects to declare its identity. Card is
// the node's capability summary (a compact JSON object); it is carried as raw
// JSON so the transport stays decoupled from the ledger package that owns the
// CapabilitySummary type. Sig is the HMAC-SHA256 (hex) of NodeID, Ts, and —
// when present — Nonce under the shared secret, proving the identity was
// minted by a node that holds the secret and bounding replay to MaxHelloAge
// (design §16 / P0-1). Nonce makes every dial's signature unique even when two
// hellos land inside the same second: the receiver's single-use replay cache
// keys on signed fields only, so without it an honest same-second reconnect is
// indistinguishable from a replay. Old peers send none and are verified
// against the two-field form.
type HelloPayload struct {
	NodeID string `json:"node_id"`
	Ver    string `json:"ver"`
	// Prov is the sender's build provenance (release | self | unsigned) —
	// the version.ProvenanceClass stamp. Absent means a pre-provenance peer
	// that does not send the field; a current dev build sends "unsigned"
	// explicitly. Self-asserted and outside the signature domain, like Ver:
	// honest-node bookkeeping the receiver's trust policy reads
	// (accept.<tier>.self_signed), never a security proof — the locally
	// asserted tier stays the boundary.
	Prov   string          `json:"prov,omitempty"`
	Card   json.RawMessage `json:"card,omitempty"`
	Ts     int64           `json:"ts,omitempty"`    // unix seconds, bound into Sig
	Nonce  string          `json:"nonce,omitempty"` // per-dial random, bound into Sig when present
	Sig    string          `json:"sig"`
	PubKey string          `json:"pub_key,omitempty"` // Ed25519 public key hex (cryptographic identity)
	EdSig  string          `json:"ed_sig,omitempty"`  // Ed25519 signature over NodeID:Ts:Nonce
	// To names the node this hello is addressed to. When set, the hello is
	// signed for that receiver alone (SigB/EdSigB) and a receiver will refuse
	// it unless the id is its own — the cross-node replay a member could
	// otherwise mount with a captured hello inside the freshness window.
	// Empty means the sender predates binding, or could not resolve the
	// address it dialed to a node id; such a hello is verified the legacy way
	// and SigB/EdSigB carry nothing.
	To     string `json:"to,omitempty"`
	SigB   string `json:"sig_b,omitempty"`    // HMAC over NodeID:To:Ts:Nonce
	EdSigB string `json:"ed_sig_b,omitempty"` // Ed25519 over the same bytes
	// XPub is the node's X25519 identity key (hex) — the second half of the
	// dual-key identity (cross-network mesh design §3): Ed25519 signs,
	// X25519 agrees transit keys. It is self-asserted like UDPPort: only
	// recorded after the hello's EdSig verified the claimed Ed25519 key, and
	// a peer that lies about its own x_pub only breaks sealing TO itself.
	// Peers that predate the field send nothing and transit to them
	// degrades to the member-level mesh seal.
	XPub string `json:"x_pub,omitempty"`
	// UDPPort advertises the sender's datagram-plane listener (farsky). A
	// peer that cannot hold a TCP session to us can still be reached by a
	// punch: combine this port with the IP we observe its connection coming
	// from and the reflexive addresses its punch offer carries. Zero means
	// the node runs no UDP plane; old peers send nothing.
	UDPPort int `json:"udp_port,omitempty"`
	// You is set only on the hello REPLY: the IP this peer's connection
	// arrived from, as our listener observed it. It is free reflexive
	// discovery — a node behind NAT learns its public IP without a STUN
	// server, and punches at <You>:<own UDP port>. Unsigned (the sig covers
	// NodeID/Ts/Nonce only): a tampered You costs the attacker nothing but a
	// wrong candidate list, which the punch handshake detects anyway.
	You string `json:"you,omitempty"`
	// Caps advertises optional wire features this build supports (e.g.
	// binary data frames) so a peer can pick the better encoding without a
	// version-number parse. Absent on old nodes; receivers that do not know
	// a listed capability simply never exercise it.
	Caps []string `json:"caps,omitempty"`
	// Authoritative declares that the sending process holds the node
	// identity lock — the daemon, not a borrowed engine (CLI/TUI sibling
	// that probe-passed at startup). Same-id arbitration uses it to let a
	// real daemon reclaim its peer edge from a sibling session that claimed
	// ownership while the daemon was down: without it, a live stray conn
	// holds the edge indefinitely and delegated work lands on the wrong
	// engine. Unsigned like You — a forged claim is no worse than a forged
	// node id, which the sig already gates.
	Authoritative bool `json:"authoritative,omitempty"`
}

// HelloRejectPayload is what the server sends before closing a connection
// whose hello failed. Reason is a stable machine-readable code:
// "auth" (signature/secret mismatch — the usual case, and the one a dialer
// must surface so an operator knows the peer is online but rejecting us),
// "identity" (claimed id collides or re-keys a verified identity),
// "binding" (the peer has bound its hello before and sent an unbound one —
// a downgrade, or a rollback to a build without binding), and "replay" (a
// consumed signature). It deliberately carries nothing derived from the
// secret — the frame rides the still-unauthenticated channel.
type HelloRejectPayload struct {
	Reason string `json:"reason"`
}

// CapBinaryData is the hello-advertised capability for binary data frames:
// a peer that lists it accepts data-carrying envelopes (artifact_chunk,
// artifact_push) as a WS binary frame — 2-byte header length + JSON header +
// raw bytes — instead of the JSON/base64 form. A sender checks it before
// choosing the encoding, so mixed-version links keep working.
const CapBinaryData = "binart"

// HeartbeatPayload carries status + capacity. Card is an optional capability
// summary (same compact JSON form as HelloPayload.Card) sent when the sender
// just reloaded its card, so peers learn the new abilities without waiting for
// a reconnect — heartbeats flow every few seconds, hellos only at dial time.
// Old nodes that do not know the field ignore it; new nodes that receive it
// without it fall back to the hello-time card.
type HeartbeatPayload struct {
	Status   string          `json:"status"`   // online|busy|offline
	Load     float64         `json:"load"`     // 0.0-1.0
	Capacity string          `json:"capacity"` // raw JSON from the card
	Card     json.RawMessage `json:"card,omitempty"`
	// BlockedAgents lists agent names whose circuit is open on the sender
	// (repeated failures). Peers strip these from the sender's ability set
	// when routing, so failure history weighs into candidate selection
	// instead of being learned one bounce-decline at a time. Absent on old
	// nodes and when nothing is blocked; receivers treat absent as "drop
	// any previously published list".
	BlockedAgents []string `json:"blocked_agents,omitempty"`
	// Neighbors/Links refresh the sender's adjacency in the receiver's
	// directory every beat (§4.1): hellos only arrive at dial time and card
	// beats only at reload, but the link-state graph's edge set AND its
	// measured weights have to track the live topology continuously.
	Neighbors []string     `json:"neighbors,omitempty"`
	Links     []LinkMetric `json:"links,omitempty"`
	// Contacts carries the sender's advertised contact plan (§8.4): the
	// scheduled transmission windows custody routing plans custody moves
	// around. Semi-static — it changes on config reload, not per beat — but
	// rides the same gossip channel so a peer's plan arrives without waiting
	// for a card refresh. Deliberately NOT omitempty: a new node always emits
	// the field (empty array = "no plan"), so removing a plan clears the
	// peer directory's copy; a field absent outright is an old node's beat
	// and leaves the stored plan alone.
	Contacts []Contact `json:"contacts"`
	// Projects carries the sender's advertised residence set (§6.3): the
	// names of the projects it holds a checkout of. Semi-static like the
	// contact plan — it changes when work lands, not per beat — and shares
	// the same contract: a new node always emits it (empty = "no projects"),
	// while a field absent outright is an old node's beat and leaves the
	// stored set alone.
	Projects []string `json:"projects"`
	// Ver is the sender's software version (Track 3). Heartbeats repeat the
	// hello's stamp so a mid-release upgrade refreshes every peer's
	// directory row without waiting for a reconnect — that row is what the
	// fleet panel's version-skew warning diffs against its own build.
	// Absent on old nodes; receivers leave any stored stamp alone.
	Ver string `json:"ver,omitempty"`
	// Prov mirrors HelloPayload.Prov on every beat so a mid-life binary
	// swap (update apply, self-build) refreshes the directory row without
	// waiting for a reconnect. Absent on old nodes; receivers leave any
	// stored stamp alone.
	Prov string `json:"prov,omitempty"`
	// DirDigest is the sender's directory table digest (cross-network mesh
	// M1): SHA-256 over the sorted "id:content_hash" lines of every gossipable
	// row (session/ghost rows excluded). A receiver whose own digest differs
	// sends dir_pull — matching digests mean the two tables already agree and
	// no frames flow. Absent on old nodes; receivers skip the compare.
	DirDigest string `json:"dir_digest,omitempty"`
	// EdgeLoad is the sender's live peer-edge count (M2): rendezvous
	// scoring prefers a candidate whose mesh is not already pinned on it —
	// a node carrying forty edges is a worse redundancy pick than one
	// carrying four. Absent on old nodes; receivers treat it as unknown.
	EdgeLoad int `json:"edge_load,omitempty"`
}

// LinkMetric is the wire form of one measured edge weight (§4.1): the round-
// trip time in milliseconds toward a directly connected neighbor, sampled by
// the transport's ping/pong. It duplicates ledger.LinkMetric's JSON shape so
// the transport package stays decoupled from the capability directory — the
// same rule that keeps Card a raw JSON blob above.
type LinkMetric struct {
	Peer  string `json:"peer"`
	RTTms int64  `json:"rtt_ms"`
	// Kind names the transport carrying the edge right now ("ws", "udp") —
	// the fleet view's per-peer transport display (Track 3). Empty on nodes
	// that predate the field; their only transport was ws.
	Kind string `json:"kind,omitempty"`
}

// Contact is the wire form of one scheduled transmission window (§8.4),
// duplicating ledger.Contact's JSON shape for the same decoupling reason as
// LinkMetric above. [Start, End) is the first window in unix seconds;
// Period > 0 repeats it.
type Contact struct {
	Peer    string `json:"peer"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	RateBps int64  `json:"rate_bps,omitempty"`
	Period  int64  `json:"period,omitempty"`
}

// TransitMaxTTL bounds how many relay hops a transit frame may spend. Six
// matches the design's relay budget: long enough for a re-forwarded path
// through a sparse graph, short enough that a loop dies quickly on top of
// the msg-id dedup that kills the two-hop case outright.
const TransitMaxTTL = 6

// TransitPayload is the outer shell of a transit frame (cross-network mesh
// design M3/§3). A relay reads ONLY this: where the frame is headed, how
// many hops it has left, which seal level wraps the body, and the sealed
// body itself. Inner is the AEAD blob (nonce‖ciphertext, plus the ephemeral
// key prefix under the x25519 level) carrying a transitInner — the original
// envelope plus the sender's Ed25519 attestation binding env.From to its
// identity. The blob never rides plaintext; relays are blind by design.
type TransitPayload struct {
	Dest  string `json:"dest"`  // stable "k:<pub>" identity or instance id
	TTL   int    `json:"ttl"`   // decremented per relay; <=1 dies at the next relay
	Seal  string `json:"seal"`  // SealX25519 | SealMesh
	Inner []byte `json:"inner"` // sealed transitInner blob
}

// DialCheckPayload asks the receiver to try reaching this node once, so an
// advertised rendezvous role is proven rather than claimed. Addr is the ws
// endpoint to dial (host:port); UDP carries the datagram port for a future
// STUN-bind probe. Nonce correlates the answer with the request.
type DialCheckPayload struct {
	Addr  string `json:"addr"`
	UDP   int    `json:"udp,omitempty"`
	Nonce string `json:"nonce"`
}

// DialCheckAckPayload reports what a dial_check actually achieved. Proto is
// "ws" for the TCP probe (udp arrives with the datagram-plane variant).
type DialCheckAckPayload struct {
	Nonce string `json:"nonce"`
	OK    bool   `json:"ok"`
	Proto string `json:"proto,omitempty"`
}

// PunchWantPayload is the auto-punch request (M3): the sender asks the
// receiver — the lexicographically-smaller, offerer-elected side — to send
// it a punch_offer. TTL bounds relay hops the same way punch_offer's does.
//
// Src names the REQUESTER and must survive the relay. A relayed envelope is
// re-minted as the forwarder's (env.From = this node) because the receiving
// conn authenticates envelopes by their bound peer id — so the requester
// would otherwise be lost, and the elected offerer would punch the RELAY
// instead of the node that asked. It rides the payload for the same reason
// PunchOfferPayload.Src does.
type PunchWantPayload struct {
	Src string `json:"src,omitempty"`
	TTL int    `json:"ttl"`
}

// DirPullPayload is the differential-sync request (M1): the requester's own
// id→content_hash stamp table plus an optional resume cursor for paged
// replies. The responder diffs it against its own table and answers
// dir_rows — rows the requester lacks or hashes differently on, plus a want
// list of the ids the RESPONDER lacks, so a single exchange converges both
// directions.
type DirPullPayload struct {
	// Stamps maps directory id → content_hash (hex). Bounded by the sync
	// cap; a requester with more rows sends the prefix and pages with After.
	Stamps map[string]string `json:"stamps"`
	// After resumes a paged reply: only rows with id > After are eligible.
	// A non-empty After marks the pull as a continuation — the 30s per-peer
	// pull limit applies to fresh pulls, not to finishing a sync already
	// underway.
	After string `json:"after,omitempty"`
}

// DirRowsPayload carries signed directory rows plus the responder's want
// list. Rows travel as canonical JSON blobs — each is a credential signed by
// its owner's Ed25519 key, forwarded byte-for-byte so the signature keeps
// verifying no matter how many hops it crossed.
type DirRowsPayload struct {
	Rows []json.RawMessage `json:"rows,omitempty"`
	// Want lists the ids the SENDER of this frame still lacks: the receiver
	// should answer with one dir_rows carrying those rows (and no want of
	// its own — one round completes the exchange).
	Want []string `json:"want,omitempty"`
	// Cursor/More paginate: when the response hit a budget (row count or
	// byte cap) before the last differing id, Cursor is the last id sent and
	// More is true; the requester continues with dir_pull{after: cursor}.
	Cursor string `json:"cursor,omitempty"`
	More   bool   `json:"more,omitempty"`
}

// DirRow is the canonical row shape for content hashing and signing (M1 §2).
// content_hash covers every field EXCEPT seen_at and sig — freshness churn
// must not defeat the digest — while sig signs everything except itself
// (seen_at included: a gossiped row's age is source-minted, not attacker-
// rewritable). Field order here IS the canonical serialization order.
type DirRow struct {
	ID        string          `json:"id"`
	PubKey    string          `json:"pub,omitempty"`
	XPub      string          `json:"x_pub,omitempty"`
	Ver       string          `json:"ver,omitempty"`
	Card      json.RawMessage `json:"card,omitempty"` // CapabilitySummary JSON
	Addrs     []string        `json:"addrs,omitempty"`
	Neighbors []string        `json:"neighbors,omitempty"`
	Links     []LinkMetric    `json:"links,omitempty"`
	UDPPort   int             `json:"udp_port,omitempty"`
	Relay     json.RawMessage `json:"relay,omitempty"` // RelayProfile JSON
	SeenAt    int64           `json:"seen_at"`
	Sig       string          `json:"sig,omitempty"` // Ed25519 over canonical minus sig
}

// TaskDelegatePayload is the task handoff (design doc §10.3 example). The
// context transfer fields (design doc §12.4) select how the executor obtains
// the task's full context:
//   - context_level "pointer": context_hash references a snapshot the executor
//     may already have; on miss it fetches from the source node.
//   - context_level "summary": the intent/spec carried on the wire is the whole
//     context (no snapshot transfer).
//   - context_level "full": context_data carries the inline snapshot (base64).
type TaskDelegatePayload struct {
	TaskID       string   `json:"task_id"`
	ParentID     string   `json:"parent_id,omitempty"`
	Project      string   `json:"project,omitempty"`
	Title        string   `json:"title,omitempty"`
	ContextType  string   `json:"context_type,omitempty"`
	ContextHash  string   `json:"context_hash,omitempty"`
	ContextLevel string   `json:"context_level,omitempty"` // pointer|summary|full
	ContextData  []byte   `json:"context_data,omitempty"`  // inline full snapshot
	Intent       string   `json:"intent"`
	SpecJSON     string   `json:"spec_json,omitempty"`
	Requires     []string `json:"requires,omitempty"`
	// Paths is the child's declared project scope — the subtrees it owns
	// inside a shared worktree. It rides the wire so the receiving node
	// derives the same scope leaf locks the origin used: same-node siblings
	// with disjoint claims parallelize, overlapping ones serialize, and an
	// empty claim keeps the whole-project lock on every hop.
	Paths []string `json:"paths,omitempty"`
	// Isolate gives the child a private copy of the project tree on the
	// executing node (git worktree when available) instead of the shared
	// one; the child locks nothing shared and its changes merge back into
	// that node's project tree when it finishes. Carried so every hop and
	// requeue preserves the mode the origin's agent chose.
	Isolate       bool   `json:"isolate,omitempty"`
	PreferredNode string `json:"preferred_node,omitempty"` // soft routing hint; honored when it matches
	// TargetNode is the hard pin: the receiving node executes when the pin
	// resolves to itself, forwards toward it when it resolves onward, and
	// declines honestly when it resolves nowhere. It is the resolved row id
	// by the time it leaves the origin, so every hop reads the same identity.
	TargetNode      string   `json:"target_node,omitempty"`
	Chain           []string `json:"chain"`
	TimeoutMS       int64    `json:"timeout_ms,omitempty"`
	MaxRetries      int      `json:"max_retries,omitempty"`
	Complexity      float64  `json:"complexity,omitempty"`
	Risk            string   `json:"risk,omitempty"`
	AttemptID       string   `json:"attempt_id,omitempty"`
	ResumeSessionID string   `json:"resume_session_id,omitempty"`
	// ResumeSessionAdapter names the adapter file that minted
	// ResumeSessionID; the executor drops the hint when the run lands on a
	// different adapter rather than feeding it a foreign handle. It stays
	// OUT of ConsentDigest: it is routing provenance, not consent — and
	// keeping it unbound lets a mixed-version mesh verify a delegate frame
	// whose sender did not yet emit the field.
	ResumeSessionAdapter string `json:"resume_session_adapter,omitempty"`
	// ResourceJSON is the task's declared hardware requirement (a marshalled
	// entry.ResourceProfile). It travels with the delegation because the
	// requirement is a property of the work, not of the node that first saw it:
	// a relay that re-routes a task onward must be able to keep a training run
	// off a node with no VRAM, and without this field the constraint would be
	// lost at the first hop.
	ResourceJSON string `json:"resource_json,omitempty"`
	// Authorized carries the origin user's tier-2 consent (design §16) so a
	// delegated task does not bounce at the executor's defense layer. It is
	// only meaningful on an authenticated bus: the transport's shared-secret
	// HMAC is what makes this unforgeable by non-peers, and the origin node
	// only sets it after the user explicitly authorized (task add
	// --authorize / ask --authorize).
	Authorized bool   `json:"authorized,omitempty"`
	AuthSig    string `json:"auth_sig,omitempty"` // Ed25519 signature over TaskID:Authorized:TS (tamper-proof approval)
	AuthPub    string `json:"auth_pub,omitempty"` // Ed25519 public key hex of the authorizing node
	AuthTs     int64  `json:"auth_ts,omitempty"`  // unix seconds the consent was minted, bound into AuthSig
	// AuthHops bounds how far Authorized may travel (hop-limited consent):
	// each relay decrements it before forwarding onward, and a payload whose
	// hops are spent has its consent cleared, so the executor's defense layer
	// asks for fresh approval instead of running irreversible work under a
	// consent minted several nodes away. Zero alongside Authorized means
	// "legacy/unlimited" — senders from before this field never set it, and
	// the network's behavior for them is unchanged. New senders always set
	// it; direct re-dispatches (decline re-route, queue forward) use 1.
	AuthHops int `json:"auth_hops,omitempty"`
	// Plan-plane fields (v0.0.6). A delegated stage carries its place in the
	// plan and the artifacts it consumes: PlanID/StageID identify it for the
	// orchestrator's audit trail, and Inputs names each predecessor's packed
	// output plus a node that holds it, so the executor can pull the tree it
	// must start from. Empty on a standalone task.
	PlanID  string        `json:"plan_id,omitempty"`
	StageID string        `json:"stage_id,omitempty"`
	Inputs  []ArtifactRef `json:"inputs,omitempty"`

	// Project plane (v0.0.8). A task that belongs to a project used to travel as
	// a bare project *name*, which meant nothing on the receiving machine: the
	// executor looked the name up in its own empty projects directory, found no
	// memory and no tree, and ran an agent that could not tell what it was
	// working on. These two fields are what make the name mean something.
	//
	// ProjectPack is the project's own directory — its MEMORY.md and any
	// project-scoped skills — packed as a tar.gz and carried inline. It is
	// bounded (MaxProjectPackBytes) because it rides in the message rather than
	// being fetched: project memory is capped at a few thousand characters by
	// design, so inline is cheaper than a round trip.
	//
	// The project's *work tree* does not travel here. It goes through the
	// artifact plane as an entry in Inputs, which already knows how to move a
	// tree in chunks and how to skip the transfer when the receiver holds the
	// hash already.
	//
	// ProjectDir is the origin's path for the tree. It is display and log
	// material only — a path from another machine names nothing here, and the
	// executor derives its own directory (see Core.projectWorkDir).
	ProjectPack []byte `json:"project_pack,omitempty"`
	ProjectDir  string `json:"project_dir,omitempty"`
	// UserLocale carries the origin user's language preference across nodes.
	UserLocale string `json:"user_locale,omitempty"`
	// BundledArtifacts carries self-contained input artifacts for proactive push (whitepaper §8.3).
	BundledArtifacts []FatBundleArtifact `json:"bundled_artifacts,omitempty"`
	// Transport specifies whether the delegation travels via live streaming or DTN ("live" or "dtn").
	Transport string `json:"transport,omitempty"`
	// DeadlineUnix is the absolute task deadline for DTN tasks (§8.2): past
	// it the task is expired rather than lease-killed, because a
	// store-and-forward path has no heartbeat to renew against. It is the
	// origin's wall-clock intent carried inside the sealed payload — the
	// bundle that wraps it converts the same bound to a relative lifetime
	// (Bundle.LifetimeSec) for custody decisions at relays.
	DeadlineUnix int64 `json:"deadline_unix,omitempty"`
	// Causal-depth and budget fields (§6.1). Depth is Parent.Depth+1 at every
	// spawn hop, hard-capped by scheduler.MaxChainDepth. DelegationBudget is
	// the remaining mesh-wide delegation quota: each forward decrements it,
	// and a task that reaches zero may only execute or decline — never route
	// onward. The pointer distinguishes an absent field (a pre-budget peer —
	// the receiver seeds the default) from an explicit zero (a new-protocol
	// peer reporting the quota spent — the task must not route onward).
	// TokenBudget is the remaining LLM token quota the task may
	// consume across the mesh; zero means unbounded (legacy peers).
	Depth            int   `json:"depth,omitempty"`
	DelegationBudget *int  `json:"delegation_budget,omitempty"`
	TokenBudget      int64 `json:"token_budget,omitempty"`
	// MaxDepth advertises the effective delegation.max_depth the sender
	// enforced for this subtree — its own config tightened by whatever its
	// ancestors carried. The receiver clamps min(its own config, this value)
	// and persists the result, so the strictest bound any hop's operator set
	// governs the whole subtree. Zero = legacy sender: the receiver's own
	// config alone applies.
	MaxDepth int `json:"max_depth,omitempty"`
}

// ConsentDigest is the content hash a tier-2 consent grant binds to (the
// digest leg of AuthMsgBytesV2). It covers the fields that decide WHAT the
// task is — identity, intent, spec, requirements, inputs, project material —
// but deliberately not HOW it travels: the delegation chain, spent budgets,
// lease stamps and the fat-bundle cache are legitimately rewritten by relays
// in transit, and binding them would break consent on honest routes. The fat
// bundle's Data bytes are content-addressed against Inputs/ContextHash (which
// ARE bound), so excluding BundledArtifacts loses nothing to tampering.
//
// The whitelist struct is load-bearing: new payload fields must not silently
// change the digest, because a digest mismatch across versions drops consent
// mid-mesh (fail closed, but needlessly).
//
// The clamp mirrors clampForWire exactly: the signer computes this digest
// before marshal and the verifier after unmarshal, so the digest must cover
// what actually crossed the wire — clamped-off inline blobs are nil on both
// sides.
func (p TaskDelegatePayload) ConsentDigest() string {
	if len(p.ContextData) > MaxContextDataBytes {
		p.ContextData = nil
		if p.ContextLevel == "full" {
			p.ContextLevel = "pointer"
		}
	}
	if len(p.ProjectPack) > MaxProjectPackBytes {
		p.ProjectPack = nil
	}
	type consentBound struct {
		TaskID       string   `json:"task_id"`
		ParentID     string   `json:"parent_id,omitempty"`
		Project      string   `json:"project,omitempty"`
		Title        string   `json:"title,omitempty"`
		ContextType  string   `json:"context_type,omitempty"`
		ContextHash  string   `json:"context_hash,omitempty"`
		ContextLevel string   `json:"context_level,omitempty"`
		ContextData  []byte   `json:"context_data,omitempty"`
		Intent       string   `json:"intent"`
		SpecJSON     string   `json:"spec_json,omitempty"`
		Requires     []string `json:"requires,omitempty"`
		// Paths and Isolate decide the task's write lanes and whether it runs
		// against a private copy of the project. They were added after the
		// whitelist was written and silently went unbound: an authenticated
		// relay could rewrite the scope or flip isolation and the origin's
		// Ed25519 grant still verified (P1). Older peers send both zero, so
		// binding them does not move the digest for a mixed-version mesh.
		Paths           []string      `json:"paths,omitempty"`
		Isolate         bool          `json:"isolate,omitempty"`
		PreferredNode   string        `json:"preferred_node,omitempty"`
		TargetNode      string        `json:"target_node,omitempty"`
		MaxRetries      int           `json:"max_retries,omitempty"`
		Complexity      float64       `json:"complexity,omitempty"`
		Risk            string        `json:"risk,omitempty"`
		AttemptID       string        `json:"attempt_id,omitempty"`
		ResumeSessionID string        `json:"resume_session_id,omitempty"`
		ResourceJSON    string        `json:"resource_json,omitempty"`
		PlanID          string        `json:"plan_id,omitempty"`
		StageID         string        `json:"stage_id,omitempty"`
		Inputs          []ArtifactRef `json:"inputs,omitempty"`
		ProjectPack     []byte        `json:"project_pack,omitempty"`
		ProjectDir      string        `json:"project_dir,omitempty"`
		UserLocale      string        `json:"user_locale,omitempty"`
		DeadlineUnix    int64         `json:"deadline_unix,omitempty"`
		Depth           int           `json:"depth,omitempty"`
	}
	raw, err := json.Marshal(consentBound{
		TaskID:          p.TaskID,
		ParentID:        p.ParentID,
		Project:         p.Project,
		Title:           p.Title,
		ContextType:     p.ContextType,
		ContextHash:     p.ContextHash,
		ContextLevel:    p.ContextLevel,
		ContextData:     p.ContextData,
		Intent:          p.Intent,
		SpecJSON:        p.SpecJSON,
		Requires:        p.Requires,
		Paths:           p.Paths,
		Isolate:         p.Isolate,
		PreferredNode:   p.PreferredNode,
		TargetNode:      p.TargetNode,
		MaxRetries:      p.MaxRetries,
		Complexity:      p.Complexity,
		Risk:            p.Risk,
		AttemptID:       p.AttemptID,
		ResumeSessionID: p.ResumeSessionID,
		ResourceJSON:    p.ResourceJSON,
		PlanID:          p.PlanID,
		StageID:         p.StageID,
		Inputs:          p.Inputs,
		ProjectPack:     p.ProjectPack,
		ProjectDir:      p.ProjectDir,
		UserLocale:      p.UserLocale,
		DeadlineUnix:    p.DeadlineUnix,
		Depth:           p.Depth,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// clampForWire bounds the inline blobs a delegate can carry. An oversized
// ContextData or ProjectPack is not merely wasteful: it can push the frame past
// readLimit and get the link closed at the far end, taking the task with it.
// Dropping the inline data degrades the delegation to a fetchable context (the
// hash still travels, and handleContextFetch will serve it in chunks), which is
// strictly better than a message that never lands.
func (p TaskDelegatePayload) clampForWire() any {
	if len(p.ContextData) > MaxContextDataBytes {
		p.ContextData = nil
		if p.ContextLevel == "full" {
			p.ContextLevel = "pointer" // fetch it instead of carrying it
		}
	}
	if len(p.ProjectPack) > MaxProjectPackBytes {
		p.ProjectPack = nil
	}
	return p
}

// MaxProjectPackBytes bounds the inline project pack. Project memory is capped
// at a few thousand characters and a skill is a Markdown file, so a real pack is
// kilobytes; the cap is what keeps a project directory that has accumulated
// something unexpected (a stray binary) from being pushed through every
// delegation. Over the cap the pack is dropped and the delegation proceeds
// without it — the task still runs, with less context, which is strictly better
// than a message that cannot be sent.
const MaxProjectPackBytes = 1 << 20

// ArtifactRef names one artifact and a node known to hold it — the data-plane
// half of a stage dependency. A hash alone is not enough to fetch: content
// addressing says what the bytes must be, not who has them. Stage records which
// plan stage produced it, which is what makes the extraction order of a
// multi-input stage deterministic across nodes.
type ArtifactRef struct {
	Stage  string `json:"stage,omitempty"`
	Hash   string `json:"hash"`
	Source string `json:"source"`
	// OfTask names the producing stage's task id on the holder — the row the
	// serving node can actually resolve for authorization. The fetching
	// stage's own id cannot serve that purpose: a producer holds only its own
	// stage's row, never its siblings'.
	OfTask string `json:"of_task,omitempty"`
	// Grant is the orchestrator's Ed25519 signature over
	// (plan, consumer task, producer task, hash): capability proof that this
	// input was legitimately issued, so the producer need not know the
	// consumer's row to serve it. Issuer is the orchestrator's node id, used
	// to look up the signing key in the peer directory.
	Grant  string `json:"grant,omitempty"`
	Issuer string `json:"issuer,omitempty"`
	// Screen marks the referenced artifact as display content (the producing
	// stage drove a screen_sensing actuator). The orchestrator stamps it from
	// the producer's row so every hop downstream — routing, admission, the
	// serving verdict on a node that never saw the producer's row — can bind
	// the bytes to the mesh-only rule. Like the result flag it only narrows
	// an audience: a forged mark shrinks who may hold the input, it cannot
	// widen one.
	Screen bool `json:"screen,omitempty"`
}

// TitleOrDefault returns the explicit title, falling back to the intent.
func (p *TaskDelegatePayload) TitleOrDefault() string {
	if p.Title != "" {
		return p.Title
	}
	return p.Intent
}

// TaskAcceptPayload confirms acceptance. The Sig/Pub/Ts fields carry the
// executor's Ed25519 signature over (task, attempt, acceptor, ts) — the
// bilateral half of the contract the consent grant opens (trust zones): the
// origin's grant says WHAT may run, the executor's accept says WHO agreed to
// run it, and a relay forwarding either cannot mint the other's side. Empty
// fields mark a pre-signature peer; a peer that sends a signature that fails
// verification has its accept dropped, not degraded.
type TaskAcceptPayload struct {
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id,omitempty"`
	Sig       string `json:"sig,omitempty"`
	Pub       string `json:"pub,omitempty"`
	Ts        int64  `json:"ts,omitempty"`
}

// Decline reason codes (TaskDeclinePayload.ReasonCode) — machine-stable
// vocabulary layered over the human-readable Reason. The trust-zone codes let
// a delegator distinguish "this node cannot" from "this node's policy chose
// not to" — the first triggers re-routing as before, the second may surface a
// different fix (raise the peer's tier, or accept that open-tier strangers
// get no pushes until the offer/bid market exists).
const (
	// DeclineCodeTrustOpen — the origin's tier may not originate push-mode
	// work: open peers exist only in the directory until the market lands.
	DeclineCodeTrustOpen = "trust_open_origin"
	// DeclineCodeTrustRefused — the receiver's accept policy declined the
	// delegate (mode refuse, or an enforcing policy classifier's verdict).
	DeclineCodeTrustRefused = "trust_policy_refused"
	// DeclineCodeTrustCeiling — the sender's next-hop ceiling stripped a
	// need the task cannot run without (pack or inputs); re-routing is the
	// correct response, not retry.
	DeclineCodeTrustCeiling = "trust_send_ceiling"
)

// TaskDeclinePayload rejects with a reason.
type TaskDeclinePayload struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
	// ReasonCode is the optional machine-stable refusal vocabulary above —
	// empty for every refusal that predates the trust zones (drain, capacity,
	// chain loop), which is itself meaningful: reroute handles them alike.
	ReasonCode string `json:"reason_code,omitempty"`
	// AttemptID is the dispensation declining — a refusal from a superseded
	// attempt arriving after a re-route must not bounce the fresh copy back
	// to queued. Empty is legacy (pre-fence peers) and always processes.
	AttemptID string `json:"attempt_id,omitempty"`
}

// TaskResultPayload is the completion result.
type TaskResultPayload struct {
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
	// Chain echoes the executor's delegation chain (root → executor) so a
	// receiver that lost its local row (a restart between dispatch and
	// result) can reconstruct the real upstream path instead of guessing
	// [self, sender] — without it the relayed result stops one hop short of
	// the root. Empty for nodes that predate the field; receivers fall back
	// to the guess then.
	Chain []string `json:"chain,omitempty"`
	// State is the executor's persisted task state at the time it reports.
	// It is optional for wire compatibility with older nodes; when absent the
	// receiver derives done/failed from OK. New nodes must preserve review so a
	// supervisor that did not accept the work cannot be promoted to done by a
	// parent node.
	State string `json:"state,omitempty"`
	// ApprovalDisposition tells the delegator what approving a review means.
	// It is optional for wire compatibility; receivers classify legacy review
	// results conservatively from explicit authorization-refusal evidence only.
	ApprovalDisposition string `json:"approval_disposition,omitempty"`
	// Preserve completion evidence across the wire. Without these fields a
	// delegator could approve work the executor explicitly left unfinished.
	NeedsFollowup bool   `json:"needs_followup,omitempty"`
	Verdict       string `json:"verdict,omitempty"`
	VerdictReason string `json:"verdict_reason,omitempty"`
	Followup      string `json:"followup,omitempty"`
	// Deferred marks a result that reports custody, not execution: the
	// caller's request (a resume approval) was accepted and parked in an
	// outbox for delivery when the executor's link returns. Surfaces must
	// render it as "queued for delivery" — not as a failed exit code.
	Deferred  bool    `json:"deferred,omitempty"`
	OK        bool    `json:"ok"`
	ExitCode  int     `json:"exit_code"`
	Stdout    string  `json:"stdout,omitempty"`
	Stderr    string  `json:"stderr,omitempty"`
	Artifacts string  `json:"artifacts,omitempty"`
	Tokens    int     `json:"tokens,omitempty"`
	Cost      float64 `json:"cost,omitempty"`
	// OutputArtifact is the hash of the tree this stage produced, packed into the
	// executor's artifact pool. The node orchestrating the plan records it and
	// hands it to the successor stages as their input; the executor stays the
	// node that holds the bytes until someone pulls them.
	OutputArtifact string `json:"output_artifact,omitempty"`
	// ScreenOutput marks the output as display content (the run drove a
	// screen_sensing actuator). Every hop that adopts the result stamps its
	// own task row, so artifact_fetch can apply the mesh-only rule even for
	// operator-defined actuator ids the requires/spec predicates cannot
	// name. It can only ever make a task MORE restricted on the wire — a
	// forged flag shrinks an audience, it cannot widen one.
	ScreenOutput bool `json:"screen_output,omitempty"`
	// FilesChanged lists the workdir-relative paths the run added, removed or
	// modified — the result's footprint, so the delegator sees what the task
	// actually touched rather than trusting the agent's prose. Capped for the
	// wire (maxFilesChangedReport); absent/empty for non-agent plans and
	// executors from before the field.
	FilesChanged []string `json:"files_changed,omitempty"`
	// FilesTruncated marks a footprint diffed over a workdir too large to
	// snapshot fully (the walk stopped at the file cap, so the list covers
	// only the deterministic lexical prefix). Consumers should read it as
	// "partial evidence", not "nothing changed" — the work itself may sit
	// past the boundary.
	FilesTruncated bool `json:"files_truncated,omitempty"`
	// Question carries the clarification an agent parked on (PANDA_QUESTION):
	// the review state travels with it so the origin's task display can show
	// what the executor is asking, and a task_resume.answer replies to it.
	Question string `json:"question,omitempty"`
	// QuestionSpec is the structured form of a parked question (an
	// entry.ClarifySpec document) when the agent offered bounded options.
	// Question stays the plain-text summary for surfaces without a card.
	QuestionSpec string `json:"question_spec,omitempty"`
	// Structured attribution so the origin can consume a cross-device result
	// like an ordinary sub-agent's: who ran it (Executor — the node, stable
	// across relay hops where env.From is only the last hop), which agent
	// actually executed (Agent — the fallback chain may have swapped it),
	// and how long the execution took on the executor's clock.
	Executor string `json:"executor,omitempty"`
	// WorkDir is the workspace the executor actually ran in — the derived
	// directory, not the submitter's pin — so the origin can locate the
	// produced files on the machine that ran the work.
	WorkDir  string `json:"work_dir,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Model    string `json:"model,omitempty"`
	Injected bool   `json:"injected,omitempty"`
	// Verified and Verification carry the delivery-contract verdict. Verified
	// is true only when every selected check ran and passed; Verification is
	// the status word (verified|failed|partial|skipped) and is empty when the
	// task carried no contract. A done task without Verified is exactly what a
	// surface must not present as an unqualified success.
	Verified     bool   `json:"verified,omitempty"`
	Verification string `json:"verification,omitempty"`
	// NoRetry marks a verdict a blind re-run cannot improve: the work ran to
	// completion and failed its contract, so the orchestrator's retry budget
	// must not re-execute the same task and pay for the same result. The row
	// stays failed; the caller or a human decides what happens next.
	NoRetry        bool   `json:"no_retry,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
	// AgentSessionAdapter names the adapter file that minted
	// AgentSessionID; the delegator stores it so a later re-dispatch only
	// offers the handle back to a run that can consume it.
	AgentSessionAdapter string `json:"agent_session_adapter,omitempty"`
}

// maxWireText bounds one text field an executor fills from a child process's
// output. executil.Capture lets a command produce 8 MiB per stream while
// readLimit caps a whole frame at 4 MiB, so an unclamped result is not merely
// large: the receiver's read limit closes the connection, the result never
// lands, and a training run that took an hour is lost because its log was
// verbose. 512 KiB per field leaves the rest of the frame to the envelope and
// to JSON escaping. The full output stays on the node that produced it (and
// travels deliberately, as an artifact) — only the copy in the message shrinks.
const maxWireText = 512 << 10

// clampForWire bounds the process-output fields so the frame cannot exceed the
// transport limit. It returns a copy: the sender's own struct, which is what
// its local task row was written from, is left intact.
func (p TaskResultPayload) clampForWire() any {
	p.Stdout = clampText(p.Stdout, maxWireText)
	p.Stderr = clampText(p.Stderr, maxWireText)
	p.VerdictReason = clampText(p.VerdictReason, maxWireText)
	p.Followup = clampText(p.Followup, maxWireText)
	return p
}

// MaxContextDataBytes bounds one inline context snapshot — whether it travels
// as ContextAckPayload.Data or TaskDelegatePayload.ContextData. A snapshot is
// bounded by the packing side's context store (entry cap, not byte cap), so a
// peer that let its store grow could ship a blob whose base64 encoding pushes
// the frame past readLimit: the message would send fine and the receiver's
// read limit would close the connection instead of delivering it. The cap sits
// under what a 4 MiB frame can base64-hold so a single context message still
// lands.
const MaxContextDataBytes = 2 << 20 // 2 MiB

// clampText shortens s to at most max bytes, keeping its head and its tail. A
// long log's useful parts are its beginning (what it set out to do) and its end
// (how it turned out, the final accuracy line); the middle is the part nobody
// reads, and dropping the tail instead would throw away the answer. The cut
// lands on a rune boundary so the text stays valid UTF-8.
func clampText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const marker = "\n...[中间已截断，完整输出留在执行节点]...\n"
	keep := max - len(marker)
	if keep < 2 {
		return s[:headBoundary(s, max)]
	}
	head := headBoundary(s, keep/2)
	tail := tailBoundary(s, keep-keep/2)
	return s[:head] + marker + s[len(s)-tail:]
}

// headBoundary returns the largest n <= want with s[:n] ending on a rune
// boundary, so a truncated head stays valid UTF-8.
func headBoundary(s string, want int) int {
	for want > 0 && !utf8.RuneStart(s[want]) {
		want--
	}
	return want
}

// tailBoundary returns the largest n <= want with s[len(s)-n:] starting on a
// rune boundary — the same guarantee for the retained tail.
func tailBoundary(s string, want int) int {
	for want > 0 && !utf8.RuneStart(s[len(s)-want]) {
		want--
	}
	return want
}

// TaskProgressPayload is a liveness beat from the node executing a task to the
// node that delegated it. The executor sends one per lease-renewal tick; each
// receiver refreshes the lease on its own copy of the task and relays the beat
// one hop further up the chain, so a stage that legitimately runs for an hour
// is never mistaken for a dead executor anywhere along the delegation path.
// Note is an optional human-readable status line.
type TaskProgressPayload struct {
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id,omitempty"`
	Note      string `json:"note,omitempty"`
}

// TaskCancelPayload requests cancellation.
type TaskCancelPayload struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason,omitempty"`
	// AttemptID bounds a dispensation-scoped cancel — a lease-expiry or
	// supersede kill order — to the attempt that lapsed: a copy the
	// delegator already moved past it (a newer delegate landed first on a
	// reordered link) must not die with the dispensation it replaced.
	// Empty means tree-scoped: a human cancel/reject kills whatever the
	// row currently is.
	AttemptID string `json:"attempt_id,omitempty"`
}

// TaskResumePayload carries an approved re-run across the wire: the delegator's
// user consented to the tier-2 (irreversible) work the executor refused to run
// unauthorized, and the re-run happens on the executor — the node whose
// capability match routed the task there in the first place. AttemptID bounds
// the resume to the attempt the delegator's copy still carries, so a task that
// was transferred or re-dispatched in the meantime ignores a stale resume.
type TaskResumePayload struct {
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id,omitempty"`
	// Answer carries the user's reply to a clarification question the
	// executor parked on (Q4): a review parked on PANDA_QUESTION resumes
	// with the answer folded into the re-run's intent. Empty for the
	// classic tier-2 consent resume — older nodes ignore the field.
	Answer string `json:"answer,omitempty"`
	// Accept closes the parked copy instead of re-running it: the human on
	// one side accepted work that already ran (AcceptWork disposition), and
	// this tells the copy parked on the other side to mirror the decision
	// (review -> done). Without it, whichever side approved first left the
	// other's copy in review forever. An accept carries no consent to run
	// anything — it only ever closes a finished copy — so the receiving
	// side refuses it for any other disposition. Older nodes ignore the
	// field and stay parked; their own human can still approve locally.
	Accept bool `json:"accept,omitempty"`
}

// TaskMessagePayload is one sibling-board post on the wire. The member→owner
// leg carries To (the unresolved address the sender wrote — "stage:build",
// "parent", "siblings", or a member task id); the owner's fan-out copies
// carry Rcpt (the resolved member task id) and Fanout=true instead, so a
// replica can tell "store this slot" from "resolve and forward".
//
// Src is the node the post originates from — the posting member on the
// member→owner leg, the scope owner on fan-out copies. It lives in the
// payload for the same reason punch keeps Src there: relays re-mint the
// envelope as themselves (env.From = last hop, or the receiving conn's
// bound-peer check kills the link), so the origin identity can only survive
// inside. Receivers bind Src against what their own rows know — the owner
// expects a member-leg frame to ARRIVE along the member's dispatch path
// (env.From = the first dispatch hop), and a replica expects Src to name
// the scope owner its member row's chain root records.
//
// TTL bounds relay hops; each forwarder spends one and a spent copy drops,
// so a routed post cannot orbit the mesh between dedup evictions. A zero
// TTL is a pre-TTL peer: the first forwarder grants the standard budget.
type TaskMessagePayload struct {
	Scope       string `json:"scope"`
	Sender      string `json:"sender"`                 // sender task_id
	To          string `json:"to,omitempty"`           // unresolved address (member → owner leg)
	Rcpt        string `json:"rcpt,omitempty"`         // resolved recipient task id ("" = broadcast)
	Member      string `json:"member,omitempty"`       // member this fan-out copy is for (receiver-side check)
	Src         string `json:"src,omitempty"`          // origin node id — survives relay re-enveloping
	SenderLabel string `json:"sender_label,omitempty"` // stage id or title, owner-resolved
	Grade       string `json:"grade"`                  // status | report | artifact_ref
	Body        string `json:"body"`
	Seq         int64  `json:"seq"` // sender's monotone slot seq
	SentAt      int64  `json:"sent_at"`
	Fanout      bool   `json:"fanout,omitempty"` // owner → member copy marker
	TTL         int    `json:"ttl,omitempty"`    // remaining relay hops — one spent per forward
}

// ContextFetchPayload asks the source node for a full context snapshot.
type ContextFetchPayload struct {
	TaskID      string `json:"task_id"`
	Hash        string `json:"hash"`
	ContextType string `json:"context_type,omitempty"`
}

// ContextAckPayload is the source node's response to a context_fetch. On OK it
// carries the full snapshot blob (base64) whose SHA-256 must equal Hash; on
// failure OK is false and the executor fails the task rather than guessing.
type ContextAckPayload struct {
	TaskID string   `json:"task_id"`
	Hash   string   `json:"hash"`
	OK     bool     `json:"ok"`
	Data   []byte   `json:"data,omitempty"`
	Refs   []string `json:"refs,omitempty"`
}

// clampForWire refuses to carry a snapshot the frame cannot hold: rather than
// send a blob that would push the message past readLimit and get the connection
// closed on receipt, the ack degrades to OK=false — the executor fails the task
// with a legible reason instead of losing the link.
func (p ContextAckPayload) clampForWire() any {
	if len(p.Data) > MaxContextDataBytes {
		p.Data = nil
		p.OK = false
	}
	return p
}

// ArtifactFetchPayload asks a peer for one chunk of a task artifact, starting at
// Offset. The requester drives the transfer: it asks for the next offset only
// after the previous chunk landed, which makes a dropped or corrupt chunk a
// re-request rather than a failed transfer.
type ArtifactFetchPayload struct {
	TaskID string `json:"task_id"`
	Hash   string `json:"hash"`
	Offset int64  `json:"offset"`
	// OfTask/Grant/Issuer ride along when the fetch is a direct stage-to-stage
	// pull: OfTask is the producing task's id as named by the orchestrator's
	// input ref, Grant is the orchestrator-signed authorization, and Issuer is
	// the orchestrator's node id. All three are absent on legacy fetches,
	// which keep the participant-based authorization path.
	OfTask string `json:"of_task,omitempty"`
	Grant  string `json:"grant,omitempty"`
	Issuer string `json:"issuer,omitempty"`
}

// ArtifactChunkPayload answers an artifact_fetch. Data holds at most
// ArtifactChunkBytes of the packed archive at Offset (base64 on the wire, since
// it is a []byte). Total is the archive's full size so the requester can show
// progress and reject a stream that changes length mid-transfer, and EOF marks
// the last chunk — the point at which the accumulated bytes are hashed against
// Hash before the artifact is allowed into the pool.
//
// OK is false when the peer does not hold the artifact or refuses to serve it;
// the requester then asks a different node instead of retrying forever.
type ArtifactChunkPayload struct {
	TaskID string `json:"task_id"`
	Hash   string `json:"hash"`
	Offset int64  `json:"offset"`
	Data   []byte `json:"data,omitempty"`
	Total  int64  `json:"total,omitempty"`
	EOF    bool   `json:"eof,omitempty"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// ArtifactChunkBytes is the payload size one artifact_chunk carries. The
// transport caps a frame at readLimit (4 MiB) and []byte is base64-encoded in
// JSON (a 4/3 expansion), so 1 MiB of artifact becomes roughly 1.4 MiB on the
// wire — comfortably under the cap even with the envelope around it.
const ArtifactChunkBytes = 1 << 20

// ArtifactPushPayload is one proactively-pushed artifact chunk (§8.3
// fat-push). It mirrors the pull-side chunk shape minus the answer fields:
// Offset positions the bytes inside the archive named by Hash, and Total is
// the archive's full size so the receiver can bound and complete the staged
// copy. Chunks are sent in order on an ordered lane, but the receiver
// tolerates duplicates and gaps — its contiguous-progress status reply is
// the authoritative resume point.
type ArtifactPushPayload struct {
	TaskID string `json:"task_id"`
	Hash   string `json:"hash"`
	Offset int64  `json:"offset"`
	Data   []byte `json:"data,omitempty"`
	Total  int64  `json:"total"`
}

// ArtifactPushStatusPayload is the receiver's custody report: how many
// contiguous bytes of Hash it holds, counted from offset 0. The sender-side
// outbox retires rows only as this number covers them, which is what makes a
// send-then-crash recoverable — an unacknowledged chunk is resent on the
// next flush rather than leaving a permanent gap in the staged copy.
type ArtifactPushStatusPayload struct {
	TaskID          string `json:"task_id"`
	Hash            string `json:"hash"`
	ReceivedThrough int64  `json:"received_through"`
}

// ArtifactPushDonePayload ends one push: the staged archive verified against
// its content hash and entered the receiver's pool (OK), or staging failed
// and the sender should stop spending bandwidth on it. A receiver that
// already holds the artifact answers OK immediately, which is also how a
// sender learns it can retire rows for a peer that finished long ago.
type ArtifactPushDonePayload struct {
	TaskID string `json:"task_id"`
	Hash   string `json:"hash"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// FatBundleArtifact carries an inline artifact payload for proactive push-based DTN delivery (whitepaper §8.3).
type FatBundleArtifact struct {
	Hash string `json:"hash"`
	Data []byte `json:"data"`
}

// TargetScope defines the repository, file, and symbol scope for peer conflict negotiation (whitepaper §5.1).
type TargetScope struct {
	Repo   string `json:"repo,omitempty"`
	File   string `json:"file"`
	Symbol string `json:"symbol,omitempty"`
}

// AgentNegotiatePayload carries horizontal peer-to-peer conflict negotiation signaling (whitepaper §5.1).
type AgentNegotiatePayload struct {
	FromNode      string      `json:"from_node"`
	FromAgent     string      `json:"from_agent"`
	Timestamp     int64       `json:"timestamp"`
	Weight        int         `json:"weight"`
	TargetScope   TargetScope `json:"target_scope"`
	Intent        string      `json:"intent"`
	ActionPreview string      `json:"action_preview,omitempty"`
}

// AgentGrantPayload carries lease-based lock authorization or yield acknowledgement (whitepaper §5.2).
// Denied/Reason make an explicit negative verdict distinguishable from a
// missing reply — older peers that always grant simply leave them zero.
type AgentGrantPayload struct {
	LockID    string `json:"lock_id"`
	GrantedTo string `json:"granted_to"`
	LeaseMS   int64  `json:"lease_ms"`
	Denied    bool   `json:"denied,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// AgentYieldPayload indicates an agent has halted at an AST checkpoint and ceded execution.
type AgentYieldPayload struct {
	FromAgent string      `json:"from_agent"`
	Scope     TargetScope `json:"scope"`
	Reason    string      `json:"reason,omitempty"`
}

// DTNBundlePayload carries one CBOR-encoded DTN bundle (§8.3) verbatim. The
// receiver unmarshals, verifies signature+TTL, then feeds the inner payload
// back through the normal message path — the bundle is a signed transport
// container, not a second task protocol.
type DTNBundlePayload struct {
	Blob []byte `json:"blob"`
}

// PunchOfferPayload arranges a UDP pinhole between two NAT-bound nodes
// (farsky §9.2). It travels as an envelope through the mesh — direct conn
// when one exists, link-state next-hop relay otherwise — carrying everything
// the responder needs to start punching: the session nonce (which becomes
// the pinhole proof inside every punch datagram) and the initiator's
// candidate endpoints. Hosts mixes every address form the initiator knows:
// its local interface addresses, the reflexive IP peers have observed for it
// (HelloPayload.You), and any STUN-discovered mapping — all at its UDP
// listener port, because the punch is always sprayed FROM the shared socket
// so the mapping a NAT opens is the listener's.
//
// TTL bounds relay hops. PunchMaxTTL is the starting value; each forwarder
// decrements it and a spent offer drops, so a routed offer cannot orbit the
// mesh forever between dedup evictions.
//
// Src is the initiator's node id. It must live in the payload, not the
// envelope: a relay forwards the envelope as ITSELF (env.From = last hop,
// like dtn_bundle) because the receiving conn authenticates envelopes by
// their bound peer id — a relayed envelope that kept the origin's From
// would trip the conn's spoof check. Receivers read Src, falling back to
// env.From for a direct (unrelayed) arrival.
type PunchOfferPayload struct {
	Nonce string   `json:"nonce"`
	Src   string   `json:"src"`   // origin node id — survives relay re-enveloping
	Hosts []string `json:"hosts"` // candidate host:port UDP endpoints
	TTL   int      `json:"ttl"`
}

// PunchReadyPayload answers an offer: the responder accepts the session and
// reports its own candidates so both sides can spray at once. Nonce echoes
// the offer's — the session key both sides already hold.
type PunchReadyPayload = PunchOfferPayload

// PunchMaxTTL bounds the relays a punch coordination envelope may take. The
// mesh's own relay bound is 3; punch adds headroom because coordination is
// cheap and a failed punch costs a real reachability path.
const PunchMaxTTL = 8

// PairHelloPayload opens a Bluetooth-style pairing session on an
// unauthenticated connection. The initiator is not yet a mesh peer — this
// frame carries only ephemeral key material and self-describing display
// fields; nothing in it is trusted until the two humans compare the
// derived code on both screens.
type PairHelloPayload struct {
	Session string `json:"session"` // initiator-minted session id (uuid)
	X       string `json:"x"`       // initiator ephemeral X25519 pub, hex
	Pub     string `json:"pub"`     // initiator Ed25519 identity pub, hex (SAS-bound)
	Nonce   string `json:"nonce"`   // initiator nonce, hex — HKDF salt half
	Addr    string `json:"addr"`    // initiator's advertised listen addr (host may be 0.0.0.0)
	Name    string `json:"name"`    // configured node.name, display only
	NodeID  string `json:"node_id"` // runtime node id, display only
	// Target (P4b) asks the receiver to FORWARD this pair_hello to the named
	// stable identity instead of answering it: the receiver is being used as
	// a contact point by a joiner that cannot reach the introducer directly.
	// Absent on every pre-feature frame and on the direct (LAN) path.
	Target string `json:"target,omitempty"`
}

// PairReadyPayload answers pair_hello with the responder's ephemeral half.
// After this frame both sides hold enough material to derive the same
// short authentication string — a MITM relay produces a different one on
// each end because its two DH secrets differ.
type PairReadyPayload struct {
	Session string `json:"session"`
	X       string `json:"x"`
	Pub     string `json:"pub"`
	Nonce   string `json:"nonce"`
}

// PairSecretPayload delivers the mesh shared secret to the confirmed
// initiator. Box is base64(gcm-nonce ‖ AES-256-GCM ciphertext) keyed by
// HKDF(dh, salt=nonces, info="panda-pair/1") — the secret never crosses
// the LAN readable, and only ever flows responder→initiator.
type PairSecretPayload struct {
	Session string `json:"session"`
	Box     string `json:"box"`
}

// PairRejectPayload ends a session without joining: human said no, the
// confirm window expired, or the responder refused. Reason is a stable
// machine token ("rejected", "expired", "busy") plus optional detail.
type PairRejectPayload struct {
	Session string `json:"session"`
	Reason  string `json:"reason"`
}
