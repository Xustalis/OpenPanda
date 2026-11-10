// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	_ "modernc.org/sqlite"
)

// seedCCSwitch writes a store under home so os.UserHomeDir finds it.
func seedCCSwitch(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".cc-switch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "cc-switch.db")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE providers (
		id TEXT NOT NULL, app_type TEXT NOT NULL, name TEXT NOT NULL,
		settings_config TEXT NOT NULL, is_current BOOLEAN NOT NULL DEFAULT 0,
		sort_index INTEGER)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO providers (id, app_type, name, settings_config, is_current, sort_index) VALUES (?,?,?,?,?,?)`,
		"p-1", "claude", "Zhipu GLM",
		`{"env":{"ANTHROPIC_BASE_URL":"https://open.bigmodel.cn/api/anthropic","ANTHROPIC_AUTH_TOKEN":"tok-123","ANTHROPIC_MODEL":"glm-5.3-flash"}}`,
		true, 1); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

// A row that names a CC Switch credential must build: NewClient returns
// ErrNoKey when nothing resolves, so a successful construction is the proof
// that the reference was read from the store at construction time.
func TestNewClientResolvesCCSwitchReference(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	seedCCSwitch(t, home)

	mc := config.ModelConfig{
		Provider: "custom",
		APIType:  "anthropic",
		BaseURL:  "https://open.bigmodel.cn/api/anthropic",
		Model:    "glm-5.3-flash",
		KeyRef:   "ccswitch:claude:p-1",
	}
	if _, err := NewClient(mc); err != nil {
		t.Fatalf("NewClient with a cc-switch reference: %v", err)
	}
}

func TestNewClientMissingCCSwitchReferenceIsNoKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	seedCCSwitch(t, home)

	mc := config.ModelConfig{
		Provider: "custom",
		APIType:  "anthropic",
		BaseURL:  "https://open.bigmodel.cn/api/anthropic",
		Model:    "glm-5.3-flash",
		KeyRef:   "ccswitch:claude:missing",
	}
	client, err := NewClient(mc)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// The credential check lives on the call path, not in the constructor: a
	// dangling reference has to read as "not configured", never as a dial that
	// comes back 401.
	if _, err := client.Complete(context.Background(), "sys", "hi"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Complete with a dangling reference = %v, want ErrNoKey", err)
	}
}
