package memory

import (
	"strings"
	"testing"
)

func TestRankRelevantFirst(t *testing.T) {
	entries := []string{
		"用户偏好暗色主题",
		"这台服务器是 Debian 12",
		"别用 sudo，用户在 docker 组",
	}
	got := Retriever{}.Rank("暗色主题", entries, 2)
	if len(got) == 0 || got[0] != "用户偏好暗色主题" {
		t.Fatalf("rank = %v, want dark-theme entry first", got)
	}
	if len(got) > 2 {
		t.Fatalf("rank = %v, want at most 2", got)
	}
}

func TestRankDropsIrrelevant(t *testing.T) {
	entries := []string{"完全无关的条目"}
	got := Retriever{}.Rank("暗色主题", entries, 5)
	if len(got) != 0 {
		t.Fatalf("rank = %v, want empty (no token overlap)", got)
	}
}

func TestRankEmptyQueryKeepsOrder(t *testing.T) {
	entries := []string{"a", "b", "c"}
	got := Retriever{}.Rank("", entries, 2)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("rank = %v, want first 2 in order", got)
	}
}

func TestRankSkipsFunctionWords(t *testing.T) {
	// "是"/"这" are function words and must not decide the ranking; only content
	// overlap counts.
	entries := []string{
		"构建走 make release",
		"这台服务器是 Debian 12",
	}
	got := Retriever{}.Rank("服务器是 Debian", entries, 2)
	if len(got) == 0 || got[0] != "这台服务器是 Debian 12" {
		t.Fatalf("rank = %v, want the Debian entry first", got)
	}
}

func TestRankIgnoresStopwords(t *testing.T) {
	entries := []string{"deploy the app to production"}
	// "the"/"to" are Latin stopwords; a query of only stopwords must not match.
	got := Retriever{}.Rank("the to", entries, 5)
	if len(got) != 0 {
		t.Fatalf("rank = %v, want empty (stopword-only query)", got)
	}
	// A content token still matches once present.
	got = Retriever{}.Rank("deploy the app", entries, 5)
	if len(got) != 1 || got[0] != entries[0] {
		t.Fatalf("rank = %v, want the deploy entry", got)
	}
}

func TestConversationRetrievesByQuery(t *testing.T) {
	root := t.TempDir()
	h := NewHermes(root)
	if err := h.SaveUser(MemFile{Entries: []string{"用户偏好暗色主题"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveMemory(MemFile{Entries: []string{
		"这台服务器是 Debian 12",
		"构建走 make release",
	}}); err != nil {
		t.Fatal(err)
	}
	inj := NewInjector(h, nil)

	got, err := inj.Conversation("服务器是什么系统")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Debian 12") {
		t.Errorf("conversation should surface the Debian fact, got %q", got)
	}
	if strings.Contains(got, "make release") {
		t.Errorf("conversation should drop the irrelevant build note, got %q", got)
	}
	if !strings.Contains(got, "暗色主题") {
		t.Errorf("conversation should always include the user profile, got %q", got)
	}
}
