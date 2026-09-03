package main

// The REPL's second tier of commands — the ones a session reaches for after
// the first ask works: what did this cost, which model am I on, give me the
// screen back, save this conversation, is my install healthy. Plus the two
// input prefixes that keep the user from leaving the prompt at all: `@path`
// attaches a file to the next question, `!cmd` runs a shell command in the
// work dir.
//
// Each of these existed somewhere before — in `panda doctor`, in `panda config
// model`, in a scrollback the user had to keep themselves — but reaching them
// meant quitting the REPL. Learning cost is mostly the cost of leaving.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/cliui"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/memory"
	projectstore "github.com/Xustalis/OpenPanda/internal/projects"
)

// cmdClear wipes the screen and reprints the banner — the conversation is
// untouched (that is /new). Scrollback survives: the alternate screen buffer is
// never entered, so the user can still scroll back to what was cleared.
func (r *repl) cmdClear(arg string) {
	if stdoutIsTTY() {
		fmt.Print("\x1b[2J\x1b[3J\x1b[H")
	}
	r.printBanner()
	r.lastFooter = "" // the footer was cleared with everything else; reprint it
}

// cmdCost reports what this REPL run has spent: turns, model wall time, and
// token counts when the provider reports them. A local endpoint that reports
// no usage still gets a meaningful turn count and clock.
func (r *repl) cmdCost(arg string) {
	if r.costTurns == 0 {
		fmt.Println(i18n.T(r.loc, "repl.cost.none"))
		return
	}
	p := pal()
	fmt.Println(p.Heading(i18n.T(r.loc, "repl.cost.head") + ":"))
	fmt.Printf("  %-14s %d\n", i18n.T(r.loc, "repl.cost.turns"), r.costTurns)
	fmt.Printf("  %-14s %s\n", i18n.T(r.loc, "repl.cost.wall"), cliui.HumanDuration(r.costWall))
	if r.costIn+r.costOut == 0 {
		fmt.Println("  " + p.Muted(i18n.T(r.loc, "repl.cost.noTokens")))
		return
	}
	fmt.Printf("  %-14s %s\n", i18n.T(r.loc, "repl.cost.in"), cliui.HumanCount(r.costIn))
	fmt.Printf("  %-14s %s\n", i18n.T(r.loc, "repl.cost.out"), cliui.HumanCount(r.costOut))
}

// cmdModel is implemented in modelcmd.go — the multi-model registry that
// lists, switches, adds (from the built-in provider catalogue), removes,
// fetches and tests models. It lives apart from the other second-tier
// commands because it has real state to manage.

// cmdExport writes the bare-mode conversation to a Markdown file — the "save
// this before I close the terminal" move. With no argument the name is derived
// from the timestamp and written to the CLI state dir (or the work dir when
// there is none), and the path is printed so it can be opened straight away.
func (r *repl) cmdExport(arg string) {
	turns := r.convo
	if r.activeSess != "" && r.sessionsSt != nil {
		if s, err := r.sessionsSt.Get(r.activeSess); err == nil {
			turns = nil
			for _, t := range s.Turns {
				turns = append(turns, entry.Turn{Role: t.Role, Content: t.Text})
			}
		}
	}
	if len(turns) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.export.empty"))
		return
	}
	path := strings.TrimSpace(arg)
	if path == "" {
		dir := cliStateDir()
		if dir == "" {
			dir = r.cfg.Storage.WorkPath
		}
		path = filepath.Join(dir, "chat-"+time.Now().Format("20060102-150405")+".md")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# OpenPanda conversation\n\n_%s · node %s · model %s_\n",
		time.Now().Format(time.RFC3339), r.cfg.Node.Name, orDash(r.cfg.Model.Model))
	for _, t := range turns {
		who := "You"
		if t.Role != "user" {
			who = "Panda"
		}
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", who, strings.TrimSpace(t.Content))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		fmt.Println(i18n.Tf(r.loc, "repl.export.fail", "path", path, "err", err.Error()))
		return
	}
	fmt.Println(i18n.Tf(r.loc, "repl.export.done", "path", path, "n", fmt.Sprint(len(turns))))
}

// cmdDoctor runs the same self-check as `panda doctor`, inline. The standalone
// command exits non-zero for scripts; here the report is the whole point, so
// the shared checker returns the problem count instead of exiting.
func (r *repl) cmdDoctor(arg string) {
	if n := doctorReport(r.loc, r.configPath); n > 0 {
		fmt.Println(i18n.Tf(r.loc, "doctor.fail", "n", fmt.Sprint(n)))
		return
	}
	fmt.Println(i18n.T(r.loc, "doctor.pass"))
}

// runShell runs `!cmd` through the user's shell in the work dir and streams the
// output. It is the escape hatch every REPL eventually needs (`!git status`,
// `!ls`) and it runs with the user's own privileges in their own terminal —
// there is no privilege boundary here to cross, and no agent decides what runs:
// the user typed it. The ask engine's sandbox is unrelated and unaffected.
func (r *repl) runShell(cmdline string) {
	if cmdline == "" {
		fmt.Println(i18n.T(r.loc, "repl.bash.usage"))
		return
	}
	shell, flag := userShell()
	// Leave raw mode for the duration: a child that prints progress or reads a
	// password needs the terminal's own line discipline back.
	if r.term != nil {
		r.term.restore()
	}
	cmd := exec.Command(shell, flag, cmdline)
	cmd.Dir = r.cfg.Storage.WorkPath
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if err != nil {
		fmt.Println(pal().Danger(i18n.Tf(r.loc, "repl.bash.fail", "err", err.Error())))
	}
}

// userShell returns the shell to run `!<cmd>` through and its
// run-this-string flag. $SHELL is a POSIX convention and is normally unset on
// Windows, where /bin/sh does not exist — so cmd.exe (from %COMSPEC%, which is
// what the user's own console runs) takes over with /c. Without this, `!dir` on
// a Windows node fails with "file not found: /bin/sh" and the REPL looks broken
// rather than the shell lookup looking wrong.
func userShell() (shell, flag string) {
	if runtime.GOOS == "windows" {
		if sh := os.Getenv("COMSPEC"); sh != "" {
			return sh, "/c"
		}
		return "cmd.exe", "/c"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, "-c"
	}
	return "/bin/sh", "-c"
}

// maxRefBytes caps one @file attachment. A prompt is a context window, not a
// pipe: past a few tens of KB the file crowds out the question itself, and the
// truncation notice tells the model (and the user) that it happened.
const maxRefBytes = 32 * 1024

// expandFileRefs replaces every `@path` token in text with a fenced block of
// that file's content, appended after the prompt, and prints one notice per
// attachment. Tokens that do not resolve to a readable file are left alone — an
// email address or a decorator is not a file reference, and rewriting it would
// corrupt the question.
func (r *repl) expandFileRefs(text string) string {
	prompt, notes := r.expandFileRefsNotes(text)
	for _, n := range notes {
		fmt.Println(n)
	}
	return prompt
}

// expandFileRefsNotes does the expansion and returns the notices instead of
// printing them, so a full-screen front end can commit them to its transcript.
// Writing to stdout directly while Bubble Tea holds the terminal would land
// inside the frame it is repainting.
func (r *repl) expandFileRefsNotes(text string) (string, []string) {
	var blocks, notes []string
	seen := map[string]bool{}
	for _, field := range strings.Fields(text) {
		raw := strings.TrimRight(strings.TrimPrefix(field, "@"), ".,;:!?)")
		if !strings.HasPrefix(field, "@") || raw == "" || seen[raw] {
			continue
		}
		path := raw
		if !filepath.IsAbs(path) {
			// Relative refs resolve against the process's cwd — the directory
			// the user launched panda from, which is what they are looking at.
			path = filepath.Clean(path)
		}
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			notes = append(notes, pal().Warn(i18n.Tf(r.loc, "repl.at.fail", "path", raw, "err", err.Error())))
			continue
		}
		seen[raw] = true
		truncated := false
		if len(data) > maxRefBytes {
			data, truncated = data[:maxRefBytes], true
		}
		body := string(data)
		if truncated {
			body += "\n… (truncated at " + cliui.HumanCount(maxRefBytes) + " bytes)"
		}
		blocks = append(blocks, fmt.Sprintf("`%s`:\n```%s\n%s\n```", raw, fenceLang(raw), strings.TrimRight(body, "\n")))
		notes = append(notes, pal().Muted(pal().MarkBullet()+" "+
			i18n.Tf(r.loc, "repl.at.attached", "path", raw, "n", fmt.Sprint(countLines(body)))))
	}
	if len(blocks) == 0 {
		return text, notes
	}
	return text + "\n\n" + strings.Join(blocks, "\n\n"), notes
}

// fenceLang maps a file extension to a Markdown fence language, so the model
// sees the attachment as code rather than prose. Unknown extensions get none.
func fenceLang(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".rs":
		return "rust"
	case ".sh", ".bash", ".zsh":
		return "bash"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".md":
		return "markdown"
	case ".sql":
		return "sql"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".hpp":
		return "cpp"
	case ".java":
		return "java"
	case ".toml":
		return "toml"
	}
	return ""
}

// countLines counts the lines in s (for the attachment notice).
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for _, r := range s {
		if r == '\n' {
			n++
		}
	}
	return n
}

// bindProject hands the engine the project this machine is in, so the next ask
// belongs to it without the user naming it again. Called at startup and after
// every /project switch — one path, so the engine and the stored pointer cannot
// drift apart.
func (r *repl) bindProject() {
	if r.engine == nil {
		return
	}
	if r.projStore == nil {
		r.engine.SetProject("", "")
		return
	}
	name, err := r.projStore.Active()
	if err != nil || name == "" {
		r.engine.SetProject("", "")
		return
	}
	dir := ""
	if pr, gerr := r.projStore.Get(name); gerr == nil {
		dir = pr.WorkDir
	}
	r.engine.SetProject(name, dir)
}

// activeProjectName is the project the REPL is in, for the footer and the TUI
// context line. Empty when the store is absent or nothing was entered.
func (r *repl) activeProjectName() string {
	if r.projStore == nil {
		return ""
	}
	name, err := r.projStore.Active()
	if err != nil {
		return ""
	}
	return name
}

// cmdProjectEnter implements /project [name]: with a name, enter it; bare, report
// where you are. It is the same active pointer `panda project enter` writes, so a
// project entered in the REPL is still current in the next one-shot `panda ask`.
func (r *repl) cmdProjectEnter(arg string) {
	if r.projStore == nil {
		fmt.Println(i18n.T(r.loc, "cli.project.none"))
		return
	}
	name := strings.TrimSpace(arg)
	if name == "" {
		if cur := r.activeProjectName(); cur != "" {
			fmt.Println(i18n.Tf(r.loc, "cli.project.isActiveNamed", "name", cur))
		} else {
			fmt.Println(i18n.T(r.loc, "cli.project.noActive"))
		}
		return
	}
	// "-" leaves, mirroring /resume's spelling for detaching from a session.
	if name == "-" {
		if err := r.projStore.ClearActive(); err != nil {
			r.storeErr(err)
			return
		}
		r.bindProject()
		r.convo = loadConvo()
		fmt.Println(i18n.T(r.loc, "cli.project.noActive"))
		return
	}
	if err := projectstore.ValidateName(name); err != nil {
		fmt.Println(i18n.T(r.loc, "repl.project.bad"))
		return
	}
	// Create-then-enter. /project used to only create, so entering a name that
	// does not exist yet keeps working the way it did and lands the user inside
	// it, which is what they wanted both times they typed it.
	created := false
	if _, err := r.projStore.Get(name); err != nil {
		if _, cerr := r.projStore.EnsureFromName(name); cerr != nil {
			r.storeErr(cerr)
			return
		}
		if serr := r.projects.Save(name, memory.MemFile{Limit: r.projects.Limit()}); serr != nil {
			r.storeErr(serr)
			return
		}
		created = true
	}
	if err := r.projStore.SetActive(name); err != nil {
		r.storeErr(err)
		return
	}
	if created {
		fmt.Println(i18n.Tf(r.loc, "repl.project.created", "name", name))
	}
	r.bindProject()
	r.convo = loadConvo()
	fmt.Println(i18n.Tf(r.loc, "cli.project.entered", "name", name))
}
