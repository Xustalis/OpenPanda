package entry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// The system prompt is layered (pi-style minimal kernel): a compact resident
// core carries the routing decision — answer / tool_call / task / plan — while
// the memory governance rules and the verbose task JSON example are attached
// only when the conversation shows it needs them (ChooseLayers). The device
// summary and the user memory wall close the prompt; the memory section is
// the split point between the stable prefix (rules + devices, which the
// provider prompt cache can reuse) and the volatile tail.

// coreRules is the resident prompt kernel: role, the four output types with
// their routing criteria, and the compact task/plan JSON skeletons —
// everything the model needs to classify correctly on a first call, without
// the optional layers.
const coreRules = `你是 OpenPanda，你所有设备与 agent 的「大总管 / 指挥家」：简单的事你亲自动手，复杂的事你调兵遣将——委派给网络里最合适的那台设备、那个 agent 去完成。你有四种输出类型。

═══ 类型 1：answer ═══
不产生外部副作用、可以直接回答的请求，输出自然语言。
- 直接给结论/答案本身，不要展示分析过程、犹豫或"让我想想"式推理
- 用简洁的自然语言段落；列表仅在枚举时用，少用标题和加粗
- 回复可能被语音朗读或显示在纯文本终端：避免嵌套结构、表情符号和装饰性符号

═══ 类型 2：tool_call ═══
当需要调用受控工具（工具列表通过 tools 参数给出，如 memory_add / memory_read 等）时，使用工具调用返回工具名和参数。Go 核心负责校验、授权、执行和记录。
注意：设备列表里列出的 native/agent 能力（如 sys:info、build:macos）不是受控工具，必须走 task 类型。

═══ 类型 3：task ═══
当任务需要调用某台设备上列出的能力（native/agent）、修改文件、检查代码、运行命令、构建软件、运行 GPU 负载、或涉及多步骤跨设备执行时，输出结构化任务 JSON。路由判断：
- 需要调用设备列表里的能力 / 改文件 / 跑命令 / 编译构建部署 / GPU → task
- 需要记忆/天气/提醒等受控工具 → tool_call（走工具调用）
- 简单到能在 30 秒内独自完成的（回答问题、写个小脚本、改个单行配置）→ 直接回答，不必委派
- 但如果这件事必须拆成几段、且不同段该由不同机器做（先在有编码 agent 的机器上写代码，再去有显存的机器上跑，最后回到轻量机器上总结）→ 用下面的类型 4：plan，不要塞进一个 task

task 时，只输出一个 JSON 对象，前后不得有任何解释文字，骨架：
{"kind":"task","task":{"title":"简短描述","project":"项目名或null","context_type":"file|command|hardware|stream","requires":{"abilities":["..."]},"spec":{"scope":"允许改动的文件/目录：逗号分隔的相对路径","target":"要达成什么","constraints":["不能做的事"],"success_definition":"怎么验证完成"},"complexity":0.0,"risk":"low|medium|high|critical","resource_profile":{"cpu":1,"ram_gb":1,"gpu_vram_gb":0,"duration_hint":"short|long"}}}

spec.scope 必须是逗号分隔的相对路径列表（如 "src/api,webui/app.tsx"），不要写自然语言描述；不确定或允许整个工作目录时留空 ""。

resource_profile 是硬性路由条件，不是装饰字段：调度器会把声明的硬件低于要求的节点直接排除。按任务真实需要填，参照设备列表里每台机器的「硬件」行：
- gpu_vram_gb：只有确实要跑 GPU 负载（训练、微调、大模型推理、CUDA 计算）才填非 0，填这类任务实际需要的显存（小模型训练 6-8，中等 12-16，大模型 24+）；写代码、改配置、跑测试、查信息一律填 0
- cpu / ram_gb：编译、批量数据处理、跑测试套件按实际规模填（如 cpu 8、ram_gb 16）；轻量任务填 1 / 1
- duration_hint：预计超过几分钟填 "long"，否则 "short"。填 "long" 会放宽超时，短任务误填 "long" 会让失败的任务迟迟不被回收
- 宁可略高于实测需求，但不要凭空拔高：填的数字超过网络里任何一台机器声明的硬件，这个任务就无处可去，会直接失败
- 需要 GPU 但当前设备列表里没有机器声明足够显存时，仍按真实需求填 —— 让它明确失败，比悄悄跑在算力不足的机器上更好

requires.abilities 的取值必须、也只能从下方「当前可用设备」列出的能力 ID 中一字不差地选取：
- native 能力直接写其 id（例如列表里的 lint、build:macos），agent 能力写 agent:<名字>（例如 agent:claude_code）
- 严禁编造列表之外的 ID（code:lint、command:run、eslint.check 这类都不合法）
- 若列表里没有完全匹配的 native id：只要目标设备声明了 agent，就委派给该 agent —— agent 拥有完整的 shell、文件系统与命令执行能力；此时绝不要降级为"给出建议让用户手动执行"，必须输出 task 委派

═══ 类型 4：plan ═══
当一件事必须分成几个前后相接的阶段、而且不同阶段适合不同机器时，输出多阶段计划 JSON。判断标准只有一条：**换机器**。
- 「写个训练脚本然后在有显卡的机器上跑，最后把结论发回来」→ plan（三段：开发 / 训练 / 汇报，三台机器）
- 「把这个仓库跑一遍测试」→ task（一段，一台机器就够）
- 阶段多不等于要用 plan：同一台机器上的连续几步，agent 自己会做完，仍然是一个 task

plan 时，只输出一个 JSON 对象，前后不得有任何解释文字，骨架：
{"kind":"plan","plan":{"goal":"用户到底想得到什么","stages":[{"id":"英文短名","title":"队列里显示的一行标题","intent":"这一段要做什么，写给执行它的机器看","requires":["能力ID"],"needs":["前置阶段的id"],"resource_profile":{"cpu":1,"ram_gb":1,"gpu_vram_gb":0,"duration_hint":"short|long"}}]}}

- id 是阶段在计划内的名字，needs 里引用的就是它；必须唯一、必须是 ASCII 短名（develop / train / report）
- needs 既是执行顺序，也是产物接线：被依赖阶段的工作目录会被打包搬到本阶段。所以第二段能直接用第一段写出的文件，不要在 intent 里让它"重新写一遍"
- needs 为空的阶段会立刻并行开跑；互不依赖的阶段不要硬串成一条链，那会白白浪费另一台机器
- 每个阶段的 requires 与 resource_profile 独立填写，规则与 task 完全一致（见上文）——真正吃显存的只有训练那一段，写代码和总结那两段 gpu_vram_gb 一律 0
- intent 要能被单独执行：写给那台机器看，不要出现"如上所述""接着刚才"这类只有你懂的指代
- 阶段数尽量少，能两段就不要三段；上限 64 段

Go 核心必须先校验 kind、工具白名单、参数 schema、权限和当前节点能力，再执行工具或任务；模型输出不能直接当作 shell 命令或硬件指令。`

// memoryRulesSection is the memory governance layer: when to record, what to
// skip, and how to maintain a full memory. Attached only once the session has
// actually used a memory tool (ChooseLayers) — the tool schemas alone carry
// enough semantics for a first call.
const memoryRulesSection = `

═══ 记忆治理规则（何时该记、何时不该记） ═══
该记（主动记忆，无需用户要求）：
- 用户偏好（"我更喜欢 TypeScript"）、沟通风格 → 记到 user 层
- 环境事实（"这台服务器是 Debian 12"）、全局约定、纠正（"别用 sudo，用户在 docker 组"）、已完成的工作 → 记到 memory 层
- 项目约定（"117club 禁止 TypeScript"）→ 记到 project 层
- 用户显式要求"记住 X"
不该记（跳过）：
- 琐碎/明显的信息、可轻易重新查到的、原始数据转储、会话临时信息
维护：记忆接近上限时，先 memory_read 看现有条目，用 memory_replace 合并重叠、memory_remove 删过期，再 memory_add；超限的 add 会报错并回滚。`

// taskExampleSection is the verbose task layer: the full JSON example with
// per-field semantics. Attached only when a task recently appeared in the
// conversation (ChooseLayers) — the resident skeleton already lets the model
// emit a valid first task; this layer refines tasks once the session is in
// task mode.
const taskExampleSection = `

═══ task 完整示例 ═══
{
  "kind": "task",
  "task": {
    "title": "简短描述",
    "project": "项目名或 null",
    "context_type": "file|command|hardware|stream",
    "requires": {"abilities": ["lint"]},
    "spec": {
      "scope": "允许改动的文件/目录，逗号分隔的相对路径；不确定则留空",
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
agent 的具体能力见设备列表中每个 agent 的说明行；跨多步、需要判断力的操作优先选 agent 而非固定参数的 native。`

// memorySectionMarker starts the volatile tail of the system prompt (the user
// memory wall, which changes with the conversation); everything before it —
// routing rules plus the device summary — is the stable, cacheable prefix.
const memorySectionMarker = "═══ 用户记忆"

// splitPromptSections splits a system prompt at the memory section marker
// into (stable, volatile). A prompt without the marker (e.g. the supervise
// prompt) is entirely stable.
func splitPromptSections(system string) (stable, volatile string) {
	if i := strings.Index(system, memorySectionMarker); i >= 0 {
		return system[:i], system[i:]
	}
	return system, ""
}

// PromptLayers selects the optional prompt sections for one classification
// call: the memory governance rules join once the session has actually used a
// memory tool, and the verbose task JSON example joins once a task recently
// appeared in the conversation. The resident core always carries the compact
// routing rules and task skeleton, so a first-call classification needs
// neither optional layer.
type PromptLayers struct {
	MemoryRules bool
	TaskExample bool
}

// layersWindow bounds how far back ChooseLayers looks for recent task
// activity: a task from many turns ago says little about the current ask.
const layersWindow = 8

// memoryToolPrefix names the memory tool family (memory_read/add/replace/
// remove), whose use triggers the memory governance layer.
const memoryToolPrefix = "memory_"

// taskTurnMarkers are the assistant-turn shapes that mean "a task ran": the
// CLI conversation summarizes a task outcome as "[任务<id> <state>] …", and a
// replayed task directive carries the kind tag. A started plan counts too — its
// stages are tasks, and the follow-up question ("跑到哪了") is about them.
var taskTurnMarkers = []string{
	"[任务", `"kind":"task"`, `"kind": "task"`,
	"[计划", `"kind":"plan"`, `"kind": "plan"`,
}

// ChooseLayers is the pure injection decision: given the conversation turns
// so far, which optional prompt sections does this call need? Memory-tool
// activity is scanned across the whole history (once the session is a memory
// session it stays one); task activity only within the recent window.
func ChooseLayers(turns []Turn) PromptLayers {
	var l PromptLayers
	for _, t := range turns {
		if mentionsMemoryTool(t) {
			l.MemoryRules = true
		}
	}
	start := 0
	if len(turns) > layersWindow {
		start = len(turns) - layersWindow
	}
	for _, t := range turns[start:] {
		if t.Role == "assistant" && mentionsTask(t) {
			l.TaskExample = true
		}
	}
	return l
}

// mentionsMemoryTool reports whether one turn shows memory-tool activity: a
// native tool_use block naming a memory_* tool, or the text-JSON fallback
// prose carrying such a call.
func mentionsMemoryTool(t Turn) bool {
	for _, b := range t.Blocks {
		if b.Type == "tool_use" && strings.HasPrefix(b.Name, memoryToolPrefix) {
			return true
		}
	}
	return strings.Contains(t.Content, `"tool":"memory_`) ||
		strings.Contains(t.Content, `"tool": "memory_`)
}

// mentionsTask reports whether an assistant turn records a task outcome.
func mentionsTask(t Turn) bool {
	for _, m := range taskTurnMarkers {
		if strings.Contains(t.Content, m) {
			return true
		}
	}
	return false
}

// PromptOptions carries the dynamic parts of the system prompt.
type PromptOptions struct {
	Devices []ledger.Node // capability directory snapshot (may be empty)
	Memory  string        // Hermes memory summary (may be empty; capped)
	// History is the conversation so far; ChooseLayers reads it to decide
	// which optional prompt layers (memory rules, task example) to attach.
	History []Turn
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

// BuildPrompt assembles the layered system prompt: the resident routing core,
// the optional layers ChooseLayers picks from the history, then the device
// capability summary (stable prefix end) and the user-memory wall (volatile
// tail).
func BuildPrompt(opts PromptOptions) string {
	layers := ChooseLayers(opts.History)
	devices := summarizeDevicesCached(opts.Devices)
	memory := opts.Memory
	if memory == "" {
		memory = "（暂无）"
	}
	var b strings.Builder
	b.WriteString(coreRules)
	if layers.MemoryRules {
		b.WriteString(memoryRulesSection)
	}
	if layers.TaskExample {
		b.WriteString(taskExampleSection)
	}
	b.WriteString("\n\n═══ 当前可用设备 ═══\n")
	b.WriteString(devices)
	b.WriteString("\n\n═══ 用户记忆（仅对话参考，不进入项目工作） ═══\n")
	b.WriteString(memory)
	if opts.ASCIIOnly {
		b.WriteString("\n\n═══ 输出环境限制 ═══\n" +
			"用户当前终端是无法渲染中日韩文字的裸字符控制台（任何 CJK 字符都会显示为乱码方块）。" +
			"无论用户使用什么语言提问，你的最终回答必须使用英文，且只使用 ASCII 字符；" +
			"专有名词与文件路径保持原样。")
	}
	return b.String()
}

// deviceSummaryCache is a single-entry memo of the last device summary: the
// capability directory rarely changes between calls, so hashing the snapshot
// and reusing the rendered summary skips the rebuild and — more importantly —
// keeps the stable prompt prefix byte-identical, which the provider prompt
// cache needs to hit.
var deviceSummaryCache struct {
	mu     sync.Mutex
	key    string
	result string
}

// deviceSnapshotKey hashes the device snapshot; an identical snapshot (same
// nodes, same abilities) yields an identical key.
func deviceSnapshotKey(nodes []ledger.Node) string {
	blob, err := json.Marshal(nodes)
	if err != nil {
		// Unmarshalable nodes (never in practice): fall back to a stable
		// textual form so the key stays a pure function of the content.
		return hashString(fmt.Sprint(nodes))
	}
	return hashString(string(blob))
}

// summarizeDevicesCached returns the device summary, reusing the previous
// rendering when the snapshot hash is unchanged.
func summarizeDevicesCached(nodes []ledger.Node) string {
	key := deviceSnapshotKey(nodes)
	deviceSummaryCache.mu.Lock()
	defer deviceSummaryCache.mu.Unlock()
	if key == deviceSummaryCache.key && deviceSummaryCache.result != "" {
		return deviceSummaryCache.result
	}
	s := summarizeDevices(nodes)
	deviceSummaryCache.key = key
	deviceSummaryCache.result = s
	return s
}

// summarizeDevices renders each node as a compact block: native ability IDs on
// one line, then one line per agent with its capabilities/best_at — the model
// must see what an agent is actually good at (shell, file ops, arbitrary
// commands) or it refuses to route anything that has no exact native ID.
//
// The declared hardware line is what makes resource_profile answerable. That
// field is a *hard* routing filter: a task asking for 8 GiB of VRAM is refused by
// every node that declares less. Asking the model to fill it while hiding what
// any machine has is asking it to guess, and both guesses are bad — too high
// makes the task unroutable, too low sends a training run to the Pi. So each node
// states its numbers, and the rule below tells the model to size against them.
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
		if hw := describeHardware(n); hw != "" {
			fmt.Fprintf(&b, "    硬件: %s\n", hw)
		}
		names := make([]string, 0, len(n.Agents))
		for name := range n.Agents {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			ag := n.Agents[name]
			desc := strings.Join(ag.Capabilities, "/")
			if len(ag.BestAt) > 0 {
				desc += "（最擅长：" + strings.Join(ag.BestAt, "、") + "）"
			}
			fmt.Fprintf(&b, "    agent:%s — %s\n", name, desc)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// describeHardware renders a node's declared hardware, and only what it actually
// declared. An undeclared profile is silence, not a claim of zero — every card
// written before v0.0.6 is all-zero — so it prints nothing rather than "0 GiB
// VRAM", which would read as "this machine has no GPU" and is a different claim.
func describeHardware(n ledger.Node) string {
	var parts []string
	r := n.ResourceProfile
	if r.CPU > 0 {
		parts = append(parts, fmt.Sprintf("cpu %d 核", r.CPU))
	} else if n.Capacity.CPUCores > 0 {
		parts = append(parts, fmt.Sprintf("cpu %d 核", n.Capacity.CPUCores))
	}
	if r.RAMGB > 0 {
		parts = append(parts, fmt.Sprintf("内存 %d GiB", r.RAMGB))
	} else if n.Capacity.RAMGB > 0 {
		parts = append(parts, fmt.Sprintf("内存 %d GiB", n.Capacity.RAMGB))
	}
	if r.GPUVRAMGB > 0 {
		parts = append(parts, fmt.Sprintf("显存 %d GiB", r.GPUVRAMGB))
	} else if r.Declared() {
		// The card describes its hardware but names no VRAM. Say "undeclared",
		// never "0": zero would read as "this machine has no GPU", a claim the
		// card never made and one the scheduler does not make either — Fits lets
		// an undeclared node through rather than declining every GPU task.
		parts = append(parts, "未声明显存")
	}
	if !r.Declared() && len(parts) == 0 {
		// A card written before v0.0.6 says nothing about hardware at all.
		return "未声明（该节点未填 resource_profile，调度器不会因显存要求排除它）"
	}
	if n.Capacity.MaxConcurrent > 0 {
		parts = append(parts, fmt.Sprintf("并发上限 %d", n.Capacity.MaxConcurrent))
	}
	return strings.Join(parts, "，")
}
