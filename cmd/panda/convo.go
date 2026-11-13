// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Bare-mode conversation: persistence + window management shared by the
// REPL and `panda ask --continue`.
//
// Design notes:
//
//   - The window is bounded by a CHARACTER budget, not a turn count. A
//     fixed turn count fails both ways: short exchanges get evicted too
//     soon ("32 turns" is ~16 questions), while a few long agent outputs
//     eat it instantly. A character budget keeps whatever actually fits
//     the prompt, and trimming is pair-aligned — a user turn is never
//     replayed without its assistant answer (or vice versa), because a
//     dangling half-exchange misleads the model more than a missing one.
//   - The conversation is mirrored to cliStateDir()/conversations/
//     <project>.json (default.json outside a project) after every
//     exchange. People reopen terminals, not conversations: a new REPL
//     resumes where the last one ended, and the one-shot `panda ask
//     --continue` can pick the same thread up from scripts.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// sanitizeProjectName replaces characters that are unsafe in filenames while
// preserving Unicode (e.g. CJK project names).
func sanitizeProjectName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 || r == 0x7f {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	res := strings.TrimSpace(b.String())
	if res == "" || res == "." || res == ".." {
		return "project"
	}
	return res
}

// convoPath is the persisted conversation file ("" when no state dir).
// Conversations are sharded by project: conversations/<project>.json, with
// conversations/default.json when outside a project. Legacy conversation.json
// is migrated to default.json on first access.
func convoPath() string {
	d := cliStateDir()
	if d == "" {
		return ""
	}
	convDir := filepath.Join(d, "conversations")
	defFile := filepath.Join(convDir, "default.json")
	oldFile := filepath.Join(d, "conversation.json")

	// Migrate legacy single-file conversation if present.
	if _, err := os.Stat(oldFile); err == nil {
		if _, err := os.Stat(defFile); os.IsNotExist(err) {
			_ = os.MkdirAll(convDir, 0o700)
			_ = os.Rename(oldFile, defFile)
		}
	}

	cfg, _ := config.Load(cliConfigPath)
	var proj string
	if cfg != nil {
		proj, _ = activeProject(cfg)
	}
	return convoPathForProject(proj)
}

// convoPathForProject resolves one project's bare-conversation file without
// consulting the config pointer. The REPL knows which project it is in (an
// in-memory /project switch, or a launch cwd that is not the persisted active
// project), which can differ from what activeProject resolves — its own reads
// and writes must follow that binding.
func convoPathForProject(project string) string {
	d := cliStateDir()
	if d == "" {
		return ""
	}
	convDir := filepath.Join(d, "conversations")
	project = strings.TrimSpace(project)
	if project == "" {
		return filepath.Join(convDir, "default.json")
	}
	return filepath.Join(convDir, sanitizeProjectName(project)+".json")
}

// renameConvo renames a project's persisted conversation file from oldName to newName.
func renameConvo(oldName, newName string) {
	d := cliStateDir()
	if d == "" {
		return
	}
	convDir := filepath.Join(d, "conversations")
	oldFile := filepath.Join(convDir, sanitizeProjectName(oldName)+".json")
	newFile := filepath.Join(convDir, sanitizeProjectName(newName)+".json")
	if _, err := os.Stat(oldFile); err == nil {
		_ = os.Rename(oldFile, newFile)
	}
	// The roster is scoped to this conversation file.
	renameBareTeam(oldName, newName)
}

// deleteConvo removes a project's persisted conversation file.
func deleteConvo(name string) {
	d := cliStateDir()
	if d == "" {
		return
	}
	convFile := filepath.Join(d, "conversations", sanitizeProjectName(name)+".json")
	_ = os.Remove(convFile)
	deleteBareTeam(name)
}

// loadConvo reads the persisted conversation; missing file or a corrupt
// one degrades to empty (the next save rewrites it clean).
func loadConvo() []entry.Turn {
	p := convoPath()
	if p == "" {
		return nil
	}
	return trimConvo(readConvoFile(p), convoBudget(nil))
}

// saveConvo persists the conversation (atomic-enough for CLI state: one
// small write, best-effort — a lost update costs a stale context, never
// correctness).
func saveConvo(turns []entry.Turn) {
	saveConvoTo(convoPath(), turns)
}

// saveConvoTo is saveConvo at an explicit path, so an attached REPL can write
// its own project's shard instead of whatever the config pointer resolves.
func saveConvoTo(p string, turns []entry.Turn) {
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	data, err := json.Marshal(struct {
		Turns []entry.Turn `json:"turns"`
	}{turns})
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o600)
}

// The attached REPL's own view of the bare-conversation file. Its active
// project is state in memory (`/project`), which need not match what the
// global convoPath resolves from the config pointer and cwd.

func (r *repl) convoFile() string {
	if r == nil {
		return ""
	}
	return convoPathForProject(r.activeProjectName())
}

func (r *repl) loadConvoNow() []entry.Turn {
	p := r.convoFile()
	if p == "" {
		return nil
	}
	return trimConvo(readConvoFile(p), convoBudget(nil))
}

func (r *repl) saveConvoNow(turns []entry.Turn) { saveConvoTo(r.convoFile(), turns) }

func (r *repl) clearConvoNow() {
	if p := r.convoFile(); p != "" {
		_ = os.Remove(p)
	}
}

// convoSummaryMarker prefixes the synthetic turn that carries the running
// digest of compacted history — the same wire marker sessions.SummaryMarker
// defines for session threads.
const convoSummaryMarker = sessions.SummaryMarker

// convoBudget is the character budget the conversation window is managed
// to: derived from the serving model's advertised context window when one
// is known (sessions.ReplayBudget), DefaultHistoryBudget otherwise. Without
// an engine the raw model config resolves it — catalogue lookup only, so a
// node mid-startup (client not yet built) sizes the same way.
func convoBudget(engine *askengine.Engine) int {
	if engine != nil {
		return sessions.ReplayBudget(engine.ContextWindow())
	}
	if cfg, err := config.Load(cliConfigPath); err == nil && cfg != nil {
		return sessions.ReplayBudget(entry.ResolveContextWindow(cfg.Model))
	}
	return sessions.DefaultHistoryBudget
}

// trimConvo evicts whole exchanges from the head while the total size
// exceeds the character budget. The newest exchange always survives.
func trimConvo(turns []entry.Turn, budget int) []entry.Turn {
	total := 0
	for _, t := range turns {
		total += len(t.Content)
	}
	for total > budget && len(turns) > 2 {
		drop := 2 // one user+assistant exchange
		if len(turns)%2 == 1 {
			drop = 1 // defensive: odd tail never happens, but stay aligned
		}
		for i := 0; i < drop && i < len(turns); i++ {
			total -= len(turns[i].Content)
		}
		turns = turns[drop:]
	}
	return turns
}

// compactConvo is trimConvo with auto-compaction: when a model client is
// reachable, the evicted head (which may itself begin with the previous
// digest turn) is folded into a fresh digest that survives as the first
// turn. When the engine is nil or summarization fails it degrades to the
// same hard trim as before — context is lost, never correctness.
func compactConvo(turns []entry.Turn, engine *askengine.Engine) []entry.Turn {
	budget := convoBudget(engine)
	total := 0
	for _, t := range turns {
		total += len(t.Content)
	}
	if total <= budget || len(turns) <= 2 {
		return turns
	}
	if engine == nil {
		return trimConvo(turns, budget)
	}
	var evicted []entry.Turn
	kept := turns
	for total > budget && len(kept) > 2 {
		drop := 2
		if len(kept)%2 == 1 {
			drop = 1
		}
		for i := 0; i < drop && i < len(kept); i++ {
			total -= len(kept[i].Content)
		}
		evicted = append(evicted, kept[:drop]...)
		kept = kept[drop:]
	}
	digest, err := engine.SummarizeTurns(context.Background(), evicted)
	if err != nil || strings.TrimSpace(digest) == "" {
		return trimConvo(turns, budget)
	}
	head := entry.Turn{Role: "user", Content: convoSummaryMarker + "\n" + strings.TrimSpace(digest)}
	return append([]entry.Turn{head}, kept...)
}

// appendConvo records one exchange (user prompt + assistant outcome) into
// the conversation, trims to budget, persists, and returns the new view.
// A task outcome is summarized (title, state, result head) — the next ask
// knows what was done without the full stdout. The locale is the caller's
// (the REPL's, so /lang is honoured), not a freshly detected one. When an
// engine is supplied, overflowing history is compacted instead of dropped.
func appendConvo(turns []entry.Turn, loc i18n.Locale, text string, out *askengine.Result, engine ...*askengine.Engine) []entry.Turn {
	turns = appendConvoTurns(turns, loc, text, out, engine...)
	saveConvo(turns)
	return turns
}

// appendConvoTurns grows an in-memory conversation without persisting it. Voice
// mode keeps its replay window in memory and records the exchange to the
// session thread separately, so it must not write the bare-conversation file.
func appendConvoTurns(turns []entry.Turn, loc i18n.Locale, text string, out *askengine.Result, engine ...*askengine.Engine) []entry.Turn {
	assistant := convoSummaryOf(loc, out)
	turns = append(turns,
		entry.Turn{Role: "user", Content: text},
		entry.Turn{Role: "assistant", Content: assistant},
	)
	var eng *askengine.Engine
	if len(engine) > 0 {
		eng = engine[0]
	}
	return compactConvo(turns, eng)
}

// summarizeSessionTurns adapts the engine's entry.Turn summarizer to the
// store's sessions.Turn shape. A nil engine yields a nil summarizer, which
// CompactHistory treats as "no compaction available".
func summarizeSessionTurns(engine *askengine.Engine) func(context.Context, []sessions.Turn) (string, error) {
	if engine == nil {
		return nil
	}
	return func(ctx context.Context, evicted []sessions.Turn) (string, error) {
		turns := make([]entry.Turn, 0, len(evicted))
		for _, t := range evicted {
			turns = append(turns, entry.Turn{Role: t.Role, Content: t.Text})
		}
		return engine.SummarizeTurns(ctx, turns)
	}
}

// sessionHistory compacts a session thread if needed and builds its replay:
// the running digest as a leading context note, then the surviving turns.
// It returns the possibly-updated session (Summary/Turns may have changed).
// The replay is always budget-bounded: when no engine is reachable (or the
// summarizer failed) the stored thread stays whole but only the tail that
// fits is replayed — the same degradation the bare convo applies.
func sessionHistory(ctx context.Context, store *sessions.Store, sess *sessions.Session, engine *askengine.Engine) (*sessions.Session, []entry.Turn) {
	budget := convoBudget(engine)
	if engine != nil {
		if updated, err := store.CompactHistory(ctx, sess.ID, budget, summarizeSessionTurns(engine)); err == nil {
			sess = updated
		}
	}
	var history []entry.Turn
	if sess.Summary != "" {
		history = append(history, entry.Turn{Role: "user", Content: sessions.SummaryMarker + "\n" + sess.Summary})
	}
	_, kept := sessions.SplitForBudget(sess.ReplayTurns(), budget)
	for _, t := range kept {
		history = append(history, entry.Turn{Role: t.Role, Content: t.Text})
	}
	// A stored thread can end on a user turn when the previous answer never
	// landed. Every caller passes the current prompt separately, so replaying
	// that dangling question would put two user messages in a row — the
	// Messages API rejects that with a 400, wedging every later turn.
	for len(history) > 0 && history[len(history)-1].Role == "user" {
		history = history[:len(history)-1]
	}
	return sess, history
}

// continueTarget resolves what `panda ask --continue` replays: the project's
// newest thread plus its replay window, or the legacy bare conversation when
// the store has no thread for that project. The store comes back even when no
// thread was found, because the caller records the exchange into whichever
// one it ends up using.
func continueTarget(ctx context.Context, cfg *config.Config, project string, engine *askengine.Engine) (*sessions.Store, *sessions.Session, []entry.Turn) {
	store := sessions.NewStore(sessionStoreRoot(cfg))
	if sess := latestSessionForProject(store, project); sess != nil {
		_, history := sessionHistory(ctx, store, sess, engine)
		return store, sess, history
	}
	return store, nil, loadConvo()
}

// continueWorkDir picks where a continued thread runs: its worktree, else the
// engine's bound work path, else the caller's cwd. A thread with a worktree
// must keep running there — continuing it in another checkout would edit the
// wrong tree while replaying the right conversation.
func continueWorkDir(cwd string, sess *sessions.Session, eng *askengine.Engine) string {
	if sess != nil {
		if sess.Worktree != "" {
			return sess.Worktree
		}
		if eng != nil {
			if wp := eng.WorkPath(); wp != "" {
				return wp
			}
		}
	}
	return cwd
}

// maxConvoAnswerChars bounds one recorded assistant answer. A single
// oversized reply (a long report, or the raw tool transcript a budget-cut
// loop falls back to) must not crowd every other exchange out of the
// replay window — the full text stays reachable through the task row and
// the session log.
const maxConvoAnswerChars = 2000

// convoSummaryOf renders the assistant side of one exchange.
func convoSummaryOf(loc i18n.Locale, out *askengine.Result) string {
	if out == nil {
		return i18n.T(loc, "convo.noOutput")
	}
	switch out.Kind {
	case "answer":
		if strings.TrimSpace(out.Answer) != "" {
			return head(out.Answer, maxConvoAnswerChars)
		}
	case "clarify":
		// The question is the turn's content; persisting "no output" would
		// erase it from a thread a surface without a choice card reads back.
		if strings.TrimSpace(out.Answer) != "" {
			return head(out.Answer, maxConvoAnswerChars)
		}
	case "task":
		// A converged ask carries the model's report over the task fields —
		// the transcript should remember what the model told the user, with
		// the raw output still reachable through the task id. The pointer
		// summary below is the degraded path (queue-parked, budget-cut).
		if strings.TrimSpace(out.Answer) != "" {
			return head(out.Answer, maxConvoAnswerChars)
		}
		var s string
		if out.TaskTitle != "" {
			s = i18n.Tf(loc, "convo.taskTitle",
				"id", shortID(out.TaskID), "state", out.TaskState, "title", out.TaskTitle)
		} else {
			s = i18n.Tf(loc, "convo.task", "id", shortID(out.TaskID), "state", out.TaskState)
		}
		s += " "
		if out.OK {
			s += firstLine(head(out.Stdout, 1200))
		} else {
			s += head(out.Stderr, 400)
		}
		return s
	case "plan":
		// The next turn has to know a pipeline is under way, and by which stages:
		// "and the result?" following a plan is a question about those stage ids,
		// and the marker also keeps the task prompt layer attached (ChooseLayers).
		s := i18n.Tf(loc, "convo.plan", "id", shortID(out.PlanID), "goal", out.PlanGoal)
		if names := stageNames(out.PlanStages); names != "" {
			s += i18n.Tf(loc, "convo.stages", "names", names)
		}
		return s
	}
	return i18n.T(loc, "convo.noOutput")
}

// stageNames lists a plan's stage ids in order, for the conversation record.
func stageNames(stages []core.Task) string {
	if len(stages) == 0 {
		return ""
	}
	ids := make([]string, 0, len(stages))
	for _, t := range stages {
		ids = append(ids, t.StageID+" "+t.State)
	}
	return strings.Join(ids, " → ")
}
