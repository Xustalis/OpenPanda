package main

import (
	"strings"
	"testing"
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
