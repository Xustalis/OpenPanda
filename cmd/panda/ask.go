package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/mdtext"
)

// askValueFlags enumerates `panda ask`'s value-carrying flags for
// reorderFlags (see global.go): hoisted ahead of the positional prompt so
// `panda ask "question" --config x` parses the way users type it.
var askValueFlags = map[string]bool{
	"--config": true, "--card": true, "--mcp": true, "--output-format": true,
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
	cardPath := fs.String("card", defaultCardPath(), "path to capabilities.yaml (default: discovered ./capabilities.yaml or /etc/openpanda/capabilities.yaml)")
	authorize := fs.Bool("authorize", false, "authorize tier-2 (irreversible) commands")
	continueConvo := fs.Bool("continue", false, "continue the persisted conversation (the REPL's thread)")
	mcpCmd := fs.String("mcp", "", "MCP server command (space-separated), e.g. \"npx -y @modelcontextprotocol/server-filesystem /tmp\"")
	outputFormat := fs.String("output-format", "", "headless output: json (one object) or stream-json (NDJSON events)")
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

	// --continue: replay the persisted conversation (the REPL's thread)
	// as history, and record this exchange back into it — a one-shot ask
	// that behaves like the next turn of the sitting.
	var history []entry.Turn
	if *continueConvo {
		history = loadConvo()
	}
	recordConvo := func(out *askengine.Result) {
		if *continueConvo {
			appendConvo(history, prompt, out)
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

	out, streamed, err := askStreaming(engine, history, prompt, *authorize)
	if err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
		os.Exit(1)
	}
	recordConvo(out)

	switch out.Kind {
	case "answer":
		if !streamed {
			fmt.Println(renderCliMd(out.Answer))
		}
	case "task":
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
		printAskPlan(out)
	}
}

// printAskPlan renders a plan the entry model just started. A plan is
// asynchronous — its stages are queued and will run on other machines — so the
// useful output is the board plus how to follow it, not a result that does not
// exist yet.
func printAskPlan(out *askengine.Result) {
	if !out.OK {
		fmt.Fprintf(os.Stderr, "panda: 计划启动失败: %s\n", out.Stderr)
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
		OnDelta:  func(text string) { write(map[string]any{"type": "delta", "text": text}) },
		OnStatus: func(text string) { write(map[string]any{"type": "status", "text": text}) },
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
// answer lines print rendered as the model emits them (streamLineRenderer),
// and tool progress prints as one-line notes before any text arrives.
// Piped output stays clean: no callbacks are attached, and the full answer
// prints once at the end. The streamed marker tells the caller to skip the
// duplicate final print.
func askStreaming(engine *askengine.Engine, history []entry.Turn, prompt string, authorize bool) (*askengine.Result, bool, error) {
	if !stdoutIsTTY() {
		out, err := engine.AskTurns(context.Background(), history, prompt, "", authorize, askengine.StreamCallbacks{})
		return out, false, err
	}
	lr := newStreamLineRenderer()
	cb := askengine.StreamCallbacks{
		OnDelta: func(chunk string) { lr.delta(chunk) },
		OnStatus: func(note string) {
			if !lr.printed {
				fmt.Printf("· %s\n", note)
			}
		},
	}
	out, err := engine.AskTurns(context.Background(), history, prompt, "", authorize, cb)
	lr.flush()
	return out, lr.printed, err
}
