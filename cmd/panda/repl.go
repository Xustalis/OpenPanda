package main

// The interactive REPL — the operator's seat on top of the kernel. Slash
// commands cover the panel surfaces (queue, task, projects, nodes, approvals)
// and /web boots the embedded web console in-process; anything that is not a
// slash command goes straight to the ask engine, the same unified entry as
// `panda ask` and POST /api/ask. All user-facing strings come from
// internal/i18n (five languages, English fallback).

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
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/memory"
	"github.com/Xustalis/OpenPanda/webui/panel"
)

// repl carries the session state: the shared store/projects handles, the
// optional ask engine, the tier-2 authorization switch, the active locale,
// and the /web server once booted.
type repl struct {
	loc         i18n.Locale
	cfg         *config.Config
	db          *sql.DB
	store       *core.TaskStore
	projects    *memory.Projects
	engine      *askengine.Engine
	hasCard     bool
	authorize   bool
	interactive bool
	quit        bool
	webSrv      *http.Server
	webURL      string
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
		{"projects", "cmd.projects", (*repl).cmdProjects},
		{"project", "cmd.project", (*repl).cmdProject},
		{"nodes", "cmd.nodes", (*repl).cmdNodes},
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

	r := &repl{
		loc:         i18n.Detect(),
		cfg:         cfg,
		db:          db,
		store:       store,
		projects:    memory.NewProjects(cfg.Storage.ProjectsPath),
		hasCard:     *cardPath != "",
		interactive: stdinIsTTY(),
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

	fmt.Println(i18n.T(r.loc, "repl.welcome"))
	if r.engine != nil && !r.hasCard {
		fmt.Println(i18n.T(r.loc, "repl.ask.noCard"))
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for !r.quit {
		if r.interactive {
			fmt.Print(i18n.T(r.loc, "repl.prompt"))
		}
		if !sc.Scan() {
			break // EOF (Ctrl-D)
		}
		r.dispatch(sc.Text())
	}
	if r.quit || r.interactive {
		fmt.Println(i18n.T(r.loc, "repl.bye"))
	}
	if r.webSrv != nil {
		_ = r.webSrv.Close()
	}
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
// converged result: an answer, or a task with its outcome. On an interactive
// terminal the answer streams live as the model emits it — the panel's UX
// brought to the shell; piped output stays clean, printing the full answer
// once at the end for scripts.
func (r *repl) ask(text string) {
	if r.engine == nil {
		fmt.Println(i18n.T(r.loc, "repl.ask.noEngine"))
		return
	}
	if r.interactive {
		fmt.Println(i18n.T(r.loc, "repl.ask.busy"))
	}
	var delivered bool
	cb := askengine.StreamCallbacks{}
	if stdoutIsTTY() {
		cb.OnDelta = func(chunk string) {
			delivered = true
			fmt.Print(chunk)
		}
		cb.OnStatus = func(note string) {
			// Progress lines print only before any answer text has streamed,
			// so they never cut a sentence in half.
			if !delivered {
				fmt.Printf("· %s\n", note)
			}
		}
	}
	out, err := r.engine.AskTurns(context.Background(), nil, text, "", r.authorize, cb)
	if err != nil {
		if delivered {
			fmt.Println()
		}
		fmt.Fprintln(os.Stderr, "panda: "+err.Error())
		return
	}
	switch out.Kind {
	case "answer":
		if delivered {
			// The answer already streamed live; just close the line.
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
		fmt.Printf("  %-36s %-10s %-8s %s\n", t.TaskID, t.State, orDash(t.OwnerNode), t.Title)
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
	if err := r.projects.Save(name, memory.MemFile{Limit: memory.ProjectCharLimit}); err != nil {
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

// cmdWeb boots the embedded web console in-process (the same handler the
// webui sidecar serves) and opens the browser already logged in: the URL
// carries the token, which the app consumes once and strips. Zero-config on
// loopback — no addr defaults to 127.0.0.1:7840, no token gets an ephemeral
// one. A non-loopback bind without a configured token still refuses: an
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
		Store:    r.store,
		Engine:   r.engine,
		DB:       r.db,
		Projects: r.projects,
		Token:    token,
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

// cmdHelp lists every command with its localized one-liner.
func (r *repl) cmdHelp(arg string) {
	fmt.Println(i18n.T(r.loc, "repl.help") + ":")
	for _, c := range replCommands {
		fmt.Printf("  /%-10s %s\n", c.name, i18n.T(r.loc, c.help))
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
