// SPDX-License-Identifier: AGPL-3.0-or-later

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

func TestRankRareTokenOutranksCommon(t *testing.T) {
	// IDF: a token shared by every entry carries almost no signal. All entries
	// mention "服务器", but only one mentions "openwrt" — a query hitting both
	// must rank the rare-term entry first even though every entry overlaps.
	entries := []string{
		"服务器 机架 位置",
		"服务器 电源 冗余",
		"服务器 openwrt 固件",
	}
	got := Retriever{}.Rank("服务器 openwrt", entries, 3)
	if len(got) == 0 || got[0] != "服务器 openwrt 固件" {
		t.Fatalf("rank = %v, want the rare openwrt entry first", got)
	}
}

func TestRankLengthNormalization(t *testing.T) {
	// BM25 b: a short entry that matches the query beats a padded long entry
	// with the same match count — length alone must not buy rank.
	entries := []string{
		"部署 发布 回滚 监控 告警 日志 追踪 指标 debian",
		"debian",
	}
	got := Retriever{}.Rank("debian", entries, 2)
	if len(got) == 0 || got[0] != "debian" {
		t.Fatalf("rank = %v, want the focused short entry first", got)
	}
}

func TestRankTopKBoundary(t *testing.T) {
	// Six relevant entries, k=5: exactly the top five must come back, so the
	// boundary does not over- or under-return.
	entries := []string{
		"构建缓存清理",
		"构建发布流程",
		"构建失败排查",
		"构建日志位置",
		"构建脚本维护",
		"构建依赖下载",
	}
	got := Retriever{}.Rank("构建", entries, 5)
	if len(got) != 5 {
		t.Fatalf("rank returned %d entries, want exactly 5 (top-k boundary)", len(got))
	}
}

func TestRankZeroOrNegativeK(t *testing.T) {
	entries := []string{"构建", "发布"}
	r := Retriever{}
	if got := r.Rank("构建", entries, 0); got != nil {
		t.Fatalf("k=0: got %v, want nil", got)
	}
	if got := r.Rank("构建", entries, -1); got != nil {
		t.Fatalf("k<0: got %v, want nil", got)
	}
}

func TestRankCJKSingleIdeographOverlap(t *testing.T) {
	// A single shared content ideograph is a real (weak) relevance signal; a lone
	// CJK function word must not match anything.
	entries := []string{"构建缓存", "部署发布"}
	r := Retriever{}
	if got := r.Rank("构建", entries, 2); len(got) == 0 || got[0] != "构建缓存" {
		t.Fatalf("single-ideograph overlap = %v, want the 构建 entry", got)
	}
	if got := r.Rank("是", entries, 2); len(got) != 0 {
		t.Fatalf("function-word-only query matched: %v", got)
	}
}

func TestRankDoesNotMatchUnrelatedChineseCharacters(t *testing.T) {
	entries := []string{"广场的空气质量良好", "广州的天气晴朗"}
	got := Retriever{}.Rank("广州天气", entries, 2)
	if len(got) != 1 || got[0] != entries[1] {
		t.Fatalf("single-character coincidences must not count: %v", got)
	}
}

func TestRankShortChineseQueryAndMixedScripts(t *testing.T) {
	entries := []string{"每天喝绿茶", "Debian 12 是服务器系统"}
	if got := (Retriever{}).Rank("茶", entries, 2); len(got) != 1 || got[0] != entries[0] {
		t.Fatalf("single-character query no longer matches: %v", got)
	}
	if got := (Retriever{}).Rank("Debian 系统", entries, 2); len(got) != 1 || got[0] != entries[1] {
		t.Fatalf("mixed-script query misses memory: %v", got)
	}
}
