// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// The terminal's conversation is a session.
//
// Two conversation stores used to coexist: the web console's sessions (one
// JSON file per thread under <data>/sessions) and the terminal's bare
// conversation (cliStateDir()/conversations/<project>.json). A chat started in
// the TUI was invisible in the console, and a console thread had to be
// /resume-d by id from a terminal. The terminal now speaks the console's model:
//
//   - the first prompt of a sitting lazily creates a thread, so a terminal chat
//     shows up in the console's rail like any other conversation;
//   - launching resumes the project's most recently updated thread, which is
//     what the bare conversation file used to be (people reopen terminals, not
//     conversations);
//   - legacy bare-conversation files are imported once and archived, never
//     deleted — the original stays on disk for the user.
//
// The bare conversation remains only as a fallback: no session store, or a
// session write that failed, keeps the in-memory thread working exactly as
// before.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// convoTitle picks the title a migrated bare conversation should carry: its
// first user turn, else its first non-empty turn.
func convoTitle(turns []entry.Turn) string {
	for _, t := range turns {
		if t.Role == "user" {
			if title := sessions.Title(t.Content); title != "" {
				return title
			}
		}
	}
	for _, t := range turns {
		if title := sessions.Title(t.Content); title != "" {
			return title
		}
	}
	return ""
}

// convoDir is the directory holding the legacy bare-conversation files.
func convoDir() string {
	d := cliStateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "conversations")
}

// readConvoFile decodes one persisted conversation file. An unreadable or
// malformed file reads as empty rather than failing a launch.
func readConvoFile(path string) []entry.Turn {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f struct {
		Turns []entry.Turn `json:"turns"`
	}
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	return f.Turns
}

// migratableTurn reports whether a bare-conversation turn can become a
// session turn. The synthetic compaction digest is not: it is not something
// the user wrote, and as the thread's first user turn it would become the
// rail snippet. The session re-compacts its own history later, so the digest
// is the one part of a legacy file that is dropped.
func migratableTurn(t entry.Turn) bool {
	if t.Role != "user" && t.Role != "assistant" {
		return false
	}
	text := strings.TrimSpace(t.Content)
	return text != "" && !strings.HasPrefix(text, sessions.SummaryMarker)
}

// migrateLegacyConvos imports every bare-conversation file into a session and
// archives the file as <name>.json.migrated. It runs once per launch and is
// idempotent: the rename is what stops a second import.
func (r *repl) migrateLegacyConvos() {
	if r == nil || r.sessionsSt == nil {
		return
	}
	// convoPath folds the pre-sharding conversation.json into default.json
	// before the directory scan.
	_ = convoPath()
	dir := convoDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		archived := strings.TrimSuffix(path, ".json") + ".json.migrated"
		// Claim the file by renaming first: a second launch racing this one
		// then sees no .json and cannot import the same conversation twice.
		// The failure paths below move it back so a later launch can retry.
		if err := os.Rename(path, archived); err != nil {
			continue
		}
		project := strings.TrimSuffix(name, ".json")
		if project == "default" {
			project = ""
		}
		var kept []entry.Turn
		for _, t := range readConvoFile(archived) {
			if migratableTurn(t) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			// Nothing importable (empty, or only the synthetic digest): the
			// archived copy is the backup, and retrying buys nothing.
			continue
		}
		sess, cerr := r.sessionsSt.Create(convoTitle(kept), project)
		if cerr != nil {
			_ = os.Rename(archived, path) // store unavailable: retry next launch
			continue
		}
		ok := true
		for _, t := range kept {
			if _, aerr := r.sessionsSt.AppendTurn(sess.ID, sessions.Turn{Role: t.Role, Text: t.Content}); aerr != nil {
				ok = false
				break
			}
		}
		if !ok {
			// A partially written import would make the thread look like a
			// shorter conversation than it was. Drop it and restore the source
			// so the next launch can retry.
			_ = r.sessionsSt.Delete(sess.ID)
			_ = os.Rename(archived, path)
			continue
		}
		if team := readTeamFile(bareTeamFileFor(project)); len(team) > 0 {
			_ = r.sessionsSt.SetTeam(sess.ID, team)
		}
	}
}

// prepareConversation decides which thread this sitting runs on: an explicit
// --resume id, a fresh start (--new), or the project's most recently updated
// thread. Legacy bare conversations are imported first, so a terminal upgraded
// from an older build finds the same conversation as a real session.
func (r *repl) prepareConversation(resumeID string, startFresh bool) (*sessions.Session, error) {
	if r == nil || r.sessionsSt == nil {
		return nil, nil
	}
	r.migrateLegacyConvos()
	if startFresh {
		// --new: leave whatever was bound behind; the next prompt opens a new
		// thread. Detach explicitly so the contract holds for any caller, not
		// only the launch path where nothing is bound yet, and drop any
		// in-memory bare conversation the caller may have loaded already.
		r.setSess("")
		r.setConvo(nil)
		return nil, nil
	}
	if id := strings.TrimSpace(resumeID); id != "" {
		sess, err := r.sessionsSt.Get(id)
		if err != nil {
			return nil, err
		}
		r.setSess(sess.ID)
		return sess, nil
	}
	if sess := r.autoResumeSession(); sess != nil {
		return sess, nil
	}
	// Nothing to resume: fall back to the legacy file if a previous write
	// landed after the migration pass (or the store is unavailable).
	if c := r.loadConvoNow(); len(c) > 0 {
		r.setConvo(c)
	}
	return nil, nil
}

// autoResumeSession binds the project's most recently updated non-empty
// thread.
func (r *repl) autoResumeSession() *sessions.Session {
	sess := latestSessionForProject(r.sessionsSt, r.activeProjectName())
	if sess == nil {
		return nil
	}
	r.setSess(sess.ID)
	return sess
}

// latestSessionForProject returns the newest thread that has at least one
// turn. Empty threads (a `session new` nobody wrote to) are skipped: resuming
// one would show a blank conversation where the user expects their last one.
// Stamps carry mtimes only, so the usual case (the newest file matches the
// project) costs one Get instead of decoding the whole store. Pinned is a
// rail-ordering flag, not a launch preference: the newest thread wins.
func latestSessionForProject(st *sessions.Store, project string) *sessions.Session {
	if st == nil {
		return nil
	}
	stamps, err := st.Stamps()
	if err != nil {
		return nil
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i].ModUnixNano > stamps[j].ModUnixNano })
	project = strings.TrimSpace(project)
	for _, stamp := range stamps {
		sess, err := st.Get(stamp.ID)
		if err != nil {
			continue
		}
		if sess.Project != project || len(sess.Turns) == 0 {
			continue
		}
		return sess
	}
	return nil
}

// boundSession returns the session the repl is attached to, nil when detached
// or the id went stale.
func (r *repl) boundSession() *sessions.Session {
	if r == nil || r.sessionsSt == nil {
		return nil
	}
	sid := r.sessID()
	if sid == "" {
		return nil
	}
	sess, err := r.sessionsSt.Get(sid)
	if err != nil {
		return nil
	}
	return sess
}

// startImplicitSession binds the sitting to a new session on its first prompt
// and moves the bare conversation into it. Returns false when there is no
// store or a write failed, in which case the caller keeps bare mode.
func (r *repl) startImplicitSession(prompt string) bool {
	if r == nil || r.sessionsSt == nil {
		return false
	}
	sess, err := r.sessionsSt.Create(sessions.Title(prompt), r.activeProjectName())
	if err != nil {
		return false
	}
	for _, t := range r.convoNow() {
		if !migratableTurn(t) {
			continue
		}
		if _, err := r.sessionsSt.AppendTurn(sess.ID, sessions.Turn{Role: t.Role, Text: t.Content}); err != nil {
			_ = r.sessionsSt.Delete(sess.ID)
			return false
		}
	}
	// The roster belongs to the conversation, so it moves with it: otherwise
	// the first turn after binding would silently drop the team the user set
	// in bare mode.
	if team := r.loadBareTeam(); len(team) > 0 {
		if err := r.sessionsSt.SetTeam(sess.ID, team); err == nil {
			_ = r.saveBareTeam(nil)
		}
	}
	r.setSess(sess.ID)
	r.setConvo(nil)
	r.clearConvoNow()
	return true
}

// rebindProjectConversation is the project-switch counterpart of
// prepareConversation: the old thread belongs to the project the user just
// left, so detach and open the new project's newest thread (if any).
func (r *repl) rebindProjectConversation() *sessions.Session {
	if r == nil {
		return nil
	}
	r.setSess("")
	if sess := r.autoResumeSession(); sess != nil {
		return sess
	}
	// No thread in the new project: the legacy file is the only history left.
	r.setConvo(r.loadConvoNow())
	return nil
}

// appendExchangeToSession records one completed ask as a user/assistant pair,
// the same turn shape both front ends write. The task id rides the assistant
// turn's Ref so the console renders the reply as a link to the run.
func appendExchangeToSession(st *sessions.Store, id, prompt string, out *askengine.Result, loc i18n.Locale) {
	if st == nil || id == "" || out == nil {
		return
	}
	turn := sessions.Turn{Role: "assistant", Kind: out.Kind, Text: convoSummaryOf(loc, out)}
	switch out.Kind {
	case "task":
		turn.Ref = out.TaskID
	case "plan":
		turn.Ref = out.PlanID
	}
	// One write for the pair: a question stored without its answer would be
	// replayed back-to-back with the next prompt, which the API rejects.
	_, _ = st.AppendTurns(id, sessions.Turn{Role: "user", Text: prompt}, turn)
}

// bindTaskToSession links a spawned task to the thread it came from, so the
// console's board entry can jump back into the conversation.
func bindTaskToSession(ctx context.Context, eng *askengine.Engine, taskID, sessionID string) {
	if eng == nil || taskID == "" || sessionID == "" {
		return
	}
	if store := eng.TaskStore(); store != nil {
		_ = store.SetSessionID(ctx, taskID, sessionID)
	}
}
