// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func TestFirstConnectionUnavailableDoesNotCreateAccount(t *testing.T) {
	var enabled atomic.Bool
	var grants atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tsync/v1/capabilities":
			_ = json.NewEncoder(w).Encode(map[string]any{"integrationVersion": 1, "instanceId": "instance", "feedEpoch": "feed", "minIntegrationClientVersion": "0.1.0", "integrationFeatures": map[string]bool{"deviceFlow": enabled.Load()}})
		case "/tsync/oauth/device/code":
			grants.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "test", "user_code": "CONNECT", "verification_uri": "http://" + r.Host + "/tsync/device", "expires_in": 600, "interval": 1, "device_challenge": "challenge"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "node.db")
	s := New(testdb.OpenFile(t, path), path)
	ctx := context.Background()
	opts := ConnectOptions{ID: "first", Root: server.URL + "/tsync/"}
	for range 2 {
		if _, err := s.Connect(ctx, opts); !errors.Is(err, taskproviders.ErrUnsupported) {
			t.Fatalf("expected unsupported service: %v", err)
		}
	}
	if rows, err := s.List(ctx); err != nil || len(rows) != 0 || grants.Load() != 0 {
		t.Fatalf("unexpected connections=%d grants=%d err=%v", len(rows), grants.Load(), err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "first.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected credential: %v", err)
	}
	enabled.Store(true)
	res, err := s.Connect(ctx, opts)
	if err != nil || res.Connection.State != "authorizing" || grants.Load() != 1 {
		t.Fatalf("retry after service enabled: state=%s grants=%d err=%v", res.Connection.State, grants.Load(), err)
	}
}

func TestConnectionReconnectAndPendingResume(t *testing.T) {
	var grants, refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tsync/v1/capabilities":
			_, _ = w.Write([]byte(`{"integrationVersion":1,"instanceId":"instance","feedEpoch":"feed","minIntegrationClientVersion":"0.1.0","integrationFeatures":{"deviceFlow":true}}`))
		case "/tsync/oauth/device/code":
			grants.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "secret-code", "user_code": "CONNECT", "verification_uri": "http://" + r.Host + "/tsync/device", "expires_in": 600, "interval": 1, "device_challenge": "challenge"})
		case "/tsync/oauth/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") == "refresh_token" {
				refreshes.Add(1)
			}
			_, _ = w.Write([]byte(`{"access_token":"secret-access","refresh_token":"secret-refresh","token_type":"Bearer","device_id":"device","scope":"tasks:read members:read","expires_in":600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "node.db")
	s := New(testdb.OpenFile(t, path), path)
	ctx := context.Background()
	opts := ConnectOptions{ID: "test", Root: server.URL + "/tsync/"}
	first, err := s.Connect(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := s.Connect(ctx, opts)
	if err != nil || grants.Load() != 1 || resumed.Authorization != first.Authorization {
		t.Fatalf("pending grant was replaced: grants=%d err=%v", grants.Load(), err)
	}
	// Advance this persisted test grant without a real polling delay.
	file := filepath.Join(s.dir, "test.json")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err = json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	state["nextPollAt"] = 0
	b, _ = json.Marshal(state)
	if err = os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	connected, err := s.FinishConnect(ctx, opts.ID)
	if err != nil || connected.State != "connected" {
		t.Fatalf("connect: %+v %v", connected, err)
	}
	_, device, err := s.device(connected)
	if err != nil {
		t.Fatal(err)
	}
	if token, err := device.AccessToken(ctx); err != nil || token != "secret-access" {
		t.Fatalf("access: %v", err)
	}
	reconnected, err := s.Connect(ctx, opts)
	if err != nil || grants.Load() != 2 || reconnected.Connection.State != "authorizing" {
		t.Fatalf("explicit reconnect must work once: grants=%d err=%v", grants.Load(), err)
	}
	if _, err = device.AccessToken(ctx); !errors.Is(err, taskproviders.ErrReconnect) || refreshes.Load() != 0 {
		t.Fatalf("old credential remained usable: refresh=%d err=%v", refreshes.Load(), err)
	}
	public, err := json.Marshal(reconnected)
	if err != nil || strings.Contains(string(public), "secret-") || strings.Contains(string(public), "credentialRef") {
		t.Fatalf("connection DTO leaked private data: %v", err)
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("credential file is not a regular private file: %v", err)
	}
	// Windows reports synthetic POSIX bits; keep the Unix permission contract
	// alongside the cross-platform regular-file and credential checks above.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("credential file is not private: %v", err)
	}
	changed, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	opts.Root = server.URL + "/different/"
	if _, err = s.Connect(changed, opts); err == nil {
		t.Fatal("reconnect changed root")
	}
}
