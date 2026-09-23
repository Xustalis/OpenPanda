package entry

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// classificationCacheNS is the DiskCache namespace for intent classification
// (answer / tool_call / task).
const classificationCacheNS = "classify"

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
// the prompt side — conversation turns, the memory summary, and the tool
// roster (the available tools shape the routing decision) — and k2 hashes the
// device snapshot. Any input change lands on a different key and misses.
func classifyCacheKey(turns []Turn, memory string, devices []ledger.Node, registry *Registry) (k1, k2 string) {
	var b strings.Builder
	for _, t := range turns {
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
	b.WriteString(memory)
	if registry != nil {
		for _, s := range registry.Specs() {
			b.WriteString("\ntool:")
			b.WriteString(s.Name)
		}
	}
	return hashString(b.String()), deviceSnapshotKey(devices)
}

// cachedClassification returns a previously stored classification for these
// exact inputs, if the client carries a disk cache and one exists.
func cachedClassification(ctx context.Context, c *Client, turns []Turn, memory string, devices []ledger.Node, registry *Registry) (Output, bool) {
	dc := c.diskCache()
	if dc == nil {
		return Output{}, false
	}
	k1, k2 := classifyCacheKey(turns, memory, devices, registry)
	var out Output
	if !dc.Get(ctx, classificationCacheNS, k1, k2, &out) {
		return Output{}, false
	}
	// Never serve a cached answer that carries DSML tool-call markup: such rows
	// predate the recover/reject path (resolveDSML) and would re-leak the raw
	// protocol text to the user on every identical ask.
	if out.Kind == KindAnswer && ContainsDSMLToolCall(out.Answer) {
		return Output{}, false
	}
	return out, true
}

// storeClassification saves a successful classification for reuse. Best-effort:
// a failed write only costs a future miss.
func storeClassification(ctx context.Context, c *Client, turns []Turn, memory string, devices []ledger.Node, registry *Registry, out Output) {
	dc := c.diskCache()
	if dc == nil {
		return
	}
	k1, k2 := classifyCacheKey(turns, memory, devices, registry)
	dc.Put(ctx, classificationCacheNS, k1, k2, out)
}

// ClassifyTurnsWithTools runs the entry model with a conversation history and a
// tool registry. A native tool_use response becomes a KindToolCall output; a
// text response falls through to the existing JSON/prose parsing (answer/task).
// Identical inputs (turns, memory, devices, tool roster) are served from the
// disk cache without an LLM call.
func ClassifyTurnsWithTools(ctx context.Context, c *Client, devices []ledger.Node, memory string, turns []Turn, registry *Registry, opts ...ClassifyOption) (Output, error) {
	if out, ok := cachedClassification(ctx, c, turns, memory, devices, registry); ok {
		return out, nil
	}
	po := PromptOptions{Devices: devices, Memory: memory, History: turns}
	for _, o := range opts {
		o(&po)
	}
	system := BuildPrompt(po)
	var specs []ToolSpec
	if registry != nil {
		specs = registry.Specs()
	}
	resp, err := c.CompleteTurnsWithTools(ctx, system, turns, specs)
	if err != nil {
		return Output{}, WrapAPIError(err)
	}
	out, err := resolveResponse(resp, len(specs) > 0)
	if err != nil {
		return Output{}, err
	}
	storeClassification(ctx, c, turns, memory, devices, registry, out)
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
	if out, ok := cachedClassification(ctx, c, turns, memory, devices, registry); ok {
		if out.Kind == KindAnswer && onDelta != nil && out.Answer != "" {
			onDelta(out.Answer)
		}
		return out, nil
	}
	po := PromptOptions{Devices: devices, Memory: memory, History: turns}
	for _, o := range opts {
		o(&po)
	}
	system := BuildPrompt(po)
	var specs []ToolSpec
	if registry != nil {
		specs = registry.Specs()
	}
	resp, err := c.StreamTurnsWithTools(ctx, system, turns, specs, onDelta, onReasoning)
	if err != nil {
		return Output{}, WrapAPIError(err)
	}
	out, err := resolveResponse(resp, len(specs) > 0)
	if err != nil {
		return Output{}, err
	}
	storeClassification(ctx, c, turns, memory, devices, registry, out)
	return out, nil
}

// resolveResponse routes one completed model response: a native tool_use is
// authoritative (route to the registry); text falls through to the DSML
// recover/reject path (compatible endpoints that emit tool calls as markup in
// the content field) and then to the JSON/prose parser (answer/task).
// toolsOffered tells the DSML path whether this call carried a tool roster —
// a recovered call may only run when the model could legitimately have made
// one.
func resolveResponse(resp Response, toolsOffered bool) (Output, error) {
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
		return resolveDSML(resp, toolsOffered)
	}
	out, err := ParseOutput(resp.Text)
	if err != nil {
		// A validation failure on a structured output is a model error; surface
		// it rather than degrading silently, so the user can retry.
		return Output{}, &ClassifyError{
			UserMsg: "模型输出校验失败：" + err.Error(),
			Err:     err,
		}
	}
	if out.Kind == KindAnswer && resp.Truncated {
		// The provider stopped at max_tokens; mark the answer so the user knows
		// it is incomplete rather than silently passing a cut-off reply through.
		out.Answer += "\n\n[回答因长度上限被截断]"
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
