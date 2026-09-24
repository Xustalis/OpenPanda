package main

// The interactive REPL — the operator's seat on top of the kernel. Slash
// commands cover the panel surfaces (queue, task, sessions, memory, config,
// agents, policy) and /web boots the embedded web console in-process;
// anything that is not a slash command goes straight to the ask engine, the
// same unified entry as `panda ask` and POST /api/ask. All user-facing
// strings come from internal/i18n (five languages, English fallback).
//
// TUI model (zero new dependencies, hand-rolled on x/sys termios): a startup
// banner box, a status footer before every prompt, raw-mode line editing
// with Tab completion of slash commands, Esc/Ctrl-C to interrupt a running
// ask (double Ctrl-C exits), and a paged /help. A Bubble Tea stack was
// weighed and rejected: the surface is a prompt + status line, and x/sys is
// already a dependency.

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/carddetect"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/updater"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
	"github.com/Xustalis/OpenPanda/webui/panel"
	"github.com/Xustalis/OpenPanda/webui/push"
)

// repl carries the session state: the shared store handles, the optional ask
// engine, the tier-2 authorization switch, the active locale, the terminal
// layer, and the /web server once booted.
type repl struct {
	loc         i18n.Locale
	cfg         *config.Config
	configPath  string
	db          *sql.DB
	store       *core.TaskStore
	projects    *memory.Projects
	projStore   *projectstore.Store
	hermes      *memory.Hermes
	sessionsSt  *sessions.Store
	worktrees   *sessions.Worktrees
	engine      *askengine.Engine
	cardPath    string
	hasCard     bool
	authorize   bool
	interactive bool
	quit        bool
	webSrv      *http.Server
	webURL      string
	webToken    string
	term        *termSession
	activeSess  string
	push        *push.Service

	// activeProj is the in-memory active-project pointer, guarded by projMu:
	// the task watcher reads it on its own goroutine every poll while the
	// front end writes it on project switches.
	projMu     sync.RWMutex
	activeProj string

	// Conversation memory for bare (session-less) mode: every ask's prompt
	// and outcome accumulate here so follow-up questions keep context — the
	// multi-turn UX users expect from a chat, without /resume-ing a session.
	convo []entry.Turn
	// lastFooter is the footer as last printed; an identical one is skipped
	// (printFooter runs before every prompt).
	lastFooter string
	// Session cost, accumulated across every ask this run and reported by
	// /cost. The provider reports zero tokens for endpoints that send no
	// usage block; the turn count and model time are always meaningful.
	costTurns    int
	costIn       int64
	costOut      int64
	costWall     time.Duration
	costTotalUSD float64
	// watcher bookkeeping (repl_watch.go): asking=true suppresses
	// completion notifications while an inline ask is mid-flight (it prints
	// its own result); baseline is the last-seen task state fingerprint;
	// stallNoted remembers which "task id|state" pairs already got a
	// no-progress warning so a stuck task announces once, not every poll.
	watchMu    sync.Mutex
	asking     bool
	baseline   map[string]string
	stallNoted map[string]time.Time

	// commandMu serializes classic slash/shell dispatch and scopes its context
	// and explicit streams. The TUI supplies a cancellable context and a writer
	// that emits Bubble Tea messages; the classic loop uses the process streams.
	// No command ever swaps process-global stdout or stderr.
	commandMu     sync.Mutex
	commandCtx    context.Context
	commandIn     io.Reader
	commandOut    io.Writer
	commandErrOut io.Writer

	// Tab-completion caches (repl_complete.go). The line editor recomputes
	// its candidate menu on every keystroke, so the state lookups behind
	// argument completion are memoized for a couple of seconds.
	taskIDCache  argCache
	sessionCache argCache
	projectCache argCache
	memoryCache  argCache
}

// replCmd is one slash command: a name, the help group it is listed under
// (see repl_help.go), the i18n key of its help line, and its handler (arg is
// everything after the command name, trimmed).
type replCmd struct {
	name  string
	group string
	help  string
	run   func(r *repl, arg string)
}

// replCommands is the dispatch table in help-display order. Populated in
// init() because cmdHelp iterates it (a composite literal would cycle).
var replCommands []replCmd

func init() {
	replCommands = []replCmd{
		{"ask", "chat", "cmd.ask", (*repl).cmdAsk},
		{"goal", "chat", "cmd.goal", (*repl).cmdModeGoal},
		{"plan", "chat", "cmd.planMode", (*repl).cmdModePlan},
		{"spec", "chat", "cmd.spec", (*repl).cmdModeSpec},
		{"new", "chat", "cmd.new", (*repl).cmdNew},
		{"history", "chat", "cmd.history", (*repl).cmdHistory},
		{"export", "chat", "cmd.export", (*repl).cmdExport},
		{"cost", "chat", "cmd.cost", (*repl).cmdCost},
		{"model", "chat", "cmd.model", (*repl).cmdModel},
		{"sessions", "chat", "cmd.sessions", (*repl).cmdSessions},
		{"resume", "chat", "cmd.resume", (*repl).cmdResume},
		{"clear", "chat", "cmd.clear", (*repl).cmdClear},
		{"tasks", "tasks", "cmd.tasks", (*repl).cmdTasks},
		{"task", "tasks", "cmd.task", (*repl).cmdTask},
		{"cancel", "tasks", "cmd.cancel", (*repl).cmdCancel},
		{"delete", "tasks", "cmd.delete", (*repl).cmdDelete},
		{"approve", "tasks", "cmd.approve", (*repl).cmdApprove},
		{"reject", "tasks", "cmd.reject", (*repl).cmdReject},
		{"logs", "tasks", "cmd.logs", (*repl).cmdLogs},
		{"heatmap", "tasks", "cmd.heatmap", (*repl).cmdHeatmap},
		{"memory", "memory", "cmd.memory", (*repl).cmdMemory},
		{"projects", "memory", "cmd.projects", (*repl).cmdProjects},
		{"project", "memory", "cmd.project", (*repl).cmdProjectEnter},
		{"context", "memory", "cmd.context", (*repl).cmdContext},
		{"skills", "memory", "cmd.skills", (*repl).cmdSkills},
		{"skill", "memory", "cmd.skills", (*repl).cmdSkills},
		{"nodes", "system", "cmd.nodes", (*repl).cmdNodes},
		{"card", "system", "cmd.card", (*repl).cmdCard},
		{"agents", "system", "cmd.agents", (*repl).cmdAgents},
		{"config", "system", "cmd.config", (*repl).cmdConfig},
		{"policy", "system", "cmd.policy", (*repl).cmdPolicy},
		{"doctor", "system", "cmd.doctor", (*repl).cmdDoctor},
		{"web", "system", "cmd.web", (*repl).cmdWeb},
		{"authorize", "system", "cmd.authorize", (*repl).cmdAuthorize},
		{"approval", "system", "cmd.approval", (*repl).cmdApproval},
		{"lang", "system", "cmd.lang", (*repl).cmdLang},
		{"version", "system", "cmd.version", (*repl).cmdVersion},
		{"read", "system", "cmd.read", (*repl).cmdRead},
		{"view", "system", "cmd.read", (*repl).cmdRead},
		{"cat", "system", "cmd.read", (*repl).cmdRead},
		{"md", "system", "cmd.read", (*repl).cmdRead},
		{"help", "system", "cmd.help", (*repl).cmdHelp},
		{"quit", "system", "cmd.quit", (*repl).cmdQuit},
	}
}

// runRepl implements `panda repl` — the interactive shell over the same
// SQLite store the daemon and the webui sidecar use. It never starts the
// daemon loop; task execution happens through the ask engine exactly like
// `panda ask` (and needs --card for the same reason).
func runRepl(args []string) {
	fs := flag.NewFlagSet("repl", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	cardPath := fs.String("card", cardFlagDefault(), fmt.Sprintf("path to capabilities.yaml (default: discovered ./capabilities.yaml or %s)", systemCardPath()))
	mcpCmd := fs.String("mcp", cliMCP, "MCP server command (space-separated)")
	yesFlag := fs.Bool("yes", false, "accept workspace confirmation without prompting")
	fs.BoolVar(yesFlag, "y", false, "accept workspace confirmation without prompting (shorthand)")
	fs.Parse(args)

	cfg, err := loadConfigQuietly(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	interactive := stdinIsTTY()
	detected := i18n.Detect()
	// A locale persisted by /lang (config ui.locale) wins over the ambient
	// LANG/LC_* detection — it is an explicit choice — but OPENPANDA_LANG is
	// the stronger, per-invocation override, so it keeps precedence.
	if os.Getenv("OPENPANDA_LANG") == "" {
		if saved := i18n.Parse(cfg.UI.Locale); saved != "" {
			detected = saved
		}
	}
	if isLinuxConsole() {
		detected = "en" // console font has no CJK glyphs; keep every UI line readable
	}

	cwd, _ := os.Getwd()
	workspaceAllowed := false
	isTUI := interactive && stdoutIsTTY() && os.Getenv("PANDA_CLASSIC_REPL") == ""
	if *yesFlag || !interactive || isTUI {
		workspaceAllowed = true
	} else if cwd != "" {
		fmt.Print(i18n.Tf(detected, "cli.workspace.prompt", "path", cwd))
		var ans string
		if _, err := fmt.Scanln(&ans); err != nil && ans == "" {
			// empty line = default Yes (Y/n)
			workspaceAllowed = true
		} else {
			ans = strings.ToLower(strings.TrimSpace(ans))
			if ans == "" || ans == "y" || ans == "yes" {
				workspaceAllowed = true
			}
		}
	}

	projStore := projectstore.NewStore(db)
	var activeProjectName string
	if workspaceAllowed && cwd != "" {
		cfg.Storage.WorkPath = cwd
		if existing, err := projStore.FindByWorkDir(cwd); err == nil {
			activeProjectName = existing.Name
		} else {
			base := filepath.Base(cwd)
			if base == "/" || base == "." || base == "" {
				base = "workspace"
			}
			cleanBase := ""
			for _, ch := range base {
				if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' {
					cleanBase += string(ch)
				}
			}
			if cleanBase == "" {
				cleanBase = "workspace"
			}
			candidate := cleanBase
			for i := 1; i <= 100; i++ {
				if pr, err := projStore.Get(candidate); err == nil {
					if pr.WorkDir == "" {
						_, _ = projStore.Update(candidate, cwd, "Workspace at "+cwd)
						activeProjectName = candidate
						break
					}
					candidate = fmt.Sprintf("%s-%d", cleanBase, i+1)
				} else {
					if created, err := projStore.Create(candidate, cwd, "Workspace at "+cwd); err == nil {
						activeProjectName = created.Name
						break
					}
				}
			}
		}
		if activeProjectName != "" {
			_ = projStore.SetActive(activeProjectName)
			if interactive && !*yesFlag && !isTUI {
				fmt.Println(pal().Muted(pal().MarkBullet() + " " + i18n.Tf(detected, "cli.workspace.accepted", "path", cwd, "name", activeProjectName)))
			}
		}
	} else if !workspaceAllowed && cwd != "" && interactive {
		fmt.Println(pal().Muted(pal().MarkBullet() + " " + i18n.T(detected, "cli.workspace.declined")))
	}

	effectiveCardPath := *cardPath
	if effectiveCardPath == "" {
		effectiveCardPath = ensureDefaultCardPath()
	} else {
		_, _, _ = carddetect.EnsureCard(effectiveCardPath)
	}

	r := &repl{
		loc:         detected,
		cfg:         cfg,
		configPath:  config.ResolvePath(*configPath),
		db:          db,
		store:       store,
		projects:    memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, memoryLimits(cfg)),
		projStore:   projStore,
		activeProj:  activeProjectName,
		hermes:      memory.NewHermesWithLimits(cfg.Storage.MemoryPath, memoryLimits(cfg)),
		sessionsSt:  sessions.NewStore(sessionStoreRoot(cfg)),
		worktrees:   openWorktreesBestEffort(cfg.Storage.WorkPath),
		cardPath:    effectiveCardPath,
		hasCard:     effectiveCardPath != "",
		interactive: interactive,
	}
	if activeProjectName != "" && r.projects != nil {
		_ = r.projects.Save(activeProjectName, memory.MemFile{Limit: r.projects.Limit()})
	}
	if interactive {
		r.term = newTermSession()
		if r.term != nil {
			r.term.loc = r.loc               // the editor labels its own search line
			r.term.argHint = r.argCandidates // …and completes ids, not just command names
			if cliStateDir() != "" {
				_ = os.MkdirAll(cliStateDir(), 0o700)
				r.term.initHistory(filepath.Join(cliStateDir(), "history"))
			}
		}
	}

	// Web Push (optional) — same contract as `panda web` so /web serves the
	// full console.
	if cfg.Push.Enabled {
		if keys, err := push.LoadOrCreateVAPIDKeys(cfg.Push.VAPIDKeyPath, cfg.Push.VAPIDSubject); err == nil {
			r.push = push.NewService(keys, push.NewStore(db), nil)
		}
	}

	// The ask engine is optional: without a model endpoint the REPL still
	// serves every panel command, and asks explain themselves.
	if cfg.Model.BaseURL != "" {
		engine, err := askengine.New(context.Background(), cfg, askengine.Options{
			CardPath:   effectiveCardPath,
			MCPCommand: *mcpCmd,
			ReplyASCII: isLinuxConsole(),
			Locale:     detected,
			// The session is long-lived and interactive: peers dial in the
			// background instead of gating the banner (an offline peer's dial
			// timeout is routine, not 10s of dead air before the first prompt).
			AsyncPeers: true,
		})
		if err != nil {
			fatal("ask engine", err)
		}
		defer engine.Close()
		r.engine = engine
		// The REPL inherits the project the user entered, so the first ask of a
		// sitting already belongs to it. /project switches it mid-session.
		r.bindProject()
	}

	// The rich full-screen front end (Bubble Tea) drives an interactive TTY with
	// a configured engine; it replaces the banner, footer, task watcher and line
	// loop below with a managed display. PANDA_CLASSIC_REPL falls back here.
	if shouldUseTUI(r) {
		runTUI(r)
		if r.webSrv != nil {
			sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = r.webSrv.Shutdown(sctx)
			scancel()
			r.webSrv = nil
		}
		return
	}

	r.printBanner()
	if r.engine != nil && !r.hasCard {
		fmt.Println(i18n.T(r.loc, "repl.ask.noCard"))
	}

	// Task watcher (interactive only): poll the store's task fingerprint
	// and surface out-of-band completions while the user idles at the
	// prompt — tasks that finished elsewhere (web console, queue scheduler,
	// another node's delegation) report themselves instead of sitting
	// silent until the next /tasks.
	if r.interactive {
		watchCtx, watchCancel := context.WithCancel(context.Background())
		defer watchCancel()
		go r.watchTasks(watchCtx)
	}

	// Resume the persisted bare-mode conversation (people reopen
	// terminals, not conversations); /new starts a fresh one.
	if c := loadConvo(); len(c) > 0 {
		r.convo = c
		fmt.Println(i18n.Tf(r.loc, "repl.convo.resumed", "n", fmt.Sprint(len(c)/2)))
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for !r.quit {
		if r.interactive {
			r.printFooter()
		}
		var line string
		if r.interactive && r.term != nil {
			l, err := r.term.readLine(i18n.T(r.loc, "repl.prompt"), r.slashNames())
			if err != nil {
				break // EOF (Ctrl-D)
			}
			line = l
		} else {
			if r.interactive {
				fmt.Print(i18n.T(r.loc, "repl.prompt"))
			}
			if !sc.Scan() {
				break // EOF (Ctrl-D)
			}
			line = sc.Text()
		}
		r.dispatch(line)
	}
	if r.quit || r.interactive {
		fmt.Println(i18n.T(r.loc, "repl.bye"))
	}
	if r.term != nil {
		r.term.restore()
	}
	if r.webSrv != nil {
		sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = r.webSrv.Shutdown(sctx)
		scancel()
		r.webSrv = nil
	}
}

// slashNames lists "/name" candidates for Tab completion.
func (r *repl) slashNames() []string {
	names := make([]string, 0, len(replCommands)+1)
	for _, c := range replCommands {
		names = append(names, "/"+c.name)
	}
	names = append(names, "/exit")
	return names
}

// figletFont is the classic figlet "standard" lettering subset used by the
// banner, each glyph a fixed 8-column 5-row cell — pure ASCII, so the wordmark
// renders identically on iTerm2 and a bare Linux console.
var figletFont = map[rune][]string{
	'O': {"  ___   ", " / _ \\  ", "| | | | ", "| |_| | ", " \\___/  "},
	'p': {" _ __   ", "| '_ \\  ", "| |_) | ", "| .__/  ", "|_|     "},
	'e': {"        ", "  ___   ", " / _ \\  ", "|  __/  ", " \\___|  "},
	'n': {"        ", " _ __   ", "| '_ \\  ", "| | | | ", "|_| |_| "},
	'P': {" ____   ", "|  _ \\  ", "| |_) | ", "| __/   ", "|_|     "},
	'a': {"        ", "  __ _  ", " / _` | ", "| (_| | ", " \\__,_| "},
	'd': {"     | |", "  __| | ", " / _` | ", "| (_| | ", " \\__,_| "},
}

// figlet renders word in the 5-row lettering above (unknown runes render as
// blanks); the caller prints each returned line.
func figlet(word string) []string {
	rows := make([]string, 5)
	for _, ch := range word {
		glyph, ok := figletFont[ch]
		if !ok {
			glyph = []string{"        ", "        ", "        ", "        ", "        "}
		}
		for i := 0; i < 5; i++ {
			rows[i] += glyph[i]
		}
	}
	return rows
}

// printBanner draws the startup screen: the OpenPanda wordmark in figlet
// lettering, then node/model/workdir info lines and orientation hints.
// Implementation lives behind the lite split: banner_full.go renders the
// themed banner in a full build, tui_lite.go a one-line greeting.

// printFooter prints the status line above the prompt: node name, approval
// mode (color-coded), authorization state, and the active session.
//
// It prints only when something in it changed. Reprinting an identical line
// before every prompt was pure noise — three quarters of a long session's
// scrollback was the same footer — while a change (an /auth toggle, a /resume,
// a turn added to the conversation) is exactly what the user needs to see.
func (r *repl) printFooter() {
	p := pal()
	mode := r.cfg.Approval.NormalizedMode()
	remembered := ""
	if r.engine != nil {
		// The footer reports the EFFECTIVE policy: a project's approval_mode
		// override and a remembered answer both change what the next tier-2
		// action does, so both belong in the readout.
		st := r.engine.ApprovalState(r.activeSess, r.activeProjectName())
		mode = st.Mode
		if st.Decision != "" {
			remembered = "·" + st.Decision + "@" + st.DecisionScope
		}
	}
	mode += remembered
	switch strings.TrimSuffix(mode, remembered) {
	case config.ApprovalModeAlways:
		mode = p.Danger(mode)
	case config.ApprovalModeOnRequest:
		mode = p.Warn(mode)
	default:
		mode = p.Success(mode)
	}
	authz := p.Muted(i18n.T(r.loc, "repl.footer.authz.off"))
	if r.authorize {
		authz = p.Danger(i18n.T(r.loc, "repl.footer.authz.on"))
	}
	sess := "-"
	if r.activeSess != "" {
		sess = r.activeSess
	} else if n := len(r.convo) / 2; n > 0 {
		sess = fmt.Sprintf("chat(%d turns)", n)
	}
	line := p.Muted(fmt.Sprintf("%s:%s  %s:%s  %s:%s  %s:%s",
		i18n.T(r.loc, "repl.footer.node"), r.cfg.Node.Name,
		i18n.T(r.loc, "repl.footer.approval"), mode,
		i18n.T(r.loc, "repl.footer.authz"), authz,
		i18n.T(r.loc, "repl.footer.session"), sess))
	if line == r.lastFooter {
		return
	}
	r.lastFooter = line
	r.outln(line)
}

// dispatch routes one input line: slash commands to the table, `!!` repeats
// the previous ask (the shell habit), `!cmd` runs a shell command in the work
// dir, anything else goes to the ask engine (with @file references expanded
// first). Unknown commands name the closest real one, never exit.
func (r *repl) dispatch(line string) {
	r.dispatchWithIO(context.Background(), line, os.Stdin, os.Stdout, os.Stderr)
}

// dispatchWithIO runs one classic command with request-scoped cancellation and
// streams. Handlers still use the familiar fmt.Print calls, but those calls are
// redirected through package-local writers rather than by mutating os.Stdout or
// os.Stderr. commandMu prevents overlapping dispatches from sharing this scoped
// state; asks have their own streaming path and do not run through the TUI's
// slash-command executor.
func (r *repl) dispatchWithIO(ctx context.Context, line string, in io.Reader, out, errOut io.Writer) {
	if ctx == nil {
		ctx = context.Background()
	}
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = out
	}

	r.commandMu.Lock()
	defer r.commandMu.Unlock()
	prevCtx, prevIn, prevOut, prevErr := r.commandCtx, r.commandIn, r.commandOut, r.commandErrOut
	r.commandCtx, r.commandIn, r.commandOut, r.commandErrOut = ctx, in, out, errOut
	defer func() {
		r.commandCtx, r.commandIn, r.commandOut, r.commandErrOut = prevCtx, prevIn, prevOut, prevErr
	}()

	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if line == "!!" {
		r.repeatLast()
		return
	}
	if strings.HasPrefix(line, "!") {
		r.runShell(strings.TrimSpace(line[1:]))
		return
	}
	if !strings.HasPrefix(line, "/") {
		r.ask(line)
		return
	}
	name, arg, _ := strings.Cut(line[1:], " ")
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "exit" {
		name = "quit"
	}
	if name == "cls" || name == "claer" {
		name = "clear"
	}
	if name == "ver" || name == "v" {
		name = "version"
	}
	for _, c := range replCommands {
		if c.name == name {
			c.run(r, strings.TrimSpace(arg))
			return
		}
	}
	r.outln(i18n.Tf(r.loc, "repl.unknown", "cmd", "/"+name))
	if s := suggest(name, commandNames()); s != "" {
		r.outln("  " + i18n.Tf(r.loc, "repl.didyoumean", "cmd", "/"+s))
	}
}

func (r *repl) commandContext() context.Context {
	if r != nil && r.commandCtx != nil {
		return r.commandCtx
	}
	return context.Background()
}

func (r *repl) commandInput() io.Reader {
	if r != nil && r.commandIn != nil {
		return r.commandIn
	}
	return os.Stdin
}

func (r *repl) commandOutput() io.Writer {
	if r != nil && r.commandOut != nil {
		return r.commandOut
	}
	return os.Stdout
}

func (r *repl) commandError() io.Writer {
	if r != nil && r.commandErrOut != nil {
		return r.commandErrOut
	}
	return os.Stderr
}

func (r *repl) outf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.commandOutput(), format, args...)
}

func (r *repl) errf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.commandError(), format, args...)
}

func (r *repl) outln(args ...any) {
	_, _ = fmt.Fprintln(r.commandOutput(), args...)
}

// askContext derives the conversation history and working directory for one
// prompt, and records the user's turn where it belongs. With an active session
// it replays the thread as it stands (so the model sees the full history the
// web console would send) in the session's worktree and persists the fresh
// turn; a stale session id drops silently back to bare mode. The persisted
// turn never enters the replayed history: AskTurns carries it as the prompt,
// so replaying it too would send two consecutive user messages — which the
// Messages API rejects with a 400. In bare mode it replays
// this run's in-memory conversation and leaves the turn to be paired with its
// answer by recordOutcome.
//
// Both front ends (the classic loop and the Bubble Tea TUI) call this, so
// /resume binds a session for either one instead of only the loop it was typed
// in.
func (r *repl) askContext(text string) ([]entry.Turn, string) {
	var history []entry.Turn
	workDir := ""
	if r.activeSess != "" && r.sessionsSt != nil {
		sess, err := r.sessionsSt.Get(r.activeSess)
		if err != nil {
			r.activeSess = "" // stale id: drop silently back to bare mode
		} else {
			for _, t := range sess.Turns {
				history = append(history, entry.Turn{Role: t.Role, Content: t.Text})
			}
			if _, err := r.sessionsSt.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: text}); err == nil {
				workDir = sess.Worktree
				if workDir == "" && r.engine != nil {
					workDir = r.engine.WorkPath()
				}
				return history, workDir
			}
			history = nil // append failed: fall back to bare mode
		}
	}
	return append(history, r.convo...), workDir
}

// recordOutcome persists the assistant side of a finished turn: into the active
// session thread (binding a spawned task to it so the console can follow the
// run), or into this run's in-memory conversation when no session is bound. A
// task or plan stores its id as the turn's ref, which is what /history and the
// console render as a link rather than prose. The stored text is the converged
// report when the ask produced one (the sub-agent round's whole point — the
// next turn replays it as the model's own words), or the pointer summary
// otherwise.
func (r *repl) recordOutcome(ctx context.Context, text string, out *askengine.Result) {
	if out == nil {
		return
	}
	if r.activeSess == "" || r.sessionsSt == nil {
		r.rememberTurn(text, out)
		return
	}
	turn := sessions.Turn{Role: "assistant", Kind: out.Kind, Text: convoSummaryOf(r.loc, out)}
	switch out.Kind {
	case "task":
		turn.Ref = out.TaskID
		if out.TaskID != "" && r.store != nil {
			_ = r.store.SetSessionID(ctx, out.TaskID, r.activeSess)
		}
	case "plan":
		turn.Ref = out.PlanID
	}
	_, _ = r.sessionsSt.AppendTurn(r.activeSess, turn)
}

// currentProject returns the active-project pointer under its lock.
func (r *repl) currentProject() string {
	r.projMu.RLock()
	defer r.projMu.RUnlock()
	return r.activeProj
}

// setProject updates the active-project pointer under its lock.
func (r *repl) setProject(name string) {
	r.projMu.Lock()
	r.activeProj = name
	r.projMu.Unlock()
}

// recordErrorTurn persists a failed turn. In session mode it appends the
// assistant side, mirroring the panel and `session ask` error paths: without
// it the thread ends on a dangling user turn (askContext persists the
// question before the ask runs) and the next ask replays two consecutive
// user messages — a provider 400 that then poisons every following turn.
// In bare mode the user turn was never persisted either (it waits for the
// answer pairing), so the whole failed exchange lands here as one pair —
// otherwise a failed ask leaves no trace in /history or the convo file.
func (r *repl) recordErrorTurn(text string, err error) {
	if err == nil {
		return
	}
	if r.activeSess == "" || r.sessionsSt == nil {
		if strings.TrimSpace(text) != "" {
			r.convo = append(r.convo,
				entry.Turn{Role: "user", Content: text},
				entry.Turn{Role: "assistant", Content: "⚠ " + err.Error()},
			)
			r.convo = trimConvo(r.convo)
			saveConvo(r.convo)
		}
		return
	}
	_, _ = r.sessionsSt.AppendTurn(r.activeSess, sessions.Turn{Role: "assistant", Text: "⚠ " + err.Error(), Kind: "error"})
}

// ask runs one prompt through the unified entry engine and prints the
// converged result. With an active session (/resume) the turn runs with the
// session's full history in its worktree and is persisted to the thread —
// the web console's session ask, in the shell. In bare mode the turn runs
// with this REPL run's accumulated conversation (r.convo) so follow-ups
// keep context; /new clears it. Esc / Ctrl-C interrupts the run; a double
// Ctrl-C exits.
// ask is askMode with the classifier choosing the lens itself.
func (r *repl) ask(text string) { r.askMode(text, "") }

// askMode runs one prompt under a slash-mode directive: "goal", "plan",
// "spec", or "" for the default classification path.
func (r *repl) askMode(text, mode string) {
	if r.engine == nil {
		r.outln(i18n.T(r.loc, "repl.ask.noEngine"))
		return
	}
	// @path references become inline file blocks before the prompt leaves the
	// REPL, so "explain @main.go" works without the user pasting the file.
	text = r.expandFileRefs(text)

	// Session-aware context (mirrors panel sessionAsk); bare mode replays
	// the in-memory conversation instead.
	history, workDir := r.askContext(text)

	// The live status line: a spinner, the verb, elapsed time, and the interrupt
	// hint, repainted in place. Streamed answer text prints *through* it (Suspend
	// erases the line, prints, repaints below), so the wait is never silent.
	st := newStatusLine(r.loc)
	if r.interactive {
		st.Hint(i18n.T(r.loc, "cli.status.interrupt"))
		st.Start(statusVerb(r.loc))
		defer st.Stop()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lr := newStreamLineRenderer()
	cb := askengine.StreamCallbacks{}
	if stdoutIsTTY() {
		cb.OnDelta = func(chunk string) {
			// A chunk without a newline only fills the renderer's buffer — no
			// output, so no need to disturb the status line for it. The buffered
			// tail is previewed on that line instead, so a long paragraph reads
			// as "being written" rather than "hung".
			if strings.ContainsRune(chunk, '\n') {
				st.Suspend(func() { lr.delta(chunk) })
				st.Preview(lr.pending())
				return
			}
			lr.delta(chunk)
			st.Preview(lr.pending())
		}
		cb.OnProgress = func(p askengine.Progress) {
			// Advance the phase chain (classify → routing → executing → judging)
			// so the status line's trailing meta tracks a delegated run instead of
			// sitting on "routing" until the result lands.
			switch p.Kind {
			case askengine.ProgressTask, askengine.ProgressPlan, askengine.ProgressRoute:
				st.Phase("route", "routing")
			case askengine.ProgressExec, askengine.ProgressTool:
				st.Phase("exec", "executing")
			case askengine.ProgressJudge:
				st.Phase("judge", "judging")
			}
			// Before any answer text a progress note is worth keeping on screen
			// (it explains a long wait); once the answer is streaming, the same
			// note would interrupt it, so it stays ephemeral in the status line.
			note := progressNote(r.loc, p)
			if p.Kind == askengine.ProgressTool || lr.printed {
				st.Note(note)
				return
			}
			st.Log(pal().Muted(pal().MarkBullet() + " " + note))
		}
		// Reasoning (chain-of-thought) arrives before the answer on reasoning
		// models. Phase 1 surfaces it as a live dim preview on the status line so
		// the model's thinking is visible without a stall; it is display-only and
		// never persisted (D14). The richer collapsible thought block is Phase 2.
		var thinking thoughtPreview
		cb.OnReasoning = func(chunk string) {
			if lr.printed {
				return // answer has started; reasoning is done
			}
			if line := thinking.feed(chunk); line != "" {
				st.Preview(pal().Muted(line))
			}
		}
	}
	delivered := func() bool { return lr.printed }

	r.setAsking(true)
	defer r.setAsking(false)

	type outcome struct {
		out *askengine.Result
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		out, err := r.engine.AskTurnsSession(ctx, history, text, workDir, mode, r.activeSess, r.authorize, cb)
		ch <- outcome{out, err}
	}()
	got := make(chan struct{})
	var res outcome
	go func() { res = <-ch; close(got); cancel() }()
	if r.interactive && r.term != nil {
		r.term.watchInterrupt(ctx, cancel, i18n.T(r.loc, "repl.interrupted"))
	}
	<-got
	st.Stop() // erase the spinner before anything else prints
	lr.flush()
	// Absorb whatever terminal states this ask produced into the watcher's
	// baseline so the completion is not notified twice (the ask prints it).
	r.resetWatchBaseline()

	if res.err != nil {
		r.errf("%s\n", "panda: "+res.err.Error())
		// A cancelled ask leaves no dangling turn worth pairing (the user
		// aborted it); every other failure records one so the thread keeps
		// its user/assistant alternation.
		if !errors.Is(res.err, context.Canceled) {
			r.recordErrorTurn(text, res.err)
		}
		return
	}
	out := res.out

	// Inline approval gate: a tier-2 task with no standing consent comes back
	// parked in review (the engine leaves OnApproval nil for the REPL so the
	// prompt happens here, on the main loop, after the interrupt watcher has
	// released the terminal — a raw-mode read from the ask goroutine would fight
	// it for stdin). On a yes, re-run the same task authorized in place.
	if out != nil && out.NeedsApproval && out.Approval != nil {
		out = r.approveInline(out, workDir)
	}

	// Bind a spawned task back to the active session and persist the reply.
	r.recordOutcome(context.Background(), text, out)

	switch out.Kind {
	case "answer":
		if delivered() {
			break
		}
		if out.Note != "" {
			r.outln(r.renderMd(out.Note))
		}
		r.outln(r.renderMd(out.Answer))
	case "task":
		// Sub-agent round: the converged report is the reply. It streamed
		// live like an answer's text, so print it only when nothing was
		// delivered, and demote the raw agent output to a pointer line —
		// it used to be the whole display, burying the model's summary
		// under a wall of log. Without a report (queue-parked, budget-cut,
		// report degraded) the raw output stays the primary display.
		if strings.TrimSpace(out.Answer) != "" {
			if !delivered() {
				r.outln(r.renderMd(out.Answer))
			}
			reportNote := i18n.Tf(r.loc, "repl.ask.taskReport", "id", out.TaskID, "state", out.TaskState)
			if out.Agent != "" || out.Executor != "" {
				execNote := out.Agent
				if out.Model != "" {
					execNote += fmt.Sprintf(" (%s)", out.Model)
				}
				if out.Injected {
					execNote += " · " + i18n.T(r.loc, "tui.task.injected")
				}
				if out.Executor != "" {
					if execNote != "" {
						execNote += " @ "
					}
					execNote += out.Executor
				}
				reportNote += " · " + i18n.Tf(r.loc, "tui.task.execBy", "exec", execNote)
			}
			r.outln(pal().Muted(reportNote))
			break
		}
		// LLM-generated summary: the dedicated "report after execution" call
		// fills Report so the user sees a human-readable summary instead of
		// raw stdout/stderr. Render it before the raw output.
		if strings.TrimSpace(out.Report) != "" {
			r.outln(i18n.Tf(r.loc, "repl.ask.task", "id", out.TaskID, "state", out.TaskState))
			r.outln(r.renderMd(out.Report))
			break
		}
		r.outln(i18n.Tf(r.loc, "repl.ask.task", "id", out.TaskID, "state", out.TaskState))
		if out.OK {
			r.outf("%s", r.renderMd(out.Stdout))
			if s := strings.TrimRight(out.Stdout, "\n"); s != "" && !strings.HasSuffix(out.Stdout, "\n") {
				r.outln()
			}
		} else {
			r.errf("exit %d: %s\n", out.ExitCode, out.Stderr)
		}
	case "plan":
		// A plan does not finish inside the ask: its stages are queued and will
		// run on other machines. Print the board and how to follow it.
		if !out.OK {
			r.errf("%s\n", "panda: "+i18n.Tf(r.loc, "cli.plan.failed", "err", out.Stderr))
			break
		}
		r.outln(i18n.Tf(r.loc, "cli.plan.started",
			"id", out.PlanID, "n", strconv.Itoa(len(out.PlanStages)), "goal", out.PlanGoal))
		printPlanStagesTo(r.commandOutput(), out.PlanStages)
		r.outln(i18n.Tf(r.loc, "cli.plan.follow", "id", out.PlanID))
	}

	// The closing line: what this turn cost (elapsed, and tokens when the
	// provider reports them), and the same numbers added to the session total
	// that /cost reports.
	r.costTurns++
	r.costIn += out.InputTokens
	r.costOut += out.OutputTokens
	r.costWall += out.Latency
	r.costTotalUSD += out.Cost
	printCost(st, out)
}

// repeatLast re-runs the previous user ask (`!!`) — the shell habit for
// "ask that again" (after a model swap, a failed run, new context).
func (r *repl) repeatLast() {
	if r.activeSess == "" {
		for i := len(r.convo) - 1; i >= 0; i-- {
			if r.convo[i].Role == "user" {
				r.outln("!! " + r.convo[i].Content)
				r.ask(r.convo[i].Content)
				return
			}
		}
	}
	if r.term != nil {
		for i := len(r.term.history) - 1; i >= 0; i-- {
			l := strings.TrimSpace(r.term.history[i])
			if l != "" && !strings.HasPrefix(l, "/") && l != "!!" {
				r.outln("!! " + l)
				r.ask(l)
				return
			}
		}
	}
	r.outln(i18n.T(r.loc, "repl.bang.none"))
}

// rememberTurn records one bare-mode exchange through the shared convo
// helpers (see convo.go): pair-aligned character-budget trimming and
// persistence to the state dir, so the next REPL and `ask --continue`
// both resume it.
func (r *repl) rememberTurn(text string, out *askengine.Result) {
	r.convo = appendConvo(r.convo, r.loc, text, out)
}

// cmdNew clears the bare-mode conversation (/new) — the "new chat" of a
// chat app. Bound sessions keep their own history and are unaffected.
func (r *repl) cmdNew(arg string) {
	if r.activeSess != "" {
		r.outln(i18n.T(r.loc, "repl.new.session"))
		return
	}
	n := len(r.convo)
	r.convo = nil
	clearConvo()
	r.outln(i18n.Tf(r.loc, "repl.new.cleared", "n", fmt.Sprint(n/2)))
}

// cmdHistory prints the recent conversation compactly — the "scroll up" of a
// chat app when the terminal has moved on. A bound session lists its thread;
// bare mode reads the in-memory convo, falling back to the persisted file so
// a restart or a never-loaded memory cannot report an empty history while
// exchanges sit on disk.
func (r *repl) cmdHistory(arg string) {
	var turns []entry.Turn
	if r.activeSess != "" && r.sessionsSt != nil {
		if s, err := r.sessionsSt.Get(r.activeSess); err == nil {
			for _, t := range s.Turns {
				turns = append(turns, entry.Turn{Role: t.Role, Content: t.Text})
			}
		}
	} else {
		turns = r.convo
		if len(turns) == 0 {
			turns = loadConvo()
		}
	}
	if len(turns) == 0 {
		r.outln(i18n.T(r.loc, "repl.history.empty"))
		return
	}
	r.outln(i18n.T(r.loc, "repl.history.head"))
	// newest last, like a chat transcript; cap the listing, the window
	// itself may hold far more.
	if len(turns) > 20 {
		turns = turns[len(turns)-20:]
	}
	for _, t := range turns {
		who := i18n.T(r.loc, "repl.history.you")
		if t.Role != "user" {
			who = i18n.T(r.loc, "repl.history.panda")
		}
		r.outf("  %s: %s\n", who, head(t.Content, 200))
	}
}

// renderMd on the REPL delegates to renderCliMd (ask.go): same sink rules
// — color TTYs get the ANSI Markdown render, pipes and bare consoles get
// plain text. Streaming deltas bypass rendering: they print raw for the
// typing feel; only final blocks render.
func (r *repl) renderMd(s string) string { return renderCliMd(s) }

// head truncates s to at most n runes, marking a cut with an ellipsis.
func head(s string, n int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= n {
		return string(rs)
	}
	return string(rs[:n]) + "…"
}

// firstLine returns s's first non-empty line — for a task summary turn the
// opening sentence usually carries the answer.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// shortID abbreviates a UUID to its first dash-group for one-line display.
func shortID(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	return id
}

// cmdAsk is the explicit /ask form; bare input is the shortcut.
func (r *repl) cmdAsk(arg string) {
	if arg == "" {
		r.outln("/ask " + i18n.T(r.loc, "cmd.ask"))
		return
	}
	r.askMode(arg, "")
}

// cmdModeGoal/cmdModePlan/cmdModeSpec run one ask under a slash-mode lens:
// /goal refines the goal, /plan requests a staged plan, /spec writes a
// specification. Empty arguments print the mode's usage line instead of
// sending an empty prompt.
func (r *repl) cmdModeGoal(arg string) {
	if arg == "" {
		r.outln(i18n.Tf(r.loc, "tui.mode.usage", "cmd", "/goal"))
		return
	}
	r.askMode(arg, "goal")
}

func (r *repl) cmdModePlan(arg string) {
	if arg == "" {
		r.outln(i18n.Tf(r.loc, "tui.mode.usage", "cmd", "/plan"))
		return
	}
	r.askMode(arg, "plan")
}

func (r *repl) cmdModeSpec(arg string) {
	if arg == "" {
		r.outln(i18n.Tf(r.loc, "tui.mode.usage", "cmd", "/spec"))
		return
	}
	r.askMode(arg, "spec")
}

// cmdTasks lists the queue, optionally filtered by state (/tasks running);
// "/tasks clear" wipes the whole board (cancel running, delete every record);
// "/tasks watch" (or "/tasks running watch") opens the live board — the
// in-place refreshing view of `panda queue --watch`, inside the REPL.
func (r *repl) cmdTasks(arg string) {
	state := ""
	watch := false
	for _, f := range strings.Fields(arg) {
		if f == "clear" {
			r.cmdTasksClear()
			return
		}
		if f == "watch" || f == "-w" {
			watch = true
			continue
		}
		if state == "" {
			state = f
		}
	}
	if watch {
		watchQueueTo(r.commandContext(), r.store, state, "", r.commandOutput(), false)
		return
	}
	tasks, err := r.store.ListByState(context.Background(), state)
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(tasks) == 0 {
		r.outln(i18n.T(r.loc, "repl.tasks.none"))
		return
	}
	printTaskTableTo(r.commandOutput(), r.loc, tasks)
}

// cmdTasksClear implements "/tasks clear": confirm, cancel everything still
// moving, then delete every task record — the REPL twin of `panda queue clear`.
func (r *repl) cmdTasksClear() {
	tasks, err := r.store.ListByState(context.Background(), "")
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(tasks) == 0 {
		r.outln(i18n.T(r.loc, "cli.queue.clear.empty"))
		return
	}
	p := pal()
	if !r.confirm(i18n.Tf(r.loc, "cli.queue.clear.confirm", "n", strconv.Itoa(len(tasks)))) {
		return
	}
	if r.engine != nil {
		for _, t := range tasks {
			if !core.Terminal(t.State) {
				_, _ = r.engine.CancelTask(context.Background(), t.TaskID)
			}
		}
	} else {
		r.outln(p.Muted(i18n.T(r.loc, "cli.queue.clear.noEngine")))
	}
	cancelled, deleted, err := r.store.ClearQueue(context.Background())
	if err != nil {
		r.storeErr(err)
		return
	}
	r.outln(p.Success(i18n.Tf(r.loc, "cli.queue.clear.done",
		"c", strconv.Itoa(cancelled), "d", strconv.Itoa(deleted))))
}

// confirm asks a yes/no question and reports the answer. It reads through the
// raw-mode editor when interactive (Esc/Ctrl-C reads as "no") and defaults to
// "no" everywhere else — a destructive action never fires on ambiguity.
func (r *repl) confirm(question string) bool {
	if !r.interactive || r.term == nil {
		return false
	}
	ans, err := r.term.readLine(question, nil)
	if err != nil {
		return false
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// cmdDelete removes a task and its subtree from the store (queued or finished
// only — an active task must be cancelled first, matching `panda task delete`).
func (r *repl) cmdDelete(arg string) {
	if arg == "" {
		r.outln("/delete " + i18n.T(r.loc, "cmd.delete"))
		return
	}
	id, ok := r.resolveRef(arg)
	if !ok {
		return
	}
	n, err := r.store.Delete(context.Background(), id)
	if err != nil {
		if errors.Is(err, core.ErrTaskActive) {
			state := ""
			if t, gerr := r.store.Get(context.Background(), id); gerr == nil {
				state = t.State
			}
			r.outln(pal().Warn(i18n.Tf(r.loc, "cli.task.delete.active", "id", id, "state", state)))
			return
		}
		r.storeErr(err)
		return
	}
	r.outln(pal().Success(i18n.Tf(r.loc, "cli.task.delete.done", "n", strconv.Itoa(n))))
}

// resolveRef resolves a task reference for a REPL command. Same rules as the
// one-shot CLI (full id, or a unique prefix — the form every listing shows), but
// a bad reference reports and returns false rather than ending the process: the
// REPL survives a typo.
func (r *repl) resolveRef(ref string) (string, bool) {
	id, err := r.store.ResolveTaskID(context.Background(), ref)
	switch {
	case err == nil:
		return id, true
	case errors.Is(err, sql.ErrNoRows):
		r.outln(i18n.Tf(r.loc, "repl.task.none", "id", ref))
	default:
		var amb *core.AmbiguousTaskIDError
		if errors.As(err, &amb) {
			r.outln(ambiguousTaskMsg(r.loc, amb))
			return "", false
		}
		r.storeErr(err)
	}
	return "", false
}

// cmdTask shows one task's row and event timeline.
func (r *repl) cmdTask(arg string) {
	if arg == "" {
		r.outln("/task " + i18n.T(r.loc, "cmd.task"))
		return
	}
	id, ok := r.resolveRef(arg)
	if !ok {
		return
	}
	t, err := r.store.Get(context.Background(), id)
	if err != nil {
		r.storeErr(err)
		return
	}
	r.outf("  id:      %s\n", t.TaskID)
	r.outf("  project: %s\n", orDash(t.Project))
	r.outf("  title:   %s\n", t.Title)
	r.outf("  state:   %s\n", t.State)
	r.outf("  prio:    %s\n", priorityName(t.Priority))
	r.outf("  owner:   %s\n", orDash(t.OwnerNode))
	r.outf("  created: %s\n", ts(t.CreatedAt))
	r.outf("  updated: %s\n", ts(t.UpdatedAt))
	r.printEvents(t.TaskID)
}

// cmdCancel cancels a task and its subtree.
func (r *repl) cmdCancel(arg string) {
	if arg == "" {
		r.outln("/cancel " + i18n.T(r.loc, "cmd.cancel"))
		return
	}
	id, ok := r.resolveRef(arg)
	if !ok {
		return
	}
	ids, err := r.store.CancelCascade(context.Background(), id)
	if err != nil {
		r.storeErr(err)
		return
	}
	r.outln(i18n.Tf(r.loc, "repl.cancel.done", "n", fmt.Sprint(len(ids))))
}

// approveInline renders the tier-2 approval card for a task the engine parked
// in review, prompts the user on the main loop, and — on a yes — re-runs the
// task authorized in place, returning the resumed Result. On a no (or a failed
// prompt) it returns the original review Result so the caller prints the parked
// state and the /approve hint. It runs after the ask goroutine has finished and
// the interrupt watcher released the terminal, so the raw-mode read is safe.
func (r *repl) approveInline(out *askengine.Result, workDir string) *askengine.Result {
	req := out.Approval
	p := pal()
	r.outln(p.Warn(p.MarkBullet() + " " + i18n.T(r.loc, "repl.approval.head")))
	r.outln(p.Muted("  " + i18n.Tf(r.loc, "repl.approval.task", "title", req.Title)))
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		r.outln(p.Muted("  " + i18n.Tf(r.loc, "repl.approval.reason", "reason", reason)))
	}
	approved := false
	scope := req.Scope
	if scope == "" {
		scope = projectstore.ScopeSession
	}
	if r.interactive && r.term != nil {
		ans, err := r.term.readLine(i18n.Tf(r.loc, "repl.approval.promptScope", "scope", scope), nil)
		if err == nil {
			approved, scope = parseApprovalAnswer(ans, scope)
		}
	}
	r.rememberApproval(req, approved, scope)
	if !approved {
		r.outln(p.Muted(i18n.Tf(r.loc, "repl.approval.denied", "id", req.TaskID)))
		return out
	}
	r.outln(p.Success(i18n.T(r.loc, "repl.approval.approved")))
	cb := askengine.StreamCallbacks{}
	if stdoutIsTTY() {
		cb.OnProgress = func(p askengine.Progress) {
			r.outf("%s %s\n", pal().MarkBullet(), progressNote(r.loc, p))
		}
	}
	return r.engine.ResumeApproved(context.Background(), req.TaskID, workDir, cb)
}

// parseApprovalAnswer reads the approval card's reply: "y"/"yes"/"n"/"no"
// (anything else is a no — the card fails closed) plus an optional remember
// scope (once|session|project). The scope defaults to the card's preselected
// one; an unrecognized token degrades to once, since remembering less than
// the user asked is the safe error.
func parseApprovalAnswer(ans, defScope string) (approved bool, scope string) {
	scope = defScope
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(ans)))
	if len(fields) == 0 {
		return false, scope
	}
	switch fields[0] {
	case "y", "yes":
		approved = true
	case "n", "no":
	default:
		return false, scope
	}
	if len(fields) > 1 {
		switch fields[1] {
		case projectstore.ScopeOnce, projectstore.ScopeSession, projectstore.ScopeProject:
			scope = fields[1]
		default:
			scope = projectstore.ScopeOnce
		}
	}
	return approved, scope
}

// rememberApproval writes the card's answer into the chosen scope — session
// into the engine's in-memory map, project onto the project row, once nowhere.
// A project-scope answer without a project context must not silently persist:
// it degrades to the session scope instead.
func (r *repl) rememberApproval(req *askengine.ApprovalRequest, approved bool, scope string) {
	if r.engine == nil || scope == projectstore.ScopeOnce {
		return
	}
	decision := projectstore.DecisionDeny
	if approved {
		decision = projectstore.DecisionApprove
	}
	if scope == projectstore.ScopeProject && req.Project == "" {
		scope = projectstore.ScopeSession
	}
	if err := r.engine.RememberApproval(r.activeSess, req.Project, scope, decision); err != nil {
		r.errf("%s\n", "panda: "+err.Error())
		return
	}
	r.outln(pal().Muted(i18n.Tf(r.loc, "repl.approval.remembered", "decision", decision, "scope", scope)))
}

// cmdApproval shows or manages the tier-2 approval policy for the current
// session+project: "/approval" reports the effective state, "/approval clear"
// forgets remembered answers, "/approval mode inherit|never|on-request|always"
// and "/approval scope once|session|project" write the project's own policy —
// the per-project half of "approval logic set per project or per session".
func (r *repl) cmdApproval(arg string) {
	if r.engine == nil {
		r.outln(i18n.T(r.loc, "repl.approval.noEngine"))
		return
	}
	proj := r.activeProjectName()
	fields := strings.Fields(strings.TrimSpace(arg))
	if len(fields) == 0 {
		st := r.engine.ApprovalState(r.activeSess, proj)
		r.outln(i18n.T(r.loc, "repl.approval.stateHead"))
		r.outf("  project:  %s\n", orDash(proj))
		r.outf("  mode:     %s\n", st.Mode)
		r.outf("  scope:    %s\n", st.Scope)
		decision := "-"
		if st.Decision != "" {
			decision = st.Decision + " (" + st.DecisionScope + ")"
		}
		r.outf("  decision: %s\n", decision)
		return
	}
	if len(fields) < 2 {
		r.outln("/approval [clear | mode inherit|never|on-request|always | scope once|session|project]")
		return
	}
	setPolicy := func(mode, scope string) {
		if proj == "" {
			r.outln(i18n.T(r.loc, "repl.approval.needProject"))
			return
		}
		if err := r.engine.SetProjectApprovalPolicy(proj, mode, scope); err != nil {
			r.errf("%s\n", "panda: "+err.Error())
			return
		}
		r.outln(i18n.T(r.loc, "repl.approval.saved"))
	}
	switch fields[0] {
	case "clear":
		if err := r.engine.ClearApproval(r.activeSess, proj); err != nil {
			r.errf("%s\n", "panda: "+err.Error())
			return
		}
		r.outln(i18n.T(r.loc, "repl.approval.cleared"))
	case "mode":
		mode := fields[1]
		if mode == "inherit" {
			mode = ""
		}
		if err := projectstore.ValidateApprovalMode(mode); err != nil {
			r.errf("%s\n", "panda: "+err.Error())
			return
		}
		setPolicy(mode, "")
	case "scope":
		if err := projectstore.ValidateApprovalScope(fields[1]); err != nil {
			r.errf("%s\n", "panda: "+err.Error())
			return
		}
		setPolicy("", fields[1])
	default:
		r.outln("/approval [clear | mode inherit|never|on-request|always | scope once|session|project]")
	}
}

// cmdApprove accepts completed reviewed work or resumes a task that parked
// before execution. Detached approval relies on the task's persisted workdir.
func (r *repl) cmdApprove(arg string) {
	if arg == "" {
		r.outln("/approve " + i18n.T(r.loc, "cmd.approve"))
		return
	}
	id, ok := r.resolveRef(arg)
	if !ok {
		return
	}
	cb := askengine.StreamCallbacks{}
	if stdoutIsTTY() {
		cb.OnProgress = func(p askengine.Progress) {
			r.outf("%s %s\n", pal().MarkBullet(), progressNote(r.loc, p))
		}
	}
	out := r.engine.ResumeApproved(context.Background(), id, "", cb)
	r.outln(i18n.Tf(r.loc, "repl.ask.task", "id", out.TaskID, "state", out.TaskState))
	if out.OK {
		if stdout := strings.TrimRight(out.Stdout, "\n"); stdout != "" {
			r.outln(renderCliMd(stdout))
		}
		return
	}
	r.errf("exit %d: %s\n", out.ExitCode, out.Stderr)
}

// cmdReject rejects a reviewed task (review -> failed); the reason is the
// rest of the line after the id.
func (r *repl) cmdReject(arg string) {
	ref, reason, _ := strings.Cut(arg, " ")
	if ref == "" {
		r.outln("/reject " + i18n.T(r.loc, "cmd.reject"))
		return
	}
	id, ok := r.resolveRef(ref)
	if !ok {
		return
	}
	if err := r.store.Reject(context.Background(), id, strings.TrimSpace(reason)); err != nil {
		r.storeErr(err)
		return
	}
	r.outln(i18n.Tf(r.loc, "repl.reject.done", "id", id))
}

// cmdLogs shows one task's event timeline only.
func (r *repl) cmdLogs(arg string) {
	if arg == "" {
		r.outln("/logs " + i18n.T(r.loc, "cmd.logs"))
		return
	}
	id, ok := r.resolveRef(arg)
	if !ok {
		return
	}
	r.printEvents(id)
}

// printEvents prints a task's event lines, or the none-message.
func (r *repl) printEvents(id string) {
	events, err := r.store.Events(context.Background(), id)
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(events) == 0 {
		r.outln(i18n.Tf(r.loc, "repl.logs.none", "id", id))
		return
	}
	printEventTimelineTo(r.commandOutput(), events, "  ")
}

// cmdSessions lists chat sessions (the web console's session rail).
func (r *repl) cmdSessions(arg string) {
	if r.sessionsSt == nil {
		r.outln(i18n.T(r.loc, "repl.sessions.none"))
		return
	}
	list, err := r.sessionsSt.List()
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(list) == 0 {
		r.outln(i18n.T(r.loc, "repl.sessions.none"))
		return
	}
	r.outln(i18n.T(r.loc, "repl.sessions.head"))
	for _, s := range list {
		mark := " "
		if s.ID == r.activeSess {
			mark = "*"
		}
		branch := orDash(s.Branch)
		r.outf("  %s %-16s %-18s turns=%-3d %s (%s)\n", mark, s.ID, s.UpdatedAt.Format("2006-01-02 15:04"), len(s.Turns), s.Title, branch)
	}
}

// cmdResume attaches the REPL to a session: `/resume <id>` makes bare asks
// run with that session's history and worktree; `/resume` alone shows the
// current attachment; `/resume -` detaches.
func (r *repl) cmdResume(arg string) {
	if r.sessionsSt == nil {
		r.outln(i18n.T(r.loc, "repl.sessions.none"))
		return
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		if r.activeSess == "" {
			r.outln(i18n.T(r.loc, "repl.resume.none"))
		} else {
			r.outln(i18n.Tf(r.loc, "repl.resume.current", "id", r.activeSess))
		}
		return
	}
	if arg == "-" {
		r.activeSess = ""
		r.outln(i18n.T(r.loc, "repl.resume.detached"))
		return
	}
	if _, err := r.sessionsSt.Get(arg); err != nil {
		r.outln(i18n.Tf(r.loc, "repl.resume.bad", "id", arg))
		return
	}
	r.activeSess = arg
	r.outln(i18n.Tf(r.loc, "repl.resume.done", "id", arg))
}

// cmdMemory inspects the memory layer: bare `/memory` lists the selective-
// load manifest; `/memory get <name>` prints one file's content.
func (r *repl) cmdMemory(arg string) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		files, err := r.hermes.Files()
		if err != nil {
			r.storeErr(err)
			return
		}
		names, perr := r.projects.List()
		if perr != nil {
			r.storeErr(perr)
			return
		}
		if len(files) == 0 && len(names) == 0 {
			r.outln(i18n.T(r.loc, "repl.memory.none"))
			return
		}
		r.outln(i18n.T(r.loc, "repl.memory.head"))
		for _, f := range files {
			name := f.Name
			switch name {
			case "USER.md":
				name = "user"
			case "MEMORY.md":
				name = "memory"
			default:
				name = "topic:" + strings.TrimSuffix(strings.TrimPrefix(name, "topics/"), ".md")
			}
			r.outf("  %-20s entries=%-4d chars=%-6d %s\n", name, f.Entries, f.Chars, f.Summary)
		}
		for _, n := range names {
			if m, err := r.projects.Load(n); err == nil {
				r.outf("  %-20s entries=%-4d chars=%-6d\n", "project:"+n, len(m.Entries), m.Chars())
			}
		}
		r.outln(i18n.T(r.loc, "repl.memory.hint"))
		return
	}
	if fields[0] != "get" || len(fields) != 2 {
		r.outln(i18n.T(r.loc, "repl.memory.usage"))
		return
	}
	target, err := resolveMemoryTarget(r.cfg, fields[1])
	if err != nil {
		r.errf("%s\n", "panda: "+err.Error())
		return
	}
	data, err := os.ReadFile(target.path)
	if err != nil {
		if os.IsNotExist(err) {
			r.outln(i18n.Tf(r.loc, "repl.memory.empty", "name", target.name))
			return
		}
		r.storeErr(err)
		return
	}
	r.outln(r.renderMd(string(data)))
}

// cmdProjects lists existing project memories.
func (r *repl) cmdProjects(arg string) {
	names, err := r.projects.List()
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(names) == 0 {
		r.outln(i18n.T(r.loc, "repl.projects.none"))
		return
	}
	r.outln(i18n.T(r.loc, "repl.projects.head"))
	for _, n := range names {
		r.outln("  " + n)
	}
}

// cmdSkills manages procedural skills from inside the REPL.
func (r *repl) cmdSkills(arg string) {
	fields := strings.Fields(arg)
	sub := "list"
	if len(fields) > 0 {
		sub = fields[0]
		fields = fields[1:]
	}

	skillsPath := ""
	if r.cfg != nil {
		skillsPath = r.cfg.Storage.SkillsPath
	}
	store := skills.NewStore(skillsPath)
	_ = store.EnsureBuiltins()

	switch sub {
	case "list":
		skillList(store)
	case "find", "discover":
		if len(fields) == 0 {
			r.outln(i18n.T(r.loc, "repl.skill.find.usage"))
			return
		}
		query := strings.Join(fields, " ")
		hubURL := ""
		if r.cfg != nil {
			hubURL = r.cfg.Skills.HubURL
		}
		r.outln(i18n.Tf(r.loc, "repl.skill.find.searching", "q", query))
		sk, isNew, err := store.DiscoverAndInstall(context.Background(), hubURL, query, skills.ImportOptions{Scope: skills.ScopeGlobal, Status: skills.StatusActive})
		if err != nil {
			r.errf("panda: %v\n", err)
			return
		}
		if isNew {
			r.outln(i18n.Tf(r.loc, "repl.skill.find.installed", "name", sk.Name, "desc", sk.Description, "status", string(sk.Status)))
		} else {
			r.outln(i18n.Tf(r.loc, "repl.skill.find.matched", "name", sk.Name, "desc", sk.Description))
		}
	case "reset":
		if len(fields) == 0 {
			r.outln(i18n.T(r.loc, "repl.skill.reset.usage"))
			return
		}
		target := fields[0]
		if strings.EqualFold(target, "all") {
			for _, b := range skills.BuiltinSkills() {
				_, _ = store.ResetBuiltin(b.Name)
			}
			r.outln(i18n.T(r.loc, "repl.skill.reset.all"))
			return
		}
		sk, err := store.ResetBuiltin(target)
		if err != nil {
			r.errf("panda: %v\n", err)
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.skill.reset.done", "name", sk.Name))
	case "add", "install":
		if len(fields) == 0 {
			r.outln(i18n.T(r.loc, "repl.skill.add.usage"))
			return
		}
		target := fields[0]
		if skills.IsBuiltinSkill(target) {
			r.outln(i18n.Tf(r.loc, "repl.skill.add.builtin", "name", target))
			return
		}
		ctx := context.Background()
		opts := skills.ImportOptions{Scope: skills.ScopeGlobal, Status: skills.StatusActive}
		hubURL := ""
		if r.cfg != nil {
			hubURL = r.cfg.Skills.HubURL
		}
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.Contains(target, "/") || strings.Contains(target, "\\") {
			res, err := store.ImportSource(ctx, target, opts)
			if err != nil {
				r.errf("panda: %v\n", err)
				return
			}
			var names []string
			for _, s := range res {
				names = append(names, s.Name)
			}
			r.outln(i18n.Tf(r.loc, "repl.skill.add.imported", "n", strconv.Itoa(len(res)), "names", strings.Join(names, ", ")))
			return
		}
		sk, err := skills.InstallFromHub(ctx, store, hubURL, target, opts)
		if err != nil {
			r.errf("panda: %v\n", err)
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.skill.add.installed", "name", sk.Name, "desc", sk.Description))
	case "hub":
		action := "list"
		if len(fields) > 0 {
			action = fields[0]
			fields = fields[1:]
		}
		ctx := context.Background()
		hubURL := ""
		if r.cfg != nil {
			hubURL = r.cfg.Skills.HubURL
		}
		idx, err := skills.FetchHubIndex(ctx, hubURL)
		if err != nil {
			r.errf("panda: %s\n", i18n.Tf(r.loc, "repl.skill.hub.fetchFail", "err", err.Error()))
			return
		}
		switch action {
		case "list":
			for _, s := range idx.Skills {
				installed := false
				if sk, _ := store.Load(skills.ScopeGlobal, "", s.Name); sk != nil {
					installed = true
				}
				tagStr := ""
				if installed {
					tagStr = i18n.T(r.loc, "repl.skill.hub.installedTag")
				}
				r.outf("  %-20s %s%s\n", s.Name, s.Description, tagStr)
			}
		case "search":
			if len(fields) == 0 {
				r.outln(i18n.T(r.loc, "repl.skill.hub.search.usage"))
				return
			}
			q := strings.Join(fields, " ")
			res := skills.SearchHub(idx, q)
			if len(res) == 0 {
				r.outln(i18n.Tf(r.loc, "repl.skill.hub.search.none", "q", q))
				return
			}
			for _, s := range res {
				r.outf("  %-20s %s\n", s.Name, s.Description)
			}
		case "install":
			if len(fields) == 0 {
				r.outln(i18n.T(r.loc, "repl.skill.hub.install.usage"))
				return
			}
			name := fields[0]
			opts := skills.ImportOptions{Scope: skills.ScopeGlobal, Status: skills.StatusActive}
			sk, err := skills.InstallFromHub(ctx, store, hubURL, name, opts)
			if err != nil {
				r.errf("panda: %v\n", err)
				return
			}
			r.outln(i18n.Tf(r.loc, "repl.skill.hub.installedOK", "name", sk.Name))
		default:
			r.outln(i18n.Tf(r.loc, "repl.skill.hub.unknown", "action", action))
		}
	default:
		r.outln(i18n.T(r.loc, "repl.skill.usage"))
	}
}

// cmdNodes lists the local capability directory, and carries the pairing
// verbs: `add` appends a peer (config + live dial), `disconnect` drops one
// from the dial list, `invite` prints the other machine's join guide.
func (r *repl) cmdNodes(arg string) {
	fields := strings.Fields(arg)
	if len(fields) > 0 {
		switch fields[0] {
		case "add":
			r.cmdNodesAdd(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(arg), "add")))
			return
		case "disconnect", "dc":
			r.cmdNodesDisconnect(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(arg), fields[0])))
			return
		case "invite":
			r.cmdNodesInvite()
			return
		}
	}
	nodes, err := ledger.Query(r.db, "", "")
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(nodes) == 0 {
		r.outln(i18n.T(r.loc, "repl.nodes.none"))
		return
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	r.outln(i18n.T(r.loc, "repl.nodes.head"))
	for _, n := range nodes {
		seen := time.Unix(n.LastSeen, 0).Format(time.RFC3339)
		if n.LastSeen == 0 {
			seen = "never"
		}
		r.outf("  %-16s %-8s %-8s %-30s %s\n", n.ID, n.NodeKind, n.Status, n.Chip, seen)
	}
}

// cmdAgents lists the agent CLIs this node can delegate to (same probe as
// `panda agents`, driven by the agent registry).
func (r *repl) cmdAgents(arg string) {
	statuses := probeAgentStatuses()
	r.outln(i18n.T(r.loc, "repl.agents.head"))
	for _, a := range statuses {
		mark := " "
		if a.Installed {
			mark = "*"
		}
		r.outf("  %s %-12s %-8s %s\n", mark, a.Name, a.Binary, orDash(a.Path))
	}
}

// cmdConfig views or edits config.yaml: bare `/config` shows the six policy
// sections; `/config set <section> <args>` persists one value (comments
// preserved) with a restart hint.
func (r *repl) cmdConfig(arg string) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		r.outln(i18n.T(r.loc, "repl.config.head"))
		r.outf("  model:      %s @ %s\n", orDash(r.cfg.Model.Model), orDash(r.cfg.Model.BaseURL))
		r.outf("  mcp:        %s\n", orDash(r.cfg.MCP.Command))
		r.outf("  limits:     user=%d memory=%d project=%d\n",
			r.cfg.Memory.Limits.User, r.cfg.Memory.Limits.Memory, r.cfg.Memory.Limits.Project)
		r.outf("  routing:    %s\n", orDash(strings.Join(r.cfg.Routing.PreferredAgents, ",")))
		r.outf("  injection:  %s\n", r.cfg.Injection.NormalizedModel())
		r.outf("  approval:   %s\n", r.cfg.Approval.NormalizedMode())
		return
	}
	if fields[0] != "set" || len(fields) < 3 {
		r.outln(i18n.T(r.loc, "repl.config.usage"))
		return
	}
	section, rest := fields[1], fields[2:]
	var err error
	switch section {
	case "injection":
		switch rest[0] {
		case config.InjectionModelAuto, config.InjectionModelAlways, config.InjectionModelNever:
			err = config.UpdateSectionField(r.configPath, []string{"injection"}, "model", rest[0])
			if err == nil {
				r.cfg.Injection.Model = rest[0]
			}
		default:
			r.outln(i18n.T(r.loc, "repl.config.badValue"))
			return
		}
	case "approval":
		switch rest[0] {
		case config.ApprovalModeAlways, config.ApprovalModeOnRequest, config.ApprovalModeNever:
			err = config.UpdateSectionField(r.configPath, []string{"approval"}, "mode", rest[0])
			if err == nil {
				r.cfg.Approval.Mode = rest[0]
			}
		default:
			r.outln(i18n.T(r.loc, "repl.config.badValue"))
			return
		}
	case "limits":
		if len(rest) != 2 {
			r.outln(i18n.T(r.loc, "repl.config.usage"))
			return
		}
		value, aerr := strconv.Atoi(rest[1])
		if aerr != nil || value <= 0 || (rest[0] != "user" && rest[0] != "memory" && rest[0] != "project") {
			r.outln(i18n.T(r.loc, "repl.config.badValue"))
			return
		}
		err = config.UpdateSectionFieldInt(r.configPath, []string{"memory", "limits"}, rest[0], value)
		if err == nil {
			switch rest[0] {
			case "user":
				r.cfg.Memory.Limits.User = value
			case "memory":
				r.cfg.Memory.Limits.Memory = value
			case "project":
				r.cfg.Memory.Limits.Project = value
			}
		}
	case "routing":
		var agents []string
		for _, name := range strings.Split(rest[0], ",") {
			if name = strings.TrimSpace(name); name != "" {
				agents = append(agents, name)
			}
		}
		err = config.UpdateSectionList(r.configPath, []string{"routing"}, "preferred_agents", agents)
		if err == nil {
			r.cfg.Routing.PreferredAgents = agents
		}
	case "mcp":
		command := strings.Join(rest, " ")
		err = config.UpdateMCPSection(r.configPath, command)
		if err == nil {
			r.cfg.MCP.Command = command
		}
	default:
		r.outln(i18n.T(r.loc, "repl.config.usage"))
		return
	}
	if err != nil {
		r.storeErr(err)
		return
	}
	r.outln(i18n.Tf(r.loc, "cli.config.saved", "section", section))
	r.outln(i18n.T(r.loc, "cli.config.restart"))
}

// cmdContext summarizes what the next ask will run with: model, work dir,
// memory manifest size, active session, and authorization.
func (r *repl) cmdContext(arg string) {
	r.outln(i18n.T(r.loc, "repl.context.head"))
	model := i18n.T(r.loc, "repl.banner.noModel")
	if r.cfg.Model.BaseURL != "" {
		model = r.cfg.Model.Model + " @ " + r.cfg.Model.BaseURL
	}
	r.outf("  model:    %s\n", model)
	r.outf("  workdir:  %s\n", r.cfg.Storage.WorkPath)
	files, _ := r.hermes.Files()
	r.outf("  memory:   %d file(s) in the selective-load manifest\n", len(files))
	sess := "-"
	if r.activeSess != "" {
		sess = r.activeSess
		if s, err := r.sessionsSt.Get(r.activeSess); err == nil {
			sess = fmt.Sprintf("%s (turns=%d branch=%s)", s.ID, len(s.Turns), orDash(s.Branch))
		}
	}
	r.outf("  session:  %s\n", sess)
	r.outf("  authz:    %v\n", r.authorize)
	if r.engine != nil {
		st := r.engine.ApprovalState(r.activeSess, r.activeProjectName())
		r.outf("  approval: %s (scope %s)\n", st.Mode, st.Scope)
		if st.Decision != "" {
			r.outf("            remembered %s for %s\n", st.Decision, st.DecisionScope)
		}
	}
	r.outf("  card:     %v\n", r.hasCard)
}

// cmdPolicy shows the four app-policy groups (the web console's Settings →
// app policy page, read form).
func (r *repl) cmdPolicy(arg string) {
	r.outln(i18n.T(r.loc, "repl.policy.head"))
	r.outf("  injection_model: %s\n", r.cfg.Injection.NormalizedModel())
	r.outf("  approval_mode:   %s\n", r.cfg.Approval.NormalizedMode())
	r.outf("  preferred_agents: %s\n", orDash(strings.Join(r.cfg.Routing.PreferredAgents, ", ")))
	r.outf("  memory_limits:   user=%d memory=%d project=%d\n",
		r.cfg.Memory.Limits.User, r.cfg.Memory.Limits.Memory, r.cfg.Memory.Limits.Project)
}

// cmdWeb boots the embedded web console in-process with the full dependency
// set (sessions, worktrees, skills, reminders, push — same as `panda web`)
// and opens the browser already logged in: the URL carries the token, which
// the app consumes once and strips. Zero-config on loopback — no addr
// defaults to 127.0.0.1:7840, no token gets an ephemeral one. A
// non-loopback bind without a configured token still refuses: an
// unauthenticated panel on the network is never acceptable.
func (r *repl) cmdWeb(arg string) {
	addr := r.cfg.Network.PanelAddr
	if addr == "" {
		addr = "127.0.0.1:7840"
	}
	token := r.cfg.Network.PanelToken
	if token == "" {
		if !panel.IsLoopbackAddr(addr) {
			r.outln(i18n.T(r.loc, "repl.web.noToken"))
			return
		}
		token = panel.NewToken()
		r.outln(i18n.T(r.loc, "repl.web.ephemeral"))
	}
	if r.webSrv != nil {
		// Already serving: re-open the browser logged in with the token the
		// running panel was started with (a fresh ephemeral would not match
		// the server). The user typed /web because they want the console.
		r.outln(i18n.Tf(r.loc, "repl.web.running", "url", r.webURL))
		openBrowser(panel.AppendToken(r.webURL, r.webToken))
		return
	}
	// Self-update: discover newer CLI releases in the background; apply gates
	// on the task queue being idle (same policy as `panda web`).
	updateMgr := updater.New(updater.Options{
		Current:     versionpkg.Version,
		Idle:        r.store.Idle,
		SchemaFloor: schemaFloorFunc(r.db),
	})
	updateMgr.StartAutoCheck(context.Background(), 0)

	handler := panel.New(panel.Deps{
		Store:        r.store,
		Engine:       r.engine,
		DB:           r.db,
		Projects:     r.projects,
		ProjectStore: r.projStore,
		Sessions:     r.sessionsSt,
		Worktrees:    r.worktrees,
		SkillStore: func() *skills.Store {
			st := skills.NewStore(r.cfg.Storage.SkillsPath)
			_ = st.EnsureBuiltins()
			return st
		}(),
		Reminders:  reminders.NewStore(r.db),
		Push:       r.push,
		Cfg:        r.cfg,
		ConfigPath: r.configPath,
		CardPath:   r.cardPath,
		Token:      token,
		Updater:    updateMgr,
	})
	logDir := filepath.Join(cliStateDir(), "logs")
	_ = os.MkdirAll(logDir, 0o755)
	var logWriter io.Writer = io.Discard
	if lf, err := os.OpenFile(filepath.Join(logDir, "web.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		defer lf.Close()
		logWriter = lf
	}
	srv := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		Handler:           handler,
		ErrorLog:          stdlog.New(logWriter, "", 0),
	}
	// Bind synchronously so a taken port surfaces as an error, not a silent
	// goroutine death. A taken port falls forward to a nearby one instead of
	// failing — /web must always end with a usable console.
	ln, bound, err := listenPanel(addr)
	if err != nil {
		r.errf("%s\n", "panda: "+err.Error())
		return
	}
	if bound != addr {
		r.outln(i18n.Tf(r.loc, "web.portfallback", "orig", addr, "actual", bound))
	}
	go func() { _ = srv.Serve(ln) }()
	r.webSrv = srv
	r.webURL = panelURL(ln.Addr().String())
	r.webToken = token
	// The token is never shown to the user: the browser opens already
	// authenticated. The URL printed is the clean one.
	r.outln(i18n.Tf(r.loc, "repl.web.started", "url", r.webURL))
	openBrowser(panel.AppendToken(r.webURL, token))
}

// cmdAuthorize toggles tier-2 (irreversible) command authorization for the
// ask engine; it starts off, like `panda ask` without --authorize.
func (r *repl) cmdAuthorize(arg string) {
	r.authorize = !r.authorize
	if r.authorize {
		r.outln(i18n.T(r.loc, "repl.auth.on"))
	} else {
		r.outln(i18n.T(r.loc, "repl.auth.off"))
	}
}

// cmdLang switches the session locale; with no argument it lists the
// supported languages and marks the current one.
func (r *repl) cmdLang(arg string) {
	if arg == "" {
		for _, loc := range i18n.Locales {
			mark := " "
			if loc == r.loc {
				mark = "*"
			}
			r.outf("  %s %-6s %s\n", mark, loc, i18n.LocaleNames[loc])
		}
		return
	}
	for _, loc := range i18n.Locales {
		if strings.EqualFold(string(loc), strings.TrimSpace(arg)) {
			r.loc = loc
			if r.term != nil {
				r.term.loc = loc
			}
			if r.engine != nil {
				r.engine.SetLocale(loc)
			}
			r.outln(i18n.Tf(r.loc, "repl.lang.set", "lang", i18n.LocaleNames[loc]))
			r.persistLocale(loc)
			return
		}
	}
	r.outln(i18n.Tf(r.loc, "repl.lang.bad", "lang", arg, "list", localeCodes()))
}

// persistLocale records the /lang choice as ui.locale in config.yaml so the
// next run starts in the same language. A write failure is reported but not
// fatal — the session keeps the new locale either way, it just does not
// survive a restart.
func (r *repl) persistLocale(loc i18n.Locale) {
	if r.cfg == nil || r.cfg.UI.Locale == string(loc) {
		return
	}
	if r.configPath != "" {
		if err := config.UpdateSectionField(r.configPath, []string{"ui"}, "locale", string(loc)); err != nil {
			r.errf("%s\n", "panda: "+i18n.Tf(r.loc, "repl.lang.persistFail", "err", err.Error()))
			return
		}
	}
	r.cfg.UI.Locale = string(loc)
}

// cmdQuit exits the loop; defers close the db, engine, and web server.
func (r *repl) cmdQuit(arg string) {
	r.quit = true
}

// storeErr reports a store failure through the localized template.
func (r *repl) storeErr(err error) {
	r.errf("%s\n", i18n.Tf(r.loc, "repl.err.store", "err", err.Error()))
}

// localeCodes joins the supported locale codes for error messages.
func localeCodes() string {
	codes := make([]string, len(i18n.Locales))
	for i, loc := range i18n.Locales {
		codes[i] = string(loc)
	}
	return strings.Join(codes, ", ")
}

// stdinIsTTY reports whether stdin is an interactive terminal (prompt and
// progress hints are printed only then; piped input stays clean for scripts).
func stdinIsTTY() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

// stdoutIsTTY reports whether stdout is an interactive terminal (answers
// stream live only then; piped output prints once, clean for scripts).
func stdoutIsTTY() bool {
	stat, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

// panelURL turns a listen address into a clickable URL, mapping wildcard
// binds to localhost (the browser runs on this machine).
func panelURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// openBrowser launches the default browser at url, best-effort: failures
// (headless box, unknown platform) are ignored — the URL was just printed.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		// rundll32 + FileProtocolHandler, not `cmd /c start <url>`: cmd parses
		// `&`, `^` and parentheses in the URL as command syntax, and `start`
		// treats a quoted argument as a window title — either one silently
		// opens the wrong page for any URL carrying a query string. rundll32
		// takes the URL as a plain argument, so the token-carrying panel URL
		// arrives intact.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
