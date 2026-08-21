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
func (c *Client) StreamTurnsWithTools(ctx context.Context, system string, turns []Turn, tools []ToolSpec, onDelta func(string)) (Response, error) {
	if c.apiType == config.APITypeOpenAI {
		return c.streamWithRetry(ctx, func(on func(string)) (Response, error) {
			return c.streamOpenAI(ctx, system, turns, tools, on)
		}, onDelta)
	}
	return c.streamWithRetry(ctx, func(on func(string)) (Response, error) {
		return c.streamAnthropic(ctx, system, turns, tools, on)
	}, onDelta)
}

// streamWithRetry gives the streaming path the transport resilience the
// non-streaming path has (completeWithRetry): a retryable failure (weak
// network, 429, 5xx) is replayed with backoff — but only while no delta has
// been delivered to the caller. Once the user has seen text, a replay would
// duplicate it, so the failure surfaces instead. A caller-cancelled context is
// never retried. Delivery is judged by the deltaGuard, so a structured output
// whose deltas were suppressed still counts as unseen and stays retryable.
func (c *Client) streamWithRetry(ctx context.Context, stream func(onDelta func(string)) (Response, error), onDelta func(string)) (Response, error) {
	guard := newDeltaGuard(onDelta)
	var lastErr error
	for attempt := 0; attempt <= c.maxRetry; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.retryBase<<uint(attempt-1)); err != nil {
				return Response{}, err
			}
		}
		resp, err := stream(guard.on)
		if err == nil {
			guard.flush()
			return resp, nil
		}
		lastErr = err
		if guard.delivered || ctx.Err() != nil || !retryable(err) {
			break
		}
	}
	return Response{}, lastErr
}

// deltaGuard wraps the caller's onDelta for one streaming call and owns the
// two concerns that share a single piece of state — what the user has
// actually seen:
//
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
	onDelta    func(string)
	buffered   []string
	decided    bool
	structured bool
	delivered  bool
	pending    strings.Builder // prose mode: bytes withheld pending a possible structured start
}

func newDeltaGuard(onDelta func(string)) *deltaGuard {
	return &deltaGuard{onDelta: onDelta}
}

// on is the delta sink the stream implementations call.
func (g *deltaGuard) on(text string) {
	if !g.decided {
		g.buffered = append(g.buffered, text)
		trimmed := strings.TrimLeft(strings.Join(g.buffered, ""), " \t\r\n")
		if trimmed == "" {
			return // nothing visible yet; keep buffering
		}
		g.decided = true
		g.structured = trimmed[0] == '{' || trimmed[0] == '`'
		if g.structured {
			g.buffered = nil
			return
		}
		text = strings.Join(g.buffered, "")
		g.buffered = nil
	} else if g.structured {
		return
	}
	// Prose mode. The parser accepts a lead-in followed by a JSON directive,
	// so a structured block may still appear mid-stream (e.g. a reasoning
	// preamble before the task JSON). Watch for a line-initial '{' or a
	// ``` fence — including one split across deltas — and suppress from
	// there; the parsed Output renders the directive at the end.
	g.pending.WriteString(text)
	s := g.pending.String()
	if i := structuredStartIndex(s); i >= 0 {
		// Keep the directive's own leading newline with the prose so the
		// lead-in ends on its blank line; everything from '{' on is dropped.
		g.forward(s[:i+1])
		g.structured = true
		g.pending.Reset()
		return
	}
	safe := len(s) - holdbackLen(s)
	if safe > 0 {
		g.forward(s[:safe])
		rest := s[safe:]
		g.pending.Reset()
		g.pending.WriteString(rest)
	}
}

// flush forwards any prose still withheld at end-of-stream: bytes held back
// as a possible structured prefix that never developed.
func (g *deltaGuard) flush() {
	if g.decided && !g.structured {
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

// structuredStartIndex reports the offset of the first line-initial '{' or
// ``` fence in s (the shapes the parser's extractJSONObject/stripFences act
// on), or -1. A '{' on the SAME line as prior prose is left alone — inline
// braces in normal answers must keep streaming.
func structuredStartIndex(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		t := strings.TrimLeft(s[i+1:], " \t")
		if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "```") {
			return i
		}
	}
	return -1
}

// holdbackLen returns how many trailing bytes of s must wait for the next
// delta: trailing newline(s) (the directive may start next), or a last-line
// tail that is a strict prefix of "{" / "```json" (mid-fence split).
func holdbackLen(s string) int {
	j := len(s)
	for j > 0 && (s[j-1] == '\n' || s[j-1] == '\r') {
		j--
	}
	if j < len(s) {
		return len(s) - j
	}
	i := strings.LastIndexByte(s, '\n')
	tail := s[i+1:]
	if tail != "" && (tail[0] == '{' || tail[0] == '`') && structPrefixChars(tail) {
		return len(tail)
	}
	return 0
}

// structPrefixChars reports whether every byte of t could belong to the
// prologue of a structured block ('{', '`', or fence language letters).
func structPrefixChars(t string) bool {
	for _, c := range t {
		switch {
		case c == '{', c == '`':
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		default:
			return false
		}
	}
	return true
}

// ---- Anthropic Messages SSE streaming ----

// anthropicStreamEvent is one SSE data payload; only the fields PANDA acts on
// are modeled. index addresses the content block a delta belongs to.
type anthropicStreamEvent struct {
	Type         string         `json:"type"` // content_block_start | content_block_delta | message_delta | error
	Index        int            `json:"index"`
	ContentBlock ContentBlock   `json:"content_block"`
	Delta        anthropicDelta `json:"delta"`
}

type anthropicDelta struct {
	Type        string `json:"type"` // text_delta | input_json_delta
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

// anthAccumulator assembles a Response from Anthropic stream events.
type anthAccumulator struct {
	texts     []string
	blocks    map[int]*ContentBlock    // by stream index
	rawArgs   map[int]*strings.Builder // tool_use partial_json, by index
	order     []int
	truncated bool
}

func (a *anthAccumulator) start(ev *anthropicStreamEvent) {
	b := ev.ContentBlock
	a.blocks[ev.Index] = &ContentBlock{Type: b.Type, ID: b.ID, Name: b.Name}
	if b.Type == "tool_use" {
		a.rawArgs[ev.Index] = &strings.Builder{}
	}
	a.order = append(a.order, ev.Index)
}

func (a *anthAccumulator) delta(ev *anthropicStreamEvent, onDelta func(string)) {
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
	out.Truncated = a.truncated
	return out
}

func (c *Client) streamAnthropic(ctx context.Context, system string, turns []Turn, tools []ToolSpec, onDelta func(string)) (Response, error) {
	if c.apiKey == "" {
		return Response{}, ErrNoKey
	}
	req := messagesRequest{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System:    system,
		Messages:  turnsToMessages(turns),
		Stream:    true,
	}
	if len(tools) > 0 {
		req.Tools = tools
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("entry: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.hcStream.Do(httpReq)
	if err != nil {
		// A caller-cancelled context is not a transient failure and must not
		// be retried; anything else (DNS, reset, EOF) is.
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("entry: request: %w", err)
		}
		return Response{}, &transientError{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Response{}, &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Response{}, &statusError{status: resp.StatusCode, body: string(body)}
	}

	acc := &anthAccumulator{blocks: map[int]*ContentBlock{}, rawArgs: map[int]*strings.Builder{}}
	scanner := bufio.NewScanner(resp.Body)
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
				acc.delta(&ev, onDelta)
			case "message_delta":
				if ev.Delta.StopReason == "max_tokens" {
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
	return acc.result(), nil
}

// ---- OpenAI Chat Completions SSE streaming ----

func (c *Client) streamOpenAI(ctx context.Context, system string, turns []Turn, tools []ToolSpec, onDelta func(string)) (Response, error) {
	if c.apiKey == "" {
		return Response{}, ErrNoKey
	}
	req := oaiRequest{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		Stream:    true,
		Messages:  turnsToOpenAI(system, turns),
	}
	if len(tools) > 0 {
		req.Tools = specsToOpenAI(tools)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("entry: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	httpReq.Header.Set("authorization", "Bearer "+c.apiKey)

	resp, err := c.hcStream.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, fmt.Errorf("entry: request: %w", err)
		}
		return Response{}, &transientError{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Response{}, &retryableError{status: resp.StatusCode, body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Response{}, &statusError{status: resp.StatusCode, body: string(body)}
	}

	acc := &oaiAccumulator{calls: map[int]oaiToolCall{}}
	scanner := bufio.NewScanner(resp.Body)
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
			acc.feed(&chunk, onDelta)
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		// A mid-stream transport drop (unexpected EOF / reset) is transient;
		// streamWithRetry only replays it when nothing was delivered yet.
		return Response{}, &transientError{err: fmt.Errorf("read stream: %w", err)}
	}
	return acc.result(), nil
}
