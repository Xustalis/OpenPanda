// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/version"
)

// Epidemic directory sync (cross-network mesh design M1/§2).
//
// Every heartbeat carries dir_digest — SHA-256 over the sender's sorted
// "id:content_hash" stamp table — so two peers holding identical directories
// cost zero extra frames. A mismatch prompts a dir_pull carrying the
// requester's full stamp table; the responder diffs and answers dir_rows
// with (a) the rows the requester lacks or hashes differently and (b) a want
// list of the ids the RESPONDER lacks, so one exchange converges both sides.
//
// Rows are credentials, not views: each is signed by its owner's Ed25519 key
// (seen_at inside the signature domain, excluded from content_hash so
// freshness churn cannot defeat the digest) and forwarded byte-for-byte.
// Conflict resolution is signed > unsigned, then newer source-minted seen_at.
// Old nodes ignore the whole exchange — the field is absent, the frames are
// unknown types, and neither side notices.

const (
	// dirPullEvery rate-limits fresh dir_pulls per peer. Continuation pulls
	// (non-empty After) are exempt — they finish a sync already underway —
	// but bounded by dirPullMaxPages so a looping peer cannot pin us paging.
	dirPullEvery = 30 * time.Second
	// dirPullMaxPages caps continuation pulls per sync conversation.
	dirPullMaxPages = 8
	// dirRowsPerFrame / dirRowsMaxBytes are the response budgets (§2):
	// whichever fills first ends the page and sets the resume cursor.
	dirRowsPerFrame = 256
	dirRowsMaxBytes = 1 << 20
	// dirStampMax bounds the stamp table a pull may carry; a larger table
	// would force the responder to diff more than a directory can hold.
	dirStampMax = 1024
	// dirFutureSkew clamps source-minted seen_at into local clock sanity:
	// a row claiming to be fresher than now+skew was minted on a skewed
	// clock and is recorded at the clamp, not rejected — honest peers on
	// slow clocks would otherwise never win a conflict.
	dirFutureSkew = 5 * time.Minute
	// dirGossipFresh is how young a signed row's seen_at must be for its
	// node to read "online" in our live view. Unsigned rows never claim
	// liveness — they are forgeable by any gossip hop.
	dirGossipFresh = 90 * time.Second
	// dirRowsEvery/dirRowsBurst bound inbound row frames per sender. Each
	// frame can carry dirRowsPerFrame rows, so without this a member could
	// stream new ids at line rate.
	dirRowsEvery = 10 * time.Second
	dirRowsBurst = 8
	// dirRowsNewPerWindow bounds NEW ids one sender may introduce per
	// dirRowsEvery. The frame budget alone still lets 8×256 ids land in one
	// window, enough to fill the whole directory and block honest peers.
	dirRowsNewPerWindow = 256
	// dirRowsShareMax bounds how much of the directory ONE sender may
	// occupy. The window budget bounds the RATE at which a member can push
	// new ids in; this bounds the SHARE it ends up holding, so a member
	// cannot fill the table with its own rows and lock honest peers out of
	// it (their rows would never fit).
	dirRowsShareMax = dirRowsMax / 4
	// dirRowsMax caps the whole directory at the stamp table's own limit.
	// Beyond it peers can never converge anyway (dirStampMax trims pulls),
	// so accepting more rows only grows the DB and the heartbeat scan.
	dirRowsMax = dirStampMax
	// dirRowTTL is how long an offline, never-directly-pinned row survives
	// before the stale sweep deletes it. Pinned rows (a verified hello) are
	// never pruned — their key is a TOFU commitment.
	dirRowTTL = 30 * 24 * time.Hour
)

// dirHashEntry is a memoized content hash: raw fingerprints the exact stored
// bytes, so any rewrite (a re-mint, a newer signature) invalidates the entry.
type dirHashEntry struct {
	raw  [sha256.Size]byte
	hash string
}

// dirState holds the sync bookkeeping: pullAt/pages bound the pulls WE send;
// recvAt/recvPages bound the pulls we ANSWER — a compliant peer self-limits
// but a hostile one costs us a full-table diff plus a page of rows per
// request, so the responder enforces the same budget independently.
type dirState struct {
	mu        sync.Mutex
	pullAt    map[string]time.Time
	pages     map[string]int
	recvAt    map[string]time.Time
	recvPages map[string]int
	// rowsAt bounds inbound dir_rows frames per sender; rowsNew/rowsNewAt
	// bound the NEW ids that sender may introduce per window.
	rowsAt    map[string][]time.Time
	rowsNew   map[string]int
	rowsNewAt map[string]time.Time
	// digest/digestAt cache the folded directory digest for dirDigestTTL.
	digest   string
	digestAt time.Time
	// hashes memoizes a stored row's content hash keyed by its exact bytes.
	// The fold runs on every dir_digest (cached 2s) and every pull; without
	// this, each pass re-parses the card and re-hashes every link array.
	// Guarded by hashMu so the fold never serializes on dir.mu during SQL.
	hashMu sync.Mutex
	hashes map[string]dirHashEntry
	// sumAt/sum cache the static half of the self-row's capability summary:
	// summary() probes each agent's usability, too expensive to run every
	// heartbeat — the card changes on reload, not per beat. Live fields
	// (neighbors, links, addrs, contacts) refresh per row regardless.
	sumAt time.Time
	sum   ledger.CapabilitySummary
	sumOK bool
}

// dirSummary returns the capability summary for the self row: the probed
// static card fields from a 60s cache, with live fields (adjacency, links,
// contacts, addrs) refreshed every call.
func (c *Core) dirSummary() ledger.CapabilitySummary {
	c.dir.mu.Lock()
	if c.dir.sumOK && time.Since(c.dir.sumAt) < time.Minute {
		defer c.dir.mu.Unlock()
		return c.dir.sum
	}
	c.dir.mu.Unlock()
	// summary() probes agent usability — potentially slow I/O, so it runs
	// outside dir.mu. A stampede here only wastes one probe pass per caller;
	// last writer wins and every winner's value is equally valid.
	sum := c.summary()
	c.dir.mu.Lock()
	c.dir.sum = sum
	c.dir.sumAt = time.Now()
	c.dir.sumOK = true
	c.dir.mu.Unlock()
	return sum
}

// selfDirRow mints this node's own directory row from live state: the same
// capability summary the hello advertises, our advertised dial candidates,
// live adjacency, UDP reachability and relay profile — then seen_at=now and
// the owner's Ed25519 signature over the canonical bytes.
func (c *Core) selfDirRow(now time.Time) (*bus.DirRow, error) {
	sum := c.dirSummary()
	// Live fields re-stamp per mint — the cached summary's copy ages a
	// minute while adjacency moves per beat.
	sum.Neighbors = c.livePeerIDs()
	sum.Links = c.linkMetrics()
	sum.Contacts = c.contacts
	sum.Addrs = c.selfAddrs()
	card, err := json.Marshal(&sum)
	if err != nil {
		return nil, err
	}
	row := &bus.DirRow{
		ID:     c.nodeID,
		Ver:    version.Version,
		Card:   card,
		Addrs:  sum.Addrs,
		SeenAt: now.Unix(),
	}
	pub, priv, ok := c.nodeKeyPair()
	if !ok {
		return nil, fmt.Errorf("node key unavailable")
	}
	row.PubKey = hex.EncodeToString(pub)
	if _, xpub, xok := c.nodeXKey(); xok {
		row.XPub = hex.EncodeToString(xpub.Bytes())
	}
	if sum.Relay != nil {
		if b, err := json.Marshal(sum.Relay); err == nil {
			row.Relay = b
		}
	}
	// Neighbors/Links live at both levels: inside the card (for consumers
	// that read the summary) and as row fields (for the link-state reader
	// that only diffs row shape).
	for _, l := range sum.Links {
		row.Links = append(row.Links, bus.LinkMetric{Peer: l.Peer, RTTms: l.RTTms, Kind: l.Kind})
	}
	row.Neighbors = sum.Neighbors
	row.UDPPort = c.UDPPort()
	bus.SignDirRow(priv, row)
	return row, nil
}

// refreshSelfDirRow re-mints our row and stores its canonical bytes in the
// gossip layer, so what we serve in dir_rows is exactly what our own stamp
// table hashes. Runs once per heartbeat broadcast, immediately before the
// digest is taken — seen_at then stays inside dirGossipFresh everywhere.
func (c *Core) refreshSelfDirRow(ctx context.Context) {
	row, err := c.selfDirRow(time.Now())
	if err != nil {
		c.logger.Debug("self dir row", "err", err)
		return
	}
	canon := bus.CanonicalDirRowBytes(row)
	if canon == nil {
		return
	}
	hash := bus.DirRowContentHash(row)
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO employee_cache (id, dir_row_json, dir_sig, seen_at,
		       content_hash, status, last_seen)
		VALUES (?, ?, ?, ?, ?, 'online', ?)
		ON CONFLICT(id) DO UPDATE SET
			dir_row_json=excluded.dir_row_json, dir_sig=excluded.dir_sig,
			seen_at=excluded.seen_at, content_hash=excluded.content_hash`,
		c.nodeID, string(canon), row.Sig, row.SeenAt, hash, row.SeenAt); err != nil {
		c.logger.Debug("store self dir row", "err", err)
	}
}

// dirStamps builds the id→content_hash table the digest folds and a dir_pull
// carries. Stored canonical rows hash from their verbatim bytes; rows with
// no gossip layer yet (direct-hello observations) synthesize an unsigned row
// from live columns — the receiver re-parses either way, so the two sides'
// hashes agree by construction. Session rows never enter the table.
func (c *Core) dirStamps(ctx context.Context) (map[string]string, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT `+dirRowCols+`
		FROM employee_cache ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stamps := make(map[string]string)
	for rows.Next() {
		var s dirRowSynthesis
		if err := s.scan(rows); err != nil {
			return nil, err
		}
		if ledger.SessionRowID(s.ID) {
			continue
		}
		if s.RowJSON != "" {
			// Verbatim gossip row: hash the canonical form of the stored
			// bytes. content_hash is a cache only — recompute so a stale or
			// empty column cannot skew the digest. The in-memory memo is
			// keyed by the exact bytes, so a refresh invalidates itself and
			// the fold skips re-parsing an unchanged card.
			sum, h, ok := c.lookupRowHash(s.ID, s.RowJSON)
			if ok {
				stamps[s.ID] = h
				continue
			}
			var r bus.DirRow
			if err := json.Unmarshal([]byte(s.RowJSON), &r); err == nil {
				h := bus.DirRowContentHashHex(&r)
				c.rememberRowHash(s.ID, sum, h)
				stamps[s.ID] = h
				continue
			}
		}
		// No gossip layer: synthesize the unsigned row a peer would get if
		// it pulled this id, and hash that. Same construction on both sides
		// keeps digests comparable.
		stamps[s.ID] = bus.DirRowContentHashHex(s.synth())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Our own row is always in the table — dirRowBytes serves it fresh-
	// minted whether or not a stored copy exists, so the stamp must name it
	// too. (Mint excludes seen_at/sig from the hash, so this equals the
	// stored row's stamp whenever a refresh has run.)
	if row, err := c.selfDirRow(time.Now()); err == nil {
		stamps[c.nodeID] = bus.DirRowContentHashHex(row)
	}
	return stamps, nil
}

// lookupRowHash returns the memoized content hash for id when the stored
// bytes are unchanged, along with the fingerprint the caller passes back to
// rememberRowHash on a miss.
func (c *Core) lookupRowHash(id, raw string) (sum [sha256.Size]byte, hash string, ok bool) {
	sum = sha256.Sum256([]byte(raw))
	c.dir.hashMu.Lock()
	e, hit := c.dir.hashes[id]
	c.dir.hashMu.Unlock()
	if hit && e.raw == sum {
		return sum, e.hash, true
	}
	return sum, "", false
}

// rememberRowHash stores one memoized content hash. The map is bounded by the
// stamp table's own ceiling; exceeding it just starts a fresh generation
// (entries are a cache, never a source of truth).
func (c *Core) rememberRowHash(id string, sum [sha256.Size]byte, hash string) {
	c.dir.hashMu.Lock()
	defer c.dir.hashMu.Unlock()
	if c.dir.hashes == nil || len(c.dir.hashes) >= 2*dirStampMax {
		c.dir.hashes = make(map[string]dirHashEntry, dirStampMax)
	}
	c.dir.hashes[id] = dirHashEntry{raw: sum, hash: hash}
}

// dirRowCols is the column set a served row needs: the gossip layer
// verbatim, plus every column an unsigned synthesis reads. Order matches the
// scan in dirStamps and dirRowBytes.
const dirRowCols = `id, COALESCE(dir_row_json,''), COALESCE(pub_key,''),
	COALESCE(x_pub,''), COALESCE(ver,''), COALESCE(name,''),
	COALESCE(chip,''), COALESCE(node_kind,''), COALESCE(node_identity,''),
	COALESCE(os,''), COALESCE(device_class,''), COALESCE(scheduler_tier,0),
	COALESCE(native_json,''), COALESCE(agents_json,''),
	COALESCE(manual_json,''), COALESCE(capacity_json,''),
	COALESCE(resource_profile_json,''), COALESCE(neighbors_json,''),
	COALESCE(links_json,''), COALESCE(contacts_json,''),
	COALESCE(projects_json,''), COALESCE(addrs,''), COALESCE(relay,'')`

// dirRowSynthesis is one scanned row either served verbatim (RowJSON != "")
// or rebuilt into an unsigned row.
type dirRowSynthesis struct {
	ID, RowJSON, Pub, XPub, Ver     string
	Name, Chip, Kind, Identity      string
	OS, Form                        string
	Tier                            int
	Native, Agents, Manual, Cap     string
	Res, Neighbors, Links, Contacts string
	Projects, Addrs, Relay          string
}

// scanDirRow reads one row of dirRowCols.
func (s *dirRowSynthesis) scan(rows interface {
	Scan(...any) error
}) error {
	return rows.Scan(&s.ID, &s.RowJSON, &s.Pub, &s.XPub, &s.Ver, &s.Name,
		&s.Chip, &s.Kind, &s.Identity, &s.OS, &s.Form, &s.Tier,
		&s.Native, &s.Agents, &s.Manual, &s.Cap, &s.Res, &s.Neighbors,
		&s.Links, &s.Contacts, &s.Projects, &s.Addrs, &s.Relay)
}

// synthDirRow rebuilds the unsigned row a direct-hello-learned node serves:
// the card is re-exploded from the JSON columns into a CapabilitySummary —
// lossy only where UpsertRemote itself dropped fields (resource_class,
// actuator split), so the gossiped abilities match what routing already sees
// in this node's live view.
func (s *dirRowSynthesis) synth() *bus.DirRow {
	r := &bus.DirRow{ID: s.ID, PubKey: s.Pub, XPub: s.XPub, Ver: s.Ver}
	if s.Addrs != "" {
		_ = json.Unmarshal([]byte(s.Addrs), &r.Addrs)
	}
	if s.Neighbors != "" {
		_ = json.Unmarshal([]byte(s.Neighbors), &r.Neighbors)
	}
	if s.Links != "" {
		_ = json.Unmarshal([]byte(s.Links), &r.Links)
	}
	if s.Relay != "" {
		r.Relay = json.RawMessage(s.Relay)
	}
	var sum ledger.CapabilitySummary
	sum.Device = s.Name
	sum.Chip = s.Chip
	sum.NodeKind = s.Kind
	sum.NodeIdentity = s.Identity
	sum.OS = s.OS
	sum.FormFactor = s.Form
	sum.SchedulerTier = s.Tier
	sum.Neighbors = r.Neighbors
	sum.Addrs = r.Addrs
	var native []ledger.NativeAbility
	if s.Native != "" {
		_ = json.Unmarshal([]byte(s.Native), &native)
	}
	for _, n := range native {
		sum.NativeIDs = append(sum.NativeIDs, n.ID)
	}
	if s.Agents != "" {
		var agents map[string]ledger.Agent
		if json.Unmarshal([]byte(s.Agents), &agents) == nil && len(agents) > 0 {
			sum.AgentCaps = make(map[string][]string, len(agents))
			for name, ag := range agents {
				sum.AgentCaps[name] = ag.Capabilities
			}
		}
	}
	if s.Manual != "" {
		var manual []ledger.ManualAbility
		if json.Unmarshal([]byte(s.Manual), &manual) == nil {
			for _, m := range manual {
				sum.ManualIDs = append(sum.ManualIDs, m.ID)
			}
		}
	}
	if s.Cap != "" {
		_ = json.Unmarshal([]byte(s.Cap), &sum.Capacity)
	}
	if s.Res != "" {
		_ = json.Unmarshal([]byte(s.Res), &sum.ResourceProfile)
	}
	if s.Links != "" {
		_ = json.Unmarshal([]byte(s.Links), &sum.Links)
	}
	if s.Contacts != "" {
		_ = json.Unmarshal([]byte(s.Contacts), &sum.Contacts)
	}
	if s.Projects != "" {
		_ = json.Unmarshal([]byte(s.Projects), &sum.Projects)
	}
	if s.Relay != "" {
		var rp ledger.RelayProfile
		if json.Unmarshal([]byte(s.Relay), &rp) == nil {
			sum.Relay = &rp
		}
	}
	if card, err := json.Marshal(&sum); err == nil {
		r.Card = card
	}
	return r
}

// dirDigestTTL is how long a folded directory digest is reused. The fold
// reads every row of the directory and hashes it, and an inbound heartbeat
// arrives per peer per beat — an uncached fold is a CPU amplifier a single
// member can aim at the node.
const dirDigestTTL = 2 * time.Second

// dirDigest folds the stamp table into the single heartbeat field.
func (c *Core) dirDigest(ctx context.Context) string {
	c.dir.mu.Lock()
	if c.dir.digest != "" && time.Since(c.dir.digestAt) < dirDigestTTL {
		d := c.dir.digest
		c.dir.mu.Unlock()
		return d
	}
	c.dir.mu.Unlock()
	stamps, err := c.dirStamps(ctx)
	if err != nil {
		return ""
	}
	d := bus.DirDigestFromStamps(stamps)
	c.dir.mu.Lock()
	c.dir.digest = d
	c.dir.digestAt = time.Now()
	c.dir.mu.Unlock()
	return d
}

// dirPullAllowed enforces the 30s fresh-pull rate limit and the continuation
// page cap on pulls WE send. A pull with After=="" starts a new conversation;
// one with After continues it within the page budget.
func (c *Core) dirPullAllowed(peer string, after string) bool {
	c.dir.mu.Lock()
	defer c.dir.mu.Unlock()
	if c.dir.pullAt == nil {
		c.dir.pullAt = make(map[string]time.Time)
		c.dir.pages = make(map[string]int)
	}
	if after == "" {
		if last, ok := c.dir.pullAt[peer]; ok && time.Since(last) < dirPullEvery {
			return false
		}
		c.dir.pullAt[peer] = time.Now()
		c.dir.pages[peer] = 0
		return true
	}
	if c.dir.pages[peer] >= dirPullMaxPages {
		return false
	}
	c.dir.pages[peer]++
	return true
}

// dirAnswerAllowed is the responder-side mirror of dirPullAllowed: a fresh
// pull from a peer is answered at most once per dirPullEvery, continuations
// at most dirPullMaxPages deep. It costs a hostile peer nothing to send a
// pull, but it costs us a diff plus a page of rows — so the budget is
// enforced here too, not just self-imposed at the sender.
func (c *Core) dirAnswerAllowed(peer string, after string) bool {
	c.dir.mu.Lock()
	defer c.dir.mu.Unlock()
	if c.dir.recvAt == nil {
		c.dir.recvAt = make(map[string]time.Time)
		c.dir.recvPages = make(map[string]int)
	}
	if after == "" {
		if last, ok := c.dir.recvAt[peer]; ok && time.Since(last) < dirPullEvery {
			return false
		}
		c.dir.recvAt[peer] = time.Now()
		c.dir.recvPages[peer] = 0
		return true
	}
	if c.dir.recvPages[peer] >= dirPullMaxPages {
		return false
	}
	c.dir.recvPages[peer]++
	return true
}

// dirRowsAllowed bounds inbound dir_rows frames per sender. A pull is already
// rate-limited; rows can also arrive unsolicited (the requester's want reply
// and a push), so the receiving side needs its own budget.
func (c *Core) dirRowsAllowed(peer string) bool {
	if peer == "" {
		return false
	}
	c.dir.mu.Lock()
	defer c.dir.mu.Unlock()
	if c.dir.rowsAt == nil {
		c.dir.rowsAt = make(map[string][]time.Time)
	}
	cutoff := time.Now().Add(-dirRowsEvery)
	hits := c.dir.rowsAt[peer]
	kept := hits[:0]
	for _, at := range hits {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= dirRowsBurst {
		c.dir.rowsAt[peer] = kept
		return false
	}
	c.dir.rowsAt[peer] = append(kept, time.Now())
	return true
}

// dirNewRowsReserve takes up to n new-id slots out of peer's current window
// budget and returns how many it got. The reservation happens under the lock:
// checking the budget and charging it later would let two frames from the
// same sender (one over a direct conn, one over a transit path) each take
// the full budget.
func (c *Core) dirNewRowsReserve(peer string, n int) int {
	c.dir.mu.Lock()
	defer c.dir.mu.Unlock()
	if c.dir.rowsNew == nil {
		c.dir.rowsNew = make(map[string]int)
		c.dir.rowsNewAt = make(map[string]time.Time)
	}
	now := time.Now()
	if now.Sub(c.dir.rowsNewAt[peer]) >= dirRowsEvery {
		c.dir.rowsNew[peer] = 0
		c.dir.rowsNewAt[peer] = now
	}
	left := dirRowsNewPerWindow - c.dir.rowsNew[peer]
	if left < 0 {
		left = 0
	}
	if n > left {
		n = left
	}
	c.dir.rowsNew[peer] += n
	return n
}

// dirNewRowsRefund hands reserved-but-unused slots back to the sender's
// window budget.
func (c *Core) dirNewRowsRefund(peer string, n int) {
	if n <= 0 {
		return
	}
	c.dir.mu.Lock()
	defer c.dir.mu.Unlock()
	if c.dir.rowsNew == nil {
		return
	}
	c.dir.rowsNew[peer] -= n
	if c.dir.rowsNew[peer] < 0 {
		c.dir.rowsNew[peer] = 0
	}
}

// dirRowBudget returns the directory's remaining room for NEW ids and the
// sender's current share from one aggregate scan.
//
// Share attribution is by AUTHOR, not by delivery hop: learned_via names the
// peer that handed us a row, so charging every relayed row to the relay would
// let a star topology pin its only relay at the share cap, and a member that
// reconnects under a fresh session id would get a fresh allowance. Signed
// rows therefore count against the key that signed them. Unsigned rows have
// no author, so they count against every instance of the sender's proven key
// (learned_via resolved through pub_key), which is what makes a session-id
// rotation not reset the count. A sender with no pinned key falls back to its
// literal id.
func (c *Core) dirRowBudget(ctx context.Context, peer string) (room, share int) {
	if c.db == nil || peer == "" {
		return 0, 0
	}
	var pub string
	if err := c.db.QueryRowContext(ctx,
		`SELECT COALESCE(pub_key,'') FROM employee_cache WHERE id = ?`, peer).Scan(&pub); err != nil {
		pub = ""
	}
	var total, mine int
	var err error
	if pub != "" {
		// A row counts against the sender that SIGNED it (dir_signer) and
		// against the sender it was learned from (learned_via). dir_signer is
		// what makes the share cap bite: without it a member that signed rows
		// for fresh ids contributed nothing to its own share and could fill
		// the table until no honest peer had room to publish.
		err = c.db.QueryRowContext(ctx, `
			SELECT COUNT(1),
			       COALESCE(SUM(CASE
			         WHEN dir_signer = ? THEN 1
			         WHEN dir_signer = '' AND dir_sig = '' AND learned_via IN
			              (SELECT id FROM employee_cache WHERE pub_key = ?) THEN 1
			         ELSE 0 END), 0)
			FROM employee_cache`, pub, pub).Scan(&total, &mine)
	} else {
		err = c.db.QueryRowContext(ctx, `
			SELECT COUNT(1),
			       COALESCE(SUM(CASE WHEN dir_sig = '' AND learned_via = ? THEN 1 ELSE 0 END), 0)
			FROM employee_cache`, peer).Scan(&total, &mine)
	}
	if err != nil {
		// Fail closed: an unreadable directory is not room to grow. The share
		// alone would only close the door on honest rows, but the same scan
		// carries both numbers, so room=0 is the safe answer.
		return 0, 0
	}
	if total >= dirRowsMax {
		return 0, mine
	}
	return dirRowsMax - total, mine
}

// sendDirPull emits a differential-sync request toward peer. Stamps are the
// requester's whole table — bounded by the directory cap, not an extra gate.
func (c *Core) sendDirPull(peer, after string) {
	if !c.dirPullAllowed(peer, after) {
		return
	}
	stamps, err := c.dirStamps(context.Background())
	if err != nil {
		return
	}
	if len(stamps) > dirStampMax {
		stamps = firstStamps(stamps, dirStampMax)
	}
	msgID, err := newUUID()
	if err != nil {
		return
	}
	env, err := bus.NewEnvelope(bus.MsgDirPull, c.nodeID, msgID, bus.DirPullPayload{Stamps: stamps, After: after})
	if err != nil {
		return
	}
	env.To = peer
	c.mesh.dirPulls.Add(1)
	if err := c.sendTo(peer, env); err != nil {
		c.logger.Debug("dir_pull send", "peer", peer, "err", err)
	}
}

// firstStamps trims a stamp map to the n lowest ids — deterministic so both
// sides trim identically and a capped pull still converges what it covers.
func firstStamps(stamps map[string]string, n int) map[string]string {
	ids := make([]string, 0, len(stamps))
	for id := range stamps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make(map[string]string, n)
	for i, id := range ids {
		if i >= n {
			break
		}
		out[id] = stamps[id]
	}
	return out
}

// maybeDirPull compares a heartbeat's dir_digest against our own table and
// kicks a pull on divergence. Absent digests (old peers) skip silently.
func (c *Core) maybeDirPull(ctx context.Context, peer, theirDigest string) {
	if theirDigest == "" || c.db == nil {
		return
	}
	if theirDigest == c.dirDigest(ctx) {
		return
	}
	c.sendDirPull(peer, "")
}

// handleDirPull diffs the requester's stamps against our table and answers
// dir_rows: the rows THEY lack or hash differently (bounded and paged), plus
// the ids WE lack or disagree on (want) — which the requester pushes back in
// one reply, completing a full bidirectional exchange per pull.
func (c *Core) handleDirPull(ctx context.Context, env bus.Envelope) {
	var p bus.DirPullPayload
	if err := env.PayloadInto(&p); err != nil || len(p.Stamps) > dirStampMax {
		return
	}
	if !c.dirAnswerAllowed(env.From, p.After) {
		return
	}
	mine, err := c.dirStamps(ctx)
	if err != nil {
		return
	}
	// Their misses/diffs → we serve. Id order is the page order; the cursor
	// resumes strictly after the last id served.
	var need []string
	for id, h := range mine {
		if got, ok := p.Stamps[id]; !ok || got != h {
			need = append(need, id)
		}
	}
	sort.Strings(need)
	// Their extras/diffs → we want back.
	var want []string
	for id, h := range p.Stamps {
		if got, ok := mine[id]; !ok || got != h {
			want = append(want, id)
		}
	}
	// Sort before the page cut: the stamp table is a map, so an unsorted
	// want list would hand every round a DIFFERENT random subset of the
	// missing ids — the two sides would then chase each other without ever
	// covering the same ids.
	sort.Strings(want)
	rows, cursor, more := c.dirRowsFor(ctx, need, p.After)
	if len(want) > dirRowsPerFrame {
		want = want[:dirRowsPerFrame]
	}
	c.sendDirRows(env.From, rows, want, cursor, more)
}

// dirRowsFor materializes wire bytes for the wanted ids past `after`,
// respecting both budgets; returns the resume cursor and the overflow flag.
func (c *Core) dirRowsFor(ctx context.Context, ids []string, after string) (rows []json.RawMessage, cursor string, more bool) {
	size := 0
	for _, id := range ids {
		if id <= after {
			continue
		}
		raw, ok := c.dirRowBytes(ctx, id)
		if !ok {
			continue
		}
		if len(rows) >= dirRowsPerFrame || size+len(raw) > dirRowsMaxBytes {
			more = true
			break
		}
		rows = append(rows, raw)
		size += len(raw)
		cursor = id
	}
	return rows, cursor, more
}

// dirRowBytes returns the canonical wire bytes for one directory row: the
// stored gossip-layer row verbatim when present (its signature survives
// byte-for-byte), else the unsigned synthesis from live columns.
func (c *Core) dirRowBytes(ctx context.Context, id string) (json.RawMessage, bool) {
	if ledger.SessionRowID(id) {
		return nil, false
	}
	// Our own row is always minted fresh on serve — seen_at stays inside
	// the signature domain, so only a just-signed row carries honest
	// freshness; the stored copy is the digest's, not the payload's.
	if id == c.nodeID {
		if row, err := c.selfDirRow(time.Now()); err == nil {
			if canon := bus.CanonicalDirRowBytes(row); canon != nil {
				return canon, true
			}
		}
	}
	var s dirRowSynthesis
	err := c.db.QueryRowContext(ctx, `
		SELECT `+dirRowCols+`
		FROM employee_cache WHERE id=?`, id).Scan(
		&s.ID, &s.RowJSON, &s.Pub, &s.XPub, &s.Ver, &s.Name, &s.Chip,
		&s.Kind, &s.Identity, &s.OS, &s.Form, &s.Tier, &s.Native, &s.Agents,
		&s.Manual, &s.Cap, &s.Res, &s.Neighbors, &s.Links, &s.Contacts,
		&s.Projects, &s.Addrs, &s.Relay)
	if err != nil {
		return nil, false
	}
	if s.RowJSON != "" {
		return json.RawMessage(s.RowJSON), true
	}
	canon := bus.CanonicalDirRowBytes(s.synth())
	if canon == nil {
		return nil, false
	}
	return canon, true
}

// sendDirRows emits a rows/want reply toward peer.
func (c *Core) sendDirRows(peer string, rows []json.RawMessage, want []string, cursor string, more bool) {
	if len(rows) == 0 && len(want) == 0 {
		return
	}
	msgID, err := newUUID()
	if err != nil {
		return
	}
	env, err := bus.NewEnvelope(bus.MsgDirRows, c.nodeID, msgID, bus.DirRowsPayload{
		Rows: rows, Want: want, Cursor: cursor, More: more,
	})
	if err != nil {
		return
	}
	env.To = peer
	if err := c.sendTo(peer, env); err != nil {
		c.logger.Debug("dir_rows send", "peer", peer, "err", err)
		return
	}
	c.mesh.dirRowsOut.Add(int64(len(rows)))
}

// handleDirRows applies each row under the conflict rules, then answers the
// sender's want list with one page of our rows (no want of our own — the
// exchange completes in one round).
func (c *Core) handleDirRows(ctx context.Context, env bus.Envelope) {
	var p bus.DirRowsPayload
	if err := env.PayloadInto(&p); err != nil {
		return
	}
	if len(p.Rows) > dirRowsPerFrame || len(p.Want) > dirRowsPerFrame {
		return
	}
	if !c.dirRowsAllowed(env.From) {
		c.logger.Debug("dir_rows: rate limited", "from", env.From)
		return
	}
	applied := 0
	// Reserve the sender's new-id budget under the lock, then hand back
	// whatever the frame did not use.
	reserved := c.dirNewRowsReserve(env.From, dirRowsNewPerWindow)
	room, share := c.dirRowBudget(ctx, env.From)
	if reserved < room {
		room = reserved
	}
	// The sender's SHARE of the directory: it may not fill the table with
	// rows no honest peer could ever add.
	switch {
	case share >= dirRowsShareMax:
		room = 0
	case dirRowsShareMax-share < room:
		room = dirRowsShareMax - share
	}
	startRoom := room
	for _, raw := range p.Rows {
		if c.applyDirRowLimited(ctx, raw, env.From, &room) {
			applied++
		}
	}
	c.dirNewRowsRefund(env.From, reserved-(startRoom-room))
	if applied > 0 {
		c.mesh.dirRowsIn.Add(int64(applied))
		c.logger.Debug("dir rows applied", "from", env.From, "n", applied)
	}
	// The want reply: our rows for the ids they lack. One page, no cursor —
	// if the want list exceeded a page the next digest mismatch re-syncs.
	if len(p.Want) > 0 {
		rows, _, _ := c.dirRowsFor(ctx, p.Want, "")
		c.sendDirRows(env.From, rows, nil, "", false)
	}
	// Overflow page: resume the pull at the cursor.
	if p.More && p.Cursor != "" {
		c.sendDirPull(env.From, p.Cursor)
	}
}

// applyDirRow validates and folds one gossiped row under M1's conflict rules:
// signed > unsigned, then newer source-minted seen_at (future clamped to
// ±dirFutureSkew). A signature that fails verification is corruption, not
// "unsigned" — the row drops rather than downgrades. Identity columns
// (pub_key/x_pub) write only under a verified signature and never contradict
// a pinned key. Returns true when the row landed.
func (c *Core) applyDirRow(ctx context.Context, raw json.RawMessage, via string) bool {
	return c.applyDirRowLimited(ctx, raw, via, nil)
}

// applyDirRowLimited is applyDirRow with an optional new-row budget. room is
// shared across one inbound frame so a 256-row page cannot push the directory
// past dirRowsMax; nil means the caller is a trusted local path (tests, seed).
func (c *Core) applyDirRowLimited(ctx context.Context, raw json.RawMessage, via string, room *int) bool {
	row, err := bus.ParseDirRow(raw)
	if err != nil {
		return false
	}
	if row.ID == c.nodeID || ledger.SessionRowID(row.ID) {
		return false
	}
	signed := row.Sig != ""
	if signed && !bus.VerifyDirRow(row) {
		c.logger.Debug("dir row sig invalid", "id", row.ID, "via", via)
		return false
	}
	// A stable id is self-certifying: k:<pub> must be signed by <pub>.
	// Otherwise a member could sign a row naming another node's stable id
	// with its own key and have the identity write below pin it.
	if signed && strings.HasPrefix(row.ID, stableIDPrefix) &&
		!strings.EqualFold(row.ID, stableIDPrefix+row.PubKey) {
		c.logger.Debug("dir row stable-id/key mismatch", "id", row.ID, "via", via)
		return false
	}
	now := time.Now().Unix()
	seenAt := row.SeenAt
	if seenAt > now+int64(dirFutureSkew.Seconds()) {
		seenAt = now + int64(dirFutureSkew.Seconds())
	}
	// Load the incumbent's gossip-layer facts for the conflict compare.
	var incSig, incPub string
	var incSeen int64
	var found bool
	err = c.db.QueryRowContext(ctx, `
		SELECT dir_sig, COALESCE(seen_at,0), COALESCE(pub_key,'')
		FROM employee_cache WHERE id=?`, row.ID).
		Scan(&incSig, &incSeen, &incPub)
	switch {
	case err == nil:
		found = true
	case err == sql.ErrNoRows:
		found = false
	default:
		return false
	}
	if found {
		if !signed {
			// An unsigned row may CREATE a row but never displace one. The
			// incumbent's card, addrs and relay are what the live view trusts;
			// an unsigned challenger carries no proof of anything, and letting
			// it win would let any member rewrite a peer's identity in the
			// directory (and knock it out of the dial path with an empty
			// dir_sig). A signed challenger still wins — see the rules below.
			return false
		}
		if (incSig == "") == (row.Sig == "") && incSeen >= seenAt {
			return false // same signedness, incumbent is newer or equal
		}
		// Pinned identity never contradicts: a signed row naming a DIFFERENT
		// pub than the one a verified hello bound is a rekey claim gossip
		// cannot adjudicate — drop it and let a direct hello rekey properly.
		if incPub != "" && row.PubKey != "" && row.PubKey != incPub {
			return false
		}
	}
	// A row that arrives already past dirRowTTL is born dead: PruneOffline
	// deletes unpinned offline rows whose last_seen and seen_at are older than
	// that TTL, and the peer that gossiped the row re-sends the same bytes on
	// its next pull. The directory then oscillates between "the fleet" and
	// "the fleet plus the same dead cards" on every sync window — the console
	// paints them, the sweep deletes them, and the loop never ends because a
	// missing row is exactly what a pull is allowed to create. Refuse the row
	// at the door instead of storing it for the sweeper to delete again.
	// Stable ids are exempt: they self-certify, so the frame itself pins the
	// key and the sweep leaves the row alone.
	if !found && !strings.HasPrefix(row.ID, stableIDPrefix) && seenAt < now-int64(dirRowTTL.Seconds()) {
		c.logger.Debug("dir row expired on arrival", "id", row.ID, "via", via, "seen_at", seenAt)
		return false
	}
	// A new id consumes one slot from this frame's budget. Existing rows
	// (card/addrs refreshes) are free — the cap bounds the table, not churn.
	if !found && room != nil {
		if *room <= 0 {
			c.logger.Debug("dir row cap reached", "id", row.ID, "via", via)
			return false
		}
		*room--
	}
	// Identity writes are signature-gated: the row's Ed25519 proof binds
	// pub and x_pub as one claim, recorded through the same atomic upsert a
	// verified hello uses (with its key-change verification reset).
	//
	// A gossip row may REFRESH an existing pin, or establish one for a
	// self-certifying k:<pub> id, but it may never mint the first pin for a
	// non-stable id: the signature proves "the holder of this key claims
	// this id", not that the id belongs to that key. Without this rule any
	// member could bind another node's id to its own key before a verified
	// hello arrives, and transit's From check would then trust forged
	// frames as that node.
	mayPin := strings.HasPrefix(row.ID, stableIDPrefix) || (found && incPub != "")
	if signed && row.PubKey != "" && mayPin {
		c.recordPeerPubKey(ctx, row.ID, row.PubKey, row.XPub)
	}
	// Store the canonical row into the gossip layer so re-serving keeps the
	// signature verifiable byte-for-byte.
	canon := bus.CanonicalDirRowBytes(row)
	if canon == nil {
		return false
	}
	hash := bus.DirRowContentHash(row)
	// dir_signer records who SIGNED the row. The signature covers it, so it
	// is proof rather than a claim — and it is what the share accounting
	// counts: pub_key only names the key an id is PINNED to, which a
	// non-stable id never gets, so a sender's phantom rows used to count
	// against nobody and its share stayed at zero while it filled the table.
	signer := ""
	if signed {
		signer = row.PubKey
	}
	// Upsert: recordPeerPubKey may have created the row a moment ago, and a
	// hello-learned row may already exist — never a plain insert.
	//
	// learned_via records the FIRST hop that brought us this id. A row may
	// already exist when the upsert runs (recordPeerPubKey creates a
	// provenance-less skeleton for a self-certifying id), so fill the column
	// only while it is empty; letting every refresh overwrite it would move
	// the row's provenance to whichever relay forwarded the newest copy, and
	// the share accounting keys unsigned rows on it.
	_, err = c.db.ExecContext(ctx, `
		INSERT INTO employee_cache (id, dir_row_json, dir_sig, dir_signer, seen_at,
		       content_hash, learned_via, status, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'offline', 0)
		ON CONFLICT(id) DO UPDATE SET
			dir_row_json=excluded.dir_row_json, dir_sig=excluded.dir_sig,
			dir_signer=excluded.dir_signer,
			seen_at=excluded.seen_at, content_hash=excluded.content_hash,
			learned_via=CASE WHEN employee_cache.learned_via = ''
			                 THEN excluded.learned_via
			                 ELSE employee_cache.learned_via END`,
		row.ID, string(canon), row.Sig, signer, seenAt, hash, via)
	if err != nil {
		return false
	}
	// Live-column application: while we hold a conn to the node our direct
	// observation is fresher than anything gossip carries — the gossip layer
	// above still updated for forwarding, but columns stay ours.
	if c.connFor(row.ID) != nil {
		return true
	}
	c.applyDirRowColumns(ctx, row, seenAt, signed, now)
	return true
}

// applyDirRowColumns writes a winning row into the live view: capability card
// (sanitized through the same path a hello card takes), reachability fields
// (x_pub, addrs, relay, adjacency, udp port) and liveness — a signed row
// fresh enough asserts online; unsigned rows never do. seen_at becomes the
// row's last_seen so the stale sweep decays it on the usual clock.
func (c *Core) applyDirRowColumns(ctx context.Context, row *bus.DirRow, seenAt int64, signed bool, now int64) {
	if len(row.Card) > 0 {
		var sum ledger.CapabilitySummary
		if err := json.Unmarshal(row.Card, &sum); err == nil {
			if err := ledger.UpsertGossiped(c.db, row.ID, sanitizeSummary(sum), seenAt, signed && now-seenAt < int64(dirGossipFresh.Seconds())); err != nil {
				c.logger.Debug("gossip card apply", "id", row.ID, "err", err)
			}
		}
	} else {
		// No card → insert the skeleton row so id/ver/keys resolve; status
		// follows the same signed-freshness rule.
		status := "offline"
		if signed && now-seenAt < int64(dirGossipFresh.Seconds()) {
			status = "online"
		}
		_, _ = c.db.ExecContext(ctx, `
			INSERT INTO employee_cache (id, status, last_seen) VALUES (?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				status = CASE WHEN excluded.status='online' THEN 'online' ELSE employee_cache.status END,
				last_seen = CASE WHEN employee_cache.last_seen > excluded.last_seen THEN employee_cache.last_seen ELSE excluded.last_seen END`,
			row.ID, status, seenAt)
	}
	if signed && row.UDPPort > 0 {
		c.learnPeerUDP(row.ID, row.UDPPort, nil, "")
	}
	if row.Ver != "" {
		_ = ledger.SetNodeVerIfChanged(c.db, row.ID, row.Ver)
	}
	if len(row.Addrs) > 0 {
		if b, err := json.Marshal(row.Addrs); err == nil {
			_ = ledger.UpdateAddrsIfChanged(c.db, row.ID, string(b))
		}
	}
	if len(row.Relay) > 0 {
		_, _ = c.db.ExecContext(ctx, `UPDATE employee_cache SET relay=? WHERE id=?`, string(row.Relay), row.ID)
	}
}
