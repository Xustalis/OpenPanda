// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/mdtext"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// askValueFlags enumerates `panda ask`'s value-carrying flags for the
// bare-input heuristic (main.go): `panda fix --project x` is a one-word line,
// not a phrase, so the value of a known value-flag must not read as a second
// word. Parsing itself does not consult this table — runAsk reorders against
// its own FlagSet, which cannot drift; keep the two in agreement when adding
// a flag, or the heuristic misjudges an unquoted one-liner.
var askValueFlags = map[string]bool{
	"config": true, "card": true, "mcp": true, "output-format": true, "accept": true,
	"project": true, "team": true, "node": true,
}

// askJSON is the headless wire form of one ask result (shared by
// --output-format json and stream-json's final event).
type askJSON struct {
	Kind      string `json:"kind"`
	Answer    string `json:"answer,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	TaskState string `json:"task_state,omitempty"`
	OK        bool   `json:"ok,omitempty"`
	// Deferred marks an approval parked for delivery — the task didn't fail,
	// it's waiting for the executor's link. Lets scripts tell custody from
	// failure without parsing task_state.
	Deferred bool   `json:"deferred,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	// Execution attribution: which agent harness ran the task, on which node,
	// with which model (and whether that model was injected by panda), plus
	// the entry model that served the ask's own classify/answer calls.
	Agent    string `json:"agent,omitempty"`
	Model    string `json:"model,omitempty"`
	Injected bool   `json:"injected,omitempty"`
	Executor string `json:"executor,omitempty"`
	// WorkDir is the workspace the executor ran in — the derived directory,
	// not the submitter's pin — so a script can locate the produced files.
	WorkDir string `json:"work_dir,omitempty"`
	// RouteFallback names the peer the task was routed to before the link
	// failed and it ran locally — the "meant for remote, ran here" marker.
	RouteFallback string `json:"route_fallback,omitempty"`
	// Verification is the delivery contract's verdict (verified|failed|
	// partial|skipped), empty when the task carried no contract. Scripts
	// should read this instead of inferring success from task_state.
	Verification string `json:"verification,omitempty"`
	Verified     bool   `json:"verified,omitempty"`
	EntryModel   string `json:"entry_model,omitempty"`
	// Plan fields (kind == "plan"): the stage list is what makes the routing
	// decision auditable from a script — which stage went where, and what it is
	// waiting for.
	PlanID   string          `json:"plan_id,omitempty"`
	PlanGoal string          `json:"plan_goal,omitempty"`
	Stages   []planStageJSON `json:"stages,omitempty"`
	// Clarification: the entry model asked before acting and this surface
	// could not answer inline. A script answers by sending the next ask with
	// the answers in the prompt; the question set is echoed for it.
	NeedsClarification bool               `json:"needs_clarification,omitempty"`
	Clarification      *entry.ClarifySpec `json:"clarification,omitempty"`
}

func resultToJSON(out *askengine.Result) askJSON {
	j := askJSON{
		Kind: out.Kind, Answer: out.Answer, TaskID: out.TaskID, TaskState: out.TaskState,
		OK: out.OK, Deferred: out.Deferred, Stdout: out.Stdout, Stderr: out.Stderr, ExitCode: out.ExitCode,
		Agent: out.Agent, Model: out.Model, Injected: out.Injected,
		Executor: out.Executor, WorkDir: out.WorkDir, RouteFallback: out.RouteFallback, EntryModel: out.EntryModel,
		Verification: out.Verification, Verified: out.Verified,
		PlanID: out.PlanID, PlanGoal: out.PlanGoal,
		NeedsClarification: out.NeedsClarification, Clarification: out.Clarification,
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	cardPath := fs.String("card", cardFlagDefault(), fmt.Sprintf("path to capabilities.yaml (default: discovered ./capabilities.yaml or %s)", systemCardPath()))
	authorize := fs.Bool("authorize", false, "authorize tier-2 (irreversible) commands")
	continueConvo := fs.Bool("continue", false, "continue the persisted conversation (the REPL's thread)")
	mcpCmd := fs.String("mcp", cliMCP, "MCP server command (space-separated), e.g. \"npx -y @modelcontextprotocol/server-filesystem /tmp\"")
	outputFormat := fs.String("output-format", "", "headless output: json (one object) or stream-json (NDJSON events)")
	project := fs.String("project", "", "run this ask inside a project (default: the one you entered)")
	team := fs.String("team", "", "comma-separated agents this ask may dispatch to (default: any; --continue inherits the conversation's roster)")
	node := fs.String("node", "", "run this turn's task on a specific node id or name (default: scheduler)")
	var accept repeatableFlag
	fs.Var(&accept, "accept", "acceptance check the orchestrator runs after the task: 'auto' enumerates this directory's checks, or a command line (no shell syntax; repeatable)")
	fs.Parse(reorderFlags(fs, args))

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
		fail(2, i18n.T(loc, "cli.ask.usage"))
	}
	switch *outputFormat {
	case "", "json", "stream-json":
	default:
		fail(2, i18n.Tf(loc, "cli.ask.badFormat", "format", *outputFormat))
	}

	cfg, err := loadConfigQuietly(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	if !modelConfigured(cfg) {
		fail(2, "panda: "+i18n.T(loc, "cli.ask.noModel"))
	}

	// With a live queue consumer (the daemon, or the console) tasks enqueue
	// and the ask follows them to settle — the run survives this process
	// exiting, and same-worktree serialization is enforced by resource keys
	// rather than by this one-off process. Without a consumer the ask owns
	// the whole turn anyway, so it submits inline: a queued task with nobody
	// to claim it would just sit.
	queueLive := queueConsumerAlive(cfg)
	engine, err := askengine.New(context.Background(), cfg, askengine.Options{
		CardPath:    *cardPath,
		MCPCommand:  *mcpCmd,
		ReplyASCII:  isLinuxConsole(),
		Locale:      loc,
		ConfigPath:  *configPath,
		QueueTasks:  queueLive,
		AwaitQueued: queueLive,
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
	// that behaves like the next turn of the sitting. The thread is a session
	// now, so "the REPL's thread" means the project's newest one; the bare
	// conversation file is the fallback for a node that never migrated.
	proj := strings.TrimSpace(*project)
	if proj == "" {
		proj, _ = activeProject(cfg)
	}
	var contStore *sessions.Store
	var contSess *sessions.Session
	var history []entry.Turn
	var inheritTeam []string
	if *continueConvo {
		contStore, contSess, history = continueTarget(context.Background(), cfg, proj, engine)
		if contSess != nil {
			inheritTeam = contSess.Team
		} else {
			inheritTeam = loadBareTeam()
		}
	}
	sessID := ""
	if contSess != nil {
		sessID = contSess.ID
	}
	// The roster: an explicit --team wins; --continue inherits the thread's
	// persisted pick, so a team chosen in the TUI keeps binding the same thread
	// from scripts. A one-shot ask with neither stays unrestricted.
	agents, err := resolveAskTeamWith(*team, inheritTeam)
	if err != nil {
		fail(2, "panda: "+err.Error())
	}
	if len(agents) > 0 {
		if unknown := unknownAgentNames(agents, *cardPath); len(unknown) > 0 {
			fmt.Fprintln(os.Stderr, "⚠ "+i18n.Tf(loc, "repl.team.unknown", "names", strings.Join(unknown, ", ")))
		}
	}
	recordConvo := func(out *askengine.Result) {
		if !*continueConvo {
			return
		}
		if contSess != nil {
			appendExchangeToSession(contStore, contSess.ID, prompt, out, loc)
			if out.Kind == "task" {
				bindTaskToSession(context.Background(), engine, out.TaskID, contSess.ID)
			}
			return
		}
		appendConvo(history, loc, prompt, out, engine)
	}

	cwd, _ := os.Getwd()
	// Continuing a thread runs where the thread lives: its worktree when it has
	// one, else the engine's bound work path. A one-shot ask keeps the caller's
	// cwd — the TUI's askContext makes the same choice for a bound session.
	workDir := continueWorkDir(cwd, contSess, engine)
	var acceptChecks []contract.Check
	if len(accept) > 0 {
		checks, cerr := buildAcceptanceChecks(accept, workDir)
		if cerr != nil {
			fail(2, i18n.Tf(loc, "cli.task.add.badAccept", "err", cerr.Error()))
		}
		acceptChecks = checks
	}
	switch *outputFormat {
	case "json":
		out, err := engine.AskTurnsScoped(context.Background(), history, prompt, askengine.AskScope{
			WorkDir: workDir, Agents: agents, Node: strings.TrimSpace(*node), AmbientProject: true,
			SessionID: sessID, Acceptance: acceptChecks,
		}, *authorize, askengine.StreamCallbacks{})
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
		runAskStreamJSON(engine, history, prompt, workDir, agents, strings.TrimSpace(*node), *authorize, acceptChecks, sessID, recordConvo)
		return
	}

	out, streamedText, st, err := askStreaming(engine, history, prompt, workDir, agents, strings.TrimSpace(*node), *authorize, acceptChecks, sessID, loc)
	if err != nil {
		fail(1, "panda: "+err.Error())
	}
	// Inline approval gate: a tier-2 task with no standing consent returns parked
	// in review. On an interactive terminal, prompt and — on a yes — re-run it
	// authorized in place before recording the turn, so --continue captures the
	// resolved outcome rather than the transient review.
	// A plan may park more than once: each approval re-enters the stage watch
	// and the next parked stage comes back as another NeedsApproval. The loop
	// ends on a denial or when the same stage still parks after its resume —
	// re-prompting that card forever would deadlock.
	for out.NeedsApproval && out.Approval != nil {
		next := confirmApprovalCLI(engine, out, loc, "")
		if next == out || (next.NeedsApproval && next.Approval != nil && next.Approval.TaskID == out.Approval.TaskID) {
			out = next
			break
		}
		out = next
	}
	// A task the agent parked on a structured question gets the same choice
	// card the TUI and web show: one command, no "now go find panda approve".
	// The answer resumes through the approval path and may park again; the
	// loop is bounded so a model that keeps asking cannot hold the command.
	for round := 0; round < 3 && out.Kind == "task" && out.QuestionSpec != nil; round++ {
		answers, ok := promptClarifyCLI(*out.QuestionSpec, loc)
		if !ok {
			break
		}
		answer := entry.ClarifyAnswerText(out.QuestionSpec, answers)
		cb := askengine.StreamCallbacks{
			OnProgress: func(p askengine.Progress) {
				fmt.Printf("%s %s\n", pal().MarkBullet(), progressNote(loc, p))
			},
		}
		next := engine.ResumeApproved(context.Background(), out.TaskID, workDir, cb, answer)
		if next == nil {
			break
		}
		out = next
	}
	recordConvo(out)

	switch out.Kind {
	case "answer":
		// The parsed Answer is the model's full reply; whatever the stream
		// withheld still lands here — nothing the model wrote may silently
		// truncate behind the "already streamed" flag.
		if tail := answerTail(streamedText, out.Answer); tail != "" {
			fmt.Println(renderCliMd(tail))
		}
	case "task":
		reportNote := i18n.Tf(loc, "cli.ask.task", "id", out.TaskID, "state", out.TaskState)
		if out.Agent != "" || out.Executor != "" {
			execNote := out.Agent
			if out.Model != "" {
				execNote += fmt.Sprintf(" (%s)", out.Model)
			}
			if out.Injected {
				execNote += " · " + i18n.T(loc, "tui.task.injected")
			}
			// Which machine actually ran it matters as much as which harness:
			// a task forwarded to a peer reports that peer's node id, never the
			// local one — so the line doubles as proof of where the work went.
			// A native (no-agent) task carries no harness name, just the node.
			if out.Executor != "" {
				if execNote != "" {
					execNote += " @ "
				}
				execNote += out.Executor
			}
			reportNote += " · " + i18n.Tf(loc, "tui.task.execBy", "exec", execNote)
		}
		// "Meant for the Mac, ran here" must surface in the receipt, not only
		// in the audit trail — the user's intent was a remote node.
		if out.RouteFallback != "" {
			reportNote += " · " + i18n.Tf(loc, "cli.task.route_fallback", "peer", out.RouteFallback)
		}
		fmt.Println(pal().Muted(reportNote))
		// The contract verdict is part of the receipt, not a footnote: a
		// "done" the orchestrator could not verify must not read like a
		// verified one.
		if out.Verification != "" {
			fmt.Println(pal().Muted("  " + i18n.Tf(loc, "tui.task.verification", "status", out.Verification)))
		}

		// A task parked on a clarification question must lead with the
		// question — it is the only thing standing between the work and done,
		// and "review" alone tells the user nothing actionable.
		if out.Question != "" {
			fmt.Println(pal().Heading(i18n.T(loc, "cli.ask.question")) + " " + out.Question)
			fmt.Println("  " + pal().Muted(i18n.Tf(loc, "cli.ask.question.hint", "id", out.TaskID)))
		}

		answerText := strings.TrimSpace(out.Answer)
		if answerText == "" {
			answerText = strings.TrimSpace(out.Report)
		}
		stdoutText := strings.TrimSpace(out.Stdout)

		// The report already streamed live on a TTY — re-print only the
		// slice that never reached the screen, exactly like the answer
		// branch. (A fully streamed report must not render a second copy.)
		reportTail := answerTail(streamedText, answerText)
		if reportTail != "" {
			fmt.Println(renderCliMd(reportTail))
		}
		if out.OK {
			// One outcome, one report. The synthesized answer already covers what
			// happened; appending the raw agent transcript whenever the model
			// paraphrased rather than quoted printed the same result twice — once
			// in the user's language and once in the agent's own English. The
			// verbatim log stays one command away.
			if answerText == "" && stdoutText != "" {
				fmt.Print(renderCliMd(out.Stdout))
				if !strings.HasSuffix(out.Stdout, "\n") {
					fmt.Println()
				}
			} else if answerText != "" && stdoutText != "" && !strings.Contains(answerText, stdoutText) && out.TaskID != "" {
				if reportTail != "" {
					fmt.Println()
				}
				fmt.Println(pal().Muted(i18n.Tf(loc, "tui.task.rawLogHint", "id", out.TaskID)))
			}
		} else {
			if out.Stderr != "" {
				fmt.Fprintf(os.Stderr, "exit %d: %s\n", out.ExitCode, out.Stderr)
			}
			os.Exit(1)
		}
	case "plan":
		printAskPlan(loc, out)
	case "clarify":
		// A headless ask could not show a choice card; the rendered questions
		// are the answer. The next ask should carry the choices.
		fmt.Println(renderCliMd(out.Answer))
	}
	printCost(st, out)
}

// printAskPlan renders a plan the entry model just started. A plan is
// a multi-stage, multi-device workflow whose stages have not all run
// yet; ask reports the plan ID and the stages that were planned so the
// user can follow along.
func printAskPlan(loc i18n.Locale, out *askengine.Result) {
	if !out.OK {
		fail(1, "panda: "+i18n.Tf(loc, "cli.plan.failed", "err", out.Stderr))
	}
	fmt.Printf("plan:   %s\n", out.PlanID)
	fmt.Printf("goal:   %s\n", out.PlanGoal)
	fmt.Printf("stages: %d\n", len(out.PlanStages))
	printPlanStages(out.PlanStages)
	if out.Warning != "" {
		fmt.Fprintln(os.Stderr, out.Warning)
	}
	fmt.Printf("\nfollow: panda plan show %s\n", out.PlanID)
}

// renderCliMd adapts a model/agent output block to the CLI sink: color
// TTYs get the ANSI Markdown render, pipes and bare consoles get plain
// text (what the voice pipeline would speak). Shared by ask / session ask.
func renderCliMd(s string) string {
	if s == "" {
		return ""
	}
	if os.Getenv("NO_COLOR") != "" || !stdoutIsTTY() || isLinuxConsole() || !cliui.WindowsVTReady() {
		return mdtext.Plain(s)
	}
	return mdtext.RenderTerminal(s)
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
	// WindowsVTReady doubles as the enabler: on a classic console host it
	// turns VT processing on for this process, so the ANSI path below renders
	// instead of printing raw escape sequences.
	return &streamLineRenderer{ansi: stdoutIsTTY() && os.Getenv("NO_COLOR") == "" && !isLinuxConsole() && cliui.WindowsVTReady()}
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

// answerTail returns the slice of the parsed answer the stream never
// delivered — "" when everything already reached the user. The delta guard
// only withholds real directives, but reconciling against the parsed Answer
// makes the display guarantee absolute: a max-token notice, an unwrapped
// envelope, or any future suppression that resolves to prose still lands on
// screen instead of vanishing behind the "already streamed" flag.
func answerTail(streamed, answer string) string {
	if strings.TrimSpace(answer) == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(answer, streamed):
		if tail := answer[len(streamed):]; strings.TrimSpace(tail) != "" {
			return tail
		}
	case !strings.Contains(streamed, answer):
		// The stream and the parsed answer genuinely diverged (not a prefix,
		// not an unwrap the stream already contains): show the parsed form
		// whole rather than silently keep whatever was withheld.
		return answer
	}
	return ""
}

// runAskStreamJSON is the headless streaming mode: one NDJSON event per line
// (status / delta / result / error), consumable by scripts and other tools.
func runAskStreamJSON(engine *askengine.Engine, history []entry.Turn, prompt, workDir string, agents []string, node string, authorize bool, accept []contract.Check, sessionID string, record func(*askengine.Result)) {
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
	out, err := engine.AskTurnsScoped(context.Background(), history, prompt, askengine.AskScope{
		WorkDir: workDir, Agents: agents, Node: node, AmbientProject: true, Acceptance: accept, SessionID: sessionID,
	}, authorize, cb)
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
func askStreaming(engine *askengine.Engine, history []entry.Turn, prompt, workDir string, agents []string, node string, authorize bool, accept []contract.Check, sessionID string, loc i18n.Locale) (*askengine.Result, string, *cliui.Status, error) {
	scope := askengine.AskScope{WorkDir: workDir, Agents: agents, Node: node, AmbientProject: true, Acceptance: accept, SessionID: sessionID}
	st := newStatusLine(loc)
	if !stdoutIsTTY() {
		out, err := engine.AskTurnsScoped(context.Background(), history, prompt, scope, authorize, askengine.StreamCallbacks{})
		return out, "", st, err
	}
	lr := newStreamLineRenderer()
	var streamedText strings.Builder
	var thinking thoughtPreview
	cb := askengine.StreamCallbacks{
		OnDelta: func(chunk string) {
			// Only a chunk that completes a line prints anything; the rest just
			// fills the renderer's buffer, so the status line stays put — but
			// the buffered tail is previewed there, because a paragraph that
			// takes twenty seconds to reach its newline should still look like
			// it is being written. The raw stream is accumulated too, for the
			// end-of-turn reconcile against the parsed answer.
			streamedText.WriteString(chunk)
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
		OnQuestion: func(spec entry.ClarifySpec) ([]entry.ClarifyAnswer, bool) {
			// The choice card owns the terminal: stop the spinner so the
			// options are not painted over, then restart the wait line when
			// the answers release the classifier.
			st.Stop()
			answers, ok := promptClarifyCLI(spec, loc)
			st.Start(statusVerb(loc))
			return answers, ok
		},
		OnProgress: func(p askengine.Progress) {
			// A usage report is a counter, not a phase: it updates the closing
			// meta in place and must not push a log line or a note.
			if p.Kind == askengine.ProgressUsage {
				st.SetTokens(p.InputTokens + p.OutputTokens)
				return
			}
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
			if p.Kind == askengine.ProgressTool || p.Kind == askengine.ProgressAgentText || lr.printed {
				st.Note(note) // tool execution, live agent chatter, or mid-answer: ephemeral, never interrupts the text
				return
			}
			st.Log(pal().Muted(pal().MarkBullet() + " " + note))
		},
	}
	st.Start(statusVerb(loc))
	st.Phase("classify", "classifying")
	out, err := engine.AskTurnsScoped(context.Background(), history, prompt, scope, authorize, cb)
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
	return out, streamedText.String(), st, err
}

// confirmApprovalCLI renders the tier-2 approval card for a task the engine
// parked in review, prompts on the interactive terminal, and — on a yes —
// re-runs the task authorized in place, returning the resumed Result. On a no,
// a non-interactive stdin, or a read error it returns the original review
// Result unchanged. It mirrors the REPL's approveInline for the one-shot
// `panda ask` path; sessionID binds a session-scoped remember ("" for the
// bare one-shot bucket).
func confirmApprovalCLI(engine *askengine.Engine, out *askengine.Result, loc i18n.Locale, sessionID string) *askengine.Result {
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
	scope := req.Scope
	if scope == "" {
		scope = projectstore.ScopeSession
	}
	fmt.Print(i18n.Tf(loc, "repl.approval.promptScope", "scope", scope))
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	approved, scope := parseApprovalAnswer(line, scope)
	// The answer stores under the chosen scope: "session" lands in the
	// engine's in-memory map (a one-shot ask's own lifetime — it answers any
	// further prompts this run produces), "project" persists onto the project
	// row where later asks, REPLs, and consoles find it. A project answer
	// without a project degrades to the session bucket.
	if scope != projectstore.ScopeOnce {
		if scope == projectstore.ScopeProject && req.Project == "" {
			scope = projectstore.ScopeSession
		}
		decision := projectstore.DecisionDeny
		if approved {
			decision = projectstore.DecisionApprove
		}
		if err := engine.RememberApproval(sessionID, req.Project, scope, decision); err == nil {
			fmt.Println(p.Muted(i18n.Tf(loc, "repl.approval.remembered", "decision", decision, "scope", scope)))
		}
	}
	if !approved {
		fmt.Println(p.Muted(i18n.Tf(loc, "repl.approval.denied", "id", req.TaskID)))
		return out
	}
	fmt.Println(p.Success(i18n.T(loc, "repl.approval.approved")))
	cb := askengine.StreamCallbacks{
		OnProgress: func(p askengine.Progress) {
			// Usage reports have no one to update on this path (the resumed run
			// prints no status line) and must not print an empty bullet.
			if p.Kind == askengine.ProgressUsage {
				return
			}
			fmt.Printf("%s %s\n", pal().MarkBullet(), progressNote(loc, p))
		},
	}
	resumed := engine.ResumeApproved(context.Background(), req.TaskID, "", cb)
	// Approving a plan stage unblocks one task; the pipeline behind it still
	// needs the plan watch or the ask ends at the resumed stage's receipt
	// while later stages run unreported. A later parked stage comes back as
	// another NeedsApproval for the caller's prompt loop.
	if out.Kind == "plan" && out.PlanID != "" {
		if awaited := engine.AwaitPlanOutcome(context.Background(), out.PlanID, cb); awaited != nil {
			return awaited
		}
	}
	return resumed
}

// promptClarifyCLI renders a pre-execution question round on the terminal and
// reads one line per question: option numbers (comma-separated for a
// multi-select) or free text, which becomes the "Other" answer. It returns
// ok=false when stdin is not interactive, so the engine hands the question
// back as a NeedsClarification Result instead of blocking a piped ask.
func promptClarifyCLI(spec entry.ClarifySpec, loc i18n.Locale) ([]entry.ClarifyAnswer, bool) {
	if !stdinIsTTY() || !stdoutIsTTY() {
		return nil, false
	}
	p := pal()
	fmt.Println(p.Warn(p.MarkBullet() + " " + i18n.T(loc, "clarify.title")))
	if plan := strings.TrimSpace(spec.Plan); plan != "" {
		fmt.Println(renderCliMd(plan))
	}
	reader := bufio.NewReader(os.Stdin)
	answers := make([]entry.ClarifyAnswer, 0, len(spec.Questions))
	for i, q := range spec.Questions {
		fmt.Printf("\n[%s] %s (%d/%d)\n", q.Header, q.Question, i+1, len(spec.Questions))
		for j, o := range q.Options {
			suffix := ""
			if o.Label == q.Recommended {
				suffix = clarifyRecommendedSuffix(o.Label, loc)
			}
			desc := ""
			if d := strings.TrimSpace(o.Description); d != "" {
				desc = " — " + p.Muted(d)
			}
			fmt.Printf("  %d) %s%s%s\n", j+1, o.Label, suffix, desc)
			// A preview is visual context for a single-select choice; the
			// terminal has no hover, so it prints under its own option.
			if !q.MultiSelect {
				if prev := clarifyPreviewText(o.Preview, 6); prev != "" {
					for _, line := range strings.Split(prev, "\n") {
						fmt.Println(p.Muted("     │ " + line))
					}
				}
			}
		}
		if q.MultiSelect {
			fmt.Println("  " + p.Muted(i18n.T(loc, "clarify.multi")))
		}
		fmt.Print("> ")
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return nil, false
		}
		answers = append(answers, parseClarifyLine(q, strings.TrimSpace(line)))
	}
	return answers, true
}

// clarifyRecommendedSuffix returns the localized recommended marker, or "" when
// the label already carries it in any bracket style — the model is told to put
// the recommendation first, and some models also append the wording.
func clarifyRecommendedSuffix(label string, loc i18n.Locale) string {
	marker := i18n.T(loc, "clarify.recommended")
	bare := strings.NewReplacer("（", "", "）", "", "(", "", ")", "", " ", "").Replace(marker)
	if strings.Contains(label, marker) || (bare != "" && strings.Contains(label, bare)) {
		return ""
	}
	return " " + marker
}

// clarifyPreviewText bounds an option preview to a screen-sized sample: the
// full text still lives in the spec, but a card or a terminal shows a glance,
// and the cut says so.
func clarifyPreviewText(text string, maxLines int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if maxLines > 0 && len(lines) > maxLines {
		lines = append(lines[:maxLines], "…")
	}
	return strings.Join(lines, "\n")
}

// parseClarifyLine turns one typed line into an answer. A line of option
// numbers selects those labels; anything else is free text ("Other"). An empty
// line leaves the question unanswered, and the model falls back to its
// recommendation.
func parseClarifyLine(q entry.ClarifyQuestion, line string) entry.ClarifyAnswer {
	a := entry.ClarifyAnswer{ID: q.ID}
	if line == "" {
		return a
	}
	parts := strings.FieldsFunc(line, func(r rune) bool {
		return r == ',' || r == '，' || r == ' ' || r == '\t'
	})
	var labels []string
	allIndices := len(parts) > 0
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > len(q.Options) {
			allIndices = false
			break
		}
		labels = append(labels, q.Options[n-1].Label)
	}
	if allIndices {
		if !q.MultiSelect && len(labels) > 1 {
			labels = labels[:1]
		}
		a.Selected = labels
		return a
	}
	a.Other = line
	return a
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
		// The closing line is also the model attribution: entry model first,
		// then elapsed/tokens, so "which model answered" is never implicit.
		if out.EntryModel != "" {
			s = out.EntryModel + " " + pal().Separator() + " " + s
		}
		if out.Cost > 0 {
			s += fmt.Sprintf(" ($%.4f)", out.Cost)
		}
		fmt.Println(pal().Muted(pal().MarkBullet() + " " + s))
	}
}
