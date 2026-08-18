package commander

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/executil"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// adapterDir is where adapter scripts live. Resolved relative to the working
// directory, so the daemon must run from the install root (adapters/ sits
// beside the binary in the repo layout) or adapters/ must be linked into the
// cwd — a bare binary copied to /usr/local/bin will not find them. It is a var
// so tests can point it at a temp dir.
var adapterDir = "adapters"

// adapterPath joins an adapter name under adapterDir, rejecting any name that
// could escape it via a path separator or a ".." element (P2-5). Adapter names
// are flat filenames, so anything path-like is a traversal attempt.
func adapterPath(name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid adapter name %q", name)
	}
	return filepath.Join(adapterDir, name), nil
}

// AdapterRequest is written to the adapter's stdin.
type AdapterRequest struct {
	Prompt   string `json:"prompt"`
	TimeoutS int    `json:"timeout_s"`
	CWD      string `json:"cwd,omitempty"`
}

// modelEnv injects the model provider config into the adapter process env so
// adapters (e.g. the claude CLI) point at DeepSeek. Secrets are passed only
// via env and never echoed to logs. Empty base_url/model fall back to the same
// defaults the entry model applies, so the adapter and entry never diverge.
func modelEnv(model config.ModelConfig) []string {
	base := model.BaseURL
	if base == "" {
		base = "https://api.deepseek.com/anthropic"
	}
	name := model.Model
	if name == "" {
		name = "deepseek-chat"
	}
	env := []string{
		"ANTHROPIC_BASE_URL=" + base,
		"ANTHROPIC_API_KEY=" + model.APIKey,
		"ANTHROPIC_MODEL=" + name,
	}
	return env
}

// adapterTimeoutS is the budget advertised to the adapter in its request.
// adapterHardTimeout is the enforced wall-clock limit (P1-18): TimeoutS used
// to be a polite JSON suggestion an adapter could ignore and run forever, so
// the Go side wraps the spawn in a hard context deadline slightly past the
// advertised budget. Combined with process-group cancellation (executil),
// hitting the deadline kills the adapter and every CLI it spawned.
// adapterHardTimeout is a var so tests can shrink it.
const adapterTimeoutS = 600

var adapterHardTimeout = (adapterTimeoutS + 30) * time.Second

// runAdapterProcess spawns adapters/<name> with a JSON request on stdin and
// reads a JSON result from stdout. The model config is injected via env so the
// adapter reaches the configured provider; the subprocess is sandboxed to the
// task directory with a minimal environment (see security.Sandbox).
func runAdapterProcess(ctx context.Context, name string, prompt string, cwd string, model config.ModelConfig) AgentResult {
	// The model endpoint must be HTTPS so the API key never travels cleartext,
	// and pinned to the configured host so the allowlist is never empty (D7).
	if model.BaseURL != "" {
		if err := security.NewNetworkGuard(security.EndpointHost(model.BaseURL)).CheckURL(model.BaseURL); err != nil {
			return AgentResult{OK: false, Result: security.Redact(err.Error()), ExitCode: 1}
		}
	}

	req := AdapterRequest{Prompt: prompt, TimeoutS: adapterTimeoutS, CWD: cwd}
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return AgentResult{OK: false, Result: "bad adapter request", ExitCode: 1}
	}

	path, err := adapterPath(name)
	if err != nil {
		return AgentResult{OK: false, Result: security.Redact(err.Error()), ExitCode: 1}
	}
	ctx, cancel := context.WithTimeout(ctx, adapterHardTimeout)
	defer cancel()
	cmd := executil.CommandContext(ctx, "python3", path)
	cmd.Stdin = bytes.NewReader(reqJSON)
	var stdout, stderr executil.Capture
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	security.NewSandbox(cwd).Apply(cmd, modelEnv(model)...)

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return AgentResult{OK: false, Result: "adapter timed out (hard limit)", ExitCode: 124}
		}
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
