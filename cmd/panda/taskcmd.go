// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda task` grew from a one-shot show into the task verb family: show
// stays the default, and add/priority/move join approve/reject/cancel/logs
// (which keep their bare-command forms too). add is the kernel-form of the
// board's POST /api/tasks — same enqueue + linked-session semantics.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// runTask dispatches `panda task`: a known verb routes to its handler,
// anything else is the legacy `panda task <id>` show form.
func runTask(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "add", "new", "create":
			runTaskAdd(args[1:])
			return
		case "priority":
			runTaskPriority(args[1:])
			return
		case "move":
			runTaskMove(args[1:])
			return
		case "approve":
			runApprove(args[1:])
			return
		case "reject":
			runReject(args[1:])
			return
		case "cancel":
			runCancel(args[1:])
			return
		case "delete", "del", "rm":
			runTaskDelete(args[1:])
			return
		case "revert":
			runTaskRevert(args[1:])
			return
		case "followup":
			runTaskFollowup(args[1:])
			return
		case "send", "msg", "message":
			runTaskSend(args[1:])
			return
		case "continue":
			runTaskContinue(args[1:])
			return
		case "board":
			runTaskBoard(args[1:])
			return
		case "artifact":
			runTaskArtifact(args[1:])
			return
		case "logs":
			runLogs(args[1:])
			return
		case "show":
			runTaskShow(args[1:])
			return
		case "verdict":
			runTaskVerdict(args[1:])
			return
		case "help", "-h", "--help":
			taskUsage()
			return
		}
	}
	runTaskShow(args)
}

func taskUsage() {
	fmt.Fprintln(os.Stderr, "usage: panda task <verb|task-id>")
	fmt.Fprintln(os.Stderr, "  <id>                                    show one task and its timeline")
	fmt.Fprintln(os.Stderr, "  add --title T [--prompt P] [--priority "+cliPriorities+"]")
	fmt.Fprintln(os.Stderr, "      [--project p] [--parent-id ID] [--preferred NODE] [--authorize] [--card PATH]")
	fmt.Fprintln(os.Stderr, "      [--paths d1,d2 | --isolate] [--agents a,b [--mode m] | --nodes n1,n2]")
	fmt.Fprintln(os.Stderr, "      [--accept CMD|auto ...] [--wait [--wait-timeout D]] [--json]   enqueue a task")
	fmt.Fprintln(os.Stderr, "  priority <id> <level>                   change a task's priority")
	fmt.Fprintln(os.Stderr, "  move <id> <seq>                         reorder the drag-sort queue")
	fmt.Fprintln(os.Stderr, "  <id> --trace                            hop-by-hop delegation trace (hops, timings, transport)")
	fmt.Fprintln(os.Stderr, "  delete <id>                             remove a task and its subtree (queued/finished)")
	fmt.Fprintln(os.Stderr, "  verdict <id>                            show the acceptance checks and the contract verdict")
	fmt.Fprintln(os.Stderr, "  revert <id>                             roll the run's git checkout back to its pre-task baseline")
	fmt.Fprintln(os.Stderr, "  followup <id>                           hand the task's recorded result to the model and continue")
	fmt.Fprintln(os.Stderr, "  send [--inject] <id> <text>             message a running/queued task: queue (next-round context) or inject (leads the next round)")
	fmt.Fprintln(os.Stderr, "  continue <id> [-m NOTE]                 requeue a follow-up-parked task so it keeps working")
	fmt.Fprintln(os.Stderr, "  board <id>                              show the sibling-message boards a task reads")
	fmt.Fprintln(os.Stderr, "  artifact <id> [--list|--file P|--out D]  fetch the task's output artifact (list, preview, or extract)")
	fmt.Fprintln(os.Stderr, "  approve|reject|cancel|logs <id>         same as the bare commands")
}

// runTaskShow is the legacy `panda task <id>` — one task's full row plus
// event timeline. --trace switches the record view for the derived
// hop-by-hop delegation trace (Track 3).
func runTaskShow(args []string) {
	fs := flag.NewFlagSet("task", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	trace := fs.Bool("trace", false, "show the hop-by-hop delegation trace instead of the task record")
	full := fs.Bool("full", false, "print raw event payloads without terminal truncation")
	fs.Parse(reorderFlags(fs, args))
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		taskUsage()
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	id = resolveTaskRef(store, id)
	t, err := store.Get(context.Background(), id)
	if err != nil {
		taskStoreFatal(err, id)
	}

	if *trace {
		events, err := store.Events(context.Background(), id)
		if err != nil {
			fatal("load events", err)
		}
		metrics, err := store.DelegationMetricsFor(context.Background(), id)
		if err != nil {
			fatal("load delegation metrics", err)
		}
		if jsonOutput {
			emitJSON(traceJSON(t, events, metrics))
			return
		}
		printTaskTrace(t, events, metrics)
		return
	}

	if jsonOutput {
		emitJSON(taskToJSONWithStore(store, t))
		return
	}

	// One field printer for the whole record: the labels line up (they did not —
	// "complexity:" is a column wider than the format string allowed for), and a
	// multi-line value hangs under its own label instead of starting at column
	// zero, where a long intent used to read as the end of the record.
	taskField("id", t.TaskID)
	taskField("parent", orDash(t.ParentID))
	// Sub-agent visibility: a task that fanned out shows its children with
	// their live states — the line between "one agent running" and "the
	// parent is orchestrating N sub-agents in parallel" should be visible
	// from the record itself, not only inside --trace events.
	kids, kerr := store.Children(context.Background(), t.TaskID)
	// Spawn depth matters only inside a sub-agent tree — a root that fanned
	// out (depth 0 with children) and every delegated child show it; a
	// standalone task stays quiet.
	if kerr == nil && (len(kids) > 0 || t.SpawnDepth > 0) {
		taskField("depth", strconv.Itoa(t.SpawnDepth))
	}
	if kerr == nil && len(kids) > 0 {
		var lines []string
		for _, k := range kids {
			lines = append(lines, fmt.Sprintf("%s  [%s]  %s", k.TaskID, colorState(k.State), k.Title))
		}
		taskField("children", strings.Join(lines, "\n"))
	}
	taskField("project", orDash(t.Project))
	// Parallel-mode visibility: how this task relates to the shared tree it
	// runs against — an isolated copy that merges back, or the path lanes it
	// claims inside the shared worktree. Unscoped tasks stay quiet (they
	// hold the whole-project lock, which is the default everyone knows).
	if t.Isolate {
		taskField("mode", "isolated (private copy → merge back)")
	} else if len(t.ScopePaths) > 0 {
		taskField("scope", strings.Join(t.ScopePaths, ", "))
	}
	taskField("title", t.Title)
	stateLabel := colorState(t.State)
	// A dispatched row with a parked outbox delivery is not on a wire — it is
	// in this node's mailbox awaiting the target's next connect. Say so:
	// "dispatched" reads as running, which is precisely the lie that made
	// parked pins look like work in flight.
	if t.State == core.StateDispatched && store.TaskOutboxPending(context.Background(), t.TaskID) {
		stateLabel += " (" + i18n.T(i18n.Detect(), "cli.task.waiting_link") + ")"
	}
	taskField("state", stateLabel)
	if t.Verification != "" {
		taskField("verification", t.Verification)
	}
	taskField("priority", priorityName(t.Priority))
	taskField("owner", t.OwnerNode)
	// Owner is the lease holder — the node the queue/store believes drives the
	// task. The EXECUTOR is whichever node the latest delegation targeted; on
	// a forwarded task those differ, and showing only the owner made every
	// remotely-executed task look local.
	if target, terr := store.DispatchTarget(context.Background(), t.TaskID); terr == nil &&
		target != "" && target != t.OwnerNode {
		taskField("executor", target)
	}
	// Where the work happened — the first question a produced file raises.
	// This node's own derived directory wins; a delegated result carries the
	// executor's directory on the wire (recorded on the row too, for the
	// queue board), so the origin can locate the output on the machine that
	// made it; the submitter's pin is the fallback before any run has
	// derived one.
	workDir, workDirOn := t.ExecWorkDir, ""
	if dir, exec, ok := resultWorkDir(t.ResultJSON); ok &&
		(workDir == "" || workDir == dir) && exec != "" && exec != t.OwnerNode {
		// The row's field and the result agree — annotate the node that ran
		// it, which the bare path cannot say.
		workDir, workDirOn = dir, exec
	}
	if workDir == "" {
		workDir = t.WorkDir
	}
	if workDir != "" {
		val := workDir
		if workDirOn != "" && workDirOn != t.OwnerNode {
			val += " " + pal().Separator() + " " + workDirOn
		}
		taskField("workdir", val)
	}
	// The events are loaded once here so the field block can annotate a
	// route_fallback (meant for a peer, ran locally) alongside the timeline.
	events, err := store.Events(context.Background(), id)
	if err != nil {
		fatal("load events", err)
	}
	if fb := latestRouteFallback(events); fb != "" {
		taskField("route", fb)
	}
	if pin := core.PinnedNode(t); pin != "" {
		taskField("pinned", pin)
	}
	// The soft hint rides its own line, never "pinned": it constrains nothing
	// and a miss leaves a route_fallback rather than a failure. Showing it is
	// what makes "prefer my laptop, but run it anywhere" auditable.
	if pref := core.PreferredNode(t); pref != "" {
		taskField("preferred", pref)
	}
	taskField("attempt", t.AttemptID)
	taskField("chain", strings.Join(t.Chain, " "+pal().MarkArrow()+" "))
	taskField("created", ts(t.CreatedAt))
	taskField("updated", ts(t.UpdatedAt))
	if t.SessionID != "" {
		taskField("session", t.SessionID)
	}
	if t.ContextType != "" {
		taskField("context", t.ContextType)
	}
	if t.Intent != "" {
		taskField("intent", t.Intent)
	}
	if t.SpecJSON != "" {
		printTaskSpec(t.SpecJSON)
	}
	if t.Risk != "" {
		taskField("risk", t.Risk)
	}
	if t.Complexity != 0 {
		taskField("complexity", fmt.Sprintf("%.2f", t.Complexity))
	}
	if t.ResultJSON != "" {
		printTaskResult(t.ResultJSON)
	}

	if len(events) > 0 {
		fmt.Println(pal().Heading("events:"))
		printEventTimelineFull(events, "  ", *full)
	}
	// The hint prints the command the user must type next, so it spells the
	// task by the same short ref the board shows — a full UUID invites a
	// copy-failure when the prefix would have resolved anyway.
	ref := refOr(taskRefsFor(context.Background(), store, []core.Task{t}), t.TaskID)
	if t.State == core.StateReview {
		if reason := latestReviewReason(events); reason != "" {
			fmt.Println(pal().Heading(i18n.T(i18n.Detect(), "cli.task.review.reason")+":") + " " + reason)
		}
	}
	if hint := taskNextStepHint(i18n.Detect(), cfg, t, ref); hint != "" {
		fmt.Println(pal().Muted(hint))
	}
}

// taskNextStepHint names the one command that moves a task out of its current
// parked state — or "" when nothing is waiting on the user. `task show` reads
// like a dead end without it: a review row's events end at "review" and the
// reader has to guess that `panda approve` is the way out.
//
// waiting_context is an agent's clarification question — the same park, the
// same resume verb; the stored question itself was already printed under
// result:. A queued row with no live consumer is the silent-stall shape the
// queue listing already warns about; show it here too. ref is the typeable
// task reference (the board's short prefix), not the full UUID.
func taskNextStepHint(loc i18n.Locale, cfg *config.Config, t core.Task, ref string) string {
	switch t.State {
	case core.StateReview:
		// A failure-parked review carries needs_changed_input, and the store
		// refuses approve for it. Telling the user to approve was a dead end;
		// name the only path that works.
		if t.ApprovalDisposition == core.ApprovalNeedsChangedInput {
			return i18n.Tf(loc, "cli.task.wait.resubmit", "id", ref)
		}
		return i18n.Tf(loc, "cli.task.wait.review", "id", ref)
	case core.StateWaitingCtx:
		return i18n.Tf(loc, "cli.ask.question.hint", "id", ref)
	case core.StateQueued, core.StateSubmitted:
		if cfg != nil && !queueConsumerAlive(cfg) {
			return i18n.T(loc, "cli.queue.noConsumer")
		}
	}
	return ""
}

// taskReviewHint names the command that can actually move a review row.
// A "needs changed input" park refuses `panda approve` by design — telling
// the user to approve it sent them into a dead end. The resubmit path is
// `panda task continue`, which carries the corrected note back into the run.
func taskReviewHint(loc i18n.Locale, t core.Task, ref string) string {
	if t.ApprovalDisposition == core.ApprovalNeedsChangedInput {
		return i18n.Tf(loc, "cli.task.review.needsInput", "id", ref)
	}
	return i18n.Tf(loc, "cli.task.wait.review", "id", ref)
}

// latestReviewReason returns the full reason carried by the newest review
// event. Event values in the timeline are width-truncated; the reason a task
// parked is the one line a user must be able to read whole.
func latestReviewReason(events []core.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != core.EvReview {
			continue
		}
		var data map[string]any
		if json.Unmarshal([]byte(events[i].DataJSON), &data) != nil {
			continue
		}
		for _, key := range []string{"reason", "failed", "stderr", "question"} {
			if v, ok := data[key].(string); ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	return ""
}

// taskFieldWidth is the label column of the task record, sized to its longest
// label ("complexity:") plus a space.
const taskFieldWidth = 12

// taskField prints one label/value line of the task record, hanging a multi-line
// value under the label so the record stays one readable block.
func taskField(label, value string) {
	head := pal().Muted(cell(label+":", taskFieldWidth))
	lines := strings.Split(strings.TrimRight(value, "\n"), "\n")
	fmt.Println(head + lines[0])
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			fmt.Println() // a blank line in the value stays blank, not 12 spaces
			continue
		}
		fmt.Println(strings.Repeat(" ", taskFieldWidth) + l)
	}
}

// printTaskTrace renders `panda task <id> --trace`: the task's path across
// nodes derived from the event log (Track 3). Each delegate event opens a
// hop — holder → target; the events until the next delegate belong to that
// hop and print with +Δ from its start, so "how long did the task sit on
// node X" reads directly. delegation_metrics rows append the per-hop
// executor timing the scheduler itself learned from.
func printTaskTrace(t core.Task, events []core.Event, metrics []core.DelegationMetric) {
	p := pal()
	chain := t.Chain
	if len(chain) == 0 && t.OwnerNode != "" {
		chain = []string{t.OwnerNode}
	}
	transport := t.Transport
	if transport == "" {
		transport = "live" // pre-transport-field rows only ever ran live
	}
	fmt.Println(p.Heading("trace:"))
	fmt.Printf("  %s   %s\n", strings.Join(chain, " "+p.MarkArrow()+" "), p.Muted("transport="+transport))

	holder := t.OwnerNode
	if len(t.Chain) > 0 {
		holder = t.Chain[0]
	}
	hop := 0
	var hopStart int64
	for _, e := range events {
		if e.Type == core.EvDelegate {
			hop++
			hopStart = e.TS
			target := orDash(traceEventField(e.DataJSON, "target"))
			by := traceEventField(e.DataJSON, "by")
			when := time.Unix(e.TS, 0).Format("01-02 15:04:05")
			if by != "" {
				when += "  by=" + by
			}
			fmt.Printf("  hop %-3d %-42s %s\n", hop, holder+" "+p.MarkArrow()+" "+target, p.Muted(when))
			holder = target
			continue
		}
		line := eventLine(e, "         ")
		if hopStart > 0 && e.TS >= hopStart {
			line += p.Muted(fmt.Sprintf("  +%ds", e.TS-hopStart))
		}
		fmt.Println(line)
	}
	if len(events) == 0 {
		fmt.Println("  " + p.Muted("(no events recorded)"))
	}
	for _, m := range metrics {
		outcome := "ok"
		if !m.Success {
			outcome = "failed"
		}
		extra := ""
		if m.Tokens.Valid {
			extra += fmt.Sprintf("  tokens=%d", m.Tokens.Int64)
		}
		fmt.Printf("  %s %s %s %s  %dms %s%s\n",
			p.Muted("metric"), shortNode(m.Delegator), p.MarkArrow(), shortNode(m.Executor),
			m.LatencyMs, outcome, p.Muted(extra))
	}
}

// traceEventField extracts one string field from an event's JSON payload;
// missing keys and malformed payloads both read as "" rather than breaking
// the trace on old or odd rows.
func traceEventField(dataJSON, key string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(dataJSON), &obj); err != nil {
		return ""
	}
	var s string
	if raw, ok := obj[key]; ok && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// traceJSON is the --json --trace payload: the same hop structure the text
// view renders, kept flat so a script can diff two runs.
func traceJSON(t core.Task, events []core.Event, metrics []core.DelegationMetric) map[string]any {
	type hop struct {
		From   string  `json:"from"`
		To     string  `json:"to"`
		TS     int64   `json:"ts"`
		By     string  `json:"by,omitempty"`
		Events []int64 `json:"event_ids,omitempty"`
	}
	hops := []hop{}
	cur := -1
	for _, e := range events {
		if e.Type == core.EvDelegate {
			hops = append(hops, hop{To: traceEventField(e.DataJSON, "target"), TS: e.TS, By: traceEventField(e.DataJSON, "by")})
			cur = len(hops) - 1
			continue
		}
		if cur >= 0 {
			hops[cur].Events = append(hops[cur].Events, e.ID)
		}
	}
	// Back-fill each hop's origin from the chain: chain[i] is the node that
	// sent hop i. A local task has chain=[self]; the first hop leaves it.
	for i := range hops {
		if i < len(t.Chain) {
			hops[i].From = t.Chain[i]
		} else if i > 0 {
			hops[i].From = hops[i-1].To
		} else {
			hops[i].From = t.OwnerNode
		}
	}
	type metric struct {
		Delegator string  `json:"delegator"`
		Executor  string  `json:"executor"`
		Success   bool    `json:"success"`
		LatencyMs int64   `json:"latency_ms"`
		Tokens    int64   `json:"tokens,omitempty"`
		Cost      float64 `json:"cost,omitempty"`
	}
	ms := []metric{}
	for _, m := range metrics {
		row := metric{Delegator: m.Delegator, Executor: m.Executor, Success: m.Success, LatencyMs: m.LatencyMs}
		if m.Tokens.Valid {
			row.Tokens = m.Tokens.Int64
		}
		if m.Cost.Valid {
			row.Cost = m.Cost.Float64
		}
		ms = append(ms, row)
	}
	return map[string]any{
		"task_id":   t.TaskID,
		"chain":     t.Chain,
		"transport": t.Transport,
		"hops":      hops,
		"metrics":   ms,
	}
}

// printTaskSpec renders the classifier's structured spec as labelled lines. The
// stored form is a JSON document, and printing it raw put the task's target and
// success criteria — the two things a reader of `panda task` came for — inside
// escaped quotes on one unwrappable line.
func printTaskSpec(raw string) {
	var spec struct {
		Scope       string   `json:"scope"`
		Target      string   `json:"target"`
		Constraints []string `json:"constraints"`
		Success     string   `json:"success_definition"`
	}
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		fmt.Printf("spec:     %s\n", raw) // unknown shape: better raw than dropped
		return
	}
	p := pal()
	field := func(label, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		fmt.Printf("  %s %s\n", p.Muted(cell(label, 9)), value)
	}
	fmt.Println(p.Heading("spec:"))
	field("scope", spec.Scope)
	field("target", spec.Target)
	for i, c := range spec.Constraints {
		label := ""
		if i == 0 {
			label = "limits"
		}
		field(label, p.MarkBullet()+" "+c)
	}
	field("success", spec.Success)
}

// printTaskResult renders the executor's stored result: the outcome first, then
// the output as text. The raw column is a JSON document whose stdout field holds
// the entire agent report with its newlines escaped — the one field a person
// opens `panda task` to read, in the one encoding they cannot read it in.
func printTaskResult(raw string) {
	var res struct {
		ExitCode       int      `json:"exit_code"`
		OK             bool     `json:"ok"`
		Stdout         string   `json:"stdout"`
		Stderr         string   `json:"stderr"`
		Failed         string   `json:"failed"`
		FilesChanged   []string `json:"files_changed"`
		FilesTruncated bool     `json:"files_truncated"`
		Question       string   `json:"question"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		fmt.Printf("result:   %s\n", raw)
		return
	}
	p := pal()
	outcome := p.Success(p.MarkOK() + " ok")
	if !res.OK {
		// A {"failed": reason} row never ran — a decline, a restart
		// interruption, a parked pin that timed out. Rendering its zero
		// exit code as "exit 0" reads like a success beside the ✗; the
		// reason it never ran is printed in the body below.
		if res.Failed != "" && res.ExitCode == 0 {
			outcome = p.Danger(p.MarkFail() + " failed")
		} else {
			outcome = p.Danger(fmt.Sprintf("%s exit %d", p.MarkFail(), res.ExitCode))
		}
	}
	fmt.Println(p.Heading("result:") + " " + outcome)
	if res.Question != "" {
		fmt.Println(p.Heading("question:") + " " + res.Question)
		fmt.Println("  " + p.Muted("answer with: panda approve <task-id> -m \"<answer>\""))
	}
	if len(res.FilesChanged) > 0 || res.FilesTruncated {
		const shown = 15
		list := res.FilesChanged
		more := ""
		if len(list) > shown {
			more = fmt.Sprintf(" … +%d", len(list)-shown)
			list = list[:shown]
		}
		line := strings.Join(list, ", ") + more
		if res.FilesTruncated {
			if line != "" {
				line += " "
			}
			line += "(partial — workdir too large to diff fully)"
		}
		fmt.Println(p.Heading("files:") + "  " + p.Muted(line))
	}
	for _, body := range []string{res.Failed, strings.TrimRight(res.Stdout, "\n"), strings.TrimRight(res.Stderr, "\n")} {
		if strings.TrimSpace(body) == "" {
			continue
		}
		for _, line := range strings.Split(body, "\n") {
			fmt.Println("  " + line)
		}
	}
}

// latestRouteFallback renders the newest route_fallback event as a one-line
// field, or "" when the task never fell back. A fallback means the task was
// routed to a peer, the link failed at send time, and it ran locally — the
// reader should see that without digging through the timeline.
func latestRouteFallback(events []core.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Type != core.EvRouteFallback {
			continue
		}
		var d struct {
			Intended string `json:"intended"`
			Reason   string `json:"reason"`
		}
		_ = json.Unmarshal([]byte(e.DataJSON), &d)
		return i18n.Tf(i18n.Detect(), "cli.task.route_fallback", "peer", orDash(d.Intended))
	}
	return ""
}

// taskJSON is the --json wire form of one task (fields mirror the web API's
// taskJSON closely enough for scripts).
// taskChildJSON is the compact sub-agent reference embedded in a parent's
// taskJSON — id/state/title is all a script needs to follow the tree.
type taskChildJSON struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Title string `json:"title,omitempty"`
}

// taskEventJSON is one audit row in the task's --json payload. It is what
// makes a failed task diagnosable without a second command or a database
// query: the terminal timeline clips long reasons, this does not.
type taskEventJSON struct {
	ID   int64           `json:"id"`
	TS   int64           `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

type taskJSON struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	// Children lists the task's spawned sub-agents (PANDA_DELEGATE children,
	// `task add --parent-id` rows) so an orchestrating parent reads as one.
	Children []taskChildJSON `json:"children,omitempty"`
	// SpawnDepth is the task's generation in the delegation tree: 0 for the
	// user's root task, 1 for its direct children, and so on. Omitted for
	// standalone tasks that are neither parent nor child.
	SpawnDepth *int   `json:"spawn_depth,omitempty"`
	Project    string `json:"project,omitempty"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Priority   string `json:"priority"`
	Owner      string `json:"owner,omitempty"`
	Executor   string `json:"executor,omitempty"`
	Pinned     string `json:"pinned,omitempty"`
	// Preferred is the soft device hint (spec.preferred). It is reported
	// separately from pinned because the two mean opposite things: pinned is
	// where the task MUST run, preferred is where it would PREFER to run and
	// may legitimately fall back from (route_fallback).
	Preferred string `json:"preferred,omitempty"`
	Session   string `json:"session_id,omitempty"`
	Intent    string `json:"intent,omitempty"`
	// WorkDir is the submitter's pin (a session worktree), empty for
	// ordinary tasks; ExecWorkDir is where the run actually happened.
	WorkDir     string `json:"work_dir,omitempty"`
	ExecWorkDir string `json:"exec_work_dir,omitempty"`
	// WaitingLink marks a dispatched row whose real location is this node's
	// outbox — parked awaiting the target's link, not running anywhere.
	WaitingLink bool `json:"waiting_link,omitempty"`
	// RouteFallback names the peer routing intended before the link failed
	// and the task ran locally — empty when the task went where decided.
	RouteFallback string `json:"route_fallback,omitempty"`
	// ScopePaths/Isolate mirror the delegate declaration the row carries —
	// how the task claims the shared tree: disjoint path lanes, or a private
	// copy that merges back. Empty means the default whole-project claim.
	ScopePaths []string `json:"scope_paths,omitempty"`
	Isolate    bool     `json:"isolate,omitempty"`
	// Verification is the delivery contract's verdict (verified|failed|
	// partial|skipped), empty when the task carried no contract. It is a
	// property of the outcome, not a state: done+unverified stays visible.
	Verification string `json:"verification,omitempty"`
	// ApprovalDisposition names why a review row is parked (accept_work,
	// resume_execution, needs_changed_input, ...). ReviewReason is the full
	// last review reason; Result is the raw result payload. The human view
	// truncates event values, so scripts need these two to diagnose a park
	// without reading SQLite.
	ApprovalDisposition string `json:"approval_disposition,omitempty"`
	ReviewReason        string `json:"review_reason,omitempty"`
	Result              string `json:"result,omitempty"`
	Created             string `json:"created_at"`
	Updated             string `json:"updated_at"`
	// Events is the full audit trail, newest last. The same payload the
	// timeline clips for the terminal is kept intact here for scripts and for
	// reading the real failure reason.
	Events []taskEventJSON `json:"events,omitempty"`
}

func taskToJSON(t core.Task) taskJSON {
	j := taskJSON{
		ID:                  t.TaskID,
		Verification:        t.Verification,
		ApprovalDisposition: string(t.ApprovalDisposition),
		Result:              t.ResultJSON,
		ParentID:            t.ParentID,
		Project:             t.Project,
		Title:               t.Title,
		State:               t.State,
		Priority:            priorityName(t.Priority),
		Owner:               t.OwnerNode,
		Pinned:              core.PinnedNode(t),
		Preferred:           core.PreferredNode(t),
		Session:             t.SessionID,
		Intent:              t.Intent,
		WorkDir:             t.WorkDir,
		ExecWorkDir:         t.ExecWorkDir,
		ScopePaths:          t.ScopePaths,
		Isolate:             t.Isolate,
		Created:             ts(t.CreatedAt),
		Updated:             ts(t.UpdatedAt),
	}
	return j
}

// resultWorkDir extracts the executor's workspace and node from a stored
// result payload — present when the work ran on another node (the local row
// then has no exec_work_dir of its own).
func resultWorkDir(resultJSON string) (dir, executor string, ok bool) {
	if resultJSON == "" {
		return "", "", false
	}
	var res struct {
		WorkDir  string `json:"work_dir"`
		Executor string `json:"executor"`
	}
	if json.Unmarshal([]byte(resultJSON), &res) != nil || res.WorkDir == "" {
		return "", "", false
	}
	return res.WorkDir, res.Executor, true
}

// taskToJSONWithStore fills the store-derived fields (executor) taskToJSON
// leaves empty; the JSON emitter calls it where a store handle exists.
func taskToJSONWithStore(store *core.TaskStore, t core.Task) taskJSON {
	j := taskToJSON(t)
	if target, err := store.DispatchTarget(context.Background(), t.TaskID); err == nil &&
		target != "" && target != t.OwnerNode {
		j.Executor = target
	}
	j.WaitingLink = t.State == core.StateDispatched &&
		store.TaskOutboxPending(context.Background(), t.TaskID)
	j.RouteFallback = store.RouteFallbackPeer(context.Background(), t.TaskID)
	if t.State == core.StateReview {
		if events, err := store.Events(context.Background(), t.TaskID); err == nil {
			j.ReviewReason = latestReviewReason(events)
		}
	}
	if kids, err := store.Children(context.Background(), t.TaskID); err == nil {
		for _, k := range kids {
			j.Children = append(j.Children, taskChildJSON{ID: k.TaskID, State: k.State, Title: k.Title})
		}
		if len(kids) > 0 || t.SpawnDepth > 0 {
			d := t.SpawnDepth
			j.SpawnDepth = &d
		}
	}
	if events, err := store.Events(context.Background(), t.TaskID); err == nil {
		j.Events = make([]taskEventJSON, 0, len(events))
		for _, e := range events {
			ev := taskEventJSON{ID: e.ID, TS: e.TS, Type: e.Type}
			if raw := strings.TrimSpace(e.DataJSON); raw != "" && raw != "null" {
				ev.Data = json.RawMessage(raw)
			}
			j.Events = append(j.Events, ev)
		}
	}
	return j
}

// resolveTaskRef turns a user-typed task reference into a full task id, or ends
// the command with a message that says what to type instead. Every listing shows
// a task by its short id, so every command that takes one has to accept it.
func resolveTaskRef(store *core.TaskStore, ref string) string {
	id, err := store.ResolveTaskID(context.Background(), ref)
	if err == nil {
		return id
	}
	var amb *core.AmbiguousTaskIDError
	if errors.As(err, &amb) {
		fail(2, "panda: "+ambiguousTaskMsg(i18n.Detect(), amb))
	}
	taskStoreFatal(err, ref)
	return ""
}

// ambiguousTaskMsg phrases a prefix collision: how many tasks it hit, then the
// ids themselves — the user's next command is a copy-paste from this list, so
// printing the candidates is the whole point of the message.
func ambiguousTaskMsg(loc i18n.Locale, amb *core.AmbiguousTaskIDError) string {
	msg := i18n.Tf(loc, "cli.task.ambiguous",
		"ref", amb.Ref, "n", strconv.Itoa(len(amb.Candidates)))
	for _, c := range amb.Candidates {
		msg += "\n  " + c
	}
	return msg
}

func taskStoreFatal(err error, id string) {
	if errors.Is(err, sql.ErrNoRows) {
		fail(1, "panda: "+i18n.Tf(i18n.Detect(), "cli.noSuch.task", "id", id))
	}
	fatal("get task", err)
}

// repeatableFlag collects a flag given more than once. Go's flag package
// replaces the value on each Set; acceptance checks are a list, so they need
// their own Value.
type repeatableFlag []string

func (r *repeatableFlag) String() string { return strings.Join(*r, ", ") }

func (r *repeatableFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// buildAcceptanceChecks turns --accept values into a validated contract. Each
// plain value is one command; "auto" expands to the workspace's own build and
// test entries. Duplicates collapse, because running the same suite twice is
// only slower, never stronger.
func buildAcceptanceChecks(values []string, root string) ([]contract.Check, error) {
	var checks []contract.Check
	seen := make(map[string]bool, len(values))
	add := func(id, run string) {
		run = strings.TrimSpace(run)
		if run == "" || seen[run] {
			return
		}
		seen[run] = true
		if strings.TrimSpace(id) == "" {
			id = fmt.Sprintf("cli-%d", len(checks)+1)
		}
		checks = append(checks, contract.Check{ID: id, Run: run})
	}
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.EqualFold(raw, "auto") {
			for _, c := range contract.Enumerate(root) {
				add(c.ID, c.Run)
			}
			continue
		}
		add("", raw)
	}
	if len(checks) == 0 {
		return nil, fmt.Errorf("no acceptance checks found in %s", root)
	}
	return contract.Validate(checks)
}

// withAcceptance merges the contract into a task's spec JSON, preserving any
// fields already there (an actuator dispatch, for example).
func withAcceptance(specJSON string, checks []contract.Check) (string, error) {
	if len(checks) == 0 {
		return specJSON, nil
	}
	spec := map[string]any{}
	if strings.TrimSpace(specJSON) != "" {
		if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
			return "", err
		}
	}
	spec["acceptance"] = checks
	blob, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	return string(blob), nil
}

// runTaskVerdict prints the acceptance evidence for one task: every recorded
// check by phase, plus the verification word stamped on the row. It exits 1
// when the contract failed, so a script can gate on it.
func runTaskVerdict(args []string) {
	fs := flag.NewFlagSet("task verdict", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	ref := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if ref == "" {
		taskUsage()
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()
	ctx := context.Background()
	id := resolveTaskRef(store, ref)
	t, err := store.Get(ctx, id)
	if err != nil {
		taskStoreFatal(err, id)
	}
	rows, err := store.Checks(ctx, id)
	if err != nil {
		fatal("load checks", err)
	}
	loc := i18n.Detect()
	if jsonOutput {
		emitJSON(map[string]any{
			"task_id": id, "state": t.State, "verification": t.Verification, "checks": rows,
		})
		return
	}
	status := t.Verification
	if status == "" {
		status = "unverified"
	}
	fmt.Printf("%s  %s  %s\n", id, t.State, i18n.Tf(loc, "contract.verdict.head", "status", status))
	if len(rows) == 0 {
		fmt.Println("  " + i18n.T(loc, "contract.verdict.none"))
		return
	}
	for _, r := range rows {
		line := fmt.Sprintf("  %-8s %-14s %-6s exit %-3d %6dms  %s",
			r.Phase, r.CheckID, r.Status, r.ExitCode, r.DurationMS, truncate(oneLine(r.Command), 70))
		if r.Reason != "" {
			line += "  · " + truncate(oneLine(r.Reason), 80)
		}
		fmt.Println(line)
	}
	if status == contract.VerdictFailed {
		os.Exit(1)
	}
}

// runTaskAdd enqueues a user task through the ask engine's scheduler core —
// the kernel-form of the board's "new task" (the capability card is
// auto-discovered and only consulted at dispatch; a missing card does not
// block enqueueing). A linked session is created so the task's progress
// streams into `panda session show`.
func runTaskAdd(args []string) {
	fs := flag.NewFlagSet("task add", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	cardPath := fs.String("card", cardFlagDefault(), "path to capabilities.yaml (default: discovered)")
	mcpCmd := fs.String("mcp", cliMCP, "MCP server command (space-separated)")
	title := fs.String("title", "", "task title (required)")
	prompt := fs.String("prompt", "", "task prompt (defaults to the title)")
	priority := fs.String("priority", "normal", "priority: "+cliPriorities)
	project := fs.String("project", "", "project to attach the task to")
	authorize := fs.Bool("authorize", false, "authorize tier-2 (irreversible) commands")
	requires := fs.String("requires", "coding", "comma-separated ability ids the task needs (routed cross-device)")
	agents := fs.String("agents", "", "comma-separated agent harnesses to run this task on (e.g. claude_code,codex); more than one becomes a plan")
	mode := fs.String("mode", "parallel", "with --agents: 'parallel' runs every harness at once, 'serial' chains them in order")
	parentID := fs.String("parent-id", "", "parent task id (defaults to PANDA_TASK_ID environment variable)")
	preferred := fs.String("preferred", "", "soft device hint: prefer this node id/name, but let the scheduler fall back if it is offline or incapable (use --nodes for a hard pin)")
	nodes := fs.String("nodes", "", "comma-separated node ids to pin the task onto (one task per node; each lands on its named node or fails)")
	actionSpec := fs.String("action-spec", "", "actuator dispatch JSON: {\"target_actuator\":\"hardware:x\",\"action\":\"verb\",\"parameters\":{...}}")
	scopePaths := fs.String("paths", "", "comma-separated project-relative dirs the task owns (needs --project): disjoint tasks run in parallel in the shared tree")
	isolate := fs.Bool("isolate", false, "run in a private copy of the project tree (git worktree when available) and merge changes back on success — needs --project")
	workDir := fs.String("workdir", "", "pin execution to this directory (created if missing, and required for a contract whose checks live elsewhere); the task then runs on THIS node only")
	var accept repeatableFlag
	fs.Var(&accept, "accept", "acceptance check run by the orchestrator after the agent finishes: 'auto' enumerates the project's own build/test entries, or a command line (no shell syntax; repeatable)")
	wait := fs.Bool("wait", false, "block until the task settles (done/failed/cancelled/review) and print the result")
	waitTimeout := fs.Duration("wait-timeout", 30*time.Minute, "with --wait: stop watching after this long (the task itself keeps running)")
	fs.Parse(reorderFlags(fs, args))

	loc := i18n.Detect()
	// Positional words join --prompt: `task add --title T do the thing` —
	// silently dropping them would send the title as the whole prompt.
	var joined string
	if rest := fs.Args(); len(rest) > 0 {
		joined = strings.TrimSpace(strings.Join(rest, " "))
		if joined != "" {
			if *prompt == "" {
				*prompt = joined
			} else {
				*prompt = strings.TrimSpace(*prompt + " " + joined)
			}
		}
	}
	*title = strings.TrimSpace(*title)
	// `panda task add 帮我看下日志` with no --title: the words are the title,
	// and the prompt falls through to it below.
	if *title == "" {
		*title = joined
	}
	if *title == "" {
		fail(2, i18n.T(loc, "cli.task.add.noTitle"))
	}
	*prompt = strings.TrimSpace(*prompt)
	if *prompt == "" {
		*prompt = *title
	}
	prio, ok := parseCLIPriority(*priority)
	if !ok {
		fail(2, i18n.Tf(loc, "cli.task.add.badPriority", "level", *priority, "list", cliPriorities))
	}

	pID := strings.TrimSpace(*parentID)
	if pID == "" {
		pID = os.Getenv("PANDA_TASK_ID")
	}
	pref := strings.TrimSpace(*preferred)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	engine, err := askengine.New(context.Background(), cfg, askengine.Options{
		CardPath:   *cardPath,
		MCPCommand: *mcpCmd,
		ConfigPath: *configPath,
		// A write-and-exit command: enqueue the row and go. Opening the LAN
		// discovery socket for the milliseconds in between only contends for
		// the port with a daemon (or with the other seven parallel `task add`s
		// a script just launched) and buys a pairing window nobody can use.
		LocalOnly: true,
	})
	if err != nil {
		fatal("ask engine", err)
	}
	defer engine.Close()

	requiresList := []string{}
	requiresExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "requires" {
			requiresExplicit = true
		}
	})
	for _, r := range strings.Split(*requires, ",") {
		if r = strings.TrimSpace(r); r != "" {
			requiresList = append(requiresList, r)
		}
	}
	if len(requiresList) == 0 {
		requiresList = []string{"coding"}
	}

	// --agents a,b fans the task out to several harnesses: one stage per
	// agent on a plan — parallel releases them together, serial chains each
	// on the previous stage's output. A single agent folds into the normal
	// requires path unchanged.
	agentList := parseAgentList(*agents)
	nodeList := parseAgentList(*nodes)
	// --mode describes how a fan-out runs and nothing else, but it used to be
	// validated only on the fan-out path: `--mode plan` with one agent was
	// accepted and then ignored, so a user who meant "read only" got a writable
	// task and an executed acceptance contract. Refuse the vocabulary
	// everywhere; a value that is valid but inapplicable still only applies to
	// a fan-out, which is what the flag's help says.
	if err := validateAgentMode(*mode); err != nil {
		fail(2, i18n.Tf(loc, "cli.task.add.badMode", "mode", *mode))
	}
	// --wait watches one row; a fan-out creates several, so a single exit
	// code could never describe the batch. Judging by the raw --nodes string
	// refused a single pinned node too (one task, perfectly waitable) with a
	// message claiming it had fanned out.
	if *wait && waitRejectsFanout(len(agentList), len(nodeList)) {
		fail(2, i18n.T(loc, "cli.task.wait.multi"))
	}
	if len(agentList) > 1 {
		// A multi-harness plan and an actuator dispatch are different task
		// shapes; accepting both would silently drop the action_spec.
		if strings.TrimSpace(*actionSpec) != "" {
			fail(2, i18n.T(loc, "cli.task.add.actionSpecAgents"))
		}
		// A contract belongs to one task; a plan fans out per agent and each
		// stage would need its own. Refuse rather than silently verify only
		// the stages' shared prefix.
		if len(accept) > 0 {
			fail(2, i18n.T(loc, "cli.task.add.acceptAgents"))
		}
		// Plan stages get their own per-stage keys and private dirs — a
		// scope claim or isolation request on the parent would be silently
		// dropped, which is worse than saying so.
		if strings.TrimSpace(*scopePaths) != "" || *isolate {
			fail(2, i18n.T(loc, "cli.task.add.scopePlanConflict"))
		}
		runTaskAddAgents(loc, engine, cfg, agentList, *mode, *title, *prompt, requiresList, requiresExplicit, prio, jsonOutput)
		return
	}
	if len(agentList) == 1 {
		requiresList = append(requiresList, "agent:"+agentList[0])
	}

	// --action-spec carries the §7.2 actuator dispatch the model would emit in
	// spec.action_spec: the JSON is parsed into the canonical wire type (so a
	// malformed shape is refused here, not at the executor) and marshalled
	// back into spec_json. The context flips to "hardware" — a servo has no
	// checkout to attach.
	contextType := ""
	specJSON := ""
	if raw := strings.TrimSpace(*actionSpec); raw != "" {
		var as ledger.ActionSpec
		if err := json.Unmarshal([]byte(raw), &as); err != nil {
			fail(2, i18n.Tf(loc, "cli.task.add.badActionSpec", "err", err.Error()))
		}
		if as.TargetActuator == "" || as.Action == "" {
			fail(2, i18n.T(loc, "cli.task.add.actionSpecMissing"))
		}
		detail := struct {
			ActionSpec ledger.ActionSpec `json:"action_spec"`
		}{ActionSpec: as}
		b, _ := json.Marshal(detail)
		specJSON = string(b)
		contextType = "hardware"
		// The spec's exact target leads requires: without it a defaulted
		// "coding" routes the task to a coding node whose plan is not an
		// actuator, and the executor would fail the dispatch there.
		requiresList = ledger.RequiresForActionSpec(requiresList, &as)
	}
	projName := strings.TrimSpace(*project)
	if projName == "" {
		// Unnamed: the launch directory is the project space — the owning or
		// active project wins, else the directory is adopted when it looks
		// like a workspace (same rule `panda ask` uses).
		projName, _ = ambientProject(cfg)
	}
	// The same-node parallelism levers are declared on the task row the same
	// way a delegate marker would: they need a project to scope (the ambient
	// resolution above counts), and isolate must not be asked for a lane it
	// will never see — the copy ignores path claims entirely.
	var pathList []string
	for _, p := range strings.Split(*scopePaths, ",") {
		if p = strings.TrimSpace(p); p != "" {
			pathList = append(pathList, p)
		}
	}
	// An explicit --paths that parses to nothing ("", ",,") must not fall
	// through as a silent whole-project claim — the user asked for lanes.
	if strings.TrimSpace(*scopePaths) != "" && len(pathList) == 0 {
		fail(2, i18n.T(loc, "cli.task.add.pathsEmpty"))
	}
	if (len(pathList) > 0 || *isolate) && projName == "" {
		fail(2, i18n.T(loc, "cli.task.add.scopeNoProject"))
	}
	if len(pathList) > 0 && *isolate {
		fail(2, i18n.T(loc, "cli.task.add.scopeIsolateConflict"))
	}
	// --workdir names the tree this task writes, which makes the project key
	// redundant and the routing pins contradictory: a pinned directory exists on
	// THIS node only (forwardScheduled refuses to ship a non-file task whose
	// work dir cannot travel), so asking for a peer as well would promise a
	// destination that cannot run the work. --paths is project-relative and a
	// pinned directory is absolute — they describe different trees.
	pinnedWorkDir := strings.TrimSpace(*workDir)
	if pinnedWorkDir != "" {
		if pref != "" || strings.TrimSpace(*nodes) != "" || strings.TrimSpace(*scopePaths) != "" || len(agentList) > 1 {
			fail(2, i18n.T(loc, "cli.task.add.workdirConflict"))
		}
		if abs, aerr := filepath.Abs(pinnedWorkDir); aerr == nil {
			pinnedWorkDir = abs
		}
	}
	// The delivery contract. Commands are enumerated from this workspace at
	// submit time and persisted on the row, so the executor runs the same
	// concrete checks; "auto" is resolved here rather than at execution, where
	// the selected commands would be invisible to the user. The workspace is
	// pinned onto the task: a check enumerated here is only meaningful where
	// this tree lives, and a task that runs elsewhere would be judged against
	// a directory it cannot see.
	acceptRoot := ""
	if len(accept) > 0 {
		root := pinnedWorkDir
		if root == "" {
			root = strings.TrimSpace(*workDir)
		}
		if root == "" {
			root, _ = os.Getwd()
		}
		if root == "" {
			fail(2, i18n.T(loc, "cli.task.add.acceptNoDir"))
		}
		if abs, aerr := filepath.Abs(root); aerr == nil {
			root = abs
		}
		checks, cerr := buildAcceptanceChecks(accept, root)
		if cerr != nil {
			fail(2, i18n.Tf(loc, "cli.task.add.badAccept", "err", cerr.Error()))
		}
		merged, merr := withAcceptance(specJSON, checks)
		if merr != nil {
			fatal("build spec", merr)
		}
		specJSON = merged
		acceptRoot = root
	}

	in := core.TaskInput{
		Title:       *title,
		ParentID:    pID,
		Project:     projName,
		ContextType: contextType,
		Intent:      *prompt,
		SpecJSON:    specJSON,
		Requires:    requiresList,
		Paths:       pathList,
		Isolate:     *isolate,
		WorkDir:     pinnedWorkDir,
		// --preferred is the SOFT hint (docs/routing-layers.md §5): the scorer
		// prefers this node but may still run the task elsewhere, leaving a
		// route_fallback behind. --nodes is the only hard pin this command
		// writes, and it runs through runTaskAddNodes below, so nothing that
		// reaches here should land in TargetNode.
		PreferredNode: pref,
		Authorized:    *authorize,
	}
	// A contract is bound to the tree it was enumerated from. Pinning the work
	// dir keeps the run and the checks on the same bytes; it also keeps the
	// task on this node, which is the honest meaning of "verified here".
	if wd := strings.TrimSpace(*workDir); wd != "" {
		if abs, aerr := filepath.Abs(wd); aerr == nil {
			wd = abs
		}
		in.WorkDir = wd
	} else if acceptRoot != "" {
		in.WorkDir = acceptRoot
	}
	q := core.DefaultQueueSpec()
	q.Priority = prio
	// A pinned work dir rides BOTH surfaces: Enqueue stamps work_dir from the
	// QueueSpec (SetQueueMeta overwrites the column), and TaskInput.WorkDir is
	// what the createTask path persists for the non-queue branches. Setting one
	// alone leaves the row and the lock set disagreeing.
	if pinnedWorkDir != "" {
		q.WorkDir = pinnedWorkDir
	}
	// Ordinary tasks carry no WorkDir: engine.WorkPath() is the node-wide
	// default the executor already falls back to, and pinning it re-classifies
	// the task as local-only work — forwardScheduled skips any task with a
	// WorkDir, so `--requires pi.uptime` on a node without that ability never
	// reached the peer that has it (the same trap plan submission avoids).
	// --workdir and --accept are the deliberate exceptions: a contract's
	// checks run in the tree they were enumerated from, and that tree lives
	// here, so the task is pinned here on purpose.

	if len(nodeList) > 0 {
		// --nodes IS the routing preference: combining it with --preferred or
		// a multi-harness plan would produce ambiguous ownership per stage.
		if pref != "" || len(agentList) > 1 {
			fail(2, i18n.T(loc, "cli.task.add.nodesConflict"))
		}
		if len(accept) > 0 {
			fail(2, i18n.T(loc, "cli.task.add.acceptNodes"))
		}
		runTaskAddNodes(loc, engine, cfg, nodeList, in, q, priorityName(prio), *wait, *waitTimeout)
		return
	}

	task, warn, err := engine.EnqueueTask(context.Background(), in, q)
	if err != nil {
		fatal("enqueue task", err)
	}

	// Linked session (same contract as the board): created after a successful
	// enqueue so a failure leaves no orphan behind.
	sessionID := linkTaskSession(cfg, task.TaskID, *title, *prompt)

	// The advisory precedes the output split: --json keeps stdout clean but a
	// stderr line is still the cheapest way to keep a silently-stalled queue
	// from looking like a successful enqueue. The engine's warning is
	// store-aware: it also fires when a running daemon/web console drains a
	// different db_path, which the process probes alone cannot see.
	if warn != "" {
		fmt.Fprintln(os.Stderr, "⚠ "+warn)
	}
	if *wait {
		waitForTaskAdd(loc, cfg, task.TaskID, *waitTimeout)
		return
	}
	if jsonOutput {
		emitJSON(map[string]string{"task_id": task.TaskID, "session_id": sessionID, "state": task.State})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.task.add.done", "id", task.TaskID, "state", task.State, "priority", priorityName(prio)))
	if sessionID != "" {
		fmt.Println(i18n.Tf(loc, "cli.task.add.session", "id", sessionID))
	}
}

// waitTask polls a task row until it settles or ctx ends. "Settled" is
// broader than core.Terminal: a review row is an answer to "what happened to
// my task?". A failed row is only settled when nothing is going to retry it:
// the supervisor leaves the row in StateFailed for the backoff window before
// RequeueForRetry, and returning there reported "failed" for work that was
// still running. failedAwaitingRetry distinguishes that transient shape from
// a pre-dispatch failure (a bad actuator spec) that is already final.
func waitTask(ctx context.Context, store *core.TaskStore, taskID string) (core.Task, error) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var last core.Task
	for {
		t, err := store.Get(ctx, taskID)
		switch {
		case err != nil && ctx.Err() != nil:
			// The watch expired mid-read — report the last good row so the
			// caller can still say what state the task was in.
			return last, ctx.Err()
		case err != nil:
			return last, err
		}
		last = t
		if core.Terminal(t.State) || t.State == core.StateReview {
			return t, nil
		}
		if t.State == core.StateFailed && !failedAwaitingRetry(ctx, store, taskID) {
			return t, nil
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-tick.C:
		}
	}
}

// failedAwaitingRetry reports whether the row's newest failure came from an
// agent round the supervisor still means to retry. The recovery assessment it
// wrote decides that; only a row without one (an older binary, a crash between
// the result and the assessment) falls back to "an exec round ran, so a retry
// is behind it". A rejected or expired review is final by definition. A store
// read error fails safe: keep watching rather than declaring a live task dead.
func failedAwaitingRetry(ctx context.Context, store *core.TaskStore, taskID string) bool {
	events, err := store.Events(ctx, taskID)
	if err != nil {
		return true
	}
	return awaitingRetry(events)
}

// awaitingRetry is the retry rule itself, separated from the store so it can be
// tested against the event sequences production actually writes.
//
// A failed row says nothing about intent: the same state means "the supervisor
// will try again in 30 seconds" and "this is over". The recovery assessment —
// written after the failed result, before the retry decision — is the only
// event that answers it, and ignoring it made `--wait` sit on a settled task
// until the timeout expired.
func awaitingRetry(events []core.Event) bool {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case core.EvReview, core.EvExpire, core.EvCancel:
			return false
		case core.EvRecoveryAssessment:
			switch recoveryStrategy(events[i].DataJSON) {
			case core.RecoveryBoundedRetry, core.RecoverySessionResume:
				return true
			case core.RecoveryStop, core.RecoveryHumanReview:
				return false
			}
			// An assessment this code cannot read (an older row) falls through
			// to the result rule: keep watching rather than declare a live
			// task dead, the same way a store read error does.
		case core.EvResult:
			start := 0
			for j := i - 1; j >= 0; j-- {
				if events[j].Type == core.EvSubmit || events[j].Type == core.EvRetry {
					start = j + 1
					break
				}
			}
			for j := start; j < i; j++ {
				if events[j].Type == core.EvExecAgentStart || events[j].Type == core.EvSupervisionRound {
					return true
				}
			}
			return false
		}
	}
	return false
}

// recoveryStrategy reads the strategy out of one assessment event. Empty means
// "no opinion": the payload is shape-checked here, not trusted.
func recoveryStrategy(dataJSON string) string {
	var payload struct {
		Assessment struct {
			Strategy string `json:"strategy"`
		} `json:"assessment"`
	}
	if json.Unmarshal([]byte(dataJSON), &payload) != nil {
		return ""
	}
	return payload.Assessment.Strategy
}

// waitRejectsFanout reports whether --wait can describe the batch the two
// fan-outs would create. A single --nodes entry is one task and IS waitable;
// the bug this pins judged the raw --nodes string instead of its parsed
// length, rejecting a single pinned node with a "fan out to several" error.
func waitRejectsFanout(agentCount, nodeCount int) bool {
	return agentCount > 1 || nodeCount > 1
}

// waitForTaskAdd is the --wait half of `task add`: the row is already
// enqueued, so this only watches it — an interrupted or expired watch leaves
// the task running under whatever consumer picks it up. It owns the exit
// code: 0 done, 1 anything-not-done, 130 on Ctrl-C.
func waitForTaskAdd(loc i18n.Locale, cfg *config.Config, taskID string, timeout time.Duration) {
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()
	ctx, cancel := shutdownContext()
	defer cancel()
	wctx, wcancel := context.WithTimeout(ctx, timeout)
	defer wcancel()
	// Nothing is draining the queue: waiting out the full timeout hides the
	// fix behind a 30-minute silence. Say it now and exit. A settled row is
	// still reported normally — the consumer may have finished it before
	// this check.
	if cur, gerr := store.Get(ctx, taskID); gerr == nil &&
		!core.Terminal(cur.State) && cur.State != core.StateReview &&
		!queueConsumerAlive(cfg) {
		if jsonOutput {
			emitJSON(map[string]string{"task_id": taskID, "state": cur.State, "outcome": "no_consumer"})
		} else {
			fmt.Println(i18n.Tf(loc, "cli.task.wait.noConsumer", "id", taskID))
		}
		os.Exit(1)
	}
	if !jsonOutput {
		fmt.Println(i18n.Tf(loc, "cli.task.waiting", "id", taskID))
	}
	final, werr := waitTask(wctx, store, taskID)
	exit := 1
	switch {
	case errors.Is(werr, context.DeadlineExceeded):
		if jsonOutput {
			emitJSON(map[string]string{"task_id": taskID, "state": final.State, "outcome": "timeout"})
		} else {
			fmt.Println(i18n.Tf(loc, "cli.task.wait.timeout",
				"id", taskID, "state", final.State, "dur", timeout.String()))
		}
	case errors.Is(werr, context.Canceled):
		exit = 130
		if jsonOutput {
			emitJSON(map[string]string{"task_id": taskID, "state": final.State, "outcome": "interrupted"})
		} else {
			fmt.Println(i18n.Tf(loc, "cli.task.wait.interrupted", "id", taskID))
		}
	case werr != nil:
		fatal("watch task", werr)
	default:
		if final.State == core.StateDone {
			exit = 0
		}
		if jsonOutput {
			emitJSON(taskToJSONWithStore(store, final))
		} else {
			fmt.Println(i18n.Tf(loc, "cli.task.wait.state", "id", final.TaskID, "state", final.State))
			if final.Verification != "" {
				fmt.Println(i18n.Tf(loc, "tui.task.verification", "status", final.Verification))
			}
			if final.State == core.StateReview {
				if events, eerr := store.Events(context.Background(), final.TaskID); eerr == nil {
					if reason := latestReviewReason(events); reason != "" {
						fmt.Println(i18n.T(loc, "cli.task.review.reason") + ": " + reason)
					}
				}
				if hint := taskNextStepHint(loc, cfg, final, final.TaskID); hint != "" {
					fmt.Println(hint)
				}
			} else if final.ResultJSON != "" {
				printTaskResult(final.ResultJSON)
			}
		}
	}
	os.Exit(exit)
}

// linkTaskSession creates the board-style linked session for an enqueued
// task: a session row, the user's prompt as its first turn, and the task's
// session_id back-link. Returns "" on any failure — a task must never fail
// to enqueue because its optional session could not be created.
func linkTaskSession(cfg *config.Config, taskID, title, prompt string) string {
	return linkTaskSessionIn(cfg, taskID, title, prompt, "")
}

// linkTaskSessionIn is linkTaskSession with the project the thread belongs to.
// The console files every board-created thread under its project
// (sessions.Create takes one); a surface that starts work for a project-owned
// row has to do the same, or the thread lands outside the project it came
// from and never shows up there.
func linkTaskSessionIn(cfg *config.Config, taskID, title, prompt, project string) string {
	sessStore := sessions.NewStore(sessionStoreRoot(cfg))
	sess, err := sessStore.Create(title, project)
	if err != nil {
		return ""
	}
	_, _ = sessStore.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: prompt})
	db, store, derr := panelStore(cfg)
	if derr == nil {
		_ = store.SetSessionID(context.Background(), taskID, sess.ID)
		db.Close()
	}
	return sess.ID
}

// runTaskAddNodes fans one task out to the named nodes (Track 2): one
// enqueued task per node, each hard-pinned by TargetNode. routePinned never
// falls through to a scored candidate, so a copy either runs on its named
// node, parks in task_outbox until the link returns, or fails honestly. The
// operator named a machine; substituting another one silently is not an
// option here.
// Results are aggregated as the per-node task list: enqueue is
// asynchronous, so what can be reported here is where each copy went, and
// `panda task show <id>` tracks each outcome.
func runTaskAddNodes(loc i18n.Locale, engine *askengine.Engine, cfg *config.Config, nodes []string, in core.TaskInput, q core.QueueSpec, prioName string, wait bool, waitTimeout time.Duration) {
	type fanResult struct {
		Node      string `json:"node"`
		TaskID    string `json:"task_id"`
		SessionID string `json:"session_id,omitempty"`
		State     string `json:"state,omitempty"`
		Error     string `json:"error,omitempty"`
	}
	results := make([]fanResult, 0, len(nodes))
	failed := 0
	consumerWarn := ""
	for _, node := range nodes {
		per := in
		per.TargetNode = node
		r := fanResult{Node: node}
		task, warn, err := engine.EnqueueTask(context.Background(), per, q)
		if err != nil {
			r.Error = err.Error()
			failed++
		} else {
			r.TaskID = task.TaskID
			r.State = task.State
			r.SessionID = linkTaskSession(cfg, task.TaskID, in.Title, in.Intent)
			if consumerWarn == "" {
				consumerWarn = warn
			}
		}
		results = append(results, r)
	}
	if consumerWarn != "" {
		fmt.Fprintln(os.Stderr, "⚠ "+consumerWarn)
	}
	// One --nodes entry produces exactly one row, so --wait has a single
	// outcome to follow. Keep this before the output split: the ordinary
	// `task add --wait` path also blocks instead of printing a JSON envelope.
	if wait && len(nodes) == 1 {
		for _, r := range results {
			if r.TaskID != "" {
				waitForTaskAdd(loc, cfg, r.TaskID, waitTimeout)
				return
			}
		}
	}
	if jsonOutput {
		emitJSON(map[string]any{"fanout": len(nodes), "tasks": results})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.task.add.fanout", "count", strconv.Itoa(len(nodes))))
	for _, r := range results {
		if r.Error != "" {
			fmt.Printf("  %s: ✗ %s\n", r.Node, r.Error)
			continue
		}
		fmt.Printf("  %s: %s → %s (%s)\n", r.Node, r.TaskID, r.State, prioName)
	}
	if failed == len(nodes) {
		os.Exit(1)
	}
}

// runTaskPriority implements `panda task priority <id> <level>`.
func runTaskPriority(args []string) {
	fs := flag.NewFlagSet("task priority", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	loc := i18n.Detect()
	if fs.NArg() != 2 {
		fail(2, "usage: panda task priority <task-id> <"+cliPriorities+">")
	}
	id, level := fs.Arg(0), fs.Arg(1)
	prio, ok := parseCLIPriority(level)
	if !ok {
		fail(2, i18n.Tf(loc, "cli.task.add.badPriority", "level", level, "list", cliPriorities))
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()
	id = resolveTaskRef(store, id)
	if err := store.SetPriority(context.Background(), id, prio); err != nil {
		fatal("set priority", err)
	}
	if jsonOutput {
		emitJSON(map[string]string{"id": id, "priority": priorityName(prio)})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.task.priority.done", "id", id, "priority", priorityName(prio)))
}

// runTaskMove implements `panda task move <id> <seq>` — the drag-sort order
// the queue scheduler honors before priority.
func runTaskMove(args []string) {
	fs := flag.NewFlagSet("task move", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	if fs.NArg() != 2 {
		fail(2, "usage: panda task move <task-id> <seq>")
	}
	id := fs.Arg(0)
	seq, err := strconv.ParseInt(fs.Arg(1), 10, 64)
	if err != nil {
		failf(2, "panda: seq must be an integer, got %q\n", fs.Arg(1))
	}
	cfg, cerr := config.Load(*configPath)
	if cerr != nil {
		fatal("load config", cerr)
	}
	db, store, oerr := panelStore(cfg)
	if oerr != nil {
		fatal("open store", oerr)
	}
	defer db.Close()
	id = resolveTaskRef(store, id)
	if err := store.SetSeq(context.Background(), id, seq); err != nil {
		fatal("move task", err)
	}
	if jsonOutput {
		emitJSON(map[string]any{"id": id, "seq": seq})
		return
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.task.move.done", "id", id, "seq", strconv.FormatInt(seq, 10)))
}

// runTaskDelete implements `panda task delete <id>` — remove one task (and its
// subtree) from the store. Queued and finished tasks go; a task that is still
// executing refuses with a pointer at `panda cancel`, because deleting the row
// under a live executor would strand its result and lease handling.
func runTaskDelete(args []string) {
	fs := flag.NewFlagSet("task delete", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	loc := i18n.Detect()
	if id == "" {
		fail(2, "usage: panda task delete [--config PATH] <task-id>")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	id = resolveTaskRef(store, id)
	// Fetch the row before it goes: its exec/work dirs name the checkouts that
	// may still carry this task's baseline or shadow refs.
	t, _ := store.Get(context.Background(), id)
	n, err := store.Delete(context.Background(), id)
	if err != nil {
		if errors.Is(err, core.ErrTaskActive) {
			t, gerr := store.Get(context.Background(), id)
			state := ""
			if gerr == nil {
				state = t.State
			}
			fail(1, i18n.Tf(loc, "cli.task.delete.active",
				"id", id, "state", state))
		}
		taskStoreFatal(err, id)
	}
	if n > 0 {
		// A deleted task can never be reverted — drop the refs it left so a
		// shared checkout does not accumulate dead panda-base branches.
		for _, dir := range taskGitDirs(t, cfg) {
			defense.CleanupGitRefs(dir, id)
		}
	}
	if jsonOutput {
		emitJSON(map[string]any{"id": id, "deleted": n})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.task.delete.done", "n", strconv.Itoa(n)))
}

// taskGitDirs returns the deduped candidate checkouts a task's git refs could
// live in: the recorded exec dir first, then the pinned/originating work dir,
// and finally the node's default work path as the last-resort guess.
func taskGitDirs(t core.Task, cfg *config.Config) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range []string{t.ExecWorkDir, t.WorkDir, cfg.Storage.WorkPath} {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// runTaskRevert implements `panda task revert <id>`: roll a task's git
// checkout back to the whole-tree baseline the executor saved before the run
// began (refs/heads/panda-base-<id>). Tracked and untracked changes alike are
// undone — that is the point of a rollback — so the printed summary names
// what was written back and what was deleted.
func runTaskRevert(args []string) {
	fs := flag.NewFlagSet("task revert", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	loc := i18n.Detect()
	if id == "" {
		fail(2, "usage: panda task revert <task-id>")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	id = resolveTaskRef(store, id)
	t, err := store.Get(context.Background(), id)
	if err != nil {
		taskStoreFatal(err, id)
	}

	var restored, removed []string
	reverted := false
	for _, dir := range taskGitDirs(t, cfg) {
		r, rm, rerr := defense.RollbackGitBaseline(dir, id)
		if errors.Is(rerr, defense.ErrNoGitBaseline) {
			continue
		}
		if rerr != nil {
			fatal("task revert", rerr)
		}
		restored, removed, reverted = r, rm, true
		break
	}
	if !reverted {
		fail(1, "panda: "+i18n.Tf(loc, "cli.task.revert.none", "id", id))
	}
	if jsonOutput {
		emitJSON(map[string]any{"id": id, "reverted": true, "restored": restored, "removed": removed})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.task.revert.done",
		"id", id, "restored", strconv.Itoa(len(restored)), "removed", strconv.Itoa(len(removed))))
}

// runTaskFollowup implements `panda task followup <id>` — the CLI twin of
// /task followup: resolve the task, fold its recorded result into the same
// follow-up prompt, and send it through the one-shot ask path (engine build,
// project binding and output formats all stay in runAsk).
func runTaskFollowup(args []string) {
	fs := flag.NewFlagSet("task followup", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	ref := strings.TrimSpace(strings.Join(fs.Args(), " "))
	loc := i18n.Detect()
	if ref == "" {
		fail(2, "usage: panda task followup <task-id>")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()
	id := resolveTaskRef(store, ref)
	t, err := store.Get(context.Background(), id)
	if err != nil {
		taskStoreFatal(err, id)
	}
	runAsk([]string{"--config", *configPath, taskFollowupText(loc, t)})
}

// runTaskBoard prints every live slot on the boards a task reads — its plan
// or parent board plus the board of its own child set. Debug/verify surface
// for the sibling-message layer; slots are rendered newest-seq per sender.
func runTaskBoard(args []string) {
	fs := flag.NewFlagSet("task board", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(fs, args))
	ref := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if ref == "" {
		fail(2, "usage: panda task board <task-id>")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()
	id := resolveTaskRef(store, ref)
	t, err := store.Get(context.Background(), id)
	if err != nil {
		taskStoreFatal(err, id)
	}
	ctx := context.Background()
	scopes := core.BoardScopes(t)
	if jsonOutput {
		boards := map[string][]core.TaskMessage{}
		for _, sc := range scopes {
			if msgs, err := store.TaskMessages(ctx, sc); err == nil {
				boards[sc] = msgs
			}
		}
		emitJSON(boards)
		return
	}
	for _, sc := range scopes {
		msgs, err := store.TaskMessages(ctx, sc)
		if err != nil {
			fatal("read board "+sc, err)
		}
		fmt.Printf("== %s (%d slot%s)\n", sc, len(msgs), pluralS(len(msgs)))
		for _, m := range msgs {
			to := m.Rcpt
			if to == "" {
				to = "*"
			}
			age := time.Since(time.Unix(m.SentAt, 0)).Round(time.Second)
			fmt.Printf("  %s -> %s  #%d  %-12s %s ago\n    %s\n",
				m.Sender, to, m.SenderSeq, m.Grade, age, m.Body)
		}
	}
}

// runTaskSend delivers a user message into a running or queued task — the
// two send modes (core/steer.go): default queues the text as board context
// the agent reads at its next supervision round; --inject marks it a steer
// that leads the next round and aborts the in-flight one when this process
// holds the task's execution (a QueueTasks REPL/web console). A message sent
// from a process that does not run the task degrades to next-boundary
// delivery — the note is durable either way.
func runTaskSend(args []string) {
	fs := flag.NewFlagSet("task send", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	cardPath := fs.String("card", "", "path to capabilities.yaml")
	inject := fs.Bool("inject", false, "abort the in-flight round and make the text the next round's leading directive")
	fs.Parse(reorderFlags(fs, args))
	rest := fs.Args()
	loc := i18n.Detect()
	if len(rest) < 2 {
		fail(2, i18n.T(loc, "cli.task.send.usage"))
	}
	ref := strings.TrimSpace(rest[0])
	text := strings.TrimSpace(strings.Join(rest[1:], " "))
	if text == "" {
		fail(2, i18n.T(loc, "cli.task.send.usage"))
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	engine, err := askengine.New(context.Background(), cfg, askengine.Options{
		CardPath:   *cardPath,
		ConfigPath: *configPath,
		LocalOnly:  true, // store write and exit — see runTaskAdd
	})
	if err != nil {
		fatal("ask engine", err)
	}
	defer engine.Close()
	id := resolveTaskRef(engine.TaskStore(), ref)
	status, warn, err := engine.SendTaskMessage(context.Background(), id, text, *inject)
	if err != nil {
		fatal("send task message", err)
	}
	key := "cli.task.send.queued"
	if status == "interrupted" {
		key = "cli.task.send.interrupted"
	}
	if jsonOutput {
		emitJSON(map[string]string{"task_id": id, "status": status, "warning": warn})
		return
	}
	fmt.Println(i18n.Tf(loc, key, "id", id))
	if warn != "" {
		fmt.Fprintln(os.Stderr, "⚠ "+warn)
	}
}

// runTaskContinue implements `panda task continue <id> [-m NOTE]` — requeue a
// task the supervisor parked with needs_followup so the queue scheduler runs
// it under a fresh round budget (same session, same work dir). The note, when
// given, leads the continued round as a steer-grade directive.
func runTaskContinue(args []string) {
	fs := flag.NewFlagSet("task continue", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	cardPath := fs.String("card", "", "path to capabilities.yaml")
	note := fs.String("m", "", "operator note leading the continued round")
	fs.StringVar(note, "note", "", "operator note leading the continued round")
	fs.Parse(reorderFlags(fs, args))
	loc := i18n.Detect()
	id := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if id == "" {
		fail(2, i18n.T(loc, "cli.task.continue.usage"))
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	engine, err := askengine.New(context.Background(), cfg, askengine.Options{
		CardPath:   *cardPath,
		ConfigPath: *configPath,
		LocalOnly:  true, // store write and exit — see runTaskAdd
	})
	if err != nil {
		fatal("ask engine", err)
	}
	defer engine.Close()
	id = resolveTaskRef(engine.TaskStore(), id)
	out := engine.ContinueReviewed(context.Background(), id, strings.TrimSpace(*note), askengine.StreamCallbacks{})
	if !out.OK {
		fatal("continue", errors.New(out.Stderr))
	}
	if jsonOutput {
		emitJSON(map[string]string{"task_id": out.TaskID, "state": out.TaskState, "warning": out.Warning})
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.task.continue.done", "id", out.TaskID))
	if out.Warning != "" {
		fmt.Fprintln(os.Stderr, "⚠ "+out.Warning)
	}
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// parseAgentList normalizes the --agents value: comma-separated harness names,
// "agent:" prefixes stripped, blanks and duplicates dropped while order is
// kept (the order is the serial chain order).
func parseAgentList(raw string) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range strings.Split(raw, ",") {
		a = strings.TrimSpace(a)
		if stripped, ok := strings.CutPrefix(a, "agent:"); ok {
			a = stripped
		}
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// errBadAgentMode marks an unrecognised --agents mode; the CLI turns it into
// a localized usage error (exit 2) rather than a fatal.
var errBadAgentMode = errors.New("bad agents mode")

// buildMultiAgentPlan turns `task add --agents a,b,...` into a plan: one
// stage per harness, released together (parallel) or chained on the previous
// stage's output (serial). Each stage pins its harness with an "agent:<name>"
// requirement, so routing lands the stage on a node that actually has it —
// which is the existing plan/DAG machinery, not a special path.
func buildMultiAgentPlan(agents []string, mode, title, prompt string, requires []string, requiresExplicit bool) (plan.Plan, error) {
	if err := validateAgentMode(mode); err != nil {
		return plan.Plan{}, err
	}
	serial := agentModeSerial(mode)
	p := plan.Plan{Goal: title}
	prev := ""
	for i, a := range agents {
		stageRequires := []string{"agent:" + a}
		if requiresExplicit {
			stageRequires = append(stageRequires, requires...)
		}
		st := plan.Stage{
			ID:       fmt.Sprintf("s%d-%s", i+1, stageIDName(a)),
			Title:    fmt.Sprintf("%s (%s)", title, a),
			Intent:   prompt,
			Requires: stageRequires,
		}
		if serial && prev != "" {
			st.Needs = []string{prev}
		}
		p.Stages = append(p.Stages, st)
		prev = st.ID
	}
	return p, plan.Validate(p)
}

// validateAgentMode accepts the fan-out shapes `task add --mode` documents.
func validateAgentMode(mode string) error {
	switch mode {
	case "parallel", "serial", "chain", "sequential":
		return nil
	}
	return fmt.Errorf("%w: %s", errBadAgentMode, mode)
}

// agentModeSerial reports whether the stages chain on each other rather than
// starting together. "chain" and "sequential" are the spellings the usage line
// has accepted since before the flag was validated.
func agentModeSerial(mode string) bool {
	return mode == "serial" || mode == "chain" || mode == "sequential"
}

// stageIDName maps an agent harness name into the [A-Za-z0-9_-] alphabet a
// stage id allows ("Claude Code" → "claude-code"); the s<n>- prefix keeps ids
// unique even when two names sanitize alike.
func stageIDName(agent string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, agent))
}

// runTaskAddAgents submits the synthesized multi-harness plan and reports the
// stages it created.
func runTaskAddAgents(loc i18n.Locale, engine *askengine.Engine, cfg *config.Config, agents []string, mode, title, prompt string, requires []string, requiresExplicit bool, prio int, jsonOut bool) {
	p, err := buildMultiAgentPlan(agents, mode, title, prompt, requires, requiresExplicit)
	if err != nil {
		if errors.Is(err, errBadAgentMode) {
			fail(2, i18n.Tf(loc, "cli.task.add.badMode", "mode", mode))
		}
		fatal("build plan", err)
	}
	q := core.DefaultQueueSpec()
	q.Priority = prio
	// No work dir, same rule as `panda plan run`: a path on this machine
	// means nothing to the node that runs the stage.
	q.WorkDir = ""
	planID, err := engine.StartPlan(context.Background(), p, q)
	if err != nil {
		fatal("start plan", err)
	}
	stages, serr := engine.PlanStages(context.Background(), planID)
	if serr != nil {
		fatal("read plan", serr)
	}
	if !queueConsumerAlive(cfg) {
		warnNoConsumerStderr(loc)
	}
	if jsonOut {
		emitJSON(planToJSON(planID, p.Goal, stages))
		return
	}
	fmt.Println(i18n.Tf(loc, "cli.task.add.plan", "id", planID, "stages", strconv.Itoa(len(stages)), "mode", mode))
	printPlanStages(stages)
	fmt.Println(i18n.Tf(loc, "cli.task.add.planFollow", "id", planID))
}
