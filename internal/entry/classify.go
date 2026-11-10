// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// classificationCacheNS is the DiskCache namespace for intent classification
// (answer / tool_call / task).
const classificationCacheNS = "classify"

// ToolNameTaskSubmit is the wire name of the native task-dispatch bridge
// tool — the call an entry model emits when a turn should become a task. It
// lives here rather than in askengine (which imports this package and builds
// the tool spec) so classification code can recognize the call without a
// package cycle, and so a rename has one edit point.
const ToolNameTaskSubmit = "task_submit"

// ToolNameTaskBudget is the wire name of the budget-declaration tool: the call
// a model makes when the round needs more dispatches than the engine's derived
// limit allows. Like task_submit it lives here so the tool loop can recognize
// the call by name without importing the assembly package.
const ToolNameTaskBudget = "task_budget"

// classificationCacheVersion tags every cache row with the semantics that
// produced it. Bump it whenever the key coverage, the cacheability rules or
// the resident prompt rules change, so rows written under the previous
// semantics can never be served (they expire by TTL). v3: coreRules gained
// the "Operating this node" routing guidance. v4: the key carries the
// workspace map and the acceptance shortlist. v5: both changes landed.
// v6: broad workspace investigation is routed to an evidence-backed agent
// task rather than cached as a conceptual answer.
// v7: task_kind persists the task-specific delivery evidence contract.
// v8: workspace classifications bypass cache until content revisions are keyed.
const classificationCacheVersion = "v8"

// classificationCacheable reports whether a classified Output may be replayed
// from the disk cache. Kinds that materialize durable work — a task row, or a
// plan and its stage rows — must never be replayed: the identical sentence
// would silently create the work twice. task_submit is the same hazard behind
// a tool call (it is the native bridge that creates a task), so calls naming
// it are stored live only as well.
func classificationCacheable(out Output) bool {
	switch out.Kind {
	case KindPlan, KindTask, KindClarify:
		// A clarify round is an interaction, not a verdict: replaying it from
		// cache would show a stale question (and options) for a sentence whose
		// workspace may have changed since.
		return false
	case KindToolCall:
		// A text-parsed tool call carries only Tool; the native path fills
		// both Tool and Tools.
		tools := out.Tools
		if len(tools) == 0 && out.Tool != nil {
			tools = []*ToolCall{out.Tool}
		}
		for _, t := range tools {
			if t != nil && t.Tool == ToolNameTaskSubmit {
				return false
			}
		}
	}
	return true
}

// Classify runs the unified entry model once with no tools and returns the
// parsed Output. It is the answer/fallback entry point; the tool path is
// ClassifyTurnsWithTools.
func Classify(ctx context.Context, c *Client, devices []ledger.Node, memory, user string) (Output, error) {
	return ClassifyTurnsWithTools(ctx, c, devices, memory, []Turn{{Role: "user", Content: user}}, nil)
}

// ClassifyTurns is Classify with a conversation history and no tools.
func ClassifyTurns(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn, opts ...ClassifyOption) (Output, error) {
	return ClassifyTurnsWithTools(ctx, c, devices, memory, turns, nil, opts...)
}

// classifyCacheKey builds the disk-cache key for one classification: k1 hashes
// the prompt side — model, request mode, locale, conversation turns, the
// memory summary, and the tool roster (the available tools shape the routing
// decision) — and k2 hashes the rendered device summary. Any input change
// lands on a different key and misses.
//
// The model name is part of the decision: a client switch (manual or failover
// fallback) must never serve the previous model's verdict. RequestMode/locale/
// ASCIIOnly shape the system prompt, so a /plan turn must not hit a row
// written by a plain turn (apply the ClassifyOptions before building the key).
// k2 hashes the device summary text the model actually sees rather than the
// raw node rows: LastSeen heartbeats and other unrendered live metrics churn
// the row every beat and would keep the cache a permanent miss.
func classifyCacheKey(po PromptOptions, model string, registry *Registry) (k1, k2 string) {
	var b strings.Builder
	b.WriteString(classificationCacheVersion)
	b.WriteString("\nmodel:")
	b.WriteString(model)
	b.WriteString("\nmode:")
	b.WriteString(po.RequestMode)
	b.WriteString("\nlocale:")
	b.WriteString(string(promptLocale(po)))
	if po.ASCIIOnly {
		b.WriteString("\nascii")
	}
	// A side channel renders a different system prompt for the same turns; its
	// verdict is not interchangeable with the main thread's.
	if po.Side {
		b.WriteString("\nside")
	}
	// The team pick is part of the system prompt, so two threads that differ
	// only in roster must not share a cached classification: one may legally
	// pin agent:opencode where the other must refuse it.
	if len(po.AgentRoster) > 0 {
		b.WriteString("\nroster:")
		b.WriteString(strings.Join(po.AgentRoster, ","))
	}
	b.WriteByte('\n')
	for _, t := range po.History {
		b.WriteString(t.Role)
		b.WriteByte('\n')
		b.WriteString(t.Content)
		if len(t.Blocks) > 0 {
			if blob, err := json.Marshal(t.Blocks); err == nil {
				b.Write(blob)
			}
		}
		b.WriteByte('\n')
	}
	b.WriteString("memory:\n")
	b.WriteString(po.Memory)
	// The acceptance shortlist and the workspace map are both prompt content:
	// two conversations with the same history in DIFFERENT workspaces must
	// not share a cached classification, or the second one can select a
	// check id its tree does not have.
	for _, c := range po.AcceptanceCandidates {
		b.WriteString("\naccept:")
		b.WriteString(c.ID)
		b.WriteByte('=')
		b.WriteString(c.Run)
	}
	if po.WorkspaceMap != "" {
		b.WriteString("\nworkspace:\n")
		b.WriteString(po.WorkspaceMap)
	}
	if registry != nil {
		for _, s := range registry.Specs() {
			b.WriteString("\ntool:")
			b.WriteString(s.Name)
		}
	}
	return hashString(b.String()), hashString(summarizeDevicesCached(po.Devices, promptLocale(po)))
}

// A map identifies structure, not the revision of its files. Workspace
// answers and follow-up tool choices must be recomputed after edits, including
// uncommitted edits. Do not pretend a path-only cache key proves freshness.
func liveWorkspaceClassification(po PromptOptions, registry *Registry) bool {
	if po.WorkspaceMap != "" {
		return true
	}
	if registry != nil {
		_, ok := registry.Lookup("workspace_read")
		return ok
	}
	return false
}

// cachedClassification returns a previously stored classification for these
// exact inputs, if the client carries a disk cache and one exists.
func cachedClassification(ctx context.Context, c *Client, po PromptOptions, registry *Registry) (Output, bool) {
	dc := c.diskCache()
	if dc == nil || liveWorkspaceClassification(po, registry) {
		return Output{}, false
	}
	k1, k2 := classifyCacheKey(po, c.ModelName(), registry)
	var out Output
	if !dc.Get(ctx, classificationCacheNS, k1, k2, &out) {
		return Output{}, false
	}
	// Defense in depth against rows written before a cacheability rule
	// tightened: a side-effectful verdict is treated as a miss.
	if !classificationCacheable(out) {
		return Output{}, false
	}
	// Never serve a cached answer that carries textual tool-call markup: such
	// rows predate the recover/reject paths (resolveDSML, resolveXMLToolCall)
	// and would re-leak the raw protocol text to the user on every identical
	// ask.
	if out.Kind == KindAnswer && ContainsTextualToolCall(out.Answer) {
		return Output{}, false
	}
	return out, true
}

// storeClassification saves a successful classification for reuse. Best-effort:
// a failed write only costs a future miss.
func storeClassification(ctx context.Context, c *Client, po PromptOptions, registry *Registry, out Output) {
	dc := c.diskCache()
	if dc == nil || liveWorkspaceClassification(po, registry) {
		return
	}
	if !classificationCacheable(out) {
		return
	}
	k1, k2 := classifyCacheKey(po, c.ModelName(), registry)
	dc.Put(ctx, classificationCacheNS, k1, k2, out)
}

// ClassifyTurnsWithTools runs the entry model with a conversation history and a
// tool registry. A native tool_use response becomes a KindToolCall output; a
// text response falls through to the existing JSON/prose parsing (answer/task).
// Identical inputs (turns, memory, devices, tool roster) are served from the
// disk cache without an LLM call.
func ClassifyTurnsWithTools(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn, registry *Registry, opts ...ClassifyOption) (Output, error) {
	po := PromptOptions{Devices: devices, Memory: memory, History: turns}
	for _, o := range opts {
		o(&po)
	}
	if out, ok := cachedClassification(ctx, c, po, registry); ok {
		return out, nil
	}
	system := BuildPrompt(po)
	var specs []ToolSpec
	if registry != nil {
		specs = registry.Specs()
	}
	resp, err := c.CompleteTurnsWithTools(ctx, system, turns, specs)
	if err == nil && emptyCompletion(resp) {
		resp, err = c.CompleteTurnsWithTools(ctx, system, turns, specs)
	}
	if err != nil {
		return Output{}, WrapAPIError(err, po.UserLocale)
	}
	out, err := resolveResponse(resp, len(specs) > 0, po.UserLocale)
	if err != nil {
		return Output{}, err
	}
	storeClassification(ctx, c, po, registry, out)
	return out, nil
}

// ClassifyStreamWithTools is ClassifyTurnsWithTools with live text streaming:
// answer deltas are delivered to onDelta as the provider emits them, while the
// parsed Output is returned once complete. Chain-of-thought from a provider's
// separate reasoning field is delivered to onReasoning live (display-only, kept
// out of Output per D14); pass nil to ignore it. A cache hit delivers the
// stored answer as one delta; structured outputs (task/tool_call) never stream,
// same as the live path.
func ClassifyStreamWithTools(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn, registry *Registry, onDelta func(string), onReasoning func(string), opts ...ClassifyOption) (Output, error) {
	po := PromptOptions{Devices: devices, Memory: memory, History: turns}
	for _, o := range opts {
		o(&po)
	}
	if out, ok := cachedClassification(ctx, c, po, registry); ok {
		if out.Kind == KindAnswer && onDelta != nil && out.Answer != "" {
			onDelta(out.Answer)
		}
		return out, nil
	}
	system := BuildPrompt(po)
	var specs []ToolSpec
	if registry != nil {
		specs = registry.Specs()
	}
	var streamOpts []StreamOption
	if po.UsageObserver != nil {
		streamOpts = append(streamOpts, WithUsageObserver(po.UsageObserver))
	}
	resp, err := c.StreamTurnsWithTools(ctx, system, turns, specs, onDelta, onReasoning, streamOpts...)
	if err == nil && emptyCompletion(resp) {
		// Nothing streamed (Text is empty by definition here), so replaying
		// the call cannot double-display deltas on the user's screen.
		resp, err = c.StreamTurnsWithTools(ctx, system, turns, specs, onDelta, onReasoning, streamOpts...)
	}
	if err != nil {
		return Output{}, WrapAPIError(err, po.UserLocale)
	}
	out, err := resolveResponse(resp, len(specs) > 0, po.UserLocale)
	if err != nil {
		return Output{}, err
	}
	storeClassification(ctx, c, po, registry, out)
	return out, nil
}

// emptyCompletion reports a response that carries nothing ParseOutput could
// work with: no tool calls and no text. Providers occasionally answer 200-OK
// with an empty content field — a transient hiccup (or a reasoning-only turn
// whose chain-of-thought lives in the separate reasoning field, per D14), not
// a real verdict. Retrying once rides it out instead of letting the empty
// body surface as a validation error that kills the whole turn.
func emptyCompletion(resp Response) bool {
	return len(resp.ToolUses) == 0 && strings.TrimSpace(resp.Text) == ""
}

// resolveResponse routes one completed model response: a native tool_use is
// authoritative (route to the registry); text falls through to the DSML
// recover/reject path (compatible endpoints that emit tool calls as markup in
// the content field) and then to the JSON/prose parser (answer/task).
// toolsOffered tells the DSML path whether this call carried a tool roster —
// a recovered call may only run when the model could legitimately have made
// one.
func resolveResponse(resp Response, toolsOffered bool, locs ...i18n.Locale) (Output, error) {
	loc := i18n.Locale("")
	if len(locs) > 0 {
		loc = locs[0]
	}
	// A tool_use is authoritative: the model chose controlled tools, so route
	// every call to the registry rather than the text parser. All of them are
	// returned in Tools — executing just the first made one round trip per
	// call, which burned the whole round budget on a single batch intent
	// ("clean the queue" cancelling N tasks = N rounds).
	if len(resp.ToolUses) > 0 {
		out := Output{Kind: KindToolCall}
		for _, tu := range resp.ToolUses {
			out.Tools = append(out.Tools, &ToolCall{ID: tu.ID, Tool: tu.Name, Arguments: tu.Input})
		}
		out.Tool = out.Tools[0]
		if note := droppedToolNote(resp); note != "" {
			out.Note = note
		}
		return out, nil
	}
	if ContainsDSMLToolCall(resp.Text) {
		return resolveDSML(resp, toolsOffered, loc)
	}
	// A second dialect of the same failure: GLM's <tool_call> template. Both
	// are protocol artifacts, never answer prose, and the recovery contract is
	// identical — see texttool.go.
	if strings.Contains(resp.Text, xmlToolOpen) {
		return resolveXMLToolCall(resp, toolsOffered, loc)
	}
	out, err := ParseOutput(resp.Text)
	if err != nil {
		// A validation failure on a structured output is a model error; surface
		// it rather than degrading silently, so the user can retry.
		return Output{}, &ClassifyError{
			UserMsg: i18n.Tf(loc, "entry.err.validation", "err", err.Error()),
			Err:     err,
		}
	}
	if out.Kind == KindAnswer && resp.Truncated {
		// The provider stopped at max_tokens; mark the answer so the user knows
		// it is incomplete rather than silently passing a cut-off reply through.
		out.Answer += i18n.T(loc, "entry.answer.truncated")
	}
	return out, nil
}

// droppedToolNote captures the text the model emitted alongside its tool
// call(s) so the ask loop can surface it to the user and replay it to the
// model instead of silently discarding it. The calls themselves are no longer
// part of the note — every one is executed in the same round.
func droppedToolNote(resp Response) string {
	return strings.TrimSpace(resp.Text)
}
