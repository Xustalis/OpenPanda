// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package install

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The path StopServices removes must be the one `panda service install`
// writes. A drift here is silent until a logon relaunches a deleted binary.
func TestWebServiceUnitPathMatchesTheInstallerName(t *testing.T) {
	p := webServiceUnitPath()
	if p == "" {
		t.Fatal("no unit path")
	}
	base := filepath.Base(p)
	want := WebServiceUnitName
	if runtime.GOOS == "darwin" {
		want = WebServiceLabel + ".plist"
	}
	if base != want {
		t.Fatalf("unit path %q has base %q, want %q", p, base, want)
	}
	if !strings.Contains(p, filepath.Join("Library", "LaunchAgents")) && runtime.GOOS == "darwin" {
		t.Fatalf("darwin unit path %q is not under LaunchAgents", p)
	}
}
