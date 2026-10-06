package commander

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/agents"
	"github.com/Xustalis/OpenPanda/internal/executil"
	"github.com/Xustalis/OpenPanda/internal/security"
)

// This file is the Go-native implementation of the generic adapter contract
// (adapters/generic.py). A card that declares
//
//	agents:
//	  mytool:
//	    adapter: "generic"          # no .py — runs without a Python runtime
//	    install_check: "mytool --version"
//	    command: "mytool --headless --prompt {prompt} --workdir {cwd}"
//
// gets the same argv-template expansion and the same exit-code mapping
// (124 timeout / 127 missing binary / 126 not executable / 2 bad template)
// with zero interpreter dependency — the portable execution surface for
// devices where adapters/ scripts cannot run: bare Windows hosts, minimal
// containers, edge boards. Placeholders are literal argv substitutions,
// never shell syntax, identical to generic.py:
//
//	{prompt} {stdin} {cwd} {resume} {max_turns} {task_id} {timeout_s}
//	{effort} {system_prompt} {env:NAME}
//
// The same fail-closed rules apply: no restricted read-only face (an
// unconsented remote task is refused upstream), no tools_policy enforcement
// beyond what the template declares, no model-env injection (an unknown
// generic agent resolves no credential manifest).
//
// envPlaceholderRe matches {env:NAME} — a card-declared env var reference,
// resolved from the process environment at expansion time (see generic.py).
var envPlaceholderRe = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

// templateEnvRefs returns the distinct env-var names a generic template
// references through {env:NAME}. The commander forwards those daemon-env
// values into the script adapter's filtered environment so generic.py
// expands against the same set the native path resolves directly — the
// sandbox still drops every name the card did not declare.
func templateEnvRefs(tpl string) []string {
	seen := map[string]bool{}
	var names []string
	for _, m := range envPlaceholderRe.FindAllStringSubmatch(tpl, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}

// expandGenericTemplate argv-expands the card's command template exactly like
// generic.py's expand(): returns (argv, prompt_via_stdin), argv nil when the
// template is empty or expands to nothing.
func expandGenericTemplate(req AdapterRequest) ([]string, bool) {
	toks := splitArgv(req.Cmd)
	if len(toks) == 0 {
		return nil, false
	}
	cwd := req.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	optional := map[string]string{
		"{cwd}":           cwd,
		"{resume}":        req.Resume,
		"{task_id}":       req.TaskID,
		"{effort}":        req.Effort,
		"{system_prompt}": req.SystemPrompt,
	}
	if req.MaxTurns > 0 {
		optional["{max_turns}"] = strconv.Itoa(req.MaxTurns)
	} else {
		optional["{max_turns}"] = ""
	}
	if req.TimeoutS > 0 {
		optional["{timeout_s}"] = strconv.Itoa(req.TimeoutS)
	} else {
		optional["{timeout_s}"] = ""
	}
	var out []string
	seenPrompt, viaStdin := false, false
	for _, arg := range toks {
		if arg == "{stdin}" {
			viaStdin = true
			continue
		}
		missingEnv := false
		expanded := envPlaceholderRe.ReplaceAllStringFunc(arg, func(m string) string {
			v := os.Getenv(envPlaceholderRe.FindStringSubmatch(m)[1])
			if v == "" {
				missingEnv = true
			}
			return v
		})
		unset := ""
		for ph, v := range optional {
			if v == "" && strings.Contains(arg, ph) {
				unset = ph
				break
			}
		}
		if missingEnv || unset != "" {
			// Unresolved optional placeholder: the element drops, and a bare
			// placeholder ("--flag {x}") takes its introducing flag with it —
			// a dangling flag would otherwise eat the next element.
			bare := arg == unset || envPlaceholderRe.FindString(arg) == arg
			if bare && len(out) > 0 && strings.HasPrefix(out[len(out)-1], "-") &&
				!strings.Contains(out[len(out)-1], "=") {
				out = out[:len(out)-1]
			}
			continue
		}
		arg = expanded
		if arg == "{prompt}" {
			out = append(out, req.Prompt)
			seenPrompt = true
			continue
		}
		if strings.Contains(arg, "{prompt}") {
			arg = strings.ReplaceAll(arg, "{prompt}", req.Prompt)
			seenPrompt = true
		}
		for ph, v := range optional {
			if v != "" && strings.Contains(arg, ph) {
				arg = strings.ReplaceAll(arg, ph, v)
			}
		}
		out = append(out, arg)
	}
	if len(out) == 0 {
		return nil, viaStdin
	}
	if !seenPrompt && !viaStdin {
		out = append(out, req.Prompt)
	}
	return out, viaStdin
}

// runGenericNative executes a card-declared argv template directly — the
// Python-free equivalent of spawning generic.py. It exists so a node with no
// interpreter can still serve as an execution endpoint for arbitrary CLIs,
// drivers and platform tools. The request's own timeout drives the context
// deadline; the run rides the same sandbox policy as a Python adapter.
func runGenericNative(ctx context.Context, agentName, prompt, cwd string, env []string) AgentResult {
	// buildAdapterRequest is shared with the script path: it enforces the
	// restricted-mode refusal (adapter: "generic" declares no read-only
	// face, so an unconsented remote task is refused before expansion).
	req, timeout, refusal := buildAdapterRequest(ctx, agents.GenericNativeAdapter, prompt, cwd)
	if refusal != nil {
		return *refusal
	}
	argv, viaStdin := expandGenericTemplate(req)
	if len(argv) == 0 {
		return AgentResult{OK: false, ExitCode: 2,
			Result: "generic adapter: card agent.command template is empty, unparseable, or expands to nothing"}
	}
	// The script path enforces timeout_s inside the adapter with the Go-side
	// hard limit as backstop; the native path has no inner watchdog, so the
	// advertised budget itself is the deadline (plus the same grace).
	ctx, cancel := context.WithTimeout(ctx,
		time.Duration(timeout)*time.Second+hardTimeoutGrace)
	defer cancel()

	if tid := TaskID(ctx); tid != "" {
		env = append(append([]string(nil), env...), "PANDA_TASK_ID="+tid)
	}
	if sink, ok := ctx.Value(progressKey{}).(ProgressFunc); ok && sink != nil {
		sink("exec: "+argv[0], "")
	}
	cmd := executil.CommandContext(ctx, argv[0], argv[1:]...)
	if viaStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	var stdout, stderr executil.Capture
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// WaitDelay matches the adapter path: a detached grandchild holding the
	// pipes must not wedge Wait forever.
	cmd.WaitDelay = 5 * time.Second
	security.NewSandbox(cwd).ApplyPolicy(cmd,
		adapterSandboxPolicy(agentName, agents.GenericNativeAdapter, cwd), env...)

	err := cmd.Run()
	res := AgentResult{ExitCode: 0}
	res.Result = strings.TrimSpace(stdout.String())
	if res.Result == "" {
		res.Result = strings.TrimSpace(stderr.String())
	}
	switch {
	case err == nil:
		res.OK = true
		if res.Result == "" {
			res.Result = "(no output)"
		}
	case ctx.Err() == context.DeadlineExceeded:
		res.ExitCode = 124 // shell convention: timeout
		if res.Result == "" {
			res.Result = argv[0] + " timed out"
		}
	case ctx.Err() == context.Canceled:
		res.ExitCode = 130 // SIGINT
		if res.Result == "" {
			res.Result = argv[0] + " canceled"
		}
	default:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else if errors.Is(err, fs.ErrPermission) {
			res.ExitCode = 126 // present but not executable
		} else {
			res.ExitCode = 127 // spawn failure / binary missing
		}
		if res.Result == "" {
			res.Result = err.Error()
		}
	}
	res.Result = security.Redact(res.Result)
	res.Stderr = security.Redact(stderr.String())
	return res
}
