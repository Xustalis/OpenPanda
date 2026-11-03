// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps the package out of the operator's real data directory. The
// engine materializes an ability card at config.DefaultCardTarget when the
// config names none, and the node identity lock resolves through
// os.UserConfigDir() — neither reads the temp paths a test hands it. Without
// this, `go test ./internal/askengine` leaves capabilities.yaml and locks/ in
// ~/Library/Application Support/openpanda.
//
// GOCACHE and GOPATH are pinned before HOME moves so a test that shells out to
// the toolchain keeps its caches.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "panda-askengine-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "askengine tests: temp home: %v\n", err)
		os.Exit(1)
	}

	os.Setenv("GOCACHE", goEnv("GOCACHE"))
	os.Setenv("GOPATH", goEnv("GOPATH"))
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	os.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	os.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	os.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	os.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	// Tests run against the checkout's adapters/ even though the test binary
	// lives in a temp build dir: production resolution only trusts a cwd
	// directory that belongs to the running binary's install tree.
	os.Setenv("OPENPANDA_DEV_ADAPTERS", "1")

	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// goEnv returns the current value of a go env key, asking the toolchain when
// the environment carries none. Called before HOME changes.
func goEnv(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
