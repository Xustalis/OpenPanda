// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain keeps the package out of the operator's real data directory. The
// panel's engine path materializes an ability card at
// config.DefaultCardTarget when the config names none, and the node identity
// lock resolves through os.UserConfigDir(); neither reads the temp paths a
// test hands it. Without this, `go test ./webui/panel` leaves
// capabilities.yaml and locks/ in ~/Library/Application Support/openpanda.
//
// GOCACHE and GOPATH are pinned before HOME moves so a test that shells out to
// the toolchain keeps its caches.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "panda-panel-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "panel tests: temp home: %v\n", err)
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

// removeTempDir deletes dir with a short retry, before t.TempDir's own cleanup
// reaches it. Go's TempDir cleanup calls os.RemoveAll exactly once; on macOS a
// directory under the shared temporary root can still be populated for a
// moment after its last writer leaves, because Spotlight and the AV file
// provider walk new directories asynchronously and an unlinkat landing in that
// window returns ENOTEMPTY. The result is a test that passes its body and then
// fails in cleanup with "TestX: TempDir RemoveAll cleanup: unlinkat …
// directory not empty" — a message about the harness, not about anything the
// code under test did.
//
// Fixtures call this from the same t.Cleanup that closes their engine, so it
// runs after the Close (which already drains queue runners and artifact
// adoptions) and before the TempDir cleanup registered earlier. Reporting the
// survivors rather than swallowing the error keeps a genuine leftover — a
// writer the Close path missed — visible and diagnosable.
func removeTempDir(t *testing.T, dir string) {
	t.Helper()
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
	}
	entries, _ := os.ReadDir(dir)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Errorf("temp dir %s survived cleanup: %v (leftovers: %v)", dir, err, names)
}
