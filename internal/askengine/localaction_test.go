// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// localEngine builds the minimum an action needs: a config for the panel
// address and a logger. Nothing else on Engine is read by the fast path.
func localEngine(cfg *config.Config) *Engine {
	return &Engine{cfg: cfg, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// TestRunLocalActionExecutesShortcut is the core of P0-2: a matched action
// runs the binary with the table's argv and its output becomes the answer, so
// no model is consulted.
func TestRunLocalActionExecutesShortcut(t *testing.T) {
	e := localEngine(&config.Config{})
	var got []string
	e.SetLocalActionRunner(func(_ context.Context, argv []string) (string, error) {
		got = argv
		return "node ready", nil
	})

	text, ok := e.RunLocalAction(context.Background(), entry.LocalAction{ID: "doctor", Argv: []string{"doctor"}}, i18n.ChineseSimp)
	if !ok {
		t.Fatal("local action reported fall-through, want handled")
	}
	if len(got) != 1 || got[0] != "doctor" {
		t.Fatalf("runner argv = %v, want [doctor]", got)
	}
	if !strings.Contains(text, "node ready") {
		t.Fatalf("answer %q does not carry the command output", text)
	}
	if !strings.Contains(text, "panda doctor") {
		t.Fatalf("answer %q does not name what ran", text)
	}
}

// TestRunLocalActionFallsThroughOnError guards the degradation rule: a local
// failure must return the caller to the expensive path, never an error, since
// the model may still know a better way.
func TestRunLocalActionFallsThroughOnError(t *testing.T) {
	e := localEngine(&config.Config{})
	e.SetLocalActionRunner(func(context.Context, []string) (string, error) {
		return "boom", errors.New("exit status 1")
	})
	if _, ok := e.RunLocalAction(context.Background(), entry.LocalAction{ID: "doctor", Argv: []string{"doctor"}}, i18n.ChineseSimp); ok {
		t.Fatal("failed action was reported as handled")
	}
}

// TestRunLocalActionReportsDoctorFindings pins the report rule: `doctor` exits
// non-zero exactly when it found something, so that finding is the answer and
// must not be thrown away in favour of a model call. A timeout, by contrast,
// is a failure of the action and still falls through.
func TestRunLocalActionReportsDoctorFindings(t *testing.T) {
	e := localEngine(&config.Config{})
	act := entry.LocalAction{ID: "doctor", Argv: []string{"doctor"}, ExitStatusIsReport: true}

	e.SetLocalActionRunner(func(context.Context, []string) (string, error) {
		return "shared_secret: missing\n3 checks failed", fakeExit()
	})
	text, ok := e.RunLocalAction(context.Background(), act, i18n.ChineseSimp)
	if !ok {
		t.Fatal("doctor findings were dropped instead of reported")
	}
	if !strings.Contains(text, "3 checks failed") {
		t.Fatalf("answer %q does not carry the findings", text)
	}

	e.SetLocalActionRunner(func(context.Context, []string) (string, error) {
		return "partial", fmt.Errorf("gave up: %w", context.DeadlineExceeded)
	})
	if _, ok := e.RunLocalAction(context.Background(), act, i18n.ChineseSimp); ok {
		t.Fatal("a timed-out doctor was reported as an answer")
	}

	// The web action never had findings to report; a non-zero exit there is a
	// real failure and must reach the task path. The panel address is pinned
	// to a closed port so the already-running shortcut cannot answer instead.
	web := localEngine(&config.Config{})
	web.cfg.Network.PanelAddr = "127.0.0.1:1"
	web.SetLocalActionRunner(func(context.Context, []string) (string, error) {
		return "port in use", fakeExit()
	})
	if _, ok := web.RunLocalAction(context.Background(), entry.LocalAction{ID: "web", Argv: []string{"web", "--daemon"}}, i18n.ChineseSimp); ok {
		t.Fatal("a failed console start was reported as an answer")
	}
}

// fakeExit builds the error shape os/exec returns for a non-zero status. It
// runs this test binary with an undefined flag, which exits 2 on every
// platform without needing a shell.
func fakeExit() error {
	return exec.Command(os.Args[0], "-openpanda-no-such-flag").Run()
}

// TestRunLocalActionRefusesSecondConsole pins the one side effect worth
// preventing: a console already answering on the panel port means the action
// must report the URL instead of starting a duplicate on the fallback port.
func TestRunLocalActionRefusesSecondConsole(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	e := localEngine(&config.Config{})
	e.cfg.Network.PanelAddr = ln.Addr().String()
	ran := false
	e.SetLocalActionRunner(func(context.Context, []string) (string, error) {
		ran = true
		return "", nil
	})

	text, ok := e.RunLocalAction(context.Background(), entry.LocalAction{ID: "web", Argv: []string{"web", "--daemon"}}, i18n.ChineseSimp)
	if !ok {
		t.Fatal("already-running console reported fall-through")
	}
	if ran {
		t.Fatal("started a second console instead of reporting the live one")
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	if !strings.Contains(text, port) {
		t.Fatalf("answer %q does not carry the live console address", text)
	}
}

// TestAddrsForURL covers the browser-facing rewrite: a wildcard bind means
// "this machine", so the URL must not be handed to a browser as 0.0.0.0.
func TestAddrsForURL(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:7840":   "127.0.0.1:7840",
		"[::]:7840":      "127.0.0.1:7840",
		"127.0.0.1:7840": "127.0.0.1:7840",
		"192.168.1.9:80": "192.168.1.9:80",
	}
	for in, want := range cases {
		if got := addrsForURL(in); got != want {
			t.Errorf("addrsForURL(%q) = %q, want %q", in, got, want)
		}
	}
}
