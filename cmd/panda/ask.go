package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
)

// runAsk implements `panda ask "..."` — one call through the unified entry
// engine (internal/askengine), shared with the web panel: answer/tool_call
// print the classified output; task additionally executes it on the node
// network via the scheduler core.
func runAsk(args []string) {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	cardPath := fs.String("card", "", "path to capabilities.yaml (required to execute tasks)")
	authorize := fs.Bool("authorize", false, "authorize tier-2 (irreversible) commands")
	mcpCmd := fs.String("mcp", "", "MCP server command (space-separated), e.g. \"npx -y @modelcontextprotocol/server-filesystem /tmp\"")
	fs.Parse(args)

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		if stat, _ := os.Stdin.Stat(); stat != nil && stat.Mode()&os.ModeCharDevice == 0 {
			// Non-tty stdin: read the whole stream as the prompt.
			b, _ := io.ReadAll(os.Stdin)
			prompt = strings.TrimSpace(string(b))
		}
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: panda ask [--config PATH] [--card PATH] [--authorize] \"<question>\"")
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
		fmt.Printf("task %s %s\n", out.TaskID, out.TaskState)
		if out.OK {
			fmt.Print(out.Stdout)
		} else {
			fmt.Fprintf(os.Stderr, "exit %d: %s\n", out.ExitCode, out.Stderr)
			os.Exit(1)
		}
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
