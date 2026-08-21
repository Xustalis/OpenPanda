package mdtext

import (
	"strings"
	"testing"
)

func TestPlainStripsEmphasisAndLinks(t *testing.T) {
	in := "最大文件是 **large.bin**（`512 KB`），详见 [报告](http://x/y)"
	want := "最大文件是 large.bin（512 KB），详见 报告"
	if got := Plain(in); got != want {
		t.Errorf("Plain emphasis = %q, want %q", got, want)
	}
}

func TestPlainHeadingAndList(t *testing.T) {
	in := "## 结论\n- 第一点\n- 第二点"
	want := "结论\n• 第一点\n• 第二点"
	if got := Plain(in); got != want {
		t.Errorf("Plain heading/list = %q, want %q", got, want)
	}
}

func TestPlainTableFlattens(t *testing.T) {
	in := "| 文件 | 大小 |\n|---|---|\n| **large.bin** | `512 KB` |"
	got := Plain(in)
	if strings.Contains(got, "|") || strings.Contains(got, "---") {
		t.Errorf("Plain table kept layout syntax: %q", got)
	}
	if !strings.Contains(got, "large.bin, 512 KB") {
		t.Errorf("Plain table cells = %q", got)
	}
	if strings.Contains(got, "**") || strings.Contains(got, "`") {
		t.Errorf("Plain table kept inline markers: %q", got)
	}
}

func TestPlainKeepsFenceContent(t *testing.T) {
	in := "结果：\n```bash\ndu -ah . | sort -rh\n```\n完成"
	want := "结果：\ndu -ah . | sort -rh\n完成"
	if got := Plain(in); got != want {
		t.Errorf("Plain fence = %q, want %q", got, want)
	}
}

func TestPlainRule(t *testing.T) {
	if got := Plain("上文\n---\n下文"); !strings.Contains(got, "—") {
		t.Errorf("Plain rule = %q", got)
	}
}

func TestANSIHeadingBold(t *testing.T) {
	got := ANSI("# 标题\n正文 **重点** 继续")
	if !strings.Contains(got, "\x1b[1;36m") {
		t.Errorf("ANSI heading missing cyan-bold: %q", got)
	}
	if !strings.Contains(got, "\x1b[1m重点\x1b[22m") {
		t.Errorf("ANSI bold missing: %q", got)
	}
	if !strings.Contains(got, "正文") || !strings.Contains(got, "继续") {
		t.Errorf("ANSI dropped prose: %q", got)
	}
}

func TestANSICodeFenceDim(t *testing.T) {
	got := ANSI("```\ncat x\n```")
	if !strings.Contains(got, "\x1b[2mcat x\x1b[0m") {
		t.Errorf("ANSI fence not dim: %q", got)
	}
	if strings.Contains(got, "```") {
		t.Errorf("ANSI kept fence marker: %q", got)
	}
}

func TestANSITableNoBarePipes(t *testing.T) {
	in := "| a | b |\n|---|---|\n| 1 | 2 |"
	got := ANSI(in)
	if strings.Contains(got, "---") {
		t.Errorf("ANSI kept separator row: %q", got)
	}
	if !strings.Contains(got, "a") || !strings.Contains(got, "2") {
		t.Errorf("ANSI table lost cells: %q", got)
	}
}

func TestPlainLeavesPlainTextAlone(t *testing.T) {
	in := "温度 37.2 摄氏度，正常范围。"
	if got := Plain(in); got != in {
		t.Errorf("Plain modified plain text: %q", got)
	}
}
