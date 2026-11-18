// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package security

import "testing"

// TestHiddenByTmpfs pins which programs the jail has to bind back after it
// replaces /tmp. The failing case is a test binary under /tmp/go-build…: the
// jail is otherwise correct and the child dies with "execvp: No such file or
// directory" before it runs.
func TestHiddenByTmpfs(t *testing.T) {
	cases := []struct {
		name   string
		exe    string
		target string
		ok     bool
	}{
		{"test binary", "/tmp/go-build1234/b493/commander.test", "/tmp/go-build1234/b493", true},
		{"directly in tmp", "/tmp/tool", "/tmp/tool", true},
		{"installed binary", "/usr/local/bin/panda", "", false},
		{"sibling of tmp", "/var/tmp/panda", "", false},
		{"tmp prefix but not under it", "/tmpfoo/panda", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target, ok := hiddenByTmpfs(c.exe)
			if ok != c.ok || target != c.target {
				t.Fatalf("hiddenByTmpfs(%q) = %q, %v; want %q, %v", c.exe, target, ok, c.target, c.ok)
			}
		})
	}
}
