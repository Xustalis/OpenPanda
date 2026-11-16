// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"errors"
	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/util"
	"sort"
	"time"
)

// msgDedupWindow is how long a received message id is remembered. It must
// comfortably outlive the resend patterns it guards against — a peer's send
// retry, an outbox flush across a reconnect — without turning msgSeen into a
// second unbounded ledger. Ten minutes matches the hello replay window's
// order of magnitude while keeping pruning cheap.
const msgDedupWindow = 10 * time.Minute

// msgSeenMax hard-caps the dedup map: a peer free to mint arbitrary ids could
// otherwise grow it without bound. At capacity the oldest entries are evicted
// — a replay arriving after its eviction window re-executes once, which is
// the same behaviour as having no dedup, never worse.
const msgSeenMax = 8192

// msgSeenSweepInterval bounds how often claimMsgID walks the dedup map for
// expiry — far below the dedup window, so entries can only outlive their
// window by this much, and the amortized cost of a sweep is one map walk per
// interval no matter how hot the frame rate is.
const msgSeenSweepInterval = 30 * time.Second

// msgSeenKey is the dedup map key: sender + envelope id, as a struct so one
// claim costs zero allocations — a "from|id" string concat allocated once
// per inbound frame on the dispatch path.
type msgSeenKey struct {
	from string
	id   string
}

// dispatch routes an envelope to its handler. conn is the connection the
// message arrived on, needed so the hello handler can bind the authenticated
// peer identity to it.
func (c *Core) dispatch(ctx context.Context, conn *bus.Conn, env bus.Envelope) {
	// Hello frames skip the msgSeen dedup: they have their own replay
	// protection in helloSeen (keyed on signed fields, below), and claiming
	// their ids here would run before authentication — an unauthenticated
	// peer could then burn a victim's hello msg_id into msgSeen and get the
	// real hello dropped when it arrived.
	if env.Type != bus.MsgHello && !c.claimMsgID(env) {
		c.logger.Info("duplicate message dropped", "type", env.Type, "from", env.From, "msg", env.MsgID)
		return
	}
	switch env.Type {
	case bus.MsgHello:
		c.handleHello(ctx, conn, env)
	case bus.MsgHelloReject:
		c.handleHelloReject(ctx, conn, env)
	case bus.MsgPairHello:
		c.handlePairHello(ctx, conn, env)
	case bus.MsgPairReject:
		// A pair_reject arriving inbound is the initiator aborting the
		// session we host — cancel it.
		c.handlePairCancel(ctx, conn, env)
	case bus.MsgTaskDelegate:
		c.handleDelegate(ctx, env)
	case bus.MsgTaskAccept:
		c.handleAccept(ctx, env)
	case bus.MsgTaskDecline:
		c.handleDecline(ctx, env)
	case bus.MsgTaskProgress:
		c.handleProgress(ctx, env)
	case bus.MsgTaskActivityFetch:
		c.handleActivityFetch(ctx, env)
	case bus.MsgTaskActivities:
		c.handleActivities(ctx, env)
	case bus.MsgTaskResult:
		c.handleResult(ctx, env)
	case bus.MsgTaskCancel:
		c.handleCancel(ctx, env)
	case bus.MsgTaskResume:
		c.handleResume(ctx, env)
	case bus.MsgTaskMessage:
		c.handleTaskMessage(ctx, env)
	case bus.MsgContextFetch:
		c.handleContextFetch(ctx, env)
	case bus.MsgContextAck:
		c.handleContextAck(ctx, env)
	case bus.MsgArtifactFetch:
		c.handleArtifactFetch(ctx, env)
	case bus.MsgArtifactChunk:
		c.handleArtifactChunk(ctx, env)
	case bus.MsgArtifactPush:
		c.handleArtifactPush(ctx, env)
	case bus.MsgArtifactPushStatus:
		c.handleArtifactPushStatus(ctx, env)
	case bus.MsgArtifactPushDone:
		c.handleArtifactPushDone(ctx, env)
	case bus.MsgHeartbeat:
		c.handleHeartbeat(ctx, env)
	case bus.MsgAgentNegotiate:
		c.handleAgentNegotiate(ctx, env)
	case bus.MsgAgentGrant:
		c.handleAgentGrant(ctx, env)
	case bus.MsgAgentYield:
		c.handleAgentYield(ctx, env)
	case bus.MsgDTNBundle:
		c.handleDTNBundle(ctx, env)
	case bus.MsgTransit:
		c.handleTransit(ctx, conn, env)
	case bus.MsgDialCheck:
		c.handleDialCheck(ctx, env)
	case bus.MsgDialCheckAck:
		c.handleDialCheckAck(ctx, env)
	case bus.MsgDirPull:
		c.handleDirPull(ctx, env)
	case bus.MsgDirRows:
		c.handleDirRows(ctx, env)
	case bus.MsgPunchOffer, bus.MsgPunchReady:
		// Coordination envelopes are relayable: To names the node that must
		// punch, and an intermediate hop forwards rather than handling. The
		// payload TTL bounds the relays; msg-id dedup kills the residual loop.
		if env.To != "" && env.To != c.nodeID {
			c.forwardPunch(ctx, env)
			return
		}
		if env.Type == bus.MsgPunchOffer {
			c.handlePunchOffer(ctx, env)
		} else {
			c.handlePunchReady(ctx, env)
		}
	case bus.MsgPunchWant:
		// The auto-punch hint rides the same relayed-To pattern as the
		// offer it requests; the handler forwards or honors it.
		c.handlePunchWant(ctx, env)
	default:
		c.logger.Warn("unhandled message type", "type", env.Type, "from", env.From)
	}
}

// claimMsgID reports whether this envelope's message id is new and, when it
// is, records it so a later replay of the same (from, msg_id) is dropped.
// An empty id claims nothing — a peer that never fills MsgID (pre-dedup
// firmware on the fleet) keeps exactly the at-most-once semantics it always
// had, and a sender that omits ids is not penalised. The keyed-by-sender map
// is pruned on every claim and hard-capped; both walks are cheap against the
// message rate a single daemon sees.
func (c *Core) claimMsgID(env bus.Envelope) bool {
	if env.MsgID == "" {
		return true
	}
	now := time.Now()
	c.msgSeenMu.Lock()
	defer c.msgSeenMu.Unlock()
	key := msgSeenKey{from: env.From, id: env.MsgID}
	if _, dup := c.msgSeen[key]; dup {
		return false
	}
	if now.Sub(c.msgSeenSweep) > msgSeenSweepInterval {
		c.msgSeenSweep = now
		for k, t := range c.msgSeen {
			if now.Sub(t) > msgDedupWindow {
				delete(c.msgSeen, k)
			}
		}
	}
	if len(c.msgSeen) >= msgSeenMax {
		// Drop the oldest quarter, so eviction follows the same order as
		// expiry: the entries closest to aging out go first, and a replay
		// only slips through after its own dedup window would have closed
		// anyway. Evicting in arbitrary map order could drop a fresh id and
		// re-admit its replay while stale ids survived.
		type seenEntry struct {
			k msgSeenKey
			t time.Time
		}
		oldest := make([]seenEntry, 0, len(c.msgSeen))
		for k, t := range c.msgSeen {
			oldest = append(oldest, seenEntry{k, t})
		}
		sort.Slice(oldest, func(i, j int) bool { return oldest[i].t.Before(oldest[j].t) })
		for _, e := range oldest[:msgSeenMax/4] {
			delete(c.msgSeen, e.k)
		}
	}
	c.msgSeen[key] = now
	return true
}

// stateWindowCap is the sliding window of recent tree states kept per
// project/plan for the §6.2 monotonic-progress check. Five is the doc's
// window: long enough to catch A→B→A regressions, short enough that a slow
// legitimate convergence never trips it.
const stateWindowCap = 5

// stateHashMaxFiles bounds the per-round tree hash: a work dir larger than
// this skips the oscillation check rather than paying an unbounded hash walk
// every supervision round.
const stateHashMaxFiles = 20000

// stateWinKeysMax / stateWinTTL bound the oscillation window table: past the
// key bound, windows untouched for the TTL are dropped (see stateOscillates).
const (
	stateWinKeysMax = 512
	stateWinTTL     = 24 * time.Hour
)

// stateOscillates records the content hash of a task's work tree and reports
// whether the tree regressed to an earlier state — agent B undoing agent A's
// fix — which is the "logical oscillation" the check exists to kill (§6.2).
// A repeat of the *latest* state means the tree did not move at all: that is
// stagnation, which the supervision loop's own detector adjudicates, so it is
// deliberately not flagged here. key is per project or plan.
func (c *Core) stateOscillates(key, hash string) bool {
	if key == "" || hash == "" {
		return false
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.stateWin == nil {
		c.stateWin = make(map[string][]string)
		c.stateWinAt = make(map[string]time.Time)
	}
	// A long-lived daemon runs plans forever and every plan id is a fresh
	// UUID: without this prune the map keeps one window per plan ever seen.
	// The sweep only runs past the bound, so the steady state is cheap.
	if len(c.stateWinAt) > stateWinKeysMax {
		cutoff := time.Now().Add(-stateWinTTL)
		for k, at := range c.stateWinAt {
			if at.Before(cutoff) {
				delete(c.stateWinAt, k)
				delete(c.stateWin, k)
			}
		}
	}
	c.stateWinAt[key] = time.Now()
	w := c.stateWin[key]
	if n := len(w); n > 0 && w[n-1] == hash {
		return false // no movement
	}
	for _, h := range w {
		if h == hash {
			return true // regressed to a state this window already saw
		}
	}
	w = append(w, hash)
	if len(w) > stateWindowCap {
		w = w[len(w)-stateWindowCap:]
	}
	c.stateWin[key] = w
	return false
}

// sendTo sends env to peer id over whichever track is up: the WS conn first
// (ordered, frame-sized), then the datagram route a punch established. A
// datagram that cannot carry the frame fails the same way a missing peer
// does — the caller's outbox parking is the next hop for both.
func (c *Core) sendTo(id string, env bus.Envelope) error {
	_, err := c.sendToDetail(id, env)
	return err
}

// sendToDetail is sendTo with a delivery signal: relayed=true means the frame
// was handed to a transit relay whose onward delivery is NOT confirmed. Most
// callers do not care (the mesh is best-effort); the durable paths — task
// results, cancels, resumes and parked work — use the flag to keep their
// outbox row instead of treating a relay handoff as delivery.
func (c *Core) sendToDetail(id string, env bus.Envelope) (relayed bool, err error) {
	conn := c.connFor(id)
	if conn != nil {
		return false, conn.Send(env)
	}
	c.udpMu.Lock()
	u := c.udp
	addr := c.udpRoutes[id]
	c.udpMu.Unlock()
	if u != nil && addr != nil {
		return false, u.SendEnvelope(env, addr, id)
	}
	// No live track: kick the reachability ladder in the background — the
	// directory addrs and the punch rung may turn up a direct edge so the
	// NEXT send rides direct instead of relay. The kick never blocks this
	// send and never re-enters sendTo; the dedup window makes it cheap for
	// a burst of sends against the same dead peer.
	c.kickReachable(id)
	// The blind-transit fallback (M3) seals the frame for the
	// destination's identity and hands it to a relay hop before the caller
	// gives up and parks it in the outbox. sendViaTransit never re-enters
	// sendTo (it forwards on a conn directly), so this cannot recurse.
	if err := c.sendViaTransit(context.Background(), id, env); err == nil {
		return true, nil
	}
	return false, errors.New("peer not connected: " + id)
}

// sendDataFrame delivers an envelope whose payload carries a bulk []byte. On
// a direct conn whose peer advertised CapBinaryData the bytes ride a binary
// frame — the header is the same envelope with its data field cleared, the
// body the raw bytes, no base64 and no codec on either side. Everywhere else
// (old peer, UDP route, no direct conn) the full JSON form goes through the
// ordinary path, so a mixed-version mesh never loses the data. Both payload
// arguments are the same message: header with Data cleared, full with it set.
func (c *Core) sendDataFrame(to, typ string, header, full any, body []byte) error {
	msgID, err := newUUID()
	if err != nil {
		return err
	}
	if conn := c.connFor(to); conn != nil && conn.Supports(bus.CapBinaryData) {
		env, err := bus.NewEnvelope(typ, c.nodeID, msgID, header)
		if err != nil {
			return err
		}
		env.To = to
		return conn.SendData(env, body)
	}
	env, err := bus.NewEnvelope(typ, c.nodeID, msgID, full)
	if err != nil {
		return err
	}
	env.To = to
	return c.sendTo(to, env)
}

// newUUID mints a fresh message id. It returns an error rather than panicking
// so a transient entropy/time failure degrades the affected message instead of
// crashing the whole daemon (which would take the node permanently offline).
func newUUID() (string, error) {
	return util.UUIDv7()
}

// signalResult delivers an inbound task_result to every synchronous Submit
// waiter for that task. Each send is non-blocking, so a waiter that already
// gave up (ctx done) simply drops the late result and the read loop is never
// held up by a parked caller.
func (c *Core) signalResult(taskID string, p bus.TaskResultPayload) {
	c.waiters.signal(taskID, p)
}
