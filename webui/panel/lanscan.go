// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// Active LAN sweep for the pairing panel.
//
// Discovery's primary path is the broadcast beacon (internal/core/discovery.go):
// cheap, zero-config, and it names the device. It is also the first thing an
// access point eats — client isolation and multicast/broadcast filtering are
// routine on phone hotspots, guest Wi-Fi and managed APs, and when they apply
// every node on the LAN is present and invisible at the same time. A node whose
// own discovery socket never came up looks identical to that from the panel:
// an empty list.
//
// This sweep is the fallback the operator triggers explicitly: walk the node's
// own IPv4 /24 and TCP-connect to the mesh port. It proves reachability only —
// no identity, no membership — so a hit is a candidate to dial through the
// normal pairing handshake, where the shared secret and the Ed25519 hello still
// decide admission. Nothing here weakens that gate.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

const (
	// lanScanDialTimeout covers a same-segment SYN/ACK with room for the first
	// packet on a path that is not warm yet. 400ms measured too tight on Wi-Fi
	// against a peer whose ARP entry or radio had gone idle: the first dial
	// timed out while a burst of concurrent dials to the same host succeeded,
	// which is exactly the shape of a lost first SYN rather than an absent
	// listener.
	lanScanDialTimeout = 900 * time.Millisecond
	// lanScanWorkers bounds concurrent connects: a /24 is 254 addresses and
	// the whole point is to finish in one operator-visible pause.
	lanScanWorkers = 96
	// lanScanBudget caps the whole sweep. A /24 at 900ms with 96 lanes needs
	// about three rounds; the budget leaves room for the retry pass and still
	// returns before the panel's request timeout.
	lanScanBudget = 12 * time.Second
	// defaultMeshPort is the WS listen port every panda node ships with.
	defaultMeshPort = 7836
)

// scanCandidate is one address that accepted a TCP connection on the mesh
// port. It is deliberately not a "device": a reachable port is a hint, and the
// pairing handshake is what turns it into a peer.
type scanCandidate struct {
	Addr string `json:"addr"`
}

// scanResult is POST /api/pair/scan's payload. Subnets and Probed exist so the
// panel can say what was actually tried — "scanned 254 addresses on
// 192.168.1.0/24, found none" is actionable, "nothing found" is not.
type scanResult struct {
	Port       int             `json:"port"`
	Subnets    []string        `json:"subnets"`
	Candidates []scanCandidate `json:"candidates"`
	Probed     int             `json:"probed"`
	ElapsedMS  int64           `json:"elapsed_ms"`
	Truncated  bool            `json:"truncated,omitempty"`
}

// scanPair serves POST /api/pair/scan — an on-demand sweep of the node's local
// IPv4 /24(s) for a listening mesh port.
func (h *handler) scanPair(w http.ResponseWriter, r *http.Request) {
	if h.cfg == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("node not ready"))
		return
	}
	port := defaultMeshPort
	h.readCfg(func(c *config.Config) {
		if p := meshPortFromListenAddr(c.Network.ListenAddr); p != 0 {
			port = p
		}
	})

	start := time.Now()
	targets, subnets := lanScanTargets(port)
	ctx, cancel := context.WithTimeout(r.Context(), lanScanBudget)
	defer cancel()
	hits := probeAddrs(ctx, targets)

	out := scanResult{
		Port:       port,
		Subnets:    subnets,
		Candidates: hits,
		Probed:     len(targets),
		ElapsedMS:  time.Since(start).Milliseconds(),
	}
	if out.Subnets == nil {
		out.Subnets = []string{}
	}
	if out.Candidates == nil {
		out.Candidates = []scanCandidate{}
	}
	if ctx.Err() != nil {
		out.Truncated = true
	}
	writeJSON(w, out)
}

// meshPortFromListenAddr extracts the port the mesh listener uses. An empty or
// unparseable address falls back to the shipped default rather than failing the
// sweep — the port is a best guess either way, since the listener may also be
// bound by a sibling process.
func meshPortFromListenAddr(addr string) int {
	if addr == "" {
		return defaultMeshPort
	}
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return defaultMeshPort
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return defaultMeshPort
	}
	return n
}

// localIPv4Nets lists this host's usable IPv4 interfaces: up, not loopback,
// not link-local (169.254/16 — nothing pairs there).
//
// One walker feeds two answers that must agree. The sweep needs the set to
// skip its own addresses; the pairing panel needs one address to print as
// "this device", because that is what the operator reads out loud while
// standing at the other machine. Two independent enumerations would drift into
// advertising an address the sweep never probes from.
func localIPv4Nets() []*net.IPNet {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []*net.IPNet
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, &net.IPNet{IP: ip4, Mask: ipn.Mask})
		}
	}
	return out
}

// localIPv4Strings is localIPv4Nets as plain addresses, in interface order.
// The first entry is the one a human is most likely to recognise as "this
// machine" (Wi-Fi before virtual bridges on macOS, ethernet first on Linux).
func localIPv4Strings() []string {
	nets := localIPv4Nets()
	out := make([]string, 0, len(nets))
	for _, n := range nets {
		out = append(out, n.IP.String())
	}
	return out
}

// lanScanTargets returns the host:port pairs to probe and the subnet labels
// they belong to.
//
// Only each interface's own /24 is walked even when it is numbered wider: a /16
// campus LAN is 65k dials and the peers this feature exists for sit on the same
// segment. The node's own addresses are excluded because a successful dial to
// itself says nothing about who else is present.
func lanScanTargets(port int) (targets []string, subnets []string) {
	type netSeg struct {
		base string
		ones int
		ip   net.IP
	}
	self := map[string]bool{}
	var segs []netSeg

	for _, ipn := range localIPv4Nets() {
		ip4 := ipn.IP.To4()
		self[ip4.String()] = true
		ones, bits := ipn.Mask.Size()
		if bits != 32 {
			continue
		}
		if ones < 24 {
			ones = 24
		}
		baseIP := ip4.Mask(net.CIDRMask(ones, 32)).To4()
		if baseIP == nil {
			continue
		}
		segs = append(segs, netSeg{base: baseIP.String(), ones: ones, ip: baseIP})
	}

	// Self addresses are collected across every interface before any target is
	// generated, otherwise a second address on the same interface would not yet
	// be known when the first one's subnet is walked.
	seen := map[string]bool{}
	for _, seg := range segs {
		key := fmt.Sprintf("%s/%d", seg.base, seg.ones)
		if seen[key] {
			continue
		}
		seen[key] = true
		subnets = append(subnets, key)
		baseU := binary.BigEndian.Uint32(seg.ip)
		size := uint32(1) << uint(32-seg.ones)
		for i := uint32(1); i+1 < size; i++ {
			var raw [4]byte
			binary.BigEndian.PutUint32(raw[:], baseU+i)
			ip := net.IP(raw[:])
			if self[ip.String()] {
				continue
			}
			targets = append(targets, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		}
	}
	return targets, subnets
}

// probeAddrs dials every target with bounded concurrency and returns the ones
// that accepted. Errors are the common case here and are not reported
// individually: on a /24, 250 of 254 dials are expected to fail.
func probeAddrs(ctx context.Context, targets []string) []scanCandidate {
	if len(targets) == 0 {
		return nil
	}
	sem := make(chan struct{}, lanScanWorkers)
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		hits []scanCandidate
	)
	dialer := net.Dialer{Timeout: lanScanDialTimeout}
	for _, addr := range targets {
		sem <- struct{}{}
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			defer func() { <-sem }()
			conn, err := dialer.DialContext(ctx, "tcp", addr)
			if err != nil {
				// One retry. A lost first SYN on a cold path — ARP entry not
				// resolved, Wi-Fi radio asleep — is indistinguishable from a
				// closed port, and the second attempt is what tells them apart:
				// measured on real hardware, the first dial timed out while the
				// immediate retry completed in under 200ms.
				if ctx.Err() != nil {
					return
				}
				conn, err = dialer.DialContext(ctx, "tcp", addr)
				if err != nil {
					return
				}
			}
			_ = conn.Close()
			mu.Lock()
			hits = append(hits, scanCandidate{Addr: addr})
			mu.Unlock()
		}(addr)
	}
	wg.Wait()
	sort.Slice(hits, func(i, j int) bool {
		a, b := net.ParseIP(hostOf(hits[i].Addr)), net.ParseIP(hostOf(hits[j].Addr))
		if a == nil || b == nil {
			return hits[i].Addr < hits[j].Addr
		}
		return binary.BigEndian.Uint32(a.To4()) < binary.BigEndian.Uint32(b.To4())
	})
	return hits
}

// hostOf splits host:port and returns the host, or the input unchanged when it
// does not split.
func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
