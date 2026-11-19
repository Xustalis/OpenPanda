// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/objectives"
	"github.com/Xustalis/OpenPanda/internal/projects"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// The entry provider is scripted. Everything after task_submit is real:
// the main session stream, queue, separate POSIX processes, isolation,
// result merge, finalizer and authenticated artifact bytes.
func TestSessionScopedDispatchRunsRealProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process barrier")
	}
	for _, mode := range []string{"parallel", "overlap", "single-slot", "whole-project", "dependency", "out-of-scope"} {
		t.Run(mode, func(t *testing.T) { testSessionScopedDispatch(t, mode) })
	}
}

func testSessionScopedDispatch(t *testing.T, mode string) {
	testSessionScopedDispatchWithBrowser(t, mode, "")
}

// Opt-in: point this at a private directory to run the same proof from the
// packaged browser UI. endpoint.json and the two barrier files coordinate
// the browser driver; the regular test has no browser or network dependency.
func TestSessionScopedDispatchBrowser(t *testing.T) {
	dir := os.Getenv("PANDA_SESSION_BROWSER_DIR")
	if dir == "" || runtime.GOOS == "windows" {
		t.Skip("opt-in packaged console proof")
	}
	mode := os.Getenv("PANDA_SESSION_BROWSER_MODE")
	switch mode {
	case "":
		mode = "parallel"
	case "parallel", "dependency", "out-of-scope", "cancel", "retry", "missing-max":
	default:
		t.Fatalf("unknown browser proof mode %q", mode)
	}
	testSessionScopedDispatchWithBrowser(t, mode, dir)
}

func testSessionScopedDispatchWithBrowser(t *testing.T, mode, browserDir string) {
	isolateUserDirs(t)
	root, project := t.TempDir(), t.TempDir()
	timeout := 25 * time.Second
	if browserDir != "" {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	worker := filepath.Join(root, "worker.sh")
	write(worker, `set -eu
barrier="$1" name="$2" output="$3" mode="$4" fixture="$5"
printf '%s\n' "$$" > "$barrier/$name.pid"
i=0
while [ ! -f "$barrier/$name.release" ]; do
  i=$((i+1)); [ "$i" -lt 3600 ] || exit 80
  sleep 0.05
done
if [ "$mode" = dependency ] && [ "$name" = right ]; then
  [ "$(cat left.txt)" = left ] || exit 81
fi
if [ "$mode" = retry ] && [ "$name" = right ] && [ ! -f "$fixture/allow-retry" ]; then
  printf 'fixture execution failed\n' >&2
  exit 82
fi
printf '%s\n' "$name" > "$output"
if [ "$mode" = out-of-scope ] && [ "$name" = right ]; then
  printf 'unexpected\n' > undeclared.txt
fi
printf 'wrote %s\n' "$output"
`)
	paths := []string{"left.txt", "right.txt"}
	if mode == "overlap" {
		paths[1] = paths[0]
	}
	slots := 2
	if mode == "single-slot" {
		slots = 1
	}
	card := filepath.Join(root, "capabilities.yaml")
	write(card, fmt.Sprintf(`device: panel-scoped-proof
resource_class: Standard
native:
  - id: proof:left
    command: sh
    args: [%q, %q, left, %q, %q, %q]
    tier: 1
  - id: proof:right
    command: sh
    args: [%q, %q, right, %q, %q, %q]
    tier: 1
capacity: { cpu_cores: 4, ram_gb: 8, max_concurrent_tasks: %d }
`, worker, root, paths[0], mode, browserDir, worker, root, paths[1], mode, browserDir, slots))
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) != 1 {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Task receipts recorded.\"}}]}\n\ndata: [DONE]\n\n")
			return
		}
		policy := objectives.Policy{Version: 1, Revision: 1, Dimensions: []objectives.Dimension{{Dimension: "delivery", Weight: 1, Priority: objectives.Normal, Reason: "fixture", Requirement: "Produce the named file", Criteria: []string{"process writes the file"}}}}
		if mode == "dependency" {
			directive, _ := json.Marshal(map[string]any{"kind": "plan", "plan": map[string]any{
				"goal": "Produce a predecessor and consume its output", "final_stage": "right", "objectives": policy,
				"stages": []map[string]any{
					{"id": "left", "title": "left", "intent": "Produce left.txt", "requires": []string{"proof:left"}},
					{"id": "right", "title": "right", "intent": "Read left.txt then produce right.txt", "requires": []string{"proof:right"}, "needs": []string{"left"}},
				},
			}})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": string(directive)}}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		var content []map[string]any
		for i, name := range []string{"left", "right"} {
			payload := map[string]any{
				"title": name, "target": "Produce " + paths[i], "abilities": []string{"proof:" + name}, "paths": []string{paths[i]}, "objectives": policy,
			}
			if i == 1 && mode == "whole-project" {
				delete(payload, "paths")
			}
			if i == 1 && mode == "missing-max" {
				strict := policy
				strict.Dimensions = append([]objectives.Dimension(nil), policy.Dimensions...)
				strict.Dimensions[0].Priority = objectives.Max
				payload["objectives"] = strict
			}
			args, _ := json.Marshal(payload)
			content = append(content, map[string]any{"index": i, "type": "function", "id": "submit-" + name,
				"function": map[string]any{"name": "task_submit", "arguments": string(args)}})
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": content}, "finish_reason": "tool_calls"}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer model.Close()
	cfg := &config.Config{
		Node: config.NodeConfig{Name: "panel-scoped-proof", Kind: "vm", ResourceClass: "Standard"},
		Storage: config.StorageConfig{DBPath: filepath.Join(root, "panda.db"), WorkPath: filepath.Join(root, "work"),
			ProjectsPath: filepath.Join(root, "projects"), MemoryPath: filepath.Join(root, "memory"), SkillsPath: filepath.Join(root, "skills"), ArtifactPath: filepath.Join(root, "artifacts")},
		Model: config.ModelConfig{APIType: config.APITypeOpenAI, BaseURL: model.URL, NoAuth: true, Model: "test"},
		UI:    config.UIConfig{TermsAccepted: true, TermsVersion: config.TermsVersionCurrent, Locale: "en"},
	}
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatal(err)
	}
	proj := projects.NewStore(db)
	if _, err := proj.Create("proof", project, ""); err != nil {
		t.Fatal(err)
	}
	engine, err := askengine.New(ctx, cfg, askengine.Options{CardPath: card, QueueTasks: true, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	store := sessions.NewStore(filepath.Join(root, "sessions"))
	sess, err := store.Create("Scoped proof", "proof")
	if err != nil {
		t.Fatal(err)
	}
	pool := artifact.NewStore(cfg.Storage.ArtifactPath)
	staticDir := t.TempDir()
	if browserDir != "" {
		staticDir = "" // embedded production build
	}
	h := New(Deps{Store: engine.TaskStore(), Engine: engine, DB: db, Sessions: store, ProjectStore: proj, Artifacts: pool, StaticDir: staticDir, Token: testToken, Cfg: cfg})
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(method, path, strings.NewReader(body)).WithContext(ctx))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", path, rr.Code, rr.Body)
		}
		return rr
	}
	if browserDir != "" {
		browser := httptest.NewServer(h)
		defer func() {
			browser.CloseClientConnections()
			browser.Close()
		}()
		url := browser.URL + "/?token=" + testToken + "#/chat/" + sess.ID
		write(filepath.Join(browserDir, "endpoint.txt"), url)
	} else {
		stream := request(http.MethodPost, "/api/sessions/"+sess.ID+"/ask", `{"prompt":"Dispatch two independent fixture jobs to the named native workers."}`)
		if mode == "dependency" {
			if !strings.Contains(stream.Body.String(), `"kind":"plan"`) {
				t.Fatalf("plan receipt required: %s", stream.Body)
			}
		} else if strings.Count(stream.Body.String(), "event: task\n") != 2 {
			t.Fatalf("two task receipts required: %s", stream.Body)
		}
	}
	list := func() []taskJSON {
		t.Helper()
		var tasks []taskJSON
		if err := json.Unmarshal(request(http.MethodGet, "/api/tasks?brief=1&session="+sess.ID, "").Body.Bytes(), &tasks); err != nil {
			t.Fatal(err)
		}
		return tasks
	}
	wait := func(why string, pred func([]taskJSON) bool) []taskJSON {
		t.Helper()
		for ctx.Err() == nil {
			tasks := list()
			if pred(tasks) {
				return tasks
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("timed out %s", why)
		return nil
	}
	pid := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(root, name+".pid"))
		return strings.TrimSpace(string(b))
	}
	wantRunning := 1
	if mode != "overlap" && mode != "single-slot" && mode != "whole-project" && mode != "dependency" {
		wantRunning = 2
	}
	tasks := wait("waiting for staged process evidence", func(tasks []taskJSON) bool {
		if len(tasks) != 2 {
			return false
		}
		n := 0
		for _, task := range tasks {
			if task.Executing && pid(task.Title) != "" {
				n++
			}
		}
		return n == wantRunning
	})
	if tasks[0].OperationID == "" || tasks[0].OperationID != tasks[1].OperationID || tasks[0].DispatchOrder == tasks[1].DispatchOrder {
		t.Fatalf("bad persisted dispatch identities: %+v", tasks)
	}
	for _, task := range tasks {
		if task.Executing && task.CallName != "proof:"+task.Title {
			t.Fatalf("%s: expected actual call name, got %q", task.Title, task.CallName)
		}
	}
	if wantRunning == 2 {
		if pid("left") == pid("right") {
			t.Fatal("processes must have distinct PIDs")
		}
		t.Logf("overlap at shared barrier: left PID %s, right PID %s", pid("left"), pid("right"))
	} else {
		for _, task := range tasks {
			waitingState := core.StateQueued
			if mode == "dependency" {
				waitingState = core.StateSubmitted
				if task.Title == "right" && pid(task.Title) != "" {
					t.Fatal("consumer started before the predecessor produced an artifact")
				}
			}
			if pid(task.Title) == "" && task.State != waitingState {
				t.Fatalf("conflicting/over-limit task is not queued: %+v", task)
			}
		}
	}
	if browserDir != "" {
		if mode == "parallel" {
			for _, task := range tasks {
				if err := engine.TaskStore().RecordEvent(ctx, task.ID, core.EvAgentEvent, map[string]any{
					"ev": "thinking", "attempt_id": task.AttemptID, "thinking": "Fixture progress for " + task.Title,
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
		wait("for browser to verify both live rows", func(_ []taskJSON) bool {
			_, err := os.Stat(filepath.Join(browserDir, "release"))
			return err == nil
		})
	}
	write(filepath.Join(root, "left.release"), "go")
	if mode != "cancel" {
		write(filepath.Join(root, "right.release"), "go")
	}
	if mode == "cancel" || mode == "retry" || mode == "missing-max" {
		if browserDir == "" {
			t.Fatal("browser required for lifecycle proof")
		}
		var before string
		if mode == "retry" {
			tasks := wait("for explicit retry eligibility", func(tasks []taskJSON) bool {
				for _, task := range tasks {
					if task.Title == "right" && task.State == core.StateReview && task.RetryAvailable {
						before = task.AttemptID
						return true
					}
				}
				return false
			})
			if len(tasks) != 2 {
				t.Fatal("retry lost a task")
			}
			write(filepath.Join(browserDir, "retry-ready"), "ready")
		}
		wait("for browser to check lifecycle", func(_ []taskJSON) bool {
			_, err := os.Stat(filepath.Join(browserDir, "observed"))
			return err == nil
		})
		for _, task := range list() {
			if task.Title == "left" {
				if task.State != core.StateDone {
					t.Fatalf("left delivery lost: %s", task.State)
				}
				f := request(http.MethodGet, "/api/tasks/"+task.ID+"/artifact/file?path=left.txt", "")
				if strings.TrimSpace(f.Body.String()) != "left" {
					t.Fatal("left artifact changed")
				}
				continue
			}
			switch mode {
			case "cancel":
				if task.State != core.StateCancelled {
					t.Fatalf("cancel did not settle: %s", task.State)
				}
			case "retry":
				if task.State != core.StateDone || task.AttemptID == before {
					t.Fatalf("recovery did not deliver from a fresh attempt: %s", task.State)
				}
			case "missing-max":
				if task.State != core.StateReview || !task.NeedsFollowup {
					t.Fatalf("missing evidence must remain reviewable: %s", task.State)
				}
				approve := httptest.NewRecorder()
				h.ServeHTTP(approve, authedReq(http.MethodPost, "/api/tasks/"+task.ID+"/approve", strings.NewReader(`{"scope":"once"}`)).WithContext(ctx))
				if approve.Code != http.StatusConflict {
					t.Fatalf("missing evidence accepted or errored unexpectedly: %s", approve.Body)
				}
			}
		}
		return
	}
	if mode == "out-of-scope" {
		tasks = wait("waiting for the valid delivery and blocked merge", func(tasks []taskJSON) bool {
			states := make(map[string]string, len(tasks))
			for _, task := range tasks {
				states[task.Title] = task.State
			}
			return states["left"] == core.StateDone && states["right"] != "" && states["right"] != core.StateRunning
		})
		for _, task := range tasks {
			if task.Title != "right" {
				continue
			}
			full, err := engine.TaskStore().Get(ctx, task.ID)
			if err != nil || task.State != core.StateReview || !strings.Contains(full.ResultJSON, "outside the declared write scope") {
				t.Fatalf("out-of-scope merge should require review: %s %s %v", task.State, full.ResultJSON, err)
			}
		}
		for _, name := range []string{paths[1], "undeclared.txt"} {
			if _, err := os.Lstat(filepath.Join(project, name)); !os.IsNotExist(err) {
				t.Fatalf("blocked merge wrote %s to the project: %v", name, err)
			}
		}
		if browserDir == "" {
			return
		}
		wait("for browser to verify blocked delivery", func(_ []taskJSON) bool {
			_, err := os.Stat(filepath.Join(browserDir, "observed"))
			return err == nil
		})
		return
	}
	tasks = wait("waiting for both deliveries", func(tasks []taskJSON) bool {
		for _, task := range tasks {
			if task.State == core.StateReview || task.State == core.StateFailed {
				full, _ := engine.TaskStore().Get(ctx, task.ID)
				t.Fatalf("%s: %s: %s", task.Title, task.State, full.ResultJSON)
			}
		}
		return len(tasks) == 2 && tasks[0].State == core.StateDone && tasks[1].State == core.StateDone
	})
	for _, task := range tasks {
		path := paths[0]
		if task.Title == "right" {
			path = paths[1]
			if mode == "whole-project" {
				// Ordinary in-place native commands do not archive the shared tree.
				data, err := os.ReadFile(filepath.Join(project, path))
				if err != nil || strings.TrimSpace(string(data)) != task.Title {
					t.Fatalf("whole-project output: %q %v", data, err)
				}
				continue
			}
		}
		file := request(http.MethodGet, "/api/tasks/"+task.ID+"/artifact/file?path="+path, "")
		if strings.TrimSpace(file.Body.String()) != task.Title {
			t.Fatalf("wrong artifact for %s: %q", task.Title, file.Body)
		}
	}
	if browserDir != "" {
		wait("for browser to verify delivery", func(_ []taskJSON) bool {
			_, err := os.Stat(filepath.Join(browserDir, "observed"))
			return err == nil
		})
	}
}
