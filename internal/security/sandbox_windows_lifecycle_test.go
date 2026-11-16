// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package security

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsSandboxWaiting struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	input  io.WriteCloser
	output *bufio.Reader
	stderr bytes.Buffer
}

func TestWindowsSandboxDLLInitializationFailureIsDiagnosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := windowsSandboxTestCommand(t, ctx, Policy{Mode: ModeStandard, WorkDir: t.TempDir()}, "dll-init-failure")
	out, err := cmd.CombinedOutput()
	if err == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 0xc0000142 {
		t.Fatalf("DLL failure lost its process status: %v %s", err, out)
	}
	if !strings.Contains(string(out), ErrSandboxUnavailable.Error()) || !strings.Contains(string(out), "Isolation remains enabled") {
		t.Fatalf("DLL failure lacks its sandbox compatibility diagnosis: %s", out)
	}
}

func TestWindowsSandboxRootExitKillsDescendants(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := windowsSandboxTestCommand(t, ctx, Policy{Mode: ModeStandard, WorkDir: t.TempDir()}, "leave-child")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("root command: %v %s", err, out)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		t.Fatal("no descendant PID")
	}
	pid, err := strconv.ParseUint(fields[0], 10, 32)
	if err != nil {
		t.Fatalf("descendant PID: %q %v", out, err)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return // process object has already gone away
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	if state, err := windows.WaitForSingleObject(child, 0); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("root exit left a live descendant: %d %v", state, err)
	}
}

func TestWindowsSandboxCannotReenterLauncher(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	work := t.TempDir()
	cmd := windowsSandboxTestCommand(t, ctx, Policy{Mode: ModeStandard, WorkDir: work}, "nested-launch", filepath.Join(work, "nested"))
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "nested launch refused") {
		t.Fatalf("launcher token boundary: %v %s", err, out)
	}
}

func TestWindowsSandboxJournalRecordValidation(t *testing.T) {
	nonce := "fedcba98765432100123456789abcdef"
	deny, err := windowsSandboxDenySID(nonce)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := windows.UTF16PtrFromString("OpenPanda.Sandbox." + nonce)
	var app *windows.SID
	hr, _, _ := deriveProfileSID.Call(uintptr(unsafe.Pointer(profile)), uintptr(unsafe.Pointer(&app)))
	if int32(hr) < 0 {
		t.Fatalf("derive profile: %#x", hr)
	}
	defer windows.FreeSid(app)
	meta := windowsSandboxJournalRecord{Nonce: nonce, App: app.String(), Deny: deny.String()}
	header, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	header = append(header, '\n')
	wrong := meta
	wrong.App = "S-1-1-0"
	wrongHeader, err := json.Marshal(wrong)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		ok   bool
	}{
		{"empty", nil, true},
		{"partial-header", []byte(`{"nonce":`), true},
		{"complete-invalid-header", []byte("invalid\n"), false},
		{"partial-final-append", append(append([]byte(nil), header...), []byte(`{"path":`)...), true},
		{"complete-invalid-append", append(append([]byte(nil), header...), []byte("invalid\n")...), false},
		{"different-package-SID", append(wrongHeader, '\n'), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), nonce+".lease")
			if err := os.WriteFile(path, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			err = recoverWindowsSandboxJournal(f, nonce)
			f.Close()
			if (err == nil) != tc.ok {
				t.Fatalf("recovery result: %v; want success=%v", err, tc.ok)
			}
		})
	}
}

func startWindowsSandboxWaiting(t *testing.T, policy Policy, args ...string) *windowsSandboxWaiting {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	w := &windowsSandboxWaiting{cancel: cancel}
	w.cmd = windowsSandboxTestCommand(t, ctx, policy, args...)
	w.cmd.Stderr = &w.stderr
	var err error
	if w.input, err = w.cmd.StdinPipe(); err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := w.cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	w.output = bufio.NewReader(output)
	if err = w.cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if w.cmd.ProcessState == nil {
			_ = w.cmd.Wait()
		}
	})
	return w
}

func (w *windowsSandboxWaiting) ready(t *testing.T) string {
	t.Helper()
	line, err := w.output.ReadString('\n')
	if err != nil {
		w.cancel()
		_ = w.cmd.Wait()
		t.Fatalf("sandbox readiness: %v %q %s", err, line, w.stderr.String())
	}
	return strings.TrimSpace(line)
}

func windowsSandboxDACL(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	// Request all compared fields: DACL-only queries can incidentally return
	// owner/group before the first SetSecurityInfo but omit them afterwards.
	// AI records that Windows recomputed inheritance; it changes neither the
	// ACEs nor the inheritance barrier. Normalize that marker in this snapshot,
	// while keeping the owner, group, protected bit and every ACE unchanged.
	if err = sd.SetControl(windows.SE_DACL_AUTO_INHERITED, 0); err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

func TestWindowsSandboxACLComparisonDetectsPermissionChanges(t *testing.T) {
	path := t.TempDir()
	before := windowsSandboxDACL(t, path)
	windowsSandboxGrantTestFile(t, path, "S-1-15-2-1", windows.FILE_GENERIC_WRITE, false)
	if after := windowsSandboxDACL(t, path); after == before {
		t.Fatal("ACL comparison hid a new package write grant")
	}
}

func TestWindowsSandboxTemporaryCleanupDoesNotFollowJunction(t *testing.T) {
	outside := t.TempDir()
	marker := filepath.Join(outside, "original.txt")
	if err := os.WriteFile(marker, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	link := filepath.Join(private, "junction")
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/d", "/c", "mklink", "/J", link, outside)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create private junction fixture: %v %s", err, out)
	}
	if err := windowsSandboxEmptyTemporary(private); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(private)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private temporary contents remain: %v %v", entries, err)
	}
	windowsSandboxAssertOriginal(t, marker)
}

func TestWindowsSandboxConcurrentReadMasks(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	root := t.TempDir()
	secretDir := filepath.Join(root, "secret")
	secret := filepath.Join(secretDir, "value.txt")
	if err := os.Mkdir(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := windowsSandboxDACL(t, secretDir)
	policy := Policy{Mode: ModeStrict, WorkDir: t.TempDir(), DenyReadPaths: []string{secretDir}}
	first := startWindowsSandboxWaiting(t, policy, "wait-read", secret, root)
	if got := first.ready(t); got != "ready" {
		t.Fatalf("first ready: %s", got)
	}
	policy.WorkDir = t.TempDir()
	second := startWindowsSandboxWaiting(t, policy, "wait-read", secret, root)
	if got := second.ready(t); got != "ready" {
		t.Fatalf("second ready: %s", got)
	}
	// first's exit must not restore inheritance while second's package grant
	// still exists on the common readonly parent.
	first.cancel()
	if err := first.cmd.Wait(); err == nil {
		t.Fatal("cancelled first sandbox reported success")
	}
	if err := second.input.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(second.output)
	if err != nil {
		t.Fatal(err)
	}
	if err = second.cmd.Wait(); err != nil || !strings.Contains(string(out), "denied") {
		t.Fatalf("other lease's cleanup reopened a live read hole: %v %s %s", err, out, second.stderr.String())
	}
	if after := windowsSandboxDACL(t, secretDir); after != before {
		t.Fatalf("read-mask inheritance not restored:\nbefore %s\nafter %s", before, after)
	}
	windowsSandboxAssertOriginal(t, secret)
}

func TestWindowsSandboxReadMaskCoversNewChildren(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	root := t.TempDir()
	denied := filepath.Join(root, "denied")
	if err := os.Mkdir(denied, 0o700); err != nil {
		t.Fatal(err)
	}
	future := filepath.Join(denied, "future.txt")
	w := startWindowsSandboxWaiting(t, Policy{Mode: ModeStrict, WorkDir: t.TempDir(), DenyReadPaths: []string{denied}}, "wait-read", future, root)
	if got := w.ready(t); got != "ready" {
		t.Fatal(got)
	}
	if err := os.WriteFile(future, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	windowsSandboxGrantTestFile(t, future, user.User.Sid.String(), sandboxFileAllAccess, false)
	if err = w.input.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(w.output)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.cmd.Wait(); err != nil || !strings.Contains(string(out), "denied") {
		t.Fatalf("new child bypassed directory read hole: %v %s %s", err, out, w.stderr.String())
	}
}

func TestWindowsSandboxCrashRecovery(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("LOCALAPPDATA", cache)
	work := t.TempDir()
	denied := filepath.Join(work, "secret")
	if err := os.Mkdir(denied, 0o700); err != nil {
		t.Fatal(err)
	}
	beforeWork, beforeSecret := windowsSandboxDACL(t, work), windowsSandboxDACL(t, denied)
	w := startWindowsSandboxWaiting(t, Policy{Mode: ModeStrict, WorkDir: work, DenyReadPaths: []string{denied}}, "wait-pid")
	pid, err := strconv.ParseUint(w.ready(t), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	// Simulate an abrupt launcher crash without its graceful cancel handler.
	if err = w.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = w.cmd.Wait(); err == nil {
		t.Fatal("killed launcher reported success")
	}
	if state, err := windows.WaitForSingleObject(child, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("launcher crash stranded a sandbox descendant: %d %v", state, err)
	}
	control := filepath.Join(cache, "OpenPanda", "sandbox-leases")
	entries, err := os.ReadDir(control)
	if err != nil {
		t.Fatal(err)
	}
	leases := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".lease") {
			leases++
			// An interrupted final write must not hide earlier complete ACL
			// cleanup records or prevent their recovery.
			f, err := os.OpenFile(filepath.Join(control, entry.Name()), os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := fmt.Fprint(f, `{"path":`)
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("append partial record: %v %v", writeErr, closeErr)
			}
		}
	}
	if leases != 1 {
		t.Fatalf("crashed launcher left %d lease journals, want one", leases)
	}
	if err = recoverWindowsSandboxJournals(control); err != nil {
		t.Fatal(err)
	}
	if after := windowsSandboxDACL(t, work); after != beforeWork {
		t.Fatalf("work ACL not recovered:\nbefore %s\nafter %s", beforeWork, after)
	}
	if after := windowsSandboxDACL(t, denied); after != beforeSecret {
		t.Fatalf("read hole not recovered:\nbefore %s\nafter %s", beforeSecret, after)
	}
	entries, err = os.ReadDir(control)
	if err != nil || len(entries) != 0 {
		t.Fatalf("recovery state remains: %v %v", entries, err)
	}
	entries, err = os.ReadDir(filepath.Join(cache, "Packages"))
	if err != nil && !os.IsNotExist(err) || len(entries) != 0 {
		t.Fatalf("crashed sandbox's projected profile remains: %v %v", entries, err)
	}
}

func TestWindowsSandboxRecoveryRefusesReplacedObject(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("LOCALAPPDATA", cache)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	moved := filepath.Join(root, "moved")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	before := windowsSandboxDACL(t, work)
	w := startWindowsSandboxWaiting(t, Policy{Mode: ModeStandard, WorkDir: work}, "wait")
	if got := w.ready(t); got != "ready" {
		t.Fatal(got)
	}
	if err := w.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = w.cmd.Wait()
	if err := os.Rename(work, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementACL := windowsSandboxDACL(t, work)
	control := filepath.Join(cache, "OpenPanda", "sandbox-leases")
	if err := recoverWindowsSandboxJournals(control); err == nil || !strings.Contains(err.Error(), "was replaced") {
		t.Fatalf("replacement was not detected: %v", err)
	}
	if got := windowsSandboxDACL(t, work); got != replacementACL {
		t.Fatalf("recovery changed the replacement's ACL: %s", got)
	}
	// Finish cleanup using only this fixture's known moved object; production
	// recovery correctly requires that identity to be resolved explicitly.
	entries, err := os.ReadDir(control)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".lease") {
			continue
		}
		path := filepath.Join(control, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var rewritten bytes.Buffer
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
			var row windowsSandboxJournalRecord
			if err = json.Unmarshal(line, &row); err != nil {
				t.Fatal(err)
			}
			if strings.EqualFold(row.Path, work) {
				row.Path = moved
			}
			if err = json.NewEncoder(&rewritten).Encode(row); err != nil {
				t.Fatal(err)
			}
		}
		if err = os.WriteFile(path, rewritten.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err = recoverWindowsSandboxJournals(control); err != nil {
		t.Fatal(err)
	}
	if got := windowsSandboxDACL(t, moved); got != before {
		t.Fatalf("known original fixture ACL not recovered: %s", got)
	}
}
