//go:build !lite

package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

func TestSelectionListNavigation(t *testing.T) {
	items := []SelectionItem{
		{Index: 1, Title: "Item 1", ID: "id-1"},
		{Index: 2, Title: "Item 2", ID: "id-2"},
		{Index: 3, Title: "Item 3", ID: "id-3"},
	}
	sl := NewSelectionList("Test List", items)

	if sel, ok := sl.Selected(); !ok || sel.ID != "id-1" {
		t.Fatalf("expected id-1, got %+v", sel)
	}

	sl.MoveDown(10)
	if sel, ok := sl.Selected(); !ok || sel.ID != "id-2" {
		t.Fatalf("expected id-2 after move down, got %+v", sel)
	}

	sl.MoveDown(10)
	if sel, ok := sl.Selected(); !ok || sel.ID != "id-3" {
		t.Fatalf("expected id-3 after move down, got %+v", sel)
	}

	// Should not exceed bounds
	sl.MoveDown(10)
	if sel, ok := sl.Selected(); !ok || sel.ID != "id-3" {
		t.Fatalf("expected id-3 at bottom bound, got %+v", sel)
	}

	sl.MoveUp()
	if sel, ok := sl.Selected(); !ok || sel.ID != "id-2" {
		t.Fatalf("expected id-2 after move up, got %+v", sel)
	}

	sl.MoveUp()
	sl.MoveUp() // past top
	if sel, ok := sl.Selected(); !ok || sel.ID != "id-1" {
		t.Fatalf("expected id-1 at top bound, got %+v", sel)
	}
}

func TestSelectionListEmpty(t *testing.T) {
	sl := NewSelectionList("Empty", nil)
	sl.EmptyText = "暂无数据引导说明"
	th := newTheme("en")
	rendered := sl.Render(th, 80, 24)

	if !strings.Contains(rendered, "暂无数据引导说明") {
		t.Fatalf("expected empty guide in output: %s", rendered)
	}
	if _, ok := sl.Selected(); ok {
		t.Fatal("empty list should not have selection")
	}
}

func TestSelectionListBoxed(t *testing.T) {
	items := []SelectionItem{
		{Title: "deepseek-v4-flash", Badge: "[当前]"},
		{Title: "gpt-4o"},
	}
	sl := NewSelectionList("模型管理", items)
	sl.Boxed = true
	sl.ActionHints = "[A] 添加  [D] 删除  [E] 编辑"
	sl.FooterHints = "↑↓ 选择 · Enter 切换 · Esc 返回"

	th := newTheme("zh")
	rendered := sl.Render(th, 80, 24)

	if !strings.Contains(rendered, "模型管理") {
		t.Fatalf("expected title in boxed render: %s", rendered)
	}
	if !strings.Contains(rendered, "deepseek-v4-flash") {
		t.Fatalf("expected model in boxed render: %s", rendered)
	}
	if !strings.Contains(rendered, "[当前]") {
		t.Fatalf("expected current badge in boxed render: %s", rendered)
	}
	if !strings.Contains(rendered, "[A] 添加") {
		t.Fatalf("expected action hints in boxed render: %s", rendered)
	}
	if !strings.Contains(rendered, "Enter 切换") {
		t.Fatalf("expected footer hints in boxed render: %s", rendered)
	}
}

// TestSelectionListMovePage pins the PgUp/PgDn behaviour: a page jump moves the
// cursor by the viewport budget, clamps at both ends, and keeps the Top window
// glued to the cursor so the highlighted row is always rendered.
func TestSelectionListMovePage(t *testing.T) {
	var items []SelectionItem
	for i := 1; i <= 30; i++ {
		items = append(items, SelectionItem{Index: i, Title: fmt.Sprintf("Item %d", i), ID: fmt.Sprintf("id-%d", i)})
	}
	sl := NewSelectionList("Paged", items)

	sl.MovePage(10, 10)
	if sel, ok := sl.Selected(); !ok || sel.ID != "id-11" {
		t.Fatalf("PgDn should land on id-11, got %+v", sel)
	}
	if sl.Top != 1 { // same window math as MoveDown: Top = Cursor - visibleRows + 1
		t.Fatalf("window should follow the cursor, Top=%d", sl.Top)
	}

	sl.MovePage(10, 10)
	if sel, ok := sl.Selected(); !ok || sel.ID != "id-21" {
		t.Fatalf("second PgDn should land on id-21, got %+v", sel)
	}

	// Past the bottom: clamps to the last item, window includes it.
	sl.MovePage(10, 10)
	if sel, ok := sl.Selected(); !ok || sel.ID != "id-30" {
		t.Fatalf("PgDn past bottom should clamp to id-30, got %+v", sel)
	}
	if sl.Top+10 > 30 || sl.Cursor < sl.Top {
		t.Fatalf("window out of range after clamp: top=%d cursor=%d", sl.Top, sl.Cursor)
	}

	sl.MovePage(-10, 10)
	if sel, ok := sl.Selected(); !ok || sel.ID != "id-20" {
		t.Fatalf("PgUp should land on id-20, got %+v", sel)
	}

	sl.MovePage(-100, 10)
	if sel, ok := sl.Selected(); !ok || sel.ID != "id-1" {
		t.Fatalf("PgUp past top should clamp to id-1, got %+v", sel)
	}
	if sl.Top != 0 {
		t.Fatalf("window should return to top, Top=%d", sl.Top)
	}
}

// TestSelectionListChromeIsLocalized pins the i18n fix for the list chrome. The
// footer legend and the empty-state line used to be hardcoded Chinese — once in
// the constructor and again as a fallback inside each renderer — so an English
// or Japanese list showed Chinese whenever a caller did not override them. Both
// renderers now resolve them through the theme's locale.
func TestSelectionListChromeIsLocalized(t *testing.T) {
	for _, loc := range i18n.Locales {
		th := newTheme(loc)

		plain := NewSelectionList("title", nil)
		rendered := plain.renderPlain(th, 80, 24)
		if want := i18n.T(loc, "tui.list.empty"); !strings.Contains(rendered, want) {
			t.Errorf("%s: empty list must carry the localized line, got %q", loc, rendered)
		}
		if want := i18n.T(loc, "tui.list.footerHints"); !strings.Contains(rendered, want) {
			t.Errorf("%s: plain list must carry the localized footer, got %q", loc, rendered)
		}

		boxed := NewSelectionList("title", nil)
		boxed.Boxed = true
		renderedBox := boxed.renderBoxed(th, 80, 24)
		if want := i18n.T(loc, "tui.model.footerHints"); !strings.Contains(renderedBox, want) {
			t.Errorf("%s: boxed list must carry the localized footer, got %q", loc, renderedBox)
		}
	}
}
