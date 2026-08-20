package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLimitsConfigured verifies A3: the character caps are constructor
// parameters (config memory.limits), and the compile-time constants only
// survive as zero-value fallbacks.
func TestLimitsConfigured(t *testing.T) {
	// A tighter-than-default cap is enforced.
	tight := NewHermesWithLimits(t.TempDir(), Limits{User: 20, Memory: 30})
	if err := tight.SaveUser(MemFile{Entries: []string{strings.Repeat("x", 25)}}); !errors.Is(err, ErrOverLimit) {
		t.Errorf("tight user cap not enforced: %v", err)
	}
	if err := tight.SaveMemory(MemFile{Entries: []string{strings.Repeat("y", 40)}}); !errors.Is(err, ErrOverLimit) {
		t.Errorf("tight memory cap not enforced: %v", err)
	}

	// A looser-than-default cap admits content the old constants rejected.
	loose := NewHermesWithLimits(t.TempDir(), Limits{Memory: MemoryCharLimit * 10})
	big := strings.Repeat("z", MemoryCharLimit+500)
	if err := loose.SaveMemory(MemFile{Entries: []string{big}}); err != nil {
		t.Errorf("loose cap should admit %d chars: %v", len(big), err)
	}

	// Zero limits fall back to the historical constants.
	fallback := NewHermes(t.TempDir())
	if err := fallback.SaveMemory(MemFile{Entries: []string{strings.Repeat("q", MemoryCharLimit+1)}}); !errors.Is(err, ErrOverLimit) {
		t.Errorf("fallback cap should still be MemoryCharLimit: %v", err)
	}

	// Projects honors its own configured cap.
	p := NewProjectsWithLimits(t.TempDir(), Limits{Project: 15})
	if err := p.Save("demo", MemFile{Entries: []string{strings.Repeat("p", 20)}}); !errors.Is(err, ErrOverLimit) {
		t.Errorf("project cap not enforced: %v", err)
	}
	if p.Limit() != 15 {
		t.Errorf("Limit() = %d, want 15", p.Limit())
	}
	if NewProjects(t.TempDir()).Limit() != ProjectCharLimit {
		t.Errorf("fallback Limit() = %d, want ProjectCharLimit", NewProjects(t.TempDir()).Limit())
	}
}

// TestTopicsCRUD verifies the topics/ extension files: list/save/load/delete
// round-trip, the shared memory-limit cap, and name validation against
// directory traversal.
func TestTopicsCRUD(t *testing.T) {
	h := NewHermesWithLimits(t.TempDir(), Limits{Memory: 50})

	if names, err := h.ListTopics(); err != nil || len(names) != 0 {
		t.Fatalf("fresh store topics = %v, err %v", names, err)
	}
	if err := h.SaveTopic("work", MemFile{Entries: []string{"team ships on fridays"}}); err != nil {
		t.Fatalf("save topic: %v", err)
	}
	if err := h.SaveTopic("home", MemFile{Entries: []string{"cat named pixel"}}); err != nil {
		t.Fatalf("save topic: %v", err)
	}

	names, err := h.ListTopics()
	if err != nil || len(names) != 2 || names[0] != "home" || names[1] != "work" {
		t.Fatalf("ListTopics = %v, err %v", names, err)
	}
	got, err := h.LoadTopic("work")
	if err != nil || len(got.Entries) != 1 || got.Entries[0] != "team ships on fridays" {
		t.Fatalf("LoadTopic = %+v, err %v", got, err)
	}
	if got.Limit != 50 {
		t.Errorf("topic limit = %d, want configured 50", got.Limit)
	}

	// The memory cap applies to topics too.
	if err := h.SaveTopic("big", MemFile{Entries: []string{strings.Repeat("b", 60)}}); !errors.Is(err, ErrOverLimit) {
		t.Errorf("topic cap not enforced: %v", err)
	}

	if err := h.DeleteTopic("home"); err != nil {
		t.Fatalf("delete topic: %v", err)
	}
	if err := h.DeleteTopic("home"); err == nil {
		t.Errorf("deleting a missing topic should error")
	}

	// Directory traversal must never escape topics/.
	for _, evil := range []string{"../evil", "..", "a/b", "", "."} {
		if err := h.SaveTopic(evil, MemFile{Entries: []string{"x"}}); err == nil {
			t.Errorf("SaveTopic(%q) must be rejected", evil)
		}
		if _, err := h.TopicPath(evil); err == nil {
			t.Errorf("TopicPath(%q) must be rejected", evil)
		}
	}
}

// TestHermesFilesIndex verifies the manifest index: every non-empty memory
// file (USER.md, MEMORY.md, topics/*) appears with an absolute path, counts
// and a summary hint; empty files stay out of the index.
func TestHermesFilesIndex(t *testing.T) {
	h := NewHermes(t.TempDir())
	if files, err := h.Files(); err != nil || len(files) != 0 {
		t.Fatalf("empty store files = %v, err %v", files, err)
	}
	if err := h.SaveUser(MemFile{Entries: []string{"user likes tea"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveMemory(MemFile{Entries: []string{"go builds fast", "second entry"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveTopic("infra", MemFile{Entries: []string{"kubernetes on orange pi"}}); err != nil {
		t.Fatal(err)
	}

	files, err := h.Files()
	if err != nil || len(files) != 3 {
		t.Fatalf("files = %+v, err %v", files, err)
	}
	want := map[string]struct{ entries int }{
		"USER.md":         {1},
		"MEMORY.md":       {2},
		"topics/infra.md": {1},
	}
	for _, f := range files {
		w, ok := want[f.Name]
		if !ok {
			t.Errorf("unexpected file %q", f.Name)
			continue
		}
		if f.Entries != w.entries {
			t.Errorf("%s entries = %d, want %d", f.Name, f.Entries, w.entries)
		}
		if !filepath.IsAbs(f.Path) {
			t.Errorf("%s path not absolute: %q", f.Name, f.Path)
		}
		if f.Summary == "" {
			t.Errorf("%s summary empty", f.Name)
		}
	}
}

// TestRenderManifest verifies the selective-loading prompt block: fenced as
// data, one line per file, read-on-demand instruction; empty index renders
// nothing.
func TestRenderManifest(t *testing.T) {
	if got := RenderManifest(nil); got != "" {
		t.Errorf("empty manifest should render \"\", got %q", got)
	}
	got := RenderManifest([]FileSummary{
		{Name: "USER.md", Path: "/m/USER.md", Entries: 1, Chars: 14, Summary: "user likes tea"},
	})
	for _, sub := range []string{"<memory_data>", "记忆文件清单", "/m/USER.md", "摘要：user likes tea", "自行读取"} {
		if !strings.Contains(got, sub) {
			t.Errorf("manifest missing %q:\n%s", sub, got)
		}
	}
}

// TestInjectorConversationUsesTopics verifies the built-in Q&A path keeps
// retrieval injection across the multi-file pool: a topic entry matching the
// query is injected while USER.md stays whole.
func TestInjectorConversationUsesTopics(t *testing.T) {
	h := NewHermes(t.TempDir())
	if err := h.SaveUser(MemFile{Entries: []string{"user likes tea"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveMemory(MemFile{Entries: []string{"unrelated weather note about rain"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveTopic("infra", MemFile{Entries: []string{"kubernetes cluster runs on orange pi"}}); err != nil {
		t.Fatal(err)
	}

	inj := NewInjector(h, nil)
	got, err := inj.Conversation("how is the orange pi kubernetes cluster wired")
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if !strings.Contains(got, "user likes tea") {
		t.Errorf("USER.md must stay fully injected:\n%s", got)
	}
	if !strings.Contains(got, "orange pi") {
		t.Errorf("matching topic entry must be retrieved:\n%s", got)
	}

	// Manifest mirrors the same files.
	files, err := inj.Manifest()
	if err != nil || len(files) != 3 {
		t.Fatalf("manifest = %+v, err %v", files, err)
	}
}

// TestSchedulerPruneWiring verifies A4: with a Daily attached, each tick
// enforces the retention windows at most once per day — independently of the
// idle/dream gates — and stamps .dreams/last-prune.
func TestSchedulerPruneWiring(t *testing.T) {
	hermes := NewHermes(t.TempDir())
	daily := NewDaily(hermes.WarmDir())
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	dreamer := NewDreamer(hermes)
	dreamer.now = func() time.Time { return now }

	sched := NewScheduler(dreamer, nil, func() bool { return false }, time.Hour).WithDaily(daily)

	writeOld := func() {
		old := now.AddDate(0, 0, -120) // >90d: delete window
		if err := daily.Append(old, "ancient log line that must be pruned"); err != nil {
			t.Fatalf("append old: %v", err)
		}
	}

	writeOld()
	if ran, err := sched.tick(now); err != nil || ran {
		t.Fatalf("tick: ran=%v err=%v (idle=false, so no dream, but no error)", ran, err)
	}
	if _, err := os.Stat(daily.PathFor(now.AddDate(0, 0, -120))); !os.IsNotExist(err) {
		t.Errorf("first tick must prune the >90d file, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(hermes.ColdDir(), "last-prune")); err != nil {
		t.Errorf("last-prune stamp missing: %v", err)
	}

	// Within 24h the prune is suppressed even though a stale file reappears.
	writeOld()
	if _, err := sched.tick(now.Add(time.Hour)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if _, err := os.Stat(daily.PathFor(now.AddDate(0, 0, -120))); err != nil {
		t.Errorf("tick within 24h must not prune again: %v", err)
	}

	// After the interval it runs again.
	if _, err := sched.tick(now.Add(25 * time.Hour)); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if _, err := os.Stat(daily.PathFor(now.AddDate(0, 0, -120))); !os.IsNotExist(err) {
		t.Errorf("tick after 24h must prune, stat err = %v", err)
	}
}

// TestDreamExternalDiscount verifies A4: a candidate carrying untrusted
// (external daily-log) provenance scores half of an otherwise identical
// trusted candidate — logs are low-weight evidence.
func TestDreamExternalDiscount(t *testing.T) {
	hermes := NewHermes(t.TempDir())
	dreamer := NewDreamer(hermes)
	dreamer.now = func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) }
	day := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	text := "a candidate used to measure the discount factor"

	mk := func(mixed bool) *Candidate {
		return &Candidate{
			Text:  text,
			Days:  []time.Time{day},
			Count: 1, // below minRecall: scored, then dropped before promotion
			Sources: []Source{
				{Path: "2026-08-12.md", Line: 1, Trusted: true},
				{Path: "2026-08-12.md", Line: 2, Trusted: !mixed},
			},
		}
	}
	trusted, mixed := mk(false), mk(true)
	if _, err := dreamer.deep([]*Candidate{trusted}); err != nil {
		t.Fatalf("deep trusted: %v", err)
	}
	if _, err := dreamer.deep([]*Candidate{mixed}); err != nil {
		t.Fatalf("deep mixed: %v", err)
	}
	if trusted.Score <= 0 {
		t.Fatalf("trusted score = %v", trusted.Score)
	}
	if got, want := mixed.Score, trusted.Score*dailyExternalDiscount; got != want {
		t.Errorf("mixed score = %v, want %v (0.5x trusted)", got, want)
	}
}

// TestDreamWhitelistPromotesRepeatedEmphasis verifies the A+B channel: a
// candidate tainted by one external source still promotes when the agent
// itself observed it on >=3 distinct days within the last 7 — annotated with
// the source tag and announced via OnPromotion.
func TestDreamWhitelistPromotesRepeatedEmphasis(t *testing.T) {
	hermes := NewHermes(t.TempDir())
	dreamer := NewDreamer(hermes)
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	dreamer.now = func() time.Time { return now }

	text := "user always commits before lunch"
	c := &Candidate{
		Text:  text,
		Days:  []time.Time{time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)},
		Count: 4,
		Sources: []Source{
			{Path: "2026-08-10.md", Line: 1, Trusted: true},
			{Path: "2026-08-11.md", Line: 1, Trusted: true},
			{Path: "2026-08-12.md", Line: 1, Trusted: true},
			{Path: "2026-08-12.md", Line: 2, Trusted: false}, // one external sighting taints the candidate
		},
	}

	var hookEntry string
	var hookWhitelist bool
	dreamer.OnPromotion = func(entry string, viaWhitelist bool) {
		hookEntry, hookWhitelist = entry, viaWhitelist
	}

	promoted, err := dreamer.deep([]*Candidate{c})
	if err != nil {
		t.Fatalf("deep: %v", err)
	}
	want := text + " " + PromotionSourceTag
	if len(promoted) != 1 || promoted[0] != want {
		t.Fatalf("promoted = %v, want [%s]", promoted, want)
	}
	mem, _ := hermes.LoadMemory()
	if len(mem.Entries) != 1 || mem.Entries[0] != want {
		t.Errorf("MEMORY.md = %v, want [%s]", mem.Entries, want)
	}
	if hookEntry != want || !hookWhitelist {
		t.Errorf("OnPromotion = (%q, %v), want (%q, true)", hookEntry, hookWhitelist, want)
	}
}

// TestDreamWhitelistStaysClosed verifies the whitelist never reopens the
// injection channel: stale trusted sightings (outside the 7-day window) and
// external-only repetition both stay blocked.
func TestDreamWhitelistStaysClosed(t *testing.T) {
	hermes := NewHermes(t.TempDir())
	dreamer := NewDreamer(hermes)
	dreamer.now = func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) }

	// Trusted sightings are stale (>7d) and the recent one is external-only:
	// the repeated emphasis must come from the agent itself, recently.
	stale := &Candidate{
		Text:  "stale trusted sightings with one fresh external line",
		Days:  []time.Time{time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)},
		Count: 4,
		Sources: []Source{
			{Path: "2026-07-01.md", Line: 1, Trusted: true},
			{Path: "2026-07-02.md", Line: 1, Trusted: true},
			{Path: "2026-08-12.md", Line: 1, Trusted: false},
		},
	}
	// External-only repetition — exactly the injection pattern.
	extOnly := &Candidate{
		Text:  "external text repeating itself across many days",
		Days:  []time.Time{time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)},
		Count: 3,
		Sources: []Source{
			{Path: "2026-08-10.md", Line: 1, Trusted: false},
			{Path: "2026-08-11.md", Line: 1, Trusted: false},
			{Path: "2026-08-12.md", Line: 1, Trusted: false},
		},
	}

	promoted, err := dreamer.deep([]*Candidate{stale, extOnly})
	if err != nil {
		t.Fatalf("deep: %v", err)
	}
	if len(promoted) != 0 {
		t.Fatalf("whitelist must stay closed for stale/external-only candidates, got %v", promoted)
	}
}
