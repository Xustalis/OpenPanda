// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/trust"
)

// Trust zones are an experimental feature (docs/plans/design-trust-zones-
// agent-economy.md). A fleet that never configures a tier resolves every peer
// to mesh, every SendPolicyFor(mesh) is a no-op clamp, and every
// AcceptPolicyFor(mesh) auto-admits — the feature is absent by construction,
// not merely by flag.

// SetTrustConfig installs the operator's per-tier policy tables
// (network.trust.*). An empty config is valid and means "every tier at its
// built-in defaults".
func (c *Core) SetTrustConfig(cfg config.TrustConfig) {
	c.trustMu.Lock()
	c.trustCfg = cfg
	c.trustMu.Unlock()
}

// SetPeerTrustHints installs the configured addr→tier map (network.peers
// entries carrying a trust: field). Keys are normalized dial addresses so a
// configured "ws://host:7836/" and the recorded "host:7836" binding compare
// equal.
func (c *Core) SetPeerTrustHints(hints map[string]string) {
	norm := make(map[string]string, len(hints))
	for addr, level := range hints {
		if _, err := trust.ParseLevel(level); err != nil {
			c.logger.Warn("ignoring invalid trust hint", "peer", addr, "trust", level)
			continue
		}
		norm[NormalizeDialAddr(addr)] = level
	}
	c.trustMu.Lock()
	c.peerTrustHints = norm
	c.trustMu.Unlock()
}

// SetReputationEnabled toggles the local track-record term in routing scores
// (routing.reputation). Default off: scoring is a mainline behavior, so the
// term only enters when the operator opts in.
func (c *Core) SetReputationEnabled(on bool) {
	c.reputationOn.Store(on)
}

// ReputationEnabled reports whether routing scores include the local
// delegation-metrics term.
func (c *Core) ReputationEnabled() bool { return c.reputationOn.Load() }

// sendPolicyFor resolves the outbound ceiling the given tier runs under —
// tier defaults plus the operator's network.trust.send overrides.
func (c *Core) sendPolicyFor(l trust.Level) trust.SendPolicy {
	c.trustMu.RLock()
	defer c.trustMu.RUnlock()
	return c.trustCfg.SendPolicyFor(l)
}

// acceptPolicyFor resolves the inbound admission rule for a tier the same
// way.
func (c *Core) acceptPolicyFor(l trust.Level) trust.AcceptPolicy {
	c.trustMu.RLock()
	defer c.trustMu.RUnlock()
	return c.trustCfg.AcceptPolicyFor(l)
}

// peerTrustOf resolves a peer's effective trust level. Sources, merged
// least-trusting so no single stale assertion can upgrade an identity:
//
//  1. the tier pinned on the peer's own directory row (nodes trust / --trust)
//  2. tiers pinned on sibling rows sharing its proven Ed25519 key — trust
//     follows the identity a restarted node keeps, not the instance row
//  3. the configured addr hint, via the peer_addrs binding a signed hello
//     recorded
//
// Unset everywhere means mesh — the pre-feature default.
func (c *Core) peerTrustOf(ctx context.Context, peerID string) trust.Level {
	if c.db == nil || peerID == "" {
		return trust.Mesh
	}
	best := trust.Mesh
	merge := func(s string) {
		if l, err := trust.ParseLevel(s); err == nil {
			best = trust.Min(best, l)
		}
	}
	pub := ""
	if strings.HasPrefix(peerID, stableIDPrefix) {
		pub = peerID[len(stableIDPrefix):]
	} else {
		lvl, pk := ledger.PeerTrust(c.db, peerID)
		merge(lvl)
		pub = pk
		if pub == "" {
			// An ephemeral session id shares its host node's keypair: resolve
			// the tier through the host row, or a short-lived ask session on
			// a guarded peer would read as an unpinned stranger while the
			// node that spawned it carries the assertion.
			if base, ok := scheduler.EphemeralBase(peerID); ok {
				_, pub = ledger.PeerTrust(c.db, base)
			}
		}
	}
	if pub != "" {
		merge(ledger.PeerTrustByKey(c.db, pub))
	}
	// Configured addr hints only bind once the peer_addrs table has mapped
	// the dial address to this identity — recorded at hello, so a hint never
	// lands on an unauthenticated claim.
	c.trustMu.RLock()
	hints := c.peerTrustHints
	c.trustMu.RUnlock()
	if len(hints) == 0 {
		return best
	}
	for _, id := range c.peerIdentityIDs(ctx, peerID, pub) {
		rows, err := c.db.QueryContext(ctx, `SELECT addr FROM peer_addrs WHERE node_id = ?`, id)
		if err != nil {
			continue
		}
		for rows.Next() {
			var addr string
			if rows.Scan(&addr) == nil {
				merge(hints[NormalizeDialAddr(addr)])
			}
		}
		rows.Close()
	}
	return best
}

// peerIdentityIDs lists the directory ids an identity may be known under:
// the peer id itself plus, when its key is proven, every sibling row holding
// that key. Stable-key inputs yield the sibling set directly.
func (c *Core) peerIdentityIDs(ctx context.Context, peerID, pub string) []string {
	if pub == "" {
		return []string{peerID}
	}
	rows, err := c.db.QueryContext(ctx, `SELECT id FROM employee_cache WHERE pub_key = ?`, pub)
	if err != nil {
		return []string{peerID}
	}
	defer rows.Close()
	ids := []string{peerID}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && id != peerID {
			ids = append(ids, id)
		}
	}
	return ids
}

// clampForPeer applies the send ceiling of a NEXT-HOP peer to an outbound
// delegate. It reports whether the task may still be handed to that peer at
// all: a delegate that needs what the ceiling strips (its project pack, its
// artifact inputs) is rerouted by the caller rather than lobotomized on the
// wire. Clamping is monotone — a relay calling this re-applies its own
// next-hop ceiling over whatever upstream already removed, and nothing comes
// back.
func (c *Core) clampForPeer(nextHop string, d *bus.TaskDelegatePayload) bool {
	level := c.peerTrustOf(context.Background(), nextHop)
	pol := c.sendPolicyFor(level)
	if !pol.ExecutorOK(trust.NeedsOf(*d)) {
		return false
	}
	applied := pol.Apply(d)
	if applied.Any() {
		c.logger.Info("delegate clamped for peer trust",
			"task", d.TaskID, "peer", nextHop, "trust", level,
			"context", applied.ContextLevel, "pack", applied.ProjectPack,
			"inputs", applied.Inputs, "consent", applied.Consent)
	}
	return true
}

// admissionTier folds the authenticated sender and EVERY claimed chain hop
// into the least-trusting level. The chain is peer-written metadata, but the
// wire contract (chain must end at env.From, receivers append themselves)
// forces a dishonest origin to either name itself — where folding catches
// its real tier — or hide behind a relay's entry, which only shifts the cap
// to that relay's tier. Folding only the origin would let an open node mint
// chain[0]=a-mesh-id and have a mesh relay launder the delegate onward;
// folding the whole path matches the send side's weakest-link clamp. The
// receiver's own id — appended by AppendChain before admission runs — is
// skipped: the gate judges the path TO this node, and a self-pin must not
// lock the operator's own node out of inbound work.
func (c *Core) admissionTier(ctx context.Context, chain []string, sender string) trust.Level {
	tier := c.peerTrustOf(ctx, sender)
	for _, hop := range chain {
		if hop == c.nodeID {
			continue // the stored chain ends at this node; folding its own
			// pin would let an operator lock their own node out of inbound work
		}
		if tier == trust.Open {
			break // already at the floor — no query worth spending
		}
		tier = trust.Min(tier, c.peerTrustOf(ctx, hop))
	}
	return tier
}

// delegateAdmission is the inbound gate's verdict: admit outright, park for
// this node's operator to review, or decline with a wire reason code.
type delegateAdmission struct {
	admit  bool
	review bool
	code   string
	reason string
}

// admitDelegate applies the configured accept policy for a delegate's
// admission tier. It runs inside handleLocalDelegate before any capacity
// reservation or execution, so a refused task costs this node nothing. The
// verdict is deterministic first: the optional policy classifier only ever
// answers a productivity question and can never relax the tier's own rules —
// an unreachable or unparsable classifier parks the task rather than
// silently admitting it.
func (c *Core) admitDelegate(ctx context.Context, taskID string, p bus.TaskDelegatePayload, chain []string, sender string) delegateAdmission {
	tier := c.admissionTier(ctx, chain, sender)
	if tier == trust.Mesh {
		return delegateAdmission{admit: true}
	}
	pol := c.acceptPolicyFor(tier)
	if !pol.DelegateOrigin {
		return delegateAdmission{
			code:   bus.DeclineCodeTrustOpen,
			reason: fmt.Sprintf("origin at %s trust may not push work (offer/bid market is not built)", tier),
		}
	}
	mode := pol.Mode
	if pol.PolicyLLM != trust.LLMOff {
		v, vReason := c.policyVerdict(ctx, p)
		c.recordTrustVerdict(ctx, taskID, tier, pol.PolicyLLM, v, vReason)
		if pol.PolicyLLM == trust.LLMEnforce {
			mode = v
		}
	}
	switch mode {
	case trust.AcceptRefuse:
		return delegateAdmission{
			code:   bus.DeclineCodeTrustRefused,
			reason: fmt.Sprintf("accept policy refuses %s-origin work", tier),
		}
	case trust.AcceptReview:
		return delegateAdmission{
			review: true,
			reason: fmt.Sprintf("accept policy parks %s-origin work for operator review", tier),
		}
	default:
		return delegateAdmission{admit: true}
	}
}

// policySystemPrompt instructs the optional policy classifier: a zero-tool
// judgment of whether a delegate is work this node should want — never a
// safety question, which the deterministic tier gates still own.
const policySystemPrompt = `你是节点准入策略评审员。一个来自半信任邻接节点的委派任务刚到，判断本节点是否值得承接。

判断规则：
- 只裁生产性问题：任务是否清晰、有价值、与声明的能力相符、成本合理。
- 安全性不是你的职责：授权、文件范围、出站权限由确定性规则把守，不在你的判决内。
- 拿不准一律判 review，交给人。明显的任务滥用、空任务、或与节点能力完全无关的请求判 refuse。

只输出一个 JSON 对象，不要输出任何其他文字：
{"decision":"accept"|"review"|"refuse","reason":"一句话结论"}`

// policyVerdict runs the optional zero-tool classifier over a delegate. Its
// answer is an accept-mode string ("auto"|"review"|"refuse"); every failure
// mode — no model configured, unreachable endpoint, unparseable reply —
// resolves to review, because a policy gate that cannot decide must not
// become a silent admit.
// policyVerdictTimeout bounds the classifier call: admission runs inside the
// peer's inbound read loop, and an unbounded model call would let one
// delegate stall every frame that peer sends next. The bound is generous for
// a zero-tool judgment; on expiry the verdict resolves to review.
const policyVerdictTimeout = 15 * time.Second

func (c *Core) policyVerdict(ctx context.Context, p bus.TaskDelegatePayload) (string, string) {
	if c.supervisor == nil {
		return trust.AcceptReview, "policy classifier unavailable (no model configured)"
	}
	ctx, cancel := context.WithTimeout(ctx, policyVerdictTimeout)
	defer cancel()
	user := fmt.Sprintf("任务标题：%s\n任务意图：%s\n能力要求：%s\n复杂度：%.2f  风险：%s",
		p.TitleOrDefault(), p.Intent, strings.Join(p.Requires, ","), p.Complexity, p.Risk)
	text, err := c.supervisor.Complete(ctx, policySystemPrompt, user)
	if err != nil {
		return trust.AcceptReview, "policy classifier unreachable: " + err.Error()
	}
	mode, reason, err := parsePolicyVerdict(text)
	if err != nil {
		return trust.AcceptReview, "policy verdict unparsable: " + err.Error()
	}
	return mode, reason
}

// parsePolicyVerdict extracts {"decision","reason"} from the classifier's
// reply, tolerating code fences and surrounding prose the way the supervise
// verdict parser does. "accept" maps to the auto admission mode.
func parsePolicyVerdict(raw string) (mode, reason string, err error) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	lo, hi := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if lo < 0 || hi <= lo {
		return "", "", fmt.Errorf("no JSON object found")
	}
	var v struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(s[lo:hi+1]), &v); err != nil {
		return "", "", err
	}
	switch strings.ToLower(strings.TrimSpace(v.Decision)) {
	case "accept":
		return trust.AcceptAuto, v.Reason, nil
	case "refuse":
		return trust.AcceptRefuse, v.Reason, nil
	case "review":
		return trust.AcceptReview, v.Reason, nil
	default:
		return "", "", fmt.Errorf("unexpected decision %q", v.Decision)
	}
}

// recordTrustVerdict writes the classifier's answer to the task's audit
// trail. Under shadow mode this row is the whole feature: the mode still
// decides, but the operator can diff "what the model would have done"
// against what the rules did before enabling enforce.
func (c *Core) recordTrustVerdict(ctx context.Context, taskID string, tier trust.Level, setting, verdict, reason string) {
	if err := c.store.RecordEvent(ctx, taskID, EvTrustPolicy, map[string]any{
		"tier":    string(tier),
		"setting": setting,
		"verdict": verdict,
		"reason":  reason,
	}); err != nil {
		c.logger.Debug("trust policy event record failed", "task", taskID, "err", err)
	}
}

// eligibleForSend drops route candidates whose trust ceiling cannot hold the
// task's needs — an open peer is never a delegate target in this phase, and
// a guarded peer cannot execute work whose pack or inputs its ceiling strips.
// Clampable needs (context level, consent) never disqualify; they narrow on
// the wire instead. Self rows pass through untouched: the filter exists to
// drop outbound targets, and the routing layer reads self separately.
func (c *Core) eligibleForSend(ctx context.Context, needs trust.Needs, nodes []ledger.Node) []ledger.Node {
	out := make([]ledger.Node, 0, len(nodes))
	for _, n := range nodes {
		if scheduler.IsSelfRow(n.ID, c.nodeID) ||
			c.sendPolicyFor(c.peerTrustOf(ctx, n.ID)).ExecutorOK(needs) {
			out = append(out, n)
		}
	}
	return out
}

// reputationScores aggregates this node's OWN delegation_metrics into a
// smoothed success rate per executor — a local observation only, never a
// reputation a peer reported about itself. Laplace smoothing keeps a
// single-sample executor near the neutral 0.5; the score term only reaches a
// real edge once a few outcomes exist.
func (c *Core) reputationScores(ctx context.Context) map[string]float64 {
	if c.db == nil {
		return nil
	}
	// Track record follows the proven Ed25519 key, not the instance id that
	// earned it — a restarted node under a fresh instance id inherits its
	// key's history instead of cold-starting, matching the k: identity the
	// trust pins follow. Rows whose executor has no recorded key group under
	// the raw executor id, exactly as before.
	rows, err := c.db.QueryContext(ctx,
		`SELECT COALESCE(NULLIF(e.pub_key,''), m.executor) AS k,
		        COUNT(*), COALESCE(SUM(m.success),0)
		 FROM delegation_metrics m
		 LEFT JOIN employee_cache e ON e.id = m.executor
		 WHERE m.executor != '' AND m.executor NOT LIKE 'entry:%'
		 GROUP BY k`)
	if err != nil {
		c.logger.Debug("reputation scores query", "err", err)
		return nil
	}
	defer rows.Close()
	var out map[string]float64
	for rows.Next() {
		var executor string
		var n, ok int64
		if rows.Scan(&executor, &n, &ok) != nil || n == 0 {
			continue
		}
		if out == nil {
			out = make(map[string]float64)
		}
		out[executor] = float64(ok+1) / float64(n+2)
	}
	return out
}

// needsForTask derives a delegate's send-ceiling needs from the persisted
// row, for the paths that re-dispatch a stored task rather than an incoming
// payload (re-route after decline, queue forward). A project or file task
// cannot lose its pack/tree and still mean anything; consent and context
// level are clampable so they never need listing here.
func needsForTask(t Task) trust.Needs {
	return trust.Needs{
		Pack:   t.Project != "",
		Inputs: len(t.Inputs) > 0 || t.ContextType == "file" || t.Project != "",
	}
}

// needsForInput is the same needs derivation at submission time, where the
// row does not exist yet and the shape lives in the TaskInput.
func needsForInput(in TaskInput) trust.Needs {
	return trust.Needs{
		Pack:   in.Project != "",
		Inputs: in.Project != "" || in.ContextType == "file",
	}
}

// acceptClockSkew bounds how far an accept signature's timestamp may sit from
// our clock: a signed frame is a fresh commitment, not a replayable artifact.
const acceptClockSkew = 5 * time.Minute

// verifyTaskAccept authenticates a signed task_accept before it may move our
// copy or relay further up the chain. The signature binds (task, attempt,
// the signer's stable "k:" identity, ts) — self-authenticating at every hop,
// since the frame itself carries pub. Three rules:
//
//  1. no sig and no signing fields: a pre-feature peer — accept unchanged;
//  2. sig present but pub/timestamp/partial fields malformed: drop — a
//     half-formed signature is not a legacy frame;
//  3. sig present and well-formed but not verifying or stale: drop.
//
// There is deliberately NO "signer must equal the wire sender" binding:
// relays forward the executor's frame verbatim, so at every hop above the
// direct one the sender legitimately differs from the signer, and the two
// cases are indistinguishable without extra wire claims. They also add
// nothing: env-level checks already gate the sender to the recorded dispatch
// target, and Ed25519 + the bound (task, attempt) tuple means a peer can
// neither mint another's accept nor transplant one across work.
func (c *Core) verifyTaskAccept(ctx context.Context, from string, t Task, p bus.TaskAcceptPayload) error {
	if p.Sig == "" {
		if p.Pub != "" || p.Ts != 0 {
			return fmt.Errorf("partially signed frame (sig absent but pub/ts set)")
		}
		return nil
	}
	pub, err := hex.DecodeString(p.Pub)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("malformed signer key")
	}
	if p.Ts <= 0 || time.Since(time.Unix(p.Ts, 0)) > acceptClockSkew ||
		time.Until(time.Unix(p.Ts, 0)) > acceptClockSkew {
		return fmt.Errorf("signer timestamp outside %s skew", acceptClockSkew)
	}
	if !bus.VerifyAcceptance(ed25519.PublicKey(pub), p.TaskID, p.AttemptID,
		stableIDPrefix+p.Pub, p.Ts, p.Sig) {
		return fmt.Errorf("signature does not verify")
	}
	return nil
}

// annotateReputation stamps each node's locally observed success rate onto
// the directory row the scorer sees. Called only when routing.reputation is
// on: the annotation is what the score reads, so leaving it unset keeps the
// scoring byte-identical to before.
func (c *Core) annotateReputation(ctx context.Context, nodes []ledger.Node) {
	if !c.reputationOn.Load() {
		return
	}
	scores := c.reputationScores(ctx)
	if len(scores) == 0 {
		return
	}
	for i := range nodes {
		key := nodes[i].PubKey
		if key == "" {
			key = nodes[i].ID // unkeyed row: only its own instance history applies
		}
		if r, ok := scores[key]; ok {
			v := r
			nodes[i].Reputation = &v
		}
	}
}
