// SPDX-License-Identifier: AGPL-3.0-or-later

package devserver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".panda"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigAbsentIsNotAnError(t *testing.T) {
	if _, _, err := LoadConfig(t.TempDir()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}

func TestLoadConfigNormalizes(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"command":"npm run dev","url":"127.0.0.1:5173/"}`)
	cfg, cwd, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.URL != "http://127.0.0.1:5173" {
		t.Fatalf("url = %q", cfg.URL)
	}
	if cfg.ReadyTimeoutS != DefaultReadyTimeout {
		t.Fatalf("timeout = %d", cfg.ReadyTimeoutS)
	}
	if cwd != dir {
		t.Fatalf("cwd = %q, want %q", cwd, dir)
	}
}

func TestLoadConfigResolvesRelativeCwd(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `{"command":"npm run dev","url":"http://localhost:3000","cwd":"web"}`)
	cfg, cwd, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Cwd != filepath.Join(dir, "web") {
		t.Fatalf("cfg cwd = %q", cfg.Cwd)
	}
	if cwd != cfg.Cwd {
		t.Fatalf("run cwd = %q", cwd)
	}
}

func TestLoadConfigRefusesUnsafeDeclarations(t *testing.T) {
	cases := map[string]string{
		"no command":      `{"url":"http://127.0.0.1:5173"}`,
		"no url":          `{"command":"npm run dev"}`,
		"non-loopback":    `{"command":"npm run dev","url":"http://example.com:5173"}`,
		"lan":             `{"command":"npm run dev","url":"http://192.168.1.10:5173"}`,
		"no port":         `{"command":"npm run dev","url":"http://127.0.0.1"}`,
		"base path":       `{"command":"npm run dev","url":"http://127.0.0.1:5173/app"}`,
		"cwd leaves root": `{"command":"npm run dev","url":"http://127.0.0.1:5173","cwd":"../elsewhere"}`,
		"bad scheme":      `{"command":"npm run dev","url":"ftp://127.0.0.1:5173"}`,
		"not json":        `{`,
	}
	for name, body := range cases {
		dir := t.TempDir()
		writeConfig(t, dir, body)
		if _, _, err := LoadConfig(dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFrameableCSPKeepsTheRest(t *testing.T) {
	got := frameableCSP("default-src 'self'; frame-ancestors 'none'; img-src *")
	if strings.Contains(got, "'none'") {
		t.Fatalf("frame-ancestors survived: %q", got)
	}
	if !strings.Contains(got, "default-src 'self'") || !strings.Contains(got, "img-src *") {
		t.Fatalf("other directives were dropped: %q", got)
	}
	if !strings.Contains(got, "frame-ancestors *") {
		t.Fatalf("no frame-ancestors allowance: %q", got)
	}
}

func TestProxyCookieDropsOriginAttributes(t *testing.T) {
	got := proxyCookie("sid=abc; Path=/; Domain=localhost; Secure; HttpOnly; SameSite=None")
	for _, bad := range []string{"Domain=", "Secure", "SameSite=None"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q survived in %q", bad, got)
		}
	}
	for _, want := range []string{"sid=abc", "Path=/", "HttpOnly"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, got)
		}
	}
}
