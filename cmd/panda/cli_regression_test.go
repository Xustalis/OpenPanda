// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// CLI regression tests: the subcommands a user actually types, run as the real
// binary in a subprocess.
//
// Why a subprocess instead of calling runX(args) in-process:
//
//   - every command path ends in os.Exit (fatal/fail), so an in-process test
//     could never assert the exit code — the one thing a shell script depends on;
//   - argv parsing and global-flag extraction (--config, --card, --json) live
//     above the command functions and are exactly where regressions land;
//   - the package globals (cliConfigPath, jsonOutput) are process-wide, so
//     in-process cases would leak into each other.
//
// The binary is built once per `go test` run. Each case gets its own HOME, so
// config, database, skills and pid files stay inside the test's temp dir and
// never touch the developer's real node.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var (
	cliBuildOnce sync.Once
	cliBuildPath string
	cliBuildErr  error
)

// cliBinary builds cmd/panda once and returns its path.
func cliBinary(t *testing.T) string {
	t.Helper()
	cliBuildOnce.Do(func() {
		if _, err := exec.LookPath("go"); err != nil {
			cliBuildErr = err
			return
		}
		dir, err := os.MkdirTemp("", "panda-cli-regression-")
		if err != nil {
			cliBuildErr = err
			return
		}
		name := "panda"
		if filepath.Separator == '\\' {
			name += ".exe"
		}
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", out, ".")
		if build, err := cmd.CombinedOutput(); err != nil {
			cliBuildErr = errors.New(string(build))
			return
		}
		cliBuildPath = out
	})
	if cliBuildErr != nil {
		t.Skipf("cannot build the panda binary here: %v", cliBuildErr)
	}
	return cliBuildPath
}

// cliResult is one completed CLI invocation.
type cliResult struct {
	stdout string
	stderr string
	code   int
}

// output is stdout+stderr, for assertions that do not care which stream a
// hint landed on.
func (r cliResult) output() string { return r.stdout + r.stderr }

// runCLI runs the binary with an isolated HOME and an explicit working
// directory. The environment is scrubbed of everything that could reach the
// developer's real node or a real model endpoint: OPENPANDA_*/PANDA_* (model
// keys, config overrides, release channels), HOME/XDG_* (state and data
// dirs), and the locale variables (so the English assertions are stable on a
// Chinese or German workstation).
func runCLI(t *testing.T, home string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(cliBinary(t), args...)
	cmd.Dir = home
	cmd.Env = isolatedEnv(home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run panda %v: %v", args, err)
		}
		code = exit.ExitCode()
	}
	return cliResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func isolatedEnv(home string) []string {
	drop := map[string]bool{
		"HOME": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
		"XDG_STATE_HOME": true,
		"LANG":           true, "LC_ALL": true, "LC_MESSAGES": true, "LANGUAGE": true,
	}
	env := make([]string, 0, len(os.Environ())+12)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if drop[upper] || strings.HasPrefix(upper, "OPENPANDA_") || strings.HasPrefix(upper, "PANDA_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+home,
		"USERPROFILE="+home,
		"APPDATA="+filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"),
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"OPENPANDA_LANG=en",
		"NO_COLOR=1",
		"TERM=dumb",
	)
}

// initNode runs the first-run command into the test's HOME and returns the
// config and card paths. Every DB-backed case starts here: it is the entry
// point a new user takes, so a break in it must fail a test rather than a
// first run.
func initNode(t *testing.T, home string) (configPath, cardPath string) {
	t.Helper()
	configPath = filepath.Join(home, "config.yaml")
	cardPath = filepath.Join(home, "capabilities.yaml")
	r := runCLI(t, home, "init", "--defaults", "--non-interactive",
		"--config", configPath, "--card", cardPath)
	if r.code != 0 {
		t.Fatalf("panda init exited %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	for _, p := range []string{configPath, cardPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("init did not write %s: %v", p, err)
		}
	}
	return configPath, cardPath
}

func TestCLIVersion(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, home, "version")
	if r.code != 0 {
		t.Fatalf("version exited %d: %s", r.code, r.output())
	}
	if !strings.HasPrefix(r.stdout, "panda ") {
		t.Fatalf("version stdout = %q, want it to start with %q", r.stdout, "panda ")
	}
}

func TestCLIVersionJSON(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, home, "--json", "version")
	if r.code != 0 {
		t.Fatalf("version --json exited %d: %s", r.code, r.output())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("version --json is not JSON: %v\n%s", err, r.stdout)
	}
	for _, key := range []string{"version", "schema"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("version --json lacks %q: %s", key, r.stdout)
		}
	}
}

func TestCLIHelp(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, home, "help")
	if r.code != 0 {
		t.Fatalf("help exited %d: %s", r.code, r.output())
	}
	// One command per plane: a missing line means a command family dropped out
	// of the operator's only map.
	for _, want := range []string{"daemon", "ask <text>", "task add", "plan run", "doctor", "completion"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("help does not mention %q", want)
		}
	}
}

func TestCLIInitIsIdempotentAndForceBacksUp(t *testing.T) {
	home := t.TempDir()
	configPath, cardPath := initNode(t, home)

	// A second init without --force must refuse rather than overwrite the
	// node's identity and model settings.
	again := runCLI(t, home, "init", "--defaults", "--non-interactive",
		"--config", configPath, "--card", cardPath)
	if again.code != 1 {
		t.Fatalf("second init exited %d, want 1\n%s", again.code, again.output())
	}
	if !strings.Contains(again.output(), "exists") {
		t.Fatalf("second init does not say the config exists: %s", again.output())
	}

	// --force rewrites but keeps the previous file as .bak.
	forced := runCLI(t, home, "init", "--defaults", "--non-interactive", "--force",
		"--config", configPath, "--card", cardPath)
	if forced.code != 0 {
		t.Fatalf("init --force exited %d: %s", forced.code, forced.output())
	}
	if _, err := os.Stat(configPath + ".bak"); err != nil {
		t.Fatalf("init --force did not back up the old config: %v", err)
	}
}

func TestCLIConfigModelRoundTrip(t *testing.T) {
	home := t.TempDir()
	configPath, _ := initNode(t, home)

	set := runCLI(t, home, "config", "model", "set", "--config", configPath,
		"--base-url", "https://example.invalid/anthropic",
		"--model", "regression-model", "--max-tokens", "512")
	if set.code != 0 {
		t.Fatalf("config model set exited %d: %s", set.code, set.output())
	}

	get := runCLI(t, home, "--json", "config", "model", "get", "--config", configPath)
	if get.code != 0 {
		t.Fatalf("config model get exited %d: %s", get.code, get.output())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(get.stdout), &got); err != nil {
		t.Fatalf("config model get --json is not JSON: %v\n%s", err, get.stdout)
	}
	if got["base_url"] != "https://example.invalid/anthropic" {
		t.Errorf("base_url = %v", got["base_url"])
	}
	if got["model"] != "regression-model" {
		t.Errorf("model = %v", got["model"])
	}
	if got["max_tokens"] != float64(512) {
		t.Errorf("max_tokens = %v", got["max_tokens"])
	}
	// The key was never written, and the output must not invent one.
	if got["api_key_set"] != false {
		t.Errorf("api_key_set = %v, want false", got["api_key_set"])
	}
}

func TestCLIModelAddSubcommandFlags(t *testing.T) {
	home := t.TempDir()
	configPath, _ := initNode(t, home)

	// Subcommand flags must reach modelAdd: the old top-level flag parse
	// reordered --force in front of "add" and rejected it as undefined.
	add := runCLI(t, home, "model", "add", "deepseek",
		"sk-test-00000000000000000000000000", "--force", "--config", configPath)
	if add.code != 0 {
		t.Fatalf("model add --force exited %d: %s", add.code, add.output())
	}
	status := runCLI(t, home, "model", "--config", configPath)
	if status.code != 0 || !strings.Contains(status.output(), "deepseek") {
		t.Fatalf("model status after add = %d: %s", status.code, status.output())
	}

	// --config before the subcommand works the same way.
	add = runCLI(t, home, "model", "--config", configPath, "add", "claude",
		"sk-test-00000000000000000000000000", "--force")
	if add.code != 0 {
		t.Fatalf("model --config add exited %d: %s", add.code, add.output())
	}
}

func TestCLIConfigRoutingAgentModels(t *testing.T) {
	home := t.TempDir()
	configPath, _ := initNode(t, home)

	set := runCLI(t, home, "config", "routing", "set", "agent_models",
		"opencode", "opencode-go/kimi-k3,opencode/deepseek-v4.1-flash-free",
		"--config", configPath)
	if set.code != 0 {
		t.Fatalf("config routing set agent_models exited %d: %s", set.code, set.output())
	}

	get := runCLI(t, home, "--json", "config", "routing", "get", "--config", configPath)
	if get.code != 0 {
		t.Fatalf("config routing get exited %d: %s", get.code, get.output())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(get.stdout), &got); err != nil {
		t.Fatalf("config routing get --json is not JSON: %v\n%s", err, get.stdout)
	}
	models, _ := got["agent_models"].(map[string]any)
	list, _ := models["opencode"].([]any)
	if len(list) != 2 || list[0] != "opencode-go/kimi-k3" ||
		list[1] != "opencode/deepseek-v4.1-flash-free" {
		t.Fatalf("agent_models = %v", got["agent_models"])
	}

	// "-" clears the list without disturbing preferred_agents.
	clear := runCLI(t, home, "config", "routing", "set", "agent_models",
		"opencode", "-", "--config", configPath)
	if clear.code != 0 {
		t.Fatalf("clear exited %d: %s", clear.code, clear.output())
	}
	get = runCLI(t, home, "--json", "config", "routing", "get", "--config", configPath)
	if err := json.Unmarshal([]byte(get.stdout), &got); err != nil {
		t.Fatalf("config routing get after clear is not JSON: %v\n%s", err, get.stdout)
	}
	if models, ok := got["agent_models"].(map[string]any); ok && len(models) > 0 {
		if list, ok := models["opencode"].([]any); ok && len(list) > 0 {
			t.Fatalf("agent_models not cleared: %v", got["agent_models"])
		}
	}
}

func TestCLICardAgentRoundTrip(t *testing.T) {
	home := t.TempDir()
	configPath, cardPath := initNode(t, home)

	add := runCLI(t, home, "card", "agent", "add", "regression_agent",
		"--adapter", "generic.py", "--tier", "1",
		"--config", configPath, "--card", cardPath)
	if add.code != 0 {
		t.Fatalf("card agent add exited %d: %s", add.code, add.output())
	}

	show := runCLI(t, home, "--json", "card", "show", "--card", cardPath)
	if show.code != 0 {
		t.Fatalf("card show exited %d: %s", show.code, show.output())
	}
	var got struct {
		Card struct {
			Agents map[string]struct {
				Adapter string `json:"adapter"`
				Tier    int    `json:"tier"`
			} `json:"agents"`
		} `json:"card"`
	}
	if err := json.Unmarshal([]byte(show.stdout), &got); err != nil {
		t.Fatalf("card show --json is not JSON: %v\n%s", err, show.stdout)
	}
	agent, ok := got.Card.Agents["regression_agent"]
	if !ok {
		t.Fatalf("card show does not contain the added agent: %s", show.stdout)
	}
	if agent.Adapter != "generic.py" || agent.Tier != 1 {
		t.Fatalf("agent = %+v, want adapter generic.py tier 1", agent)
	}
}

func TestCLITodoLifecycle(t *testing.T) {
	home := t.TempDir()
	configPath, _ := initNode(t, home)

	add := runCLI(t, home, "--json", "todo", "add", "regression todo", "--config", configPath)
	if add.code != 0 {
		t.Fatalf("todo add exited %d: %s", add.code, add.output())
	}
	var created struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(add.stdout), &created); err != nil {
		t.Fatalf("todo add --json is not JSON: %v\n%s", err, add.stdout)
	}
	if created.ID == 0 || created.Title != "regression todo" {
		t.Fatalf("created todo = %+v", created)
	}

	list := runCLI(t, home, "--json", "todo", "list", "--config", configPath)
	if list.code != 0 {
		t.Fatalf("todo list exited %d: %s", list.code, list.output())
	}
	var rows []struct {
		ID     int64  `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(list.stdout), &rows); err != nil {
		t.Fatalf("todo list --json is not JSON: %v\n%s", err, list.stdout)
	}
	if len(rows) != 1 || rows[0].Title != "regression todo" {
		t.Fatalf("board rows = %+v, want the one added todo", rows)
	}

	// done moves the row out of the default (open) view but not out of the
	// board: --all still shows it, in the done lane.
	done := runCLI(t, home, "--json", "todo", "done", strconv.FormatInt(created.ID, 10), "--config", configPath)
	if done.code != 0 {
		t.Fatalf("todo done exited %d: %s", done.code, done.output())
	}
	open := runCLI(t, home, "--json", "todo", "list", "--config", configPath)
	if open.code != 0 {
		t.Fatalf("todo list after done exited %d: %s", open.code, open.output())
	}
	var openRows []json.RawMessage
	if err := json.Unmarshal([]byte(open.stdout), &openRows); err != nil {
		t.Fatalf("todo list after done is not JSON: %v\n%s", err, open.stdout)
	}
	if len(openRows) != 0 {
		t.Fatalf("done todo still on the open board: %s", open.stdout)
	}
	all := runCLI(t, home, "--json", "todo", "list", "--all", "--config", configPath)
	var allRows []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(all.stdout), &allRows); err != nil {
		t.Fatalf("todo list --all is not JSON: %v\n%s", err, all.stdout)
	}
	if len(allRows) != 1 || allRows[0].Status != "done" {
		t.Fatalf("--all rows = %+v, want one done row", allRows)
	}
}

// TestCLITaskAddAndShow pins the queue's scriptable contract: adding a task
// prints only JSON on stdout (the "no consumer running" advisory is stderr,
// which is exactly what --json promises), and the id it returns reads back
// with the same fields. This is the path automation uses; a stray advisory on
// stdout would break every consumer of it.
func TestCLITaskAddAndShow(t *testing.T) {
	home := t.TempDir()
	configPath, _ := initNode(t, home)

	add := runCLI(t, home, "--json", "task", "add", "--title", "regression task",
		"--project", "demo", "--config", configPath)
	if add.code != 0 {
		t.Fatalf("task add exited %d: %s", add.code, add.output())
	}
	var created struct {
		TaskID string `json:"task_id"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal([]byte(add.stdout), &created); err != nil {
		t.Fatalf("task add --json wrote non-JSON to stdout: %v\nstdout:\n%s\nstderr:\n%s", err, add.stdout, add.stderr)
	}
	if created.TaskID == "" {
		t.Fatalf("task add returned no task_id: %s", add.stdout)
	}
	if created.State != "queued" {
		t.Fatalf("new task state = %q, want queued", created.State)
	}

	show := runCLI(t, home, "--json", "task", created.TaskID, "--config", configPath)
	if show.code != 0 {
		t.Fatalf("task show exited %d: %s", show.code, show.output())
	}
	var got struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Project string `json:"project"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal([]byte(show.stdout), &got); err != nil {
		t.Fatalf("task show --json is not JSON: %v\n%s", err, show.stdout)
	}
	if got.ID != created.TaskID || got.Title != "regression task" ||
		got.Project != "demo" || got.State != "queued" {
		t.Fatalf("task show = %+v, want the queued task just added", got)
	}
}

func TestCLICompletion(t *testing.T) {
	home := t.TempDir()
	for _, shell := range []string{"bash", "zsh", "fish"} {
		r := runCLI(t, home, "completion", shell)
		if r.code != 0 {
			t.Fatalf("completion %s exited %d: %s", shell, r.code, r.output())
		}
		if len(strings.TrimSpace(r.stdout)) == 0 {
			t.Fatalf("completion %s produced no script", shell)
		}
	}
}

func TestCLIPlanExample(t *testing.T) {
	home := t.TempDir()
	r := runCLI(t, home, "plan", "example")
	if r.code != 0 {
		t.Fatalf("plan example exited %d: %s", r.code, r.output())
	}
	if !strings.Contains(r.stdout, "stages:") {
		t.Fatalf("plan example does not look like a plan:\n%s", r.stdout)
	}
}
