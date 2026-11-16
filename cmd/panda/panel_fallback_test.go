// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

func TestMeshFallbackWhileDaemonStopped(t *testing.T) {
	for _, hasView := range []bool{false, true} {
		t.Run(map[bool]string{false: "no local view", true: "stale local view"}[hasView], func(t *testing.T) {
			cfg := config.Default()
			cfg.Network.ListenAddr = "127.0.0.1:1"
			cfg.Network.Peers = []config.PeerEntry{{Addr: "punch:designated-relay", RelayPriority: config.RelayPriorityFallback}}
			db := testdb.Open(t)
			var views []nodeStatusView
			if hasView {
				views = []nodeStatusView{{Node: ledger.Node{ID: "self", Neighbors: []string{"designated-relay"}}, Local: true, Running: false}}
			}
			var buf bytes.Buffer
			printMeshLineTo(&buf, i18n.Locale("en"), cfg, db, views)
			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if len(lines) != 2 || !strings.Contains(lines[1], "punch:designated-relay") || !strings.Contains(lines[1], "down") {
				t.Fatalf("stopped daemon must name its configured fallback as down: %q", buf.String())
			}
		})
	}
}

func TestMeshFallbackLiveAndUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		neighbors []string
		brokenDB  bool
		unmarked  bool
		peer      string
		state     string
	}{
		{name: "first relay", neighbors: []string{"first", "backup"}, peer: "relay.example:7836", state: "up"},
		{name: "backup relay", neighbors: []string{"backup"}, peer: "punch:backup", state: "up"},
		{name: "neither linked", peer: "relay.example:7836", state: "down"},
		{name: "unreadable bindings", neighbors: []string{"first", "backup"}, brokenDB: true, peer: "relay.example:7836", state: "down"},
		{name: "no fallback configured", unmarked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Network.ListenAddr = "127.0.0.1:1"
			cfg.Network.Peers = []config.PeerEntry{
				{Addr: "relay.example:7836", RelayPriority: config.RelayPriorityFallback},
				{Addr: "punch:backup", RelayPriority: config.RelayPriorityFallback},
			}
			if tc.unmarked {
				cfg.Network.Peers = []config.PeerEntry{{Addr: "relay.example:7836"}}
			}
			db := testdb.Open(t)
			if err := ledger.RecordPeerAddr(db, "relay.example:7836", "first"); err != nil {
				t.Fatal(err)
			}
			if tc.brokenDB {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			views := []nodeStatusView{{Node: ledger.Node{ID: "self", Neighbors: tc.neighbors}, Local: true, Running: true}}
			var buf bytes.Buffer
			printMeshLineTo(&buf, i18n.Locale("en"), cfg, db, views)
			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if tc.unmarked {
				if len(lines) != 1 {
					t.Fatalf("unmarked peer produced a fallback line: %q", buf.String())
				}
			} else {
				want := i18n.Tf(i18n.Locale("en"), "cli.status.mesh.fallback", "peer", tc.peer, "state", tc.state)
				if len(lines) != 2 || !strings.Contains(lines[1], want) {
					t.Fatalf("fallback must name %s as %s: %q", tc.peer, tc.state, buf.String())
				}
			}
		})
	}
}
