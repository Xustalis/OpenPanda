// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package sessions

import (
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestGetDuringWindowsFileReplacement(t *testing.T) {
	store := NewStore(t.TempDir())
	sess, err := store.Create("live checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	path := store.path(sess.ID)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("temporary sharing conflict", func(t *testing.T) {
		h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if h != windows.InvalidHandle {
				windows.CloseHandle(h)
			}
		}()
		if _, err := os.ReadFile(path); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatalf("fixture did not create a real sharing conflict: %v", err)
		}
		type result struct {
			session *Session
			err     error
		}
		done := make(chan result, 1)
		go func() {
			got, err := NewStore(store.root).Get(sess.ID)
			done <- result{got, err}
		}()
		select {
		case got := <-done:
			t.Fatalf("read returned while replacement still held the file: %v", got.err)
		case <-time.After(20 * time.Millisecond):
		}
		if err := windows.CloseHandle(h); err != nil {
			t.Fatal(err)
		}
		h = windows.InvalidHandle
		select {
		case got := <-done:
			if got.err != nil || got.session == nil || got.session.Title != sess.Title {
				t.Fatalf("checkpoint did not recover after replacement: %+v %v", got.session, got.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("read did not settle after release")
		}
	})

	t.Run("persistent sharing conflict", func(t *testing.T) {
		h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(h)
		got, err := NewStore(store.root).Get(sess.ID)
		if got != nil || !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatalf("persistent conflict was hidden: %+v %v", got, err)
		}
	})
}
