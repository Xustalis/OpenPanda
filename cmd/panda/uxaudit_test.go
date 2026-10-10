// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// --- adaptive task refs ------------------------------------------------------

// UUIDv7 packs the millisecond timestamp into the leading bytes, so tasks
// submitted in the same second share the first dash group — the classic
// shortID cut. Refs must lengthen until the row's id resolves unambiguously.
func TestTaskRefsCollidingPrefixes(t *testing.T) {
	all := []string{
		"01a11f36-9c2b-7fb5-b73a-93f9301dba7b",
		"01a11f36-9c71-7826-b029-198189f94707",
		"01a11f36-8364-71a1-83e3-0bb8424c4a81",
		"01a12000-0000-7000-8000-000000000000",
	}
	refs := taskRefs(all, all)
	for _, id := range all {
		ref := refs[id]
		if len(ref) < taskRefFloor {
			t.Fatalf("ref %q shorter than floor", ref)
		}
		matches := 0
		for _, other := range all {
			if strings.HasPrefix(other, ref) {
				matches++
			}
		}
		if matches != 1 {
			t.Fatalf("ref %q for %s matches %d ids", ref, id, matches)
		}
	}
	// The three same-second ids must differ from each other.
	if refs[all[0]] == refs[all[1]] || refs[all[1]] == refs[all[2]] || refs[all[0]] == refs[all[2]] {
		t.Fatalf("colliding refs: %v", refs)
	}
	// The lone id keeps the short form.
	if refs[all[3]] != "01a12000" {
		t.Fatalf("unique id ref = %q, want 01a12000", refs[all[3]])
	}
}

func TestTaskRefsCapAndFallback(t *testing.T) {
	// Two ids identical through the cap cannot disambiguate — the ref stops at
	// the ceiling rather than eating the title column.
	a := "01a11f36-9c2b-7000-8000-000000000001"
	b := "01a11f36-9c2b-7000-8000-000000000002"
	refs := taskRefs([]string{a, b}, []string{a, b})
	if len(refs[a]) > taskRefCeil {
		t.Fatalf("ref over cap: %q", refs[a])
	}
	// An unlisted id falls back to the first-group cut.
	if got := refOr(refs, "zzzz"); got != "zzzz" {
		t.Fatalf("refOr fallback = %q", got)
	}
}

// --- event payload tail truncation -------------------------------------------

func TestEventPayloadTailFlag(t *testing.T) {
	payload, tail := eventPayload(`{"failed":"can't open file '/x/adapters/op.py': No such file or directory"}`)
	if !tail {
		t.Fatalf("single failed field not flagged tail, payload=%q", payload)
	}
	if !strings.HasPrefix(payload, "failed=") {
		t.Fatalf("payload = %q", payload)
	}

	payload, tail = eventPayload(`{"state":"queued","node":"n1"}`)
	if tail {
		t.Fatalf("multi-field payload flagged tail")
	}
	if !strings.Contains(payload, "state=queued") {
		t.Fatalf("payload = %q", payload)
	}

	// Scheduler bookkeeping never reaches the row.
	payload, _ = eventPayload(`{"candidates":["a","b"],"score_breakdown":{"a":1},"node":"n1"}`)
	if strings.Contains(payload, "candidates") || strings.Contains(payload, "score_breakdown") {
		t.Fatalf("noise leaked: %q", payload)
	}
	if payload != "node=n1" {
		t.Fatalf("payload = %q", payload)
	}

	// Empty/absent values flatten away.
	payload, _ = eventPayload(`{"reason":"","count":0,"flag":false}`)
	if payload != "" {
		t.Fatalf("empty payload = %q", payload)
	}
}

func TestEventLineKeepsErrorTail(t *testing.T) {
	long := "can't open file '/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/lib/python313.zip/adapters/opencode.py': [Errno 2] No such file or directory"
	raw := `{"failed":` + mustJSON(t, long) + `}`
	line := eventLine(core.Event{TS: 1700000000, Type: core.EvResult, DataJSON: raw}, "")
	if !strings.Contains(line, "No such file or directory") {
		t.Fatalf("cause truncated away: %q", line)
	}
	if !strings.Contains(line, "failed=") {
		t.Fatalf("key lost: %q", line)
	}
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	out := strings.Builder{}
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\'':
			out.WriteRune(r)
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}

// --- task next-step hints ------------------------------------------------------

func TestTaskNextStepHint(t *testing.T) {
	loc := i18n.Locale("en")

	review := core.Task{TaskID: "abc", State: core.StateReview}
	if hint := taskNextStepHint(loc, nil, review); !strings.Contains(hint, "approve") {
		t.Fatalf("review hint = %q", hint)
	}

	waiting := core.Task{TaskID: "abc", State: core.StateWaitingCtx}
	if hint := taskNextStepHint(loc, nil, waiting); hint == "" {
		t.Fatalf("waiting_context hint empty")
	}

	done := core.Task{TaskID: "abc", State: core.StateDone}
	if hint := taskNextStepHint(loc, nil, done); hint != "" {
		t.Fatalf("done hint = %q", hint)
	}

	// queued with nil config: no consumer probe possible, no scare text.
	queued := core.Task{TaskID: "abc", State: core.StateQueued}
	if hint := taskNextStepHint(loc, nil, queued); hint != "" {
		t.Fatalf("queued hint with nil cfg = %q", hint)
	}
}

// --- ability list count truncation -------------------------------------------

func TestTruncateListCount(t *testing.T) {
	items := []string{"shell:sh", "agent:claude", "agent:codex", "agent:gemini", "manual:review"}
	if got := truncateListCount(items, 200, true); got != strings.Join(items, ", ") {
		t.Fatalf("fits: %q", got)
	}
	got := truncateListCount(items, 40, true)
	if !strings.Contains(got, "…+") {
		t.Fatalf("no count suffix: %q", got)
	}
	if strings.HasSuffix(got, "…") {
		t.Fatalf("bare ellipsis, no count: %q", got)
	}
	// Nothing fits: pure count.
	got = truncateListCount(items, 8, true)
	if got != "…+5" {
		t.Fatalf("all-hidden = %q, want …+5", got)
	}
	// ASCII fallback.
	got = truncateListCount(items, 8, false)
	if got != "...+5" {
		t.Fatalf("ascii all-hidden = %q, want ...+5", got)
	}
}

// --- config action/section swap ------------------------------------------------

func TestIsConfigAction(t *testing.T) {
	for _, s := range []string{"get", "set", "test"} {
		if !isConfigAction(s) {
			t.Fatalf("%q not an action", s)
		}
	}
	for _, s := range []string{"model", "mcp", "help", ""} {
		if isConfigAction(s) {
			t.Fatalf("%q wrongly an action", s)
		}
	}
}
