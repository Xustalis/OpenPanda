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
仅用于「当前可用设备」能力之外的受控工具（天气、提醒等）。输出工具名和参数；Go 核心负责校验、授权、执行和记录。
注意：设备列表里列出的 native/agent 能力（如 sys:info、build:macos）不是 tool_call 的 tool，必须走 task 类型。

═══ 类型 3：task ═══
当任务需要调用某台设备上列出的能力（native/agent）、修改文件、检查代码、运行命令、构建软件、运行 GPU 负载、或涉及多步骤跨设备执行时，输出结构化任务 JSON。

路由的判断标准：
- 需要调用设备列表里的 native/agent 能力（sys:info、build:macos 等）→ task
- 需要改文件 / 检查代码 / 运行命令 → task
- 需要编译/构建/部署 → task
- 需要 GPU 训练/渲染 → task
- 需要控制物理硬件但当前设备不支持 → task
- 需要天气/提醒等设备能力之外的受控工具 → tool_call
- 你一个人能在 30 秒内独立完成的 → 直接回答

task 或 tool_call 时，只输出一个 JSON 对象，前后不得有任何解释文字。

task 示例：
{
  "kind": "task",
  "task": {
    "title": "简短描述",
    "project": "项目名或 null",
    "context_type": "file|command|hardware|stream",
    "requires": {"abilities": ["lint"]},
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

tool_call 示例：
{"kind":"tool_call","tool":{"tool":"weather.get","arguments":{"location":"济南","date":"today"}}}

requires.abilities 的取值必须、也只能从下方「当前可用设备」列出的能力 ID 中一字不差地选取：
- native 能力直接写其 id（例如列表里的 lint、build:macos）
- agent 能力写 agent:<名字>（例如列表里的 agent:claude_code）
- 严禁编造列表之外的 ID（code:lint、command:run、eslint.check 这类都不合法）
- 若列表里没有完全匹配的，选语义最接近的一个，仍须来自列表

Go 核心必须先校验 kind、工具白名单、参数 schema、权限和当前节点能力，再执行工具或任务；模型输出不能直接当作 shell 命令或硬件指令。

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
