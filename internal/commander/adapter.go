package commander

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/xenith/panda/internal/config"
	"github.com/xenith/panda/internal/security"
)

// adapterDir is where adapter scripts live. Resolved relative to the working
// directory; the install layout puts adapters/ next to the binary. It is a
// var so tests can point it at a temp dir.
var adapterDir = "adapters"

// AdapterRequest is written to the adapter's stdin.
type AdapterRequest struct {
	Prompt   string `json:"prompt"`
	TimeoutS int    `json:"timeout_s"`
	CWD      string `json:"cwd,omitempty"`
}

// modelEnv injects the model provider config into the adapter process env so
// adapters (e.g. the claude CLI) point at DeepSeek. Secrets are passed only
// via env and never echoed to logs.
func modelEnv(model config.ModelConfig) []string {
	env := []string{
		"ANTHROPIC_BASE_URL=" + model.BaseURL,
		"ANTHROPIC_API_KEY=" + model.APIKey,
		"ANTHROPIC_MODEL=" + model.Model,
	}
	return env
}

// runAdapterProcess spawns adapters/<name> with a JSON request on stdin and
// reads a JSON result from stdout. The model config is injected via env so the
// adapter reaches the configured provider; the subprocess is sandboxed to the
// task directory with a minimal environment (see security.Sandbox).
func runAdapterProcess(ctx context.Context, name string, prompt string, cwd string, model config.ModelConfig) AgentResult {
	// The model endpoint must be HTTPS so the API key never travels cleartext.
	if model.BaseURL != "" {
		if err := security.NewNetworkGuard().CheckURL(model.BaseURL); err != nil {
			return AgentResult{OK: false, Result: security.Redact(err.Error()), ExitCode: 1}
		}
	}

	req := AdapterRequest{Prompt: prompt, TimeoutS: 600, CWD: cwd}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return AgentResult{OK: false, Result: "bad adapter request", ExitCode: 1}
	}

	path := adapterDir + "/" + name
	cmd := exec.CommandContext(ctx, "python3", path)
	cmd.Stdin = bytes.NewReader(reqJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	security.NewSandbox(cwd).Apply(cmd, modelEnv(model)...)

	if err := cmd.Run(); err != nil {
		code := 1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		msg := stderr.String()
		if msg == "" {
			msg = err.Error()
		}
		return AgentResult{OK: false, Result: security.Redact(msg), ExitCode: code}
	}

	var out AgentResult
	out.ExitCode = 0
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return AgentResult{OK: false, Result: security.Redact(fmt.Sprintf("adapter output not JSON: %s", stdout.String())), ExitCode: 1}
	}
	// A well-behaved adapter returns JSON; scrub anything secret-shaped it may
	// have echoed into the result before it enters the task/log pipeline.
	out.Result = security.Redact(out.Result)
	return out
}
