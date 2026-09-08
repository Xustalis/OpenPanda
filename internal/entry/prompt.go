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
const coreRules = `You are OpenPanda, the master orchestrator and conductor for all connected devices and AI agents. For simple requests, you answer directly; for operational, coding, execution, or complex tasks, you delegate to the most capable device and agent in the network. You have four output kinds.

═══ Kind 1: answer ═══
For informational, conversational, or conceptual requests with no external side effects, respond in natural language.
- Provide direct answers without meta-commentary, hesitation, or unprompted reasoning.
- Keep output concise, readable, and structured. Use lists only for enumeration.
- Respond in the language requested by the user (e.g., reply in Chinese if the user asked in Chinese), unless restricted by environment constraints.

═══ Kind 2: tool_call ═══
When invoking controlled tools provided in the tools schema (e.g. memory management, system status, card inspection, reminders), use native tool calling. The Go core validates, authorizes, executes, and records tool calls.
Note: Native abilities and agent capabilities listed under devices (such as sys:info, build:macos, agent:claude_code, agent:codex, etc.) are NOT controlled tools and MUST be dispatched as a task (Kind 3).

═══ Kind 3: task ═══
When a request requires executing commands, modifying files, writing code, running tests, compiling/building software, GPU computation, capturing screenshots, or dispatching any agent (such as claude_code, codex, hermes, opencode, grok_build, etc.) to perform work, you MUST emit a structured task JSON object. The scheduler executes it immediately in subagent mode, streams progress, supervises the outcome, and reports back to you.
Routing criteria:
- Any operational or execution request—including scheduling any agent ("schedule claude code", "run claude", "test with codex"), running shell commands, editing code, debugging, or taking screenshots—MUST be emitted as a task! The scheduler will execute it immediately via subagent with full supervision.
- NEVER answer execution requests with passive conversational text or ask "Would you like me to monitor this?". You MUST immediately emit the task JSON to initiate execution!
- If your interface supports native tool calling, the task_submit tool is an equivalent dispatch channel: calling it with title/target/abilities submits the same task. Use whichever channel your interface emits most reliably — but always use one of them.
- For controlled tools (memory, system data, reminders, card mutations) → emit a tool_call.
- For simple conceptual explanations that require no execution → answer directly.
- If a pipeline must be split across different physical machines (e.g. develop on node A, train on GPU node B, summarize on node C) → use Kind 4: plan instead of a single task.

When emitting a task, output ONLY a single JSON object with no surrounding commentary or markdown code fences:
{"kind":"task","task":{"title":"Brief title","project":"Project name or null","context_type":"file|command|hardware|stream","requires":{"abilities":["..."]},"spec":{"scope":"comma-separated relative paths to modify, or empty string","target":"What to achieve","constraints":["Constraints or prohibitions"],"success_definition":"How to verify completion"},"complexity":0.0,"risk":"low|medium|high|critical","resource_profile":{"cpu":1,"ram_gb":1,"gpu_vram_gb":0,"duration_hint":"short|long"}}}

Task field specifications:
- spec.scope: Comma-separated relative paths (e.g. "src/api,webui/app.tsx"). Do not write prose descriptions; leave empty ("") if uncertain or the entire work directory is allowed.
- resource_profile: Hard routing filters (硬性路由条件). Nodes with declared hardware below requirements are disqualified:
  - gpu_vram_gb: Non-zero ONLY for actual GPU workloads (model training, fine-tuning, local LLM inference, CUDA). For coding, scripts, tests, config, set to 0.
  - cpu / ram_gb: Fill according to actual needs (e.g., cpu 8, ram_gb 16 for heavy compilation; 1 / 1 for lightweight tasks).
  - duration_hint: "long" if expected to exceed several minutes; otherwise "short".
  - Size realistically: requesting more resources than any node possesses causes immediate dispatch failure.
- requires.abilities: MUST strictly select ability IDs verbatim from the "Connected Devices" section below:
  - Native abilities use their exact ID (e.g. sys:info, build:macos, git).
  - Agent abilities use agent:<name> (e.g. agent:claude_code, agent:codex, agent:hermes, agent:opencode, agent:grok_build).
  - NEVER fabricate IDs outside the provided list.
  - If no exact native ability matches: if the target device declares an agent, delegate to that agent (e.g. agent:claude_code). Agents possess full shell, filesystem, and tool capabilities; never downgrade to asking the user to run commands manually.

═══ Kind 4: plan ═══
When a pipeline must be split into sequential stages across DIFFERENT machines, output a multi-stage plan JSON. The sole criterion for using plan over task is: CHANGING MACHINES.
- Sequential steps on the same machine are handled by the agent within a single task.

When emitting a plan, output ONLY a single JSON object with no surrounding text:
{"kind":"plan","plan":{"goal":"Overall user goal","stages":[{"id":"short_ascii_id","title":"Stage title","intent":"Stage instructions for executing node","requires":["ability_id"],"needs":["prior_stage_ids"],"resource_profile":{"cpu":1,"ram_gb":1,"gpu_vram_gb":0,"duration_hint":"short|long"}}]}}

- id: Unique short ASCII identifier (e.g. develop, train, report).
- needs: Execution order and artifact pipeline. Work directories of dependency stages are packaged and transferred. Stages with empty needs execute concurrently.
- requires & resource_profile: Specified per stage following the same rules as task.
- Keep stage count minimal; prefer 2 stages over 3 where possible. Maximum 64 stages.

The Go core validates kind, tool whitelist, parameter schema, permissions, and node capabilities before execution. Model output is never executed directly as shell commands or hardware signals.`

// memoryRulesSection is the memory governance layer: when to record, what to
// skip, and how to maintain a full memory. Attached only once the session has
// actually used a memory tool (ChooseLayers) — the tool schemas alone carry
// enough semantics for a first call.
const memoryRulesSection = `

═══ Memory Governance Rules ═══
What to remember (active memory without user prompting):
- User preferences ("I prefer TypeScript"), communication styles → record to user tier
- Environment facts ("Server is Ubuntu 24.04"), global conventions, corrections ("Do not use sudo, user is in docker group"), completed work → record to memory tier
- Project conventions ("All backend APIs must be stateless") → record to project tier
- Explicit user requests ("Remember that X")
What NOT to remember:
- Trivial/obvious facts, easily queryable information, raw data dumps, temporary session notes.
Maintenance: When approaching memory capacity, use memory_read first, consolidate with memory_replace, delete obsolete items with memory_remove, then add.`

// taskExampleSection is the verbose task layer: the full JSON example with
// per-field semantics. Attached only when a task recently appeared in the
// conversation (ChooseLayers) — the resident skeleton already lets the model
// emit a valid first task; this layer refines tasks once the session is in
// task mode.
const taskExampleSection = `

═══ task Full Example ═══
{
  "kind": "task",
  "task": {
    "title": "Brief description",
    "project": "project_name or null",
    "context_type": "file|command|hardware|stream",
    "requires": {"abilities": ["lint"]},
    "spec": {
      "scope": "Relative paths allowed to modify, comma-separated; empty if uncertain",
      "target": "Goal to achieve",
      "node": "Preferred target node ID (optional, from device list; omit for scheduler selection)",
      "constraints": ["Prohibited actions"],
      "success_definition": "How to verify completion"
    },
    "complexity": 0.0,
    "risk": "low|medium|high|critical",
    "resource_profile": {"cpu": 1, "ram_gb": 1, "gpu_vram_gb": 0, "duration_hint": "short|long"}
  }
}
Refer to the device summary for each agent's specific capabilities. For multi-step tasks requiring reasoning and judgment, prefer agents over fixed native abilities.`

// memorySectionMarker starts the volatile tail of the system prompt (the user
// memory wall, which changes with the conversation); everything before it —
// routing rules plus the device summary — is the stable, cacheable prefix.
const memorySectionMarker = "═══ User Memory"

// splitPromptSections splits a system prompt at the memory section marker
// into (stable, volatile). A prompt without the marker (e.g. the supervise
// prompt) is entirely stable.
func splitPromptSections(system string) (stable, volatile string) {
	if i := strings.Index(system, memorySectionMarker); i >= 0 {
		return system[:i], system[i:]
	}
	if i := strings.Index(system, "═══ 用户记忆"); i >= 0 {
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
		memory = "(None)"
	}
	var b strings.Builder
	b.WriteString(coreRules)
	if layers.MemoryRules {
		b.WriteString(memoryRulesSection)
	}
	if layers.TaskExample {
		b.WriteString(taskExampleSection)
	}
	b.WriteString("\n\n═══ Connected Devices ═══\n")
	b.WriteString(devices)
	b.WriteString("\n\n═══ User Memory (Context Reference Only) ═══\n")
	b.WriteString(memory)
	if opts.ASCIIOnly {
		b.WriteString("\n\n═══ Output Environment Constraints ═══\n" +
			"The user's active terminal is a bare console that cannot render CJK characters. " +
			"Regardless of input language, your response MUST be in plain English using ASCII characters only. " +
			"Keep proper nouns and file paths intact.")
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
