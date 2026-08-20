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
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/internal/reminders"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/skills"
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
	hermes      *memory.Hermes
	sessionsSt  *sessions.Store
	worktrees   *sessions.Worktrees
	engine      *askengine.Engine
	hasCard     bool
	authorize   bool
	interactive bool
	quit        bool
	webSrv      *http.Server
	webURL      string
	term        *termSession
	activeSess  string
	push        *push.Service
}

// replCmd is one slash command: a name, the i18n key of its help line, and
// its handler (arg is everything after the command name, trimmed).
type replCmd struct {
	name string
	help string
	run  func(r *repl, arg string)
}

// replCommands is the dispatch table in help-display order. Populated in
// init() because cmdHelp iterates it (a composite literal would cycle).
var replCommands []replCmd

func init() {
	replCommands = []replCmd{
		{"ask", "cmd.ask", (*repl).cmdAsk},
		{"tasks", "cmd.tasks", (*repl).cmdTasks},
		{"task", "cmd.task", (*repl).cmdTask},
		{"cancel", "cmd.cancel", (*repl).cmdCancel},
		{"approve", "cmd.approve", (*repl).cmdApprove},
		{"reject", "cmd.reject", (*repl).cmdReject},
		{"logs", "cmd.logs", (*repl).cmdLogs},
		{"sessions", "cmd.sessions", (*repl).cmdSessions},
		{"resume", "cmd.resume", (*repl).cmdResume},
		{"memory", "cmd.memory", (*repl).cmdMemory},
		{"projects", "cmd.projects", (*repl).cmdProjects},
		{"project", "cmd.project", (*repl).cmdProject},
		{"nodes", "cmd.nodes", (*repl).cmdNodes},
		{"agents", "cmd.agents", (*repl).cmdAgents},
		{"config", "cmd.config", (*repl).cmdConfig},
		{"context", "cmd.context", (*repl).cmdContext},
		{"policy", "cmd.policy", (*repl).cmdPolicy},
		{"web", "cmd.web", (*repl).cmdWeb},
		{"authorize", "cmd.authorize", (*repl).cmdAuthorize},
		{"lang", "cmd.lang", (*repl).cmdLang},
		{"help", "cmd.help", (*repl).cmdHelp},
		{"quit", "cmd.quit", (*repl).cmdQuit},
	}
}

// runRepl implements `panda repl` — the interactive shell over the same
// SQLite store the daemon and the webui sidecar use. It never starts the
// daemon loop; task execution happens through the ask engine exactly like
// `panda ask` (and needs --card for the same reason).
func runRepl(args []string) {
	fs := flag.NewFlagSet("repl", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	cardPath := fs.String("card", "", "path to capabilities.yaml (required to execute tasks)")
	mcpCmd := fs.String("mcp", "", "MCP server command (space-separated)")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, store, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	interactive := stdinIsTTY()
	r := &repl{
		loc:         i18n.Detect(),
		cfg:         cfg,
		configPath:  config.ResolvePath(*configPath),
		db:          db,
		store:       store,
		projects:    memory.NewProjectsWithLimits(cfg.Storage.ProjectsPath, memoryLimits(cfg)),
		hermes:      memory.NewHermesWithLimits(cfg.Storage.MemoryPath, memoryLimits(cfg)),
		sessionsSt:  sessions.NewStore(sessionStoreRoot(cfg)),
		worktrees:   openWorktreesBestEffort(cfg.Storage.WorkPath),
		hasCard:     *cardPath != "",
		interactive: interactive,
	}
	if interactive {
		r.term = newTermSession()
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
			CardPath:   *cardPath,
			MCPCommand: *mcpCmd,
		})
		if err != nil {
			fatal("ask engine", err)
		}
		defer engine.Close()
		r.engine = engine
	}

	r.printBanner()
	if r.engine != nil && !r.hasCard {
		fmt.Println(i18n.T(r.loc, "repl.ask.noCard"))
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
		_ = r.webSrv.Close()
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

// printBanner draws the codex-style startup box: identity line, model line,
// work dir, then three orientation hints.
func (r *repl) printBanner() {
	model := i18n.T(r.loc, "repl.banner.noModel")
	if r.cfg.Model.BaseURL != "" {
		model = r.cfg.Model.Model
		if model == "" {
			model = r.cfg.Model.BaseURL
		}
	}
	lines := []string{
		fmt.Sprintf("%s v%s", i18n.T(r.loc, "repl.banner.title"), version),
		i18n.Tf(r.loc, "repl.banner.node", "node", r.cfg.Node.Name, "model", model),
		i18n.Tf(r.loc, "repl.banner.dir", "dir", r.cfg.Storage.WorkPath),
	}
	width := 0
	for _, l := range lines {
		if n := len([]rune(l)); n > width {
			width = n
		}
	}
	fmt.Println("┌─" + strings.Repeat("─", width+2) + "┐")
	for _, l := range lines {
		pad := width - len([]rune(l))
		fmt.Println("│ " + l + strings.Repeat(" ", pad) + " │")
	}
	fmt.Println("└─" + strings.Repeat("─", width+2) + "┘")
	fmt.Println(i18n.T(r.loc, "repl.banner.hint1"))
	fmt.Println(i18n.T(r.loc, "repl.banner.hint2"))
	fmt.Println(i18n.T(r.loc, "repl.banner.hint3"))
	fmt.Println()
}

// printFooter prints the status line above the prompt: node name, approval
// mode (color-coded), authorization state, and the active session.
func (r *repl) printFooter() {
	color := func(code, s string) string {
		if !stdoutIsTTY() {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}
	mode := r.cfg.Approval.NormalizedMode()
	switch mode {
	case config.ApprovalModeAlways:
		mode = color("31", mode) // red
	case config.ApprovalModeOnRequest:
		mode = color("33", mode) // yellow
	default:
		mode = color("32", mode) // green
	}
	authz := color("2", i18n.T(r.loc, "repl.footer.authz.off"))
	if r.authorize {
		authz = color("31", i18n.T(r.loc, "repl.footer.authz.on"))
	}
	sess := "-"
	if r.activeSess != "" {
		sess = r.activeSess
	}
	fmt.Println(color("2", fmt.Sprintf("%s:%s  %s:%s  %s:%s  %s:%s",
		i18n.T(r.loc, "repl.footer.node"), r.cfg.Node.Name,
		i18n.T(r.loc, "repl.footer.approval"), mode,
		i18n.T(r.loc, "repl.footer.authz"), authz,
		i18n.T(r.loc, "repl.footer.session"), sess)))
}

// dispatch routes one input line: slash commands to the table, anything else
// to the ask engine. Unknown commands name the fix (/help), never exit.
func (r *repl) dispatch(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
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
	for _, c := range replCommands {
		if c.name == name {
			c.run(r, strings.TrimSpace(arg))
			return
		}
	}
	fmt.Println(i18n.Tf(r.loc, "repl.unknown", "cmd", "/"+name))
}

// ask runs one prompt through the unified entry engine and prints the
// converged result. With an active session (/resume) the turn runs with the
// session's full history in its worktree and is persisted to the thread —
// the web console's session ask, in the shell. Esc / Ctrl-C interrupts the
// run; a double Ctrl-C exits.
func (r *repl) ask(text string) {
	if r.engine == nil {
		fmt.Println(i18n.T(r.loc, "repl.ask.noEngine"))
		return
	}

	// Session-aware context (mirrors panel sessionAsk).
	var history []entry.Turn
	workDir := ""
	if r.activeSess != "" && r.sessionsSt != nil {
		sess, err := r.sessionsSt.Get(r.activeSess)
		if err != nil {
			r.activeSess = "" // stale id: drop silently back to bare mode
		} else {
			if _, err := r.sessionsSt.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: text}); err == nil {
				sess, _ = r.sessionsSt.Get(sess.ID)
				for _, t := range sess.Turns {
					history = append(history, entry.Turn{Role: t.Role, Content: t.Text})
				}
				workDir = sess.Worktree
				if workDir == "" {
					workDir = r.engine.WorkPath()
				}
			}
		}
	}

	if r.interactive {
		fmt.Println(i18n.T(r.loc, "repl.ask.busy"))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var delivered bool
	cb := askengine.StreamCallbacks{}
	if stdoutIsTTY() {
		cb.OnDelta = func(chunk string) {
			delivered = true
			fmt.Print(chunk)
		}
		cb.OnStatus = func(note string) {
			if !delivered {
				fmt.Printf("· %s\n", note)
			}
		}
	}

	type outcome struct {
		out *askengine.Result
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		out, err := r.engine.AskTurns(ctx, history, text, workDir, r.authorize, cb)
		ch <- outcome{out, err}
	}()
	got := make(chan struct{})
	var res outcome
	go func() { res = <-ch; close(got); cancel() }()
	if r.interactive && r.term != nil {
		r.term.watchInterrupt(ctx, cancel, i18n.T(r.loc, "repl.interrupted"))
	}
	<-got

	if res.err != nil {
		if delivered {
			fmt.Println()
		}
		fmt.Fprintln(os.Stderr, "panda: "+res.err.Error())
		return
	}
	out := res.out

	// Bind a spawned task back to the active session and persist the reply.
	if r.activeSess != "" && r.sessionsSt != nil {
		if out.Kind == "task" && out.TaskID != "" {
			_ = r.store.SetSessionID(context.Background(), out.TaskID, r.activeSess)
		}
		turn := sessions.Turn{Role: "assistant", Kind: out.Kind}
		if out.Kind == "task" {
			turn.Text = out.TaskID
			turn.Ref = out.TaskID
		} else {
			turn.Text = out.Answer
		}
		_, _ = r.sessionsSt.AppendTurn(r.activeSess, turn)
	}

	switch out.Kind {
	case "answer":
		if delivered {
			fmt.Println()
			break
		}
		if out.Note != "" {
			fmt.Println(out.Note)
		}
		fmt.Println(out.Answer)
	case "task":
		if delivered {
			fmt.Println()
		}
		fmt.Println(i18n.Tf(r.loc, "repl.ask.task", "id", out.TaskID, "state", out.TaskState))
		if out.OK {
			fmt.Print(out.Stdout)
		} else {
			fmt.Fprintf(os.Stderr, "exit %d: %s\n", out.ExitCode, out.Stderr)
		}
	}
}

// cmdAsk is the explicit /ask form; bare input is the shortcut.
func (r *repl) cmdAsk(arg string) {
	if arg == "" {
		fmt.Println("/ask " + i18n.T(r.loc, "cmd.ask"))
		return
	}
	r.ask(arg)
}

// cmdTasks lists the queue, optionally filtered by state (/tasks running).
func (r *repl) cmdTasks(arg string) {
	state := ""
	if fields := strings.Fields(arg); len(fields) > 0 {
		state = fields[0]
	}
	tasks, err := r.store.ListByState(context.Background(), state)
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(tasks) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.tasks.none"))
		return
	}
	fmt.Println(i18n.T(r.loc, "repl.tasks.head"))
	for _, t := range tasks {
		fmt.Printf("  %-36s %-10s %-8s %-8s %s\n", t.TaskID, t.State, priorityName(t.Priority), orDash(t.OwnerNode), t.Title)
	}
}

// cmdTask shows one task's row and event timeline.
func (r *repl) cmdTask(arg string) {
	if arg == "" {
		fmt.Println("/task " + i18n.T(r.loc, "cmd.task"))
		return
	}
	t, err := r.store.Get(context.Background(), arg)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Println(i18n.Tf(r.loc, "repl.task.none", "id", arg))
			return
		}
		r.storeErr(err)
		return
	}
	fmt.Printf("  id:      %s\n", t.TaskID)
	fmt.Printf("  project: %s\n", orDash(t.Project))
	fmt.Printf("  title:   %s\n", t.Title)
	fmt.Printf("  state:   %s\n", t.State)
	fmt.Printf("  prio:    %s\n", priorityName(t.Priority))
	fmt.Printf("  owner:   %s\n", orDash(t.OwnerNode))
	fmt.Printf("  created: %s\n", ts(t.CreatedAt))
	fmt.Printf("  updated: %s\n", ts(t.UpdatedAt))
	r.printEvents(t.TaskID)
}

// cmdCancel cancels a task and its subtree.
func (r *repl) cmdCancel(arg string) {
	if arg == "" {
		fmt.Println("/cancel " + i18n.T(r.loc, "cmd.cancel"))
		return
	}
	ids, err := r.store.CancelCascade(context.Background(), arg)
	if err != nil {
		r.storeErr(err)
		return
	}
	fmt.Println(i18n.Tf(r.loc, "repl.cancel.done", "n", fmt.Sprint(len(ids))))
}

// cmdApprove approves a reviewed task (review -> done).
func (r *repl) cmdApprove(arg string) {
	if arg == "" {
		fmt.Println("/approve " + i18n.T(r.loc, "cmd.approve"))
		return
	}
	if err := r.store.Approve(context.Background(), arg); err != nil {
		r.storeErr(err)
		return
	}
	fmt.Println(i18n.Tf(r.loc, "repl.approve.done", "id", arg))
}

// cmdReject rejects a reviewed task (review -> failed); the reason is the
// rest of the line after the id.
func (r *repl) cmdReject(arg string) {
	id, reason, _ := strings.Cut(arg, " ")
	if id == "" {
		fmt.Println("/reject " + i18n.T(r.loc, "cmd.reject"))
		return
	}
	if err := r.store.Reject(context.Background(), id, strings.TrimSpace(reason)); err != nil {
		r.storeErr(err)
		return
	}
	fmt.Println(i18n.Tf(r.loc, "repl.reject.done", "id", id))
}

// cmdLogs shows one task's event timeline only.
func (r *repl) cmdLogs(arg string) {
	if arg == "" {
		fmt.Println("/logs " + i18n.T(r.loc, "cmd.logs"))
		return
	}
	r.printEvents(arg)
}

// printEvents prints a task's event lines, or the none-message.
func (r *repl) printEvents(id string) {
	events, err := r.store.Events(context.Background(), id)
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(events) == 0 {
		fmt.Println(i18n.Tf(r.loc, "repl.logs.none", "id", id))
		return
	}
	for _, e := range events {
		fmt.Printf("  %s  %-10s %s\n", ts(e.TS), e.Type, e.DataJSON)
	}
}

// cmdSessions lists chat sessions (the web console's session rail).
func (r *repl) cmdSessions(arg string) {
	if r.sessionsSt == nil {
		fmt.Println(i18n.T(r.loc, "repl.sessions.none"))
		return
	}
	list, err := r.sessionsSt.List()
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(list) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.sessions.none"))
		return
	}
	fmt.Println(i18n.T(r.loc, "repl.sessions.head"))
	for _, s := range list {
		mark := " "
		if s.ID == r.activeSess {
			mark = "*"
		}
		branch := orDash(s.Branch)
		fmt.Printf("  %s %-16s %-18s turns=%-3d %s (%s)\n", mark, s.ID, s.UpdatedAt.Format("2006-01-02 15:04"), len(s.Turns), s.Title, branch)
	}
}

// cmdResume attaches the REPL to a session: `/resume <id>` makes bare asks
// run with that session's history and worktree; `/resume` alone shows the
// current attachment; `/resume -` detaches.
func (r *repl) cmdResume(arg string) {
	if r.sessionsSt == nil {
		fmt.Println(i18n.T(r.loc, "repl.sessions.none"))
		return
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		if r.activeSess == "" {
			fmt.Println(i18n.T(r.loc, "repl.resume.none"))
		} else {
			fmt.Println(i18n.Tf(r.loc, "repl.resume.current", "id", r.activeSess))
		}
		return
	}
	if arg == "-" {
		r.activeSess = ""
		fmt.Println(i18n.T(r.loc, "repl.resume.detached"))
		return
	}
	if _, err := r.sessionsSt.Get(arg); err != nil {
		fmt.Println(i18n.Tf(r.loc, "repl.resume.bad", "id", arg))
		return
	}
	r.activeSess = arg
	fmt.Println(i18n.Tf(r.loc, "repl.resume.done", "id", arg))
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
			fmt.Println(i18n.T(r.loc, "repl.memory.none"))
			return
		}
		fmt.Println(i18n.T(r.loc, "repl.memory.head"))
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
			fmt.Printf("  %-20s entries=%-4d chars=%-6d %s\n", name, f.Entries, f.Chars, f.Summary)
		}
		for _, n := range names {
			if m, err := r.projects.Load(n); err == nil {
				fmt.Printf("  %-20s entries=%-4d chars=%-6d\n", "project:"+n, len(m.Entries), m.Chars())
			}
		}
		fmt.Println(i18n.T(r.loc, "repl.memory.hint"))
		return
	}
	if fields[0] != "get" || len(fields) != 2 {
		fmt.Println(i18n.T(r.loc, "repl.memory.usage"))
		return
	}
	target, err := resolveMemoryTarget(r.cfg, fields[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
		return
	}
	data, err := os.ReadFile(target.path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println(i18n.Tf(r.loc, "repl.memory.empty", "name", target.name))
			return
		}
		r.storeErr(err)
		return
	}
	os.Stdout.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		fmt.Println()
	}
}

// cmdProjects lists existing project memories.
func (r *repl) cmdProjects(arg string) {
	names, err := r.projects.List()
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(names) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.projects.none"))
		return
	}
	fmt.Println(i18n.T(r.loc, "repl.projects.head"))
	for _, n := range names {
		fmt.Println("  " + n)
	}
}

// cmdProject creates a project memory (idempotent empty seed, same as the
// panel's POST /api/projects).
func (r *repl) cmdProject(arg string) {
	name := strings.TrimSpace(arg)
	if err := memory.ValidateName(name); err != nil {
		fmt.Println(i18n.T(r.loc, "repl.project.bad"))
		return
	}
	if err := r.projects.Save(name, memory.MemFile{Limit: r.projects.Limit()}); err != nil {
		r.storeErr(err)
		return
	}
	fmt.Println(i18n.Tf(r.loc, "repl.project.created", "name", name))
}

// cmdNodes lists the local capability directory.
func (r *repl) cmdNodes(arg string) {
	nodes, err := ledger.Query(r.db, "", "")
	if err != nil {
		r.storeErr(err)
		return
	}
	if len(nodes) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.nodes.none"))
		return
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	fmt.Println(i18n.T(r.loc, "repl.nodes.head"))
	for _, n := range nodes {
		seen := time.Unix(n.LastSeen, 0).Format(time.RFC3339)
		if n.LastSeen == 0 {
			seen = "never"
		}
		fmt.Printf("  %-16s %-8s %-30s %s\n", n.ID, n.Status, n.Chip, seen)
	}
}

// cmdAgents lists the agent CLIs this node can delegate to (same probe as
// `panda agents`).
func (r *repl) cmdAgents(arg string) {
	statuses := make([]agentStatus, 0, len(agentProbes))
	for _, p := range agentProbes {
		statuses = append(statuses, probeAgentCLI(p))
	}
	fmt.Println(i18n.T(r.loc, "repl.agents.head"))
	for _, a := range statuses {
		mark := " "
		if a.Installed {
			mark = "*"
		}
		fmt.Printf("  %s %-12s %-8s %s\n", mark, a.Name, a.Binary, orDash(a.Path))
	}
}

// cmdConfig views or edits config.yaml: bare `/config` shows the six policy
// sections; `/config set <section> <args>` persists one value (comments
// preserved) with a restart hint.
func (r *repl) cmdConfig(arg string) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.config.head"))
		fmt.Printf("  model:      %s @ %s\n", orDash(r.cfg.Model.Model), orDash(r.cfg.Model.BaseURL))
		fmt.Printf("  mcp:        %s\n", orDash(r.cfg.MCP.Command))
		fmt.Printf("  limits:     user=%d memory=%d project=%d\n",
			r.cfg.Memory.Limits.User, r.cfg.Memory.Limits.Memory, r.cfg.Memory.Limits.Project)
		fmt.Printf("  routing:    %s\n", orDash(strings.Join(r.cfg.Routing.PreferredAgents, ",")))
		fmt.Printf("  injection:  %s\n", r.cfg.Injection.NormalizedModel())
		fmt.Printf("  approval:   %s\n", r.cfg.Approval.NormalizedMode())
		return
	}
	if fields[0] != "set" || len(fields) < 3 {
		fmt.Println(i18n.T(r.loc, "repl.config.usage"))
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
			fmt.Println(i18n.T(r.loc, "repl.config.badValue"))
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
			fmt.Println(i18n.T(r.loc, "repl.config.badValue"))
			return
		}
	case "limits":
		if len(rest) != 2 {
			fmt.Println(i18n.T(r.loc, "repl.config.usage"))
			return
		}
		value, aerr := strconv.Atoi(rest[1])
		if aerr != nil || value <= 0 || (rest[0] != "user" && rest[0] != "memory" && rest[0] != "project") {
			fmt.Println(i18n.T(r.loc, "repl.config.badValue"))
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
		fmt.Println(i18n.T(r.loc, "repl.config.usage"))
		return
	}
	if err != nil {
		r.storeErr(err)
		return
	}
	fmt.Println(i18n.Tf(r.loc, "cli.config.saved", "section", section))
	fmt.Println(i18n.T(r.loc, "cli.config.restart"))
}

// cmdContext summarizes what the next ask will run with: model, work dir,
// memory manifest size, active session, and authorization.
func (r *repl) cmdContext(arg string) {
	fmt.Println(i18n.T(r.loc, "repl.context.head"))
	model := i18n.T(r.loc, "repl.banner.noModel")
	if r.cfg.Model.BaseURL != "" {
		model = r.cfg.Model.Model + " @ " + r.cfg.Model.BaseURL
	}
	fmt.Printf("  model:    %s\n", model)
	fmt.Printf("  workdir:  %s\n", r.cfg.Storage.WorkPath)
	files, _ := r.hermes.Files()
	fmt.Printf("  memory:   %d file(s) in the selective-load manifest\n", len(files))
	sess := "-"
	if r.activeSess != "" {
		sess = r.activeSess
		if s, err := r.sessionsSt.Get(r.activeSess); err == nil {
			sess = fmt.Sprintf("%s (turns=%d branch=%s)", s.ID, len(s.Turns), orDash(s.Branch))
		}
	}
	fmt.Printf("  session:  %s\n", sess)
	fmt.Printf("  authz:    %v\n", r.authorize)
	fmt.Printf("  card:     %v\n", r.hasCard)
}

// cmdPolicy shows the four app-policy groups (the web console's Settings →
// app policy page, read form).
func (r *repl) cmdPolicy(arg string) {
	fmt.Println(i18n.T(r.loc, "repl.policy.head"))
	fmt.Printf("  injection_model: %s\n", r.cfg.Injection.NormalizedModel())
	fmt.Printf("  approval_mode:   %s\n", r.cfg.Approval.NormalizedMode())
	fmt.Printf("  preferred_agents: %s\n", orDash(strings.Join(r.cfg.Routing.PreferredAgents, ", ")))
	fmt.Printf("  memory_limits:   user=%d memory=%d project=%d\n",
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
			fmt.Println(i18n.T(r.loc, "repl.web.noToken"))
			return
		}
		token = panel.NewToken()
		fmt.Println(i18n.T(r.loc, "repl.web.ephemeral"))
	}
	if r.webSrv != nil {
		fmt.Println(i18n.Tf(r.loc, "repl.web.running", "url", r.webURL))
		return
	}
	handler := panel.New(panel.Deps{
		Store:      r.store,
		Engine:     r.engine,
		DB:         r.db,
		Projects:   r.projects,
		Sessions:   r.sessionsSt,
		Worktrees:  r.worktrees,
		SkillStore: skills.NewStore(r.cfg.Storage.SkillsPath),
		Reminders:  reminders.NewStore(r.db),
		Push:       r.push,
		Cfg:        r.cfg,
		ConfigPath: r.configPath,
		Token:      token,
	})
	srv := &http.Server{Addr: addr, Handler: handler}
	// Bind synchronously so a taken port surfaces as an error, not a silent
	// goroutine death.
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
		return
	}
	go func() { _ = srv.Serve(ln) }()
	r.webSrv = srv
	r.webURL = panelURL(ln.Addr().String())
	fmt.Println(i18n.Tf(r.loc, "repl.web.started", "url", r.webURL, "token", token))
	openBrowser(panel.AppendToken(r.webURL, token))
}

// cmdAuthorize toggles tier-2 (irreversible) command authorization for the
// ask engine; it starts off, like `panda ask` without --authorize.
func (r *repl) cmdAuthorize(arg string) {
	r.authorize = !r.authorize
	if r.authorize {
		fmt.Println(i18n.T(r.loc, "repl.auth.on"))
	} else {
		fmt.Println(i18n.T(r.loc, "repl.auth.off"))
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
			fmt.Printf("  %s %-6s %s\n", mark, loc, i18n.LocaleNames[loc])
		}
		return
	}
	for _, loc := range i18n.Locales {
		if strings.EqualFold(string(loc), strings.TrimSpace(arg)) {
			r.loc = loc
			fmt.Println(i18n.Tf(r.loc, "repl.lang.set", "lang", i18n.LocaleNames[loc]))
			return
		}
	}
	fmt.Println(i18n.Tf(r.loc, "repl.lang.bad", "lang", arg, "list", localeCodes()))
}

// cmdHelp renders the full command reference through the user's pager
// ($PAGER, falling back to less); when neither is available or the session
// is piped, it prints directly.
func (r *repl) cmdHelp(arg string) {
	var b strings.Builder
	b.WriteString(i18n.T(r.loc, "repl.help") + ":\n\n")
	for _, c := range replCommands {
		fmt.Fprintf(&b, "  /%-10s %s\n", c.name, i18n.T(r.loc, c.help))
	}
	b.WriteString("\n" + i18n.T(r.loc, "repl.help.keys") + "\n")
	b.WriteString("  " + i18n.T(r.loc, "repl.help.esc") + "\n")
	b.WriteString("  " + i18n.T(r.loc, "repl.help.ctrlc") + "\n")
	b.WriteString("  " + i18n.T(r.loc, "repl.help.ctrlc2") + "\n")
	b.WriteString("  " + i18n.T(r.loc, "repl.help.tab") + "\n")
	text := b.String()

	if !r.interactive || !stdoutIsTTY() {
		fmt.Print(text)
		return
	}
	pager := os.Getenv("PAGER")
	if pager == "" {
		if _, err := exec.LookPath("less"); err == nil {
			pager = "less"
		}
	}
	if pager == "" {
		fmt.Print(text)
		return
	}
	cmd := exec.Command(pager)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Print(text)
	}
}

// cmdQuit exits the loop; defers close the db, engine, and web server.
func (r *repl) cmdQuit(arg string) {
	r.quit = true
}

// storeErr reports a store failure through the localized template.
func (r *repl) storeErr(err error) {
	fmt.Fprintln(os.Stderr, i18n.Tf(r.loc, "repl.err.store", "err", err.Error()))
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
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
