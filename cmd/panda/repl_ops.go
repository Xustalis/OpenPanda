// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// The REPL's third tier of commands — the operator surfaces that already
// exist as one-shot CLI verbs (metrics, audit, reminders, session
// worktree management, queue edits, node removal) but used to require
// leaving the session to reach. Each handler speaks to the repl's
// already-open store/engine and writes through the scoped command
// streams, so the same code runs in the classic loop and inside the
// Bubble Tea exec pump without touching process stdout.

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/trust"
)

// splitArgs tokenizes a slash-command tail, honoring single and double
// quotes so `--title "deploy the thing"` keeps its payload intact. Single
// quotes matter for `--action-spec '{"a":"b c"}'`: JSON's own double quotes
// would otherwise toggle the parser mid-token, stripping the quotes and
// leaving {a:b c} — unparseable every time.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune // 0 outside a quote; '"'/'\'' inside one
	for _, r := range s {
		switch {
		case quote == 0 && (r == '"' || r == '\''):
			quote = r
		case r == quote:
			quote = 0
		case (r == ' ' || r == '\t') && quote == 0:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// cmdMetrics implements /metrics [--csv] — delegation metrics, same data as
// `panda metrics` but written to the scoped stream.
func (r *repl) cmdMetrics(arg string) {
	metrics, err := r.store.ListDelegationMetrics(r.commandContext())
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(metrics) == 0 {
		r.outln(i18n.T(r.loc, "cli.metrics.none"))
		return
	}
	if strings.TrimSpace(arg) == "--csv" {
		if err := writeMetricsCSV(r.commandOutput(), metrics); err != nil {
			r.errf("panda: %v\n", err)
		}
		return
	}
	printMetricsTableTo(r.commandOutput(), r.loc, metrics)
}

// writeMetricsCSV renders the full delegation history as CSV rows — the
// machine-consumable half of `panda metrics --csv`, un-anchored from stdout.
func writeMetricsCSV(w io.Writer, metrics []core.DelegationMetric) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "task_id", "delegator", "executor", "abilities", "success", "latency_ms", "tokens", "cost", "created_at"})
	for _, m := range metrics {
		abilities := ""
		if m.AbilitiesJSON != "" {
			var parsed []string
			if err := json.Unmarshal([]byte(m.AbilitiesJSON), &parsed); err == nil {
				abilities = strings.Join(parsed, ";")
			} else {
				abilities = m.AbilitiesJSON
			}
		}
		tokens := ""
		if m.Tokens.Valid {
			tokens = strconv.FormatInt(m.Tokens.Int64, 10)
		}
		cost := ""
		if m.Cost.Valid {
			cost = strconv.FormatFloat(m.Cost.Float64, 'f', 6, 64)
		}
		_ = cw.Write([]string{
			strconv.FormatInt(m.ID, 10),
			m.TaskID,
			m.Delegator,
			m.Executor,
			abilities,
			strconv.FormatBool(m.Success),
			strconv.FormatInt(m.LatencyMs, 10),
			tokens,
			cost,
			ts(m.CreatedAt),
		})
	}
	cw.Flush()
	return cw.Error()
}

// cmdAudit implements /audit [verify|entries] [task-id] — the hash-chained
// trail. Bare /audit verifies the global chain (the historical default);
// "entries" lists rows, globally or for one task's event timeline.
func (r *repl) cmdAudit(arg string) {
	fields := splitArgs(arg)
	verb := "verify"
	taskRef := ""
	for i, f := range fields {
		switch f {
		case "verify", "entries":
			verb = f
		case "--task", "-task":
			if i+1 < len(fields) {
				taskRef = fields[i+1]
			}
		default:
			if !strings.HasPrefix(f, "-") && taskRef == "" {
				taskRef = f
			}
		}
	}
	ctx := r.commandContext()
	taskID := ""
	if taskRef != "" {
		id, ok := r.resolveRef(taskRef)
		if !ok {
			return
		}
		taskID = id
	}
	if verb == "entries" {
		r.auditEntries(ctx, taskID)
		return
	}
	if taskID != "" {
		if err := r.store.VerifyTaskEventChain(ctx, taskID); err != nil {
			r.errf("panda: task event chain broken: %v\n", err)
			return
		}
		r.outf("task %s event chain: OK\n", taskID)
		return
	}
	if err := verifyAudit(r.db).VerifyChain(ctx); err != nil {
		r.errf("panda: audit chain broken: %v\n", err)
		return
	}
	r.outln("audit chain: OK")
}

// auditEntries prints audit rows — the global audit_log, or one task's
// event timeline when a task is scoped.
func (r *repl) auditEntries(ctx context.Context, taskID string) {
	if taskID != "" {
		events, err := r.store.Events(ctx, taskID)
		if err != nil {
			r.storeErr(err)
			return
		}
		if len(events) == 0 {
			r.outln(i18n.Tf(r.loc, "cli.logs.none", "id", taskID))
			return
		}
		for _, e := range events {
			r.outf("%s  %-10s %s\n", ts(e.TS), e.Type, e.DataJSON)
		}
		return
	}
	rows, err := security.NewAudit(r.db).Entries(ctx)
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(rows) == 0 {
		r.outln(i18n.T(r.loc, "cli.audit.none"))
		return
	}
	for _, row := range rows {
		r.outf("%-20s %-24s %-16s %-12s %-8s %s\n", ts(row.TS), row.Who, row.What, row.Target, row.Result, row.Detail)
	}
}

// cmdReminder implements /reminder — list|add|rm, same verbs as
// `panda reminder` but on the already-open store.
func (r *repl) cmdReminder(arg string) {
	st := reminders.NewStore(r.db)
	fields := splitArgs(arg)
	sub := "list"
	if len(fields) > 0 {
		sub = fields[0]
		fields = fields[1:]
	}
	ctx := r.commandContext()
	switch sub {
	case "list", "ls":
		list, err := st.List(ctx, true)
		if err != nil {
			r.storeErr(err)
			return
		}
		if len(list) == 0 {
			r.outln(i18n.T(r.loc, "cli.reminder.none"))
			return
		}
		for _, rem := range list {
			status := "pending"
			if rem.RepeatSeconds > 0 {
				status = fmt.Sprintf("every %s", time.Duration(rem.RepeatSeconds)*time.Second)
			} else if rem.FiredAt != 0 {
				status = "fired"
			}
			r.outf("#%-4d %-16s %-12s %s\n",
				rem.ID, time.Unix(rem.DueAt, 0).Format("2006-01-02 15:04"), status, rem.Message)
		}
	case "add":
		r.cmdReminderAdd(ctx, st, fields)
	case "rm", "remove", "del":
		if len(fields) == 0 {
			r.outln("usage: /reminder rm <id>")
			return
		}
		id, err := strconv.ParseInt(strings.TrimSpace(fields[0]), 10, 64)
		if err != nil {
			r.outln(i18n.Tf(r.loc, "cli.reminder.badID", "id", fields[0]))
			return
		}
		ok, err := st.Delete(ctx, id)
		if err != nil {
			r.storeErr(err)
			return
		}
		if !ok {
			r.outln(i18n.Tf(r.loc, "cli.reminder.notFound", "id", fields[0]))
			return
		}
		r.outln(i18n.Tf(r.loc, "cli.reminder.removed", "id", fields[0]))
	default:
		r.outln("usage: /reminder <list | add --after 10m \"text\" | add --at \"2006-01-02 15:04\" \"text\" | rm <id>>")
	}
}

// cmdReminderAdd parses `/reminder add [--after D | --at T] [--every D] msg`.
func (r *repl) cmdReminderAdd(ctx context.Context, st *reminders.Store, fields []string) {
	var after, at, every string
	var msg []string
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "--after", "-after":
			if i+1 < len(fields) {
				after = fields[i+1]
				i++
			}
		case "--at", "-at":
			if i+1 < len(fields) {
				at = fields[i+1]
				i++
			}
		case "--every", "-every":
			if i+1 < len(fields) {
				every = fields[i+1]
				i++
			}
		default:
			msg = append(msg, fields[i])
		}
	}
	message := strings.TrimSpace(strings.Join(msg, " "))
	if message == "" {
		r.outln(i18n.T(r.loc, "cli.reminder.noMessage"))
		return
	}
	if (after == "") == (at == "") {
		r.outln(i18n.T(r.loc, "cli.reminder.oneFlag"))
		return
	}
	var due time.Time
	if after != "" {
		dur, err := time.ParseDuration(after)
		if err != nil {
			r.outln(i18n.Tf(r.loc, "cli.reminder.badAfter", "value", after, "err", err.Error()))
			return
		}
		due = time.Now().Add(dur)
	} else {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
			if t, err := time.ParseInLocation(layout, at, time.Local); err == nil {
				due = t
				break
			}
		}
		if due.IsZero() {
			r.outln(i18n.Tf(r.loc, "cli.reminder.badAt", "value", at))
			return
		}
	}
	var repeat time.Duration
	if every != "" {
		d, derr := time.ParseDuration(every)
		if derr != nil || d <= 0 {
			r.outln(i18n.Tf(r.loc, "cli.reminder.badEvery", "value", every))
			return
		}
		repeat = d
	}
	rem, err := st.AddEvery(ctx, message, due, repeat, "cli")
	if err != nil {
		r.storeErr(err)
		return
	}
	r.outln(i18n.Tf(r.loc, "cli.reminder.added", "id", strconv.FormatInt(rem.ID, 10), "due", due.Format("2006-01-02 15:04:05")))
	if repeat > 0 {
		r.outln(i18n.Tf(r.loc, "cli.reminder.repeats", "every", repeat.String()))
	}
}

// cmdSession implements /session <verb> — the worktree-side session verbs
// `panda session` exposes: new | show | mv | rm | diff | merge | revert.
// `/sessions` stays the list view; `/resume` stays the attach/detach verb.
func (r *repl) cmdSession(arg string) {
	fields := splitArgs(arg)
	if len(fields) == 0 {
		r.outln("usage: /session <new|show|mv|rm|diff|merge|revert> …")
		return
	}
	if r.sessionsSt == nil {
		r.outln(i18n.T(r.loc, "repl.sessions.none"))
		return
	}
	verb, rest := fields[0], fields[1:]
	ctx := r.commandContext()
	switch verb {
	case "new":
		r.cmdSessionNew(ctx, rest)
	case "show":
		r.cmdSessionShow(rest)
	case "mv", "move":
		r.cmdSessionMove(rest)
	case "rm", "delete":
		r.cmdSessionRm(ctx, rest)
	case "diff":
		r.cmdSessionDiff(ctx, rest)
	case "merge":
		r.cmdSessionMerge(ctx, rest)
	case "revert":
		r.cmdSessionRevert(ctx, rest)
	default:
		r.outln("usage: /session <new|show|mv|rm|diff|merge|revert> …")
	}
}

// cmdSessionNew creates a detached session (carving a worktree in a repo)
// and reports the id so it can be /resume-d.
func (r *repl) cmdSessionNew(ctx context.Context, fields []string) {
	title, project := "", ""
	var pos []string
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "--title", "-title":
			if i+1 < len(fields) {
				title = fields[i+1]
				i++
			}
		case "--project", "-project":
			if i+1 < len(fields) {
				project = fields[i+1]
				i++
			}
		default:
			pos = append(pos, fields[i])
		}
	}
	if title == "" {
		title = strings.Join(pos, " ")
	}
	if project == "" {
		project = r.activeProjectName()
	}
	sess, err := r.sessionsSt.Create(title, project)
	if err != nil {
		r.storeErr(err)
		return
	}
	if r.worktrees != nil {
		if path, err := r.worktrees.Ensure(ctx, sess.ID); err == nil {
			_ = r.sessionsSt.SetWorktree(sess.ID, path, sessions.Branch(sess.ID))
			sess, _ = r.sessionsSt.Get(sess.ID)
		}
	}
	r.outf("%s  %s\n", sess.ID, i18n.T(r.loc, "cli.session.created"))
	if sess.Project != "" {
		r.outf("project:  %s\n", sess.Project)
	}
	if sess.Worktree != "" {
		r.outf("worktree: %s  branch: %s\n", sess.Worktree, sess.Branch)
	}
	r.outln(i18n.Tf(r.loc, "repl.session.attach", "id", sess.ID))
}

// cmdSessionShow prints one session and its turns (CLI `session show`).
func (r *repl) cmdSessionShow(rest []string) {
	if len(rest) == 0 {
		r.outln("usage: /session show <id>")
		return
	}
	sess, err := r.sessionsSt.Get(rest[0])
	if err != nil {
		r.outln(i18n.Tf(r.loc, "repl.resume.bad", "id", rest[0]))
		return
	}
	r.outf("id:       %s\n", sess.ID)
	r.outf("title:    %s\n", orDash(sess.Title))
	if sess.Project != "" {
		r.outf("project:  %s\n", sess.Project)
	}
	r.outf("created:  %s\n", sess.CreatedAt.Format("2006-01-02 15:04:05"))
	r.outf("updated:  %s\n", sess.UpdatedAt.Format("2006-01-02 15:04:05"))
	if sess.Branch != "" {
		r.outf("branch:   %s\n", sess.Branch)
	}
	if sess.Worktree != "" {
		r.outf("worktree: %s\n", sess.Worktree)
	}
	if len(sess.Turns) == 0 {
		return
	}
	r.outln("turns:")
	for i, t := range sess.Turns {
		text := strings.ReplaceAll(t.Text, "\n", " ")
		if len([]rune(text)) > 120 {
			text = string([]rune(text)[:120]) + "…"
		}
		ref := ""
		if t.Ref != "" {
			ref = "  [" + t.Ref + "]"
		}
		r.outf("  %2d %-9s %s%s\n", i+1, t.Role, text, ref)
	}
}

// cmdSessionMove moves a session between projects (`/session mv <id> <project>`;
// "-" or empty disassociates it).
func (r *repl) cmdSessionMove(rest []string) {
	if len(rest) < 1 {
		r.outln("usage: /session mv <id> <project|->")
		return
	}
	id := rest[0]
	project := ""
	if len(rest) > 1 {
		project = strings.TrimSpace(strings.Join(rest[1:], " "))
	}
	if project == "-" {
		project = ""
	}
	if err := r.sessionsSt.SetProject(id, project); err != nil {
		r.storeErr(err)
		return
	}
	if project != "" {
		r.outln(i18n.Tf(r.loc, "cli.session.moved", "id", id, "project", project))
	} else {
		r.outln(i18n.Tf(r.loc, "cli.session.unassociated", "id", id))
	}
}

// cmdSessionRm removes a session and its worktree. Removing the currently
// attached session is refused rather than silently detaching — detaching is
// repl state that must be mutated on the front-end goroutine (/resume -).
func (r *repl) cmdSessionRm(ctx context.Context, rest []string) {
	if len(rest) == 0 {
		r.outln("usage: /session rm <id> [id …]")
		return
	}
	// Multiple refs delete in one pass (CLI: panda session rm a b c). The
	// attached session is refused inline, not fatal to the batch.
	for _, ref := range rest {
		id, err := resolveSessionRef(r.sessionsSt, ref)
		if err != nil {
			var amb sessionAmbiguousError
			switch {
			case errors.As(err, &amb):
				r.outln(i18n.Tf(r.loc, "cli.session.rm.ambiguous", "id", ref, "n", strconv.Itoa(len(amb.candidates))))
			case errors.Is(err, sessions.ErrNotFound):
				r.outln(i18n.Tf(r.loc, "repl.resume.bad", "id", ref))
			default:
				r.storeErr(err)
			}
			continue
		}
		if id == r.sessID() {
			r.outln(i18n.Tf(r.loc, "repl.session.rmActive", "id", id))
			continue
		}
		if r.worktrees != nil {
			_ = r.worktrees.Remove(ctx, id)
		}
		if err := r.sessionsSt.Delete(id); err != nil {
			if errors.Is(err, sessions.ErrNotFound) {
				r.outln(i18n.Tf(r.loc, "repl.resume.bad", "id", ref))
				continue
			}
			r.storeErr(err)
			continue
		}
		r.outln(i18n.Tf(r.loc, "cli.session.rm.one", "id", id))
	}
}

// cmdSessionDiff shows a session's worktree changes (CLI `session diff`).
// `--stat` prints only the merge-preview roll-up, matching the CLI flag.
func (r *repl) cmdSessionDiff(ctx context.Context, rest []string) {
	if len(rest) == 0 {
		r.outln("usage: /session diff <id> [--stat]")
		return
	}
	if r.worktrees == nil {
		r.outln(i18n.T(r.loc, "repl.session.notRepo"))
		return
	}
	// The CLI's reorderFlags lets --stat sit anywhere; the REPL parser keeps
	// the same freedom rather than demanding flag-after-id order.
	var id string
	statOnly := false
	for _, f := range rest {
		if f == "--stat" {
			statOnly = true
			continue
		}
		if id == "" {
			id = f
		}
	}
	if id == "" {
		r.outln("usage: /session diff <id> [--stat]")
		return
	}
	if st, err := r.worktrees.Stat(ctx, id); err == nil {
		r.printDiffStat(st)
	} else {
		r.storeErr(err)
		return
	}
	if statOnly {
		return
	}
	changes, err := r.worktrees.Status(ctx, id)
	if err != nil {
		r.storeErr(err)
		return
	}
	patch, err := r.worktrees.Diff(ctx, id)
	if err != nil {
		r.storeErr(err)
		return
	}
	r.outf("branch: %s\n", sessions.Branch(id))
	if len(changes) == 0 {
		r.outln(i18n.T(r.loc, "cli.session.diff.clean"))
		return
	}
	for _, c := range changes {
		r.outf("  %-2s %s\n", c.Status, c.Path)
	}
	if patch != "" {
		r.outln()
		r.outf("%s", patch)
	}
}

// printDiffStat renders the merge-preview roll-up into the REPL's transcript
// — the same numbers the CLI's printDiffStat writes to stdout.
func (r *repl) printDiffStat(st sessions.DiffStat) {
	r.outln(i18n.Tf(r.loc, "cli.session.diffstat.summary",
		"files", strconv.Itoa(st.FilesChanged),
		"ins", strconv.Itoa(st.Insertions),
		"del", strconv.Itoa(st.Deletions),
		"new", strconv.Itoa(st.Untracked)))
	if len(st.Sensitive) > 0 {
		r.outln("  " + i18n.Tf(r.loc, "cli.session.diffstat.sensitive", "paths", strings.Join(st.Sensitive, ", ")))
	}
	if len(st.Binary) > 0 {
		r.outln("  " + i18n.Tf(r.loc, "cli.session.diffstat.binary", "paths", strings.Join(st.Binary, ", ")))
	}
}

// cmdSessionMerge merges a session branch into HEAD (CLI `session merge`).
func (r *repl) cmdSessionMerge(ctx context.Context, rest []string) {
	if len(rest) == 0 {
		r.outln("usage: /session merge <id> [--message M]")
		return
	}
	if r.worktrees == nil {
		r.outln(i18n.T(r.loc, "repl.session.notRepo"))
		return
	}
	id := rest[0]
	message := ""
	for i := 1; i < len(rest); i++ {
		if (rest[i] == "--message" || rest[i] == "-m") && i+1 < len(rest) {
			message = rest[i+1]
			i++
		}
	}
	// Same preview the CLI prints: what lands, and whether any of it is
	// credential-shaped, before it lands.
	if st, serr := r.worktrees.Stat(ctx, id); serr == nil {
		r.printDiffStat(st)
	}
	subject, err := r.worktrees.Merge(ctx, id, message)
	if err != nil {
		if errors.Is(err, sessions.ErrMergeConflict) {
			r.errf("panda: %v\n", err)
			return
		}
		r.storeErr(err)
		return
	}
	r.outf("merged %s: %s\n", sessions.Branch(id), subject)
}

// cmdSessionRevert undoes a recorded session merge (CLI `session revert`):
// head-on-the-merge resets cleanly, later work gets a revert commit.
func (r *repl) cmdSessionRevert(ctx context.Context, rest []string) {
	if len(rest) == 0 {
		r.outln("usage: /session revert <id>")
		return
	}
	if r.worktrees == nil {
		r.outln(i18n.T(r.loc, "repl.session.notRepo"))
		return
	}
	id := rest[0]
	subject, err := r.worktrees.Revert(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, sessions.ErrNothingToRevert):
			r.outln(i18n.Tf(r.loc, "cli.session.revert.none", "id", id))
		case errors.Is(err, sessions.ErrRevertDirty):
			r.outln(i18n.T(r.loc, "cli.session.revert.dirty"))
		default:
			r.storeErr(err)
		}
		return
	}
	r.outln(i18n.Tf(r.loc, "cli.session.revert.done", "id", sessions.Branch(id), "subject", subject))
}

// cmdTaskRevert implements `/task revert <id>` — the REPL twin of
// `panda task revert`: roll the run's git checkout back to the baseline the
// executor saved before the run began, and report what that touched.
func (r *repl) cmdTaskRevert(arg string) {
	id := strings.TrimSpace(arg)
	if id == "" {
		r.outln("usage: /task revert <id>")
		return
	}
	id, ok := r.resolveRef(id)
	if !ok {
		return
	}
	t, err := r.store.Get(r.commandContext(), id)
	if err != nil {
		r.storeErr(err)
		return
	}
	var workPath string
	r.readConfig(func(c *config.Config) { workPath = c.Storage.WorkPath })
	seen := map[string]bool{}
	for _, dir := range []string{t.ExecWorkDir, t.WorkDir, workPath} {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		restored, removed, rerr := defense.RollbackGitBaseline(dir, id)
		if errors.Is(rerr, defense.ErrNoGitBaseline) {
			continue
		}
		if rerr != nil {
			r.storeErr(rerr)
			return
		}
		r.outln(i18n.Tf(r.loc, "cli.task.revert.done",
			"id", id, "restored", strconv.Itoa(len(restored)), "removed", strconv.Itoa(len(removed))))
		return
	}
	r.outln(i18n.Tf(r.loc, "cli.task.revert.none", "id", id))
}

// cmdTaskFollowup implements `/task followup <id>` — feeds a task's recorded
// result back to the model as the next ask, so "here is what the task did,
// let's continue" is one keystroke instead of copy-pasting `/task <id>`
// output into a prompt. It works on any state: a still-running task carries
// its state and whatever partial result exists.
func (r *repl) cmdTaskFollowup(arg string) {
	ref := strings.TrimSpace(arg)
	if ref == "" {
		r.outln("usage: /task followup <id>")
		return
	}
	id, ok := r.resolveRef(ref)
	if !ok {
		return
	}
	t, err := r.store.Get(r.commandContext(), id)
	if err != nil {
		r.storeErr(err)
		return
	}
	r.askMode(taskFollowupText(r.loc, t), "")
}

// taskResultDigestLimit bounds how much of a task's result travels inside a
// follow-up prompt: enough for the model to see the outcome and the output
// tail, small enough not to crowd the context.
const taskResultDigestLimit = 1200

// taskResultDigest compresses a task's result_json into the few-line digest a
// follow-up prompt carries: the outcome word, the relevant output tail and
// the changed-files roll-up. Empty when no result was recorded. A payload
// that does not decode still returns its raw prefix — the model can work
// with odd output, and silently dropping it would lose the signal entirely.
func taskResultDigest(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var res struct {
		ExitCode     int      `json:"exit_code"`
		OK           bool     `json:"ok"`
		Stdout       string   `json:"stdout"`
		Stderr       string   `json:"stderr"`
		Failed       string   `json:"failed"`
		FilesChanged []string `json:"files_changed"`
		Question     string   `json:"question"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return truncate(raw, taskResultDigestLimit)
	}
	var b strings.Builder
	switch {
	case res.Failed != "":
		b.WriteString("failed: " + res.Failed)
	case res.OK:
		b.WriteString("ok")
	default:
		fmt.Fprintf(&b, "exit %d", res.ExitCode)
	}
	if res.Question != "" {
		b.WriteString("\nquestion: " + res.Question)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		out = strings.TrimSpace(res.Stderr)
	}
	if out != "" {
		b.WriteString("\n" + out)
	}
	if n := len(res.FilesChanged); n > 0 {
		b.WriteString(fmt.Sprintf("\n%d file(s) changed", n))
		if n <= 8 {
			b.WriteString(": " + strings.Join(res.FilesChanged, ", "))
		}
	}
	return truncate(b.String(), taskResultDigestLimit)
}

// taskFollowupText builds the model-facing prompt for "continue from this
// task": identity, state and a compact result digest, then the ask itself.
// The instruction stays outcome-neutral — summarize and flag anything worth
// attention — so it reads sensibly whether the task succeeded, failed or was
// cancelled mid-flight.
func taskFollowupText(loc i18n.Locale, t core.Task) string {
	result := taskResultDigest(t.ResultJSON)
	if result == "" {
		result = i18n.T(loc, "cli.task.followup.noResult")
	}
	title := t.Title
	if title == "" {
		title = t.TaskID
	}
	return i18n.Tf(loc, "cli.task.followup.prompt",
		"id", t.TaskID, "title", title, "state", t.State, "result", result)
}

// cmdNodesRemove implements `/nodes remove <id>` — drops a stale row from the
// capability directory, with the same refusals as `panda nodes remove`:
// never the self row, never an online node.
func (r *repl) cmdNodesRemove(arg string) {
	id := strings.TrimSpace(arg)
	if id == "" {
		r.outln("usage: /nodes remove <id>")
		return
	}
	if r.cfg != nil && id == core.RuntimeNodeID(r.cfg.Node.Name, r.cfg.Node.Kind, r.cfg.Node.EffectiveIdentity()) {
		r.outln(i18n.T(r.loc, "cli.nodes.self"))
		return
	}
	nodes, err := ledger.Query(r.db, "", "")
	if err != nil {
		r.storeErr(err)
		return
	}
	for _, n := range nodes {
		if n.ID != id {
			continue
		}
		if n.Status == "online" {
			r.outln(i18n.Tf(r.loc, "cli.nodes.online", "id", id))
			return
		}
		if _, err := ledger.Remove(r.db, id); err != nil {
			r.storeErr(err)
			return
		}
		r.outln(i18n.Tf(r.loc, "cli.nodes.removed", "id", id))
		return
	}
	r.outln(i18n.Tf(r.loc, "cli.nodes.none", "id", id))
}

// cmdNodesVerify implements `/nodes verify <id>` — the REPL twin of
// `panda nodes verify`: stamp the fingerprint the operator compared
// out-of-band so the TOFU line between "a hello claimed this" and "I
// checked" is kept honest.
func (r *repl) cmdNodesVerify(arg string) {
	id := strings.TrimSpace(arg)
	if id == "" {
		r.outln("usage: /nodes verify <id>")
		return
	}
	nodes, err := ledger.Query(r.db, "", "")
	if err != nil {
		r.storeErr(err)
		return
	}
	for _, n := range nodes {
		if n.ID != id {
			continue
		}
		fp := n.Fingerprint()
		if fp == "" {
			r.outln(i18n.Tf(r.loc, "cli.nodes.verify.nokey", "id", id))
			return
		}
		ok, err := ledger.MarkVerified(r.db, id)
		if err != nil {
			r.storeErr(err)
			return
		}
		if !ok {
			r.outln(i18n.Tf(r.loc, "cli.nodes.verify.nokey", "id", id))
			return
		}
		r.outln(i18n.Tf(r.loc, "cli.nodes.verify.done", "id", id, "fp", fp))
		return
	}
	r.outln(i18n.Tf(r.loc, "cli.nodes.none", "id", id))
}

// cmdNodesTrust implements `/nodes trust <id> [level]` — the REPL twin of
// `panda nodes trust` (experimental trust zones). No level reports the row's
// pinned tier; with one it pins it on the directory row — live, since the
// engine reads the table on every admission/exposure decision — and keeps a
// configured network.peers entry in step so a config reload cannot drop the
// assertion.
func (r *repl) cmdNodesTrust(arg string) {
	fields := strings.Fields(arg)
	if len(fields) < 1 || len(fields) > 2 {
		r.outln("usage: /nodes trust <id> [mesh|guarded|open]")
		return
	}
	id := fields[0]
	level, haveLevel := "", false
	if len(fields) == 2 {
		lv, err := trust.ParseLevel(fields[1])
		if err != nil {
			r.outln(err.Error())
			return
		}
		level = trust.StoredLevel(lv) // mesh stores as "" (the absence of a zone)
		haveLevel = true
	}
	// A "k:"-prefixed id is the stable Ed25519 identity: pin (or report the
	// merged pin of) every sibling row holding that key.
	if strings.HasPrefix(id, "k:") {
		pub := strings.TrimPrefix(id, "k:")
		if !haveLevel {
			cur := ledger.PeerTrustByKey(r.db, pub)
			if cur == "" {
				cur = string(trust.Mesh)
			}
			r.outln(i18n.Tf(r.loc, "cli.nodes.trust.show", "id", id, "level", cur))
			return
		}
		n, serr := ledger.SetPeerTrustByKey(r.db, pub, level)
		if serr != nil {
			r.storeErr(serr)
			return
		}
		if n == 0 {
			r.outln(i18n.Tf(r.loc, "cli.nodes.none", "id", id))
			return
		}
		// Same config sync the id-form below does: a bound peer_addrs address
		// that appears in network.peers carries the tier into the file, so a
		// reload cannot re-assert a stale stricter hint over the raise.
		_ = r.mutateConfigErr(func(c *config.Config) error {
			return syncPeerTrustConfig(r.db, c, configWritePath(r.configPath), peerKeyRowIDs(r.db, pub), level)
		})
		r.outln(i18n.Tf(r.loc, "cli.nodes.trust.done", "id", id, "level", displayLevel(level)))
		return
	}
	nodes, err := ledger.Query(r.db, "", "")
	if err != nil {
		r.storeErr(err)
		return
	}
	found := ""
	for _, n := range nodes {
		if n.ID == id || scheduler.NodeNamePart(n.ID) == id {
			found = n.ID
			break
		}
	}
	if found == "" {
		r.outln(i18n.Tf(r.loc, "cli.nodes.none", "id", id))
		return
	}
	if !haveLevel {
		cur, _ := ledger.PeerTrust(r.db, found)
		if cur == "" {
			cur = string(trust.Mesh)
		}
		r.outln(i18n.Tf(r.loc, "cli.nodes.trust.show", "id", found, "level", cur))
		return
	}
	if err := ledger.SetPeerTrust(r.db, found, level); err != nil {
		r.storeErr(err)
		return
	}
	// Same config sync the CLI verb does: a bound peer_addrs address that
	// appears in network.peers carries the tier into the file, so SIGHUP
	// reload resurrects nothing stale.
	_ = r.mutateConfigErr(func(c *config.Config) error {
		return syncPeerTrustConfig(r.db, c, configWritePath(r.configPath), map[string]bool{found: true}, level)
	})
	r.outln(i18n.Tf(r.loc, "cli.nodes.trust.done", "id", found, "level", displayLevel(level)))
}

// cmdNodesAdmit implements `/nodes admit <id>` — resolve a LAN-discovered
// pending row into its advertised address and run it through the same
// add-and-dial path `/nodes add` uses (live dial when the engine is up), then
// forget the pending row.
func (r *repl) cmdNodesAdmit(arg string) {
	fields := strings.Fields(arg)
	id, rest := "", fields
	if len(fields) > 0 {
		id, rest = fields[0], fields[1:]
	}
	if id == "" {
		r.outln("usage: /nodes admit <id> [--trust level]")
		return
	}
	pending, err := ledger.ListPending(r.db, 90*time.Second)
	if err != nil {
		r.storeErr(err)
		return
	}
	for _, p := range pending {
		if p.ID != id {
			continue
		}
		// Forward any trailing `--trust <level>` (experimental trust zones):
		// cmdNodesAdd parses it off the argument and pins the tier on the
		// appended entry, same as `panda nodes admit --trust`.
		r.cmdNodesAdd(strings.TrimSpace(p.Addr + " " + strings.Join(rest, " ")))
		_ = ledger.ForgetPending(r.db, id)
		if !p.Verified {
			r.outln(i18n.Tf(r.loc, "cli.nodes.admit.unverified", "id", id))
		}
		return
	}
	r.outln(i18n.Tf(r.loc, "cli.nodes.admit.none", "id", id))
}

// cmdTaskAdd implements `/task add <title>` with the optional flags the CLI
// takes (--prompt/--priority/--project/--requires/--preferred/--authorize,
// and --agents/--mode for the multi-harness plan form). It needs the ask
// engine's scheduler core — same precondition as `panda task add`.
func (r *repl) cmdTaskAdd(fields []string) {
	if r.engine.Load() == nil {
		r.outln(i18n.T(r.loc, "repl.approval.noEngine"))
		return
	}
	var title, prompt, priority, project, requires, agents, mode, preferred, actionSpec string
	authorize := false
	wait := false
	waitTimeout := 30 * time.Minute
	var pos []string
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		val := func() string {
			if i+1 < len(fields) {
				i++
				return fields[i]
			}
			return ""
		}
		switch strings.TrimLeft(f, "-") {
		case "title", "t":
			title = val()
		case "prompt":
			prompt = val()
		case "priority":
			priority = val()
		case "project":
			project = val()
		case "requires":
			requires = val()
		case "agents":
			agents = val()
		case "mode":
			mode = val()
		case "preferred":
			preferred = val()
		case "action-spec", "actionSpec":
			actionSpec = val()
		case "wait":
			wait = true
		case "wait-timeout":
			if d, err := time.ParseDuration(val()); err == nil && d > 0 {
				waitTimeout = d
			}
		case "authorize":
			authorize = true
		default:
			if !strings.HasPrefix(f, "-") {
				pos = append(pos, f)
			}
		}
	}
	if title == "" {
		title = strings.Join(pos, " ")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		r.outln(i18n.T(r.loc, "cli.task.add.noTitle"))
		return
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = title
	}
	if priority == "" {
		priority = "normal"
	}
	prio, ok := parseCLIPriority(priority)
	if !ok {
		r.outln(i18n.Tf(r.loc, "cli.task.add.badPriority", "level", priority, "list", cliPriorities))
		return
	}
	requiresList := []string{}
	for _, req := range strings.Split(requires, ",") {
		if req = strings.TrimSpace(req); req != "" {
			requiresList = append(requiresList, req)
		}
	}
	if len(requiresList) == 0 {
		requiresList = []string{"coding"}
	}
	ctx := r.commandContext()

	agentList := parseAgentList(agents)
	if wait && len(agentList) > 1 {
		r.outln(i18n.T(r.loc, "cli.task.wait.multi"))
		return
	}
	if len(agentList) > 1 {
		if strings.TrimSpace(actionSpec) != "" {
			r.outln(i18n.T(r.loc, "cli.task.add.actionSpecAgents"))
			return
		}
		if mode == "" {
			mode = "parallel"
		}
		p, err := buildMultiAgentPlan(agentList, mode, title, prompt, requiresList, requires != "")
		if err != nil {
			if errors.Is(err, errBadAgentMode) {
				r.outln(i18n.Tf(r.loc, "cli.task.add.badMode", "mode", mode))
				return
			}
			r.storeErr(err)
			return
		}
		q := core.DefaultQueueSpec()
		q.Priority = prio
		planID, err := r.engine.Load().StartPlan(ctx, p, q)
		if err != nil {
			r.storeErr(err)
			return
		}
		stages, serr := r.engine.Load().PlanStages(ctx, planID)
		if serr != nil {
			r.storeErr(serr)
			return
		}
		r.outln(i18n.Tf(r.loc, "cli.task.add.plan", "id", planID, "stages", strconv.Itoa(len(stages)), "mode", mode))
		printPlanStagesTo(r.commandOutput(), stages)
		if !queueConsumerAlive(r.cfg) {
			warnNoConsumerTo(r.commandOutput(), r.loc)
		}
		return
	}
	if len(agentList) == 1 {
		requiresList = append(requiresList, "agent:"+agentList[0])
	}
	// --action-spec carries the §7.2 actuator dispatch, same contract as the
	// CLI flag: parsed into the wire type here so a malformed spec fails in
	// the REPL, not at the executor.
	contextType := ""
	specJSON := ""
	if raw := strings.TrimSpace(actionSpec); raw != "" {
		var as ledger.ActionSpec
		if err := json.Unmarshal([]byte(raw), &as); err != nil {
			r.outln(i18n.Tf(r.loc, "cli.task.add.badActionSpec", "err", err.Error()))
			return
		}
		if as.TargetActuator == "" || as.Action == "" {
			r.outln(i18n.T(r.loc, "cli.task.add.actionSpecMissing"))
			return
		}
		detail := struct {
			ActionSpec ledger.ActionSpec `json:"action_spec"`
		}{ActionSpec: as}
		b, _ := json.Marshal(detail)
		specJSON = string(b)
		contextType = "hardware"
		requiresList = ledger.RequiresForActionSpec(requiresList, &as)
	}
	in := core.TaskInput{
		Title:         title,
		Project:       project,
		ContextType:   contextType,
		Intent:        prompt,
		SpecJSON:      specJSON,
		Requires:      requiresList,
		PreferredNode: strings.TrimSpace(preferred),
		Authorized:    authorize,
	}
	q := core.DefaultQueueSpec()
	q.Priority = prio
	task, err := r.engine.Load().EnqueueTask(ctx, in, q)
	if err != nil {
		r.storeErr(err)
		return
	}
	sessionID := ""
	if r.sessionsSt != nil {
		if sess, serr := r.sessionsSt.Create(title); serr == nil {
			sessionID = sess.ID
			_, _ = r.sessionsSt.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: prompt})
			_ = r.store.SetSessionID(ctx, task.TaskID, sess.ID)
		}
	}
	r.outln(i18n.Tf(r.loc, "cli.task.add.done", "id", task.TaskID, "state", task.State, "priority", priorityName(prio)))
	if sessionID != "" {
		r.outln(i18n.Tf(r.loc, "cli.task.add.session", "id", sessionID))
	}
	if !queueConsumerAlive(r.cfg) {
		warnNoConsumerTo(r.commandOutput(), r.loc)
	}
	if wait {
		// Same watch-only contract as `panda task add --wait`: the command
		// context ending (REPL quit) stops the watch, never the task.
		r.outln(i18n.Tf(r.loc, "cli.task.waiting", "id", task.TaskID))
		wctx, wcancel := context.WithTimeout(ctx, waitTimeout)
		defer wcancel()
		final, werr := waitTask(wctx, r.store, task.TaskID)
		switch {
		case errors.Is(werr, context.DeadlineExceeded):
			r.outln(i18n.Tf(r.loc, "cli.task.wait.timeout",
				"id", task.TaskID, "state", final.State, "dur", waitTimeout.String()))
		case werr != nil:
			r.outln(i18n.Tf(r.loc, "cli.task.wait.interrupted", "id", task.TaskID))
		default:
			r.outln(i18n.Tf(r.loc, "cli.task.wait.state", "id", final.TaskID, "state", final.State))
			if final.State == core.StateReview {
				r.outln(i18n.Tf(r.loc, "cli.task.wait.review", "id", final.TaskID))
			}
		}
	}
}

// cmdTaskPriority implements `/task priority <id> <level>`.
func (r *repl) cmdTaskPriority(rest []string) {
	if len(rest) != 2 {
		r.outln("usage: /task priority <id> <" + cliPriorities + ">")
		return
	}
	prio, ok := parseCLIPriority(rest[1])
	if !ok {
		r.outln(i18n.Tf(r.loc, "cli.task.add.badPriority", "level", rest[1], "list", cliPriorities))
		return
	}
	id, ok := r.resolveRef(rest[0])
	if !ok {
		return
	}
	if err := r.store.SetPriority(r.commandContext(), id, prio); err != nil {
		r.storeErr(err)
		return
	}
	r.outln(i18n.Tf(r.loc, "cli.task.priority.done", "id", id, "priority", priorityName(prio)))
}

// cmdTaskMove implements `/task move <id> <seq>` — the drag-sort order the
// queue scheduler honors before priority.
func (r *repl) cmdTaskMove(rest []string) {
	if len(rest) != 2 {
		r.outln("usage: /task move <id> <seq>")
		return
	}
	seq, err := strconv.ParseInt(rest[1], 10, 64)
	if err != nil {
		r.outf("panda: seq must be an integer, got %q\n", rest[1])
		return
	}
	id, ok := r.resolveRef(rest[0])
	if !ok {
		return
	}
	if err := r.store.SetSeq(r.commandContext(), id, seq); err != nil {
		r.storeErr(err)
		return
	}
	r.outln(i18n.Tf(r.loc, "cli.task.move.done", "id", id, "seq", rest[1]))
}
