// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package security

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func windowsSandboxGrantTestFile(t *testing.T, path, sidText string, mask uint32, protected bool) {
	t.Helper()
	sid, err := windows.StringToSid(sidText)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{AccessPermissions: windows.ACCESS_MASK(mask), AccessMode: windows.GRANT_ACCESS, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(sid)}}}, old)
	if err != nil {
		t.Fatal(err)
	}
	flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	if protected {
		flags |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

func windowsSandboxAssertOriginal(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatalf("outside/protected bytes changed even if WriteFile failed: %q %v", data, err)
	}
}

func TestWindowsSandboxDirectoryPolicy(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{ModeStandard, ModeStrict} {
		for _, public := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/public=%v", mode, public), func(t *testing.T) {
				work := t.TempDir()
				denied := filepath.Join(work, "denied")
				target := filepath.Join(denied, "child.txt")
				if err := os.Mkdir(denied, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
				windowsSandboxGrantTestFile(t, target, user.User.Sid.String(), sandboxFileAllAccess, public)
				if public {
					windowsSandboxGrantTestFile(t, target, "S-1-15-2-1", windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, true)
				}
				operation := "write"
				policy := Policy{Mode: mode, WorkDir: work, DenyWritePaths: []string{denied}}
				if mode == ModeStrict {
					operation = "read"
					policy.DenyReadPaths = []string{denied}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd := windowsSandboxTestCommand(t, ctx, policy, "assert-denied", operation, filepath.Join("denied", "child.txt"))
				out, runErr := cmd.CombinedOutput()
				if mode == ModeStrict && public {
					if runErr == nil || !strings.Contains(string(out), "publicly readable") || strings.Contains(string(out), "denied\n") {
						t.Fatalf("unrepresentable public read mask did not refuse launch: %v %s", runErr, out)
					}
				} else if runErr != nil || !strings.Contains(string(out), "denied") {
					t.Fatalf("directory policy: %v %s", runErr, out)
				}
				windowsSandboxAssertOriginal(t, target)
			})
		}
	}
}

func TestWindowsSandboxPublicOutsideWrite(t *testing.T) {
	for _, sid := range []string{"S-1-15-2-1", "S-1-15-2-2"} {
		t.Run(sid, func(t *testing.T) {
			root := t.TempDir()
			work := filepath.Join(root, "work")
			outside := filepath.Join(root, "outside.txt")
			if err := os.Mkdir(work, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			windowsSandboxGrantTestFile(t, outside, sid, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE, true)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := windowsSandboxTestCommand(t, ctx, Policy{Mode: ModeStandard, WorkDir: work}, "assert-denied", "write", filepath.Join("..", "outside.txt"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("public package write gate: %v %s", err, out)
			}
			windowsSandboxAssertOriginal(t, outside)
		})
	}
}

func TestWindowsSandboxHardlinks(t *testing.T) {
	for _, preexisting := range []bool{false, true} {
		t.Run(fmt.Sprint("preexisting=", preexisting), func(t *testing.T) {
			root := t.TempDir()
			work := filepath.Join(root, "work")
			outside := filepath.Join(root, "outside.txt")
			if err := os.Mkdir(work, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			if preexisting {
				if err := os.Link(outside, filepath.Join(work, "link.txt")); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := windowsSandboxTestCommand(t, ctx, Policy{Mode: ModeStandard, WorkDir: work}, "assert-denied", "new-link", outside)
			out, err := cmd.CombinedOutput()
			if preexisting {
				if err == nil || !strings.Contains(string(out), "multiply-linked") {
					t.Fatalf("preexisting shared inode was granted writes: %v %s", err, out)
				}
			} else if err != nil {
				t.Fatalf("new-link boundary: %v %s", err, out)
			}
			windowsSandboxAssertOriginal(t, outside)
		})
	}
}

func TestWindowsSandboxMissingReadHole(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	readRoot := filepath.Join(root, "readonly")
	for _, dir := range []string{work, readRoot} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	policy := Policy{Mode: ModeStrict, WorkDir: work, DenyReadPaths: []string{filepath.Join(readRoot, "future.txt")}}
	cmd := windowsSandboxTestCommand(t, ctx, policy, "assert-denied", "read", "future.txt", readRoot)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "protected path must exist") {
		t.Fatalf("missing hole in readable root did not refuse launch: %v %s", err, out)
	}
}

func TestWindowsSandboxWritableExceptionCannotReopenDeniedTree(t *testing.T) {
	for _, fileException := range []bool{false, true} {
		t.Run(fmt.Sprint("file=", fileException), func(t *testing.T) {
			work := t.TempDir()
			denied := filepath.Join(work, "protected")
			nested := filepath.Join(denied, "nested")
			target := filepath.Join(nested, "value.txt")
			if err := os.MkdirAll(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			policy := Policy{Mode: ModeStandard, WorkDir: work, DenyWritePaths: []string{denied}}
			if fileException {
				policy.WritableFiles = []string{target}
			} else {
				policy.WritablePaths = []string{nested}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := windowsSandboxTestCommand(t, ctx, policy, "assert-denied", "write", filepath.Join("protected", "nested", "value.txt"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("nested write exception: %v %s", err, out)
			}
			windowsSandboxAssertOriginal(t, target)
		})
	}
}
