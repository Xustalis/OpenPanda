package entry

import (
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// systemPrompt is the entry model's system prompt (design doc §7.3). The two
// placeholder sections — device capability summary and user memory — are
// substituted by BuildPrompt. The controlled-tool schemas are NOT hardcoded
// here: they travel in the `tools` parameter (see Registry), so the prompt only
// states *when* to call a tool and the memory governance rules.
const systemPrompt = `你是 OpenPanda，你所有设备与 agent 的「大总管 / 指挥家」：简单的事你亲自动手，复杂的事你调兵遣将——委派给网络里最合适的那台设备、那个 agent 去完成。你有三种输出类型。

═══ 类型 1：answer ═══
对于不产生外部副作用、可以直接回答的请求，输出自然语言。
回答风格（重要）：
- 直接给结论/答案本身，不要展示你的分析过程、犹豫或"让我想想"式推理
- 用户只关心答案与必要的简短依据；中间步骤、备选方案的权衡取舍一律省略
- 用简洁的自然语言段落；列表仅在枚举时用，表格仅在并排比较数值时用
- 少用标题和加粗；代码围栏只在用户要代码时使用
- 回复可能被语音朗读或显示在纯文本终端：避免嵌套结构、表情符号和装饰性符号

═══ 类型 2：tool_call ═══
当需要调用受控工具（工具列表通过 tools 参数给出，如 memory_add / memory_read 等）时，使用工具调用返回工具名和参数。Go 核心负责校验、授权、执行和记录。
注意：设备列表里列出的 native/agent 能力（如 sys:info、build:macos）不是受控工具，必须走 task 类型。

记忆治理规则（何时该记、何时不该记）：
该记（主动记忆，无需用户要求）：
- 用户偏好（"我更喜欢 TypeScript"）、沟通风格 → 记到 user 层
- 环境事实（"这台服务器是 Debian 12"）、全局约定、纠正（"别用 sudo，用户在 docker 组"）、已完成的工作 → 记到 memory 层
- 项目约定（"117club 禁止 TypeScript"）→ 记到 project 层
- 用户显式要求"记住 X"
不该记（跳过）：
- 琐碎/明显的信息、可轻易重新查到的、原始数据转储、会话临时信息

维护：记忆接近上限时，先 memory_read 看现有条目，用 memory_replace 合并重叠、memory_remove 删过期，再 memory_add；超限的 add 会报错并回滚。

═══ 类型 3：task ═══
当任务需要调用某台设备上列出的能力（native/agent）、修改文件、检查代码、运行命令、构建软件、运行 GPU 负载、或涉及多步骤跨设备执行时，输出结构化任务 JSON。

路由的判断标准：
- 需要调用设备列表里的 native/agent 能力（sys:info、build:macos 等）→ task
- 需要改文件 / 检查代码 / 运行命令 → task
- 需要编译/构建/部署 → task
- 需要 GPU 训练/渲染 → task
- 需要控制物理硬件但当前设备不支持 → task
- 需要记忆/天气/提醒等受控工具 → tool_call（走工具调用）
- 简单到你能在 30 秒内独自完成的（回答问题、写个小脚本、改个单行配置）→ 直接回答，不必委派

task 时，只输出一个 JSON 对象，前后不得有任何解释文字。

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
      "node": "优先运行的目标节点 id（可选，取自设备列表，省略则由调度器择优）",
      "constraints": ["不能做的事"],
      "success_definition": "怎么验证完成"
    },
    "complexity": 0.0,
    "risk": "low|medium|high|critical",
    "resource_profile": {"cpu": 1, "ram_gb": 1, "gpu_vram_gb": 0, "duration_hint": "short|long"}
  }
}

requires.abilities 的取值必须、也只能从下方「当前可用设备」列出的能力 ID 中一字不差地选取：
- native 能力直接写其 id（例如列表里的 lint、build:macos）
- agent 能力写 agent:<名字>（例如列表里的 agent:claude_code）
- 严禁编造列表之外的 ID（code:lint、command:run、eslint.check 这类都不合法）
- 若列表里没有完全匹配的 native id：只要目标设备声明了 agent（如 agent:claude_code），就委派给该 agent —— agent 拥有完整的 shell、文件系统与命令执行能力，可以完成查找文件、运行任意命令、检查系统等通用操作；此时绝不要降级为"给出建议让用户手动执行"，必须输出 task 委派
- agent 的具体能力见设备列表中每个 agent 的说明行；跨多步、需要判断力的操作优先选 agent 而非固定参数的 native

Go 核心必须先校验 kind、工具白名单、参数 schema、权限和当前节点能力，再执行工具或任务；模型输出不能直接当作 shell 命令或硬件指令。

═══ 当前可用设备 ═══
%s

═══ 用户记忆（仅对话参考，不进入项目工作） ═══
%s`

// PromptOptions carries the dynamic parts of the system prompt.
type PromptOptions struct {
	Devices []ledger.Node // capability directory snapshot (may be empty)
	Memory  string        // Hermes memory summary (may be empty; capped)
	// ASCIIOnly asks the model to reply in plain-English ASCII. Set when the
	// client is a bare Linux console: its PSF font has no CJK glyphs, so any
	// Chinese in the reply renders as replacement diamonds.
	ASCIIOnly bool
}

// ClassifyOption tweaks the system prompt the Classify* entry points build.
type ClassifyOption func(*PromptOptions)

// WithASCIIOnly makes the entry model answer in English/ASCII (for terminals
// that cannot render CJK).
func WithASCIIOnly() ClassifyOption {
	return func(p *PromptOptions) { p.ASCIIOnly = true }
}

// BuildPrompt assembles the system prompt with the device capability summary
// and user-memory placeholder filled in.
func BuildPrompt(opts PromptOptions) string {
	devices := summarizeDevices(opts.Devices)
	memory := opts.Memory
	if memory == "" {
		memory = "（暂无）"
	}
	prompt := fmt.Sprintf(systemPrompt, devices, memory)
	if opts.ASCIIOnly {
		prompt += "\n\n═══ 输出环境限制 ═══\n" +
			"用户当前终端是无法渲染中日韩文字的裸字符控制台（任何 CJK 字符都会显示为乱码方块）。" +
			"无论用户使用什么语言提问，你的最终回答必须使用英文，且只使用 ASCII 字符；" +
			"专有名词与文件路径保持原样。"
	}
	return prompt
}

// summarizeDevices renders each node as a compact block: native ability IDs on
// one line, then one line per agent with its capabilities/best_at — the model
// must see what an agent is actually good at (shell, file ops, arbitrary
// commands) or it refuses to route anything that has no exact native ID.
func summarizeDevices(nodes []ledger.Node) string {
	if len(nodes) == 0 {
		return "（暂无设备能力摘要）"
	}
	var b strings.Builder
	for _, n := range nodes {
		var native []string
		for _, a := range n.Native {
			native = append(native, a.ID)
		}
		fmt.Fprintf(&b, "- %s (%s) native: %s\n", n.Name, n.Chip, strings.Join(native, ", "))
		for name, ag := range n.Agents {
			desc := strings.Join(ag.Capabilities, "/")
			if len(ag.BestAt) > 0 {
				desc += "（最擅长：" + strings.Join(ag.BestAt, "、") + "）"
			}
			fmt.Fprintf(&b, "    agent:%s — %s\n", name, desc)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
