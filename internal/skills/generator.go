package skills

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Record is one task execution observed for a task class.
type Record struct {
	Project string
	Title   string
	Success bool
}

// ClassKey returns a stable key for a task class from its required abilities:
// the sorted abilities joined with "+". Tasks needing the same abilities are
// one class, so the MUSE quality gate (>=3 runs, >=70% success) aggregates
// across them.
func ClassKey(abilities []string) string {
	sorted := append([]string(nil), abilities...)
	sort.Strings(sorted)
	return strings.Join(sorted, "+")
}

// Generate builds a pending skill from a task class's execution history using a
// deterministic template, so generation runs without an LLM. The body records
// what the class does and its observed success; the LLM distillation
// (design §8.2) can enrich it later. The result is StatusPending — it is not
// loaded until the user approves it.
func Generate(scope Scope, key, class, description string, records []Record) *Skill {
	var success, total int
	for _, r := range records {
		total++
		if r.Success {
			success++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "本技能由执行历史自动生成，用于重复性任务。\n\n## 用途\n%s\n\n", description)
	fmt.Fprintf(&b, "## 成功率\n%d 次成功 / 共 %d 次执行\n\n", success, total)
	b.WriteString("## 最近执行\n")
	for i := len(records) - 1; i >= 0 && i >= len(records)-5; i-- {
		status := "成功"
		if !records[i].Success {
			status = "失败"
		}
		fmt.Fprintf(&b, "- [%s] %s\n", status, records[i].Title)
	}

	sk := &Skill{
		Name:        slug(class),
		Description: description,
		Scope:       scope,
		Status:      StatusPending,
		Body:        b.String(),
	}
	if scope == ScopeProject {
		sk.Project = key
	} else if scope == ScopeDevice {
		sk.Device = key
	}
	return sk
}

// slug turns a class key (e.g. "lint+build:macos") into a valid skill name
// (e.g. "lint-build-macos") by replacing non-alphanumerics with "-".
func slug(class string) string {
	var b strings.Builder
	for _, r := range class {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
