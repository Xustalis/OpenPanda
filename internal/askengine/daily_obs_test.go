// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/memory"
)

// Task summaries retain external provenance even when authored by a model.
func TestNoteDailyObservation(t *testing.T) {
	root := t.TempDir()
	hermes := memory.NewHermes(root)
	e := &Engine{
		daily:  memory.NewDaily(hermes.WarmDir()),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	e.noteDailyObservation(context.Background(), "任务总结：巡检脚本构建成功")
	e.noteDailyObservation(context.Background(), "   ") // blank digests are dropped
	e.noteDailyObservation(context.Background(), "")

	matches, err := filepath.Glob(filepath.Join(hermes.WarmDir(), "*.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("daily log not written: %v %v", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("daily log not written: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "任务总结：巡检脚本构建成功") {
		t.Fatalf("digest missing from daily log: %q", got)
	}
	if !strings.Contains(got, memory.ExternalMarker) {
		t.Fatalf("task summaries must remain external (%s): %q", memory.ExternalMarker, got)
	}
	if n := strings.Count(strings.TrimSpace(got), "\n") + 1; n != 1 {
		t.Fatalf("daily lines = %d, want 1 (blank digests dropped): %q", n, got)
	}

	// A nil-daily engine (hand-built test engines) must not panic.
	(&Engine{}).noteDailyObservation(context.Background(), "x")
}

func TestProjectSummaryStaysOutsidePersonalDaily(t *testing.T) {
	h := memory.NewHermes(t.TempDir())
	p := memory.NewProjects(t.TempDir())
	e := &Engine{daily: memory.NewDaily(h.WarmDir()), projects: p}
	ctx := memory.WithScopeProject(context.Background(), "alpha")
	e.noteDailyObservation(ctx, "用户偏好中文回答")
	if entries, err := os.ReadDir(h.WarmDir()); !os.IsNotExist(err) || len(entries) != 0 {
		t.Fatalf("personal daily touched: %v %v", entries, err)
	}
	path := filepath.Join(p.Root(), "alpha", "daily")
	paths, err := filepath.Glob(filepath.Join(path, "*.md"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("project daily missing: %v %v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil || !strings.Contains(string(data), memory.ExternalMarker) {
		t.Fatalf("project summary lost provenance: %s %v", data, err)
	}
}
