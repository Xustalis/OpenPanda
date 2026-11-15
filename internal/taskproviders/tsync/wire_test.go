// SPDX-License-Identifier: AGPL-3.0-or-later
package tsync

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

func fixtureTask() Task {
	return Task{Key: taskproviders.Key{InstanceID: "instance", WorkspaceID: "workspace", SpaceID: "space", TaskID: "task"}, Title: "audit task", Versions: Versions{Task: "task-r1", Input: "input-r1", Assignment: "assignment-r1", Generation: 1}}
}

func TestCanonicalContracts(t *testing.T) {
	t.Run("minimum-version", func(t *testing.T) {
		c := Capabilities{IntegrationVersion: 1, InstanceID: "instance", FeedEpoch: "epoch", MinimumVersion: "0.1.0", Features: map[string]bool{"deviceFlow": true}}
		if err := c.Require("deviceFlow"); err != nil {
			t.Fatal(err)
		}
		c.MinimumVersion = "99.0.0"
		if err := c.Require("deviceFlow"); err == nil {
			t.Fatal("future minimum accepted")
		}
	})
	t.Run("four-time-kinds", func(t *testing.T) {
		zone := "Asia/Shanghai"
		for _, value := range []Time{{Kind: "date_only", Raw: "2026-11-16"}, {Kind: "floating", Raw: "2026-11-15T09:30:00"}, {Kind: "offset", Raw: "2026-11-15T14:30:00+08:00"}, {Kind: "zoned", Raw: "2026-11-15T14:30:00", Timezone: &zone}} {
			task := fixtureTask()
			task.DueAt = &value
			s, err := task.Snapshot(1, "Space")
			if err != nil {
				t.Fatal(value.Kind, err)
			}
			if s.DueAt.Value != value.Raw {
				t.Fatal("raw value changed")
			}
			_, err = s.DueAt.ScheduleTime()
			schedulable := value.Kind == "offset" || value.Kind == "zoned"
			if (err == nil) != schedulable {
				t.Fatal(value.Kind, "schedule:", err)
			}
		}
	})
	t.Run("golden-signatures", func(t *testing.T) {
		var fixture struct {
			TestSeedHex string
			PublicKey   string
			Cases       []struct {
				Kind  string
				Input struct {
					Challenge, SourceHash, RequestID string
					ConsentPayload
				}
				MessageUtf8, Signature string
			}
		}
		b, err := os.ReadFile("testdata/signing.json")
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, &fixture); err != nil {
			t.Fatal(err)
		}
		seed, err := hex.DecodeString(fixture.TestSeedHex)
		if err != nil {
			t.Fatal(err)
		}
		key := ed25519.NewKeyFromSeed(seed)
		if hex.EncodeToString(key.Public().(ed25519.PublicKey)) != fixture.PublicKey {
			t.Fatal("public key differs")
		}
		for _, c := range fixture.Cases {
			var b []byte
			var err error
			if c.Kind == "consent" {
				b, err = ConsentMessage(c.Input.ConsentPayload)
			} else {
				b, err = proofMessage(c.Kind, c.Input.Challenge, c.Input.SourceHash, c.Input.RequestID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != c.MessageUtf8 || hex.EncodeToString(ed25519.Sign(key, b)) != c.Signature {
				t.Fatal(c.Kind, "golden mismatch")
			}
		}
	})
}

func TestNonZonedTimezoneMustBeNull(t *testing.T) {
	empty := ""
	task := fixtureTask()
	task.DueAt = &Time{Kind: "floating", Raw: "2026-11-15T09:30:00", Timezone: &empty}
	if _, err := task.Snapshot(1, "Space"); err == nil {
		t.Fatal("non-zoned timezone empty string accepted; contract requires null")
	}
}

func TestReconnectMustAwaitNewGrant(t *testing.T) {
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tsync/v1/capabilities":
			_ = json.NewEncoder(w).Encode(Capabilities{IntegrationVersion: 1, InstanceID: "instance", FeedEpoch: "epoch", MinimumVersion: "0.1.0", Features: map[string]bool{"deviceFlow": true}})
		case "/tsync/oauth/device/code":
			base := "http://" + r.Host + "/tsync/device"
			_ = json.NewEncoder(w).Encode(deviceGrant{DeviceCode: "synthetic-code", UserCode: "AUDIT", VerificationURI: base, VerificationComplete: base + "?user_code=AUDIT", Challenge: "synthetic-challenge", ExpiresIn: 600, Interval: 1})
		case "/tsync/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				refreshes.Add(1)
			}
			_ = json.NewEncoder(w).Encode(deviceToken{Access: "synthetic-new-access", Refresh: "synthetic-new-refresh", Type: "Bearer", DeviceID: "device", Scope: "tasks:read", ExpiresIn: 600})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(server.URL + "/tsync/")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewDeviceStore(filepath.Join(dir, "credential.json"), client)
	if err != nil {
		t.Fatal(err)
	}
	state := deviceState{Version: 1, Root: client.Root, Seed: strings.Repeat("07", 32), Token: &deviceToken{Access: "synthetic-old-access", Refresh: "synthetic-old-refresh", Type: "Bearer", DeviceID: "device", Scope: "tasks:read", ExpiresIn: 600}, Scopes: []string{"tasks:read"}, ExpiresAt: time.Now().Unix() + 600}
	if err = store.save(&state); err != nil {
		t.Fatal(err)
	}
	if err = store.Disable(); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Begin(context.Background(), "audit-device", []string{"tasks:read"}); err != nil {
		t.Fatal(err)
	}
	_, err = store.AccessToken(context.Background())
	if err == nil || refreshes.Load() != 0 {
		t.Fatalf("before new grant approval: refresh requests=%d, AccessToken error=%v", refreshes.Load(), err)
	}
}
