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

	"github.com/xenith/openpanda/internal/config"
)

// StreamTurnsWithTools runs one call with a conversation history, an optional
// tool set, and a text-delta callback: answer text is delivered live as the
// provider streams it, while tool_use blocks and the final Response are
// accumulated and returned at the end. It is the streaming counterpart of
// CompleteTurnsWithTools and dispatches on the configured api type.
func (c *Client) StreamTurnsWithTools(ctx context.Context, system string, turns []Turn, tools []ToolSpec, onDelta func(string)) (Response, error) {
	if c.apiType == config.APITypeOpenAI {
		return c.streamOpenAI(ctx, system, turns, tools, onDelta)
	}
	return c.streamAnthropic(ctx, system, turns, tools, onDelta)
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
		return Response{}, fmt.Errorf("entry: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Response{}, fmt.Errorf("entry: api status %d: %s", resp.StatusCode, truncate(string(body), 300))
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
		return Response{}, fmt.Errorf("entry: read stream: %w", err)
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
		return Response{}, fmt.Errorf("entry: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Response{}, fmt.Errorf("entry: api status %d: %s", resp.StatusCode, truncate(string(body), 300))
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
		return Response{}, fmt.Errorf("entry: read stream: %w", err)
	}
	return acc.result(), nil
}
