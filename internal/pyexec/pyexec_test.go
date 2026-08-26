package pyexec

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
)

// Whatever this machine is, the resolver must agree with itself: if it claims an
// interpreter, that interpreter must run a script.
func TestResolvedInterpreterActuallyRunsAScript(t *testing.T) {
	argv := Interpreter()
	if argv == nil {
		t.Skip("no python on this host — nothing to verify")
	}
	script := t.TempDir() + "/hello.py"
	if err := os.WriteFile(script, []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, ok := Command(context.Background(), script)
	if !ok {
		t.Fatal("Command reported no interpreter after Interpreter returned one")
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v failed to run a script: %v", argv, err)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Errorf("script output = %q", out)
	}
}

// The cache must not be aliased: a caller that appends its script path to the
// returned slice would otherwise corrupt every later lookup.
func TestInterpreterDoesNotAliasItsCache(t *testing.T) {
	first := Interpreter()
	if first == nil {
		t.Skip("no python on this host")
	}
	_ = append(first, "poisoned.py") //nolint:gocritic // deliberate: prove the cache is copied
	second := Interpreter()
	if len(second) != len(first) || (len(second) > 0 && second[len(second)-1] == "poisoned.py") {
		t.Errorf("cache mutated by a caller: %v then %v", first, second)
	}
}

// The candidate order is the portability contract, so it is asserted directly:
// on Windows the py launcher must be tried before the bare name, because
// python3.exe there is commonly a Store alias that resolves and does nothing.
func TestCandidateOrder(t *testing.T) {
	got := defaultCandidates()
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	if runtime.GOOS == "windows" {
		if strings.Join(got[0], " ") != "py -3" {
			t.Errorf("first Windows candidate = %v, want the py launcher", got[0])
		}
	} else if got[0][0] != "python3" {
		t.Errorf("first POSIX candidate = %v, want python3", got[0])
	}
	// python2 must never be a candidate: the adapters use f-strings.
	for _, c := range got {
		if c[0] == "python2" {
			t.Error("python2 is a candidate")
		}
	}
}

// The override lets a host pin a venv or a versioned install, and it may carry
// arguments.
func TestEnvOverrideIsTriedFirst(t *testing.T) {
	t.Setenv(EnvOverride, "/opt/venv/bin/python -X utf8")
	got := candidates()
	if len(got) == 0 || strings.Join(got[0], " ") != "/opt/venv/bin/python -X utf8" {
		t.Fatalf("override not first: %v", got)
	}
	// The defaults stay behind it, so a stale override does not brick the node.
	if len(got) < 2 {
		t.Error("override replaced the default candidates instead of preceding them")
	}
}

// A candidate that is not on PATH, and one that exits non-zero, both fail the
// probe rather than being reported as usable.
func TestWorksRejectsMissingAndNonPython(t *testing.T) {
	if works([]string{"panda-no-such-interpreter-xyz"}) {
		t.Error("a missing binary passed the probe")
	}
	if works(nil) {
		t.Error("an empty argv passed the probe")
	}
	if runtime.GOOS != "windows" {
		// `true` exists and exits 0 but prints nothing: the marker check is
		// what separates it from a real interpreter.
		if works([]string{"true"}) {
			t.Error("a binary that prints nothing passed the probe")
		}
	}
}
