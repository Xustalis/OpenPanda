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
)

// Tool is one tool advertised by an MCP server (the tools/list shape).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Client is a stdio MCP client bound to one child server process. Requests are
// serialized with a mutex because MCP stdio is a single ordered stream.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
	nextID int
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
// The child inherits the parent's environment (a server may need its own env,
// e.g. a binary path). It returns a started, initialized client; the caller
// owns it and must Close it.
func NewStdioClient(ctx context.Context, command string, args ...string) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	cmd.Stderr = nil // a server's stderr is not part of the JSON-RPC stream
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %s: %w", command, err)
	}
	c := &Client{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
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

// Close closes the child's stdin and waits for it to exit.
func (c *Client) Close() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Wait()
	}
	return nil
}

// call sends one JSON-RPC request and reads the matching response. It assumes a
// newline-delimited stream and that the server does not interleave unsolicited
// notifications with responses (true of the filesystem/git/fetch servers PANDA
// targets).
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
	resp, err := c.readResponse()
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("mcp: rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("mcp: decode result: %w", err)
		}
	}
	return nil
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
