// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// listenOn grabs a free loopback port for the panel-probe tests.
func listenOn(t *testing.T) (net.Listener, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return l, port
}

func TestPanelReachableLiveAndDead(t *testing.T) {
	l, port := listenOn(t)
	// Accept in the background so the probe's TCP dial completes.
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if !panelReachable("127.0.0.1:" + port) {
		t.Fatal("panelReachable should see a live listener")
	}
	l.Close()
	// After the listener dies the port refuses — and the +5 fallback span
	// finds nothing either.
	if panelReachable("127.0.0.1:" + port) {
		t.Fatal("panelReachable should fail on a dead port")
	}
	if panelReachable("not-an-address") {
		t.Fatal("panelReachable should fail on a malformed address")
	}
}

func TestPanelReachableWildcard(t *testing.T) {
	l, port := listenOn(t)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	// A wildcard bind probes loopback — the listener is local by definition.
	if !panelReachable("0.0.0.0:" + port) {
		t.Fatal("wildcard panel addr should probe 127.0.0.1")
	}
}

func TestPanelReachableSpan(t *testing.T) {
	// The panel slips forward a few ports when the configured one is taken;
	// a listener anywhere inside the +5 span still counts as a consumer.
	l, port := listenOn(t)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	p, _ := strconv.Atoi(port)
	// Ask about base-2 so the live port sits inside the probed window.
	if !panelReachable("127.0.0.1:" + strconv.Itoa(p-2)) {
		t.Fatal("a panel that slipped to a nearby port should still count as alive")
	}
}

func TestQueueConsumerAlivePanelOnly(t *testing.T) {
	l, port := listenOn(t)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	cfg := &config.Config{
		// Kind vm, not physical: EffectiveIdentity() ignores Identity on a
		// physical node and returns the machine's real identity — under a
		// host running an actual daemon this test would (correctly!) see a
		// live consumer. A vm kind keeps the probe scoped to the test name.
		Node:    config.NodeConfig{Kind: "vm", Identity: "test-no-daemon-identity"},
		Network: config.NetworkConfig{PanelAddr: "127.0.0.1:" + port},
	}
	if !queueConsumerAlive(cfg) {
		t.Fatal("a reachable panel should count as a live consumer")
	}
	l.Close()
	if queueConsumerAlive(cfg) {
		t.Fatal("dead panel + no daemon lock should report no consumer")
	}
	if queueConsumerAlive(nil) {
		t.Fatal("nil config must not panic and should report no consumer")
	}
}

func TestWarnNoConsumerTo(t *testing.T) {
	var buf bytes.Buffer
	warnNoConsumerTo(&buf, i18n.English)
	if !strings.Contains(buf.String(), "panda daemon") {
		t.Fatalf("warning should name the consumer commands, got %q", buf.String())
	}
}
