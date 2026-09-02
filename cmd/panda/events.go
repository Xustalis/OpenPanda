package main

// One renderer for a task's event timeline, shared by `panda task <id>`,
// `panda logs <id>` and the REPL's /logs.
//
// The three used to print `e.DataJSON` verbatim, which meant a timeline row could
// be a 700-character JSON document — the router's full candidate list and score
// breakdown, inline, per routing decision. That is the right payload to keep in
// the store (and to hand to `--json`), and the wrong thing to show a person
// reading what happened to their task: the one field that explains the row was
// buried in the fields that explain the scheduler.
//
// So a row here is a clock, the event type, and the payload flattened to sorted
// k=v pairs with the noise dropped and the whole line clipped to the terminal.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/core"
)

// eventNoiseKeys are payload fields that exist for the scheduler, not the reader.
// Both are unbounded: `candidates` grows with the fleet and `score_breakdown`
// carries seven floats per candidate, so either one alone can push the row past
// any terminal. `panda task <id> --json` and the audit trail still have them.
var eventNoiseKeys = map[string]bool{
	"candidates":      true,
	"score_breakdown": true,
}

// eventTypeWidth is the type column, sized to the longest event type panda
// records ("supervision_round", 17). A type that outgrows this is clipped rather
// than allowed to shift the payload column, but the point of the number is that
// clipping should not happen: the type is the row's index, and a reader scanning
// a timeline for "where did this fail" is scanning this column.
const eventTypeWidth = 17

// eventLine renders one event as a timeline row, prefixed by indent. The payload
// is dimmed so the eye lands on the clock and the type first, and the row is
// clipped to the terminal so a long reason cannot wrap the timeline into a wall.
func eventLine(e core.Event, indent string) string {
	p := pal()
	when := time.Unix(e.TS, 0).Format("01-02 15:04:05")
	head := indent + when + "  " + cell(e.Type, eventTypeWidth)
	payload := eventPayload(e.DataJSON)
	if payload == "" {
		return strings.TrimRight(head, " ")
	}
	budget := listWidth() - cliui.DisplayWidth(head) - 1
	if budget < 12 {
		budget = 12
	}
	return head + " " + p.Muted(cliui.Truncate(payload, budget, p.Unicode()))
}

// eventPayload flattens an event's JSON object into sorted k=v pairs, dropping
// the fields that carry no information for a reader: absent values (null, "",
// false, 0, empty list/object) and the scheduler's own bookkeeping. A payload
// that is not an object is returned as-is, so an unexpected shape degrades to
// what the timeline printed before rather than to nothing.
func eventPayload(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || raw == "{}" {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return raw
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		if eventNoiseKeys[k] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys) // JSON object order is not stable in Go
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if v := eventValue(obj[k]); v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, " ")
}

// eventValue renders one payload value, or "" for a value that says nothing.
// Strings lose their quotes and escaped newlines (an agent's note is prose, not
// a JSON literal); a composite keeps its compact JSON, which the row's clip
// then bounds.
func eventValue(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.Join(strings.Fields(s), " ") // newlines/tabs → one line
		return s
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		if !b {
			return "" // "authorized=false" is the default, not news
		}
		return "true"
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		if f == 0 {
			return ""
		}
		return trimFloat(f)
	}
	compact := strings.TrimSpace(string(raw))
	switch compact {
	case "null", "[]", "{}", "":
		return ""
	}
	return compact
}

// trimFloat prints a JSON number the way it was most likely written: as an
// integer when it is one, otherwise with two decimals (complexity, scores).
func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%.2f", f)
}

// printEventTimeline writes a task's events under an indent, newest last.
func printEventTimeline(events []core.Event, indent string) {
	for _, e := range events {
		fmt.Println(eventLine(e, indent))
	}
}
