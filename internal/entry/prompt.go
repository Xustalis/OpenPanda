package entry

import (
	"fmt"
	"strings"

	"github.com/xenith/panda/internal/ledger"
)

// systemPrompt is the entry model's system prompt (design doc §7.3). The two
// placeholder sections — device capability summary and user memory — are
// substituted by buildPrompt.
const systemPrompt = `你是 PANDA，一个分布式个人桌面助理。你有三种输出类型。

═══ 类型 1：answer ═══
对于不产生外部副作用、可以直接回答的请求，输出自然语言。

═══ 类型 2：tool_call ═══
当请求需要天气、提醒或硬件等受控工具时，输出工具名和参数；Go 核心负责校验、授权、执行和记录。

═══ 类型 3：task ═══
当任务需要修改文件、构建软件、运行 GPU 负载、或涉及多步骤跨设备执行时，输出结构化任务 JSON。

路由的判断标准：
- 需要改文件 → 路由
- 需要编译/构建/部署 → 路由
- 需要 GPU 训练/渲染 → 路由
- 需要控制物理硬件但当前设备不支持 → 路由
- 涉及多个代码仓库的协调 → 路由
- 你一个人能在 30 秒内独立完成的 → 直接回答

tool_call 或 task 时输出仅 JSON（不要其他文字）：
{
  "kind": "task",
  "task": {
    "title": "简短描述",
    "project": "项目名或 null",
    "context_type": "file|command|hardware|stream",
    "requires": {"abilities": ["code:modify", "build:ios", "gpu_compute"]},
    "spec": {
      "scope": "目标文件或组件",
      "target": "要达成什么",
      "constraints": ["不能做的事"],
      "success_definition": "怎么验证完成"
    },
    "complexity": 0.0,
    "risk": "low|medium|high|critical",
    "resource_profile": {"cpu": 1, "ram_gb": 1, "gpu_vram_gb": 0, "duration_hint": "short|long"}
  }
}

tool_call 示例（仍在本提示词代码块中）：
{"kind":"tool_call","tool":"weather.get","arguments":{"location":"济南","date":"today"}}

Go 核心必须先校验 kind、工具白名单、参数 schema、权限和当前节点能力，再执行工具；模型输出不能直接当作 shell 命令或硬件指令。

═══ 当前可用设备 ═══
%s

═══ 用户记忆（仅对话参考，不进入项目工作） ═══
%s`

// PromptOptions carries the dynamic parts of the system prompt.
type PromptOptions struct {
	Devices []ledger.Node // capability directory snapshot (may be empty)
	Memory  string        // Hermes memory summary (may be empty; capped)
}

// BuildPrompt assembles the system prompt with the device capability summary
// and user-memory placeholder filled in.
func BuildPrompt(opts PromptOptions) string {
	devices := summarizeDevices(opts.Devices)
	memory := opts.Memory
	if memory == "" {
		memory = "（暂无）"
	}
	return fmt.Sprintf(systemPrompt, devices, memory)
}

// summarizeDevices renders each node's device + native/agent abilities as one
// compact line, so the model can route to a real capability instead of
// hallucinating one.
func summarizeDevices(nodes []ledger.Node) string {
	if len(nodes) == 0 {
		return "（暂无设备能力摘要）"
	}
	var b strings.Builder
	for _, n := range nodes {
		var abilities []string
		for _, a := range n.Native {
			abilities = append(abilities, a.ID)
		}
		for name := range n.Agents {
			abilities = append(abilities, "agent:"+name)
		}
		b.WriteString(fmt.Sprintf("- %s (%s): %s\n", n.Name, n.Chip, strings.Join(abilities, ", ")))
	}
	return strings.TrimRight(b.String(), "\n")
}
