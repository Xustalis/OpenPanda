// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// dispatchSub routes the parsed subcommand to its runner. It is the
// dispatcher as a switch, deliberately — aliases share a case, and the
// one-command-per-line shape stays the cheapest read of the CLI's surface.
// Every handled case runs its runner and returns to main, which returns;
// the default branch either finds a file/bare-prompt route or exits 2 on a
// real typo. subcommandNames() is the same list written out as data for
// `suggest` and shell completion — a name missing there costs a suggestion,
// not a command.
func dispatchSub(sub string, args []string) {
	switch sub {
	case "daemon", "serve":
		runDaemon(args)
	case "ask":
		runAsk(args)
	case "repl", "chat":
		runRepl(args)
	case "web":
		runWeb(args)
	case "voice":
		runVoice(args)
	case "install":
		runInstall(args)
	case "uninstall":
		runUninstall(args)
	case "update":
		runUpdate(args)
	case "upgrade":
		runUpgrade(args)
	case "doctor":
		runDoctor(args)
	case "status":
		runStatus(args)
	case "nodes":
		// Verbs that rewrite the peer list live in dispatchNodes; `remove`
		// drops a stale directory row; `verify` stamps a fingerprint the human
		// has compared out-of-band; bare `nodes` lists the fleet.
		if !dispatchNodes(args) {
			runStatus(args)
		}
	case "pair":
		runPair(args)
	case "queue":
		runQueue(args)
	case "task":
		runTask(args)
	case "plan":
		runPlan(args)
	case "cancel":
		runCancel(args)
	case "approve":
		runApprove(args)
	case "reject":
		runReject(args)
	case "logs":
		runLogs(args)
	case "skill":
		runSkill(args)
	case "mcp":
		runMCP(args)
	case "auth":
		runAuth(args)
	case "rpc":
		runRPC(args)
	case "reminder":
		runReminder(args)
	case "detect":
		runDetect(args)
	case "card":
		runCard(args)
	case "init":
		runInit(args)
	case "metrics":
		runMetrics(args)
	case "heatmap":
		runHeatmap(args)
	case "audit":
		runAudit(args)
	case "session", "sessions":
		runSession(args)
	case "memory":
		runMemory(args)
	case "config":
		runConfig(args)
	case "model", "models":
		runModel(args)
	case "agents":
		runAgents(args)
	case "project":
		runProject(args)
	case "version":
		printVersion(args)
	case "completion":
		runCompletion(args)
	case "read", "view", "cat", "md", "markdown":
		runRead(args)
	case "help", "-h", "--help":
		printUsage(os.Stdout)
	default:
		// If the argument points to an existing file on disk (e.g. `panda README.md`),
		// automatically read and render it without requiring a subcommand.
		if fi, err := os.Stat(sub); err == nil && !fi.IsDir() {
			runRead(append([]string{sub}, args...))
			return
		}

		// Bare-input routing: words that can't be a command get treated
		// as the prompt they almost surely are (`panda fix the bug`,
		// `panda 你好`), while a single all-lowercase ASCII word stays a
		// probable typo and keeps the did-you-mean error below — the one
		// shape where "send it to the model" would surprise.
		if bareInputIsAsk(sub, args) {
			runAsk(append([]string{sub}, args...))
			return
		}

		// A bare unknown word must not silently fall through (P1-25):
		// "panda statsu" (a typo) should neither start the REPL nor a
		// resident daemon — name the fix instead. When the word is one
		// typo away from a real subcommand, say which: that is the whole
		// difference between a dead end and a correction.
		//
		// The correction is the message. This used to print the whole
		// command tree underneath, which pushed the "did you mean status?"
		// line off a short terminal — burying the answer in the reference
		// manual. One line naming `panda help` keeps the reference one
		// keystroke away without making every typo cost sixty lines.
		loc := i18n.Detect()
		p := palFor(os.Stderr)
		fmt.Fprintf(os.Stderr, "panda: %s %s\n", i18n.T(loc, "cli.unknownSub"), p.Command(sub))
		if s := suggest(sub, subcommandNames()); s != "" {
			fmt.Fprintf(os.Stderr, "  %s\n", i18n.Tf(loc, "repl.didyoumean", "cmd", p.Command(s)))
		}
		fmt.Fprintf(os.Stderr, "  %s\n", p.Muted(i18n.T(loc, "cli.unknownSub.askHint")))
		fmt.Fprintf(os.Stderr, "  %s\n", p.Muted(i18n.T(loc, "cli.help.more")))
		os.Exit(2)
	}
}

// dispatchNodes routes a `nodes` verb to its runner and reports whether the
// verb was recognized. Bare `nodes` (no verb, or an unrecognized one) is not
// dispatched here — the caller lists the fleet via runStatus.
func dispatchNodes(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "add":
		runNodesAdd(args[1:])
	case "disconnect", "dc":
		runNodesDisconnect(args[1:])
	case "invite":
		runNodesInvite(args[1:])
	case "remove", "rm":
		runNodeRemove(args[1:])
	case "prune":
		runNodePrune(args[1:])
	case "verify":
		runNodesVerify(args[1:])
	case "trust":
		runNodesTrust(args[1:])
	case "admit":
		runNodesAdmit(args[1:])
	case "drain":
		runNodesDrain(args[1:])
	default:
		return false
	}
	return true
}
