// SPDX-License-Identifier: AGPL-3.0-or-later

package executil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAugmentProcessPath: existing user bin dirs are appended, missing ones
// are skipped, existing PATH entries are preserved, and a second call is a
// no-op. This is the launchd/systemd/task-scheduler fix: without it the
// daemon cannot see the agent CLIs and advertises zero agents.
func TestAugmentProcessPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	grokBin := filepath.Join(home, ".grok", "bin")
	if err := os.MkdirAll(grokBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// ~/.cargo/bin deliberately NOT created: candidates that do not exist
	// must not be added (a stale entry makes exec.LookPath scan dead paths).

	sep := string(os.PathListSeparator)
	base := "/usr/bin" + sep + "/bin"
	if runtime.GOOS == "windows" {
		base = `C:\Windows\system32` + sep + `C:\Windows`
	}
	t.Setenv("PATH", base)

	AugmentProcessPath()

	got := filepath.SplitList(os.Getenv("PATH"))
	if len(got) < 3 {
		t.Fatalf("PATH not augmented: %v", got)
	}
	if got[0] != filepath.SplitList(base)[0] {
		t.Fatalf("existing entries must stay first, got %v", got)
	}
	joined := strings.Join(got, sep)
	for _, want := range []string{bin, grokBin} {
		if !strings.Contains(joined, want) {
			t.Fatalf("PATH missing %s: %v", want, got)
		}
	}
	if strings.Contains(joined, filepath.Join(home, ".cargo", "bin")) {
		t.Fatalf("non-existent dir was added: %v", got)
	}

	// Idempotent: the second call must not duplicate entries.
	AugmentProcessPath()
	if again := filepath.SplitList(os.Getenv("PATH")); len(again) != len(got) {
		t.Fatalf("AugmentProcessPath is not idempotent: %v -> %v", got, again)
	}
}
