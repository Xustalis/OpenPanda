// SPDX-License-Identifier: AGPL-3.0-or-later

package config

// Node identity: who this machine is, and why the answer must not change.
//
// The identity is the key the whole node model hangs from: the singleton lock
// that keeps one physical node per host, the directory row's node_identity,
// and the `name@vm-<hash>` spelling a VM sibling uses. It is not a display
// name — it is a fact about the box, so it has to survive a restart, a config
// edit, a hostname change, and a probe that fails for reasons the node cannot
// see (a locked-down shell with `reg.exe` blacklisted, a CI sandbox, a
// container without /etc/machine-id).
//
// That is why the resolved value is persisted next to the identity locks:
// probing again on every process start is how one host grew two identities
// (`machine-…` when the registry answered, `host-<hostname>` when it did not)
// and two engines both believed they were the physical singleton.

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// physicalIdentityFile holds the first hardware fingerprint this node resolved.
// It lives beside the identity locks (os.UserConfigDir()/openpanda) because
// both answer the same question — which box is this — and a lock keyed by a
// different answer than the directory row is how two processes on one machine
// both acquired "the" physical lock.
const physicalIdentityFile = "physical-identity"

var (
	physicalIdentityOnce sync.Once
	physicalIdentityVal  string

	// identityRootOverride redirects the identity file for tests. os.UserConfigDir
	// is the real root in production; tests must not read or write the user's.
	identityRootOverride string
)

// MachineIdentity returns a stable, non-secret identity for the current host.
// OPENPANDA_NODE_IDENTITY is accepted for compatibility when generating a
// default config; physical runtime locking still uses PhysicalIdentity.
func MachineIdentity() string {
	if v := os.Getenv("OPENPANDA_NODE_IDENTITY"); v != "" {
		return v
	}
	return PhysicalIdentity()
}

// PhysicalIdentity ignores user-provided overrides and identifies the host
// itself. It is used for the physical-node singleton lock.
//
// Resolution order is "most stable first": a machine-id file survives a
// rename, the platform UUID survives a NIC change, a MAC address survives
// neither a dock nor a randomized MAC but beats a hostname, and the hostname
// survives nothing. The first answer is persisted; later runs read it back
// instead of probing again, so a probe that fails tomorrow cannot rename the
// machine.
func PhysicalIdentity() string {
	physicalIdentityOnce.Do(func() {
		physicalIdentityVal = resolvePhysicalIdentity()
	})
	return physicalIdentityVal
}

// resolvePhysicalIdentity is PhysicalIdentity without the process memo — the
// seam tests use, and the one place that decides what gets persisted.
func resolvePhysicalIdentity() string {
	if id := readPersistedIdentity(); id != "" {
		return id
	}
	id := probePhysicalIdentity()
	// Only the strong form is persisted. A weak fallback (MAC, hostname) is a
	// per-process guess: persisting it would pin a sandboxed first run's
	// degraded answer forever, and the next real probe would look like a
	// different machine instead of an upgrade.
	if strings.HasPrefix(id, "machine-") {
		writePersistedIdentity(id)
	}
	return id
}

// probePhysicalIdentity reads the host's own fingerprint, without persisting.
func probePhysicalIdentity() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id", "/var/db/SystemConfiguration/.com.apple.uuid"} {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return "machine-" + shortHash(s)
			}
		}
	}
	if s := platformMachineID(); s != "" {
		return "machine-" + shortHash(s)
	}
	if s := macIdentity(); s != "" {
		return "mac-" + shortHash(s)
	}
	host, _ := os.Hostname()
	if host == "" {
		host = runtime.GOOS + "-" + runtime.GOARCH
	}
	return "host-" + shortHash(host)
}

// macIdentity hashes the machine's NIC addresses, sorted so the answer does
// not depend on enumeration order. This is the last resort before the
// hostname: a laptop that switches between Wi-Fi and Ethernet changes it, so
// it is never persisted — but it is stable across processes on one boot,
// which is enough to keep two sandboxed runs from minting two identities.
func macIdentity() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var macs []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || len(iface.HardwareAddr) < 6 {
			continue
		}
		mac := iface.HardwareAddr.String()
		if mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		macs = append(macs, mac)
	}
	if len(macs) == 0 {
		return ""
	}
	sort.Strings(macs)
	return strings.Join(macs, ",")
}

// identityFilePath resolves the persisted identity's location.
func identityFilePath() (string, error) {
	root := identityRootOverride
	if root == "" {
		var err error
		root, err = os.UserConfigDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(root, "openpanda", physicalIdentityFile), nil
}

// readPersistedIdentity returns the stored fingerprint, or "" when this host
// has none yet. Only the `machine-` shape is trusted: it is the only one this
// package persists, and accepting a hand-written `host-…` would let a config
// directory pin a weaker identity than the hardware offers.
func readPersistedIdentity() string {
	path, err := identityFilePath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if !strings.HasPrefix(s, "machine-") {
		return ""
	}
	return s
}

// writePersistedIdentity stores the fingerprint atomically. Best-effort: a
// read-only config directory must not stop a node from starting, it only
// means the next run probes again.
func writePersistedIdentity(id string) {
	path, err := identityFilePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// EffectiveIdentity is the identity used for runtime ownership. Physical
// nodes always use the host fingerprint; VM nodes may supply a separate stable
// identity so a host and its guest can coexist.
func (n NodeConfig) EffectiveIdentity() string {
	if n.Kind == NodeKindPhysical {
		return PhysicalIdentity()
	}
	if strings.TrimSpace(n.Identity) != "" {
		return strings.TrimSpace(n.Identity)
	}
	return PhysicalIdentity()
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
