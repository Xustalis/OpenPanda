// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package util

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteFileAtomicWithWindowsReader(t *testing.T) {
	for _, release := range []bool{true, false} {
		name := "persistent reader"
		if release {
			name = "temporary reader"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			reader, err := os.Open(path) // ordinary Go readers do not share DELETE
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			done := make(chan error, 1)
			go func() { done <- WriteFileAtomic(path, []byte("new"), 0o600) }()
			if release {
				select {
				case err := <-done:
					t.Fatalf("replacement returned before its temporary reader closed: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
				if err := reader.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if (err == nil) != release {
					t.Fatalf("release=%v replacement error=%v", release, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("replacement did not settle within its bounded retry window")
			}
			reader.Close()
			data, err := os.ReadFile(path)
			want := "old"
			if release {
				want = "new"
			}
			if err != nil || string(data) != want {
				t.Fatalf("content=%q err=%v, want %q", data, err, want)
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary replacement leaked: %+v %v", entries, err)
			}
		})
	}
}
