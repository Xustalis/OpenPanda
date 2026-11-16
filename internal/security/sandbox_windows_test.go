// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package security

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// sandboxInheritStdin keeps a sandboxed child on the inherited handle. Left
// nil, os/exec opens the NUL device for stdin, and the AppContainer this test
// runs in has no device capabilities: on hosts that do not grant the null
// device to AppContainers the spawn fails with "open NUL: Access is denied."
func sandboxInheritStdin(cmd *exec.Cmd) {
	cmd.Stdin = os.Stdin
}

func TestWindowsSandboxPayload(t *testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 || len(os.Args) < i+3 || os.Args[i+1] != "panda-windows-sandbox-test" {
		return
	}
	args := os.Args[i+2:]
	switch args[0] {
	case "boundary":
		outside, secret, protected := args[1], args[2], args[3]
		if data, err := io.ReadAll(os.Stdin); err != nil || string(data) != "sandbox stdin" {
			t.Fatalf("stdin: %q %v", data, err)
		}
		if err := os.WriteFile("allowed.txt", []byte("allowed"), 0o600); err != nil {
			t.Fatalf("workdir write: %v", err)
		}
		temporary, err := os.CreateTemp("", "runtime-scratch-")
		if err != nil {
			t.Fatalf("private temporary file: %v", err)
		}
		if _, err = temporary.WriteString("temporary bytes"); err != nil {
			t.Fatal(err)
		}
		temporary.Close()
		fmt.Fprintln(os.Stdout, "sandbox temporary "+temporary.Name())
		if sandboxPathWithin(filepath.Dir(outside), temporary.Name()) {
			t.Fatalf("temporary path reopened the host's working tree: %s", temporary.Name())
		}
		for _, target := range []string{outside, protected, filepath.Dir(protected)} {
			if err := windows.SetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err == nil {
				t.Fatalf("sandbox reset policy DACL on %s", target)
			}
		}
		if err := os.WriteFile(outside, []byte("escaped"), 0o600); err == nil {
			t.Fatal("outside overwrite succeeded")
		}
		if _, err := os.ReadFile(outside); err != nil {
			t.Fatalf("declared readonly input: %v", err)
		}
		if err := os.WriteFile(outside+".new", []byte("escaped"), 0o600); err == nil {
			t.Fatal("outside creation succeeded")
		}
		if err := os.WriteFile(protected, []byte("escaped"), 0o600); err == nil {
			t.Fatal("protected overwrite succeeded")
		}
		if _, err := os.ReadFile(protected); err != nil {
			t.Fatalf("write protection also denied reads: %v", err)
		}
		if err := os.Remove(protected); err == nil {
			t.Fatal("protected deletion succeeded")
		}
		if err := os.Rename(filepath.Dir(protected), filepath.Dir(protected)+".moved"); err == nil {
			t.Fatal("protected ancestor rename succeeded")
		}
		_, readErr := os.ReadFile(secret)
		if args[4] == "strict" && readErr == nil {
			t.Fatal("strict secret read succeeded")
		}
		if args[4] == "standard" && readErr != nil {
			t.Fatalf("standard secret read unexpectedly denied: %v", readErr)
		}
		if err := os.WriteFile(secret, []byte("escaped"), 0o600); err == nil {
			t.Fatal("credential write succeeded")
		}
		exe, _ := os.Executable()
		child := exec.Command(exe, "-test.run=^TestWindowsSandboxPayload$", "--", "panda-windows-sandbox-test", "outside", outside+".child")
		sandboxInheritStdin(child)
		if out, err := child.CombinedOutput(); err != nil {
			t.Fatalf("descendant: %v: %s", err, out)
		}
		fmt.Fprintln(os.Stdout, "sandbox stdout")
		fmt.Fprintln(os.Stderr, "sandbox stderr")
	case "outside":
		if err := os.WriteFile(args[1], []byte("escaped"), 0o600); err == nil {
			t.Fatal("descendant escaped write boundary")
		}
	case "network":
		connection, err := net.DialTimeout("tcp", args[2], 3*time.Second)
		if err == nil {
			connection.Close()
		}
		if args[1] == "deny" && err == nil {
			t.Fatal("outbound connection allowed without capabilities")
		}
		if args[1] == "allow" && err != nil {
			t.Fatalf("network capability unavailable: %v", err)
		}
	case "wait":
		fmt.Fprintln(os.Stdout, "ready")
		time.Sleep(time.Hour)
	case "echo":
		if err := json.NewEncoder(os.Stdout).Encode(args[1:]); err != nil {
			t.Fatal(err)
		}
	case "dll-init-failure":
		os.Exit(0xc0000142)
	case "new-acl":
		if err := os.Mkdir("new-directory", 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("new-file.txt", []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"new-file.txt", "new-directory"} {
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err == nil {
				t.Fatalf("worker reset a newly created object's DACL: %s", path)
			}
		}
	case "wait-pid":
		fmt.Fprintln(os.Stdout, os.Getpid())
		time.Sleep(time.Hour)
	case "leave-child":
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		child := exec.Command(exe, "-test.run=^TestWindowsSandboxPayload$", "--", "panda-windows-sandbox-test", "wait")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		sandboxInheritStdin(child)
		if err = child.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, child.Process.Pid)
	case "nested-launch":
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		request := windowsSandboxRequest{Path: exe, Args: []string{exe, "-test.run=^$"}, Policy: Policy{Mode: ModeStandard, WorkDir: args[1]}, Nonce: "0123456789abcdef0123456789abcdef", ParentPID: uint32(os.Getpid())}
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		child := exec.Command(exe, windowsSandboxLaunchArgument)
		child.Env = append(windowsSandboxChildEnv(os.Environ()), windowsSandboxEnv+"COUNT=1", windowsSandboxEnv+"0="+base64.RawStdEncoding.EncodeToString(data))
		sandboxInheritStdin(child)
		out, err := child.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "refusing to launch a new sandbox from an AppContainer") {
			t.Fatalf("worker could reenter the trusted launcher: %v %s", err, out)
		}
		fmt.Fprintln(os.Stdout, "nested launch refused")
	case "assert-denied", "wait-read":
		if args[0] == "wait-read" {
			fmt.Fprintln(os.Stdout, "ready")
			if _, err := io.ReadAll(os.Stdin); err != nil {
				t.Fatal(err)
			}
			args = append([]string{"assert-denied", "read"}, args[1:]...)
		}
		if args[1] == "read" {
			if _, err := os.ReadFile(args[2]); err == nil {
				t.Fatal("read crossed the policy boundary")
			}
		} else if args[1] == "write" {
			if err := os.WriteFile(args[2], []byte("escaped"), 0o600); err == nil {
				t.Fatal("write crossed the policy boundary")
			}
		} else if args[1] == "new-link" {
			if _, err := os.ReadFile(args[2]); err != nil {
				t.Fatalf("readonly hardlink source was not readable: %v", err)
			}
			if err := os.Link(args[2], "new-link.txt"); err == nil {
				if err = os.WriteFile("new-link.txt", []byte("escaped"), 0o600); err == nil {
					t.Fatal("new hardlink crossed the write boundary")
				}
			}
		} else {
			t.Fatalf("unknown denied operation %s", args[1])
		}
		fmt.Fprintln(os.Stdout, "denied")
	default:
		t.Fatalf("unknown payload %s", args[0])
	}
	os.Exit(0)
}

func TestWindowsSandboxCancellationRestoresACL(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprint("wait-for-payload=", ready), func(t *testing.T) {
			work := t.TempDir()
			before := windowsSandboxDACL(t, work)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := windowsSandboxTestCommand(t, ctx, Policy{Mode: ModeStandard, WorkDir: work}, "wait")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if ready {
				got := make(chan error, 1)
				go func() {
					buf := make([]byte, 6)
					_, e := io.ReadFull(pipe, buf)
					if e == nil && string(buf) != "ready\n" {
						e = fmt.Errorf("unexpected ready response %q", buf)
					}
					got <- e
				}()
				select {
				case err = <-got:
					if err != nil {
						cancel()
						cmd.Wait()
						t.Fatalf("payload readiness: %v: %s", err, stderr.String())
					}
				case <-time.After(10 * time.Second):
					cancel()
					cmd.Wait()
					t.Fatal("sandbox never became ready")
				}
			}
			cancel()
			if err = cmd.Wait(); err == nil {
				t.Fatal("cancelled command reported success")
			}
			if after := windowsSandboxDACL(t, work); before != after {
				t.Fatalf("cancellation left ACL changes: %s\nbefore %s\nafter %s", stderr.String(), before, after)
			}
		})
	}
}

func windowsSandboxTestCommand(t *testing.T, ctx context.Context, p Policy, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv := append([]string{"-test.run=^TestWindowsSandboxPayload$", "--", "panda-windows-sandbox-test"}, args...)
	cmd := exec.CommandContext(ctx, exe, argv...)
	if err := NewSandbox(p.WorkDir).ApplyPolicy(cmd, p, "PATH="+filepath.Join(os.Getenv("SystemRoot"), "System32")); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestWindowsSandboxBoundaries(t *testing.T) {
	if platformBackend() == "" {
		t.Skip("Windows AppContainer APIs are unavailable")
	}
	for _, mode := range []Mode{ModeStandard, ModeStrict} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			work := filepath.Join(root, "work")
			guard := filepath.Join(work, "guard")
			if err := os.MkdirAll(guard, 0o700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(root, "outside.txt")
			secret := filepath.Join(work, "secret.txt")
			protected := filepath.Join(guard, "protected.txt")
			for _, path := range []string{outside, secret, protected} {
				if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := windowsSandboxDACL(t, work)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := windowsSandboxTestCommand(t, ctx, Policy{Mode: mode, WorkDir: work, DenyWritePaths: []string{protected}, DenyReadPaths: []string{secret}}, "boundary", outside, secret, protected, string(mode))
			cmd.Stdin = strings.NewReader("sandbox stdin")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("sandbox command: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "sandbox stdout") || !strings.Contains(string(out), "sandbox stderr") {
				t.Fatalf("stdio lost: %s", out)
			}
			foundTemp := false
			for _, line := range strings.Split(string(out), "\n") {
				if path, ok := strings.CutPrefix(line, "sandbox temporary "); ok {
					foundTemp = true
					if _, err := os.Stat(filepath.Dir(strings.TrimSpace(path))); !os.IsNotExist(err) {
						t.Fatalf("private temporary directory remains after exit: %s: %v", path, err)
					}
				}
			}
			if !foundTemp {
				t.Fatal("payload did not report its temporary file")
			}
			for _, path := range []string{outside, secret, protected} {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "original" {
					t.Fatalf("protected original changed at %s: %q %v", path, data, err)
				}
			}
			if after := windowsSandboxDACL(t, work); before != after {
				t.Fatalf("sandbox left ACL changes:\nbefore %s\nafter %s", before, after)
			}
		})
	}
}

func TestWindowsSandboxNetwork(t *testing.T) {
	if platformBackend() == "" {
		t.Skip("Windows AppContainer APIs are unavailable")
	}
	address := os.Getenv("OPENPANDA_SANDBOX_NETWORK_TEST_ADDR")
	if address == "" {
		address = "www.microsoft.com:443"
	}
	// Socket creation alone is not a network access check: WFP enforces the
	// AppContainer capability when traffic leaves the process. Require an
	// unsandboxed positive control, then test both capability settings against
	// the very same reachable peer. Offline hosts cannot prove this boundary.
	control, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Skipf("network integration endpoint is unavailable: %v", err)
	}
	control.Close()
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			word := "deny"
			if allow {
				word = "allow"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := windowsSandboxTestCommand(t, ctx, Policy{Mode: ModeStandard, WorkDir: t.TempDir(), AllowNetwork: allow}, "network", word, address)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("network boundary: %v\n%s", err, out)
			}
		})
	}
}
