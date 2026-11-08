// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain runs the fake MCP server when OPENPANDA_MCP_FAKE is set, so the client
// tests can spawn this test binary as a real stdio subprocess. The fake server
// speaks the JSON-RPC handshake the client expects and exits on stdin EOF.
func TestMain(m *testing.M) {
	if os.Getenv("OPENPANDA_MCP_FAKE") == "1" {
		fakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeServer reads newline-delimited JSON-RPC requests from stdin and writes the
// corresponding responses to stdout. It advertises one tool ("echo") and echoes
// the tool call's argument back, so the client round-trip is observable.
//
// The child inherits no environment, so each behaviour switch is passed
// explicitly through NewStdioClient's env argument.
func fakeServer() {
	noisy := os.Getenv("OPENPANDA_MCP_NOISY") == "1"
	paged := os.Getenv("OPENPANDA_MCP_PAGED") == "1"
	cursorLoop := os.Getenv("OPENPANDA_MCP_CURSOR_LOOP") == "1"
	strID := os.Getenv("OPENPANDA_MCP_STRID") == "1"
	huge := os.Getenv("OPENPANDA_MCP_HUGE") == "1"
	die := os.Getenv("OPENPANDA_MCP_DIE") == "1"
	v2 := os.Getenv("OPENPANDA_MCP_V2") == "1"

	if die {
		// Exit without ever answering initialize. The client sees stdout EOF
		// immediately; the stderr line is the only diagnostic available.
		if os.Getenv("OPENPANDA_MCP_DIE_SECRET") == "1" {
			fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY=sk-super-secret-value")
		}
		fmt.Fprintln(os.Stderr, "fake server: module not found")
		os.Exit(2)
	}

	// answer writes the frames a real server may interleave with a response,
	// then the response itself. A client that mistakes any of them for the
	// pending call's answer will fail loudly in TestResponseIDMatching.
	answer := func(id any, result any) {
		if noisy {
			fmt.Println(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}`)
			fmt.Println(`{"jsonrpc":"2.0","id":"srv-1","method":"ping"}`)
			fmt.Println(`{"jsonrpc":"2.0","id":999,"result":{"noise":true}}`)
		}
		if strID {
			id = fmt.Sprint(id)
		}
		respond(id, result)
	}

	tools := []map[string]any{
		{
			"name":        "echo",
			"description": "echo back the arguments",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"x": map[string]any{"type": "string"}},
			},
		},
		{
			"name":        "failing",
			"description": "always reports an error",
			"inputSchema": map[string]any{"type": "object"},
		},
		{
			"name":        "hang",
			"description": "never responds",
			"inputSchema": map[string]any{"type": "object"},
		},
		{
			"name":        "extra",
			"description": "only present on the second page",
			"inputSchema": map[string]any{"type": "object"},
		},
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			protocol := "2024-11-05"
			if v2 {
				protocol = "2025-06-18"
			}
			answer(req.ID, map[string]any{
				"protocolVersion": protocol,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fake", "version": "1.0"},
			})
		case "tools/list":
			if huge {
				// A single frame past maxResponseBytes: the reader must refuse
				// it with bounded memory instead of buffering the whole line.
				b, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"result": map[string]any{"tools": []any{}, "pad": strings.Repeat("x", maxResponseBytes)},
				})
				fmt.Println(string(b))
				break
			}
			if cursorLoop {
				answer(req.ID, map[string]any{"tools": tools[:1], "nextCursor": "same"})
				break
			}
			if paged {
				var p struct {
					Cursor string `json:"cursor"`
				}
				_ = json.Unmarshal(req.Params, &p)
				switch p.Cursor {
				case "":
					answer(req.ID, map[string]any{"tools": tools[:3], "nextCursor": "page-2"})
				case "page-2":
					answer(req.ID, map[string]any{"tools": tools[3:]})
				default:
					answer(req.ID, map[string]any{"tools": []map[string]any{}})
				}
				break
			}
			answer(req.ID, map[string]any{"tools": tools[:3]})
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			switch params.Name {
			case "failing":
				answer(req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "boom: disk is full"}},
					"isError": true,
				})
			case "image_only":
				answer(req.ID, map[string]any{
					"content": []map[string]any{{"type": "image", "data": "AAAA", "mimeType": "image/png"}},
				})
			case "getenv":
				// Not advertised in tools/list; used by the environment test to
				// observe which variables the child process actually received.
				answer(req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": os.Getenv(fmt.Sprintf("%v", params.Arguments["name"]))}},
				})
			case "hang":
				// Never respond, so the client must hit its call timeout and
				// kill the process group. Use a long sleep (not select{}) so the
				// Go runtime deadlock detector does not terminate the fake server.
				time.Sleep(1 * time.Hour)
			default:
				answer(req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("echo: %v", params.Arguments["x"])}},
				})
			}
		}
		// notifications (no id) are not answered. The -32601 reply to our
		// server-initiated ping arrives here with no method and is ignored.
	}
}

func respond(id any, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(b))
}

// fakeEnv is the standard child environment plus the behaviour switches under
// test. The sandbox does not inherit the parent's environment (M5).
func fakeEnv(switches ...string) []string {
	return append([]string{"OPENPANDA_MCP_FAKE=1"}, switches...)
}

func newFakeClient(t *testing.T, switches ...string) *Client {
	t.Helper()
	c, err := NewStdioClient(context.Background(), os.Args[0], fakeEnv(switches...))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestStdioRoundTrip(t *testing.T) {
	// Spawn this test binary as the fake server: OPENPANDA_MCP_FAKE makes TestMain
	// run fakeServer() instead of the test suite. It is passed explicitly
	// because the child no longer inherits the parent environment (M5).
	client, err := NewStdioClient(context.Background(), os.Args[0], []string{"OPENPANDA_MCP_FAKE=1"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 3 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, want echo + failing + hang", tools)
	}

	got, err := client.CallTool(context.Background(), "echo", map[string]any{"x": "hello"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("call result = %q, want it to echo hello", got)
	}
}

func TestCallToolError(t *testing.T) {
	client, err := NewStdioClient(context.Background(), os.Args[0], []string{"OPENPANDA_MCP_FAKE=1"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	// A server signalling isError must surface as an error, not silent text —
	// and the tool's own explanation must reach the model.
	_, err = client.CallTool(context.Background(), "failing", nil)
	if err == nil {
		t.Fatal("call tool: expected an error for an isError response")
	}
	if !strings.Contains(err.Error(), "boom: disk is full") {
		t.Fatalf("isError text was dropped: %v", err)
	}
}

// TestCallToolTimeoutKillsProcess verifies that a hung MCP tool call hits the
// hard timeout and kills the child process group, leaving the client unable to
// make further calls.
func TestCallToolTimeoutKillsProcess(t *testing.T) {
	client, err := NewStdioClient(context.Background(), os.Args[0], []string{"OPENPANDA_MCP_FAKE=1"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	client.callTimeout = 500 * time.Millisecond

	start := time.Now()
	_, err = client.CallTool(context.Background(), "hang", nil)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed > 2*client.callTimeout {
		t.Fatalf("timeout took too long: %v", elapsed)
	}

	// After the process group is killed, subsequent calls must fail fast on
	// the broken flag instead of writing into a dead pipe.
	_, err = client.CallTool(context.Background(), "echo", map[string]any{"x": "hello"})
	if err == nil {
		t.Fatal("expected error after process was killed")
	}
	if !strings.Contains(err.Error(), "client unusable") {
		t.Fatalf("expected a fast-fail broken client, got: %v", err)
	}
}

// TestServerSurvivesSpawnContextCancel pins the child-lifetime contract (A9):
// NewStdioClient's ctx bounds only the spawn+handshake. Callers pass
// request-scoped contexts (the settings PUT handler wraps a 30s timeout), so
// binding the child to that ctx killed the server moments after configuration.
// After the spawn ctx is canceled, the server must keep serving until Close.
func TestServerSurvivesSpawnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client, err := NewStdioClient(ctx, os.Args[0], []string{"OPENPANDA_MCP_FAKE=1"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	cancel() // the HTTP request ended — the server must not die with it

	// Give a hypothetical ctx watcher a beat to fire, then prove the server
	// still answers: before the fix this call raced a SIGKILL and failed.
	time.Sleep(100 * time.Millisecond)
	got, err := client.CallTool(context.Background(), "echo", map[string]any{"x": "alive"})
	if err != nil {
		t.Fatalf("call after spawn ctx cancel: %v", err)
	}
	if !strings.Contains(got, "alive") {
		t.Fatalf("call result = %q, want it to echo alive", got)
	}
}

// TestMinimalEnvironment verifies the MCP child no longer inherits the parent's
// environment (M5): only the sandbox base (PATH etc.) and explicitly declared
// variables are visible to the server.
func TestMinimalEnvironment(t *testing.T) {
	t.Setenv("OPENPANDA_MCP_LEAK_CHECK", "parent-secret")
	client, err := NewStdioClient(context.Background(), os.Args[0],
		[]string{"OPENPANDA_MCP_FAKE=1", "OPENPANDA_MCP_DECLARED=granted"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	got, err := client.CallTool(context.Background(), "getenv", map[string]any{"name": "OPENPANDA_MCP_LEAK_CHECK"})
	if err != nil {
		t.Fatalf("getenv: %v", err)
	}
	if got != "" {
		t.Fatalf("child inherited parent env OPENPANDA_MCP_LEAK_CHECK=%q", got)
	}

	got, err = client.CallTool(context.Background(), "getenv", map[string]any{"name": "OPENPANDA_MCP_DECLARED"})
	if err != nil {
		t.Fatalf("getenv: %v", err)
	}
	if got != "granted" {
		t.Fatalf("declared env not forwarded, got %q", got)
	}

	got, err = client.CallTool(context.Background(), "getenv", map[string]any{"name": "PATH"})
	if err != nil {
		t.Fatalf("getenv: %v", err)
	}
	if got == "" {
		t.Fatal("sandbox base env lost PATH")
	}
}

// TestResponseIDMatching: a server may interleave a progress notification,
// its own request (sampling/ping) and a response to another id on the same
// stream. Accepting the first frame with a non-zero id used to misattribute
// those to the pending call and decode their result as ours.
func TestResponseIDMatching(t *testing.T) {
	c := newFakeClient(t, "OPENPANDA_MCP_NOISY=1")
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools with interleaved frames: %v", err)
	}
	if len(tools) != 3 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, want echo + failing + hang", tools)
	}
	got, err := c.CallTool(context.Background(), "echo", map[string]any{"x": "hello"})
	if err != nil {
		t.Fatalf("call tool with interleaved frames: %v", err)
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("call result = %q, want it to echo hello", got)
	}
}

// TestStringResponseIDs pins the JSON-RPC allowance for string ids: the
// response struct used to decode id into an int, so a compliant server that
// echoes "1" instead of 1 failed with a parse error.
func TestStringResponseIDs(t *testing.T) {
	c := newFakeClient(t, "OPENPANDA_MCP_STRID=1")
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools with string ids: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("tools = %+v, want 3", tools)
	}
	got, err := c.CallTool(context.Background(), "echo", map[string]any{"x": "world"})
	if err != nil || !strings.Contains(got, "world") {
		t.Fatalf("call with string ids = (%q, %v)", got, err)
	}
}

// TestListToolsPagination: tools/list is cursor-paginated. Reading only the
// first page silently hid every tool after it.
func TestListToolsPagination(t *testing.T) {
	c := newFakeClient(t, "OPENPANDA_MCP_PAGED=1")
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list paged tools: %v", err)
	}
	if len(tools) != 4 || tools[3].Name != "extra" {
		t.Fatalf("paged tools = %+v, want all 4 including the second page", tools)
	}
}

func TestListToolsRepeatingCursor(t *testing.T) {
	c := newFakeClient(t, "OPENPANDA_MCP_CURSOR_LOOP=1")
	if _, err := c.ListTools(context.Background()); err == nil || !strings.Contains(err.Error(), "repeating cursor") {
		t.Fatalf("a self-referential cursor must be refused, got %v", err)
	}
}

func TestNegotiatedProtocolVersion(t *testing.T) {
	c := newFakeClient(t, "OPENPANDA_MCP_V2=1")
	if got := c.ProtocolVersion(); got != "2025-06-18" {
		t.Fatalf("protocolVersion = %q, want the server's negotiated 2025-06-18", got)
	}
}

// TestNonTextContentReported: an image/audio/resource result used to decode
// into an empty string, indistinguishable from a tool that returned nothing.
func TestNonTextContentReported(t *testing.T) {
	c := newFakeClient(t)
	got, err := c.CallTool(context.Background(), "image_only", nil)
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if !strings.Contains(got, "non-text content") || !strings.Contains(got, "image") {
		t.Fatalf("non-text result was silently dropped: %q", got)
	}
}

// TestOversizedResponseRejected mirrors the server's bounded request reader:
// a frame with no newline must not grow the buffer without limit, and the
// session must be marked unusable rather than resynchronized blindly.
func TestOversizedResponseRejected(t *testing.T) {
	c := newFakeClient(t, "OPENPANDA_MCP_HUGE=1")
	if _, err := c.ListTools(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("oversized frame must be refused with bounded memory, got %v", err)
	}
	if _, err := c.CallTool(context.Background(), "echo", nil); err == nil || !strings.Contains(err.Error(), "client unusable") {
		t.Fatalf("session must be marked broken after an oversized frame, got %v", err)
	}
}

// TestServerStderrInError: cmd.Stderr used to be discarded, so a server that
// failed at startup looked exactly like one that hung.
func TestServerStderrInError(t *testing.T) {
	_, err := NewStdioClient(context.Background(), os.Args[0], fakeEnv("OPENPANDA_MCP_DIE=1"))
	if err == nil {
		t.Fatal("expected a handshake failure when the server exits immediately")
	}
	if !strings.Contains(err.Error(), "module not found") {
		t.Fatalf("server stderr missing from failure: %v", err)
	}
}

// TestServerStderrIsRedacted: a third-party server can print a credential to
// stderr while failing. Capturing stderr for diagnostics must not turn that
// into a log/model leak — the project's redactor owns this, not a local regex.
func TestServerStderrIsRedacted(t *testing.T) {
	_, err := NewStdioClient(context.Background(), os.Args[0],
		fakeEnv("OPENPANDA_MCP_DIE=1", "OPENPANDA_MCP_DIE_SECRET=1"))
	if err == nil {
		t.Fatal("expected a handshake failure when the server exits immediately")
	}
	if strings.Contains(err.Error(), "sk-super-secret-value") {
		t.Fatalf("secret leaked into the error: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") || !strings.Contains(err.Error(), "module not found") {
		t.Fatalf("stderr must be redacted but still useful: %v", err)
	}
}
