// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// The mic loop itself cannot be driven by a unit test (entry.Listen owns the
// sidecars), so the session-binding half lives in voiceThread and is covered
// here: first utterance creates, later ones reuse, and a failed first ask
// leaves no empty thread behind.

import (
	"context"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

func TestVoiceThreadCreatesReusesAndRecords(t *testing.T) {
	st := sessions.NewStore(t.TempDir())
	vt := &voiceThread{store: st}

	vt.ensure("turn on the lights")
	if vt.sessionID() == "" {
		t.Fatal("first utterance did not create a thread")
	}
	vt.record(context.Background(), nil, "turn on the lights",
		&askengine.Result{Kind: "answer", Answer: "done"}, i18n.English)

	first := vt.sessionID()
	sess, err := st.Get(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != 2 || sess.Turns[0].Text != "turn on the lights" {
		t.Fatalf("turns = %+v", sess.Turns)
	}
	if sess.Title != "turn on the lights" {
		t.Fatalf("title = %q", sess.Title)
	}

	// The second utterance stays on the same thread and records its pair.
	vt.ensure("and the fan")
	if vt.sessionID() != first {
		t.Fatalf("second utterance created %q, want the existing %q", vt.sessionID(), first)
	}
	vt.record(context.Background(), nil, "and the fan",
		&askengine.Result{Kind: "task", TaskID: "t-1", OK: true}, i18n.English)
	sess, err = st.Get(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Turns) != 4 || sess.Turns[3].Ref != "t-1" {
		t.Fatalf("turns after second exchange = %+v", sess.Turns)
	}
}

func TestVoiceThreadDiscardsEmptyThread(t *testing.T) {
	st := sessions.NewStore(t.TempDir())
	vt := &voiceThread{store: st}
	vt.ensure("nobody heard this")
	id := vt.sessionID()
	if id == "" {
		t.Fatal("ensure created nothing")
	}
	vt.discardEmpty()
	if vt.sessionID() != "" {
		t.Fatalf("binding survived discard: %q", vt.sessionID())
	}
	if _, err := st.Get(id); err == nil {
		t.Fatalf("empty thread %q survived", id)
	}
}
