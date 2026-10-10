// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func TestResolveSessionRef(t *testing.T) {
	store := sessions.NewStore(t.TempDir())
	a, err := store.Create("alpha", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	b, err := store.Create("beta", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Force a shared prefix: ids are generated, so rename one file to collide.
	// Simpler: derive refs from the real ids — the first 4 chars may or may
	// not collide, but the full id always resolves exactly.
	if got, err := resolveSessionRef(store, a.ID); err != nil || got != a.ID {
		t.Fatalf("exact ref: %q %v", got, err)
	}
	// Unique prefix resolves; make the prefix long enough to be unique.
	prefix := a.ID[:len(a.ID)-1]
	if b.ID[:len(b.ID)-1] == prefix {
		t.Skip("ids collided at len-1 — cannot build a unique prefix case")
	}
	if got, err := resolveSessionRef(store, prefix); err != nil || got != a.ID {
		t.Fatalf("prefix ref: %q %v", got, err)
	}
	if _, err := resolveSessionRef(store, "zzzzzz"); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatalf("missing ref err = %v", err)
	}
	// Ambiguous: the shared first char between a and b must exist (both are
	// 16-char time-ordered ids — find the longest common prefix and extend).
	common := 0
	for common < len(a.ID) && common < len(b.ID) && a.ID[common] == b.ID[common] {
		common++
	}
	if common > 0 {
		_, err := resolveSessionRef(store, a.ID[:common])
		var amb sessionAmbiguousError
		if !errors.As(err, &amb) || len(amb.candidates) < 2 {
			t.Fatalf("ambiguous ref err = %v", err)
		}
	}
}

func TestParseOlderThan(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"30d":   30 * 24 * time.Hour,
		"2w":    14 * 24 * time.Hour,
		"12h":   12 * time.Hour,
		"90m":   90 * time.Minute,
		"1h30m": 90 * time.Minute,
	} {
		got, err := parseOlderThan(in)
		if err != nil || got != want {
			t.Fatalf("parseOlderThan(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0d", "-3d", "soon", "10x"} {
		if _, err := parseOlderThan(bad); err == nil {
			t.Fatalf("parseOlderThan(%q) should fail", bad)
		}
	}
}

func TestReplSessionRmBatch(t *testing.T) {
	r, _ := newOpsTestRepl(t)
	var ids []string
	for _, title := range []string{"one", "two", "three"} {
		out := dispatchCapture(t, r, `/session new --title "`+title+`"`)
		fields := strings.Fields(out)
		if len(fields) == 0 {
			t.Fatalf("/session new printed nothing for %q: %q", title, out)
		}
		ids = append(ids, fields[0])
	}

	// A bad ref in the batch reports but does not stop the rest.
	out := dispatchCapture(t, r, "/session rm zzzz "+ids[0]+" "+ids[1])
	if !strings.Contains(out, ids[0]) || !strings.Contains(out, ids[1]) {
		t.Fatalf("batch rm should delete the two good refs, got %q", out)
	}
	for _, id := range ids[:2] {
		if _, err := r.sessionsSt.Get(id); err == nil {
			t.Fatalf("session %s should be gone", id)
		}
	}
	if _, err := r.sessionsSt.Get(ids[2]); err != nil {
		t.Fatalf("unlisted session %s should survive", ids[2])
	}

	// The attached session refuses inside a batch without killing siblings.
	out = dispatchCapture(t, r, `/session new --title "four"`)
	fields := strings.Fields(out)
	four := fields[0]
	r.activeSess = ids[2]
	out = dispatchCapture(t, r, "/session rm "+ids[2]+" "+four)
	if !strings.Contains(out, "/resume -") {
		t.Fatalf("attached session should refuse inline, got %q", out)
	}
	if _, err := r.sessionsSt.Get(ids[2]); err != nil {
		t.Fatal("attached session should survive the batch")
	}
	if _, err := r.sessionsSt.Get(four); err == nil {
		t.Fatal("sibling in the same batch should be deleted")
	}
}
