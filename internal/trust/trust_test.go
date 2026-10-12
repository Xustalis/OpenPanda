// SPDX-License-Identifier: AGPL-3.0-or-later

package trust

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/bus"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want Level
		ok   bool
	}{
		{"", Mesh, true},
		{"  ", Mesh, true},
		{"mesh", Mesh, true},
		{"MESH", Mesh, true},
		{"guarded", Guarded, true},
		{"Guarded", Guarded, true},
		{"open", Open, true},
		{"friend", "", false},
		{"0", "", false},
	}
	for _, tc := range cases {
		got, err := ParseLevel(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("ParseLevel(%q) err=%v want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("ParseLevel(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestMin(t *testing.T) {
	cases := []struct {
		a, b, want Level
	}{
		{Mesh, Mesh, Mesh},
		{Mesh, Guarded, Guarded},
		{Guarded, Mesh, Guarded},
		{Guarded, Open, Open},
		{Open, Mesh, Open},
		{Open, Open, Open},
	}
	for _, tc := range cases {
		if got := Min(tc.a, tc.b); got != tc.want {
			t.Errorf("Min(%q,%q)=%q want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestDefaultSendMeshIsNoop(t *testing.T) {
	p := DefaultSend(Mesh)
	if !p.Delegate || !p.ProjectPack || !p.Inputs || !p.Consent || p.MaxContext != "full" {
		t.Fatalf("mesh ceiling must impose nothing, got %+v", p)
	}
}

func TestDefaultSendGuardedClamps(t *testing.T) {
	p := DefaultSend(Guarded)
	if !p.Delegate {
		t.Fatal("guarded must still delegate")
	}
	if p.ProjectPack || p.Inputs || p.Consent {
		t.Fatalf("guarded must strip pack/inputs/consent, got %+v", p)
	}
	if p.MaxContext != "pointer" {
		t.Fatalf("guarded context ceiling = %q, want pointer", p.MaxContext)
	}
}

func TestDefaultSendOpenBarsDelegate(t *testing.T) {
	if DefaultSend(Open).Delegate {
		t.Fatal("open peers must never receive a delegate in this phase")
	}
}

func TestDefaultAccept(t *testing.T) {
	if a := DefaultAccept(Mesh); !a.DelegateOrigin || a.Mode != AcceptAuto {
		t.Fatalf("mesh accept must auto-admit, got %+v", a)
	}
	if a := DefaultAccept(Guarded); !a.DelegateOrigin || a.Mode != AcceptAuto {
		t.Fatalf("guarded accept must default to auto, got %+v", a)
	}
	if a := DefaultAccept(Open); a.DelegateOrigin || a.Mode != AcceptRefuse {
		t.Fatalf("open accept must refuse origin, got %+v", a)
	}
}

// TestDefaultAcceptModelCreds: credential withholding is the sub-mesh
// default — a semi-trusted task's text must never read this node's API keys
// out of the subprocess env, while mesh keeps the pre-feature provide.
func TestDefaultAcceptModelCreds(t *testing.T) {
	if a := DefaultAccept(Mesh); a.ModelCreds != CredsProvide {
		t.Fatalf("mesh must provide creds (pre-feature behaviour), got %+v", a)
	}
	for _, l := range []Level{Guarded, Open} {
		if a := DefaultAccept(l); a.ModelCreds != CredsWithhold {
			t.Fatalf("%s must withhold model creds by default, got %+v", l, a)
		}
	}
}

func TestNeedsOf(t *testing.T) {
	p := bus.TaskDelegatePayload{
		Project:          "demo",
		ProjectPack:      []byte("{}"),
		Inputs:           []bus.ArtifactRef{{Hash: "h"}},
		BundledArtifacts: []bus.FatBundleArtifact{{Hash: "h"}},
		ContextLevel:     "full",
		Authorized:       true,
	}
	n := NeedsOf(p)
	if !n.Pack || !n.Inputs || !n.FullContext || !n.Consent {
		t.Fatalf("needs = %+v, want all true", n)
	}
	n = NeedsOf(bus.TaskDelegatePayload{ContextLevel: "pointer"})
	if n.Pack || n.Inputs || n.FullContext || n.Consent {
		t.Fatalf("bare pointer delegate needs = %+v, want all false", n)
	}
	// A file-context task needs its worktree even when Inputs was never
	// populated on this path — the store- and submit-side derivations
	// (needsForTask/needsForInput) already say so, and NeedsOf must agree
	// or one gate passes what another refuses.
	n = NeedsOf(bus.TaskDelegatePayload{ContextType: "file"})
	if !n.Inputs {
		t.Fatalf("file-context delegate needs = %+v, want Inputs true", n)
	}
}

func TestExecutorOK(t *testing.T) {
	mesh := DefaultSend(Mesh)
	guarded := DefaultSend(Guarded)
	open := DefaultSend(Open)

	if !mesh.ExecutorOK(Needs{Pack: true, Inputs: true, FullContext: true, Consent: true}) {
		t.Fatal("mesh must execute anything")
	}
	if open.ExecutorOK(Needs{}) {
		t.Fatal("open must never execute")
	}
	// Clampable needs never disqualify a guarded executor.
	if !guarded.ExecutorOK(Needs{FullContext: true, Consent: true}) {
		t.Fatal("guarded must still execute when only context/consent are clampable")
	}
	// A pack the ceiling would strip means a blind executor: reroute instead.
	if guarded.ExecutorOK(Needs{Pack: true}) {
		t.Fatal("guarded must not execute work whose pack it would strip")
	}
	if guarded.ExecutorOK(Needs{Inputs: true}) {
		t.Fatal("guarded must not execute work whose inputs it would strip")
	}
}

func TestApplyMeshNoop(t *testing.T) {
	d := bus.TaskDelegatePayload{
		ProjectPack:  []byte("{}"),
		Inputs:       []bus.ArtifactRef{{Hash: "h"}},
		ContextLevel: "full",
		ContextData:  []byte("snap"),
		Authorized:   true,
		AuthSig:      "s", AuthPub: "p", AuthTs: 1, AuthHops: 2,
	}
	got := DefaultSend(Mesh).Apply(&d)
	if got.Any() {
		t.Fatalf("mesh clamp must be a no-op, applied %+v", got)
	}
	if len(d.ContextData) == 0 || !d.Authorized || len(d.Inputs) == 0 {
		t.Fatal("mesh clamp mutated the payload")
	}
}

func TestApplyGuardedFullToPointer(t *testing.T) {
	d := bus.TaskDelegatePayload{
		ProjectPack:      []byte("{}"),
		Inputs:           []bus.ArtifactRef{{Hash: "h"}},
		BundledArtifacts: []bus.FatBundleArtifact{{Hash: "h"}},
		ContextLevel:     "full",
		ContextData:      []byte("snap"),
		ContextHash:      "hh",
		ContextType:      "snapshot",
		Authorized:       true,
		AuthSig:          "s", AuthPub: "p", AuthTs: 9, AuthHops: 3,
	}
	got := DefaultSend(Guarded).Apply(&d)
	if !got.ContextLevel || !got.ProjectPack || !got.Inputs || !got.Consent {
		t.Fatalf("guarded clamp should hit every clause, got %+v", got)
	}
	if d.ContextLevel != "pointer" {
		t.Fatalf("context level = %q, want pointer", d.ContextLevel)
	}
	if len(d.ContextData) != 0 {
		t.Fatal("full snapshot must not ride a guarded delegate")
	}
	if d.ContextHash != "hh" || d.ContextType != "snapshot" {
		t.Fatal("pointer keeps the pull reference — the fetch is the authorization point")
	}
	if len(d.ProjectPack) != 0 || len(d.Inputs) != 0 || len(d.BundledArtifacts) != 0 {
		t.Fatal("pack/inputs must be stripped")
	}
	if d.Authorized || d.AuthSig != "" || d.AuthPub != "" || d.AuthTs != 0 || d.AuthHops != 0 {
		t.Fatal("consent and its signature fields must be stripped together")
	}
}

func TestApplySummaryCeilingStripsReference(t *testing.T) {
	d := bus.TaskDelegatePayload{ContextLevel: "full", ContextData: []byte("x"), ContextHash: "h", ContextType: "snapshot"}
	p := DefaultSend(Guarded)
	p.MaxContext = "summary"
	p.Apply(&d)
	if d.ContextLevel != "summary" || d.ContextHash != "" || d.ContextType != "" {
		t.Fatalf("summary ceiling must strip the snapshot reference, got %+v", d)
	}
}

func TestApplyIsMonotone(t *testing.T) {
	// A relay re-clamping for its own next hop must never resurrect what an
	// upstream hop stripped.
	d := bus.TaskDelegatePayload{ContextLevel: "pointer", ContextHash: "h"}
	DefaultSend(Guarded).Apply(&d)
	if d.ContextHash != "h" {
		t.Fatal("guarded (pointer ceiling) must keep an already-pointer delegate intact")
	}
	open := SendPolicy{Delegate: true, MaxContext: "summary"}
	open.Apply(&d)
	if d.ContextLevel != "summary" || d.ContextHash != "" {
		t.Fatal("a stricter downstream ceiling must still clamp")
	}
	mesh := DefaultSend(Mesh)
	mesh.Apply(&d)
	if d.ContextLevel != "summary" {
		t.Fatal("clamping never restores stripped data")
	}
}

// TestStoredLevel: mesh is the absence of a zone, so every write path stores
// it as "" — an explicit "mesh" would leave needless tier mappings in configs
// that never opted into trust zones (and break the scalar YAML form).
func TestStoredLevel(t *testing.T) {
	if got := StoredLevel(Mesh); got != "" {
		t.Fatalf("StoredLevel(mesh) = %q, want \"\"", got)
	}
	if got := StoredLevel(Guarded); got != "guarded" {
		t.Fatalf("StoredLevel(guarded) = %q", got)
	}
	if got := StoredLevel(Open); got != "open" {
		t.Fatalf("StoredLevel(open) = %q", got)
	}
}
