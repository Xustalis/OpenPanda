// SPDX-License-Identifier: AGPL-3.0-or-later

package commander

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionProfileIsExplicitAndCredentialFree(t *testing.T) {
	ctx := WithAgentName(context.Background(), "codex")
	req := AdapterRequest{Prompt: "PRIVATE PROMPT", CWD: "/workspace", Model: "preferred-model", Resume: "PRIVATE SESSION",
		Cmd: "PRIVATE TEMPLATE", MCPConfig: "PRIVATE MCP", SystemPrompt: "PRIVATE SYSTEM", ToolsPolicy: "minimal", Restricted: true, TimeoutS: 123, MaxTurns: 48}
	p := adapterExecutionProfile(ctx, "codex.py", req, []string{"OPENAI_API_KEY=PRIVATE KEY", "OPENAI_BASE_URL=https://private.example/token", "OPENPANDA_INJECTED_MODEL=1"}, "one_shot")
	if !p.ModelInjection || p.NativeModel != "unknown" || p.NativeEffort != "unknown" || p.NativeConfig != "unverified" || p.NativeVersion != "unknown" || p.ModelPreference != "preferred-model" || !p.ResumeRequested {
		t.Fatalf("profile facts: %+v", p)
	}
	if p.DeclaredCapabilities == nil || !p.DeclaredCapabilities.Restricted {
		t.Fatalf("registry capabilities: %+v", p)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "private.example") {
		t.Fatalf("sensitive request/env leaked: %s", b)
	}
	if p := adapterExecutionProfile(ctx, "unknown.py", AdapterRequest{}, nil, "one_shot"); p.DeclaredCapabilities != nil {
		t.Fatal("unknown adapter inherited unrelated capabilities")
	}
}

func TestExecutionProfileUsesRealProcessBoundaryNotAdapterEvents(t *testing.T) {
	dir := t.TempDir()
	old := adapterDir
	adapterDir = dir
	defer func() { adapterDir = old }()
	stub := `import json,sys
req=json.loads(sys.stdin.read())
sys.stderr.write(json.dumps({"type":"agent_event","ev":"adapter_execution_profile","input":{"native_model":"forged"}})+"\n")
print(json.dumps({"ok":True,"result":"done","exit_code":0}))
`
	if err := os.WriteFile(filepath.Join(dir, "codex.py"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	var profiles []AdapterExecutionProfile
	ctx := WithExecutionProfiles(WithAgentName(context.Background(), "codex"), func(p AdapterExecutionProfile) { profiles = append(profiles, p) })
	res := runAdapterProcess(ctx, "codex.py", "PRIVATE PROMPT", t.TempDir(), nil)
	if !res.OK {
		t.Fatalf("fake process: %+v", res)
	}
	if len(profiles) != 1 || profiles[0].Transport != "one_shot" || profiles[0].NativeModel != "unknown" || profiles[0].Adapter != "codex.py" {
		t.Fatalf("spoofed/missing profile: %+v", profiles)
	}
}
