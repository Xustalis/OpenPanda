// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPersistedIdentityWinsOverProbe pins the stability contract: once a
// fingerprint is on disk, resolving the identity must read it instead of
// probing again. Probing per process is how one host grew two identities — the
// registry answered in one shell and not the other, and the singleton lock
// stopped recognising its own machine.
func TestPersistedIdentityWinsOverProbe(t *testing.T) {
	dir := t.TempDir()
	old := identityRootOverride
	identityRootOverride = dir
	t.Cleanup(func() { identityRootOverride = old })

	writePersistedIdentity("machine-0123456789abcdef")
	if got := readPersistedIdentity(); got != "machine-0123456789abcdef" {
		t.Fatalf("readPersistedIdentity = %q", got)
	}
	if got := resolvePhysicalIdentity(); got != "machine-0123456789abcdef" {
		t.Fatalf("resolvePhysicalIdentity = %q, want the persisted value", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "openpanda", physicalIdentityFile)); err != nil {
		t.Fatalf("identity file should live under openpanda/: %v", err)
	}
}

// TestPersistedIdentityRejectsWeakShapes: only the strong machine- form is
// trusted. A hand-written host-… (or a torn write) must not pin a weaker
// identity than the hardware offers.
func TestPersistedIdentityRejectsWeakShapes(t *testing.T) {
	dir := t.TempDir()
	old := identityRootOverride
	identityRootOverride = dir
	t.Cleanup(func() { identityRootOverride = old })

	path := filepath.Join(dir, "openpanda", physicalIdentityFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("host-deadbeef\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := readPersistedIdentity(); got != "" {
		t.Fatalf("readPersistedIdentity = %q, want the weak value ignored", got)
	}
}

// TestPhysicalIdentityIsStableWithinAProcess: the memo is the other half of
// the fix — /api/self and /api/nodes ask several times per request, and each
// answer has to be the same one the lock was taken under.
func TestPhysicalIdentityIsStableWithinAProcess(t *testing.T) {
	old := identityRootOverride
	identityRootOverride = t.TempDir()
	t.Cleanup(func() { identityRootOverride = old })

	first := PhysicalIdentity()
	if first == "" {
		t.Fatal("PhysicalIdentity must never be empty")
	}
	if second := PhysicalIdentity(); second != first {
		t.Fatalf("PhysicalIdentity changed between calls: %q then %q", first, second)
	}
}
