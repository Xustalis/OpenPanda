// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// actuatorCard builds a card whose actuator drives /bin/echo — a stand-in for
// real drivers (panda-servo & friends) that needs no hardware and exists on
// every POSIX test host. Its argv exercises every placeholder the spec can
// fill: {action}, {param:<name>} and {intent}.
func actuatorCard(id string) ledger.Card {
	return ledger.Card{
		Device:        id,
		ResourceClass: "Standard",
		Actuators: []ledger.ActuatorProfile{{
			ID:       "hardware:echo_servo",
			Type:     "hardware",
			Category: "motor_control",
			Tier:     1,
			Command:  "/bin/echo",
			Args:     []string{"acted", "{action}", "{param:angle}", "{intent}"},
		}},
		Capacity: ledger.Capacity{CPUCores: 8, RAMGB: 16, MaxConcurrent: 3},
	}
}

func newCoreWithActuator(t *testing.T, id, addr string) *Core {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("actuator e2e uses /bin/echo, a POSIX fixture")
	}
	db := openTestDB(t)
	c := NewCore(db, id, actuatorCard(id), 5, testLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	c.SetWorkDir(t.TempDir())
	return c
}

const echoServoSpec = `{"action_spec":{"target_actuator":"hardware:echo_servo","action":"rotate","parameters":{"angle":90}}}`

// TestActuatorTaskExecutesDriverLocally is the single-node half of the §7.2
// dispatch contract: a task requiring the actuator resolves to a native plan,
// SubstituteActionSpec fills the argv template, and the driver really runs —
// its stdout is the proof.
func TestActuatorTaskExecutesDriverLocally(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithActuator(t, "act-local", "")

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "turn servo",
		ContextType: "hardware",
		Intent:      "turn the servo to 90 degrees",
		Requires:    []string{"hardware:echo_servo"},
		SpecJSON:    echoServoSpec,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if task.State != StateDone {
		t.Fatalf("state = %s, want done", task.State)
	}
	if !result.OK {
		t.Fatalf("result failed: %+v", result)
	}
	out := result.Stdout + result.Stderr
	if !strings.Contains(out, "acted rotate 90 turn the servo to 90 degrees") {
		t.Fatalf("driver output missing substituted argv: %q", out)
	}
}

// TestActuatorTaskCrossDevice runs the actuator dispatch across the wire: the
// origin node routes on the actuator ability id, the action_spec survives in
// spec_json, and the executor's driver runs with the substituted argv.
func TestActuatorTaskCrossDevice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root := newCore(t, "act-root", "127.0.0.1:18210")
	leaf := newCoreWithActuator(t, "act-leaf", "127.0.0.1:18211")
	startPair(t, ctx, root, leaf, "127.0.0.1:18210", "127.0.0.1:18211")

	task, result, err := root.Submit(ctx, TaskInput{
		Title:       "turn servo",
		ContextType: "hardware",
		Intent:      "turn the servo to 90 degrees",
		Requires:    []string{"hardware:echo_servo"},
		SpecJSON:    echoServoSpec,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.OK {
		t.Fatalf("result failed: %+v", result)
	}
	out := result.Stdout + result.Stderr
	if !strings.Contains(out, "acted rotate 90") {
		t.Fatalf("remote driver output missing: %q", out)
	}

	// The executor's row is terminal too — the spec was parsed and the driver
	// ran under its own task dir.
	deadline := time.Now().Add(10 * time.Second)
	for {
		lt, gerr := leaf.store.Get(ctx, task.TaskID)
		if gerr == nil && Terminal(lt.State) {
			if lt.State != StateDone {
				t.Fatalf("leaf task = %s, want done", lt.State)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("leaf never finished the actuator task")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestActuatorTaskSpecMismatchRefuses guards the wrong-actuator gate: an
// action_spec naming a different actuator than the one the plan resolved must
// fail at substitution, never execute a second device's driver with the first
// spec's parameters.
func TestActuatorTaskSpecMismatchRefuses(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithActuator(t, "act-mismatch", "")

	marker := filepath.Join(t.TempDir(), "ran")
	spec := `{"action_spec":{"target_actuator":"hardware:other","action":"rotate","parameters":{"angle":90,"m":"` + marker + `"}}}`
	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "confused",
		ContextType: "hardware",
		Intent:      "rotate",
		Requires:    []string{"hardware:echo_servo"},
		SpecJSON:    spec,
	})
	// The task must not report success — substitution is a hard gate.
	if err == nil && result.OK {
		t.Fatalf("mismatched action_spec executed: %+v", result)
	}
	if task.State == StateDone {
		t.Fatalf("mismatched action_spec reached done")
	}
}

// TestActuatorTaskMissingParamFails guards the placeholder contract: a driver
// that needs {param:angle} must not run when the spec omits it — the error
// names the missing parameter instead of executing literal braces.
func TestActuatorTaskMissingParamFails(t *testing.T) {
	ctx := context.Background()
	c := newCoreWithActuator(t, "act-noparam", "")

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "no angle",
		ContextType: "hardware",
		Intent:      "rotate",
		Requires:    []string{"hardware:echo_servo"},
		SpecJSON:    `{"action_spec":{"target_actuator":"hardware:echo_servo","action":"rotate"}}`,
	})
	if err == nil && result.OK {
		t.Fatalf("missing param executed: %+v", result)
	}
	if task.State == StateDone {
		t.Fatalf("missing param reached done")
	}
}

// TestActuatorSpecOnNonActuatorPlanFailsClosed guards the mirror check: a spec
// naming a hardware target that reached a NON-actuator plan (its id never made
// requires, or requires matched a plain ability first) must fail the dispatch —
// silently dropping the spec would run the intent text as a shell command,
// claiming hardware action that never happened.
func TestActuatorSpecOnNonActuatorPlanFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/echo fixture")
	}
	ctx := context.Background()
	db := openTestDB(t)
	card := actuatorCard("act-strand")
	card.Native = []ledger.NativeAbility{{
		ID: "shell_ok", Command: "/bin/echo", Args: []string{"ran-native"},
	}}
	c := NewCore(db, "act-strand", card, 5, testLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	c.SetWorkDir(t.TempDir())

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:    "stranded spec",
		Intent:   "rotate",
		Requires: []string{"shell_ok"},
		SpecJSON: `{"action_spec":{"target_actuator":"hardware:echo_servo","action":"rotate","parameters":{"angle":90}}}`,
	})
	// The spec asked for hardware but requires resolved a plain native plan:
	// dispatch must fail, never execute the intent as a native command.
	if err == nil && result.OK {
		t.Fatalf("stranded action_spec executed a non-actuator plan: %+v", result)
	}
	if task.State == StateDone {
		t.Fatalf("stranded action_spec reached done")
	}
	if strings.Contains(result.Stdout+result.Stderr, "ran-native") {
		t.Fatalf("non-actuator plan executed despite action_spec: %+v", result)
	}
}

// TestLockActuatorSerializes pins the primitive behind the run()-level
// device lock: same actuator id blocks, a different id does not.
func TestLockActuatorSerializes(t *testing.T) {
	c := newCoreWithActuator(t, "act-lock", "")
	release := c.lockActuator("hardware:echo_servo")
	acquired := make(chan struct{})
	go func() {
		r := c.lockActuator("hardware:echo_servo")
		r()
		close(acquired)
	}()
	select {
	case <-acquired:
		release()
		t.Fatal("second lockActuator acquired while the first was held")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("actuator lock was never released")
	}
	// A different actuator id is a different device — it must not block.
	other := c.lockActuator("hardware:other_dev")
	other()
}

// TestActuatorRunsSerializeOnDevice is the execution-layer half of the
// 2026-09-29 audit P0: two tasks reaching an actuator plan concurrently —
// the inline path never passes through the queue's resource-key registry —
// must not run their drivers at the same time. The driver guards a sentinel
// directory and reports CONCURRENT-CONFLICT if a second process enters while
// the first holds the device.
func TestActuatorRunsSerializeOnDevice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh fixture")
	}
	ctx := context.Background()
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "dev.lock")
	db := openTestDB(t)
	card := actuatorCard("act-ser")
	card.Actuators[0].Command = "/bin/sh"
	card.Actuators[0].Args = []string{"-c",
		"if ! mkdir '" + sentinel + "'; then echo CONCURRENT-CONFLICT; exit 9; fi; sleep 0.2; rmdir '" + sentinel + "'; echo acted-ok"}
	c := NewCore(db, "act-ser", card, 5, testLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	c.SetWorkDir(dir)

	var wg sync.WaitGroup
	results := make([]bus.TaskResultPayload, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, res, err := c.SubmitLocal(ctx, TaskInput{
				Title: "drive servo", Intent: "rotate", ContextType: "hardware",
				Requires: []string{"hardware:echo_servo"}, Authorized: true,
			})
			results[i], errs[i] = res, err
		}(i)
	}
	wg.Wait()
	for i := range results {
		out := results[i].Stdout + results[i].Stderr
		if strings.Contains(out, "CONCURRENT-CONFLICT") {
			t.Fatalf("task %d saw a concurrent driver on the same device: %q (err %v)", i, out, errs[i])
		}
		if errs[i] != nil || !results[i].OK || !strings.Contains(out, "acted-ok") {
			t.Fatalf("task %d failed serialized run: %+v err=%v", i, results[i], errs[i])
		}
	}
}

// TestActuatorTaskSpeclessIntent still works: no action_spec at all fills
// {action}/{param} with nothing — the driver gets {intent} only. A plan whose
// argv needs {param} fails instead (covered above); a placeholder-free driver
// is the shape this exercises indirectly through intent text.
func TestActuatorDriverWithoutPlaceholders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/echo fixture")
	}
	ctx := context.Background()
	db := openTestDB(t)
	card := actuatorCard("act-plain")
	card.Actuators[0].Args = []string{"pong"}
	c := NewCore(db, "act-plain", card, 5, testLogger(), config.ModelConfig{})
	c.SetSharedSecret(testSharedSecret)
	c.SetWorkDir(t.TempDir())

	task, result, err := c.SubmitLocal(ctx, TaskInput{
		Title:       "ping actuator",
		ContextType: "hardware",
		Intent:      "ping the actuator",
		Requires:    []string{"servo"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.OK || task.State != StateDone {
		t.Fatalf("placeholder-free actuator task failed: %+v state=%s", result, task.State)
	}
	if !strings.Contains(result.Stdout+result.Stderr, "pong") {
		t.Fatalf("driver output missing: %+v", result)
	}
}
