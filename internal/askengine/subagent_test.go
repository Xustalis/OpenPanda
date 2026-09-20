package askengine

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

func TestSubagentMultiLanguage(t *testing.T) {
	spec := &entry.TaskSpec{
		Title: "Analyze Codebase",
		Requires: entry.Requires{
			Abilities: []string{"agent:codex", "terminal:exec"},
		},
		Spec: entry.TaskSpecDetail{
			Node:   "gpu-worker-1",
			Target: "Find memory leaks",
		},
	}

	// English dispatch note
	enDispatch := taskDispatchNote(spec, i18n.English)
	if !strings.Contains(enDispatch, "Dispatched subagent task: Analyze Codebase") {
		t.Errorf("English dispatch missing header: %s", enDispatch)
	}
	if !strings.Contains(enDispatch, "Device node: gpu-worker-1") {
		t.Errorf("English dispatch missing device node: %s", enDispatch)
	}
	if !strings.Contains(enDispatch, "Harness: codex") {
		t.Errorf("English dispatch missing harness: %s", enDispatch)
	}
	if !strings.Contains(enDispatch, "Required abilities: agent:codex, terminal:exec") {
		t.Errorf("English dispatch missing abilities: %s", enDispatch)
	}
	if !strings.Contains(enDispatch, "Target: Find memory leaks") {
		t.Errorf("English dispatch missing target: %s", enDispatch)
	}
	if !strings.Contains(enDispatch, "Assigned device: gpu-worker-1") {
		t.Errorf("English dispatch missing assigned node: %s", enDispatch)
	}

	// Chinese dispatch note
	zhDispatch := taskDispatchNote(spec, i18n.ChineseSimp)
	if !strings.Contains(zhDispatch, "已派发子代理任务：Analyze Codebase") {
		t.Errorf("Chinese dispatch missing header: %s", zhDispatch)
	}
	if !strings.Contains(zhDispatch, "设备节点: gpu-worker-1") {
		t.Errorf("Chinese dispatch missing device node: %s", zhDispatch)
	}
	if !strings.Contains(zhDispatch, "需要能力：agent:codex、terminal:exec") {
		t.Errorf("Chinese dispatch missing abilities: %s", zhDispatch)
	}
	if !strings.Contains(zhDispatch, "目标：Find memory leaks") {
		t.Errorf("Chinese dispatch missing target: %s", zhDispatch)
	}

	// Budget notes
	enBudget := taskBudgetNote(3, i18n.English)
	if !strings.Contains(enBudget, "subagent task budget for this conversation round (3)") {
		t.Errorf("English budget note unexpected: %s", enBudget)
	}
	zhBudget := taskBudgetNote(3, i18n.ChineseSimp)
	if !strings.Contains(zhBudget, "本轮对话的子代理任务预算（3 个）已用完") {
		t.Errorf("Chinese budget note unexpected: %s", zhBudget)
	}

	// Subagent observation (success)
	resSuccess := &Result{
		OK:        true,
		TaskID:    "task-456",
		TaskTitle: "Analyze Codebase",
		TaskState: "done",
		Agent:     "codex",
		Model:     "claude-3-5",
		Injected:  true,
		ExitCode:  0,
		Stdout:    "Analysis complete: no leaks detected.",
	}
	enObs := taskObservation(resSuccess, i18n.English)
	if !strings.Contains(enObs, "[Subagent Result] Analyze Codebase (task-456)\nStatus: done") {
		t.Errorf("English observation missing header: %s", enObs)
	}
	if !strings.Contains(enObs, "Agent/Harness: codex") {
		t.Errorf("English observation missing agent: %s", enObs)
	}
	if !strings.Contains(enObs, "Model: claude-3-5") {
		t.Errorf("English observation missing model: %s", enObs)
	}
	if !strings.Contains(enObs, "(System model injected)") {
		t.Errorf("English observation missing injected note: %s", enObs)
	}
	if !strings.Contains(enObs, "Output Excerpt:") {
		t.Errorf("English observation missing output excerpt header: %s", enObs)
	}
	if !strings.Contains(enObs, "[Core Instruction]") {
		t.Errorf("English observation missing core instruction: %s", enObs)
	}

	zhObs := taskObservation(resSuccess, i18n.ChineseSimp)
	if !strings.Contains(zhObs, "[子代理任务结果] Analyze Codebase (task-456)\n状态：done") {
		t.Errorf("Chinese observation missing header: %s", zhObs)
	}
	if !strings.Contains(zhObs, "执行智能体/Harness：codex") {
		t.Errorf("Chinese observation missing agent: %s", zhObs)
	}
	if !strings.Contains(zhObs, "【核心指示】") {
		t.Errorf("Chinese observation missing core instruction: %s", zhObs)
	}

	// Excerpt truncation
	longText := strings.Repeat("A", 10000)
	enExcerpt := excerpt(longText, 6000, i18n.English)
	if !strings.Contains(enExcerpt, "Intermediate output omitted from prompt") {
		t.Errorf("English excerpt missing omission notice: %s", enExcerpt)
	}
	zhExcerpt := excerpt(longText, 6000, i18n.ChineseSimp)
	if !strings.Contains(zhExcerpt, "中间输出在提示词中略去") {
		t.Errorf("Chinese excerpt missing omission notice: %s", zhExcerpt)
	}
}
