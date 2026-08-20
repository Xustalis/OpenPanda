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
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

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
}

func resultToJSON(out *askengine.Result) askJSON {
	return askJSON{
		Kind: out.Kind, Answer: out.Answer, TaskID: out.TaskID, TaskState: out.TaskState,
		OK: out.OK, Stdout: out.Stdout, Stderr: out.Stderr, ExitCode: out.ExitCode,
	}
}

// runAsk implements `panda ask "..."` — one call through the unified entry
// engine (internal/askengine), shared with the web panel: answer/tool_call
// print the classified output; task additionally executes it on the node
// network via the scheduler core. --output-format json|stream-json switches
// to headless machine output (no TTY streaming, stable NDJSON/JSON).
func runAsk(args []string) {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	cardPath := fs.String("card", "", "path to capabilities.yaml (required to execute tasks)")
	authorize := fs.Bool("authorize", false, "authorize tier-2 (irreversible) commands")
	mcpCmd := fs.String("mcp", "", "MCP server command (space-separated), e.g. \"npx -y @modelcontextprotocol/server-filesystem /tmp\"")
	outputFormat := fs.String("output-format", "", "headless output: json (one object) or stream-json (NDJSON events)")
	fs.Parse(args)

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

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}

	engine, err := askengine.New(context.Background(), cfg, askengine.Options{
		CardPath:   *cardPath,
		MCPCommand: *mcpCmd,
	})
	if err != nil {
		fatal("ask engine", err)
	}
	defer engine.Close()

	switch *outputFormat {
	case "json":
		out, err := engine.Ask(context.Background(), prompt, *authorize)
		if err != nil {
			emitJSON(map[string]string{"error": err.Error()})
			os.Exit(1)
		}
		emitJSON(resultToJSON(out))
		if out.Kind == "task" && !out.OK {
			os.Exit(1)
		}
		return
	case "stream-json":
		runAskStreamJSON(engine, prompt, *authorize)
		return
	}

	out, streamed, err := askStreaming(engine, prompt, *authorize)
	if err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
		os.Exit(1)
	}

	switch out.Kind {
	case "answer":
		if !streamed {
			fmt.Println(out.Answer)
		}
	case "task":
		fmt.Println(i18n.Tf(loc, "cli.ask.task", "id", out.TaskID, "state", out.TaskState))
		if out.OK {
			fmt.Print(out.Stdout)
		} else {
			fmt.Fprintf(os.Stderr, "exit %d: %s\n", out.ExitCode, out.Stderr)
			os.Exit(1)
		}
	}
}

// runAskStreamJSON is the headless streaming mode: one NDJSON event per line
// (status / delta / result / error), consumable by scripts and other tools.
func runAskStreamJSON(engine *askengine.Engine, prompt string, authorize bool) {
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
	out, err := engine.AskTurns(context.Background(), nil, prompt, "", authorize, cb)
	if err != nil {
		write(map[string]any{"type": "error", "message": err.Error()})
		os.Exit(1)
	}
	result := resultToJSON(out)
	line, _ := json.Marshal(struct {
		Type string  `json:"type"`
		Data askJSON `json:"data"`
	}{"result", result})
	fmt.Println(string(line))
	if out.Kind == "task" && !out.OK {
		os.Exit(1)
	}
}

// askStreaming runs one ask with live streaming on an interactive terminal —
// answer text prints as the model emits it, and tool progress prints as
// one-line notes before any text arrives. Piped output stays clean: no
// callbacks are attached, and the full answer prints once at the end. The
// streamed marker tells the caller to skip the duplicate final print.
func askStreaming(engine *askengine.Engine, prompt string, authorize bool) (*askengine.Result, bool, error) {
	if !stdoutIsTTY() {
		out, err := engine.Ask(context.Background(), prompt, authorize)
		return out, false, err
	}
	var delivered bool
	cb := askengine.StreamCallbacks{
		OnDelta: func(chunk string) {
			delivered = true
			fmt.Print(chunk)
		},
		OnStatus: func(note string) {
			if !delivered {
				fmt.Printf("· %s\n", note)
			}
		},
	}
	out, err := engine.AskTurns(context.Background(), nil, prompt, "", authorize, cb)
	if err != nil && delivered {
		fmt.Println()
	}
	if err == nil && delivered {
		fmt.Println()
	}
	return out, delivered, err
}
