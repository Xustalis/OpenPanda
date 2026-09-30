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
	"io"
	"net"
	"os"
	"slices"

	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
	rest := fs.Args()
	if len(rest) != 1 {
		fatal("usage", fmt.Errorf("panda nodes add <host:port>"))
	}
	addr := rest[0]
	// host:port dials ws:// (subject to the cleartext gate at dial time);
	// an explicit ws(s):// URL carries its scheme — wss is the way to reach
	// a peer over an untrusted network; punch:<id> names a NAT peer.
	if err := config.ValidatePeerAddr(addr); err != nil {
		fatal("bad address", fmt.Errorf("%s", i18n.Tf(i18n.Detect(), "cli.nodes.badaddr", "addr", addr)))
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	admitPeerAddr(*configPath, cfg, addr)
}

// admitPeerAddr is the shared half of `nodes add` and `nodes admit`: ensure a
// shared secret exists, append addr to the peer list, persist, report. The
// daemon picks the new peer up on restart — the restart hint says so.
func admitPeerAddr(configPath string, cfg *config.Config, addr string) {
	secret := cfg.Network.SharedSecret
	generated := false
	if secret == "" {
		var err error
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
	if err := config.UpdateNetworkSection(configWritePath(configPath), config.NetworkConfig{
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
	warnIfCleartextRefused(os.Stdout, loc, cfg, addr)
	printJoinGuide(i18n.Detect(), cfg)
}

// warnIfCleartextRefused warns when addr would trip the dial-time cleartext
// gate (cleartextOK): the add/pair paths persist the peer before the daemon
// ever dials, so a refused ws:// LAN address would otherwise only surface as
// a `peer dial failed` line inside keepalive logs — the "admit succeeded but
// it never connects" trap the LAN-discovery flow runs straight into.
func warnIfCleartextRefused(w io.Writer, loc i18n.Locale, cfg *config.Config, addr string) {
	if core.CleartextDialError(addr, cfg.Network.AllowCleartext) != nil {
		fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.cleartext.hint", "addr", addr))
	}
}

// runNodesAdmit implements `panda nodes admit <id>` — convert a LAN-discovered
// pending row into a configured peer in one step: look up the beacon's
// advertised address, run the same add path `nodes add` uses, then drop the
// pending row. Admission still travels through the operator's own channels —
// the beacon only carried the hint; the shared secret and the signed hello
// remain what actually let the node in.
func runNodesAdmit(args []string) {
	fs := flag.NewFlagSet("nodes admit", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
	rest := fs.Args()
	if len(rest) != 1 {
		fatal("usage", fmt.Errorf("panda nodes admit <node-id>"))
	}
	id := rest[0]

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	pending, err := ledger.ListPending(db, 90*time.Second)
	if err != nil {
		fatal("query pending", err)
	}
	loc := i18n.Detect()
	for _, p := range pending {
		if p.ID != id {
			continue
		}
		admitPeerAddr(*configPath, cfg, p.Addr)
		_ = ledger.ForgetPending(db, id)
		fmt.Println(i18n.Tf(loc, "cli.nodes.admit.done", "id", id))
		return
	}
	fatal("admit node", fmt.Errorf("%s", i18n.Tf(loc, "cli.nodes.admit.none", "id", id)))
}

// runNodesDisconnect implements `panda nodes disconnect <addr>` — the opposite
// of add: the address leaves the dial list. (The stale-directory-row cleanup
// stays `nodes remove`, a different thing: that drops a ledger row, this
// changes who the daemon dials.)
func runNodesDisconnect(args []string) {
	fs := flag.NewFlagSet("nodes disconnect", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	fs.Parse(reorderFlags(args, commonValueFlags))
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
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
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	secret := fs.String("secret", "", "shared secret copied from the inviting node's config.yaml")
	peer := fs.String("peer", "", "the inviting node's host:port to dial")
	listen := fs.String("listen", "", "this node's listen address (default: keep current)")
	fs.Parse(args)

	if *secret == "" || *peer == "" {
		fmt.Fprintln(os.Stderr, i18n.T(i18n.Detect(), "cli.pair.usage"))
		os.Exit(2)
	}
	if err := config.ValidatePeerAddr(*peer); err != nil {
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
	warnIfCleartextRefused(os.Stdout, i18n.Detect(), cfg, *peer)
}

// printJoinGuide writes the three-step instructions for whoever sets up the
// other machine. The secret itself is deliberately not in the text — only the
// file it lives in — so logs and terminals never carry it.
func printJoinGuide(loc i18n.Locale, cfg *config.Config) {
	printJoinGuideTo(os.Stdout, loc, cfg)
}

// printJoinGuideTo is the writer-scoped form — /nodes invite and /nodes add
// route it through commandOutput so the guide lands in the TUI transcript
// instead of escaping to the host terminal behind the alt screen.
func printJoinGuideTo(w io.Writer, loc i18n.Locale, cfg *config.Config) {
	listen := cfg.Network.ListenAddr
	if host, port, err := net.SplitHostPort(listen); err == nil && host == "" {
		listen = "<this-machine>" + port
	} else if err == nil && isLoopbackHost(host) {
		// The secure default binds loopback only; a peer on another machine
		// cannot reach 127.0.0.1. Point the operator at setting listen_addr to
		// a routable (or overlay) address before the join can work.
		fmt.Fprintln(w)
		fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.invite.loopback", "port", port))
		listen = "<this-machine>" + port
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T(loc, "cli.nodes.invite.head"))
	fmt.Fprintln(w, i18n.T(loc, "cli.nodes.invite.step1"))
	fmt.Fprintln(w, "  curl -fsSL https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.sh | sh")
	fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.invite.step2", "path", configWritePath("")))
	fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.invite.step3", "listen", listen))
	fmt.Fprintln(w, i18n.T(loc, "cli.nodes.invite.step4"))
	// The joining side will dial ws://<listen>: when this machine's listen
	// address is a literal host outside the cleartext gate's safe set, the
	// join dead-ends in the joiner's keepalive log unless we say so here.
	if host, _, err := net.SplitHostPort(cfg.Network.ListenAddr); err == nil && host != "" {
		if core.CleartextDialError("ws://"+host, false) != nil {
			fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.cleartext.hint", "addr", "ws://"+cfg.Network.ListenAddr))
		}
	}
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
