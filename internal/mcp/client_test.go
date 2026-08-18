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

// TestMain runs the fake MCP server when PANDA_MCP_FAKE is set, so the client
// tests can spawn this test binary as a real stdio subprocess. The fake server
// speaks the JSON-RPC handshake the client expects and exits on stdin EOF.
func TestMain(m *testing.M) {
	if os.Getenv("PANDA_MCP_FAKE") == "1" {
		fakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeServer reads newline-delimited JSON-RPC requests from stdin and writes the
// corresponding responses to stdout. It advertises one tool ("echo") and echoes
// the tool call's argument back, so the client round-trip is observable.
func fakeServer() {
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
			respond(req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fake", "version": "1.0"},
			})
		case "tools/list":
			respond(req.ID, map[string]any{
				"tools": []map[string]any{
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
				},
			})
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			switch params.Name {
			case "failing":
				respond(req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "boom"}},
					"isError": true,
				})
			case "getenv":
				// Not advertised in tools/list; used by the environment test to
				// observe which variables the child process actually received.
				respond(req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": os.Getenv(fmt.Sprintf("%v", params.Arguments["name"]))}},
				})
			case "hang":
				// Never respond, so the client must hit its call timeout and
				// kill the process group. Use a long sleep (not select{}) so the
				// Go runtime deadlock detector does not terminate the fake server.
				time.Sleep(1 * time.Hour)
			default:
				respond(req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("echo: %v", params.Arguments["x"])}},
				})
			}
		}
		// notifications (no id) are not answered.
	}
}

func respond(id any, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(b))
}

func TestStdioRoundTrip(t *testing.T) {
	// Spawn this test binary as the fake server: PANDA_MCP_FAKE makes TestMain
	// run fakeServer() instead of the test suite. It is passed explicitly
	// because the child no longer inherits the parent environment (M5).
	client, err := NewStdioClient(context.Background(), os.Args[0], []string{"PANDA_MCP_FAKE=1"})
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
	client, err := NewStdioClient(context.Background(), os.Args[0], []string{"PANDA_MCP_FAKE=1"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	// A server signalling isError must surface as an error, not silent text.
	if _, err := client.CallTool(context.Background(), "failing", nil); err == nil {
		t.Fatal("call tool: expected an error for an isError response")
	}
}

// TestCallToolTimeoutKillsProcess verifies that a hung MCP tool call hits the
// hard timeout and kills the child process group, leaving the client unable to
// make further calls.
func TestCallToolTimeoutKillsProcess(t *testing.T) {
	client, err := NewStdioClient(context.Background(), os.Args[0], []string{"PANDA_MCP_FAKE=1"})
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

	// After the process group is killed, subsequent calls must fail.
	_, err = client.CallTool(context.Background(), "echo", map[string]any{"x": "hello"})
	if err == nil {
		t.Fatal("expected error after process was killed")
	}
}

// TestMinimalEnvironment verifies the MCP child no longer inherits the parent's
// environment (M5): only the sandbox base (PATH etc.) and explicitly declared
// variables are visible to the server.
func TestMinimalEnvironment(t *testing.T) {
	t.Setenv("PANDA_MCP_LEAK_CHECK", "parent-secret")
	client, err := NewStdioClient(context.Background(), os.Args[0],
		[]string{"PANDA_MCP_FAKE=1", "PANDA_MCP_DECLARED=granted"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	got, err := client.CallTool(context.Background(), "getenv", map[string]any{"name": "PANDA_MCP_LEAK_CHECK"})
	if err != nil {
		t.Fatalf("getenv: %v", err)
	}
	if got != "" {
		t.Fatalf("child inherited parent env PANDA_MCP_LEAK_CHECK=%q", got)
	}

	got, err = client.CallTool(context.Background(), "getenv", map[string]any{"name": "PANDA_MCP_DECLARED"})
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
