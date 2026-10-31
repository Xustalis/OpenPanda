// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Xustalis/OpenPanda/internal/contract"
	"github.com/Xustalis/OpenPanda/internal/i18n"
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
const coreRules = `You are OpenPanda, the master orchestrator and conductor for all connected devices and AI agents. For simple requests, you answer directly; for operational, coding, execution, or complex tasks, you delegate to the most capable device and agent in the network. All external agent harnesses (such as claude_code, codex, opencode, grok_build) and heterogeneous devices (such as orangepi/香橙派, win/Windows PC, mac, linux GPU) are Subagents available to schedule. You have four output kinds.

═══ Kind 1: answer ═══
For informational, conversational, or conceptual requests with no external side effects, respond in natural language.
- Provide direct answers without meta-commentary, hesitation, or unprompted reasoning.
- Keep output concise, readable, and structured. Use lists only for enumeration.
- Respond in the language requested by the user (e.g., reply in Chinese if the user asked in Chinese), unless restricted by environment constraints.

═══ Kind 2: tool_call ═══
When invoking controlled tools provided in the tools schema (e.g. memory management, system status, card inspection, reminders), use native tool calling. The Go core validates, authorizes, executes, and records tool calls.
Note: Native abilities and agent capabilities listed under devices (such as sys:info, build:macos, agent:claude_code, agent:codex, etc.) are NOT controlled tools and MUST be dispatched as a subagent task (Kind 3).

═══ Kind 3: task ═══
When a request requires executing commands, modifying files, writing code, running tests, compiling/building software, GPU computation, capturing screenshots, or dispatching any agent harness (such as claude_code, codex, hermes, opencode, grok_build, etc.) or scheduling specific devices (such as orangepi/香橙派, win/Windows, mac, linux) as subagents, you MUST emit a structured task JSON object. The scheduler executes it immediately in subagent mode, streams progress, supervises the outcome, and reports back to you.
Routing criteria:
- Any operational or execution request—including scheduling any agent ("schedule claude code", "run claude", "test with codex"), targeting specific hardware/devices ("run on orange pi", "execute on win"), running shell commands, editing code, debugging, or taking screenshots—MUST be emitted as a task! The scheduler will execute it immediately via subagent with full supervision.
- Subagent scheduling archetypes:
  - Cognitive / Coding Harness Subagents:
    - agent:claude_code: Top-tier for large architecture refactoring, complex multi-file edits, and autonomous deep investigation.
    - agent:codex: Best for unit tests, bug fixes, focused code generation, and code review.
    - agent:opencode / agent:grok_build / agent:pi: Fast scripts, quick edits, diagnostics.
  - Device / Hardware Environment Subagents:
    - Orange Pi / 香橙派 (ARM64 SBC Subagent): Hardware/GPIO sensing, edge computing, sensor monitoring, low-power continuous tasks. (Low RAM, keep tasks lightweight, 0 GPU VRAM).
    - Windows Subagent (Win PC / Workstation): Windows-native tasks, PowerShell automation, .NET / MSBuild, DirectX / Windows UI testing.
    - Mac / Linux GPU Subagents: High-performance compilation, CUDA training, heavy computations.
    - Desktop / GUI Subagents (computer use): a device advertising hardware:screen_capture + hardware:gui_input can see and drive its graphical session. Chain the pair — capture the screen, reason over the image in an agent task on the same device, then click/type via action_spec. GUI input is irreversible and runs behind the node's approval tier.
- NEVER answer execution requests with passive conversational text or ask "Would you like me to monitor this?". You MUST immediately emit the task JSON to initiate execution!
- If your interface supports native tool calling, the task_submit tool is an equivalent dispatch channel: calling it with title/target/abilities submits the same task. Use whichever channel your interface emits most reliably — but always use one of them.
- For controlled tools (memory, system data, reminders, card mutations) → emit a tool_call.
- For simple conceptual explanations that require no execution → answer directly.
- If a pipeline must be split across different physical machines (e.g. develop on node A, train on GPU node B, summarize on node C) → use Kind 4: plan instead of a single task.

When emitting a task, output ONLY a single JSON object with no surrounding commentary or markdown code fences:
{"kind":"task","task":{"title":"Brief title","project":"Project name or null","context_type":"file|command|hardware|stream","requires":{"abilities":["..."]},"spec":{"scope":"comma-separated relative paths to modify, or empty string","target":"Faithful restatement of what the user asked — preserve their details, do not paraphrase away specifics","constraints":["Constraints or prohibitions"],"success_definition":"How to verify completion","acceptance":[],"action_spec":{"target_actuator":"hardware:x","action":"verb","parameters":{"name":0}}},"complexity":0.0,"risk":"low|medium|high|critical","tools_policy":"minimal|extended","max_turns":0,"resource_profile":{"cpu":1,"ram_gb":1,"gpu_vram_gb":0,"duration_hint":"short|long"}}}

Task field specifications:
- spec.scope: Comma-separated relative paths (e.g. "src/api,webui/app.tsx"). Do not write prose descriptions; leave empty ("") if uncertain or the entire work directory is allowed.
- spec.acceptance: OPTIONAL, and only when an "Acceptance Candidates" section is present. Copy 1-3 checks from that list as {"id":"<id>","run":"<run>"}. Never invent a command; omit the field when none of the candidates prove this task's success.
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
- spec.target: Restate the user's request faithfully and completely. The executing agent reads target as its instruction — a summary that drops their specifics produces wrong work. Keep names, file paths, error messages, and numbers verbatim. Write it as an order an engineer can execute without asking back: the concrete change/deliverable, where it applies, and how "done" is checked. No rhetorical framing, no "please confirm" tails.
- spec.action_spec: REQUIRED when the task drives a physical or desktop actuator — an ability whose ID starts with "hardware:" in the device list (e.g. hardware:servo_rotate, hardware:mic_record, hardware:camera_snap, hardware:notify, hardware:screen_capture, hardware:gui_input). Emit {"target_actuator":"<the exact hardware:* ID, also placed in requires>","action":"<verb such as rotate|record|snap|notify|capture|click|type|key>","parameters":{"<name>":<scalar>}}. A screen-capture actuator needs no parameters. GUI input (hardware:gui_input) takes one "args" string matching the verb contract: move/click/dblclick → "x,y[,button]", type → the literal text, key → "ctrl+c" or "Return", scroll → "up|down[,n]", pos → "". Parameters must be scalar (number, string, or boolean) and must name the values the action implies — the driver fails loudly when a needed placeholder is missing. Omit the whole field for ordinary command/file/agent tasks.
- tools_policy: omit or "minimal" for routine tasks. Use "extended" ONLY for high-complexity agent tasks that legitimately need the agent's full tool face (sub-agents, MCP servers, web tools).
- max_turns: omit or 0 for the adapter default. Set higher (e.g. 60-100) only for complex multi-file build-test-debug work the 30-turn default would truncate.

═══ Kind 4: plan ═══
When a pipeline must be split into sequential stages across DIFFERENT machines, output a multi-stage plan JSON. The sole criterion for using plan over task is: CHANGING MACHINES.
- Sequential steps on the same machine are handled by the agent within a single task.

When emitting a plan, output ONLY a single JSON object with no surrounding text:
{"kind":"plan","plan":{"goal":"Overall user goal","stages":[{"id":"short_ascii_id","title":"Stage title","intent":"Stage instructions for executing node","requires":["ability_id"],"needs":["prior_stage_ids"],"node":"device_id_or_empty","resource_profile":{"cpu":1,"ram_gb":1,"gpu_vram_gb":0,"duration_hint":"short|long"}}]}}

- id: Unique short ASCII identifier (e.g. develop, train, report).
- needs: Execution order and artifact pipeline. Work directories of dependency stages are packaged and transferred. Stages with empty needs execute concurrently.
- node: HARD PIN — when the user names a specific device for a stage ("在 Mac 上写", "on the Windows box"), set node to that device's id or display name from the Connected Devices list. The stage then runs ONLY there — never silently rerouted to another machine that happens to satisfy requires. Omit for free routing.
- requires & resource_profile: Specified per stage following the same rules as task. requires alone NEVER expresses device choice — an ability like agent:claude_code exists on several machines; only node pins the device.
- Keep stage count minimal; prefer 2 stages over 3 where possible. Maximum 64 stages.

The Go core validates kind, tool whitelist, parameter schema, permissions, and node capabilities before execution. Model output is never executed directly as shell commands or hardware signals.

═══ Live state & self-correction ═══
- Claims about what a task or plan is doing must be grounded in THIS turn's tool output — an earlier exchange is a stale snapshot, not evidence. When live data contradicts something you said before, say so plainly and correct it; never defend a stale claim.
- Work that already exists is operated on by id (check status, resume, approve, cancel). Never re-submit or re-plan the same work unless the user asks for a fresh attempt.
- Distinguish waiting-for-approval, queued-on-device, and actually-running: a parked task is not a dead one, and a remote task reports the node and directory where it runs.
- A task that finishes while leaving a service running is DONE — describe it as completed and name what it left behind (URL, port, PID); "service retained" is not "task still running", and a long-lived server process is normal, not a stuck task.`

// executionContract is the closing rider appended to every system prompt: a
// checkable execution contract the model is bound to when it emits or
// supervises a task. It is static so it stays in the cacheable prefix.
const executionContract = `

═══ Execution Contract ═══
[execution context] Every task run is framed by its task title, its project, and the workspace-as-cwd. The executing agent must stay within spec.scope (the comma-separated relative paths it may modify) and must treat spec.success_definition as the check it must pass.
Before you claim done:
- Read the project's own sources before editing; do not work from memory of other repos.
- Prove the success definition with the project's own build/test convention (make build / make test / make gate) before claiming done.
- Stay inside the declared scope (spec.scope); do not touch paths outside it.
- Report the commands you ran and their outcomes.
- Name any criterion you could not verify.`

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
      "target": "Goal to achieve — restate the user's request faithfully, keep specifics verbatim",
      "node": "Preferred target node ID (optional, from device list; omit for scheduler selection)",
      "constraints": ["Prohibited actions"],
      "success_definition": "How to verify completion",
      "action_spec": "ONLY when driving a hardware:* actuator: {\"target_actuator\":\"hardware:x\",\"action\":\"verb\",\"parameters\":{\"name\":value}}"
    },
    "complexity": 0.0,
    "risk": "low|medium|high|critical",
    "tools_policy": "minimal|extended (optional; extended only for complex agent work needing full tool face)",
    "max_turns": 0,
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
// CLI conversation summarizes a task outcome as "[任务<id> <state>] …" or "[task <id> <state>] …",
// and a replayed task directive carries the kind tag. A started plan or subagent counts too — its
// stages are tasks, and the follow-up question is about them.
var taskTurnMarkers = []string{
	"[任务", "[task", `"kind":"task"`, `"kind": "task"`,
	"[计划", "[plan", `"kind":"plan"`, `"kind": "plan"`,
	"[Subagent", "[subagent", "[子代理",
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
	// UserLocale indicates the target UI/prompt language preference.
	UserLocale i18n.Locale
	// RequestMode is the interaction mode the user picked for this turn with
	// a slash prefix: "goal" (refine the goal before acting), "plan" (emit a
	// staged plan), or "spec" (write a detailed specification). Empty means
	// the classifier decides on its own.
	RequestMode string
	// AgentRoster is the thread's team pick — the agents the user chose for
	// this conversation, in the order they picked them. It is shown to the
	// model because a roster the model cannot see is a roster it will
	// contradict: asked to "test the agents I picked", an entry model that
	// only knows the node's capability card invented claude_code, codex and
	// deepseek_harness for a thread whose team was deepseek_harness and
	// opencode, and pinned each task to an agent the thread had excluded.
	// Empty means "no pick" (the scheduler routes by capability).
	AgentRoster []string
	// AcceptanceCandidates are the verification entry points the workspace
	// already declares (Makefile targets, package scripts, toolchain
	// defaults, CI steps). The model selects from this list for
	// spec.acceptance; it never invents a command. Empty means "no project
	// in view" and the task will be reported unverified.
	AcceptanceCandidates []contract.Candidate
}

// ClassifyOption tweaks the system prompt the Classify* entry points build.
type ClassifyOption func(*PromptOptions)

// WithASCIIOnly makes the entry model answer in English/ASCII (for terminals
// that cannot render CJK).
func WithASCIIOnly() ClassifyOption {
	return func(p *PromptOptions) { p.ASCIIOnly = true }
}

// WithLocale sets the user language preference for prompt construction.
func WithLocale(loc i18n.Locale) ClassifyOption {
	return func(p *PromptOptions) { p.UserLocale = loc }
}

// WithRequestMode records the slash-prefix mode the user chose for the turn
// (/goal, /plan, /spec). Unknown values are ignored.
func WithRequestMode(mode string) ClassifyOption {
	return func(p *PromptOptions) {
		switch mode {
		case "goal", "plan", "spec":
			p.RequestMode = mode
		}
	}
}

// WithAgentRoster records the thread's team pick so the entry model can name
// the user's own agents instead of inventing them. Names are trimmed, empty
// entries dropped, duplicates removed, order preserved.
func WithAgentRoster(names []string) ClassifyOption {
	return func(p *PromptOptions) {
		seen := make(map[string]bool, len(names))
		out := make([]string, 0, len(names))
		for _, raw := range names {
			name := strings.TrimSpace(raw)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
		p.AgentRoster = out
	}
}

// WithAcceptanceCandidates tells the entry model which verification checks
// the workspace already declares. The model selects; it never authors a
// command. Duplicates and empty entries are dropped, and the list is capped so
// the prompt stays a shortlist rather than a menu.
func WithAcceptanceCandidates(cands []contract.Candidate) ClassifyOption {
	return func(p *PromptOptions) {
		seen := make(map[string]bool, len(cands))
		out := make([]contract.Candidate, 0, len(cands))
		for _, c := range cands {
			id := strings.TrimSpace(c.ID)
			run := strings.TrimSpace(c.Run)
			if id == "" || run == "" || seen[id] || len(out) >= 12 {
				continue
			}
			seen[id] = true
			c.ID, c.Run = id, run
			out = append(out, c)
		}
		p.AcceptanceCandidates = out
	}
}

// acceptanceCandidatesLine renders the project's own verification entry
// points. The instruction is blunt about the two failure modes this section
// exists to prevent: inventing a command that does not exist in the project,
// and selecting a step whose purpose is to ship rather than to prove.
func acceptanceCandidatesLine(cands []contract.Candidate) string {
	var b strings.Builder
	b.WriteString("The workspace already declares these verification checks. When you emit a task, choose 1-3 of them that actually prove THIS task's success and put them in spec.acceptance as {\"id\":\"<id>\",\"run\":\"<run>\"}. Do not invent commands, do not alter the command text, and do not select deploy/publish/clean steps.\n")
	b.WriteString("If none of them can prove this task's success, omit spec.acceptance; the task will be reported unverified rather than judged against a check that proves nothing.\n")
	for _, c := range cands {
		b.WriteString("- " + c.ID + ": " + c.Run)
		if c.Source != "" {
			b.WriteString(" (" + c.Source + ")")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// agentRosterLine renders the thread's team pick. It is deliberately blunt
// about two things the model got wrong when this section did not exist: the
// roster is a closed list (a task pinned elsewhere is refused, not re-staffed),
// and "test the agents I picked" means one task per listed agent rather than
// whatever names happen to appear in the capability summary.
func agentRosterLine(names []string) string {
	list := strings.Join(names, ", ")
	return "The user picked these agents for this conversation: " + list + ".\n" +
		"Only these agents may run this thread's tasks. A task whose requires pin an agent outside this list is refused.\n" +
		"When the user asks you to test, compare, or exercise \"the agents I picked\", emit one task per agent on this list, each with requires [\"agent:<name>\"] — do not invent agents that are not on it, and do not assume a single task covers the whole team.\n" +
		"本会话团队：" + list + "。只有名单内的 agent 可以执行本会话的任务；requires 点名名单外 agent 的任务会被拒绝。\n" +
		"用户要求「测试我选的这几个 agent」时，为名单内每个 agent 各发一个任务，requires 用 [\"agent:<name>\"]；不得编造名单外的 agent，也不要用一个任务代表整个团队。"
}

// requestModeSection renders the slash-mode directive. It goes after the user
// memory block (volatile tail) so it cannot poison the cached prefix, and it
// is deliberately imperative: a mode the user typed must not be overruled by
// the default "classify at your own discretion" framing above.
var requestModeSections = map[string]string{
	"goal": "\n\n═══ Request Mode: GOAL ═══\n" +
		"The user invoked /goal. Treat this turn as goal refinement, not execution: " +
		"restate the goal in one precise sentence, list the assumptions you are making, " +
		"name the success criteria and constraints, and ask only the clarifying questions " +
		"that genuinely block progress. Answer with plain prose (kind answer) — do not " +
		"delegate a task or emit a plan yet.",
	"plan": "\n\n═══ Request Mode: PLAN ═══\n" +
		"The user invoked /plan. Produce a staged execution plan: if the request is concrete " +
		"enough to act on, respond with kind plan and a complete stages spec; if critical " +
		"details are missing, answer with the short list of blocking questions instead of " +
		"guessing. Do not respond with a bare answer once the requirements are clear.",
	"spec": "\n\n═══ Request Mode: SPEC ═══\n" +
		"The user invoked /spec. Write a detailed specification document as your answer " +
		"(kind answer): purpose and scope, functional requirements, interfaces and data " +
		"shapes, edge cases and failure modes, and acceptance criteria. Be precise and " +
		"complete enough that an implementer could start from it without a follow-up " +
		"question. Do not delegate execution.",
}

// promptLocale resolves the locale the system prompt renders in: an explicit
// option wins, ASCII-only output forces English, and otherwise the process
// locale. classifyCacheKey resolves the same way — the cache key must track
// the prompt that is actually sent, or a detected locale would key rows to a
// rendering the model never saw (and, with the single-entry device memo, make
// the two callers thrash each other's summary on every ask).
func promptLocale(opts PromptOptions) i18n.Locale {
	if opts.UserLocale != "" {
		return opts.UserLocale
	}
	if opts.ASCIIOnly {
		return i18n.English
	}
	return i18n.Detect()
}

// BuildPrompt assembles the layered system prompt: the resident routing core,
// the optional layers ChooseLayers picks from the history, then the device
// capability summary (stable prefix end) and the user-memory wall (volatile
// tail).
func BuildPrompt(opts PromptOptions) string {
	layers := ChooseLayers(opts.History)
	devices := summarizeDevicesCached(opts.Devices, promptLocale(opts))
	memory := opts.Memory
	if memory == "" {
		memory = "(None)"
	}
	var b strings.Builder
	b.WriteString(coreRules)
	b.WriteString(executionContract)
	if layers.MemoryRules {
		b.WriteString(memoryRulesSection)
	}
	if layers.TaskExample {
		b.WriteString(taskExampleSection)
	}
	if len(opts.AcceptanceCandidates) > 0 {
		b.WriteString("\n\n═══ Acceptance Candidates ═══\n")
		b.WriteString(acceptanceCandidatesLine(opts.AcceptanceCandidates))
	}
	b.WriteString("\n\n═══ Connected Devices ═══\n")
	b.WriteString(devices)
	if len(opts.AgentRoster) > 0 {
		b.WriteString("\n\n═══ This Thread's Team ═══\n")
		b.WriteString(agentRosterLine(opts.AgentRoster))
	}
	b.WriteString("\n\n═══ User Memory (Context Reference Only) ═══\n")
	b.WriteString(memory)
	if section, ok := requestModeSections[opts.RequestMode]; ok {
		b.WriteString(section)
	}
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
// nodes, same abilities) yields an identical key. Volatile bookkeeping that
// the rendered summary never shows — LastSeen heartbeats and the live/load
// half of Capacity — is zeroed before hashing: it changes every beat and
// would otherwise force a rebuild of an unchanged summary (and, through the
// classify cache key, a full re-classification on every ask).
func deviceSnapshotKey(nodes []ledger.Node) string {
	stable := make([]ledger.Node, len(nodes))
	copy(stable, nodes)
	for i := range stable {
		stable[i].LastSeen = 0
		stable[i].Capacity.CurrentTasks = 0
		stable[i].Capacity.QueuedTasks = 0
		stable[i].Capacity.Live = nil
	}
	blob, err := json.Marshal(stable)
	if err != nil {
		// Unmarshalable nodes (never in practice): fall back to a stable
		// textual form so the key stays a pure function of the content.
		return hashString(fmt.Sprint(stable))
	}
	return hashString(string(blob))
}

// summarizeDevicesCached returns the device summary, reusing the previous
// rendering when the snapshot hash and locale are unchanged.
func summarizeDevicesCached(nodes []ledger.Node, loc ...i18n.Locale) string {
	targetLoc := i18n.English
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	key := deviceSnapshotKey(nodes) + ":" + string(targetLoc)
	deviceSummaryCache.mu.Lock()
	defer deviceSummaryCache.mu.Unlock()
	if key == deviceSummaryCache.key && deviceSummaryCache.result != "" {
		return deviceSummaryCache.result
	}
	s := summarizeDevices(nodes, targetLoc)
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
func summarizeDevices(nodes []ledger.Node, loc ...i18n.Locale) string {
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	if len(nodes) == 0 {
		return i18n.T(targetLoc, "prompt.device.summary.none")
	}
	var b strings.Builder
	for _, n := range nodes {
		var native []string
		for _, a := range n.Native {
			native = append(native, a.ID)
		}
		fmt.Fprintf(&b, "- %s (%s) native: %s\n", n.Name, n.Chip, strings.Join(native, ", "))
		if hw := describeHardware(n, targetLoc); hw != "" {
			hwLabel := i18n.T(targetLoc, "prompt.device.hardware.label")
			fmt.Fprintf(&b, "    %s: %s\n", hwLabel, hw)
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
				bestSep := ", "
				if targetLoc == i18n.ChineseSimp {
					bestSep = "、"
				}
				bestAtStr := strings.Join(ag.BestAt, bestSep)
				bestAtText := i18n.Tf(targetLoc, "prompt.device.agent.best_at", "skills", bestAtStr)
				desc += " " + bestAtText
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
func describeHardware(n ledger.Node, loc ...i18n.Locale) string {
	targetLoc := i18n.ChineseSimp
	if len(loc) > 0 && loc[0] != "" {
		targetLoc = loc[0]
	}
	var parts []string
	r := n.ResourceProfile
	if r.CPU > 0 {
		parts = append(parts, i18n.Tf(targetLoc, "prompt.device.hardware.cpu", "n", strconv.Itoa(r.CPU)))
	} else if n.Capacity.CPUCores > 0 {
		parts = append(parts, i18n.Tf(targetLoc, "prompt.device.hardware.cpu", "n", strconv.Itoa(n.Capacity.CPUCores)))
	}
	if r.RAMGB > 0 {
		parts = append(parts, i18n.Tf(targetLoc, "prompt.device.hardware.ram", "n", strconv.Itoa(r.RAMGB)))
	} else if n.Capacity.RAMGB > 0 {
		parts = append(parts, i18n.Tf(targetLoc, "prompt.device.hardware.ram", "n", strconv.Itoa(n.Capacity.RAMGB)))
	}
	if r.GPUVRAMGB > 0 {
		parts = append(parts, i18n.Tf(targetLoc, "prompt.device.hardware.gpu", "n", strconv.Itoa(r.GPUVRAMGB)))
	} else if r.Declared() {
		parts = append(parts, i18n.T(targetLoc, "prompt.device.hardware.undeclared"))
	}
	if !r.Declared() && len(parts) == 0 {
		return i18n.T(targetLoc, "prompt.device.hardware.no_profile")
	}
	if n.Capacity.MaxConcurrent > 0 {
		parts = append(parts, i18n.Tf(targetLoc, "prompt.device.hardware.max_concurrent", "n", strconv.Itoa(n.Capacity.MaxConcurrent)))
	}
	sep := ", "
	if targetLoc == i18n.ChineseSimp {
		sep = "，"
	}
	return strings.Join(parts, sep)
}
