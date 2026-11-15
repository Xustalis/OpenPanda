// SPDX-License-Identifier: AGPL-3.0-or-later

package configsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func TestModelSyncAcrossIndependentRuntimes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.yaml")
	db := testdb.OpenFile(t, filepath.Join(t.TempDir(), "runtime.db"))
	var daemon, sidecar config.ModelConfig
	a := New(db, path, "daemon", func() config.ModelConfig { return daemon }, func(m config.ModelConfig) error { daemon = m; return nil })
	b := New(db, path, "engine", func() config.ModelConfig { return sidecar }, func(m config.ModelConfig) error { sidecar = m; return nil })
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("node:\n  name: sync-test\nmodel:\n  provider: deepseek\n  model: selected\n  api_key: super-secret\n")
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := ReadReport(ctx, db, path)
	if err != nil || len(report.Processes) != 1 || !report.Processes[0].Applied {
		t.Fatalf("report: %+v %v", report, err)
	}
	if err := b.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if daemon.Model != "selected" || sidecar.Model != daemon.Model {
		t.Fatalf("did not sync: %+v %+v", daemon, sidecar)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "super-secret") || strings.Contains(string(raw), "api_key") {
		t.Fatal("secret in acknowledgement")
	}
	// Disk change is pending until that process has actually observed it.
	write("node:\n  name: sync-test\nmodel:\n  provider: deepseek\n  model: next\n")
	report, _ = ReadReport(ctx, db, path)
	for _, p := range report.Processes {
		if p.Applied {
			t.Fatal("stale revision falsely acknowledged")
		}
	}
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if daemon.Model != "next" || sidecar.Model != "selected" {
		t.Fatal("runtime identities were conflated")
	}
	// Explicit clear propagates. File deletion must not clear credentials.
	write("node:\n  name: sync-test\nmodel: {}\n")
	if err := a.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if daemon.Configured() {
		t.Fatal("clear not propagated")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync(ctx); err == nil || sidecar.Model != "selected" {
		t.Fatal("missing config revoked last-good model")
	}
}

func TestModelSyncRejectsInvalidAndFailedApply(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.yaml")
	db := testdb.OpenFile(t, filepath.Join(t.TempDir(), "runtime.db"))
	current := config.ModelConfig{Model: "last-good"}
	reject := false
	w := New(db, path, "daemon", func() config.ModelConfig { return current }, func(mc config.ModelConfig) error {
		if reject {
			return errors.New("must-not-persist-secret")
		}
		current = mc
		return nil
	})
	for _, tc := range []struct {
		body, code string
		reject     bool
	}{
		{"model: [broken", "config_invalid", false},
		{"node:\n  name: sync-test\nmodel:\n  provider: deepseek\n", "apply_failed", true},
	} {
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		reject = tc.reject
		if err := w.Sync(ctx); err == nil {
			t.Fatal("expected rejection")
		}
		r, err := ReadReport(ctx, db, path)
		if err != nil || len(r.Processes) != 1 || r.Processes[0].Code != tc.code || r.Processes[0].Applied {
			t.Fatalf("report: %+v %v", r, err)
		}
		if current.Model != "last-good" {
			t.Fatal("rejection changed runtime")
		}
	}
	reject = false
	if err := w.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if current.Provider != "deepseek" {
		t.Fatal("did not recover")
	}
}

// The helper deliberately uses another OS process and another SQLite handle:
// an in-process Engine.SetModel call cannot make this acknowledgement appear.
func TestModelSyncSubprocessHelper(t *testing.T) {
	if os.Getenv("OPENPANDA_SYNC_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	path, dbPath := os.Getenv("OPENPANDA_SYNC_CONFIG"), os.Getenv("OPENPANDA_SYNC_DB")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var current config.ModelConfig
	w := New(db, path, "helper-daemon", func() config.ModelConfig { return current }, func(mc config.ModelConfig) error { current = mc; return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		_ = w.Sync(ctx) // a concurrent atomic replacement is retried just like Run
		if current.Model == "cross-process" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper did not observe external write")
}

func TestModelSyncAcrossOSProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	if err := os.WriteFile(path, []byte("node:\n  name: subprocess-sync\nmodel:\n  provider: deepseek\n  model: first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db := testdb.OpenFile(t, dbPath)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, exe, "-test.run=^TestModelSyncSubprocessHelper$", "-test.count=1")
	child.Env = append(os.Environ(), "OPENPANDA_SYNC_HELPER=1", "OPENPANDA_SYNC_CONFIG="+path, "OPENPANDA_SYNC_DB="+dbPath)
	var output bytes.Buffer
	child.Stdout = &output
	child.Stderr = &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if err := config.UpdateModelSection(path, config.ModelConfig{Provider: "deepseek", Model: "cross-process"}); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("helper: %v %s", err, output.String())
	}
	report, err := ReadReport(context.Background(), db, path)
	if err != nil || len(report.Processes) != 1 || !report.Processes[0].Applied || report.Processes[0].PID == os.Getpid() || report.Processes[0].Model != "cross-process" {
		t.Fatalf("missing cross-process acknowledgement: %+v %v; child %s", report, err, output.String())
	}
}
