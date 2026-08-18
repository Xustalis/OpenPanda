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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/xenith/openpanda/internal/executil"
	"github.com/xenith/openpanda/internal/security"
)

// defaultCallTimeout is the hard upper bound for any single MCP request,
// matching the commander adapter's execution ceiling (P1-17 / A4).
const defaultCallTimeout = 630 * time.Second

// Tool is one tool advertised by an MCP server (the tools/list shape).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Client is a stdio MCP client bound to one child server process. Requests are
// serialized with a mutex because MCP stdio is a single ordered stream.
type Client struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      *bufio.Reader
	stdoutPipe  io.ReadCloser // closed on timeout/cleanup to unblock the reader goroutine
	mu          sync.Mutex
	nextID      int
	callTimeout time.Duration
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewStdioClient spawns command args... and performs the initialize handshake.
// The child runs with a minimal environment (M5): the security.Sandbox base
// (PATH and the usual session variables) plus the explicitly declared env —
// the parent's full environment is no longer inherited, so a server only sees
// the variables the caller grants it. It returns a started, initialized
// client; the caller owns it and must Close it.
func NewStdioClient(ctx context.Context, command string, env []string, args ...string) (*Client, error) {
	cmd := executil.CommandContext(ctx, command, args...)
	security.NewSandbox("").Apply(cmd, env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	cmd.Stderr = nil // a server's stderr is not part of the JSON-RPC stream
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %s: %w", command, err)
	}
	c := &Client{cmd: cmd, stdin: stdin, stdoutPipe: stdoutPipe, stdout: bufio.NewReader(stdoutPipe), callTimeout: defaultCallTimeout}
	if err := c.initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// initialize sends the initialize request and the following initialized
// notification (the MCP handshake).
func (c *Client) initialize(ctx context.Context) error {
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "panda", "version": "0.1.0"},
	}, &result); err != nil {
		return err
	}
	return c.notify("notifications/initialized")
}

// ListTools returns the tools the server advertises.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var result struct {
		Tools []Tool `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// CallTool invokes a tool and returns the concatenated text content.
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
	if result.IsError {
		return "", fmt.Errorf("mcp: tool %q reported an error", name)
	}
	var b strings.Builder
	for _, c := range result.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String(), nil
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

// call sends one JSON-RPC request and reads the matching response. It assumes a
// newline-delimited stream and that the server does not interleave unsolicited
// notifications with responses (true of the filesystem/git/fetch servers PANDA
// targets). A hard callTimeout bounds the wait; on expiry the whole child
// process group is killed so a hung MCP server cannot stall the ask loop.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	req := rpcRequest{JSONRPC: "2.0", ID: c.nextID, Method: method, Params: params}
	b, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("mcp: marshal request: %w", err)
	}
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("mcp: write request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()

	type result struct {
		resp rpcResponse
		err  error
	}
	respCh := make(chan result, 1)
	go func() {
		r, e := c.readResponse()
		respCh <- result{resp: r, err: e}
	}()

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
		return fmt.Errorf("mcp: call %s timed out after %s: %w", method, c.callTimeout, callCtx.Err())
	case res := <-respCh:
		if res.err != nil {
			return res.err
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

// readResponse reads lines from stdout, skipping notifications (messages with
// no id, e.g. logging/progress), and returns the first JSON-RPC response.
func (c *Client) readResponse() (rpcResponse, error) {
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			return rpcResponse{}, fmt.Errorf("mcp: read response: %w", err)
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			return rpcResponse{}, fmt.Errorf("mcp: parse response: %w", err)
		}
		if resp.ID == 0 {
			continue // notification, not the response we are waiting for
		}
		return resp, nil
	}
}

// notify sends a JSON-RPC notification (no id, no response).
func (c *Client) notify(method string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("mcp: write notification: %w", err)
	}
	return nil
}
