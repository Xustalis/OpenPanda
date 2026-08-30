package main

// `panda voice` — the hands-free entry surface. On the Orange Pi sitting on a
// desk there is no terminal to type into, so the sidecars in extensions/voice
// (wake word → ASR → TTS) are what makes "发布任务" a spoken sentence. Every one
// of those sidecars already existed and so did the Go side (entry.Listen /
// entry.Speak); nothing called them, which meant the entry surface the whole
// scenario starts from was unreachable. This file is that call.
//
// The turn runs through the same askengine every other surface uses, so a spoken
// sentence gets the identical treatment as a typed one: a simple question is
// answered locally, real work becomes a task routed to a capable node, and work
// that has to cross machines becomes a plan. The reply is spoken back, which is
// the "通过香橙派回发给我" half of the loop.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// voiceValueFlags enumerates `panda voice`'s value-carrying flags for
// reorderFlags (see global.go).
var voiceValueFlags = map[string]bool{
	"--config": true, "--card": true, "--mcp": true, "--listen": true,
}

// maxSpeakChars caps what gets read aloud. A long answer spoken in full is worse
// than a truncated one: the listener cannot skim, cannot scroll back, and has no
// way to interrupt a paragraph of prose. The full text still prints.
const maxSpeakChars = 700

// runVoice implements `panda voice` — wake word, transcribe, ask, speak back,
// repeat. Ctrl-C ends the sitting.
//
// Tier-2 consent is deliberately never granted here: there is no --authorize
// flag, and none is honored. An irreversible command spoken at a desk pet is
// exactly the case that should park in review for a person to approve from the
// queue, not run because the microphone heard it. `panda approve` (or the web
// console) is the second factor.
func runVoice(args []string) {
	fs := flag.NewFlagSet("voice", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	cardPath := fs.String("card", defaultCardPath(), fmt.Sprintf("path to capabilities.yaml (default: discovered ./capabilities.yaml or %s)", systemCardPath()))
	mcpCmd := fs.String("mcp", "", "MCP server command (space-separated)")
	listen := fs.Float64("listen", 0, "seconds to wait for the wake word each round (0 = wait indefinitely)")
	once := fs.Bool("once", false, "handle a single utterance and exit")
	mute := fs.Bool("mute", false, "print the reply instead of speaking it (TTS driver not needed)")
	fs.Parse(reorderFlags(args, voiceValueFlags))

	cfg, err := loadConfigQuietly(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	loc := i18n.Detect()
	if cfg.Model.BaseURL == "" {
		fmt.Fprintln(os.Stderr, "panda: "+i18n.T(loc, "cli.voice.needModel"))
		os.Exit(2)
	}

	// SIGINT/SIGTERM cancels the context, which kills the sidecar mid-capture
	// instead of leaving a python process holding the microphone.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	engine, err := askengine.New(ctx, cfg, askengine.Options{
		CardPath:   *cardPath,
		MCPCommand: *mcpCmd,
		ReplyASCII: isLinuxConsole(),
		// Voice is a long-lived listening loop: same reasoning as the REPL —
		// peers dial in the background, the mic opens without dead air.
		AsyncPeers: true,
	})
	if err != nil {
		fatal("ask engine", err)
	}
	defer engine.Close()

	// Voice shares the persisted thread with the REPL and `panda ask --continue`:
	// you speak a task at the Pi and follow it up from a terminal on another
	// machine, which is the point of a mobile entry surface.
	history := loadConvo()

	if *cardPath == "" {
		fmt.Fprintln(os.Stderr, i18n.T(loc, "cli.voice.noCard"))
	}
	fmt.Println(i18n.T(loc, "cli.voice.ready"))

	for {
		t := entry.Listen(ctx, *listen)
		if ctx.Err() != nil {
			fmt.Println("\n" + i18n.T(loc, "cli.voice.exited"))
			return
		}
		if !t.OK {
			// A timeout is normal — nobody spoke — and just goes around again. A
			// sidecar failure (missing driver, no microphone) will fail identically
			// on the next round, so it stops the loop instead of spinning on it,
			// respawning python forever behind a silent prompt.
			if !t.Timeout {
				fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(loc, "cli.voice.unavailable", "err", firstLine(t.Err)))
				os.Exit(1)
			}
			if *once {
				fmt.Println(i18n.T(loc, "cli.voice.noWake"))
				return
			}
			continue
		}
		text := strings.TrimSpace(t.Text)
		if text == "" {
			if *once {
				return
			}
			continue
		}
		fmt.Println("\n" + i18n.Tf(loc, "cli.voice.heard", "text", text))

		out, err := engine.AskTurns(ctx, history, text, "", false, askengine.StreamCallbacks{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "panda: %v\n", err)
			if *once {
				os.Exit(1)
			}
			continue
		}
		history = appendConvo(history, loc, text, out)
		speakBack(ctx, loc, out, *mute)
		if *once {
			return
		}
	}
}

// speakBack prints the outcome in full and speaks the short form. The two differ
// on purpose: the terminal gets the board and the ids, the speaker gets the one
// sentence a person standing in the room needs.
func speakBack(ctx context.Context, loc i18n.Locale, out *askengine.Result, mute bool) {
	switch out.Kind {
	case "answer":
		fmt.Println(renderCliMd(out.Answer))
	case "task":
		fmt.Println(i18n.Tf(loc, "cli.voice.task", "id", out.TaskID, "state", out.TaskState))
		if out.OK {
			fmt.Print(renderCliMd(out.Stdout))
			if s := strings.TrimRight(out.Stdout, "\n"); s != "" && !strings.HasSuffix(out.Stdout, "\n") {
				fmt.Println()
			}
		} else if out.Stderr != "" {
			fmt.Fprintf(os.Stderr, "exit %d: %s\n", out.ExitCode, out.Stderr)
		}
	case "plan":
		if !out.OK {
			fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(loc, "cli.plan.failed", "err", out.Stderr))
		} else {
			fmt.Println(i18n.Tf(loc, "cli.plan.started",
				"id", out.PlanID, "n", strconv.Itoa(len(out.PlanStages)), "goal", out.PlanGoal))
			printPlanStages(out.PlanStages)
			fmt.Println(i18n.Tf(loc, "cli.plan.follow", "id", out.PlanID))
		}
	}
	line := spokenReply(loc, out)
	if line == "" {
		return
	}
	if mute {
		fmt.Println(i18n.Tf(loc, "cli.voice.muted", "line", line))
		return
	}
	if err := entry.Speak(ctx, line); err != nil && ctx.Err() == nil {
		// A missing TTS driver must not lose the reply: it is already printed
		// above, so the note is all that is owed.
		fmt.Fprintln(os.Stderr, "panda: "+i18n.Tf(loc, "cli.voice.cannotSpeak", "err", firstLine(err.Error())))
	}
}

// spokenReply is the sentence read aloud for one outcome.
func spokenReply(loc i18n.Locale, out *askengine.Result) string {
	switch out.Kind {
	case "answer":
		return head(strings.TrimSpace(out.Answer), maxSpeakChars)
	case "task":
		// review is the pending-approval state and the one outcome the user must
		// hear about: nothing ran, and nothing will until they approve it.
		if out.TaskState == "review" {
			return i18n.T(loc, "cli.voice.say.review")
		}
		if !out.OK {
			return i18n.Tf(loc, "cli.voice.say.failed", "detail", head(firstLine(out.Stderr), 200))
		}
		if s := head(firstLine(out.Stdout), maxSpeakChars); s != "" {
			return i18n.Tf(loc, "cli.voice.say.done", "detail", s)
		}
		return i18n.T(loc, "cli.voice.say.doneBare")
	case "plan":
		if !out.OK {
			return i18n.T(loc, "cli.voice.say.planFailed")
		}
		return i18n.Tf(loc, "cli.voice.say.plan", "n", strconv.Itoa(len(out.PlanStages)))
	}
	return ""
}
