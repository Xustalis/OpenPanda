// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFactAdmissionAndCategories(t *testing.T) {
	for _, tc := range []struct{ text, category, key, target string }{
		{"姓名：张三", "identity", "user.name", "user"},
		{"My name is Alice", "identity", "user.name", "user"},
		{"用户偏好中文回答", "preference", "preference.language", "user"},
		{"user prefers dark mode", "preference", "preference.theme", "user"},
		{"我住在上海", "personal", "user.location", "user"},
		{"Server is Ubuntu 24.04", "environment", "environment.os", "memory"},
		{"所有服务使用结构化日志", "convention", "logging.format", "memory"},
		{"go test ./...", "project", "test.command", "project"},
		{"User uses a wheelchair", "personal", "user.mobility", "user"},
		{"User is a vegetarian", "preference", "preference.diet", "user"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			category, key := "", ""
			if tc.key == "logging.format" || tc.target == "project" || tc.key == "user.mobility" || tc.key == "preference.diet" {
				category, key = tc.category, tc.key
			}
			fact, err := ClassifyFact(tc.text, category, key)
			if err != nil || fact.Category != tc.category || fact.Key != tc.key || fact.Target() != tc.target {
				t.Fatalf("fact=%+v err=%v", fact, err)
			}
			parsed, err := ClassifyFact(fact.Entry(), "", "")
			if err != nil || parsed != fact {
				t.Fatalf("roundtrip: %+v %v", parsed, err)
			}
		})
	}
	if _, err := ClassifyFact("User is a banana in this story", "", ""); !errors.Is(err, ErrNotDurable) {
		t.Fatalf("generic user statement inferred as occupation: %v", err)
	}
	for _, text := range []string{"今天想喝茶", "用户今天喜欢深色主题", "task completed successfully", "maybe user prefers tea", "用户偏好中文回答？", "hello", "[ext] 姓名：张三", "[fact:identity:user.name] [ext] 姓名：张三", "[fact:identity:user.name] 今天叫我张三", "姓名：张三\n用户喜欢茶", "姓名：张三§任务完成"} {
		if _, err := ClassifyFact(text, "", ""); !errors.Is(err, ErrNotDurable) {
			t.Errorf("admitted %q: %v", text, err)
		}
	}
	for _, tc := range []struct{ text, category, key string }{
		{"今天喜欢中文", "preference", "language"},
		{"用户姓名：张三", "project", "owner"},
		{"用户偏好中文", "environment", "language"},
		{"项目使用 Go", "convention", "stack"},
		{"long term", "unknown", "x"},
		{"long term", "identity", "../x"},
		{"long term", "identity", ""},
	} {
		if _, err := ClassifyFact(tc.text, tc.category, tc.key); !errors.Is(err, ErrNotDurable) {
			t.Errorf("admitted %+v: %v", tc, err)
		}
	}
}

func TestToolUpsertsCanonicalKeysAndKeepsIndependentPreferences(t *testing.T) {
	h := NewHermes(t.TempDir())
	tool := NewTool(h, nil)
	for _, text := range []string{"用户偏好中文回答", "user prefers English answers", "用户喜欢深色主题"} {
		if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user", "entry": text}); err != nil {
			t.Fatal(err)
		}
	}
	m, err := h.LoadUser()
	if err != nil || len(m.Entries) != 2 {
		t.Fatalf("%v %v", m, err)
	}
	facts := Facts(m, "user")
	if facts[0].Key != "preference.language" || facts[0].Text != "user prefers English answers" || facts[1].Key != "preference.theme" {
		t.Fatal(facts)
	}
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user", "entry": "用户偏好中文回答", "category": "preference", "key": "language"}); err != nil {
		t.Fatal(err)
	}
	m, _ = h.LoadUser()
	if len(m.Entries) != 2 || Facts(m, "user")[0].Text != "用户偏好中文回答" {
		t.Fatal(m)
	}
	before, _ := os.Stat(UserPath(h.root))
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user", "entry": "用户偏好中文回答"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(UserPath(h.root))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("idempotent write changed mtime")
	}
}

func TestFactUpdatePreservesDataOnFailureAndMovesLegacyFact(t *testing.T) {
	h := NewHermesWithLimits(t.TempDir(), Limits{User: 100})
	if err := h.SaveMemory(MemFile{Entries: []string{"用户偏好中文回答", "Server is Debian"}}); err != nil {
		t.Fatal(err)
	}
	tool := NewTool(h, nil)
	if _, err := tool.Execute(ToolReplace, map[string]any{"target": "memory", "old": "用户偏好中文回答", "new": "用户偏好英文回答"}); err != nil {
		t.Fatal(err)
	}
	user, _ := h.LoadUser()
	mem, _ := h.LoadMemory()
	if len(user.Entries) != 1 || len(mem.Entries) != 1 || mem.Entries[0] != "Server is Debian" {
		t.Fatalf("%v %v", user, mem)
	}
	snapshot := string(user.Bytes())
	if _, err := tool.Execute(ToolAdd, map[string]any{"target": "user", "entry": "user prefers " + strings.Repeat("English ", 40)}); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("capacity: %v", err)
	}
	user, _ = h.LoadUser()
	if string(user.Bytes()) != snapshot {
		t.Fatal("failed update erased existing fact")
	}
	if _, err := tool.Execute(ToolReplace, map[string]any{"target": "user", "old": "", "new": "用户偏好中文回答"}); err == nil {
		t.Fatal("empty old accepted")
	}
	if _, err := tool.Execute(ToolReplace, map[string]any{"target": "user", "old": "preference.language", "new": "用户偏好中文回答"}); err == nil {
		t.Fatal("metadata matched as assertion")
	}
}

func TestProjectFactsUpsertWithinScope(t *testing.T) {
	h := NewHermes(t.TempDir())
	projects := NewProjects(t.TempDir())
	tool := NewTool(h, projects)
	for _, project := range []string{"alpha", "beta"} {
		ctx := WithScopeProject(context.Background(), project)
		args := map[string]any{"target": "project", "entry": "go test ./...", "category": "project", "key": "test.command"}
		if _, err := tool.ExecuteCtx(ctx, ToolAdd, args); err != nil {
			t.Fatal(err)
		}
	}
	ctx := WithScopeProject(context.Background(), "alpha")
	if _, err := tool.ExecuteCtx(ctx, ToolAdd, map[string]any{"target": "project", "entry": "make test", "category": "project", "key": "test.command"}); err != nil {
		t.Fatal(err)
	}
	a, _ := projects.Load("alpha")
	b, _ := projects.Load("beta")
	if len(a.Entries) != 1 || !strings.Contains(a.Entries[0], "make test") || !strings.Contains(b.Entries[0], "go test") {
		t.Fatalf("%v %v", a, b)
	}
	for _, args := range []map[string]any{
		{"target": "project", "entry": "今天修复一次测试", "category": "project", "key": "test.command"},
		{"target": "project", "entry": "用户偏好中文", "category": "project", "key": "user.language"},
		{"target": "project", "project": "beta", "entry": "make build", "category": "project", "key": "build.command"},
		{"target": "user", "entry": "用户喜欢喝茶"},
	} {
		if _, err := tool.ExecuteCtx(ctx, ToolAdd, args); err == nil {
			t.Fatalf("scope/admission bypass: %v", args)
		}
	}
}

func TestDailyDropsEmptyAndDeduplicatesWithoutLosingProvenance(t *testing.T) {
	d := NewDaily(t.TempDir())
	now := time.Now()
	if err := d.Append(now, "   "); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.PathFor(now)); !os.IsNotExist(err) {
		t.Fatal("empty log created")
	}
	for i := 0; i < 3; i++ {
		if err := d.AppendExternal(now, "用户偏好中文回答"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Append(now, "用户偏好中文回答"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(d.PathFor(now))
	if err != nil || strings.Count(string(data), "\n") != 2 || strings.Count(string(data), ExternalMarker) != 1 {
		t.Fatalf("%s %v", data, err)
	}
}

func TestDreamRejectsRecurringNoiseFutureDatesAndConflictingOldHistory(t *testing.T) {
	h := NewHermes(t.TempDir())
	d := NewDaily(h.WarmDir())
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for day := 1; day <= 3; day++ {
		at := now.AddDate(0, 0, -day)
		for _, text := range []string{"今天临时喜欢深色主题", "任务总结：构建成功", "user prefers dark mode"} {
			if err := d.Append(at, text); err != nil {
				t.Fatal(err)
			}
		}
		if err := d.Append(now.AddDate(0, 0, day), "user prefers coffee"); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.SaveUser(MemFile{Entries: []string{"user prefers light mode"}}); err != nil {
		t.Fatal(err)
	}
	dream := NewDreamer(h)
	dream.now = func() time.Time { return now }
	candidates, err := dream.light()
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates: %+v %v", candidates, err)
	}
	report, err := dream.Dream()
	if err != nil || len(report.Promoted) != 0 {
		t.Fatalf("report: %+v %v", report, err)
	}
	m, _ := h.LoadUser()
	if len(m.Entries) != 1 || m.Entries[0] != "user prefers light mode" {
		t.Fatal(m)
	}
}

func TestDreamDoesNotPromoteOlderFrequentPreference(t *testing.T) {
	h := NewHermes(t.TempDir())
	d := NewDaily(h.WarmDir())
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, day := range []int{6, 5, 4} {
		if err := d.Append(now.AddDate(0, 0, -day), "user prefers dark mode"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Append(now.AddDate(0, 0, -1), "user prefers light mode"); err != nil {
		t.Fatal(err)
	}
	dream := NewDreamer(h)
	dream.now = func() time.Time { return now }
	report, err := dream.Dream()
	if err != nil || len(report.Promoted) != 0 {
		t.Fatalf("old frequent assertion won: %+v %v", report, err)
	}
}

func TestSchedulerPrunesIsolatedProjectDaily(t *testing.T) {
	h := NewHermes(t.TempDir())
	p := NewProjects(t.TempDir())
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	daily, err := p.Daily("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := daily.AppendExternal(now.AddDate(0, 0, -120), "old history"); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(NewDreamer(h), nil, func() bool { return false }, time.Hour).WithProjects(p)
	if _, err := s.tick(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(daily.PathFor(now.AddDate(0, 0, -120))); !os.IsNotExist(err) {
		t.Fatal("stale project history retained")
	}
}

func TestExplicitFactKeysKeepDevicesAndCategoriesIndependent(t *testing.T) {
	h := NewHermes(t.TempDir())
	tool := NewTool(h, nil)
	for _, args := range []map[string]any{
		{"target": "memory", "entry": "Server is Debian", "category": "environment", "key": "environment.server_a.os"},
		{"target": "memory", "entry": "Server is Ubuntu 24.04", "category": "environment", "key": "environment.server_b.os"},
		{"target": "memory", "entry": "Changes require code review", "category": "convention", "key": "shared"},
		{"target": "memory", "entry": "The default build host runs Linux", "category": "environment", "key": "shared"},
	} {
		if _, err := tool.Execute(ToolAdd, args); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := h.LoadMemory()
	if len(m.Entries) != 4 {
		t.Fatalf("independent keys merged: %v", m)
	}
}

func TestPersonalTransactionRollsBackEarlierWrite(t *testing.T) {
	h := NewHermes(t.TempDir())
	if err := h.SaveUser(MemFile{Entries: []string{"姓名：张三"}}); err != nil {
		t.Fatal(err)
	}
	err := h.updatePersonalFiles(0, func(files []personalFile) error {
		files[0].mem.Entries = []string{"姓名：李四"}
		files[1].mem.Entries = []string{"Server is Ubuntu 24.04"}
		// The destination becomes unwritable after snapshots were loaded. This
		// deterministically exercises failure after the first file was committed.
		return os.Mkdir(MemoryPath(h.root), 0o700)
	})
	if err == nil {
		t.Fatal("expected second-file write failure")
	}
	user, err := h.LoadUser()
	if err != nil || len(user.Entries) != 1 || user.Entries[0] != "姓名：张三" {
		t.Fatalf("earlier write was not restored: %v %v", user, err)
	}
}

func TestPersonalTransactionRestoresOriginalBytesAndAbsentFile(t *testing.T) {
	for _, original := range []string{"", "  " + strings.Repeat("legacy data ", 20) + "\n"} {
		t.Run(fmt.Sprint(len(original)), func(t *testing.T) {
			h := NewHermesWithLimits(t.TempDir(), Limits{User: 80})
			if original != "" {
				if err := os.WriteFile(UserPath(h.root), []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := h.updatePersonalFiles(0, func(files []personalFile) error {
				files[0].mem.Entries = []string{"姓名：李四"}
				files[1].mem.Entries = []string{"Server is Debian"}
				return os.Mkdir(MemoryPath(h.root), 0o700)
			})
			if err == nil {
				t.Fatal("expected second-file write failure")
			}
			data, err := os.ReadFile(UserPath(h.root))
			if original == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("absent file not restored: %q %v", data, err)
				}
			} else if err != nil || string(data) != original {
				t.Fatalf("original bytes not restored: %q %v", data, err)
			}
		})
	}
}

func TestToolReplaceExplicitClassificationCleansPreviousAttribute(t *testing.T) {
	h := NewHermes(t.TempDir())
	old := "用户喜欢使用机械键盘"
	if err := h.SaveMemory(MemFile{Entries: []string{old}}); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveTopic("input", MemFile{Entries: []string{old}}); err != nil {
		t.Fatal(err)
	}
	tool := NewTool(h, nil)
	if _, err := tool.Execute(ToolReplace, map[string]any{
		"target": "memory", "old": old, "new": "首选输入设备为静音机械键盘",
		"category": "preference", "key": "preference.keyboard",
	}); err != nil {
		t.Fatal(err)
	}
	user, _ := h.LoadUser()
	mem, _ := h.LoadMemory()
	topic, _ := h.LoadTopic("input")
	if len(user.Entries) != 1 || len(mem.Entries) != 0 || len(topic.Entries) != 0 {
		t.Fatalf("reclassification left old attributes: %v %v %v", user, mem, topic)
	}
	if facts := Facts(user, "user"); len(facts) != 1 || facts[0].Key != "preference.keyboard" {
		t.Fatal(facts)
	}
}

func TestForgetRemovesLegacyDuplicates(t *testing.T) {
	h := NewHermes(t.TempDir())
	if err := h.SaveUser(MemFile{Entries: []string{"用户偏好中文回答"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveTopic("prefs", MemFile{Entries: []string{"用户偏好英文回答"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.Forget("user", "用户偏好中文"); err != nil {
		t.Fatal(err)
	}
	topic, _ := h.LoadTopic("prefs")
	if len(topic.Entries) != 0 {
		t.Fatalf("stale duplicate resurfaces after removal: %v", topic)
	}
}

func TestDreamTrustedDaysFollowLocalCalendar(t *testing.T) {
	now := time.Date(2026, 11, 14, 1, 0, 0, 0, time.FixedZone("east", 14*3600))
	c := &Candidate{Sources: []Source{{Path: "2026-11-12.md", Trusted: true}, {Path: "2026-11-13.md", Trusted: true}, {Path: "2026-11-14.md", Trusted: true}, {Path: "2026-11-15.md", Trusted: true}}}
	if n := trustedDaysWithin(c, now, 7*24*time.Hour); n != 3 {
		t.Fatalf("trusted days=%d, want 3 local calendar days", n)
	}
}
