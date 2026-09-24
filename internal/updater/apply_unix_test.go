//go:build !windows

package updater

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBinary writes an executable that prints body on `version --json` —
// standing in for a staged release's bin/panda during checkStagedSchema.
func fakeBinary(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "panda")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' '"+body+"'\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return path
}

func floorFunc(v int) func(context.Context) (int, error) {
	return func(context.Context) (int, error) { return v, nil }
}

func TestCheckStagedSchema(t *testing.T) {
	tests := []struct {
		name    string
		floor   int
		body    string // fake binary's `version --json` output
		force   bool
		wantErr string // "" = allowed
	}{
		{"compatible release", 27, `{"schema": 27}`, false, ""},
		{"newer schema allowed", 25, `{"schema": 30}`, false, ""},
		{"older schema refused", 27, `{"schema": 25}`, false, "supports schema v25"},
		{"unreportable release refused", 27, `panda v0.0.8`, false, "cannot report its schema"},
		{"fresh db tolerates unreportable", 0, `panda v0.0.8`, false, ""},
		{"force bypasses", 27, `{"schema": 20}`, true, ""},
		{"staged db_schema raises floor", 25, `{"schema": 26, "db_schema": 27}`, false, "supports schema v26"},
		{"staged db_schema within ceiling", 25, `{"schema": 30, "db_schema": 27}`, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(Options{SchemaFloor: floorFunc(tt.floor), Force: tt.force})
			err := checkStagedSchema(context.Background(), m, fakeBinary(t, tt.body))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected allow, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// A nil SchemaFloor disables the guard entirely (daemon/notice-only manager).
func TestCheckStagedSchemaNoFloor(t *testing.T) {
	m := New(Options{})
	if err := checkStagedSchema(context.Background(), m, fakeBinary(t, `{"schema": 1}`)); err != nil {
		t.Fatalf("expected allow with nil floor, got %v", err)
	}
}
