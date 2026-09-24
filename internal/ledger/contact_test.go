package ledger

import "testing"

// A one-shot window that has not opened yet: the send starts at open.
func TestContactSendAtFutureWindow(t *testing.T) {
	c := Contact{Peer: "b", Start: 1000, End: 1600, RateBps: 800}
	start, done, ok := c.SendAt(500, 100) // 100B @ 800bps = 1s
	if !ok || start != 1000 || done != 1001 {
		t.Fatalf("SendAt = (%d,%d,%v), want (1000,1001,true)", start, done, ok)
	}
}

// Inside the window the send starts now; a transfer that would run past
// close is stranded, not sent — SendAt must refuse or defer it.
func TestContactSendAtInsideWindow(t *testing.T) {
	c := Contact{Peer: "b", Start: 1000, End: 1600, RateBps: 800}
	if start, done, ok := c.SendAt(1500, 100); !ok || start != 1500 || done != 1501 {
		t.Fatalf("SendAt = (%d,%d,%v), want (1500,1501,true)", start, done, ok)
	}
	// 8000B @ 800bps = 80s: at 1520 exactly 80s remain — fits at the wire.
	if _, _, ok := c.SendAt(1520, 8000); !ok {
		t.Fatalf("SendAt(1520, 8000B) should fit: 80s tx into an 80s remainder")
	}
	if _, _, ok := c.SendAt(1521, 8000); ok {
		t.Fatalf("SendAt(1521, 8000B) must not start a tx that outlives the window")
	}
}

// A one-shot window in the past is gone; a periodic one recurs.
func TestContactSendAtRecurrence(t *testing.T) {
	one := Contact{Peer: "b", Start: 1000, End: 1600}
	if _, _, ok := one.SendAt(1700, 0); ok {
		t.Fatal("closed one-shot window must not send")
	}
	// Daily 10-minute pass: [1000,1600) + k*86400.
	c := Contact{Peer: "b", Start: 1000, End: 1600, Period: 86400}
	start, _, ok := c.SendAt(1700, 0)
	if !ok || start != 1000+86400 {
		t.Fatalf("recurring SendAt = (%d,%v), want open of next pass", start, ok)
	}
}

// Boundary: t lands exactly on a window close — that window is over, the
// answer is the NEXT recurrence, not a zero-time send at close.
func TestContactSendAtExactClose(t *testing.T) {
	c := Contact{Peer: "b", Start: 1000, End: 1600, Period: 86400}
	start, _, ok := c.SendAt(1600, 0)
	if !ok || start != 1000+86400 {
		t.Fatalf("SendAt at exact close = (%d,%v), want next window %d", start, ok, 1000+86400)
	}
}

// Unknown rate (0) means size never disqualifies; an oversized transfer on a
// rated window walks to the next recurrence.
func TestContactSendAtRate(t *testing.T) {
	slow := Contact{Peer: "b", Start: 1000, End: 1600, Period: 86400, RateBps: 8}
	// 10B @ 8bps = 10s — fits the 600s window.
	if _, _, ok := slow.SendAt(500, 10); !ok {
		t.Fatal("10B @ 8bps should fit a 600s window")
	}
	// 100kB @ 8bps = 100000s — fits NO 600s window ever; bounded search fails.
	if _, _, ok := slow.SendAt(500, 100000); ok {
		t.Fatal("transfer longer than the window must never be scheduled")
	}
	free := Contact{Peer: "b", Start: 1000, End: 1600} // rate unknown
	if _, _, ok := free.SendAt(500, 1<<30); !ok {
		t.Fatal("unknown rate must not disqualify by size")
	}
}

// Malformed contacts fail closed.
func TestContactValid(t *testing.T) {
	for i, c := range []Contact{
		{Peer: "", Start: 1, End: 2},
		{Peer: "b", Start: 2, End: 2},
		{Peer: "b", Start: 3, End: 2},
		{Peer: "b", Start: 1, End: 2, Period: -5},
	} {
		if c.Valid() {
			t.Fatalf("contact %d should be invalid: %+v", i, c)
		}
		if _, _, ok := c.SendAt(0, 0); ok {
			t.Fatalf("invalid contact %d must not send", i)
		}
	}
}
