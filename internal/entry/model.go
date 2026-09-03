package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/providers"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// anthropicVersion is the header required by Anthropic-compatible endpoints.
const anthropicVersion = "2023-06-01"

// defaultModel is the entry model used when the config names none.
// deepseek-chat/deepseek-reasoner were deprecated aliases (retired by
// DeepSeek on 2026-07-24); deepseek-v4-flash is the successor default. The
// default BaseURL stays the Anthropic-compatible endpoint.
const defaultModel = "deepseek-v4-flash"

// defaultMaxTokens is the completion cap when the config does not specify one.
// It is high enough that a normal answer is not silently truncated (the previous
// hardcoded 1024 was), while still bounding a runaway generation.
const defaultMaxTokens = 4096

// Client talks to an Anthropic-compatible Messages API or an OpenAI-compatible
// Chat Completions API, selected by the config's api_type. It is small and
// dependency-free so the core daemon does not pull in an SDK.
type Client struct {
	apiType   string
	baseURL   string
	apiKey    string
	model     string
	maxTokens int
	hc        *http.Client
	// hcStream serves streaming requests. It deliberately has no
	// http.Client.Timeout — that clock covers the whole response body, so any
	// value large enough for a long generation is useless as a liveness check,
	// and any value small enough to be useful truncates a legitimate stream
	// mid-token. Liveness comes from streamTransport's per-phase deadlines plus
	// the caller's context.
	hcStream  *http.Client
	maxRetry  int
	retryBase time.Duration
	// promptCache toggles provider-native prompt-cache markers on outgoing
	// requests (Anthropic cache_control breakpoints / OpenAI
	// prompt_cache_key). Default on: the markers are hints a provider is free
	// to ignore — DeepSeek's Anthropic endpoint silently drops cache_control —
	// so the flag only ever changes what the provider may reuse, never
	// correctness.
	promptCache atomic.Bool
	// passback records a provider that demands the thinking passback on
	// multi-turn assistant messages (DeepSeek in thinking mode rejects the
	// history with a 400 "must be passed back" otherwise). Once observed the
	// flag sticks for the client's lifetime, so later calls satisfy the
	// requirement up front instead of spending a rejected round-trip each.
	passback atomic.Bool
	// cache is the optional disk cache for entry-model decisions. Nil (the
	// default) disables it; the engine attaches one over the node database.
	cache atomic.Pointer[DiskCache]
	// usageIn/usageOut accumulate the provider-reported token consumption of
	// every successful call, so callers (the metrics path) can bill the
	// commander model's own cost without threading usage through every
	// signature.
	usageIn  atomic.Int64
	usageOut atomic.Int64

	// provider is the built-in catalogue id (internal/providers) when the
	// config named one; empty for a bare custom endpoint. It drives per-vendor
	// tuning at construction time.
	provider string
	// modelsURL is the list-models endpoint resolved at construction: an
	// absolute override when the provider carries one (DeepSeek's list lives
	// on its OpenAI surface), empty to derive from api_type at call time.
	modelsURL string
	// noAuth marks a provider that needs no API key (Ollama / local vLLM), so
	// ListModels does not refuse an empty key.
	noAuth bool
	// contextWindow is the advertised context length in tokens; 0 when unknown.
	contextWindow int
}

// NewClient builds a client from the model config. A zero baseURL/model falls
// back to config defaults, so callers can pass config.Default().Model. The
// endpoint must be HTTPS so the API key never travels cleartext (M2); loopback
// http stays allowed for a local dev model, matching the guard the commander
// applies to adapter endpoints (D7).
func NewClient(model config.ModelConfig) (*Client, error) {
	p, hasProvider := providers.Lookup(model.Provider)
	base := model.BaseURL
	if base == "" {
		if hasProvider && p.BaseURL != "" {
			base = p.BaseURL
		} else {
			base = "https://api.deepseek.com/anthropic"
		}
	}
	if err := security.NewNetworkGuard(security.EndpointHost(base)).CheckURL(base); err != nil {
		return nil, err
	}
	name := model.Model
	if name == "" {
		if hasProvider && p.DefaultModel != "" {
			name = p.DefaultModel
		} else {
			name = defaultModel
		}
	}
	maxTokens := model.MaxTokens
	if maxTokens <= 0 {
		if hasProvider && p.DefaultMaxTokens > 0 {
			maxTokens = p.DefaultMaxTokens
		} else {
			maxTokens = defaultMaxTokens
		}
	}
	contextWindow := model.ContextWindow
	if contextWindow <= 0 && hasProvider {
		contextWindow = p.ContextWindow
	}
	apiType := strings.ToLower(strings.TrimSpace(model.APIType))
	if apiType == "" && hasProvider {
		apiType = strings.ToLower(strings.TrimSpace(p.APIType))
	}
	if apiType != config.APITypeOpenAI {
		apiType = config.APITypeAnthropic
	}
	c := &Client{
		apiType:       apiType,
		baseURL:       strings.TrimRight(base, "/"),
		apiKey:        model.APIKey,
		model:         name,
		maxTokens:     maxTokens,
		contextWindow: contextWindow,
		hc:            &http.Client{Timeout: 30 * time.Second},
		hcStream:      &http.Client{Transport: streamTransport()},
		maxRetry:      2,
		retryBase:     500 * time.Millisecond,
	}
	c.promptCache.Store(true)
	if hasProvider {
		c.provider = p.ID
		c.noAuth = p.NoAuth
		if p.ModelsPath != "" {
			// Resolve against the *effective* base (the user's configured
			// base_url, not the provider's catalogue default): a provider
			// with a custom endpoint (Ollama on another port) must list
			// models on that endpoint, not the shipped one.
			c.modelsURL = resolveModelsURL(base, p.ModelsPath)
		}
		if !p.PromptCache {
			c.promptCache.Store(false)
		}
		if p.ThinkingPassback {
			c.passback.Store(true)
		}
	}
	// Legacy heuristics stay as a backstop for configs without a provider id.
	if isDeepSeekModel(name) || isDeepSeekEndpoint(base) {
		c.passback.Store(true)
	}
	return c, nil
}

// resolveModelsURL turns a provider's list-models path into a concrete URL:
// an absolute http(s) path is used verbatim (DeepSeek's list lives on its
// OpenAI surface), a relative path is joined onto the endpoint base.
func resolveModelsURL(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return strings.TrimRight(strings.TrimSpace(path), "/")
	}
	return base + "/" + strings.TrimLeft(path, "/")
}

func isDeepSeekEndpoint(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	return strings.Contains(lower, "deepseek.com") || lower == ""
}

func isDeepSeekModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "deepseek") || strings.Contains(lower, "reasoner")
}

// streamTransport bounds every phase of a streaming request that can hang
// *before* tokens start flowing, without bounding the stream itself: TCP connect,
// TLS handshake, and the wait for response headers. A model endpoint that
// blackholes the connection — a wrong host, a dropped route mid-flight on a
// multi-device network — used to hang the caller indefinitely whenever its
// context carried no deadline, and the entry model sits on the critical path of
// every classify/answer, so that hang stalls the node rather than one request.
// Once headers arrive, nothing here limits how long the body may take.
func streamTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		// The gap between "request sent" and "first header byte" is the
		// provider's queue + prefill. Generous, but finite.
		ResponseHeaderTimeout: 120 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   2,
	}
}

// SetPromptCaching toggles provider-native prompt-cache markers on outgoing
// requests. Enabled by default; see Client.promptCache.
func (c *Client) SetPromptCaching(enabled bool) { c.promptCache.Store(enabled) }

// SetDiskCache attaches the disk cache used for entry-model decision reuse
// (classify / supervise). A nil cache disables it.
func (c *Client) SetDiskCache(dc *DiskCache) { c.cache.Store(dc) }

// diskCache returns the attached cache, or nil when none was attached.
func (c *Client) diskCache() *DiskCache { return c.cache.Load() }

// ModelName returns the configured model id, for metric labels.
func (c *Client) ModelName() string { return c.model }

// Provider returns the provider catalogue ID, or empty for a custom endpoint.
func (c *Client) Provider() string { return c.provider }

// ContextWindow returns the advertised context length in tokens, or 0 when unknown.
func (c *Client) ContextWindow() int { return c.contextWindow }

// Usage is the provider-reported token consumption of one or more calls.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Total is the combined input+output token count.
func (u Usage) Total() int64 { return u.InputTokens + u.OutputTokens }

// Sub returns u minus other, clamped at zero per component — the consumption
// that happened between two Usage snapshots.
func (u Usage) Sub(other Usage) Usage {
	return Usage{
		InputTokens:  max(u.InputTokens-other.InputTokens, 0),
		OutputTokens: max(u.OutputTokens-other.OutputTokens, 0),
	}
}

// Usage returns the client's cumulative token consumption across every
// successful call since construction. Providers that do not report usage
// contribute zero.
func (c *Client) Usage() Usage {
	return Usage{InputTokens: c.usageIn.Load(), OutputTokens: c.usageOut.Load()}
}

// addUsage accumulates one reported usage block.
func (c *Client) addUsage(in, out int64) {
	if in > 0 {
		c.usageIn.Add(in)
	}
	if out > 0 {
		c.usageOut.Add(out)
	}
}

// messagesRequest is the Anthropic Messages API request body. System is
// either a plain string or a []systemBlock when prompt-cache markers are on
// (see systemPayload).
type messagesRequest struct {
	Model     string     `json:"model"`
	MaxTokens int        `json:"max_tokens"`
	Stream    bool       `json:"stream,omitempty"`
	System    any        `json:"system,omitempty"`
	Messages  []message  `json:"messages"`
	Tools     []ToolSpec `json:"tools,omitempty"`
}

// systemBlock is one Anthropic system content block. cache_control marks a
// prompt-cache breakpoint: providers that support it cache everything up to
// and including the block; providers that do not (e.g. DeepSeek's Anthropic
// endpoint) ignore the marker without erroring.
type systemBlock struct {
	Type         string       `json:"type"`
	Text         string       `json:"text"`
	CacheControl *cacheMarker `json:"cache_control,omitempty"`
}

// cacheMarker is the Anthropic prompt-cache breakpoint directive.
type cacheMarker struct {
	Type string `json:"type"` // "ephemeral"
}

// systemPayload renders the system prompt for the Anthropic wire format.
// With prompt caching on, the prompt splits at the memory-section marker and
// the stable prefix (rules + devices) carries a cache_control breakpoint, so
// the provider reuses that prefix across the calls of one conversation
// instead of re-billing it every time. With caching off the system stays a
// plain string — the escape hatch for providers that reject block arrays.
func (c *Client) systemPayload(system string) any {
	if system == "" {
		return nil
	}
	if !c.promptCache.Load() {
		return system
	}
	stable, volatile := splitPromptSections(system)
	marked := systemBlock{Type: "text", Text: stable, CacheControl: &cacheMarker{Type: "ephemeral"}}
	if volatile == "" {
		return []systemBlock{marked}
	}
	return []systemBlock{marked, {Type: "text", Text: volatile}}
}

// oaiPromptCacheKey derives the OpenAI prompt_cache_key — a routing hint
// that keeps requests sharing a conversation skeleton on the same cache
// shard — from the stable prompt prefix (rules + devices). It is stable
// across the calls of one conversation and changes when the device snapshot
// changes.
func (c *Client) oaiPromptCacheKey(system string) string {
	stable, _ := splitPromptSections(system)
	return hashString(stable)[:32]
}

// message is one conversation message. Content is either a plain string (the
// common case) or a []ContentBlock when the turn carries tool_use/tool_result.
type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// ContentBlock is a structured content block in a message or response. It
// covers the Messages API vocabulary PANDA uses: text/thinking (responses) and
// tool_use/tool_result (both directions).
type ContentBlock struct {
	Type      string         `json:"type"`                  // text | thinking | tool_use | tool_result
	Text      string         `json:"text,omitempty"`        // text
	Thinking  string         `json:"thinking,omitempty"`    // thinking content (Anthropic wire format)
	Signature string         `json:"signature,omitempty"`   // thinking signature
	ID        string         `json:"id,omitempty"`          // tool_use id
	Name      string         `json:"name,omitempty"`        // tool_use name
	Input     map[string]any `json:"input,omitempty"`       // tool_use input
	ToolUseID string         `json:"tool_use_id,omitempty"` // tool_result linkage
	Content   string         `json:"content,omitempty"`     // tool_result content
}

// MarshalJSON emits only the fields each block type carries on the wire.
// Crucially, a tool_use block ALWAYS carries "input" (an empty object for a
// no-argument tool): the Anthropic Messages schema requires the field, and
// strict Anthropic-compatible providers (e.g. DeepSeek's /anthropic endpoint)
// reject the request with a 400 when map omitempty drops the empty input.
// Similarly, a thinking block ALWAYS carries "thinking" (not "text"), as
// strict Anthropic-compatible providers reject with "missing field `thinking`"
// otherwise.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	m := map[string]any{"type": b.Type}
	switch b.Type {
	case "tool_use":
		m["id"] = b.ID
		m["name"] = b.Name
		if b.Input == nil {
			m["input"] = map[string]any{}
		} else {
			m["input"] = b.Input
		}
	case "tool_result":
		m["tool_use_id"] = b.ToolUseID
		m["content"] = b.Content
	case "thinking":
		thinking := b.Thinking
		if thinking == "" {
			thinking = b.Text
		}
		if thinking == "" {
			thinking = " "
		}
		m["thinking"] = thinking
		if b.Signature != "" {
			m["signature"] = b.Signature
		}
	default: // text
		m["text"] = b.Text
	}
	return json.Marshal(m)
}

// ToolSpec describes one tool the model may call (Anthropic Messages API shape).
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// ToolUse is one tool invocation the model emitted in a response.
type ToolUse struct {
	ID    string
	Name  string
	Input map[string]any
}

// Response is the parsed result of one completion call: the textual answer plus
// any tool_use blocks the model emitted (a model may return text alongside a
// tool call, or only a tool call). Truncated reports that the provider stopped
// at max_tokens, so the text is incomplete.
type Response struct {
	Text      string
	ToolUses  []ToolUse
	Truncated bool
	// Reasoning is the model's chain-of-thought captured from a SEPARATE wire
	// field — Anthropic thinking blocks, OpenAI-compat delta.reasoning_content —
	// never from inlined <think> tags in Text. It is display-only scratch: the
	// streaming path surfaces it live via a reasoning sink, and it is kept out
	// of Text and out of session history (D14). Callers must not persist it.
	Reasoning string
}

// messagesResponse is the Messages API response body.
type messagesResponse struct {
	Content    []ContentBlock  `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      *anthropicUsage `json:"usage,omitempty"`
	Error      *apiError       `json:"error,omitempty"`
}

// anthropicUsage is the Messages API token-usage block.
type anthropicUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ErrNoKey is returned when no API key is configured.
var ErrNoKey = errors.New("entry: no model api_key configured")

// Turn is one conversation turn for multi-turn classification.
type Turn struct {
	Role    string         // "user" | "assistant"
	Content string         // plain text (used when Blocks is empty)
	Blocks  []ContentBlock // structured blocks for tool_use/tool_result
}

// Complete runs one non-streaming call and returns the model's text.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	return c.CompleteTurns(ctx, system, []Turn{{Role: "user", Content: user}})
}

// CompleteTurns runs one call with a conversation history and no tools,
// returning the model's text. It is the answer/fallback path; the tool path is
// CompleteTurnsWithTools.
func (c *Client) CompleteTurns(ctx context.Context, system string, turns []Turn) (string, error) {
	resp, err := c.CompleteTurnsWithTools(ctx, system, turns, nil)
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// CompleteTurnsWithTools runs one call with a conversation history and an
// optional tool set, returning both text and any tool_use blocks. It dispatches
// on the configured api type; the Anthropic path leaves tool_choice unset: the
// Messages API defaults to "auto" when tools are present, and DeepSeek's
// Anthropic endpoint rejects the string "auto" (it wants the internally-tagged
// object form). Omitting it is simpler and correct.
func (c *Client) CompleteTurnsWithTools(ctx context.Context, system string, turns []Turn, tools []ToolSpec) (Response, error) {
	if c.apiKey == "" {
		// Same guard as the streaming and OpenAI paths: an empty key would
		// otherwise hit the provider and come back as a misleading 401
		// "invalid key" instead of "not configured".
		return Response{}, ErrNoKey
	}
	if c.apiType == config.APITypeOpenAI {
		return c.completeOpenAI(ctx, system, turns, tools)
	}
	req := messagesRequest{Model: c.model, MaxTokens: c.maxTokens, System: c.systemPayload(system), Messages: turnsToMessages(turns)}
	if len(tools) > 0 {
		req.Tools = tools
	}
	return c.completeWithRetry(ctx, req)
}

// turnsToMessages converts the internal Turn history into Messages API
// messages: a Turn with Blocks carries structured content, a plain Turn a
// string content.
func turnsToMessages(turns []Turn) []message {
	turns = normalizeTurns(turns)
	msgs := make([]message, len(turns))
	for i, t := range turns {
		if len(t.Blocks) > 0 {
			msgs[i] = message{Role: t.Role, Content: t.Blocks}
		} else {
			msgs[i] = message{Role: t.Role, Content: t.Content}
		}
	}
	return msgs
}

// normalizeTurns merges consecutive plain-text turns of the same role into
// one so the wire payload satisfies the role-alternation contract strict
// Messages-API providers enforce with a 400 (a session replay that doubles
// or dangles a user turn would otherwise poison the whole conversation).
// Block-bearing turns — the tool_use/tool_result contract — pass through
// untouched: their shape is exact by construction, and merging across block
// kinds would corrupt the linkage.
func normalizeTurns(turns []Turn) []Turn {
	out := make([]Turn, 0, len(turns))
	for _, t := range turns {
		if len(t.Blocks) == 0 && len(out) > 0 {
			last := &out[len(out)-1]
			if last.Role == t.Role && len(last.Blocks) == 0 {
				switch {
				case last.Content == "":
					last.Content = t.Content
				case t.Content != "":
					last.Content += "\n\n" + t.Content
				}
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// completeOpenAI runs one non-streaming Chat Completions call with retry. The
// thinking-passback probe mirrors completeWithRetry: a rejection sets the
// sticky flag and retries the corrected payload off the transport budget.
func (c *Client) completeOpenAI(ctx context.Context, system string, turns []Turn, tools []ToolSpec) (Response, error) {
	if c.apiKey == "" {
		return Response{}, ErrNoKey
	}
	msgs := turnsToOpenAI(system, turns)
	if c.passback.Load() {
		msgs = injectReasoningPassback(msgs)
	}
	var lastErr error
	probed := false
	for attempt := 0; attempt <= c.maxRetry; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.retryBase<<uint(attempt-1)); err != nil {
				return Response{}, err
			}
		}
		resp, err := c.completeOnceOpenAI(ctx, system, msgs, tools)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !probed && passbackRequired(err) {
			probed = true
			c.passback.Store(true)
			msgs = injectReasoningPassback(msgs)
			attempt-- // the correction retries at once, off the transport budget
			continue
		}
		if !retryable(err) {
			break
		}
	}
	return Response{}, lastErr
}

func (c *Client) completeOnceOpenAI(ctx context.Context, system string, msgs []oaiMessage, tools []ToolSpec) (Response, error) {
	req := buildOAIRequest(c.model, c.maxTokens, false, msgs, tools, "")
	if c.promptCache.Load() {
		req.PromptCacheKey = c.oaiPromptCacheKey(system)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("entry: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIURL(c.baseURL), bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+c.apiKey)

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("entry: request: %w", err)
		}
		return Response{}, &transientError{err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, &transientError{err: fmt.Errorf("read response: %w", err)}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return Response{}, &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, &statusError{status: resp.StatusCode, body: string(body)}
	}

	var or oaiResponse
	if err := json.Unmarshal(body, &or); err != nil {
		return Response{}, fmt.Errorf("entry: parse response: %w", err)
	}
	if or.Error != nil {
		return Response{}, fmt.Errorf("entry: api error: %s", or.Error.Message)
	}
	if or.Usage != nil {
		c.addUsage(or.Usage.PromptTokens, or.Usage.CompletionTokens)
	}
	return parseOpenAIResponse(&or), nil
}

// openAIURL normalizes an OpenAI-compatible base URL into the full chat completions endpoint.
func openAIURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return "https://api.openai.com/v1/chat/completions"
	}
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") || strings.HasSuffix(base, "/v4") || strings.HasSuffix(base, "/v2") || strings.HasSuffix(base, "/v3") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// anthropicURL normalizes an Anthropic-compatible base URL into the full messages endpoint.
func anthropicURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return "https://api.deepseek.com/anthropic/v1/messages"
	}
	if strings.HasSuffix(base, "/messages") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

// passbackRequired reports whether err is the provider's demand for the
// thinking passback: a definitive 400 whose body names the requirement.
// Two wording families are matched:
//
//   - Provider-native prose: Anthropic's "content[].thinking … must be
//     passed back to the API" and Chat Completions' "reasoning_content …
//     must be passed back".
//   - Strict-deserializer reports (Rust serde, typed relays):
//     "missing field `thinking`" (Messages wire, assistant block arrays
//     lack the thinking block) and "missing field `reasoning_content`"
//     (Chat Completions wire, assistant object lacks the passback field).
//
// Both shapes mean the same thing: the history must gain a thinking /
// reasoning_content placeholder on every assistant turn before retrying.
func passbackRequired(err error) bool {
	var se *statusError
	if !errors.As(err, &se) || se.status != http.StatusBadRequest {
		return false
	}
	body := se.body
	// Canonical provider wording (DeepSeek's documented rejection).
	if strings.Contains(body, "must be passed back") {
		return true
	}
	// Strict-deserializer variants — case-insensitive because backticks,
	// straight quotes and casing differ across relays.
	lower := strings.ToLower(body)
	return strings.Contains(lower, "missing field") &&
		(strings.Contains(lower, "thinking") || strings.Contains(lower, "reasoning_content"))
}

// injectThinkingPassback satisfies a thinking-passback provider: every
// assistant message gains a leading thinking block with a one-space
// placeholder. The genuine chain-of-thought is display-only scratch the
// engine never persists (D14), so a placeholder stands in; providers that
// demand the passback accept it and the history round-trips. Assistant turns
// that already carry a thinking block and every other role pass through
// untouched.
func injectThinkingPassback(msgs []message) []message {
	out := make([]message, len(msgs))
	for i, m := range msgs {
		if m.Role != "assistant" {
			out[i] = m
			continue
		}
		var blocks []ContentBlock
		switch c := m.Content.(type) {
		case string:
			blocks = []ContentBlock{{Type: "text", Text: c}}
		case []ContentBlock:
			blocks = c
		default:
			out[i] = m
			continue
		}
		if len(blocks) > 0 && blocks[0].Type == "thinking" {
			out[i] = m
			continue
		}
		injected := make([]ContentBlock, 0, len(blocks)+1)
		injected = append(injected, ContentBlock{Type: "thinking", Thinking: " ", Text: " "})
		out[i] = message{Role: m.Role, Content: append(injected, blocks...)}
	}
	return out
}

// completeWithRetry runs req with the configured retry/backoff and returns the
// parsed response. A thinking-passback rejection is probed once: the flag is
// set, the assistant history gains the placeholder passback, and the corrected
// payload retries immediately without consuming the transport retry budget.
func (c *Client) completeWithRetry(ctx context.Context, req messagesRequest) (Response, error) {
	if c.passback.Load() {
		req.Messages = injectThinkingPassback(req.Messages)
	}
	var lastErr error
	probed := false
	for attempt := 0; attempt <= c.maxRetry; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.retryBase<<uint(attempt-1)); err != nil {
				return Response{}, err
			}
		}
		resp, err := c.completeOnce(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !probed && passbackRequired(err) {
			probed = true
			c.passback.Store(true)
			req.Messages = injectThinkingPassback(req.Messages)
			attempt-- // the correction retries at once, off the transport budget
			continue
		}
		if !retryable(err) {
			break
		}
	}
	return Response{}, lastErr
}

// completeOnce performs a single request and parses the response.
func (c *Client) completeOnce(ctx context.Context, req messagesRequest) (Response, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("entry: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL(c.baseURL), bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		// A caller-cancelled context is not a transient network failure and must
		// not be retried. Anything else (DNS, connection reset, EOF) is.
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("entry: request: %w", err)
		}
		return Response{}, &transientError{err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// A mid-body truncation (unexpected EOF / connection reset) is a
		// transient transport failure like a failed Do: no complete response was
		// received, so it is safe to retry.
		return Response{}, &transientError{err: fmt.Errorf("read response: %w", err)}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return Response{}, &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, &statusError{status: resp.StatusCode, body: string(body)}
	}

	var mr messagesResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return Response{}, fmt.Errorf("entry: parse response: %w", err)
	}
	if mr.Error != nil {
		return Response{}, fmt.Errorf("entry: api error: %s", mr.Error.Message)
	}
	if mr.Usage != nil {
		c.addUsage(mr.Usage.InputTokens, mr.Usage.OutputTokens)
	}
	return parseResponse(&mr), nil
}

// parseResponse reduces a response to its text (text blocks only) and its
// tool_use blocks. It records whether the provider stopped at max_tokens, so
// callers can surface a truncation rather than pass a silently cut answer
// through. Thinking blocks are deliberately not used as the answer: a
// thinking-only response carries no reply, and surfacing the model's private
// reasoning would leak it to the user (D14). Text blocks still pass
// stripThinkingBlock as a backstop for relays that inline reasoning into
// the text content itself.
func parseResponse(mr *messagesResponse) Response {
	var out Response
	var texts []string
	for _, b := range mr.Content {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "tool_use":
			out.ToolUses = append(out.ToolUses, ToolUse{ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	out.Text = stripThinkingBlock(strings.Join(texts, ""))
	out.Truncated = mr.StopReason == "max_tokens"
	return out
}

type retryableError struct {
	status int
	body   string
}

func (e *retryableError) Error() string {
	return fmt.Sprintf("entry: retryable status %d: %s", e.status, truncate(e.body, 200))
}

// statusError marks a definitive non-OK, non-retryable API response: the
// provider answered and rejected the call (bad key, wrong endpoint, bad
// request). The carried code lets WrapAPIError turn it into an actionable
// user message instead of a generic "try again later".
type statusError struct {
	status int
	body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("entry: api status %d: %s", e.status, truncate(e.body, 300))
}

// transientError marks a transport-level failure (connection reset, DNS, EOF,
// timeout) that is safe to retry because no definitive response was received.
type transientError struct{ err error }

func (e *transientError) Error() string { return fmt.Sprintf("entry: transient: %v", e.err) }
func (e *transientError) Unwrap() error { return e.err }

func retryable(err error) bool {
	var re *retryableError
	if errors.As(err, &re) {
		return true
	}
	var te *transientError
	return errors.As(err, &te)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ModelInfo is one entry in a provider's model catalogue.
type ModelInfo struct {
	ID string `json:"id"`
}

// listModelsResponse is the OpenAI /models shape — { "data": [ { "id": … } ] }
// — which Anthropic-compatible relays emit as well.
type listModelsResponse struct {
	Data []ModelInfo `json:"data"`
}

// ListModels fetches the provider's model catalogue. The endpoint is derived
// from the provider override or the configured api type (see
// listModelsEndpoint); the response is the OpenAI "data":[{"id":…}] form. It
// needs an API key unless the provider is no-auth (a local Ollama).
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if c.apiKey == "" && !c.noAuth {
		return nil, ErrNoKey
	}
	url, bearer := c.listModelsEndpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if bearer {
		req.Header.Set("authorization", "Bearer "+c.apiKey)
	} else {
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", anthropicVersion)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("entry: list models: %w", err)
		}
		return nil, &transientError{err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &transientError{err: fmt.Errorf("read models: %w", err)}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{status: resp.StatusCode, body: string(body)}
	}

	var lr listModelsResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("entry: parse models: %w", err)
	}
	return lr.Data, nil
}

// listModelsEndpoint resolves the list-models URL and whether it wants Bearer
// auth (OpenAI surface) or x-api-key (Anthropic surface).
func (c *Client) listModelsEndpoint() (string, bool) {
	// Bearer auth is used for OpenAI-compatible surfaces, plus DeepSeek whose
	// list-models endpoint lives on its OpenAI surface even when using the
	// Anthropic Messages dialect for completions.
	isBearer := c.apiType == config.APITypeOpenAI || c.provider == "deepseek" || isDeepSeekEndpoint(c.baseURL)

	if c.modelsURL != "" {
		return c.modelsURL, isBearer
	}
	if c.apiType == config.APITypeOpenAI {
		return strings.TrimRight(c.baseURL, "/") + "/models", true
	}
	// Anthropic dialect. DeepSeek's Anthropic endpoint has no list route, so
	// fall through to its OpenAI surface even without a provider id.
	if isDeepSeekEndpoint(c.baseURL) {
		return "https://api.deepseek.com/models", true
	}
	return strings.TrimRight(c.baseURL, "/") + "/v1/models", false
}
