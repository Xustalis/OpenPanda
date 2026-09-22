package mcpserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// serveOnce feeds a batch of request lines through the server and returns
// the response lines it wrote. Serve ends at EOF, so a fixed input makes the
// exchange synchronous — no pipes or goroutines needed.
func serveOnce(t *testing.T, s *Server, lines ...string) []map[string]json.RawMessage {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resps []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("response line not JSON: %q (%v)", line, err)
		}
		resps = append(resps, m)
	}
	return resps
}

func testTools() []Tool {
	return []Tool{
		{
			Name:        "echo",
			Description: "echoes the text argument",
			InputSchema: SchemaObject(map[string]any{"text": StringProp("what to echo")}, "text"),
			Handle: func(_ context.Context, args map[string]any) (string, error) {
				return Arg(args, "text"), nil
			},
		},
		{
			Name:        "fail",
			Description: "always errors",
			InputSchema: SchemaObject(nil),
			Handle: func(_ context.Context, _ map[string]any) (string, error) {
				return "", errors.New("boom")
			},
		},
	}
}

// TestHandshakeAndLifecycle covers the MCP opening exchange: initialize
// answers with protocolVersion + serverInfo, the initialized notification
// gets no reply, and ping answers with an empty result.
func TestHandshakeAndLifecycle(t *testing.T) {
	s := New("openpanda", "1.0-test", testTools())
	resps := serveOnce(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	)
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses (notification must be silent), got %d", len(resps))
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(resps[0]["result"], &init); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	if init.ProtocolVersion != ProtocolVersion || init.ServerInfo.Name != "openpanda" || init.ServerInfo.Version != "1.0-test" {
		t.Fatalf("initialize result wrong: %+v", init)
	}
	if string(resps[0]["id"]) != "1" || string(resps[1]["id"]) != "2" {
		t.Fatalf("ids not echoed: %s / %s", resps[0]["id"], resps[1]["id"])
	}
}

// TestToolsListAndCall covers tools/list advertisement (order, schemas) and
// tools/call's three outcomes: text content, handler error → isError
// content, and unknown tool → isError content.
func TestToolsListAndCall(t *testing.T) {
	s := New("openpanda", "1.0-test", testTools())
	resps := serveOnce(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fail","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"missing","arguments":{}}}`,
	)
	if len(resps) != 4 {
		t.Fatalf("expected 4 responses, got %d", len(resps))
	}
	var list struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resps[0]["result"], &list); err != nil {
		t.Fatalf("tools/list result: %v", err)
	}
	if len(list.Tools) != 2 || list.Tools[0].Name != "echo" || list.Tools[1].Name != "fail" {
		t.Fatalf("tools/list wrong: %+v", list.Tools)
	}
	if !strings.Contains(string(list.Tools[0].InputSchema), `"text"`) {
		t.Fatalf("schema not advertised: %s", list.Tools[0].InputSchema)
	}

	content := func(idx int) (string, bool) {
		t.Helper()
		var r struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(resps[idx]["result"], &r); err != nil {
			t.Fatalf("call result %d: %v", idx, err)
		}
		if len(r.Content) == 0 {
			t.Fatalf("call result %d has no content", idx)
		}
		return r.Content[0].Text, r.IsError
	}
	if text, isErr := content(1); isErr || text != "hi" {
		t.Fatalf("echo call = (%q, err=%v)", text, isErr)
	}
	if text, isErr := content(2); !isErr || !strings.Contains(text, "boom") {
		t.Fatalf("handler error must surface as isError content: (%q, %v)", text, isErr)
	}
	if text, isErr := content(3); !isErr || !strings.Contains(text, "missing") {
		t.Fatalf("unknown tool must surface as isError content: (%q, %v)", text, isErr)
	}
}

// TestProtocolErrors covers the JSON-RPC failure paths: unknown methods get
// -32601, malformed tools/call params get -32602, and an unparseable line
// gets -32700 with a null id.
func TestProtocolErrors(t *testing.T) {
	s := New("openpanda", "1.0-test", testTools())
	resps := serveOnce(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"arguments":{}}}`,
		`not-json-at-all`,
	)
	if len(resps) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(resps))
	}
	codes := []int{-32601, -32602, -32700}
	for i, want := range codes {
		var e struct {
			Code int `json:"code"`
		}
		if err := json.Unmarshal(resps[i]["error"], &e); err != nil || e.Code != want {
			t.Fatalf("response %d error = %s, want code %d", i, resps[i]["error"], want)
		}
	}
	if string(resps[2]["id"]) != "null" {
		t.Fatalf("parse error id must be null, got %s", resps[2]["id"])
	}
}

// TestOversizedRequestRejected: a request line past maxRequestBytes must not
// be buffered whole (bounded memory is the point), and the session must
// answer -32700 and stay alive for the request that follows it.
func TestOversizedRequestRejected(t *testing.T) {
	s := New("openpanda", "1.0-test", testTools())
	huge := `{"jsonrpc":"2.0","id":9,"method":"ping","pad":"` + strings.Repeat("x", maxRequestBytes) + `"}`
	resps := serveOnce(t, s, huge, `{"jsonrpc":"2.0","id":10,"method":"ping"}`)
	if len(resps) != 2 {
		t.Fatalf("expected error + ping response, got %d", len(resps))
	}
	var e struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(resps[0]["error"], &e); err != nil || e.Code != -32700 {
		t.Fatalf("oversized response = %s, want -32700", resps[0]["error"])
	}
	if string(resps[0]["id"]) != "null" {
		t.Fatalf("oversized id = %s, want null", resps[0]["id"])
	}
	if resps[1]["error"] != nil {
		t.Fatalf("ping after oversized request failed: %s", resps[1]["error"])
	}
}

// TestArgsHelpers pins the tolerant argument readers the tools rely on:
// missing keys are empty, []any decodes as []string, non-strings drop.
func TestArgsHelpers(t *testing.T) {
	args := map[string]any{
		"s":    "  x  ",
		"num":  3,
		"list": []any{"a", 7, "b"},
		"strs": []string{"c"},
	}
	if got := Arg(args, "s"); got != "  x  " {
		t.Fatalf("Arg = %q", got)
	}
	if got := Arg(args, "num"); got != "" {
		t.Fatalf("non-string Arg = %q", got)
	}
	if got := Arg(args, "missing"); got != "" {
		t.Fatalf("missing Arg = %q", got)
	}
	if got := fmt.Sprint(ArgList(args, "list")); got != "[a b]" {
		t.Fatalf("ArgList = %s", got)
	}
	if got := fmt.Sprint(ArgList(args, "strs")); got != "[c]" {
		t.Fatalf("ArgList strings = %s", got)
	}
	if got := ArgList(args, "missing"); len(got) != 0 {
		t.Fatalf("missing ArgList = %v", got)
	}
}
