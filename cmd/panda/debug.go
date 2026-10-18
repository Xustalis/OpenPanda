// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// The daemon's diagnostic surface: --debug-addr mounts net/http/pprof on a
// loopback-only listener. A steady-state node has no other way to answer
// "what is it doing" — a Pi user hitting a stall pulls goroutine/heap/cpu
// profiles off it instead of attaching a debugger. Off by default: it is an
// unauthenticated self-portrait of the process, so it only ever binds
// loopback and only when the operator asks.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"time"

	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/Xustalis/OpenPanda/internal/log"
)

// startDebugServer serves the pprof mux on addr, which must be loopback —
// the handlers expose goroutine stacks, heap contents and CPU profiles, none
// of which belongs on a LAN. Returns the bound listener's address for the
// startup log, or an error when addr is not a usable loopback.
func startDebugServer(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("want HOST:PORT, got %q", addr)
	}
	if !loopbackOnly(host) {
		return fmt.Errorf("pprof binds loopback only (127.0.0.1:PORT or [::1]:PORT), got %q", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	logger := log.From(context.Background())
	guard.Go(logger, "daemon: pprof", nil, func() {
		srv := &http.Server{ReadHeaderTimeout: 5 * time.Second}
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Warn("pprof listener stopped", "err", err)
		}
	})
	logger.Info("pprof listening", "url", "http://"+ln.Addr().String()+"/debug/pprof/")
	return nil
}

// loopbackOnly reports whether host names ONLY loopback addresses: 127.x,
// ::1, or a name that resolves to nothing but loopback. An empty host — the
// ":6060" wildcard — is deliberately not loopback: it would answer on every
// interface. The paircmd isLoopbackHost sibling does the literal check;
// this one resolves because pprof must not trust a name that points at a
// real interface.
func loopbackOnly(host string) bool {
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return len(addrs) > 0
}
