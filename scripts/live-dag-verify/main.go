// SPDX-License-Identifier: AGPL-3.0-or-later
// Opt-in, paid, real-harness verification. Never included in unit tests.
// Uses production Core, queue, artifacts, permissions, skills and acceptance.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Xustalis/OpenPanda/internal/artifact"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/fslock"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/plan"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func write(path string, v any) {
	b, e := json.MarshalIndent(v, "", "  ")
	must(e)
	must(os.WriteFile(path, append(b, '\n'), 0600))
}
func main() {
	rootFlag := flag.String("root", "", "isolated output directory (must not contain runtime.db)")
	planFlag := flag.String("plan", "", "real-model plan JSON")
	sourceFlag := flag.String("source", "", "source snapshot directory")
	validator := flag.String("validator", "", "absolute independent Python validator path")
	slots := flag.Int("slots", 3, "concurrent harness processes")
	maxTurns := flag.Int("max-turns", 45, "per-stage native turn cap; 1 forces a real checkpoint-continuation probe")
	resumeTask := flag.String("resume-checkpoint", "", "explicit failed checkpoint task in an existing stopped run")
	resumeNote := flag.String("resume-note", "", "operator instructions for explicit checkpoint continuation")
	deadline := flag.Duration("timeout", 25*time.Minute, "overall deadline")
	flag.Parse()
	if *maxTurns < 1 || *maxTurns > 200 {
		panic("max-turns must be 1..200")
	}
	if *rootFlag == "" || *planFlag == "" || *sourceFlag == "" || *validator == "" {
		panic("root, plan, source, validator required")
	}
	root, e := filepath.Abs(*rootFlag)
	must(e)
	source, e := filepath.Abs(*sourceFlag)
	must(e)
	must(os.MkdirAll(root, 0700))
	release, e := fslock.Acquire(filepath.Join(root, "runner.lock"), false)
	must(e)
	defer release()
	dbPath := filepath.Join(root, "runtime.db")
	if _, e = os.Stat(dbPath); *resumeTask == "" && !os.IsNotExist(e) {
		panic("refusing to reuse existing runtime.db without explicit resume-checkpoint")
	} else if *resumeTask != "" && e != nil {
		must(e)
	}
	raw, e := os.ReadFile(*planFlag)
	must(e)
	var p plan.Plan
	must(json.Unmarshal(raw, &p))
	must(plan.Validate(p))
	planHash := sha256.Sum256(raw)
	validatorBytes, e := os.ReadFile(*validator)
	must(e)
	validatorHash := sha256.Sum256(validatorBytes)
	sk := skills.NewStore(filepath.Join(root, "skills"))
	for _, def := range []struct {
		name, desc, body string
		triggers         []string
	}{
		{"editorial-design", "Editorial visual design and integration", "Use a warm ivory and terracotta editorial design with a restrained serif headline and readable system sans. Implement primitive -> semantic -> component CSS tokens. Read the provided design-system master as input, but the explicit user brief overrides its generic suggestions. Use intentional whitespace, fine dividers, accessible 4.5:1 text, visible focus, 44px controls, reduced motion, responsive 375/768/1440. Never use fake metrics, externally fetched fonts or placeholder icons. Verify screenshots if browser tools are available; otherwise explicitly report visual validation unperformed.", []string{"design", "integrate", "visual", "tokens", "verification"}},
		{"truthful-product-copy", "Concise truthful product copy and semantic integrity", "Write clear specific English. Label sample metrics demo data. Distinguish planned, running, blocked and verified states. Never describe a tool exit or file existence as full task completion. Preserve all requirements and never claim unrun tests or unmeasured speedups. Avoid stock AI marketing clichés. Report concrete deliverables and remaining limitations.", []string{"copy", "narrative", "verify", "verification"}},
		{"accessible-interaction", "Accessible offline interaction implementation", "Implement keyboard-operable filtering and details with semantic buttons, aria-pressed, dialog naming, Escape close and focus return. Offline assets only. Parse and validate JSON, handle loading/errors, respect reduced motion. Test actual behavior, never rely on syntax check alone for interaction claims.", []string{"behavior", "interaction", "integrate", "verify", "verification"}},
	} {
		must(sk.Save(&skills.Skill{Name: def.name, Description: def.desc, Scope: skills.ScopeGlobal, Status: skills.StatusActive, Triggers: def.triggers, Body: def.body}))
	}
	for i := range p.Stages {
		st := &p.Stages[i]
		checks, err := contract.Validate([]contract.Check{{ID: "independent-delivery", Kind: "command", Run: fmt.Sprintf("python3 %q %s", *validator, st.ID)}})
		must(err)
		spec := entry.TaskSpecDetail{Scope: "isolated stage workspace", SuccessDefinition: "All declared deliverables implemented; independent acceptance passes; no false completion.", ToolsPolicy: "extended", MaxTurns: *maxTurns, Acceptance: checks}
		b, e := json.Marshal(spec)
		must(e)
		st.SpecJSON = string(b)
		st.Intent += "\nRuntime acceptance: run python3 " + *validator + " " + st.ID + " from your workspace and fix failures. Do not edit the external validator. Do not start long-lived servers. Do not read credentials or alter files outside this workspace. Skills provided in the prompt are scoped guidance, not extra permissions."
	}
	if *resumeTask == "" {
		write(filepath.Join(root, "execution-plan.json"), p)
	}
	db, e := storage.Open(dbPath)
	must(e)
	defer db.Close()
	must(storage.Migrate(db))
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	card := ledger.Card{Device: "live-dag", ResourceClass: "Standard", Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: *slots}, Agents: map[string]ledger.Agent{"claude-code": {Adapter: "claude_code.py", Capabilities: []string{"web:build"}, Tier: 2}}}
	c := core.NewCore(db, "live-dag", card, 5, logger, config.ModelConfig{})
	pandaTools := false
	c.SetRouterPolicy(config.InjectionConfig{Model: "never"}, config.RoutingConfig{MaxConcurrent: *slots, AgentConcurrency: map[string]int{"claude-code": *slots}, ToolsPolicy: "extended", PandaTools: &pandaTools})
	c.SetMemoryStores(nil, nil, sk)
	c.SetWorkDir(root)
	c.SetScratchRoot(filepath.Join(root, "work"))
	c.SetArtifactStore(artifact.NewStore(filepath.Join(root, "artifacts")))
	c.SetTimeouts(config.TimeoutsConfig{AgentS: 900, TaskLeaseS: 1200})
	c.SetSuperviseRounds(2)
	ctx, cancel := context.WithTimeout(context.Background(), *deadline)
	defer cancel()
	must(c.Register(ctx))
	started := time.Now()
	id := ""
	exportDir := root
	if *resumeTask == "" {
		q := core.DefaultQueueSpec()
		q.WorkDir = source
		q.Authorized = true
		id, e = c.StartPlan(ctx, p, q)
		must(e)
	} else {
		// The operator must stop the previous runner before taking this lock.
		// Refuse active rows; never compete with a previous in-flight execution.
		active, err := c.TaskStore().ListActive(ctx)
		must(err)
		for _, t := range active {
			if t.State == core.StateRunning || t.State == core.StateDispatched || t.State == core.StateQueued {
				panic("previous run still has active tasks")
			}
		}
		t, err := c.TaskStore().Get(ctx, *resumeTask)
		must(err)
		if t.State != core.StateFailed || t.OwnerNode != "live-dag" || t.AgentSessionID == "" || t.AgentSessionNode != "live-dag" {
			panic("resume requires failed local checkpoint")
		}
		id = t.PlanID
		if id == "" {
			panic("resume requires an existing plan")
		}
		exportDir = filepath.Join(root, "continuations", started.UTC().Format("20060102T150405Z"))
		must(os.MkdirAll(exportDir, 0700))
		// Use the same guarded approval/dispatch APIs as operator resumption.
		must(c.TaskStore().ReviewWithDispositionAttempt(ctx, t.TaskID, "live-dag", t.AttemptID, "explicit operator checkpoint continuation", core.ApprovalResumeExecution))
		go func() {
			_, _, err := c.ResumeApproved(ctx, t.TaskID, *resumeNote)
			if err != nil {
				logger.Error("explicit checkpoint continuation", "err", err)
			}
		}()
	}
	c.StartQueueScheduler(ctx)
	write(filepath.Join(exportDir, "run.json"), map[string]any{"plan_id": id, "started": started.UTC(), "slots": *slots, "harness": "claude_code.py", "model_policy": "native default, actual model in native events", "plan_sha256": fmt.Sprintf("%x", planHash), "validator_sha256_at_start": fmt.Sprintf("%x", validatorHash), "resume_task": *resumeTask, "supervision": "independent command contracts; no model judge configured"})
	fmt.Printf("START plan=%s slots=%d resume=%s\n", id, *slots, *resumeTask)
	if version, err := exec.Command("claude", "--version").Output(); err == nil {
		must(os.WriteFile(filepath.Join(exportDir, "harness-version.txt"), version, 0600))
	}
	last := ""
	for {
		tasks, e := c.TaskStore().PlanStages(context.Background(), id)
		must(e)
		state := ""
		done := 0
		blocked := false
		for _, t := range tasks {
			state += t.StageID + "=" + string(t.State) + " "
			if t.State == core.StateDone {
				done++
			}
			if t.State == core.StateReview || t.State == core.StateCancelled {
				blocked = true
			}
			if t.State == core.StateFailed {
				events, err := c.TaskStore().Events(ctx, t.TaskID)
				must(err)
				if !awaitingRecovery(events) {
					blocked = true
				}
			}
		}
		if state != last {
			fmt.Println(time.Now().UTC().Format(time.RFC3339Nano), state)
			last = state
		}
		if done == len(p.Stages) || (blocked && time.Since(started) > 2*time.Second && !hasActive(tasks)) || ctx.Err() != nil {
			write(filepath.Join(exportDir, "tasks.json"), tasks)
			for _, t := range tasks {
				ev, e := c.TaskStore().Events(context.Background(), t.TaskID)
				must(e)
				write(filepath.Join(exportDir, "events-"+t.StageID+".json"), ev)
			}
			write(filepath.Join(exportDir, "summary.json"), map[string]any{"plan_id": id, "done": done, "stages": len(tasks), "elapsed_s": time.Since(started).Seconds(), "blocked": blocked, "finished": time.Now().UTC()})
			cancel()
			shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
			c.Shutdown(shutdown)
			stop()
			if done != len(p.Stages) {
				os.Exit(1)
			}
			return
		}
		select {
		case <-ctx.Done():
			// Next iteration persists terminal/partial evidence before exit.
		case <-time.After(time.Second):
		}
	}
}

func hasActive(tasks []core.Task) bool {
	for _, t := range tasks {
		if t.State == core.StateRunning || t.State == core.StateDispatched || t.State == core.StateQueued {
			return true
		}
	}
	return false
}

// A transient failed row can precede the retry backoff. The persisted recovery
// assessment, not that state alone, tells a watcher whether execution continues.
func awaitingRecovery(events []core.Event) bool { return core.RecoveryPending(events) }
