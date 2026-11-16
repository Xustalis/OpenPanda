// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package security

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWindowsSandboxCommandLine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	cmd := exec.CommandContext(ctx, shell, "/d", "/c", "echo", "wrong-argv")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + shell + `" /d /c echo preserved-command-line`}
	work := t.TempDir()
	if err := NewSandbox(work).ApplyPolicy(cmd, Policy{Mode: ModeStandard, WorkDir: work}); err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "preserved-command-line" {
		t.Fatalf("custom Windows command line changed: %v %q", err, out)
	}
}

func TestWindowsSandboxRelativeExecutableAndArguments(t *testing.T) {
	work := filepath.Join(t.TempDir(), "space 雪")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	name := "helper 雪.exe"
	dest, err := os.Create(filepath.Join(work, name))
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dest, source)
	closeErr := dest.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy fixture executable: %v %v", copyErr, closeErr)
	}
	want := []string{"spaced value", "quote\"value", "尾巴\\"}
	args := append([]string{"-test.run=^TestWindowsSandboxPayload$", "--", "panda-windows-sandbox-test", "echo"}, want...)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Path = name // Win32 interprets a relative executable against cmd.Dir.
	if err = NewSandbox(work).ApplyPolicy(cmd, Policy{Mode: ModeStandard, WorkDir: work}); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("relative executable: %v %s", err, out)
	}
	var got []string
	if err = json.Unmarshal(out, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("argv changed: %q %v", out, err)
	}
}

func TestWindowsSandboxNewObjectACL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := windowsSandboxTestCommand(t, ctx, Policy{Mode: ModeStandard, WorkDir: t.TempDir()}, "new-acl")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("new file/directory policy mutation: %v %s", err, out)
	}
}
