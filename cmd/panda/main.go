// SPDX-License-Identifier: AGPL-3.0-or-later

// Command panda is the OpenPanda CLI. With no subcommand it drops into the
// interactive REPL (the operator's seat); `panda daemon` runs the headless
// kernel that registers this node's capabilities and delegates/executes
// tasks over WebSocket. Other subcommands: ask (unified entry model), web
// (embedded console), status/queue/task/approve/reject/cancel/logs (one-shot
// panel), version.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/executil"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/log"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/nodeidentity"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/internal/updater"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
)

var version = versionpkg.Version

func main() {
	// A Windows self-update renames the running image to <exe>.old because the
	// locked file cannot be replaced in place; the fresh process sweeps that
	// sidecar here. No-op on unix (the atomic rename-over leaves nothing).
	updater.SweepResidue()
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("panda %s\n", versionpkg.Display())
		return
	}
	// `panda --help` / `panda -h` must show the main help, not be swallowed
	// by parseSubcommand's "leading dash = flag" skip and fall into the REPL.
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		printUsage(os.Stdout)
		return
	}
	args := extractGlobalFlags(os.Args[1:])
	sub, args := parseSubcommand(args)
	// An explicit --config pointing at a file that isn't there used to fall
	// back to defaults silently — the typo then surfaced as a misleading
	// "no API key configured" error. init is exempt: its --config is the
	// write target and must NOT exist yet. The warning names where the node
	// would look without the flag, so "wrong path" and "no config yet" read
	// as different problems.
	if cliConfigPath != "" && sub != "init" {
		if _, err := os.Stat(cliConfigPath); err != nil {
			loc := i18n.Detect()
			fmt.Fprintf(os.Stderr, "panda: %s\n", i18n.Tf(loc, "cli.configMissing",
				"path", cliConfigPath, "err", err.Error(), "usual", config.ResolvePath("")))
		}
	}
	if sub != "" {
		dispatchSub(sub, args)
		return
	}
	// No subcommand: the interactive REPL is the product's front door;
	// the kernel is an explicit `panda daemon` away.
	runRepl(args)
}

// subcommandNames lists every accepted subcommand for typo suggestions and
// shell completion. It is dispatchSub's switch written out once more,
// deliberately: the switch is the dispatcher and must stay a switch (aliases
// share a case), while this is the vocabulary, and a name missing here costs
// a suggestion, not a command.
func subcommandNames() []string {
	return []string{
		"daemon", "serve", "ask", "repl", "chat", "web", "voice",
		"install", "uninstall", "update", "upgrade", "doctor", "status", "nodes", "pair", "queue",
		"task", "plan", "cancel", "approve", "reject", "logs", "skill", "mcp",
		"reminder", "detect", "card", "init", "metrics", "heatmap", "audit", "session",
		"sessions", "memory", "config", "model", "models", "agents", "project",
		"auth", "rpc",
		"read", "view", "cat", "md", "markdown", "version", "help", "completion",
	}
}

// looksLikeSubcommand reports whether a bare argv word could plausibly be a
// mistyped subcommand name: all-lowercase ASCII letters, digits, '-' and '_'.
// A token carrying anything else — CJK, uppercase, punctuation — can never be
// a typo of an all-lowercase command, so routing it to the error path would
// only ever insult a prompt.
func looksLikeSubcommand(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// bareInputIsAsk decides the default branch's fate for an unrecognized first
// word. Two or more real positional words read as a sentence (`fix the bug`)
// and go to ask; so does a single token that cannot spell a command (`你好`,
// `Deploy`, `don't`). A lone lowercase-ASCII word stays an error — typo
// country. Flag tokens and the values of ask's known value-flags don't count
// as words: `panda fix --project x` is a one-word line, not a phrase.
func bareInputIsAsk(sub string, rest []string) bool {
	if !looksLikeSubcommand(sub) {
		return true
	}
	skipNext := false
	for _, a := range rest {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && askValueFlags[name] {
				skipNext = true
			}
			continue
		}
		return true // a second bare word makes it a phrase
	}
	return false
}

// parseSubcommand returns the first bare word (the subcommand) plus the rest
// of argv. Global flags are already gone — extractGlobalFlags lifted them —
// so any leading dash tokens belong to the subcommand (`panda --watch
// queue`) and forward to its FlagSet, which either accepts them or errors,
// instead of being silently dropped here. A value flag before the
// subcommand can't be resolved (its table belongs to the subcommand), so its
// value would read as the subcommand — an ambiguity no dispatcher can lift.
func parseSubcommand(args []string) (string, []string) {
	for i, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" {
			continue // flag token: belongs to the subcommand (or "--")
		}
		rest := append(append([]string{}, args[:i]...), args[i+1:]...)
		return a, rest
	}
	return "", args
}

func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, fmt.Sprintf("path to config.yaml (default %s)", config.SystemConfigPath()))
	cardPath := fs.String("card", cardFlagDefault(), fmt.Sprintf("path to capabilities.yaml (default: discovered — ./capabilities.yaml, next to the resolved config, or %s)", systemCardPath()))
	debugAddr := fs.String("debug-addr", "", "serve net/http/pprof on this loopback address (e.g. 127.0.0.1:6060); empty disables it")
	fs.Parse(reorderFlags(fs, args))
	rejectExtraArgs(fs, "usage: panda daemon [--config F] [--card F] [--debug-addr HOST:PORT]")

	// Before anything looks for an agent CLI: a launchd/systemd/task-scheduler
	// launch inherits a minimal PATH, and the CLIs live in user dirs it never
	// contains — the node would advertise zero agents and peers would route
	// no work to it.
	executil.AugmentProcessPath()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	effectiveIdentity := cfg.Node.EffectiveIdentity()
	identityLock, err := nodeidentity.Acquire(cfg.Node.Kind, effectiveIdentity)
	if err != nil {
		fatal("start node", err)
	}
	defer identityLock.Release()

	// PID file next to the database: `panda card` write commands read it to
	// SIGHUP this process into hot-reloading the card (see notifyDaemonReload
	// in cmd/panda/hotreload_unix.go). Written best-effort — without it the
	// CLI falls back to the restart hint, which is the pre-existing behavior.
	pidPath := filepath.Join(filepath.Dir(cfg.Storage.DBPath), "daemon.pid")
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err == nil {
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err == nil {
			defer os.Remove(pidPath)
		}
	}

	// A foreground daemon's stdout is its operator's console: JSON lines are
	// the right shape for journald and wrong shape for eyes.
	log.SetupTTY(cfg.Log.Level, nil, stdoutIsTTY())
	logger := log.From(context.Background())

	// The pprof listener exists so a stalled node can report itself: heap,
	// goroutine and CPU profiles straight off the running daemon. Loopback
	// only, off unless asked — see debug.go.
	if *debugAddr != "" {
		if err := startDebugServer(*debugAddr); err != nil {
			fatal("daemon --debug-addr", err)
		}
	}

	db, err := openStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	// Load the capability card if configured. A node without a card is
	// still a valid participant for heartbeat testing, but Phase 0
	// requires one to route work.
	var card ledger.Card
	if *cardPath != "" {
		card, err = ledger.LoadCard(*cardPath)
		if err != nil {
			fatal("load capabilities", err)
		}
		// A card copied from another machine can declare commands this host
		// does not have (the shipped examples are POSIX-only). Advertising them
		// would win the native plan and fail at exec, so they go before the
		// card is ever registered or sent in a hello.
		if dropped := card.PruneUnavailableNative(); len(dropped) > 0 {
			logger.Warn("native abilities dropped: command not found on this host",
				"ids", strings.Join(dropped, ","))
		}
		// Same phantom-ability rule for §7.1 actuators: a declared driver
		// that does not resolve would win the plan and fail at exec, so it
		// leaves the card before peers ever see it.
		if dropped := card.PruneUnavailableActuators(); len(dropped) > 0 {
			logger.Warn("actuators dropped: command not found on this host",
				"ids", strings.Join(dropped, ","))
		}
	}
	card.NodeKind = cfg.Node.Kind
	card.NodeIdentity = effectiveIdentity

	runtimeNodeID := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, effectiveIdentity)
	// A stable id must not look ephemeral: scheduler.EphemeralBase strips ANY
	// trailing "-"+8hex, so a node named e.g. "build-deadbeef" would register
	// its row under the full name yet be recognized by peers as "build" —
	// IsSelfRow/SameRuntimeIdentity then alias it onto a different node.
	// Rejecting at startup beats aliasing silently at route time (D-collision).
	if base, ok := scheduler.EphemeralBase(runtimeNodeID); ok {
		fatal("node id", fmt.Errorf("%q ends in an ephemeral-style -8hex suffix; it would alias onto %q — rename the node", runtimeNodeID, base))
	}
	coreNode := core.NewCore(db, runtimeNodeID, card, schedulerTier(cfg.Node.ResourceClass), logger, cfg.Model)
	// This process holds the identity lock for its lifetime — the hello it
	// sends may claim edge authority so a borrowed sibling session holding
	// a peer conn yields when the real daemon redials.
	coreNode.SetEdgeAuthority(true)
	coreNode.SetRouterPolicy(cfg.Injection, cfg.Routing)
	// Extended-policy agent runs expose the node's MCP server to the
	// delegated agent CLI (work-dir .mcp.json); minimal policy ignores it.
	coreNode.SetAgentMCPPassthrough(cfg.MCP.Command)
	// The self-tools MCP server spawned from .mcp.json inherits the daemon's
	// --config choice; empty means its own default discovery.
	coreNode.SetSelfConfigPath(*configPath)
	// Supervision (上级完成度判定): judge agent results against the task's
	// success criteria and re-delegate work that isn't complete. A model-less
	// node skips this — agent tasks finish in one shot as before.
	coreNode.AttachSupervisor(cfg.Model)
	// "在哪里启动哪里就是项目空间" — a daemon launched inside a workspace
	// directory (a VCS root or a manifest-bearing dir) adopts it as the
	// project space: WorkPath becomes the launch dir, a project row is bound
	// to it, and every harness the daemon schedules runs inside the project.
	// An explicit operator choice always wins — OPENPANDA_WORK_PATH or a
	// configured storage.work_path outranks the launch directory.
	workPathExplicit := os.Getenv("OPENPANDA_WORK_PATH") != ""
	if !workPathExplicit && cfg.Storage.WorkPath != "" {
		// A configured path is explicit — unless it is exactly the default
		// UserDataDir (what config.Load materializes when the file never set
		// one). On a UserDataDir failure err on the explicit side: a
		// configured path must never be overridden by the launch directory.
		ud, uerr := config.UserDataDir()
		workPathExplicit = uerr != nil ||
			filepath.Clean(cfg.Storage.WorkPath) != filepath.Clean(ud)
	}
	if !workPathExplicit {
		// Only the unambiguous marker set may drive an unattended daemon's
		// adoption: a Makefile/requirements.txt in a stray directory means
		// "project" at a prompt, not a reason to relocate the node's work
		// space. Interactive entry points (ask, repl) keep the wider set via
		// ambientProject → looksLikeWorkspace.
		if cwd, err := os.Getwd(); err == nil && cwd != "" && looksLikeWorkspaceStrong(cwd) {
			abs, _ := filepath.Abs(cwd)
			cfg.Storage.WorkPath = abs
			if adopted := adoptWorkspaceProject(projectstore.NewStore(db), abs); adopted != "" {
				logger.Info("workspace adopted", "dir", abs, "project", adopted)
			}
		}
	}
	// The work dir travels to adapter subprocesses as their cwd (via the
	// sandbox and the adapter request's CWD field), so it must be absolute —
	// a relative path would resolve against the TASK dir inside the adapter
	// and make python's subprocess.run fail instantly.
	if absWork, err := filepath.Abs(cfg.Storage.WorkPath); err == nil {
		coreNode.SetWorkDir(absWork)
		// A work root that is not the node's own data dir is a user-visible
		// workspace (an adopted launch dir, or an explicit work_path): keep
		// panda's runtime scratch — per-task dirs, plan stages, landed remote
		// project copies — inside one hidden .openpanda directory there, the
		// same convention session worktrees already use, rather than tasks/,
		// plans/ and projects/ appearing in the user's tree. Under the
		// default data dir they stay at the top level beside the database.
		if sr := cfg.ScratchRoot(); sr != "" {
			coreNode.SetScratchRoot(sr)
			// The workspace will host .openpanda scratch — keep it out of
			// `git status` via .git/info/exclude so the daemon never dirties
			// the user's checkout nor edits their .gitignore. A non-repo
			// work_path skips silently.
			excludePandaDir(absWork)
		}
	} else {
		coreNode.SetWorkDir(cfg.Storage.WorkPath)
	}
	coreNode.SetHostStatePaths(cfg.HostStatePaths())
	// The node's own config and card are never artifact content: a tree that
	// happens to contain them (a project rooted at the daemon dir, a dev node
	// running work_path="." in its checkout) must not ship the mesh secret
	// or the card to whoever asked for the pack.
	coreNode.SetPackProtectedPaths([]string{*configPath, *cardPath})
	// OS sandbox (sandbox.*): the environment filter always applies; when a
	// mode is configured, every spawned native command and adapter runs under
	// the platform's deny-default confinement on top. The node's own
	// bookkeeping — database dir, memory stores, artifact pool, the config
	// and card files — is write-protected at the sandbox layer too, so a
	// confused task cannot corrupt the very state the drift detector and
	// audit chain verify. cfg.HostStatePaths is NOT reused verbatim: it also
	// lists workPath/.claude, the agent's own project config, and denying
	// that would break the CLI's settings writes mid-run. The deny list is
	// shared with the embedded engine via commander.ProtectedPaths.
	if mode := cfg.Sandbox.NormalizedMode(); mode != "off" {
		protected := commander.ProtectedPaths(cfg, *configPath, *cardPath)
		if backend := commander.SetSandboxConfig(cfg.Sandbox, protected); backend == "" {
			logger.Warn("sandbox mode configured but no backend on this platform",
				"mode", mode)
		} else {
			logger.Info("subprocess sandbox enabled", "mode", mode, "backend", backend)
		}
	}
	// A secret-less node cannot mesh at all: the WS listener refuses to
	// start and discovery stays listen-only. Rather than booting a node
	// that is silently unreachable, mint one here and persist it into the
	// config so the next start (and any `panda nodes`/pair output) sees the
	// same material. The secret creates a standalone mesh domain — a node
	// joining an existing mesh later adopts the inviter's secret via pair.
	if cfg.Network.SharedSecret == "" {
		// Silent rekey guard: a node that HAS a mesh — configured peers, or
		// registered remote nodes in the directory — but no secret almost
		// certainly lost its credential (OPENPANDA_SHARED_SECRET not passed
		// this start, or the file's field was cleared). Minting a fresh
		// secret anyway exiles the node permanently: every configured peer
		// rejects its hello until the ORIGINAL secret is restored. Scream
		// about it, and keep the wrong value off disk — a persisted mismatch
		// would exile the node on every future env-less start too.
		meshed := len(cfg.Network.Peers) > 0 || hasMeshHistory(db, runtimeNodeID)
		if secret, serr := generateSharedSecret(); serr == nil {
			cfg.Network.SharedSecret = secret
			switch {
			case meshed:
				logger.Error("network.shared_secret is empty on a node that has meshed before — "+
					"a fresh in-memory secret was minted and configured peers WILL REJECT it. "+
					"If this node previously joined a mesh via OPENPANDA_SHARED_SECRET or `panda pair`, "+
					"stop it and restore the original secret; pairing again also re-adopts the mesh secret",
					"configured_peers", len(cfg.Network.Peers))
			default:
				if werr := config.UpdateNetworkSection(*configPath,
					config.NetworkConfig{SharedSecret: secret}); werr != nil {
					logger.Warn("generated shared secret could not be persisted (in-memory only)", "err", werr)
				} else {
					logger.Info("generated and saved shared secret", "config", *configPath)
				}
			}
		} else {
			logger.Warn("could not generate shared secret", "err", serr)
		}
	}
	coreNode.SetSharedSecret(cfg.Network.SharedSecret)
	coreNode.SetAllowCleartext(cfg.Network.AllowCleartext)
	coreNode.SetCleartextAllowlist(cfg.Network.AllowCleartextFor)
	coreNode.SetTrustConfig(cfg.Network.Trust)
	coreNode.SetPeerTrustHints(cfg.Network.PeerTrustHints())
	coreNode.SetReputationEnabled(cfg.Routing.Reputation)
	// The artifact pool is the data plane: a stage's packed output, named by its
	// hash, that a later stage on another node pulls over the bus. Without it a
	// delegated task can only carry a path, which means nothing on the node that
	// receives it.
	artifactStore := artifact.NewStore(cfg.Storage.ArtifactPath, cfg.Storage.ArtifactExtraPaths...)
	artifactStore.SetMaxBytes(cfg.Storage.ArtifactMaxBytes)
	artifactStore.SetMinFreeBytes(cfg.Storage.ArtifactMinFreeBytes)
	coreNode.SetArtifactStore(artifactStore)
	coreNode.SetLimits(cfg.Network.MaxConnections, cfg.Network.MaxConnectionsPerIP)
	coreNode.SetRetentionDays(cfg.Storage.EffectiveTaskRetentionDays())
	// DTN contact plan (network.contacts): the scheduled windows custody
	// routing plans around. A malformed window fails startup outright —
	// silently dropping it would strand bundles the operator scheduled.
	if len(cfg.Network.Contacts) > 0 {
		plan := make([]ledger.Contact, 0, len(cfg.Network.Contacts))
		for i, cc := range cfg.Network.Contacts {
			e, err := cc.Resolve()
			if err != nil {
				fatal("network.contacts", fmt.Errorf("entry %d: %w", i, err))
			}
			plan = append(plan, ledger.Contact{
				Peer: e.Peer, Start: e.Start, End: e.End, Period: e.Period, RateBps: e.RateBps,
			})
		}
		coreNode.SetContacts(plan)
	}
	// Execution timeouts (timeouts.*): the agent budget and the task lease. A
	// deep-learning stage runs far longer than a code edit, so both are operator
	// knobs; SetTimeouts also keeps the lease above the agent's hard limit.
	coreNode.SetTimeouts(cfg.Timeouts)
	coreNode.SetMessageBudget(cfg.Messages.Budget)

	// Attach the memory layer (design §17/§8): daily logging that feeds the
	// Dreaming engine, and skill progressive loading. Project memory is no
	// longer injected into agent prompts (A1); the injector instead supplies
	// the A3 memory-file manifest for selective loading. Character caps come
	// from config memory.limits. Load failures degrade gracefully and are
	// logged by the core, not fatal here.
	limits := memoryLimits(cfg)
	hermes := memory.NewHermesWithLimits(cfg.Storage.MemoryPath, limits)
	projects := memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, limits)
	daily := memory.NewDaily(hermes.WarmDir())
	skillStore := skills.NewStore(skillsPathFor(cfg))
	_ = skillStore.EnsureBuiltins()
	coreNode.SetMemoryStores(
		memory.NewInjector(hermes, projects),
		daily,
		skillStore,
	)
	// The project plane: the daemon is the node that receives delegations, so it
	// needs both halves — the table (to find a project's tree) and the memory root
	// (to land a delegated project's memory in).
	coreNode.SetProjectStores(projectstore.NewStore(db), cfg.Storage.ProjectsPath)

	// Web Push lives in the webui sidecar (webui/cmd/panel), not the kernel;
	// the kernel stays headless. See webui/README.md.

	ctx, cancel := shutdownContext()
	defer cancel()

	// Hot reload (阶段 3): SIGHUP re-reads the capability card and rebroadcasts
	// it — the signal `panda card` write commands send after touching the file.
	// The same signal re-reads config.yaml's mutable network half (peers,
	// secret, cleartext policy): `nodes add`/`disconnect`/`pair` signal the
	// daemon after writing, so a peer change is live without a restart. A
	// separate channel, deliberately: folding SIGHUP into the NotifyContext
	// above would make it shut the daemon down, the exact opposite of intent.
	// On Windows the notification simply never fires (the OS does not deliver
	// the signal), so the CLI prints its restart hint instead.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	guard.Go(logger, "daemon: SIGHUP hot reload", cancel, func() {
		for range hup {
			if err := coreNode.ReloadCard(context.Background(), *cardPath); err != nil {
				logger.Warn("reload card on SIGHUP", "err", err)
			}
			reloaded, err := config.Load(*configPath)
			if err != nil {
				logger.Warn("reload config on SIGHUP", "err", err)
				continue
			}
			coreNode.ApplyNetworkConfig(ctx, reloaded.Network)
		}
	})

	// Queue scheduler (panel queue redesign): adopt queued-and-scheduled tasks
	// in policy order (drag seq → priority → FIFO) when resources allow. Runs
	// alongside the daemon so enqueued tasks execute even without the panel.
	// StartQueueScheduler spawns its loop internally; a panic there still
	// crashes the process (internal/core is not wrapped here by design).
	coreNode.StartQueueScheduler(ctx)

	// Dreaming (design §17.3): consolidate the daily logs into long-term memory
	// in the background — only while the node is idle, at most once per day.
	// The same tick also enforces the daily-log retention windows (A4: the
	// production wiring of daily.Prune, once per day, independently of the
	// dream cadence). Promotions land in the audit log (EvMemoryPromotion) so
	// the Web console can show — and correct or delete — what was memorized.
	dreamer := memory.NewDreamer(hermes)
	audit := security.NewAudit(db)
	// P2-9: the dreamer's audit records carry the same attestation as the
	// kernel's. Install the signer before the scheduler goroutine starts —
	// an unsigned row written between goroutine launch and signing would
	// fail VerifyChain's "no unsigned rows after signed" rule.
	if pub, priv, ok := core.LoadNodeKey(db); ok {
		audit.SetSigner(pub, priv)
	}
	dreamer.OnPromotion = func(entry string, viaWhitelist bool) {
		channel := "threshold"
		if viaWhitelist {
			channel = "whitelist"
		}
		if err := audit.Record(ctx, security.Entry{
			Who:    cfg.Node.Name,
			What:   core.EvMemoryPromotion,
			Target: "MEMORY.md",
			Result: "ok",
			Detail: channel + ": " + entry,
		}); err != nil {
			logger.Warn("record memory promotion", "err", err)
		}
	}
	dreamSched := memory.NewScheduler(
		dreamer,
		memory.NewDreamDiary(filepath.Join(cfg.Storage.MemoryPath, "DREAMS.md")),
		func() bool { return coreNode.Idle(ctx) },
		5*time.Minute,
	).WithDaily(daily)
	dreamSched.OnError = func(err error) { logger.Warn("dreaming sweep", "err", err) }
	guard.Go(logger, "daemon: dream scheduler", cancel, func() { dreamSched.Run(ctx) })

	// Reminders (design P1-28): claim due reminders in the background and
	// log them. The web panel runs its own scanner with Web Push + SSE
	// delivery; ClaimDue's atomic claim keeps the two from double-firing.
	reminderScan := reminders.NewScanner(
		reminders.NewStore(db),
		15*time.Second,
		func(r reminders.Reminder) {
			logger.Info("reminder due", "id", r.ID, "message", r.Message,
				"due", time.Unix(r.DueAt, 0).Format(time.RFC3339))
		},
		logger,
	)
	guard.Go(logger, "daemon: reminder scanner", cancel, func() { reminderScan.Run(ctx) })

	// Self-update notices (v0.0.4 follow-up #5): the headless daemon
	// cannot apply updates itself — there is no console to consent
	// from — but it still checks periodically and logs a notice, so an
	// operator reading the daemon log learns a release is waiting
	// instead of discovering it on the next web visit.
	updateNotice := updater.New(updateEnvOptions(updater.Options{
		Current:         version,
		CurrentCodename: versionpkg.Codename,
		Logger:          logger,
		Idle:            func(ctx context.Context) bool { return coreNode.Idle(ctx) },
		OnAvailable: func(v, codename string) {
			disp := "v" + v
			if codename != "" {
				disp += " " + codename
			}
			logger.Info("update available",
				"version", disp,
				"hint", "open the web console (System → Updates) to review the changelog and apply")
		},
	}))
	updateNotice.StartAutoCheck(ctx, 6*time.Hour)

	if err := coreNode.Register(ctx); err != nil {
		fatal("register node", err)
	}
	// Materialize the Ed25519 identity now: a peerless node otherwise only
	// generates it on first hello, leaving its own `panda nodes` fingerprint
	// blank — the very value an operator compares a discovered peer against.
	coreNode.EnsureNodeKey()
	guard.Go(logger, "daemon: heartbeat", cancel, func() { coreNode.RunHeartbeat(ctx) })
	guard.Go(logger, "daemon: monitor", cancel, func() { coreNode.RunMonitor(ctx) })

	if n, err := coreNode.Recover(ctx); err != nil {
		logger.Warn("task recovery failed", "err", err)
	} else if n > 0 {
		logger.Info("recovered tasks from previous run", "count", n)
	}

	// Farsky datagram plane (UDP + NAT punching). "" follows listen_addr's
	// port but binds the wildcard interface: every datagram is AEAD-sealed or
	// HMAC-signed, so an any-interface bind exposes nothing the LAN could not
	// already see — and it is what makes a default loopback-WS node punchable
	// without extra config. "off" disables the socket outright. Started before
	// the peer loops so a "punch:<id>" entry finds the plane already up.
	if udpAddr := cfg.Network.UDPListen; udpAddr != "off" {
		if udpAddr == "" {
			if _, port, err := net.SplitHostPort(cfg.Network.ListenAddr); err == nil {
				udpAddr = ":" + port
			}
		}
		if udpAddr != "" {
			if err := coreNode.ListenUDP(ctx, udpAddr, cfg.Network.STUNServers); err != nil {
				logger.Warn("udp: datagram plane failed to start", "addr", udpAddr, "err", err)
			}
		}
	}

	// LAN discovery: unauthenticated broadcast beacons feeding a pending-join
	// list — the "see a device, then pair it" half of Track 1. Default binds
	// :7837; "off" silences both directions. Datagrams carry no credentials,
	// so the socket neither proves nor admits anything.
	if cfg.Network.DiscoveryAddr != "off" {
		guard.Go(logger, "daemon: discovery", cancel, func() {
			coreNode.RunDiscovery(ctx, cfg.Network.DiscoveryAddrOrDefault(), cfg.Network.ListenAddr, cfg.Network.DiscoveryAutoDial)
		})
	}

	// Configured peer edges: one keepalive goroutine per address, owned by
	// the core so a config reload (SIGHUP or `nodes add`) can re-sync the
	// set without a restart — ws:// / wss:// entries get the dial loop,
	// "punch:<id>" entries get the NAT-pinhole loop.
	// Pairing join hook: when an interactive pair_hello session is
	// confirmed, the initiator's address joins the dial list and applies
	// hot — persisting so the pairing survives restarts exactly like a
	// `nodes add` would have.
	coreNode.SetPairJoinHook(func(peerAddr string) error {
		fresh, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		peers := fresh.Network.Peers
		if !slices.ContainsFunc(peers, func(p config.PeerEntry) bool { return p.Addr == peerAddr }) {
			peers = append(peers, config.PeerEntry{Addr: peerAddr})
		}
		// Secret deliberately omitted: when it arrives via
		// OPENPANDA_SHARED_SECRET, Load() injects it into the struct — and
		// writing the struct back to disk would leak the env-provided
		// material into config.yaml. The hook only needs to persist the
		// peer edge; the secret lives in memory for ApplyNetworkConfig.
		if err := config.UpdateNetworkSection(configWritePath(*configPath), config.NetworkConfig{
			Peers: peers,
		}); err != nil {
			return err
		}
		coreNode.ApplyNetworkConfig(ctx, config.NetworkConfig{
			SharedSecret:      fresh.Network.SharedSecret,
			Peers:             peers,
			AllowCleartext:    fresh.Network.AllowCleartext,
			AllowCleartextFor: fresh.Network.AllowCleartextFor,
			Trust:             fresh.Network.Trust,
		})
		return nil
	})

	coreNode.SyncPeers(ctx, cfg.Network.PeerAddrs())

	logger.Info("panda core started",
		"version", version,
		"node", cfg.Node.Name,
		"node_kind", cfg.Node.Kind,
		"resource_class", cfg.Node.ResourceClass,
		"card", *cardPath,
		"listen", cfg.Network.ListenAddr,
		"db", cfg.Storage.DBPath,
	)

	// The kernel runs headless: the legacy PWA panel is an optional sidecar
	// (webui/cmd/panel), never mounted here. See webui/README.md.

	serveErr := make(chan error, 1)
	// Fail-closed transport auth (design §16 / P0-1): without a shared secret no
	// peer can authenticate, so the WebSocket listener is not started at all —
	// the node runs local-only rather than accepting unauthenticated peers.
	// Reached only when the generation+persist above also failed.
	if cfg.Network.SharedSecret == "" {
		logger.Warn("websocket disabled: network.shared_secret is not set and could not be generated (refusing to accept unauthenticated peers)")
	} else {
		guard.Go(logger, "daemon: websocket listener", cancel, func() {
			for {
				err := coreNode.Listen(ctx, cfg.Network.ListenAddr)
				if err == nil || ctx.Err() != nil {
					serveErr <- err
					return
				}
				// A bind failure here is usually a custody dispute, not a dead
				// end: a same-identity engine that claimed the node row while
				// this daemon was down holds the socket until it exits or
				// demotes itself. Dying hands the box to the stray — keep
				// retrying; the authoritative outbound dials reclaim the
				// edges in the meantime.
				logger.Warn("websocket listen failed, retrying", "err", err)
				select {
				case <-ctx.Done():
					serveErr <- nil
					return
				case <-time.After(5 * time.Second):
				}
			}
		})
	}

	select {
	case <-ctx.Done():
		logger.Info("panda core shutting down")
		// ctx is already cancelled here, so Shutdown gets its own deadline:
		// draining in-flight work with a dead context would abort it instantly.
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		coreNode.Shutdown(shutdownCtx)
		shutdownCancel()
	case err := <-serveErr:
		if err != nil {
			fatal("websocket server", err)
		}
	}
}

// hasMeshHistory reports whether this node's directory ever held a remote
// node — the signal that an empty shared_secret is a lost credential, not a
// first run. The self row is excluded because Register writes it on every
// boot regardless of mesh membership.
func hasMeshHistory(db *sql.DB, selfID string) bool {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(1) FROM employee_cache WHERE id != ?`, selfID).Scan(&n); err != nil {
		return false // can't prove history — stay on the first-run path
	}
	return n > 0
}

// schedulerTier maps a resource class to the DCPS-style scheduler tier used
// in priority scoring. Root scheduler = 10, sub-scheduler = 5, worker = 1.
func schedulerTier(resourceClass string) int {
	switch resourceClass {
	case "Full":
		return 10
	case "Standard":
		return 5
	default:
		return 1
	}
}

// memoryLimits maps the configured memory caps (config memory.limits) into
// the memory package's Limits, so the daemon, the REPL and `panda web` all
// enforce the same values; zero fields fall back inside the memory package.
func memoryLimits(cfg *config.Config) memory.Limits {
	return memory.Limits{
		User:    cfg.Memory.Limits.User,
		Memory:  cfg.Memory.Limits.Memory,
		Project: cfg.Memory.Limits.Project,
	}
}

func fatal(step string, err error) {
	fmt.Fprintf(os.Stderr, "panda: %s: %v\n", step, err)
	os.Exit(1)
}

// fatalMsg is fatal for a rejected argument rather than a failed operation:
// there is no wrapped error to print, only the sentence that tells the user what
// to type instead. Exit 2 is the usage code the flag parser and the unknown-
// subcommand path already use, so a script can tell "you asked wrong" (2) from
// "it went wrong" (1).
func fatalMsg(msg string) {
	fmt.Fprintf(os.Stderr, "panda: %s\n", msg)
	os.Exit(2)
}

// printVersion implements `panda version [--json]`. The JSON form reports the
// schema ceiling (storage.LatestVersion) alongside the build version — the
// self-updater probes a staged binary with it before swapping, so a release
// that cannot open the current database is refused instead of installed.
// --json is a global flag (extractGlobalFlags strips it into jsonOutput
// before args reach here), so the check reads the global, not args.
func printVersion(_ []string) {
	if jsonOutput {
		info := map[string]any{
			"version":  versionpkg.Version,
			"codename": versionpkg.Codename,
			"schema":   storage.LatestVersion(),
		}
		// db_schema lets a probe (self-updater, install.sh) learn the data
		// directory's current schema from this binary alone — the value that
		// decides whether a staged downgrade would still open the store.
		// Best-effort: absent when the config/DB is unreadable or absent.
		if floor, err := currentSchemaFloor(); err == nil {
			info["db_schema"] = floor
		}
		emitJSON(info)
		return
	}
	fmt.Printf("panda %s\n", versionpkg.Display())
}

// printUsage lists the subcommands as a grouped command tree — `panda help`
// should orient a first-time user, not just enumerate words. Every line goes
// through styleHelpLine, which tints headings and the typeable part of each
// entry (see ui.go); on a non-colour stream it is the same plain text as before.
//
// Command spellings stay literal (they must be typed verbatim); descriptions
// come from i18n (internal/i18n/help.go). A "\n" inside a description is a
// hard wrap: each segment lands under the entry's own continuation indent, so
// a translation may wrap wherever its word lengths need rather than fitting
// the English hand-wrap.
func printUsage(w *os.File) {
	loc := i18n.Detect()
	p := palFor(w)
	line := func(s string) { fmt.Fprintln(w, styleHelpLine(p, s)) }
	t := func(key string) string { return i18n.T(loc, key) }
	// h prints one entry: cmd padded to the description column, followed by
	// the description's first segment; "\n" segments continue under the same
	// column. Commands are ASCII, so byte-width padding is display-correct —
	// and a command as long as the column still gets a one-space gap.
	h := func(cmd, key string, col int) {
		for i, seg := range strings.Split(t(key), "\n") {
			if i == 0 && cmd != "" {
				pad := max(col, len(cmd)+1)
				line(fmt.Sprintf("%-*s%s", pad, cmd, seg))
			} else {
				line(strings.Repeat(" ", col) + seg)
			}
		}
	}
	const (
		narrow = 25 // runtime entries' description column
		wide   = 44 // entries whose command spellings are longer
	)

	line(t("cli.help.title"))
	line("")
	line(t("cli.help.g.runtime"))
	h("  (no subcommand)", "cli.help.d.nosub", narrow)
	h("  daemon", "cli.help.d.daemon", narrow)
	h("  nodes", "cli.help.d.nodes", narrow)
	h("  nodes add <host:port>", "cli.help.d.nodes_add", narrow)
	h("  nodes invite", "cli.help.d.nodes_invite", narrow)
	h("  nodes admit <id>", "cli.help.d.nodes_admit", narrow)
	h("  nodes verify <id>", "cli.help.d.nodes_verify", narrow)
	h("  nodes drain [id]", "cli.help.d.nodes_drain", narrow)
	h("  nodes prune", "cli.help.d.nodes_prune", narrow)
	h("  nodes disconnect <a>", "cli.help.d.nodes_disconnect", narrow)
	line("  pair --secret S --peer <host:port>")
	h("", "cli.help.d.pair", narrow)
	h("  ask <text>", "cli.help.d.ask", narrow)
	h("  repl", "cli.help.d.repl", narrow)
	h("  web", "cli.help.d.web", narrow)
	h("       --lan", "cli.help.d.web_lan", narrow)
	h("  voice [--once] [--mute]", "cli.help.d.voice", narrow)
	h("  read <file> | <file.md>", "cli.help.d.read", narrow)
	line("")
	line(t("cli.help.g.sessions"))
	line("  session list|new|show|rm|ask|diff|merge")
	h("          |fork|tree", "cli.help.d.session", wide)
	line("")
	line(t("cli.help.g.tasks"))
	h("  queue [--state s] [--project p] [--watch]", "cli.help.d.queue", wide)
	h("  task <id>", "cli.help.d.task", wide)
	line("  task add --title T [--prompt P] [--priority low|medium|normal|high|critical]")
	h("           [--project p] [--authorize]", "cli.help.d.task_add", wide)
	h("           [--wait [--wait-timeout 30m]]", "cli.help.d.task_wait", wide)
	line("           [--agents a,b] [--mode parallel|serial]")
	h("", "cli.help.d.task_agents", wide)
	h("  task priority <id> <level>", "cli.help.d.task_priority", wide)
	h("  task move <id> <seq>", "cli.help.d.task_move", wide)
	h("  task artifact <id> [--list|--file|--out]", "cli.help.d.task_artifact", wide)
	h("  task board <id>", "cli.help.d.task_board", wide)
	h("  cancel|approve|reject|logs <id>", "cli.help.d.task_verbs", wide)
	h("  plan run <file.yaml> [--dry-run]", "cli.help.d.plan_run", wide)
	h("  plan show <plan-id>", "cli.help.d.plan_show", wide)
	h("  project list|create", "cli.help.d.project", wide)
	line("")
	line(t("cli.help.g.memory"))
	h("  memory list|get|set|rm [name]", "cli.help.d.memory", wide)
	line("")
	line(t("cli.help.g.settings"))
	h("  model [status|list|add|rm|switch|test]", "cli.help.d.model", wide)
	line("  config model|mcp|limits|routing|injection|approval get|set|test")
	h("", "cli.help.d.config", wide)
	h("  agents [test <name>]", "cli.help.d.agents", wide)
	h("  reminder list|add|rm", "cli.help.d.reminder", wide)
	h("  skill list|find|hub|add|reset", "cli.help.d.skill", wide)
	h("  mcp", "cli.help.d.mcp", wide)
	h("  rpc", "cli.help.d.rpc", wide)
	h("  auth login|status|logout <provider>", "cli.help.d.auth", wide)
	line("")
	line(t("cli.help.g.observability"))
	h("  status", "cli.help.d.status", wide)
	h("  metrics [--csv|--runtime]", "cli.help.d.metrics", wide)
	h("  heatmap [--weeks N]", "cli.help.d.heatmap", wide)
	h("  audit verify [--task id]", "cli.help.d.audit_verify", wide)
	h("  audit entries [--task id]", "cli.help.d.audit_entries", wide)
	line("")
	line(t("cli.help.g.setup"))
	h("  install|uninstall", "cli.help.d.install", wide)
	h("  update [check|apply] | upgrade", "cli.help.d.update", wide)
	h("  init [--defaults|--non-interactive]", "cli.help.d.init", wide)
	h("  doctor", "cli.help.d.doctor", wide)
	h("  detect", "cli.help.d.detect", wide)
	h("  card show|rescan|edit|set", "cli.help.d.card", wide)
	h("  card native|agent|manual add|remove|set", "cli.help.d.card_struct", wide)
	h("  version|help", "cli.help.d.version", wide)
	h("  completion bash|zsh|fish|powershell", "cli.help.d.completion", wide)
	line("")
	line(t("cli.help.globalflags"))
	line("              " + t("cli.help.globalflags2"))
}
