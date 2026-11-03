// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	cardPath := fs.String("card", cardFlagDefault(), fmt.Sprintf("path to capabilities.yaml (default: discovered ./capabilities.yaml or %s)", systemCardPath()))
	mcpCmd := fs.String("mcp", cliMCP, "MCP server command (space-separated)")
	listen := fs.Float64("listen", 0, "seconds to wait for the wake word each round (0 = wait indefinitely)")
	once := fs.Bool("once", false, "handle a single utterance and exit")
	mute := fs.Bool("mute", false, "print the reply instead of speaking it (TTS driver not needed)")
	fs.Parse(reorderFlags(fs, args))

	cfg, err := loadConfigQuietly(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	loc := i18n.Detect()
	if !modelConfigured(cfg) {
		fail(2, "panda: "+i18n.T(loc, "cli.voice.needModel"))
	}

	// SIGINT/SIGTERM cancels the context, which kills the sidecar mid-capture
	// instead of leaving a python process holding the microphone.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	engine, err := askengine.New(ctx, cfg, askengine.Options{
		CardPath:   *cardPath,
		MCPCommand: *mcpCmd,
		ReplyASCII: isLinuxConsole(),
		ConfigPath: *configPath,
		// Voice is a long-lived listening loop: same reasoning as the REPL —
		// peers dial in the background, the mic opens without dead air.
		AsyncPeers: true,
		// Same queue-and-await contract as the REPL: scheduled execution
		// with per-project parallelism, the spoken turn still ends with the
		// outcome. Releasing the loop (SIGINT) detaches the watch, never the
		// work.
		QueueTasks:  true,
		AwaitQueued: true,
	})
	if err != nil {
		fatal("ask engine", err)
	}
	defer engine.Close()

	// Voice shares the console's thread model: it continues the project's most
	// recently updated session and records each spoken exchange back, so a task
	// spoken at the Pi is the same conversation a terminal or the web console
	// opens next. The bare-conversation file is the pre-session fallback.
	voiceProj, _ := activeProject(cfg)
	vt := &voiceThread{
		store:   sessions.NewStore(sessionStoreRoot(cfg)),
		project: voiceProj,
	}
	var history []entry.Turn
	if sess := latestSessionForProject(vt.store, voiceProj); sess != nil {
		vt.sess = sess
		_, history = sessionHistory(ctx, vt.store, sess, engine)
	} else {
		history = loadConvo()
	}

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
				fail(1, "panda: "+i18n.Tf(loc, "cli.voice.unavailable", "err", firstLine(t.Err)))
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

		// The thread exists before the ask, so a remembered approval and the
		// console's live view bind to it from the first spoken turn.
		vt.ensure(text)
		workDir := continueWorkDir("", vt.sess, engine)
		out, err := engine.AskTurnsSession(ctx, history, text, workDir, "", vt.sessionID(), false, askengine.StreamCallbacks{})
		if err != nil {
			vt.discardEmpty()
			fmt.Fprintf(os.Stderr, "panda: %v\n", err)
			if *once {
				os.Exit(1)
			}
			continue
		}
		history = appendConvoTurns(history, loc, text, out, engine)
		vt.record(ctx, engine, text, out, loc)
		speakBack(ctx, loc, out, *mute)
		if *once {
			return
		}
	}
}

// voiceThread is the spoken conversation's session binding: the thread the
// utterances accumulate into, created on the first utterance so approval
// decisions and the console's view bind to it before the ask returns.
type voiceThread struct {
	store   *sessions.Store
	project string
	sess    *sessions.Session
}

func (v *voiceThread) sessionID() string {
	if v == nil || v.sess == nil {
		return ""
	}
	return v.sess.ID
}

func (v *voiceThread) ensure(title string) {
	if v == nil || v.store == nil || v.sess != nil {
		return
	}
	if sess, err := v.store.Create(sessions.Title(title), v.project); err == nil {
		v.sess = sess
	}
}

// discardEmpty drops a thread created for an ask that produced no turn, so a
// failed first utterance does not leave a blank row in the console's rail.
func (v *voiceThread) discardEmpty() {
	if v == nil || v.store == nil || v.sess == nil {
		return
	}
	if cur, err := v.store.Get(v.sess.ID); err == nil && len(cur.Turns) == 0 {
		_ = v.store.Delete(v.sess.ID)
		v.sess = nil
	}
}

func (v *voiceThread) record(ctx context.Context, eng *askengine.Engine, text string, out *askengine.Result, loc i18n.Locale) {
	if v == nil || v.sess == nil || out == nil {
		return
	}
	appendExchangeToSession(v.store, v.sess.ID, text, out, loc)
	if out.Kind == "task" {
		bindTaskToSession(ctx, eng, out.TaskID, v.sess.ID)
	}
}

// speakBack prints the outcome in full and speaks the short form. The two differ
// on purpose: the terminal gets the board and the ids, the speaker gets the one
// sentence a person standing in the room needs.
func speakBack(ctx context.Context, loc i18n.Locale, out *askengine.Result, mute bool) {
	switch out.Kind {
	case "clarify":
		fmt.Println(renderCliMd(out.Answer))
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
	case "clarify":
		return head(strings.TrimSpace(out.Answer), maxSpeakChars)
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
