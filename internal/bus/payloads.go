package bus

import (
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
// CapabilitySummary type. Sig is the HMAC-SHA256 (hex) of NodeID and Ts under
// the shared secret, proving the identity was minted by a node that holds the
// secret and bounding replay to maxHelloAge (design §16 / P0-1).
type HelloPayload struct {
	NodeID string          `json:"node_id"`
	Ver    string          `json:"ver"`
	Card   json.RawMessage `json:"card,omitempty"`
	Ts     int64           `json:"ts,omitempty"` // unix seconds, bound into Sig
	Sig    string          `json:"sig"`
}

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
	TaskID          string   `json:"task_id"`
	ParentID        string   `json:"parent_id,omitempty"`
	Project         string   `json:"project,omitempty"`
	Title           string   `json:"title,omitempty"`
	ContextType     string   `json:"context_type,omitempty"`
	ContextHash     string   `json:"context_hash,omitempty"`
	ContextLevel    string   `json:"context_level,omitempty"` // pointer|summary|full
	ContextData     []byte   `json:"context_data,omitempty"`  // inline full snapshot
	Intent          string   `json:"intent"`
	SpecJSON        string   `json:"spec_json,omitempty"`
	Requires        []string `json:"requires,omitempty"`
	PreferredNode   string   `json:"preferred_node,omitempty"` // user-named node; honored when it matches
	Chain           []string `json:"chain"`
	TimeoutMS       int64    `json:"timeout_ms,omitempty"`
	MaxRetries      int      `json:"max_retries,omitempty"`
	Complexity      float64  `json:"complexity,omitempty"`
	Risk            string   `json:"risk,omitempty"`
	AttemptID       string   `json:"attempt_id,omitempty"`
	ResumeSessionID string   `json:"resume_session_id,omitempty"`
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
	Authorized bool `json:"authorized,omitempty"`
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
}

// TitleOrDefault returns the explicit title, falling back to the intent.
func (p *TaskDelegatePayload) TitleOrDefault() string {
	if p.Title != "" {
		return p.Title
	}
	return p.Intent
}

// TaskAcceptPayload confirms acceptance.
type TaskAcceptPayload struct {
	TaskID string `json:"task_id"`
}

// TaskDeclinePayload rejects with a reason.
type TaskDeclinePayload struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
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
	State     string  `json:"state,omitempty"`
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
	// Structured attribution so the origin can consume a cross-device result
	// like an ordinary sub-agent's: who ran it (Executor — the node, stable
	// across relay hops where env.From is only the last hop), which agent
	// actually executed (Agent — the fallback chain may have swapped it),
	// and how long the execution took on the executor's clock.
	Executor       string `json:"executor,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Model          string `json:"model,omitempty"`
	Injected       bool   `json:"injected,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
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
	return p
}

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

// ArtifactFetchPayload asks a peer for one chunk of a task artifact, starting at
// Offset. The requester drives the transfer: it asks for the next offset only
// after the previous chunk landed, which makes a dropped or corrupt chunk a
// re-request rather than a failed transfer.
type ArtifactFetchPayload struct {
	TaskID string `json:"task_id"`
	Hash   string `json:"hash"`
	Offset int64  `json:"offset"`
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
