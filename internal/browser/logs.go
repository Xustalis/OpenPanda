// SPDX-License-Identifier: AGPL-3.0-or-later

package browser

import (
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
)

// consoleLog is a bounded, ordered buffer of what the page said. It grows at
// the head and drops at the tail so a page that logs in a loop cannot grow the
// host's memory.
type consoleLog struct {
	mu    sync.Mutex
	items []ConsoleEntry
	max   int
}

func newConsoleLog(max int) *consoleLog {
	if max <= 0 {
		max = maxConsoleEntries
	}
	return &consoleLog{max: max}
}

func (l *consoleLog) add(e ConsoleEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, e)
	if len(l.items) > l.max {
		l.items = append([]ConsoleEntry(nil), l.items[len(l.items)-l.max:]...)
	}
}

func (l *consoleLog) tail(n int) []ConsoleEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.items) {
		n = len(l.items)
	}
	out := make([]ConsoleEntry, n)
	copy(out, l.items[len(l.items)-n:])
	return out
}

// netLog joins the three protocol events that describe one request. The ID is
// the protocol's request ID, which stays stable across a redirect.
type netLog struct {
	mu      sync.Mutex
	max     int
	order   []string
	entries map[string]*netRecord
}

type netRecord struct {
	entry   NetworkEntry
	started time.Time
}

func newNetLog(max int) *netLog {
	if max <= 0 {
		max = maxNetworkEntries
	}
	return &netLog{max: max, entries: make(map[string]*netRecord)}
}

func (l *netLog) request(ev *network.EventRequestWillBeSent) {
	if ev == nil || ev.Request == nil {
		return
	}
	id := string(ev.RequestID)
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.entries[id]
	if !ok {
		rec = &netRecord{started: time.Now()}
		l.entries[id] = rec
		l.order = append(l.order, id)
		for len(l.order) > l.max {
			old := l.order[0]
			l.order = l.order[1:]
			delete(l.entries, old)
		}
	}
	rec.entry.At = time.Now()
	rec.entry.Method = ev.Request.Method
	rec.entry.URL = ev.Request.URL
	rec.entry.Type = string(ev.Type)
}

func (l *netLog) response(ev *network.EventResponseReceived) {
	if ev == nil || ev.Response == nil {
		return
	}
	id := string(ev.RequestID)
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.entries[id]
	if !ok {
		rec = &netRecord{started: time.Now()}
		l.entries[id] = rec
		l.order = append(l.order, id)
		for len(l.order) > l.max {
			old := l.order[0]
			l.order = l.order[1:]
			delete(l.entries, old)
		}
	}
	rec.entry.Status = ev.Response.Status
	rec.entry.MimeType = ev.Response.MimeType
	if rec.entry.Type == "" {
		rec.entry.Type = string(ev.Type)
	}
	if rec.entry.At.IsZero() {
		rec.entry.At = time.Now()
	}
	rec.entry.DurationMS = time.Since(rec.started).Milliseconds()
}

func (l *netLog) failed(ev *network.EventLoadingFailed) {
	if ev == nil {
		return
	}
	id := string(ev.RequestID)
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.entries[id]
	if !ok {
		rec = &netRecord{started: time.Now()}
		l.entries[id] = rec
		l.order = append(l.order, id)
		for len(l.order) > l.max {
			old := l.order[0]
			l.order = l.order[1:]
			delete(l.entries, old)
		}
	}
	rec.entry.Failed = ev.ErrorText
	if ev.Canceled && rec.entry.Failed == "" {
		rec.entry.Failed = "canceled"
	}
	if rec.entry.Type == "" {
		rec.entry.Type = string(ev.Type)
	}
	if rec.entry.At.IsZero() {
		rec.entry.At = time.Now()
	}
	rec.entry.DurationMS = time.Since(rec.started).Milliseconds()
}

func (l *netLog) tail(n int) []NetworkEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.order) {
		n = len(l.order)
	}
	ids := l.order[len(l.order)-n:]
	out := make([]NetworkEntry, 0, len(ids))
	for _, id := range ids {
		if rec := l.entries[id]; rec != nil {
			out = append(out, rec.entry)
		}
	}
	return out
}
