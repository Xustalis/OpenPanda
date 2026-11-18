// SPDX-License-Identifier: AGPL-3.0-or-later

package finalaction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/util"
)

func fileIntent(t *testing.T) FileIntent {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := util.UUIDv7()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := DirectoryIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	return FileIntent{ActionID: id, RequestID: "request", AttemptID: "attempt", AccountID: "account", DeviceID: "device", InputRevision: "input", AssignmentRevision: "assignment", Generation: "1", Directory: dir, DirectoryIdentity: identity, Filename: "result.md", Content: "exact payload", SHA256: Hash([]byte("exact payload")), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
}
func TestFilePublicationRecoveryAndNoReplay(t *testing.T) {
	i := fileIntent(t)
	receipt, err := Publish(context.Background(), i, func(context.Context) error { return nil })
	if err != nil || receipt.SHA256 != i.SHA256 {
		t.Fatal(receipt, err)
	}
	// The receipt may have been lost in a crash. Reconcile must observe the
	// existing linked file, and a second dispatch must never overwrite it.
	got, err := Reconcile(context.Background(), i)
	if err != nil || got != receipt {
		t.Fatal(got, err)
	}
	if _, err = Publish(context.Background(), i, func(context.Context) error { return nil }); err == nil {
		t.Fatal("duplicate dispatch succeeded")
	}
	if err = os.WriteFile(filepath.Join(i.Directory, i.Filename), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Reconcile(context.Background(), i); err == nil {
		t.Fatal("changed contents accepted")
	}
}
func TestFilePublicationNeverClaimsExistingFile(t *testing.T) {
	i := fileIntent(t)
	if err := os.WriteFile(filepath.Join(i.Directory, i.Filename), []byte(i.Content), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(context.Background(), i, func(context.Context) error { return nil }); err == nil {
		t.Fatal("existing file overwritten")
	}
	if _, err := Reconcile(context.Background(), i); err == nil {
		t.Fatal("unrelated same-content file accepted")
	}
}
func TestFilePublicationCheckpointAndDestination(t *testing.T) {
	i := fileIntent(t)
	stop := errors.New("lease lost")
	if _, err := Publish(context.Background(), i, func(context.Context) error { return stop }); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(i.Directory, i.Filename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published after authority lost", err)
	}
	for _, name := range []string{"../outside", "sub/result", ".panda-action-spoof", `.\\outside`} {
		if ValidateDestination(i.Directory, name) == nil {
			t.Fatal("unsafe destination accepted", name)
		}
	}
	i.Content += "tampered"
	if i.Validate() == nil {
		t.Fatal("changed payload hash accepted")
	}
}

func TestFilePublicationHonorsCancellationAfterCheckpoint(t *testing.T) {
	i := fileIntent(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := Publish(ctx, i, func(context.Context) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(i.Directory, i.Filename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published after checkpoint cancellation: %v", err)
	}
}

func TestFilePublicationRejectsExpiredIntent(t *testing.T) {
	i := fileIntent(t)
	i.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	called := false
	_, err := Publish(context.Background(), i, func(context.Context) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("expired publication succeeded")
	}
	if called {
		t.Fatal("expired intent reached the authority checkpoint")
	}
	entries, err := os.ReadDir(i.Directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("expired intent left publication files: %v, %v", entries, err)
	}
}

func TestExpiredPublicationStillReconciles(t *testing.T) {
	i := fileIntent(t)
	want, err := Publish(context.Background(), i, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	i.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	got, err := Reconcile(context.Background(), i)
	if err != nil || got != want {
		t.Fatalf("lost completed publication evidence after expiry: %v, %v", got, err)
	}
}

func TestDirectoryReplacementCannotInheritConsent(t *testing.T) {
	i := fileIntent(t)
	old := i.Directory + "-original"
	_, err := Publish(context.Background(), i, func(context.Context) error {
		if e := os.Rename(i.Directory, old); e != nil {
			return e
		}
		t.Cleanup(func() { _ = os.RemoveAll(old) })
		return os.Mkdir(i.Directory, 0700)
	})
	if err == nil {
		t.Fatal("published through a replaced directory")
	}
	for _, dir := range []string{i.Directory, old} {
		if _, e := os.Stat(filepath.Join(dir, i.Filename)); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("unexpected publication in %s: %v", dir, e)
		}
	}
	if _, err = Reconcile(context.Background(), i); err == nil {
		t.Fatal("replaced directory reconciled")
	}
}
