// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mcp is a minimal Model Context Protocol (MCP) client over stdio. It
// speaks just the JSON-RPC 2.0 methods PANDA needs — initialize, tools/list,
// tools/call — against a child process's stdin/stdout.
//
// PANDA uses MCP in client mode only: it connects to a server, lists its tools,
// and executes calls itself, presenting them to the model as ordinary tool_use
// blocks. Server-side MCP tool execution (mcp_tool_use blocks) is not used,
// because DeepSeek's Anthropic-compatible endpoint does not support it.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/executil"
	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/security"
	"github.com/Xustalis/OpenPanda/internal/version"
)

const (
	// defaultCallTimeout is the hard upper bound for any single MCP request,
	// matching the commander adapter's execution ceiling (P1-17 / A4).
	defaultCallTimeout = 630 * time.Second
	// requestedProtocolVersion is the MCP revision PANDA implements. A server
	// may answer with a different one; the negotiated value is kept on the
	// client rather than assumed (see initialize).
	requestedProtocolVersion = "2024-11-05"
	// maxResponseBytes mirrors mcpserve.maxRequestBytes: a server that never
	// emits '\n' must not be able to grow the reader's buffer without bound.
	maxResponseBytes = 4 << 20
	// maxListPages bounds tools/list pagination so a server returning a
	// self-referential cursor cannot keep the ask loop inside ListTools.
	maxListPages = 100
	// maxErrorText caps the tool's own error text echoed back to the model.
	maxErrorText = 2048
	// maxStderrTail is how much of a dying server's stderr is kept for
	// diagnostics. It is a tail, so startup chatter cannot evict the failure.
	maxStderrTail = 4096
)

var errResponseTooLarge = errors.New("mcp: response line exceeds limit")

// Tool is one tool advertised by an MCP server (the tools/list shape).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Client is a stdio MCP client bound to one child server process. Requests are
// serialized with mu because MCP stdio is a single ordered stream; writeMu is
// separate so the response reader can reject a server-initiated request
// without deadlocking against the caller holding mu.
type Client struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      *bufio.Reader
	stdoutPipe  io.ReadCloser // closed on timeout/cleanup to unblock the reader goroutine
	stderr      *tailBuffer
	mu          sync.Mutex
	writeMu     sync.Mutex
	nextID      int
	callTimeout time.Duration
	// protocolVersion is what the server answered in initialize. Written once
	// during the handshake while no other call can run, then read-only.
	protocolVersion string
	// broken holds the first unrecoverable transport failure. Once set, every
	// later call fails immediately instead of writing into a dead pipe.
	broken error
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// tailBuffer keeps the last maxStderrTail bytes written by the child. os/exec
// writes to cmd.Stderr from its own goroutine, so access is mutex-guarded.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= maxStderrTail {
		b.buf = append([]byte(nil), p[len(p)-maxStderrTail:]...)
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - maxStderrTail; over > 0 {
		b.buf = append([]byte(nil), b.buf[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}

// NewStdioClient spawns command args... and performs the initialize handshake.
// The child runs with a minimal environment (M5): the security.Sandbox base
// (PATH and the usual session variables) plus the explicitly declared env —
// the parent's full environment is no longer inherited, so a server only sees
// the variables the caller grants it. It returns a started, initialized
// client; the caller owns it and must Close it.
//
// The child is deliberately NOT bound to ctx: callers hand in request-scoped
// contexts (the settings PUT handler uses a 30s timeout), and
// exec.CommandContext would SIGKILL the process group when that ctx ends —
// the server died moments after the settings page saved. ctx bounds only the
// spawn+handshake; the child's lifetime is owned by Close.
func NewStdioClient(ctx context.Context, command string, env []string, args ...string) (*Client, error) {
	cmd := executil.CommandContext(context.Background(), command, args...)
	if err := security.NewSandbox("").Apply(cmd, env...); err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	stderr := &tailBuffer{}
	cmd.Stderr = stderr // stderr is not part of the JSON-RPC stream, but it is the only evidence when a server dies
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %s: %w", command, err)
	}
	c := &Client{cmd: cmd, stdin: stdin, stdoutPipe: stdoutPipe,
		stdout: bufio.NewReader(stdoutPipe), stderr: stderr, callTimeout: defaultCallTimeout}
	if err := c.initialize(ctx); err != nil {
		_ = c.Close()
		// c.call already decorated its own transport failures with the server's
		// stderr tail; decorating again would wrap the same tail twice.
		return nil, err
	}
	return c, nil
}

// ProtocolVersion returns the revision the server selected during initialize.
// PANDA implements the 2024-11-05 behaviour regardless, but keeping the
// negotiated value makes a version mismatch diagnosable instead of invisible.
func (c *Client) ProtocolVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.protocolVersion
}

// initialize sends the initialize request and the following initialized
// notification (the MCP handshake).
func (c *Client) initialize(ctx context.Context) error {
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": requestedProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "panda", "version": version.Version},
	}, &result); err != nil {
		return err
	}
	if result.ProtocolVersion == "" {
		return fmt.Errorf("mcp: initialize response missing protocolVersion")
	}
	// A server may select a newer revision. Rejecting it outright would make
	// otherwise-compatible servers unusable, so record the negotiated value
	// and continue with the methods both revisions share.
	c.mu.Lock()
	c.protocolVersion = result.ProtocolVersion
	c.mu.Unlock()
	return c.notify("notifications/initialized")
}

// ListTools returns every tool the server advertises, following nextCursor
// pages. A server that omits the cursor (the common case) is a single page.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < maxListPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.call(ctx, "tools/list", params, &result); err != nil {
			return nil, err
		}
		all = append(all, result.Tools...)
		next := strings.TrimSpace(result.NextCursor)
		if next == "" {
			return all, nil
		}
		if next == cursor || seen[next] {
			return nil, fmt.Errorf("mcp: tools/list returned a repeating cursor %q", next)
		}
		seen[next] = true
		cursor = next
	}
	return nil, fmt.Errorf("mcp: tools/list exceeded %d pages", maxListPages)
}

// CallTool invokes a tool and returns the concatenated text content. An
// isError result carries the tool's own explanation into the error so the
// model can act on it instead of seeing only "reported an error".
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &result); err != nil {
		return "", err
	}
	var b strings.Builder
	other := map[string]int{}
	for _, item := range result.Content {
		if item.Type == "text" || item.Type == "" {
			b.WriteString(item.Text)
			continue
		}
		other[item.Type]++
	}
	text := b.String()
	if result.IsError {
		if text == "" {
			return "", fmt.Errorf("mcp: tool %q reported an error", name)
		}
		return "", fmt.Errorf("mcp: tool %q reported an error: %s", name, clipText(text, maxErrorText))
	}
	if text == "" && len(other) > 0 {
		// MCP also defines image/audio/resource blocks. This client returns a
		// string to the model, so say so explicitly rather than looking like
		// the tool produced no output at all.
		types := make([]string, 0, len(other))
		for kind, n := range other {
			types = append(types, fmt.Sprintf("%s×%d", kind, n))
		}
		sort.Strings(types)
		return fmt.Sprintf("[mcp: tool %q returned non-text content (%s); this client forwards text blocks only]", name, strings.Join(types, ", ")), nil
	}
	return text, nil
}

func clipText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…[truncated]"
}

// Close terminates the child server and waits for it to exit. It kills the
// whole process group so orphaned grandchildren are cleaned up (P1-17 / A4).
func (c *Client) Close() error {
	_ = c.kill()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.stdoutPipe != nil {
		_ = c.stdoutPipe.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Wait()
	}
	return nil
}

// kill sends SIGKILL to the child's process group. It is safe to call multiple
// times and is a no-op once the process has already exited.
func (c *Client) kill() error {
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	// executil.CommandContext sets cmd.Cancel to kill the process group.
	return c.cmd.Cancel()
}

// decorate appends the child's stderr tail to an error. Without it a server
// that fails to start (missing module, bad config) looks identical to a
// server that hung, because cmd.Stderr used to be discarded.
func (c *Client) decorate(err error) error {
	if err == nil {
		return nil
	}
	tail := ""
	if c.stderr != nil {
		tail = c.stderr.String()
	}
	if tail == "" {
		return err
	}
	return fmt.Errorf("%w (server stderr: %s)", err, security.Redact(clipText(tail, maxStderrTail)))
}

// call sends one JSON-RPC request and reads the matching response. Frames are
// newline-delimited; notifications, server-initiated requests and responses
// to other ids are skipped by readResponse. A hard callTimeout bounds the
// wait; on expiry the whole child process group is killed so a hung MCP
// server cannot stall the ask loop.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken != nil {
		return fmt.Errorf("mcp: client unusable after %w", c.broken)
	}
	c.nextID++
	id := c.nextID
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	b, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("mcp: marshal request: %w", err)
	}
	if err := c.writeLine(b); err != nil {
		c.broken = err
		return c.decorate(fmt.Errorf("mcp: write request: %w", err))
	}

	callCtx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()

	type result struct {
		resp rpcResponse
		err  error
	}
	respCh := make(chan result, 1)
	guard.Go(nil, "mcp: response reader", nil, func() {
		r, e := c.readResponse(id)
		respCh <- result{resp: r, err: e}
	})

	select {
	case <-callCtx.Done():
		_ = c.kill()
		// Close stdout to unblock the reader goroutine, then drain it so a
		// subsequent call cannot start a second reader racing on the same
		// bufio.Reader.
		if c.stdoutPipe != nil {
			_ = c.stdoutPipe.Close()
		}
		<-respCh
		timeoutErr := fmt.Errorf("mcp: call %s timed out after %s: %w", method, c.callTimeout, callCtx.Err())
		c.broken = timeoutErr
		return c.decorate(timeoutErr)
	case res := <-respCh:
		if res.err != nil {
			c.broken = res.err
			return c.decorate(res.err)
		}
		if res.resp.Error != nil {
			return fmt.Errorf("mcp: rpc error %d: %s", res.resp.Error.Code, res.resp.Error.Message)
		}
		if out != nil {
			if err := json.Unmarshal(res.resp.Result, out); err != nil {
				return fmt.Errorf("mcp: decode result: %w", err)
			}
		}
		return nil
	}
}

// writeLine is the single place that writes to the child's stdin. It uses
// writeMu (not mu) so readResponse can refuse a server request while a call
// is in flight.
func (c *Client) writeLine(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.stdin.Write(append(payload, '\n'))
	return err
}

// readResponse reads lines until it finds the response to wantID. JSON-RPC
// allows string ids, and a server may interleave notifications or its own
// requests (sampling, roots, ping) on the same stream; accepting the first
// frame with any non-zero id used to misattribute those to the pending call.
func (c *Client) readResponse(wantID int) (rpcResponse, error) {
	want := strconv.Itoa(wantID)
	for {
		line, readErr := readLine(c.stdout)
		if len(bytes.TrimSpace(line)) == 0 {
			if readErr != nil {
				return rpcResponse{}, fmt.Errorf("mcp: read response: %w", readErr)
			}
			continue
		}
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(line, &frame); err != nil {
			// A bad line does not desynchronize a line-delimited stream: keep
			// scanning, bounded by the caller's timeout.
			if readErr != nil {
				return rpcResponse{}, fmt.Errorf("mcp: read response: %w", readErr)
			}
			continue
		}
		if frame.Method != "" {
			// A server-initiated request is not a response. PANDA declares no
			// server-facing capabilities, so refuse it rather than leaving the
			// peer waiting for a reply.
			c.rejectServerRequest(frame.ID, frame.Method)
			if readErr != nil {
				return rpcResponse{}, fmt.Errorf("mcp: read response: %w", readErr)
			}
			continue
		}
		if len(frame.ID) == 0 || string(frame.ID) == "null" {
			// Notification (logging/progress/…).
			if readErr != nil {
				return rpcResponse{}, fmt.Errorf("mcp: read response: %w", readErr)
			}
			continue
		}
		if idMatches(frame.ID, wantID, want) {
			var resp rpcResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				return rpcResponse{}, fmt.Errorf("mcp: parse response: %w", err)
			}
			return resp, nil
		}
		if readErr != nil {
			return rpcResponse{}, fmt.Errorf("mcp: read response: %w", readErr)
		}
	}
}

// idMatches accepts the numeric id PANDA sent, echoed as either a JSON number
// or the equivalent JSON string used by some servers.
func idMatches(raw json.RawMessage, wantID int, want string) bool {
	s := strings.TrimSpace(string(raw))
	if s == want {
		return true
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str == want
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		if f, err := strconv.ParseFloat(num.String(), 64); err == nil && f == float64(wantID) {
			return true
		}
	}
	return false
}

func (c *Client) rejectServerRequest(id json.RawMessage, method string) {
	if len(id) == 0 || string(id) == "null" {
		return
	}
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": -32601, "message": "method not supported by panda client: " + method},
	})
	if err != nil {
		return
	}
	_ = c.writeLine(b)
}

// readLine reads one newline-terminated frame with bounded memory. Past
// maxResponseBytes it keeps consuming and discarding until the line ends,
// then reports errResponseTooLarge so the session is torn down rather than
// trying to resynchronize on an unknown amount of data.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	oversized := false
	for {
		frag, err := r.ReadSlice('\n')
		if !oversized {
			if len(buf)+len(frag) > maxResponseBytes {
				oversized = true
				buf = nil
			} else {
				buf = append(buf, frag...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if oversized {
			return nil, errResponseTooLarge
		}
		return buf, err
	}
}

// notify sends a JSON-RPC notification (no id, no response).
func (c *Client) notify(method string) error {
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return fmt.Errorf("mcp: marshal notification: %w", err)
	}
	if err := c.writeLine(b); err != nil {
		return fmt.Errorf("mcp: write notification: %w", err)
	}
	return nil
}
