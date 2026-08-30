package main

// Device pairing — the `panda pair` flow the web console's empty-fleet copy
// promises, in three commands:
//
//	panda nodes add <host:port>   # this machine dials a peer: append to peers,
//	                              # generate network.shared_secret when missing
//	panda nodes invite            # print the copy-paste guide for the other
//	                              # machine (never the secret itself)
//	panda pair --secret <s> --peer <host:port>
//	                              # the other machine's half: adopt the secret,
//	                              # dial back
//	panda nodes disconnect <addr> # remove a peer from the dial list
//
// The shared secret is symmetric HMAC material (see internal/bus/auth.go): both
// ends must hold the same value, and it must not travel in plaintext. So
// `nodes add`/`invite` only ever say where it lives (config.yaml under
// network.shared_secret) — the human copies it across machines through their
// own channel, which is the one channel this code cannot open for them.

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"slices"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// generateSharedSecret mints the HMAC material node hellos sign with. Random
// 128-bit hex: enough entropy that guessing is hopeless, short enough to
// paste between machines.
func generateSharedSecret() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// runNodesAdd implements `panda nodes add <host:port>` — validate the address,
// make sure a shared secret exists (generating one when missing), append the
// peer to config.yaml, then print what to do on the other machine. The live
// daemon keeps its old peer list until restart; the hint says so instead of
// letting the user assume the dial already happened.
func runNodesAdd(args []string) {
	fs := flag.NewFlagSet("nodes add", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 1 {
		fatal("usage", fmt.Errorf("panda nodes add <host:port>"))
	}
	addr := rest[0]
	if _, _, err := net.SplitHostPort(addr); err != nil {
		fatal("bad address", fmt.Errorf("%s", i18n.Tf(i18n.Detect(), "cli.nodes.badaddr", "addr", addr)))
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}

	secret := cfg.Network.SharedSecret
	generated := false
	if secret == "" {
		secret, err = generateSharedSecret()
		if err != nil {
			fatal("generate shared secret", err)
		}
		generated = true
	}

	peers := slices.Clone(cfg.Network.Peers)
	if slices.Contains(peers, addr) {
		fmt.Println(i18n.Tf(i18n.Detect(), "cli.nodes.add.exists", "addr", addr))
		printJoinGuide(i18n.Detect(), cfg)
		return
	}
	peers = append(peers, addr)
	if err := config.UpdateNetworkSection(configWritePath(*configPath), config.NetworkConfig{
		ListenAddr:   cfg.Network.ListenAddr,
		SharedSecret: secret,
		Peers:        peers,
	}); err != nil {
		fatal("write config", err)
	}

	loc := i18n.Detect()
	if generated {
		fmt.Println(i18n.T(loc, "cli.nodes.secret.gen"))
	}
	fmt.Println(i18n.Tf(loc, "cli.nodes.add.done", "addr", addr))
	fmt.Println(i18n.T(loc, "cli.nodes.restart"))
	printJoinGuide(i18n.Detect(), cfg)
}

// runNodesDisconnect implements `panda nodes disconnect <addr>` — the opposite
// of add: the address leaves the dial list. (The stale-directory-row cleanup
// stays `nodes remove`, a different thing: that drops a ledger row, this
// changes who the daemon dials.)
func runNodesDisconnect(args []string) {
	fs := flag.NewFlagSet("nodes disconnect", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 1 {
		fatal("usage", fmt.Errorf("panda nodes disconnect <host:port>"))
	}
	addr := rest[0]

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	remaining := make([]string, 0, len(cfg.Network.Peers))
	for _, p := range cfg.Network.Peers {
		if p != addr {
			remaining = append(remaining, p)
		}
	}
	if len(remaining) == len(cfg.Network.Peers) {
		fmt.Println(i18n.Tf(i18n.Detect(), "cli.nodes.disconnect.none", "addr", addr))
		return
	}
	if err := config.UpdateNetworkSection(configWritePath(*configPath), config.NetworkConfig{
		Peers: remaining,
	}); err != nil {
		fatal("write config", err)
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.nodes.disconnect.done", "addr", addr))
	fmt.Println(i18n.T(i18n.Detect(), "cli.nodes.restart"))
}

// runNodesInvite implements `panda nodes invite` — print the peer-side join
// guide without touching the peer list. It is what the web console's
// "add a second device" flow links out to, and what a user pastes to a
// colleague standing in front of the new machine.
func runNodesInvite(args []string) {
	fs := flag.NewFlagSet("nodes invite", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	if cfg.Network.SharedSecret == "" {
		secret, err := generateSharedSecret()
		if err != nil {
			fatal("generate shared secret", err)
		}
		if err := config.UpdateNetworkSection(configWritePath(*configPath), config.NetworkConfig{
			ListenAddr:   cfg.Network.ListenAddr,
			SharedSecret: secret,
		}); err != nil {
			fatal("write config", err)
		}
		fmt.Println(i18n.T(i18n.Detect(), "cli.nodes.secret.gen"))
	}
	printJoinGuide(i18n.Detect(), cfg)
}

// runPair implements `panda pair` — the other machine's half of the join. It
// adopts the secret copied from the inviting node, appends the peer, and
// optionally pins this node's listen address. One command instead of a
// hand-edited config file is the whole point: the values it writes are the
// exact three a join needs, and a typo in any of them fails the handshake.
func runPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	configPath := fs.String("config", "", "path to config.yaml")
	secret := fs.String("secret", "", "shared secret copied from the inviting node's config.yaml")
	peer := fs.String("peer", "", "the inviting node's host:port to dial")
	listen := fs.String("listen", "", "this node's listen address (default: keep current)")
	fs.Parse(args)

	if *secret == "" || *peer == "" {
		fmt.Fprintln(os.Stderr, i18n.T(i18n.Detect(), "cli.pair.usage"))
		os.Exit(2)
	}
	if _, _, err := net.SplitHostPort(*peer); err != nil {
		fatal("bad address", fmt.Errorf("%s", i18n.Tf(i18n.Detect(), "cli.nodes.badaddr", "addr", *peer)))
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	peers := slices.Clone(cfg.Network.Peers)
	if !slices.Contains(peers, *peer) {
		peers = append(peers, *peer)
	}
	if err := config.UpdateNetworkSection(configWritePath(*configPath), config.NetworkConfig{
		ListenAddr:   *listen,
		SharedSecret: *secret,
		Peers:        peers,
	}); err != nil {
		fatal("write config", err)
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.pair.done", "peer", *peer))
	fmt.Println(i18n.T(i18n.Detect(), "cli.nodes.restart"))
}

// printJoinGuide writes the three-step instructions for whoever sets up the
// other machine. The secret itself is deliberately not in the text — only the
// file it lives in — so logs and terminals never carry it.
func printJoinGuide(loc i18n.Locale, cfg *config.Config) {
	listen := cfg.Network.ListenAddr
	if host, port, err := net.SplitHostPort(listen); err == nil && host == "" {
		listen = "<this-machine>" + port
	} else if err == nil && isLoopbackHost(host) {
		// The secure default binds loopback only; a peer on another machine
		// cannot reach 127.0.0.1. Point the operator at setting listen_addr to
		// a routable (or overlay) address before the join can work.
		fmt.Println()
		fmt.Println(i18n.Tf(loc, "cli.nodes.invite.loopback", "port", port))
		listen = "<this-machine>" + port
	}
	fmt.Println()
	fmt.Println(i18n.T(loc, "cli.nodes.invite.head"))
	fmt.Println(i18n.T(loc, "cli.nodes.invite.step1"))
	fmt.Println("  curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh")
	fmt.Println(i18n.Tf(loc, "cli.nodes.invite.step2", "path", configWritePath("")))
	fmt.Println(i18n.Tf(loc, "cli.nodes.invite.step3", "listen", listen))
}

// isLoopbackHost reports whether host names a loopback address (127.0.0.1,
// ::1, or "localhost") — an address a peer on another machine cannot dial.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
