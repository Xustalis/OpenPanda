// Package pyexec resolves the Python interpreter that runs PANDA's agent
// adapters (adapters/*.py).
//
// It exists because "python3" is not a portable command name. A stock Windows
// install has no python3 on PATH at all: it ships the `py` launcher instead, and
// — worse than absent — it ships an app-execution alias called python3.exe that
// exists on PATH, resolves through LookPath, and does nothing but open the
// Microsoft Store. A node that assumed the name would advertise agent abilities,
// accept the delegated task, and fail at exec time, which in the flagship
// pipeline means the development stage is routed to the machine that cannot run
// it. Some Linux distributions and BSDs are the mirror image, shipping only
// `python`.
//
// So the interpreter is *probed*, not named: each candidate must actually run a
// trivial program and print the expected answer. The result is cached for the
// process lifetime — the probe costs one subprocess per candidate and the
// interpreter does not change under a running daemon.
package pyexec

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/executil"
)

// EnvOverride names the environment variable that pins the interpreter, for
// hosts where the right one is neither on PATH nor named conventionally (a venv,
// pyenv, a Windows install under a versioned directory).
const EnvOverride = "OPENPANDA_PYTHON"

// probeTimeout bounds one candidate. Generous: the Windows Store alias can take
// a second to decide it is not a Python, and a cold first run of a real
// interpreter on a slow SBC is not instant either.
const probeTimeout = 10 * time.Second

var (
	once     sync.Once
	resolved []string
)

// Interpreter returns the argv prefix that runs a Python script — {"python3"},
// {"python"}, or {"py", "-3"} — or nil when this machine has no usable Python.
//
// nil is a real answer and callers must treat it as one: a node without Python
// cannot run adapters, and saying so up front (see commander.Router.AgentViable)
// is what keeps the task on a machine that can.
func Interpreter() []string {
	once.Do(func() { resolved = resolve() })
	if resolved == nil {
		return nil
	}
	return append([]string(nil), resolved...) // callers append args; don't alias the cache
}

// Available reports whether an adapter can be launched at all on this host.
func Available() bool { return Interpreter() != nil }

// Command builds a command that runs script with the resolved interpreter. The
// bool is false when no interpreter exists, so the caller reports that instead
// of exec'ing a name that is not there.
func Command(ctx context.Context, script string, args ...string) (*exec.Cmd, bool) {
	argv := Interpreter()
	if argv == nil {
		return nil, false
	}
	argv = append(argv, script)
	argv = append(argv, args...)
	return executil.CommandContext(ctx, argv[0], argv[1:]...), true
}

// Describe renders the resolved interpreter for diagnostics (`panda doctor`),
// or "" when there is none.
func Describe() string { return strings.Join(Interpreter(), " ") }

// candidates lists the argv prefixes to probe, in preference order.
func candidates() [][]string {
	if v := strings.TrimSpace(os.Getenv(EnvOverride)); v != "" {
		// Split so the override can carry arguments ("py -3.12", "uv run python").
		return append([][]string{strings.Fields(v)}, defaultCandidates()...)
	}
	return defaultCandidates()
}

func defaultCandidates() [][]string {
	if runtime.GOOS == "windows" {
		// The py launcher first: it is what a normal Windows install provides,
		// it picks the newest interpreter, and it is not shadowed by the Store
		// alias. python3/python are tried after, for installs that opted into
		// "Add to PATH".
		return [][]string{{"py", "-3"}, {"python3"}, {"python"}, {"py"}}
	}
	return [][]string{{"python3"}, {"python"}}
}

// resolve probes each candidate and returns the first that is genuinely a
// Python 3. LookPath alone is not enough — see the package comment on the
// Windows Store alias — so the candidate has to execute and answer correctly.
func resolve() []string {
	for _, argv := range candidates() {
		if works(argv) {
			return argv
		}
	}
	return nil
}

// works runs `<argv> -c "print(...)"` and requires both a zero exit and the
// expected stdout. The marker includes the major version, so a `python` that is
// still Python 2 fails the check instead of being handed adapters it cannot
// parse (they use f-strings and would die with a SyntaxError mid-task).
func works(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	full := append(append([]string(nil), argv[1:]...), "-c", "import sys;print('panda-py'+str(sys.version_info[0]))")
	out, err := executil.CommandContext(ctx, argv[0], full...).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "panda-py3")
}
