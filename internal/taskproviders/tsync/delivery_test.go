// SPDX-License-Identifier: AGPL-3.0-or-later

package tsync

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeviceConsentGoldenAndAuthority(t *testing.T) {
	b, err := os.ReadFile("testdata/signing.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Kind      string
			Input     ConsentPayload
			Signature string
		}
	}
	if err = json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	p := fixture.Cases[2].Input
	client, err := New("https://example.test/tsync/")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewDeviceStore(filepath.Join(dir, "device.json"), client)
	if err != nil {
		t.Fatal(err)
	}
	now, err := time.Parse(time.RFC3339Nano, p.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	state := deviceState{Version: 1, Root: client.Root, Seed: strings.Repeat("07", 32), Token: &deviceToken{DeviceID: p.DeviceID}, Scopes: []string{"delivery:report-decision"}}
	if err = s.save(&state); err != nil {
		t.Fatal(err)
	}
	proof, err := s.SignConsent(context.Background(), p)
	if err != nil || proof.Signature != fixture.Cases[2].Signature {
		t.Fatalf("golden signer: %v %#v", err, proof)
	}
	key, _ := hex.DecodeString("ea4a6c63e29c520abef5507b132ec5f9954776aebebe7b92421eea691446d22c")
	sig, _ := hex.DecodeString(proof.Signature)
	p.DeliveryRevision++
	message, _ := ConsentMessage(p)
	if ed25519.Verify(ed25519.PublicKey(key), message, sig) {
		t.Fatal("changed delivery revision accepted")
	}
	p.DeliveryRevision--
	for _, change := range []func(*deviceState){
		func(s *deviceState) { s.Token.DeviceID = "other" },
		func(s *deviceState) { s.Scopes = []string{"tasks:read"} },
		func(s *deviceState) { s.Reconnect = true },
	} {
		copy := state
		token := *state.Token
		copy.Token = &token
		change(&copy)
		if err = s.save(&copy); err != nil {
			t.Fatal(err)
		}
		if _, err = s.SignConsent(context.Background(), p); err == nil {
			t.Fatal("unauthorized signer accepted")
		}
	}
	if err = s.save(&state); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now.Add(6 * time.Minute) }
	if _, err = s.SignConsent(context.Background(), p); err == nil {
		t.Fatal("expired consent accepted")
	}
}
