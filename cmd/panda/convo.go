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
//   - The conversation is mirrored to cliStateDir()/conversation.json
//     after every exchange. People reopen terminals, not conversations:
//     a new REPL resumes where the last one ended, and the one-shot
//     `panda ask --continue` can pick the same thread up from scripts.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// maxConvoChars is the replay budget for the bare-mode conversation:
// roughly the payload the entry prompt can carry without crowding out
// the device list and memory wall. ~24k chars ≈ 12–20k tokens depending
// on script mix — a couple dozen short exchanges or a handful of long
// task reports.
const maxConvoChars = 24000

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

	cfg, _ := config.Load("")
	var proj string
	if cfg != nil {
		proj, _ = activeProject(cfg)
	}
	if strings.TrimSpace(proj) == "" {
		return defFile
	}
	safe := sanitizeProjectName(strings.TrimSpace(proj))
	return filepath.Join(convDir, safe+".json")
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
}

// deleteConvo removes a project's persisted conversation file.
func deleteConvo(name string) {
	d := cliStateDir()
	if d == "" {
		return
	}
	convFile := filepath.Join(d, "conversations", sanitizeProjectName(name)+".json")
	_ = os.Remove(convFile)
}

// loadConvo reads the persisted conversation; missing file or a corrupt
// one degrades to empty (the next save rewrites it clean).
func loadConvo() []entry.Turn {
	p := convoPath()
	if p == "" {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var f struct {
		Turns []entry.Turn `json:"turns"`
	}
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	return trimConvo(f.Turns)
}

// saveConvo persists the conversation (atomic-enough for CLI state: one
// small write, best-effort — a lost update costs a stale context, never
// correctness).
func saveConvo(turns []entry.Turn) {
	p := convoPath()
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

// clearConvo removes the persisted conversation (/new).
func clearConvo() {
	if p := convoPath(); p != "" {
		_ = os.Remove(p)
	}
}

// trimConvo evicts whole exchanges from the head while the total size
// exceeds the character budget. The newest exchange always survives.
func trimConvo(turns []entry.Turn) []entry.Turn {
	total := 0
	for _, t := range turns {
		total += len(t.Content)
	}
	for total > maxConvoChars && len(turns) > 2 {
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

// appendConvo records one exchange (user prompt + assistant outcome) into
// the conversation, trims to budget, persists, and returns the new view.
// A task outcome is summarized (title, state, result head) — the next ask
// knows what was done without the full stdout. The locale is the caller's
// (the REPL's, so /lang is honoured), not a freshly detected one.
func appendConvo(turns []entry.Turn, loc i18n.Locale, text string, out *askengine.Result) []entry.Turn {
	assistant := convoSummaryOf(loc, out)
	turns = append(turns,
		entry.Turn{Role: "user", Content: text},
		entry.Turn{Role: "assistant", Content: assistant},
	)
	turns = trimConvo(turns)
	saveConvo(turns)
	return turns
}

// convoSummaryOf renders the assistant side of one exchange.
func convoSummaryOf(loc i18n.Locale, out *askengine.Result) string {
	if out == nil {
		return i18n.T(loc, "convo.noOutput")
	}
	switch out.Kind {
	case "answer":
		if strings.TrimSpace(out.Answer) != "" {
			return out.Answer
		}
	case "task":
		// A converged ask carries the model's report over the task fields —
		// the transcript should remember what the model told the user, with
		// the raw output still reachable through the task id. The pointer
		// summary below is the degraded path (queue-parked, budget-cut).
		if strings.TrimSpace(out.Answer) != "" {
			return out.Answer
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
