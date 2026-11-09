// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

// TestDeviceIntent pins the split between the DEVICE decision and the
// EXECUTOR decision. requires ("agent:opencode") is not consulted here on
// purpose: an ability names an adapter, not a machine. A hard pin naming this
// node is local work; anything else is a remote intent that must never reach
// the local c.run path.
func TestDeviceIntent(t *testing.T) {
	const self = "macbook"
	cases := []struct {
		name     string
		spec     string
		wantRef  string
		wantHard bool
	}{
		{"hard pin to another device", `{"node":"xin"}`, "xin", true},
		{"soft hint to another device", `{"preferred":"xin"}`, "xin", false},
		{"hard pin wins over hint", `{"node":"xin","preferred":"opi3b"}`, "xin", true},
		{"hard pin to this node is local", `{"node":"macbook"}`, "", false},
		{"soft hint to this node is local", `{"preferred":"macbook"}`, "", false},
		{"no device intent", `{"target":"do the work"}`, "", false},
		{"empty spec", ``, "", false},
		{"malformed spec", `{`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, hard := deviceIntent(Task{SpecJSON: tc.spec}, self)
			if ref != tc.wantRef || hard != tc.wantHard {
				t.Fatalf("deviceIntent = (%q, %v), want (%q, %v)", ref, hard, tc.wantRef, tc.wantHard)
			}
		})
	}
}

// TestHardPinSurvivesTaskInput is the other half of the board contract: a
// TargetNode must be persisted as spec_json["node"], which is the key
// targetNodeOf()/deviceIntent() read back when the queue claims the row. A
// field that stops at TaskInput would leave the task free-routed.
func TestHardPinSurvivesTaskInput(t *testing.T) {
	spec := TaskInput{TargetNode: "xin"}.detail().SpecJSON
	if got := targetNodeOf(Task{SpecJSON: spec}); got != "xin" {
		t.Fatalf("spec_json = %s, targetNodeOf = %q, want xin", spec, got)
	}
	if ref, hard := deviceIntent(Task{SpecJSON: spec}, "macbook"); !hard || ref != "xin" {
		t.Fatalf("deviceIntent = (%q, %v), want (xin, true)", ref, hard)
	}
	// A soft hint must NOT be mistaken for a hard pin.
	soft := TaskInput{PreferredNode: "xin"}.detail().SpecJSON
	if got := targetNodeOf(Task{SpecJSON: soft}); got != "" {
		t.Fatalf("soft hint produced hard pin %q in %s", got, soft)
	}
}

// TestHelloFailureReason keeps the four "auth" causes distinguishable. A
// secret mismatch used to look exactly like a clock jump, so two operators
// could spend an afternoon checking firewalls while TCP connected cleanly.
func TestHelloFailureReason(t *testing.T) {
	const secret = "mesh-secret"
	const receiver = "macbook"
	now := time.Now()
	ts := now.Unix()
	nonce := "n-1"

	valid := bus.HelloPayload{
		NodeID: "xin", Ts: ts, Nonce: nonce,
		Sig: bus.HelloSigN(secret, "xin", ts, nonce),
	}

	cases := []struct {
		name   string
		secret string
		p      bus.HelloPayload
		want   string
	}{
		{
			name:   "secret mismatch",
			secret: "different-secret",
			p:      valid,
			want:   "shared_secret mismatch (HMAC)",
		},
		{
			name:   "clock skew",
			secret: secret,
			p: bus.HelloPayload{
				NodeID: "xin", Ts: now.Add(-2 * bus.MaxHelloAge).Unix(), Nonce: nonce,
				Sig: bus.HelloSigN(secret, "xin", now.Add(-2*bus.MaxHelloAge).Unix(), nonce),
			},
			want: "clock skew",
		},
		{
			name:   "local secret empty",
			secret: "",
			p:      valid,
			want:   "local shared_secret is empty",
		},
		{
			name:   "hello bound for another receiver",
			secret: secret,
			p: bus.HelloPayload{
				NodeID: "xin", To: "opi3b", Ts: ts, Nonce: nonce,
				SigB: bus.HelloSigB(secret, "xin", "opi3b", ts, nonce),
			},
			want: "hello bound for a different receiver",
		},
		{
			name:   "bound HMAC mismatch",
			secret: secret,
			p: bus.HelloPayload{
				NodeID: "xin", To: receiver, Ts: ts, Nonce: nonce, SigB: "deadbeef",
			},
			want: "shared_secret mismatch (bound HMAC)",
		},
		{
			name:   "identity block malformed",
			secret: secret,
			p: func() bus.HelloPayload {
				p := valid
				p.PubKey = strings.Repeat("00", 32) // present, but no EdSig
				return p
			}(),
			want: "identity signature (Ed25519) invalid or half-present",
		},
		{
			name:   "legacy two-field HMAC mismatch",
			secret: "different-secret",
			p: bus.HelloPayload{
				NodeID: "xin", Ts: ts, Sig: bus.HelloSig(secret, "xin", ts),
			},
			want: "shared_secret mismatch (legacy HMAC)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := helloFailureReason(tc.secret, receiver, tc.p, now)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("helloFailureReason = %q, want it to contain %q", got, tc.want)
			}
			// The diagnosis must agree with the real verifier: every fixture
			// here is a hello VerifyHelloP refuses.
			if bus.VerifyHelloP(tc.secret, receiver, tc.p, now) {
				t.Fatalf("fixture %q is actually a valid hello", tc.name)
			}
		})
	}

	// Guard the fixture: the same fields under the right secret must pass the
	// real verifier, or the "mismatch" case above would be a broken fixture
	// rather than a secret mismatch.
	if !bus.VerifyHelloP(secret, receiver, valid, now) {
		t.Fatal("valid hello fixture does not authenticate")
	}
}

func TestHelloRejectHintOnlyBlamesSecretForAuth(t *testing.T) {
	if got := helloRejectHint("auth"); !strings.Contains(got, "shared_secret") {
		t.Fatalf("auth hint = %q, want the credential diagnosis", got)
	}
	for _, reason := range []string{"identity", "policy", "unknown", ""} {
		if got := helloRejectHint(reason); strings.Contains(got, "shared_secret") {
			t.Fatalf("hint for %q = %q, must not send the operator to re-pair", reason, got)
		}
	}
}

func TestSecretFingerprint(t *testing.T) {
	if got := MeshSecretFingerprint(""); got != "unset" {
		t.Fatalf("empty secret fingerprint = %q, want unset", got)
	}
	a, b := MeshSecretFingerprint("secret-a"), MeshSecretFingerprint("secret-b")
	if a == b {
		t.Fatalf("different secrets share fingerprint %q", a)
	}
	if a != MeshSecretFingerprint("secret-a") {
		t.Fatal("fingerprint is not stable")
	}
	if len(a) != 8 {
		t.Fatalf("fingerprint = %q, want 8 hex chars", a)
	}
}

// TestHelloVersionHint: the receiver must name the build on the other end
// when a hello fails. A v0.0.11 peer against v0.1.5 is a real LAN case; an
// operator who only sees "shared_secret differs" will re-pair forever.
func TestHelloVersionHint(t *testing.T) {
	local := "0.1.5"
	cases := []struct {
		name, peer, want string
	}{
		{"same version", "0.1.5", ""},
		{"v prefix is equivalent", "v0.1.5", ""},
		{"no version field", "", "align every node"},
		{"much older peer", "0.0.11", "peer is on an older build"},
		{"newer peer", "0.2.0", "this node is behind"},
		{"unparseable dev stamp", "dev", "builds differ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := helloVersionHint(tc.peer, local)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("helloVersionHint(%q,%q) = %q, want no hint", tc.peer, local, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("helloVersionHint(%q,%q) = %q, want it to contain %q", tc.peer, local, got, tc.want)
			}
			if strings.Contains(got, "shared_secret") {
				t.Fatalf("version hint must not replace the credential verdict: %q", got)
			}
		})
	}
	if got := helloVersionHint("0.0.11", local); !strings.Contains(got, "0.0.11") || !strings.Contains(got, local) {
		t.Fatalf("hint must name both builds, got %q", got)
	}
}
