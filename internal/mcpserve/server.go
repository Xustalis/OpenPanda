// Package mcpserve is the server half of the MCP stdio transport the agent
// tier already speaks as a client (internal/mcp): newline-delimited JSON-RPC
// 2.0 on a reader/writer pair, the initialize handshake, tools/list and
// tools/call. It exists so the panda binary can expose its own management
// surface — skills, queue, task submission, the mesh directory — as first-
// class agent tools instead of asking the model to guess at CLI flags.
//
// The server is deliberately transport-agnostic: Serve takes io.Reader /
// io.Writer so tests drive it over pipes while `panda mcp` wires os.Stdin /
// os.Stdout. Tool handlers return plain text; MCP wraps it in the content
// array the client unmarshals.
package mcpserve

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ProtocolVersion is the MCP revision this server answers. It mirrors the
// version the client half already sends, so both ends agree even before any
// negotiated downgrade logic exists.
const ProtocolVersion = "2024-11-05"

// Tool is one callable capability advertised in tools/list. Handle receives
// the decoded arguments object and returns the text payload the client
// concatenates; an error is reported as an isError tool result, not a
// JSON-RPC protocol failure (the protocol itself stayed healthy).
type Tool struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object ({"type":"object","properties":…}).
	// Kept as raw JSON so tools reuse schemas defined next to their
	// validation code without a schema-builder dependency.
	InputSchema json.RawMessage
	Handle      func(ctx context.Context, args map[string]any) (string, error)
}

// Server is one stdio MCP endpoint.
type Server struct {
	name    string
	version string
	tools   map[string]Tool
	order   []string // tools/list order: declaration order, deterministic
}

// New builds a server advertising tools in declaration order.
func New(name, version string, tools []Tool) *Server {
	s := &Server{name: name, version: version, tools: map[string]Tool{}}
	for _, t := range tools {
		s.tools[t.Name] = t
		s.order = append(s.order, t.Name)
	}
	return s
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// maxRequestBytes caps one request line. ReadBytes-style reads allocate the
// whole line before returning it, so an unbounded read lets a client that
// never sends '\n' grow memory without limit; past the cap the line is read
// and discarded, answered with a parse error, and the session continues.
const maxRequestBytes = 4 << 20

// errRequestTooLarge is readRequest's sentinel for a line that exceeded
// maxRequestBytes; Serve maps it to a -32700 response rather than ending the
// session.
var errRequestTooLarge = errors.New("mcpserve: request line exceeds limit")

// readRequest reads one newline-terminated request line. Past maxRequestBytes
// it keeps reading and discarding until the line ends — bounded memory for an
// unbounded input — then reports errRequestTooLarge.
func readRequest(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	oversized := false
	for {
		frag, err := r.ReadSlice('\n')
		if !oversized {
			if len(buf)+len(frag) > maxRequestBytes {
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
			return nil, errRequestTooLarge
		}
		return buf, err
	}
}

// Serve reads newline-delimited JSON-RPC requests until EOF or ctx cancel,
// writing one response line per request. Notifications (no id) get no
// response, matching the MCP lifecycle (notifications/initialized follows the
// handshake). A terminated line that fails to parse — including one that
// overflowed maxRequestBytes — answers with -32700 and a null id, the
// JSON-RPC convention for an unreadable request.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	write := func(resp rpcResponse) error {
		b, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
		return w.Flush()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := readRequest(r)
		if errors.Is(err, errRequestTooLarge) {
			if werr := write(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: codeParseError, Message: "request too large"}}); werr != nil {
				return werr
			}
			continue
		}
		if len(line) > 0 {
			if resp, ok := s.dispatch(ctx, line); ok {
				if werr := write(resp); werr != nil {
					return werr
				}
			}
		}
		if err != nil {
			return nil // EOF or reader failure ends the session quietly
		}
	}
}

// dispatch handles one raw request line. ok=false means the message was a
// notification and no response should be written.
func (s *Server) dispatch(ctx context.Context, line []byte) (rpcResponse, bool) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: codeParseError, Message: "parse error"}}, true
	}
	// Notifications carry no id: MCP servers must not answer them. That also
	// covers notifications/initialized after the handshake.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return rpcResponse{}, false
	}
	result, rerr := s.call(ctx, req.Method, req.Params)
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	return resp, true
}

func (s *Server) call(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		type advertised struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		}
		out := make([]advertised, 0, len(s.order))
		for _, name := range s.order {
			t := s.tools[name]
			out = append(out, advertised{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
		return map[string]any{"tools": out}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs name and arguments"}
		}
		t, ok := s.tools[p.Name]
		if !ok {
			return toolError(fmt.Sprintf("unknown tool %q", p.Name)), nil
		}
		text, err := t.Handle(ctx, p.Arguments)
		if err != nil {
			return toolError(err.Error()), nil
		}
		return toolText(text), nil
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + method}
	}
}

// toolText is the MCP content-array success shape.
func toolText(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}
}

// toolError is the same shape with isError set — the client surfaces the
// text to the model instead of treating it as a transport failure.
func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}

// SchemaObject is a small helper for declaring a tool's inputSchema without
// pulling in a schema library: properties is the field-name → subschema map,
// required the mandatory fields.
func SchemaObject(properties map[string]any, required ...string) json.RawMessage {
	s := map[string]any{"type": "object"}
	if len(properties) > 0 {
		s["properties"] = properties
	}
	if len(required) > 0 {
		s["required"] = required
	}
	b, _ := json.Marshal(s)
	return b
}

// StringProp is the {"type":"string","description":…} property shorthand.
func StringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// IntProp is the {"type":"integer","description":…} property shorthand.
func IntProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// BoolProp is the {"type":"boolean","description":…} property shorthand.
func BoolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

// StringArrayProp is the {"type":"array","items":{"type":"string"}} shorthand.
func StringArrayProp(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// Arg reads a string argument, trimming whitespace; missing keys return "".
func Arg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// ArgList reads a []string argument tolerating the []any JSON decode.
func ArgList(args map[string]any, key string) []string {
	var out []string
	switch v := args[key].(type) {
	case []string:
		out = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
