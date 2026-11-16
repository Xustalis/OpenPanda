// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/hex"
	"os"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/bus"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

func TestConsentGrantRequiresRecordedOriginKey(t *testing.T) {
	origin := newCore(t, "origin")
	relay := newCore(t, "relay")
	executor := newCore(t, "executor")
	p := bus.TaskDelegatePayload{
		TaskID: "consent-task", Chain: []string{"origin", "relay"}, Authorized: true,
	}
	origin.signConsentGrant(&p)

	// Knowing the relay authenticates the transport, not the origin's consent.
	relayPub, _, _ := relay.nodeKeyPair()
	executor.recordPeerPubKey(context.Background(), "relay", hex.EncodeToString(relayPub), "")
	if executor.consentGrantValid(p, "relay") {
		t.Fatal("a valid signature from an unknown origin was accepted")
	}
	forged := p
	forged.AuthPub, forged.AuthSig = "not-a-public-key", "not-a-signature"
	if executor.consentGrantValid(forged, "relay") {
		t.Fatal("unverifiable fields from an unknown origin were accepted")
	}

	// Once the origin's proven key is recorded, the same relayed grant works.
	originPub, _, _ := origin.nodeKeyPair()
	executor.recordPeerPubKey(context.Background(), "origin", hex.EncodeToString(originPub), "")
	if !executor.consentGrantValid(p, "relay") {
		t.Fatal("a grant signed by the recorded origin key was rejected")
	}
	if err := executor.db.Close(); err != nil {
		t.Fatal(err)
	}
	if executor.consentGrantValid(p, "relay") {
		t.Fatal("a failed origin-key lookup granted authorization")
	}
}

func TestDelegateConsentRequiresVerifiedOrigin(t *testing.T) {
	// A no-test invocation of this binary is a harmless native command on
	// every platform; shell builtins such as echo are not portable executables.
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		known      bool
		signed     bool
		relayed    bool
		lowRisk    bool
		authorized bool
	}{
		{name: "known signed direct", known: true, signed: true, authorized: true},
		{name: "known signed relay", known: true, signed: true, relayed: true, authorized: true},
		{name: "known unsigned direct", known: true},
		{name: "unknown unsigned direct"},
		{name: "unknown signed direct", signed: true},
		{name: "unknown signed relay", signed: true, relayed: true},
		{name: "legacy low risk", lowRisk: true},
		{name: "unknown origin low risk relay", signed: true, relayed: true, lowRisk: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			origin := newCore(t, "origin")
			tier := 2
			if tc.lowRisk {
				tier = 1
			}
			executor := newCoreWithNative(t, "executor", ledger.NativeAbility{
				ID: "sys:consent-check", Command: binary, Args: []string{"-test.run=^$"}, Tier: tier,
			})
			p := bus.TaskDelegatePayload{
				TaskID: "consent-task", AttemptID: "consent-attempt", Intent: "check consent",
				Requires: []string{"sys:consent-check"}, Chain: []string{"origin"}, Authorized: true,
			}
			sender := "origin"
			if tc.relayed {
				relay := newCore(t, "relay")
				pub, _, _ := relay.nodeKeyPair()
				executor.recordPeerPubKey(ctx, "relay", hex.EncodeToString(pub), "")
				p.Chain = append(p.Chain, "relay")
				sender = "relay"
			}
			if tc.signed {
				origin.signConsentGrant(&p)
			}
			if tc.known {
				pub, _, _ := origin.nodeKeyPair()
				executor.recordPeerPubKey(ctx, "origin", hex.EncodeToString(pub), "")
			}
			env, err := bus.NewEnvelope(bus.MsgTaskDelegate, sender, "consent-envelope", p)
			if err != nil {
				t.Fatal(err)
			}
			executor.handleDelegate(ctx, env)
			task, err := executor.store.Get(ctx, p.TaskID)
			if err != nil {
				t.Fatalf("delegate was not retained: %v", err)
			}
			if task.Authorized != tc.authorized {
				waitLocalTasksSettled(t, executor)
				t.Fatalf("authorized = %v, want %v", task.Authorized, tc.authorized)
			}
			wantState := StateReview
			if tc.authorized || tc.lowRisk {
				wantState = StateDone
			}
			if !tc.authorized && (task.AuthSig != "" || task.AuthPub != "" || task.AuthTs != 0) {
				t.Fatal("unverified consent was persisted for a later resume or relay")
			}
			waitState(t, ctx, executor.store, p.TaskID, wantState)
		})
	}
}
