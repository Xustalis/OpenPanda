// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// StreamTurnsWithTools runs one call with a conversation history, an optional
// tool set, and a text-delta callback: answer text is delivered live as the
// provider streams it, while tool_use blocks and the final Response are
// accumulated and returned at the end. It is the streaming counterpart of
// CompleteTurnsWithTools and dispatches on the configured api type.
func (c *Client) StreamTurnsWithTools(ctx context.Context, system string, turns []Turn, tools []ToolSpec, onDelta func(string), onReasoning func(string)) (Response, error) {
	if c.apiType == config.APITypeOpenAI {
		return c.streamWithRetry(ctx, func(on, onR func(string)) (Response, error) {
			return c.streamOpenAI(ctx, system, turns, tools, on, onR)
		}, onDelta, onReasoning)
	}
	return c.streamWithRetry(ctx, func(on, onR func(string)) (Response, error) {
		return c.streamAnthropic(ctx, system, turns, tools, on, onR)
	}, onDelta, onReasoning)
}

// streamWithRetry gives the streaming path the transport resilience the
// non-streaming path has (completeWithRetry): a retryable failure (weak
// network, 429, 5xx) is replayed with backoff — but only while no delta has
// been delivered to the caller. Once the user has seen text, a replay would
// duplicate it, so the failure surfaces instead. A caller-cancelled context is
// never retried. Delivery is judged by the deltaGuard, so a structured output
// whose deltas were suppressed still counts as unseen and stays retryable.
func (c *Client) streamWithRetry(ctx context.Context, stream func(onDelta, onReasoning func(string)) (Response, error), onDelta func(string), onReasoning func(string)) (Response, error) {
	guard := newDeltaGuard(onDelta, onReasoning)
	var lastErr error
	noBackoff := false
	for attempt := 0; attempt <= c.maxRetry; attempt++ {
		if attempt > 0 && !noBackoff {
			// Announce the backoff before sleeping it: a persistent 429/5xx
			// otherwise sits behind a motionless spinner for the whole
			// ladder and reads as a hang.
			notifyRetry(ctx, attempt, c.maxRetry, c.retryDelay(attempt), lastErr)
			if err := sleepCtx(ctx, c.retryDelay(attempt)); err != nil {
				return Response{}, err
			}
		}
		noBackoff = false
		resp, err := stream(guard.on, guard.onReason)
		if err == nil {
			// The accumulators join every raw delta — including inlined
			// reasoning — so the final text must pass the same stripping the
			// user's live deltas did (think.go, D14).
			resp.Text = stripThinkingBlock(resp.Text)
			guard.flush()
			return resp, nil
		}
		lastErr = err
		if guard.delivered || ctx.Err() != nil || !retryable(err) {
			break
		}
		// A dead link is not a failed attempt: wait for it and replay at once
		// (offline.go). Nothing has been shown to the user (guard.delivered is
		// false), so the replay cannot duplicate text.
		waited, werr := c.awaitNetwork(ctx, err)
		if werr != nil {
			return Response{}, werr
		}
		if waited {
			attempt--        // the outage costs no retry attempt
			noBackoff = true // ...nor an extra backoff before the replay
		}
	}
	return Response{}, lastErr
}

// deltaGuard wraps the caller's onDelta for one streaming call and owns the
// concerns that share a single piece of state — what the user has actually
// seen:
//
//   - Reasoning removal: providers that inline chain-of-thought into the
//     content field wrap it in  tags; thinkStripper runs ahead of every
//     other decision so reasoning never reaches the user nor biases the
//     structured-shape detection (think.go, D14).
//   - Suppression: a task spec arrives as bare JSON (the system prompt
//     demands "只输出一个 JSON 对象"), and raw JSON must never stream into a
//     chat bubble or terminal — the parsed Output delivers it rendered at
//     the end. The first visible byte decides the response's shape: '{' or
//     a code fence withholds every delta; anything else is answer prose and
//     streams live, starting with the bytes buffered while deciding.
//   - Delivery: streamWithRetry replays a failed attempt only while the user
//     has seen nothing, so a suppressed structured delta does not count as
//     delivered — a mid-JSON transport drop is still safely retried.
type deltaGuard struct {
	onDelta     func(string)
	onReasoning func(string)
	buffered    []string
	decided     bool
	structured  bool
	delivered   bool
	pending     strings.Builder // prose mode: bytes withheld pending a possible structured start
	think       thinkStripper   // removes inlined  reasoning before anything else
	// Suspect tracking mirrors ParseOutput's embedded-directive rule: the
	// FIRST '{' in the text is buffered until its balanced close and only
	// suppressed when it decodes as a real envelope under a short,
	// non-illustrative lead-in. A fence, a quoted JSON snippet, or a brace
	// that fails the envelope check is answer prose and is released, never
	// swallowed — the first-'{'-wins rule means nothing after a failed
	// candidate can become a directive either.
	suspect        bool
	noMoreSuspects bool
	leadLen        int
	leadMarker     bool
	leadTail       string
}

func newDeltaGuard(onDelta func(string), onReasoning func(string)) *deltaGuard {
	return &deltaGuard{onDelta: onDelta, onReasoning: onReasoning}
}

// onReason is the reasoning sink the stream implementations call with
// chain-of-thought from a provider's SEPARATE reasoning field (Anthropic
// thinking_delta, OpenAI reasoning_content). It bypasses the structured-shape
// decision and the think stripper entirely — reasoning arrives already
// separated from the answer, so it is neither JSON to suppress nor inline
// <think> tags to strip. It is display-only and deliberately does NOT set
// delivered: reasoning is regenerable scratch, so a transport drop that showed
// only reasoning stays retryable (the retry re-fills the thinking block).
func (g *deltaGuard) onReason(text string) {
	if text == "" || g.onReasoning == nil {
		return
	}
	g.onReasoning(text)
}

// on is the delta sink the stream implementations call.
func (g *deltaGuard) on(text string) {
	if text = g.think.feed(text); text == "" {
		return // reasoning, or bytes still undecidable as a split tag
	}
	g.deliver(text)
}

// deliver runs the structured-shape decision on think-stripped prose.
func (g *deltaGuard) deliver(text string) {
	if !g.decided {
		g.buffered = append(g.buffered, text)
		trimmed := strings.TrimLeft(strings.Join(g.buffered, ""), " \t\r\n")
		if trimmed == "" {
			return // nothing visible yet; keep buffering
		}
		if dsmlStartIndex(trimmed) == 0 {
			// The response opens with DSML tool-call markup: withhold it like
			// any other structured output; resolveDSML recovers or rejects it.
			g.decided = true
			g.structured = true
			g.buffered = nil
			return
		}
		if dsmlMarkerPrefix(trimmed) {
			return // a DSML marker split across deltas is still undecidable
		}
		switch {
		case trimmed[0] == '{':
			// A response opening on a bare object is the dominant directive
			// shape; if it is not a valid envelope the parsed Output still
			// renders in full at the end, so withholding here is safe.
			g.decided = true
			g.structured = true
			g.buffered = nil
			return
		case trimmed == "`" || trimmed == "``":
			return // an in-flight ``` fence is still undecidable
		case strings.HasPrefix(trimmed, "```"):
			// A response opening on a fence is a directive only when the
			// fenced content itself leads with '{' — a code or quoted block
			// is ordinary answer prose and must stream like any other.
			inner := trimmed[3:]
			nl := strings.IndexByte(inner, '\n')
			if nl < 0 {
				return // the fence's own line has not finished arriving
			}
			inner = strings.TrimLeft(inner[nl+1:], " \t\r\n")
			if inner == "" {
				return // the first fenced line is still in flight
			}
			g.decided = true
			if inner[0] == '{' {
				g.structured = true
				g.buffered = nil
				return
			}
		default:
			g.decided = true
		}
		text = strings.Join(g.buffered, "")
		g.buffered = nil
	} else if g.structured {
		return
	}
	// Prose mode. The parser accepts a lead-in followed by a JSON directive,
	// and the embedded case is decided by the FIRST '{' object in the text —
	// so that is the only thing worth withholding, and only until its
	// balanced close reveals whether decodeEnvelope would take it. Anything
	// else (fenced code, quoted JSON, examples) is answer text and streams.
	// DSML markup gets the suppression treatment instead: it is a protocol
	// artifact, never answer prose.
	g.pending.WriteString(text)
	for {
		s := g.pending.String()
		if g.suspect {
			obj := extractJSONObject(s)
			if obj == "" {
				// No balanced close yet. A runaway '{' past the cap is no
				// sane directive — release it as prose rather than swallow
				// the rest of the answer the parser would keep.
				if len(s) > suspectHoldCap {
					g.forwardProse(s)
					g.pending.Reset()
					g.suspect = false
					g.noMoreSuspects = true
					continue
				}
				return
			}
			if g.leadOK() {
				if suppress, answer := judgeObject(obj); suppress {
					// A wrapped {"kind":"answer"} unwraps to its inner text —
					// stream that in place of the envelope, exactly as the
					// parser renders it, so nothing the model said is lost.
					if answer != "" {
						g.forwardProse(answer)
					}
					g.structured = true
					g.pending.Reset()
					return
				}
			}
			// First '{' judged prose: release it and stop scanning — per the
			// parser's first-'{'-wins rule nothing later can be a directive.
			g.forwardProse(s[:len(obj)])
			g.pending.Reset()
			g.pending.WriteString(s[len(obj):])
			g.suspect = false
			g.noMoreSuspects = true
			continue
		}
		di, ji := dsmlStartIndex(s), -1
		if !g.noMoreSuspects {
			ji = strings.IndexByte(s, '{')
		}
		switch {
		case di >= 0 && (ji < 0 || di < ji):
			g.forwardProse(s[:di])
			g.structured = true
			g.pending.Reset()
			return
		case ji >= 0:
			g.forwardProse(s[:ji])
			g.pending.Reset()
			g.pending.WriteString(s[ji:])
			g.suspect = true
		default:
			if hold := dsmlHoldbackLen(s); len(s) > hold {
				g.forwardProse(s[:len(s)-hold])
				g.pending.Reset()
				g.pending.WriteString(s[len(s)-hold:])
			}
			return
		}
	}
}

// flush forwards any prose still withheld at end-of-stream: bytes held back
// by the think stripper as a possible split tag (now literal text), then
// bytes held back as a possible structured prefix that never developed.
func (g *deltaGuard) flush() {
	if tail := g.think.flush(); tail != "" {
		g.deliver(tail)
	}
	if !g.decided {
		// The stream ended before the shape decision — e.g. a lone "<"
		// withheld as a possible DSML marker prefix that never completed.
		// Withheld bytes are plain prose: deliver them rather than drop them.
		g.forward(strings.Join(g.buffered, ""))
		g.buffered = nil
		return
	}
	if !g.structured {
		g.forward(g.pending.String())
		g.pending.Reset()
	}
}

func (g *deltaGuard) forward(s string) {
	if s == "" {
		return
	}
	g.delivered = true
	if g.onDelta != nil {
		g.onDelta(s)
	}
}

// suspectHoldCap bounds the bytes one embedded '{' candidate can withhold
// while its balanced close is still in flight. Task envelopes run a few
// kilobytes at most; a runaway '{' past this is prose wearing a brace, and
// releasing it beats swallowing the rest of an answer the parser would keep.
const suspectHoldCap = 64 * 1024

// forwardProse delivers s to the user and folds it into the lead-in the
// suspect gate measures — every byte shown before a candidate '{' counts.
func (g *deltaGuard) forwardProse(s string) {
	if s == "" {
		return
	}
	g.noteProse(s)
	g.forward(s)
}

// noteProse folds one displayed prose chunk into the embedded-directive
// lead-in: ParseOutput accepts a mid-text directive only under ≤jsonLeadInMax
// bytes of lead-in that does not read as showing an example. Markers are
// matched against a short tail join so one split across deltas is still seen.
func (g *deltaGuard) noteProse(s string) {
	g.leadLen += len(s)
	if g.leadMarker {
		return
	}
	probe := g.leadTail + s
	if looksIllustrative(probe) {
		g.leadMarker = true
	}
	if len(probe) > 16 {
		g.leadTail = probe[len(probe)-16:]
	} else {
		g.leadTail = probe
	}
}

// leadOK reports whether the prose before a candidate '{' stays inside the
// parser's embedded-directive lead-in budget.
func (g *deltaGuard) leadOK() bool {
	return g.leadLen <= jsonLeadInMax && !g.leadMarker
}

// judgeObject applies ParseOutput's embedded-object verdict to a balanced
// '{' candidate: a real directive — or JSON whose envelope mismatch surfaces
// a validation error — stays suppressed; a wrapped {"kind":"answer"} unwraps
// to its inner answer text, which streams in place of the raw envelope.
// Only a clean non-envelope releases the bytes as ordinary prose.
func judgeObject(obj string) (suppress bool, answer string) {
	out, ok, err := decodeEnvelope(obj)
	switch {
	case err != nil:
		return true, ""
	case !ok:
		return false, ""
	case out.Kind == KindAnswer:
		return true, out.Answer
	default:
		return true, ""
	}
}

// ---- Anthropic Messages SSE streaming ----

// anthropicStreamEvent is one SSE data payload; only the fields PANDA acts on
// are modeled. index addresses the content block a delta belongs to.
// message_start carries the input-token usage on message.usage;
// message_delta carries the cumulative output-token count on usage.
type anthropicStreamEvent struct {
	Type         string         `json:"type"` // content_block_start | content_block_delta | message_start | message_delta | error
	Index        int            `json:"index"`
	ContentBlock ContentBlock   `json:"content_block"`
	Delta        anthropicDelta `json:"delta"`
	Message      struct {
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	Usage *anthropicUsage `json:"usage"`
}

type anthropicDelta struct {
	Type        string `json:"type"` // text_delta | input_json_delta | thinking_delta
	Text        string `json:"text"`
	Thinking    string `json:"thinking"` // thinking_delta: chain-of-thought scratch
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

// anthAccumulator assembles a Response from Anthropic stream events. Usage is
// accumulated per attempt (the caller bills it only when the stream
// completes, so a retried attempt is never double-counted). Reasoning from
// thinking blocks is collected separately from the answer text (D14): it is
// surfaced live via the reasoning sink and returned on Response.Reasoning,
// never joined into Response.Text.
type anthAccumulator struct {
	reasoning []string
	blocks    map[int]*ContentBlock    // by stream index
	rawArgs   map[int]*strings.Builder // tool_use partial_json, by index
	order     []int
	truncated bool
	usageIn   int64
	usageOut  int64
}

// noteUsage records a usage-bearing event: message_start carries the
// input-token count on message.usage, message_delta the cumulative
// output-token count on usage. Counts only grow within one attempt, so a
// provider that reports both takes the larger of each.
func (a *anthAccumulator) noteUsage(ev *anthropicStreamEvent) {
	var u *anthropicUsage
	switch ev.Type {
	case "message_start":
		u = ev.Message.Usage
	case "message_delta":
		u = ev.Usage
	}
	if u == nil {
		return
	}
	if u.InputTokens > a.usageIn {
		a.usageIn = u.InputTokens
	}
	if u.OutputTokens > a.usageOut {
		a.usageOut = u.OutputTokens
	}
}

func (a *anthAccumulator) start(ev *anthropicStreamEvent) {
	b := ev.ContentBlock
	a.blocks[ev.Index] = &ContentBlock{Type: b.Type, ID: b.ID, Name: b.Name}
	if b.Type == "tool_use" {
		a.rawArgs[ev.Index] = &strings.Builder{}
	}
	a.order = append(a.order, ev.Index)
}

func (a *anthAccumulator) delta(ev *anthropicStreamEvent, onDelta func(string), onReasoning func(string)) {
	b, ok := a.blocks[ev.Index]
	if !ok {
		return
	}
	switch ev.Delta.Type {
	case "text_delta":
		b.Text += ev.Delta.Text
		if b.Type == "text" && onDelta != nil {
			onDelta(ev.Delta.Text)
		}
	case "thinking_delta":
		// Chain-of-thought on a thinking block: collect it apart from the
		// answer and surface it live, but never fold it into b.Text (D14).
		a.reasoning = append(a.reasoning, ev.Delta.Thinking)
		if onReasoning != nil {
			onReasoning(ev.Delta.Thinking)
		}
	case "input_json_delta":
		if sb := a.rawArgs[ev.Index]; sb != nil {
			sb.WriteString(ev.Delta.PartialJSON)
		}
	}
}

func (a *anthAccumulator) result() Response {
	var out Response
	var texts []string
	for _, idx := range a.order {
		b := a.blocks[idx]
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "tool_use":
			var input map[string]any
			if sb := a.rawArgs[idx]; sb != nil {
				if s := strings.TrimSpace(sb.String()); s != "" {
					if err := json.Unmarshal([]byte(s), &input); err != nil {
						input = map[string]any{"_raw": s}
					}
				}
			}
			out.ToolUses = append(out.ToolUses, ToolUse{ID: b.ID, Name: b.Name, Input: input})
		}
	}
	out.Text = strings.Join(texts, "")
	out.Reasoning = strings.Join(a.reasoning, "")
	out.Truncated = a.truncated
	return out
}

func (c *Client) streamAnthropic(ctx context.Context, system string, turns []Turn, tools []ToolSpec, onDelta func(string), onReasoning func(string)) (Response, error) {
	if c.noCredential() {
		return Response{}, ErrNoKey
	}
	msgs := turnsToMessages(turns)
	if c.passback.Load() {
		msgs = injectThinkingPassback(msgs)
	}
	body, err := func() (io.ReadCloser, error) {
		resp, err := c.sendAnthropicStream(ctx, system, msgs, tools)
		if err == nil {
			return resp.Body, nil
		}
		// A thinking-passback rejection arrives before any SSE event, so the
		// corrected payload replays safely within the same call. The flag
		// sticks, so later calls inject up front.
		if passbackRequired(err) && !c.passback.Load() {
			c.passback.Store(true)
			msgs = injectThinkingPassback(msgs)
			resp, err = c.sendAnthropicStream(ctx, system, msgs, tools)
			if err == nil {
				return resp.Body, nil
			}
		}
		// A provider that rejects the thinking field gets one retry without
		// it; the denial is sticky for the client's lifetime.
		if thinkingRejected(err) && !c.thinkingDenied.Load() {
			c.thinkingDenied.Store(true)
			resp, err = c.sendAnthropicStream(ctx, system, msgs, tools)
			if err == nil {
				return resp.Body, nil
			}
		}
		return nil, err
	}()
	if err != nil {
		return Response{}, err
	}
	defer body.Close()

	acc := &anthAccumulator{blocks: map[int]*ContentBlock{}, rawArgs: map[int]*strings.Builder{}}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			data := strings.TrimSpace(after)
			if data == "" || data == "[DONE]" {
				continue
			}
			var ev anthropicStreamEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue // unknown event shape; skip
			}
			switch ev.Type {
			case "content_block_start":
				acc.start(&ev)
			case "content_block_delta":
				acc.delta(&ev, onDelta, onReasoning)
			case "message_start", "message_delta":
				acc.noteUsage(&ev)
				if ev.Type == "message_delta" && ev.Delta.StopReason == "max_tokens" {
					acc.truncated = true
				}
			case "error":
				msg := "stream error"
				if ev.Delta.Text != "" {
					msg = ev.Delta.Text
				}
				return Response{}, fmt.Errorf("entry: api error: %s", msg)
			}
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		// A mid-stream transport drop (unexpected EOF / reset) is transient;
		// streamWithRetry only replays it when nothing was delivered yet.
		return Response{}, &transientError{err: fmt.Errorf("read stream: %w", err)}
	}
	// Bill this attempt's usage only on success: a failed attempt that got
	// replaced by a retry must not be counted.
	c.addUsage(acc.usageIn, acc.usageOut)
	return acc.result(), nil
}

// sendOAIStream sends one streaming Chat Completions request with msgs and
// returns the open response on 200 (the caller owns its body), mirroring
// sendAnthropicStream's error split.
func (c *Client) sendOAIStream(ctx context.Context, system string, msgs []oaiMessage, tools []ToolSpec) (*http.Response, error) {
	promptCacheKey := ""
	if c.promptCache.Load() {
		promptCacheKey = c.oaiPromptCacheKey(system)
	}
	req := buildOAIRequest(c.model, c.maxTokens, true, msgs, tools, promptCacheKey)
	if !c.promptCache.Load() {
		req.StreamOptions = nil
	}
	c.applyThinking(&req)
	payload, err := c.marshalWithParams(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIURL(c.baseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	if err := c.applyAuth(ctx, httpReq); err != nil {
		return nil, err
	}
	c.applyExtraHeaders(httpReq)

	resp, err := c.hcStream.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("entry: request: %w", err)
		}
		return nil, &transientError{err: err}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &statusError{status: resp.StatusCode, body: string(body)}
	}
	return resp, nil
}

// sendAnthropicStream sends one streaming Messages request with msgs and
// returns the open response on 200 (the caller owns its body). Non-200 shapes
// map onto the same retryable/status error split the non-streaming path uses.
func (c *Client) sendAnthropicStream(ctx context.Context, system string, msgs []message, tools []ToolSpec) (*http.Response, error) {
	req := messagesRequest{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System:    c.systemPayload(system),
		Messages:  msgs,
		Stream:    true,
	}
	if len(tools) > 0 {
		req.Tools = tools
	}
	if th, budget := c.anthropicThinking(); th != nil {
		req.Thinking = th
		if req.MaxTokens <= budget {
			req.MaxTokens = budget + 4096
		}
		if c.maxTokensCap > 0 && req.MaxTokens > c.maxTokensCap {
			req.MaxTokens = c.maxTokensCap
		}
	}
	payload, err := c.marshalWithParams(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL(c.baseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	if err := c.applyAuth(ctx, httpReq); err != nil {
		return nil, err
	}
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	c.applyExtraHeaders(httpReq)

	resp, err := c.hcStream.Do(httpReq)
	if err != nil {
		// A caller-cancelled context is not a transient failure and must not
		// be retried; anything else (DNS, reset, EOF) is.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("entry: request: %w", err)
		}
		return nil, &transientError{err: err}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &statusError{status: resp.StatusCode, body: string(body)}
	}
	return resp, nil
}

// ---- OpenAI Chat Completions SSE streaming ----

func (c *Client) streamOpenAI(ctx context.Context, system string, turns []Turn, tools []ToolSpec, onDelta func(string), onReasoning func(string)) (Response, error) {
	if c.noCredential() {
		return Response{}, ErrNoKey
	}
	msgs := turnsToOpenAI(system, turns)
	if c.passback.Load() {
		msgs = injectReasoningPassback(msgs)
	}
	body, err := func() (io.ReadCloser, error) {
		resp, err := c.sendOAIStream(ctx, system, msgs, tools)
		if err == nil {
			return resp.Body, nil
		}
		// Same passback probe as the Anthropic stream path: the 400 lands
		// before any chunk, so the corrected payload replays safely.
		if passbackRequired(err) && !c.passback.Load() {
			c.passback.Store(true)
			msgs = injectReasoningPassback(msgs)
			resp, err = c.sendOAIStream(ctx, system, msgs, tools)
			if err == nil {
				return resp.Body, nil
			}
		}
		if thinkingRejected(err) && !c.thinkingDenied.Load() {
			c.thinkingDenied.Store(true)
			resp, err = c.sendOAIStream(ctx, system, msgs, tools)
			if err == nil {
				return resp.Body, nil
			}
		}
		return nil, err
	}()
	if err != nil {
		return Response{}, err
	}
	defer body.Close()

	acc := &oaiAccumulator{calls: map[int]oaiToolCall{}}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			data := strings.TrimSpace(after)
			if data == "" {
				continue
			}
			if data == "[DONE]" {
				break
			}
			var chunk oaiChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue // unknown chunk shape; skip
			}
			if chunk.Error != nil {
				return Response{}, fmt.Errorf("entry: api error: %s", chunk.Error.Message)
			}
			acc.feed(&chunk, onDelta, onReasoning)
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		// A mid-stream transport drop (unexpected EOF / reset) is transient;
		// streamWithRetry only replays it when nothing was delivered yet.
		return Response{}, &transientError{err: fmt.Errorf("read stream: %w", err)}
	}
	// Bill this attempt's usage only on success: a failed attempt that got
	// replaced by a retry must not be counted.
	c.addUsage(acc.usageIn, acc.usageOut)
	return acc.result(), nil
}
