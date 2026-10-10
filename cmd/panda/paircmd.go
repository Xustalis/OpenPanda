// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strings"

	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/scheduler"
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
// shared secret exists, append addr to the peer list, persist, report. A
// running daemon is SIGHUPed into applying the fresh peer list, and the
// self-check dials the peer through the real handshake so the report says
// "linked" or "unreachable" — not "restart and hope".
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
	// Persist the secret only when we just generated it. A secret that
	// Load() injected from OPENPANDA_SHARED_SECRET must stay env-provided —
	// materializing it into config.yaml leaks what the operator chose to
	// keep out of the file.
	persistSecret := ""
	if generated {
		persistSecret = secret
	}
	if err := config.UpdateNetworkSection(configWritePath(configPath), config.NetworkConfig{
		ListenAddr:   cfg.Network.ListenAddr,
		SharedSecret: persistSecret,
		Peers:        peers,
	}); err != nil {
		fatal("write config", err)
	}

	loc := i18n.Detect()
	if generated {
		fmt.Println(i18n.T(loc, "cli.nodes.secret.gen"))
	}
	fmt.Println(i18n.Tf(loc, "cli.nodes.add.done", "addr", addr))
	notifyDaemonMeshTo(os.Stdout)
	// The in-memory cfg predates the write; the self-check must present the
	// secret we just persisted, or it would test the old material.
	cfg.Network.SharedSecret = secret
	selfCheckPeer(os.Stdout, loc, configPath, cfg, addr)
	printJoinGuide(i18n.Detect(), cfg)
}

// selfCheckPeer runs the admit-time link check: one real outbound handshake
// against the just-configured address (ProbePeer). Encrypted links report
// ready immediately. A failure whose cause could be the cleartext gate (the
// peer is a pre-sessaead build) opens the host-scoped exemption and retries
// once — the operator's admit command is the consent for exactly that host.
// punch: peers ride the UDP plane; there is nothing to probe over TCP.
func selfCheckPeer(w io.Writer, loc i18n.Locale, configPath string, cfg *config.Config, addr string) {
	if strings.HasPrefix(addr, "punch:") {
		return
	}
	var pub ed25519.PublicKey
	var privKey ed25519.PrivateKey
	if db, _, err := panelStore(cfg); err == nil {
		pub, privKey, _ = core.LoadNodeKey(db)
		db.Close()
	}
	selfID := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())
	card := ledger.Card{Device: cfg.Node.Name, NodeKind: cfg.Node.Kind, NodeIdentity: cfg.Node.EffectiveIdentity()}
	for attempt := 0; attempt < 2; attempt++ {
		res, err := core.ProbePeer(context.Background(), selfID, card, cfg.Model, cfg.Network, addr, pub, privKey)
		if err == nil {
			if res.Encrypted {
				fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.probe.enc", "id", res.PeerID))
			} else {
				fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.probe.plain", "id", res.PeerID))
			}
			return
		}
		if attempt == 0 && core.CleartextDialError(addr, cfg.Network.AllowCleartext, cfg.Network.AllowCleartextFor) != nil {
			ensureCleartextAllowed(w, loc, configPath, cfg, addr)
			continue
		}
		fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.probe.fail", "err", err.Error()))
		return
	}
}

// warnIfCleartextRefused warns when addr would trip the dial-time cleartext
// gate (cleartextOK): the add/pair paths persist the peer before the daemon
// ever dials, so a refused ws:// LAN address would otherwise only surface as
// a `peer dial failed` line inside keepalive logs — the "admit succeeded but
// it never connects" trap the LAN-discovery flow runs straight into.
func warnIfCleartextRefused(w io.Writer, loc i18n.Locale, cfg *config.Config, addr string) {
	if core.CleartextDialError(addr, cfg.Network.AllowCleartext, cfg.Network.AllowCleartextFor) != nil {
		fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.cleartext.hint", "addr", addr))
	}
}

// ensureCleartextAllowed upgrades warnIfCleartextRefused for the explicit
// admit/add/pair paths: when the cleartext gate would refuse the very address
// the operator just asked to connect, the refusal is itself the bug — the
// operator's command IS consent to talk to that host. So the gate opens for
// exactly that host (never a subnet, never global) and the change is said out
// loud. A punch: peer rides the AEAD plane and needs nothing; a wss:// or
// safe host never reaches here. On a persistence failure the old warning
// still prints — the peer row is already written either way.
func ensureCleartextAllowed(w io.Writer, loc i18n.Locale, configPath string, cfg *config.Config, addr string) {
	if core.CleartextDialError(addr, cfg.Network.AllowCleartext, cfg.Network.AllowCleartextFor) == nil {
		return
	}
	host := peerDialHost(addr)
	if host == "" {
		fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.cleartext.hint", "addr", addr))
		return
	}
	allowFor := append(slices.Clone(cfg.Network.AllowCleartextFor), host)
	if err := config.UpdateNetworkSection(configWritePath(configPath), config.NetworkConfig{
		AllowCleartextFor: allowFor,
	}); err != nil {
		fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.cleartext.hint", "addr", addr))
		return
	}
	cfg.Network.AllowCleartextFor = allowFor
	fmt.Fprintln(w, i18n.Tf(loc, "cli.nodes.cleartext.opened", "host", host))
}

// peerDialHost extracts the host a ws:// dial would target from a configured
// peer address: bare host:port, ws(s):// URLs, or "" for punch: ids (those
// never touch the cleartext gate).
func peerDialHost(addr string) string {
	if strings.HasPrefix(addr, "punch:") {
		return ""
	}
	u := strings.TrimPrefix(strings.TrimPrefix(addr, "ws://"), "wss://")
	host, _, err := net.SplitHostPort(u)
	if err != nil {
		return ""
	}
	return host
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
	if len(rest) > 1 {
		fatal("usage", fmt.Errorf("panda nodes admit [node-id]"))
	}
	id := ""
	if len(rest) == 1 {
		id = rest[0]
	}

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
	if id == "" {
		// Bare `nodes admit`: the common case is exactly one node waiting —
		// admit it without making the operator retype an id they just read.
		// With several pending rows there is no safe default to pick.
		if len(pending) == 1 {
			id = pending[0].ID
		} else {
			fatal("usage", fmt.Errorf("panda nodes admit <node-id>"))
		}
	}
	for _, p := range pending {
		if p.ID != id {
			continue
		}
		admitPeerAddr(*configPath, cfg, p.Addr)
		_ = ledger.ForgetPending(db, id)
		fmt.Println(i18n.Tf(loc, "cli.nodes.admit.done", "id", id))
		if !p.Verified {
			fmt.Println(i18n.Tf(loc, "cli.nodes.admit.unverified", "id", id))
		}
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
	notifyDaemonMeshTo(os.Stdout)
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

// runPair implements `panda pair` — Bluetooth-style LAN pairing:
//
//	panda pair                 # 发现的设备 + 待确认的配对请求
//	panda pair <设备名|地址>   # 向该设备发起配对,显示配对码,等待对方确认
//	panda pair confirm [id]    # 对方设备上:核对配对码并确认 → 入网
//	panda pair reject [id]     # 拒绝
//	panda pair --secret s --peer a   # 手动抄密钥的旧路径(SSH/无发现场景)
//
// The interactive path never touches the shared secret by hand: an
// ephemeral X25519 exchange binds a 6-digit code to both identity keys,
// the humans compare the code on both screens, and the responder hands
// the secret over the DH-sealed channel only after confirmation.
func runPair(args []string) {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	secret := fs.String("secret", "", "shared secret copied from the inviting node's config.yaml")
	peer := fs.String("peer", "", "the inviting node's host:port to dial")
	listen := fs.String("listen", "", "this node's listen address (default: keep current)")
	yes := fs.Bool("yes", false, "skip the interactive code-compare prompt (confirm only)")
	fs.Parse(reorderFlags(args, commonValueFlags))
	rest := fs.Args()

	// Legacy manual path stays: --secret/--peer is still the way to join a
	// machine you can only reach over SSH, where no screen exists to
	// compare a code against.
	if *secret != "" && *peer != "" {
		runPairLegacy(*configPath, *secret, *peer, *listen)
		return
	}
	if *secret != "" || *peer != "" || *listen != "" {
		fmt.Fprintln(os.Stderr, i18n.T(i18n.Detect(), "cli.pair.usage"))
		os.Exit(2)
	}

	if len(rest) == 0 {
		pairList(os.Stdout, *configPath)
		return
	}
	switch rest[0] {
	case "confirm":
		pairAnswer(os.Stdout, *configPath, rest[1:], core.PairStateConfirmed, *yes)
	case "reject":
		pairAnswer(os.Stdout, *configPath, rest[1:], core.PairStateRejected, true)
	default:
		pairInitiate(os.Stdout, *configPath, rest[0])
	}
}

// runPairLegacy is the pre-discovery join: adopt a hand-copied secret +
// peer address. Kept verbatim — nothing about it becomes wrong just
// because a better front door now exists.
func runPairLegacy(configPath, secret, peer, listen string) {
	if err := config.ValidatePeerAddr(peer); err != nil {
		fatal("bad address", fmt.Errorf("%s", i18n.Tf(i18n.Detect(), "cli.nodes.badaddr", "addr", peer)))
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config", err)
	}
	peers := slices.Clone(cfg.Network.Peers)
	if !slices.Contains(peers, peer) {
		peers = append(peers, peer)
	}
	if err := config.UpdateNetworkSection(configWritePath(configPath), config.NetworkConfig{
		ListenAddr:   listen,
		SharedSecret: secret,
		Peers:        peers,
	}); err != nil {
		fatal("write config", err)
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.pair.done", "peer", peer))
	notifyDaemonMeshTo(os.Stdout)
	// Same caveat as admitPeerAddr: the loaded cfg predates the write, so
	// hand the self-check the secret the joining operator just typed.
	cfg.Network.SharedSecret = secret
	selfCheckPeer(os.Stdout, i18n.Detect(), configPath, cfg, peer)
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
		// A loopback listener is reachable only from this machine — a peer
		// elsewhere cannot dial 127.0.0.1. Point the operator at a routable
		// (or overlay) listen_addr before the join can work.
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
		if core.CleartextDialError("ws://"+host, cfg.Network.AllowCleartext, cfg.Network.AllowCleartextFor) != nil {
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

// runNodesDrain implements `panda nodes drain [id] [--off]` — maintenance
// mode (Track 2). Draining writes a flag into the shared settings table; the
// running daemon picks it up within one heartbeat and then: advertises
// "draining" instead of "online" so peers stop routing new work here,
// declines inbound delegates outright, and pauses new queue claims — while
// in-flight tasks run to completion. Only the local node can be drained:
// there is no remote admin channel, so a remote id fails with the
// instruction to run the command on that machine.
func runNodesDrain(args []string) {
	fs := flag.NewFlagSet("nodes drain", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	off := fs.Bool("off", false, "lift the drain and resume accepting work")
	fs.Parse(reorderFlags(args, commonValueFlags))
	rest := fs.Args()
	if len(rest) > 1 {
		fatal("usage", fmt.Errorf("panda nodes drain [id] [--off]"))
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	loc := i18n.Detect()
	selfID := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())
	id := selfID
	if len(rest) == 1 {
		id = rest[0]
	}
	if !scheduler.SameRuntimeIdentity(id, selfID) {
		fatal("drain node", fmt.Errorf("%s", i18n.Tf(loc, "cli.nodes.drain.notSelf", "id", id)))
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	v := "1"
	key := "cli.nodes.drain.on"
	if *off {
		v, key = "0", "cli.nodes.drain.off"
	}
	if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES('node_drain', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, v); err != nil {
		fatal("set drain flag", err)
	}
	fmt.Println(i18n.Tf(loc, key, "id", selfID))
}

// pairList implements bare `panda pair`: the two things an operator needs
// in one view — pairing requests waiting for THIS machine's answer, and
// LAN devices discovered via beacons that can be paired to.
func pairList(w io.Writer, configPath string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()
	loc := i18n.Detect()
	p := pal()

	sessions, err := core.ListPairSessions(db)
	if err != nil {
		fatal("list pair sessions", err)
	}
	ready := 0
	for _, s := range sessions {
		if s.State != core.PairStateReady {
			continue
		}
		if ready == 0 {
			fmt.Fprintln(w, i18n.T(loc, "cli.pair.pending.head"))
		}
		ready++
		fmt.Fprintf(w, "  %s  %s  %s  %s\n",
			p.Warn(s.ID[:8]), cell(s.PeerName, 20), cell(s.PeerAddr, 24), p.Warn(s.SAS))
	}
	if ready > 0 {
		fmt.Fprintln(w, i18n.T(loc, "cli.pair.pending.hint"))
	}

	pending, err := ledger.ListPending(db, 90*time.Second)
	if err != nil {
		fatal("query pending", err)
	}
	shown := 0
	for _, n := range pending {
		if shown == 0 {
			fmt.Fprintln(w)
			fmt.Fprintln(w, i18n.T(loc, "cli.pair.discovered.head"))
		}
		shown++
		mark := p.Success(p.MarkOK())
		if !n.Verified {
			mark = p.Warn(p.Glyph("⚠", "!"))
		}
		fmt.Fprintf(w, "  %s  %s  %s\n", mark, cell(n.ID, 30), cell(n.Addr, 24))
	}
	if shown > 0 {
		fmt.Fprintln(w, i18n.T(loc, "cli.pair.discovered.hint"))
	}
	if ready == 0 && shown == 0 {
		fmt.Fprintln(w, i18n.T(loc, "cli.pair.none"))
	}
}

// pairInitiate implements `panda pair <name|addr>` — the initiator half:
// resolve the target, open the pairing channel, exchange ephemeral keys,
// display the code, and wait out the responder's confirmation. On success
// the delivered secret + peer address land in config and the local daemon
// is nudged to dial — by the time the command exits, the join is real.
func pairInitiate(w io.Writer, configPath, target string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()
	loc := i18n.Detect()

	addr, displayName, err := resolvePairTarget(db, target)
	if err != nil {
		fatal("resolve target", err)
	}
	if cfg.Network.SharedSecret != "" && len(cfg.Network.Peers) > 0 {
		fmt.Fprintln(w, i18n.T(loc, "cli.pair.adopt.warn"))
	}

	pub, priv, _ := core.LoadNodeKey(db)
	if len(pub) == 0 {
		pub, priv, _ = ed25519.GenerateKey(rand.Reader)
	}
	_ = priv
	selfID := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())

	dialAddr := normalizeDialable(addr)
	fmt.Fprintln(w, i18n.Tf(loc, "cli.pair.start", "name", displayName, "addr", dialAddr))
	pc, err := core.DialPair(context.Background(), dialAddr, selfID, cfg.Node.Name, cfg.Network.ListenAddr, pub)
	if err != nil {
		var rj *core.PairRejected
		if errors.As(err, &rj) {
			fatal("pair", fmt.Errorf("%s", i18n.Tf(loc, "cli.pair.rejected", "reason", rj.Reason)))
		}
		fatal("dial", fmt.Errorf("%s — %s", dialAddr, i18n.T(loc, "cli.pair.unreachable")))
	}
	defer pc.Close()

	p := pal()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+p.Accent(i18n.Tf(loc, "cli.pair.code", "code", pc.Code)))
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.Tf(loc, "cli.pair.confirm_hint", "name", displayName))
	fmt.Fprintln(w, i18n.T(loc, "cli.pair.waiting"))

	// Wait out the human on the other side: the responder's session TTL is
	// 4 minutes, so give up slightly past it.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute+30*time.Second)
	defer cancel()
	secret, err := pc.WaitSecret(ctx)
	if err != nil {
		var rj *core.PairRejected
		switch {
		case errors.As(err, &rj):
			fatal("pair", fmt.Errorf("%s", i18n.Tf(loc, "cli.pair.rejected", "reason", rj.Reason)))
		case errors.Is(err, context.DeadlineExceeded):
			fatal("pair", fmt.Errorf("%s", i18n.T(loc, "cli.pair.timeout")))
		default:
			fatal("waiting", fmt.Errorf("%s — %s", err, i18n.T(loc, "cli.pair.daemonhint")))
		}
	}
	adoptPairedSecret(w, loc, configPath, cfg, secret, dialAddr)
}

// adoptPairedSecret lands the join on the initiator: persist the delivered
// secret + responder address, nudge the daemon, and run the same link
// self-check the manual path does.
func adoptPairedSecret(w io.Writer, loc i18n.Locale, configPath string, cfg *config.Config, secret, peerAddr string) {
	peers := slices.Clone(cfg.Network.Peers)
	if !slices.Contains(peers, peerAddr) {
		peers = append(peers, peerAddr)
	}
	if err := config.UpdateNetworkSection(configWritePath(configPath), config.NetworkConfig{
		SharedSecret: secret,
		Peers:        peers,
	}); err != nil {
		fatal("write config", err)
	}
	fmt.Fprintln(w, i18n.Tf(loc, "cli.pair.joined", "addr", peerAddr))
	notifyDaemonMeshTo(w)
	cfg.Network.SharedSecret = secret
	selfCheckPeer(w, loc, configPath, cfg, peerAddr)
}

// pairAnswer implements `panda pair confirm|reject [id]` — the responder's
// human decision. The daemon holds the session; this command flips the
// row and, on confirm, waits for the join to actually complete.
func pairAnswer(w io.Writer, configPath string, args []string, state string, autoYes bool) {
	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()
	loc := i18n.Detect()

	sessions, err := core.ListPairSessions(db)
	if err != nil {
		fatal("list pair sessions", err)
	}
	var ready []core.PairSessionRow
	for _, s := range sessions {
		if s.State == core.PairStateReady {
			ready = append(ready, s)
		}
	}
	if len(ready) == 0 {
		fatal("pair", fmt.Errorf("%s", i18n.T(loc, "cli.pair.confirm.none")))
	}
	var target core.PairSessionRow
	if len(args) > 0 {
		found := false
		for _, s := range ready {
			if strings.HasPrefix(s.ID, args[0]) || s.PeerName == args[0] {
				target, found = s, true
				break
			}
		}
		if !found {
			fatal("pair", fmt.Errorf("%s: %s", i18n.T(loc, "cli.pair.confirm.none"), args[0]))
		}
	} else if len(ready) == 1 {
		target = ready[0]
	} else {
		fmt.Fprintln(w, i18n.T(loc, "cli.pair.confirm.busy"))
		for _, s := range ready {
			fmt.Fprintf(w, "  %s  %s  %s\n", s.ID[:8], cell(s.PeerName, 20), s.SAS)
		}
		os.Exit(2)
	}

	if state == core.PairStateConfirmed {
		fmt.Fprintln(w, i18n.Tf(loc, "cli.pair.incoming", "name", target.PeerName, "addr", target.PeerAddr))
		p := pal()
		fmt.Fprintln(w, "  "+p.Accent(i18n.Tf(loc, "cli.pair.code", "code", target.SAS)))
		if !autoYes {
			fmt.Fprint(w, i18n.T(loc, "cli.pair.confirm_prompt"))
			var answer string
			fmt.Scanln(&answer)
			if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
				state = core.PairStateRejected
			}
		}
	}
	if err := core.AnswerPairSession(db, target.ID, state); err != nil {
		fatal("answer pair session", err)
	}
	if state == core.PairStateRejected {
		fmt.Fprintln(w, i18n.T(loc, "cli.pair.denied"))
		return
	}
	// The daemon's session goroutine resolves within ~1s of the flip; give
	// it a few seconds so the user sees "joined" rather than "probably".
	for i := 0; i < 30; i++ {
		st, err := core.PairSessionState(db, target.ID)
		if err != nil || st == "" {
			break
		}
		if st == core.PairStateDone {
			fmt.Fprintln(w, i18n.Tf(loc, "cli.pair.confirmed", "name", target.PeerName))
			return
		}
		if st == core.PairStateExpired || st == core.PairStateRejected {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	fmt.Fprintln(w, i18n.Tf(loc, "cli.pair.confirmed", "name", target.PeerName))
}

// normalizeDialable strips ws://, wss:// and a trailing /ws from an
// address — peer entries and dial targets live as bare host:port.
func normalizeDialable(addr string) string {
	addr = strings.TrimPrefix(addr, "ws://")
	addr = strings.TrimPrefix(addr, "wss://")
	return strings.TrimSuffix(addr, "/ws")
}

// resolvePairTarget turns the operator's selection into a dialable
// host:port — a pending-discovery id or configured-name fragment first,
// then a literal host:port or ws(s):// address.
func resolvePairTarget(db *sql.DB, target string) (addr, name string, err error) {
	pending, err := ledger.ListPending(db, 90*time.Second)
	if err != nil {
		return "", "", err
	}
	for _, n := range pending {
		if n.ID == target || scheduler.NodeNamePart(n.ID) == target || n.Addr == target {
			return n.Addr, scheduler.NodeNamePart(n.ID), nil
		}
	}
	// Literal address forms — ValidatePeerAddr will vet them at dial time.
	if config.ValidatePeerAddr(target) == nil {
		return target, scheduler.NodeNamePart(target), nil
	}
	return "", "", fmt.Errorf("%s", i18n.Tf(i18n.Detect(), "cli.pair.notfound", "target", target))
}
