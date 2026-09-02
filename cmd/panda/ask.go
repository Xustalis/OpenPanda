package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/mdtext"
)

// askValueFlags enumerates `panda ask`'s value-carrying flags for
// reorderFlags (see global.go): hoisted ahead of the positional prompt so
// `panda ask "question" --config x` parses the way users type it.
var askValueFlags = map[string]bool{
	"--config": true, "--card": true, "--mcp": true, "--output-format": true,
	"--project": true,
}

// askJSON is the headless wire form of one ask result (shared by
// --output-format json and stream-json's final event).
type askJSON struct {
	Kind      string `json:"kind"`
	Answer    string `json:"answer,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	TaskState string `json:"task_state,omitempty"`
	OK        bool   `json:"ok,omitempty"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	// Plan fields (kind == "plan"): the stage list is what makes the routing
	// decision auditable from a script — which stage went where, and what it is
	// waiting for.
	PlanID   string          `json:"plan_id,omitempty"`
	PlanGoal string          `json:"plan_goal,omitempty"`
	Stages   []planStageJSON `json:"stages,omitempty"`
}

func resultToJSON(out *askengine.Result) askJSON {
	j := askJSON{
		Kind: out.Kind, Answer: out.Answer, TaskID: out.TaskID, TaskState: out.TaskState,
		OK: out.OK, Stdout: out.Stdout, Stderr: out.Stderr, ExitCode: out.ExitCode,
		PlanID: out.PlanID, PlanGoal: out.PlanGoal,
	}
	for _, t := range out.PlanStages {
		j.Stages = append(j.Stages, planStageJSON{
			Stage: t.StageID, TaskID: t.TaskID, State: t.State,
			Owner: t.OwnerNode, Needs: t.Needs, Output: t.OutputArtifact,
		})
	}
	return j
}

// runAsk implements `panda ask "..."` — one call through the unified entry
// engine (internal/askengine), shared with the web panel: answer/tool_call
// print the classified output; task additionally executes it on the node
// network via the scheduler core. --output-format json|stream-json switches
// to headless machine output (no TTY streaming, stable NDJSON/JSON).
func runAsk(args []string) {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	cardPath := fs.String("card", defaultCardPath(), fmt.Sprintf("path to capabilities.yaml (default: discovered ./capabilities.yaml or %s)", systemCardPath()))
	authorize := fs.Bool("authorize", false, "authorize tier-2 (irreversible) commands")
	continueConvo := fs.Bool("continue", false, "continue the persisted conversation (the REPL's thread)")
	mcpCmd := fs.String("mcp", "", "MCP server command (space-separated), e.g. \"npx -y @modelcontextprotocol/server-filesystem /tmp\"")
	outputFormat := fs.String("output-format", "", "headless output: json (one object) or stream-json (NDJSON events)")
	project := fs.String("project", "", "run this ask inside a project (default: the one you entered)")
	fs.Parse(reorderFlags(args, askValueFlags))

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		if stat, _ := os.Stdin.Stat(); stat != nil && stat.Mode()&os.ModeCharDevice == 0 {
			// Non-tty stdin: read the whole stream as the prompt.
			b, _ := io.ReadAll(os.Stdin)
			prompt = strings.TrimSpace(string(b))
		}
	}
	loc := i18n.Detect()
	if prompt == "" {
		fmt.Fprintln(os.Stderr, i18n.T(loc, "cli.ask.usage"))
		os.Exit(2)
	}
	switch *outputFormat {
	case "", "json", "stream-json":
	default:
		fmt.Fprintln(os.Stderr, i18n.Tf(loc, "cli.ask.badFormat", "format", *outputFormat))
		os.Exit(2)
	}

	cfg, err := loadConfigQuietly(*configPath)
	if err != nil {
		fatal("load config", err)
	}

	engine, err := askengine.New(context.Background(), cfg, askengine.Options{
		CardPath:   *cardPath,
		MCPCommand: *mcpCmd,
		ReplyASCII: isLinuxConsole(),
	})
	if err != nil {
		fatal("ask engine", err)
	}
	defer engine.Close()

	// The project is ambient state, not an argument the user should retype: an
	// explicit --project wins, otherwise the one they entered. Naming a project
	// that does not exist is an error rather than a silent no-op, since the whole
	// point of the flag is to put the work somewhere findable.
	bindAskProject(engine, cfg, *project)

	// --continue: replay the persisted conversation (the REPL's thread)
	// as history, and record this exchange back into it — a one-shot ask
	// that behaves like the next turn of the sitting.
	var history []entry.Turn
	if *continueConvo {
		history = loadConvo()
	}
	recordConvo := func(out *askengine.Result) {
		if *continueConvo {
			appendConvo(history, loc, prompt, out)
		}
	}

	switch *outputFormat {
	case "json":
		out, err := engine.AskTurns(context.Background(), history, prompt, "", *authorize, askengine.StreamCallbacks{})
		if err != nil {
			emitJSON(map[string]string{"error": err.Error()})
			os.Exit(1)
		}
		recordConvo(out)
		emitJSON(resultToJSON(out))
		if (out.Kind == "task" || out.Kind == "plan") && !out.OK {
			os.Exit(1)
		}
		return
	case "stream-json":
		runAskStreamJSON(engine, history, prompt, *authorize, recordConvo)
		return
	}

	out, streamed, st, err := askStreaming(engine, history, prompt, *authorize, loc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
		os.Exit(1)
	}
	// Inline approval gate: a tier-2 task with no standing consent returns parked
	// in review. On an interactive terminal, prompt and — on a yes — re-run it
	// authorized in place before recording the turn, so --continue captures the
	// resolved outcome rather than the transient review.
	if out.NeedsApproval && out.Approval != nil {
		out = confirmApprovalCLI(engine, out, loc)
	}
	recordConvo(out)

	switch out.Kind {
	case "answer":
		if !streamed {
			fmt.Println(renderCliMd(out.Answer))
		}
	case "task":
		// Sub-agent round: the converged report is the reply (it streamed
		// live when the terminal supports it), with the raw agent output
		// demoted to a pointer line. Without a report the raw output is the
		// display, as before.
		if strings.TrimSpace(out.Answer) != "" {
			if !streamed {
				fmt.Println(renderCliMd(out.Answer))
			}
			fmt.Println(pal().Muted(i18n.Tf(loc, "repl.ask.taskReport",
				"id", out.TaskID, "state", out.TaskState)))
			if !out.OK {
				os.Exit(1)
			}
			break
		}
		fmt.Println(i18n.Tf(loc, "cli.ask.task", "id", out.TaskID, "state", out.TaskState))
		if out.OK {
			fmt.Print(renderCliMd(out.Stdout))
			if s := strings.TrimRight(out.Stdout, "\n"); s != "" && !strings.HasSuffix(out.Stdout, "\n") {
				fmt.Println()
			}
		} else {
			fmt.Fprintf(os.Stderr, "exit %d: %s\n", out.ExitCode, out.Stderr)
			os.Exit(1)
		}
	case "plan":
		printAskPlan(loc, out)
	}
	printCost(st, out)
}

// printAskPlan renders a plan the entry model just started. A plan is
// asynchronous — its stages are queued and will run on other machines — so the
// useful output is the board plus how to follow it, not a result that does not
// exist yet.
func printAskPlan(loc i18n.Locale, out *askengine.Result) {
	if !out.OK {
		fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(loc, "cli.plan.failed", "err", out.Stderr))
		os.Exit(1)
	}
	fmt.Printf("plan:   %s\n", out.PlanID)
	fmt.Printf("goal:   %s\n", out.PlanGoal)
	fmt.Printf("stages: %d\n", len(out.PlanStages))
	printPlanStages(out.PlanStages)
	fmt.Printf("\nfollow: panda plan show %s\n", out.PlanID)
}

// renderCliMd adapts a model/agent output block to the CLI sink: color
// TTYs get the ANSI Markdown render, pipes and bare consoles get plain
// text (what the voice pipeline would speak). Shared by ask / session ask.
func renderCliMd(s string) string {
	if s == "" {
		return ""
	}
	if stdoutIsTTY() && termSupportsUnicode() && !isLinuxConsole() {
		return mdtext.ANSI(s)
	}
	return mdtext.Plain(s)
}

// streamLineRenderer renders streaming answer deltas line by line instead
// of echoing raw Markdown: bytes buffer until a newline, the completed
// line renders (ANSI on color TTYs, plain otherwise) and prints, keeping
// both the live-typing feel and clean output. Inside a fenced code block
// lines pass through (dimmed on TTY).
type streamLineRenderer struct {
	buf     strings.Builder
	fence   bool
	ansi    bool
	printed bool
}

func newStreamLineRenderer() *streamLineRenderer {
	return &streamLineRenderer{ansi: stdoutIsTTY() && termSupportsUnicode() && !isLinuxConsole()}
}

// delta consumes one streamed chunk, printing every completed line.
func (l *streamLineRenderer) delta(s string) {
	l.buf.WriteString(s)
	for {
		whole := l.buf.String()
		i := strings.IndexByte(whole, '\n')
		if i < 0 {
			return
		}
		line := strings.TrimRight(whole[:i], "\r")
		l.buf.Reset()
		l.buf.WriteString(whole[i+1:])
		if mdtext.IsFenceStart(line) {
			l.fence = !l.fence
			continue // the marker itself never prints
		}
		l.print(line)
	}
}

func (l *streamLineRenderer) print(line string) {
	if l.ansi {
		fmt.Println(mdtext.LineANSI(line, l.fence))
	} else {
		fmt.Println(mdtext.LinePlain(line, l.fence))
	}
	l.printed = true
}

// flush renders whatever remains buffered at end-of-stream.
func (l *streamLineRenderer) flush() {
	rest := strings.TrimRight(l.buf.String(), "\n")
	l.buf.Reset()
	if rest = strings.TrimSpace(rest); rest != "" {
		if mdtext.IsFenceStart(rest) {
			return
		}
		l.print(rest)
	}
}

// pending is the incomplete line still in the buffer — what the model has
// written since the last newline. It cannot be printed yet (inline Markdown is
// rendered per whole line), but it can be previewed on the status line so a
// long paragraph does not look like a stall.
func (l *streamLineRenderer) pending() string { return l.buf.String() }

// runAskStreamJSON is the headless streaming mode: one NDJSON event per line
// (status / delta / result / error), consumable by scripts and other tools.
func runAskStreamJSON(engine *askengine.Engine, history []entry.Turn, prompt string, authorize bool, record func(*askengine.Result)) {
	write := func(v map[string]any) {
		line, err := json.Marshal(v)
		if err != nil {
			return
		}
		fmt.Println(string(line))
	}
	cb := askengine.StreamCallbacks{
		OnDelta:     func(text string) { write(map[string]any{"type": "delta", "text": text}) },
		OnReasoning: func(text string) { write(map[string]any{"type": "reasoning", "text": text}) },
		OnStatus:    func(text string) { write(map[string]any{"type": "status", "text": text}) },
	}
	out, err := engine.AskTurns(context.Background(), history, prompt, "", authorize, cb)
	if err != nil {
		write(map[string]any{"type": "error", "message": err.Error()})
		os.Exit(1)
	}
	if record != nil {
		record(out)
	}
	result := resultToJSON(out)
	line, _ := json.Marshal(struct {
		Type string  `json:"type"`
		Data askJSON `json:"data"`
	}{"result", result})
	fmt.Println(string(line))
	if (out.Kind == "task" || out.Kind == "plan") && !out.OK {
		os.Exit(1)
	}
}

// askStreaming runs one ask with live streaming on an interactive terminal —
// a spinner and elapsed clock while nothing has arrived yet, then answer lines
// rendered as the model emits them (streamLineRenderer), with tool progress as
// one-line notes before any text. Piped output stays clean: no callbacks are
// attached, nothing is animated, and the full answer prints once at the end.
// The streamed marker tells the caller to skip the duplicate final print; the
// returned status line carries the run's numbers for a closing cost line.
func askStreaming(engine *askengine.Engine, history []entry.Turn, prompt string, authorize bool, loc i18n.Locale) (*askengine.Result, bool, *cliui.Status, error) {
	st := newStatusLine(loc)
	if !stdoutIsTTY() {
		out, err := engine.AskTurns(context.Background(), history, prompt, "", authorize, askengine.StreamCallbacks{})
		return out, false, st, err
	}
	lr := newStreamLineRenderer()
	var thinking thoughtPreview
	cb := askengine.StreamCallbacks{
		OnDelta: func(chunk string) {
			// Only a chunk that completes a line prints anything; the rest just
			// fills the renderer's buffer, so the status line stays put — but
			// the buffered tail is previewed there, because a paragraph that
			// takes twenty seconds to reach its newline should still look like
			// it is being written.
			if strings.ContainsRune(chunk, '\n') {
				st.Suspend(func() { lr.delta(chunk) })
				st.Preview(lr.pending())
				return
			}
			lr.delta(chunk)
			st.Preview(lr.pending())
		},
		OnReasoning: func(chunk string) {
			// Chain-of-thought precedes the answer on reasoning models; preview
			// it live and dim until the answer starts. Display-only (D14).
			if lr.printed {
				return
			}
			if line := thinking.feed(chunk); line != "" {
				st.Preview(pal().Muted(line))
			}
		},
		OnProgress: func(p askengine.Progress) {
			note := progressNote(loc, p)
			// Advance the phase chain so the status line's trailing meta shows
			// classify → routing → executing (P0 redesign §D3 parity with the
			// web DecisionOrbit). Tools run inside exec; planning/task creation
			// is classified as routing work because the scheduler has to pick
			// a node at that boundary.
			switch p.Kind {
			case askengine.ProgressTask, askengine.ProgressPlan, askengine.ProgressRoute:
				st.Phase("route", "routing")
			case askengine.ProgressExec:
				st.Phase("exec", "executing")
			case askengine.ProgressJudge:
				st.Phase("judge", "judging")
			case askengine.ProgressTool:
				st.Phase("exec", "executing")
			}
			if lr.printed {
				st.Note(note) // mid-answer: ephemeral, never interrupts the text
				return
			}
			st.Log(pal().Muted(pal().MarkBullet() + " " + note))
		},
	}
	st.Start(statusVerb(loc))
	st.Phase("classify", "classifying")
	out, err := engine.AskTurns(context.Background(), history, prompt, "", authorize, cb)
	if err == nil {
		// A tool-less answer emits no progress events, so nudge it into exec for
		// the closing chain. A task/plan already advanced through route/exec/judge
		// via the progress bridge — don't re-append exec after judge. Always land
		// on done so the closing phase line reads as a finished run.
		if out.Kind == "answer" {
			st.Phase("exec", "executing")
		}
		st.Phase("done", "done")
	}
	st.Stop()
	lr.flush()
	return out, lr.printed, st, err
}

// confirmApprovalCLI renders the tier-2 approval card for a task the engine
// parked in review, prompts on the interactive terminal, and — on a yes —
// re-runs the task authorized in place, returning the resumed Result. On a no,
// a non-interactive stdin, or a read error it returns the original review
// Result unchanged. It mirrors the REPL's approveInline for the one-shot
// `panda ask` path.
func confirmApprovalCLI(engine *askengine.Engine, out *askengine.Result, loc i18n.Locale) *askengine.Result {
	req := out.Approval
	p := pal()
	fmt.Println(p.Warn(p.MarkBullet() + " " + i18n.T(loc, "repl.approval.head")))
	fmt.Println(p.Muted("  " + i18n.Tf(loc, "repl.approval.task", "title", req.Title)))
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		fmt.Println(p.Muted("  " + i18n.Tf(loc, "repl.approval.reason", "reason", reason)))
	}
	// Only an interactive terminal can answer; a piped/headless ask leaves the
	// task parked (the JSON/stream paths never reach here — they surface review
	// directly), so the operator can /approve it from the daemon or web console.
	if !stdoutIsTTY() || !stdinIsTTY() {
		fmt.Println(p.Muted(i18n.Tf(loc, "repl.approval.denied", "id", req.TaskID)))
		return out
	}
	fmt.Print(i18n.T(loc, "repl.approval.prompt"))
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	ans := strings.ToLower(strings.TrimSpace(line))
	if ans != "y" && ans != "yes" {
		fmt.Println(p.Muted(i18n.Tf(loc, "repl.approval.denied", "id", req.TaskID)))
		return out
	}
	fmt.Println(p.Success(i18n.T(loc, "repl.approval.approved")))
	return engine.ResumeApproved(req.TaskID, "")
}

// printCost closes an interactive ask with what it cost: elapsed time, and the
// token count when the provider reports one. Dimmed, one line, TTY only — a
// piped ask must stay byte-clean for whatever consumes it.
func printCost(st *cliui.Status, out *askengine.Result) {
	if st == nil || out == nil || !stdoutIsTTY() {
		return
	}
	st.SetTokens(out.Tokens())
	if s := st.Stats(); s != "" {
		fmt.Println(pal().Muted(pal().MarkBullet() + " " + s))
	}
}
