package ledger

// contact.go models the contact plan of delay-tolerant routing (whitepaper
// §8.x, CCSDS SABR 734.2): a contact is a scheduled transmission window —
// "this node can send to Peer during [Start, End)" — that nodes advertise
// through the capability directory exactly like their link-state adjacency.
// Unlike a live link, a contact's availability is known in advance: routing
// reads the plan instead of probing the link, which is what lets custody
// move toward a destination that is unreachable right now but will rise over
// the horizon at a known time.

// Contact is one transmission window this node holds toward Peer.
// [Start, End) is the first window in unix seconds; Period > 0 repeats it —
// a daily ground-station pass is Start+window with Period=86400. RateBps is
// the usable bitrate during the window: the routing layer refuses a bundle
// whose transmission cannot finish before the window closes. Zero means
// unknown — size never disqualifies the contact.
type Contact struct {
	Peer    string `json:"peer"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	RateBps int64  `json:"rate_bps,omitempty"`
	Period  int64  `json:"period,omitempty"`
}

// contactMaxWindowsAhead bounds how many recurrences SendAt walks looking
// for a window that fits sizeBytes. A periodic contact that cannot carry the
// bundle for 32 windows straight is treated as unusable rather than searched
// forever.
const contactMaxWindowsAhead = 32

// Valid reports whether the window is well-formed: a peer, a non-degenerate
// first window, and a sane period.
func (c Contact) Valid() bool {
	return c.Peer != "" && c.End > c.Start && c.Period >= 0
}

// SendAt returns the earliest time >= t at which a transmission of sizeBytes
// can START on this contact such that it finishes inside the same window,
// plus the time it completes. False means no upcoming window fits.
//
// Completion is window-bounded deliberately: a bundle that would still be
// mid-flight when the link drops is not "sent", it is stranded — the same
// half-written file problem partial LTP blocks exist to avoid.
func (c Contact) SendAt(t, sizeBytes int64) (start, done int64, ok bool) {
	if !c.Valid() {
		return 0, 0, false
	}
	// Transmission time from the advertised rate; unknown rate assumes the
	// whole window is enough (rate 0 → tx 0).
	var tx int64
	if c.RateBps > 0 && sizeBytes > 0 {
		tx = sizeBytes * 8 / c.RateBps
	}
	for i := 0; i < contactMaxWindowsAhead; i++ {
		open, close, ok := c.windowAt(t)
		if !ok {
			return 0, 0, false
		}
		s := t
		if s < open {
			s = open
		}
		if s+tx <= close {
			return s, s + tx, true
		}
		// Stepping t just past this close walks to the next recurrence —
		// works identically for the one-shot case, which simply finds no
		// further window.
		t = close + 1
	}
	return 0, 0, false
}

// windowAt returns the open/close bounds of the earliest window that has not
// fully closed by t, resolving the k-th recurrence when Period > 0. False
// means every remaining window is in the past.
func (c Contact) windowAt(t int64) (open, close int64, ok bool) {
	if c.Period <= 0 {
		if t >= c.End {
			return 0, 0, false // one-shot window already closed
		}
		return c.Start, c.End, true
	}
	var k int64
	if t >= c.End {
		// Smallest k with End + k*Period > t: floor((t-End)/Period)+1.
		// Ceiling division would return a window closing exactly at t when
		// t lands on a boundary — a window at its close is already closed.
		k = (t-c.End)/c.Period + 1
	}
	return c.Start + k*c.Period, c.End + k*c.Period, true
}
