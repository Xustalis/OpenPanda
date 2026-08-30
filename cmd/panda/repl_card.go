package main

// /card and /nodes add — the REPL's window on the structured card edits and
// device pairing. Both run the exact writers the CLI verbs use (cardmut and
// config.UpdateNetworkSection), then go one step further than the CLI can:
// the ask engine's scheduler is in this same process, so a card edit ends in
// Engine.ReloadCard and a peer add ends in Engine.DialPeer — live, zero
// restart, zero SIGHUP.
//
// The grammar mirrors the CLI's:
//
//	/card                                            summary of this node's card
//	/card native add <id> --command <cmd> [--args a,b] [--tier 1|2] [--description …]
//	/card native remove <id>
//	/card agent add <name> --adapter <script> [--capabilities a,b] …
//	/card agent remove <name>
//	/card agent set <name> tier=2 capabilities=code,shell
//	/card manual add <id> --notify <contact>
//	/card manual remove <id>
//	/nodes add <host:port>        append peer + generate secret + live dial
//	/nodes disconnect <addr>      remove a peer from the dial list
//	/nodes invite                 print the join guide for the other machine

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/cardmut"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// cmdCard views or edits this node's capability card from the REPL.
func (r *repl) cmdCard(arg string) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		r.cardSummary()
		return
	}
	switch fields[0] {
	case "native":
		r.cardNative(fields[1:])
	case "agent", "agents":
		r.cardAgent(fields[1:])
	case "manual":
		r.cardManual(fields[1:])
	default:
		fmt.Println(i18n.T(r.loc, "repl.card.usage"))
	}
}

// cardSummary prints the one-glance view of the card: what this machine is,
// what it can run, and where the file lives. Counts, not the full YAML — the
// full file is `panda card show` (or /card edit via the CLI); what a /card
// user needs mid-conversation is "did my edit land and what's on there now".
func (r *repl) cardSummary() {
	path := r.cardPath
	if path == "" {
		fmt.Println(i18n.T(r.loc, "repl.card.none"))
		return
	}
	card, err := ledger.LoadCard(path)
	if err != nil {
		fmt.Println(i18n.Tf(r.loc, "repl.card.loadFail", "err", err.Error()))
		return
	}
	fmt.Println(i18n.Tf(r.loc, "repl.card.head", "path", path))
	fmt.Printf("  %s · %s · %s\n", orDash(card.Device), orDash(card.ResourceClass), orDash(card.Chip))
	if ids := nativeIDs(card); len(ids) > 0 {
		fmt.Printf("  native (%d): %s\n", len(ids), strings.Join(ids, ", "))
	} else {
		fmt.Printf("  native (0)\n")
	}
	if len(card.Agents) > 0 {
		names := make([]string, 0, len(card.Agents))
		for name := range card.Agents {
			names = append(names, name)
		}
		slices.Sort(names)
		fmt.Printf("  agents (%d): %s\n", len(names), strings.Join(names, ", "))
	} else {
		fmt.Printf("  agents (0)\n")
	}
	if len(card.Manual) > 0 {
		ids := make([]string, 0, len(card.Manual))
		for _, ab := range card.Manual {
			ids = append(ids, ab.ID)
		}
		fmt.Printf("  manual (%d): %s\n", len(ids), strings.Join(ids, ", "))
	} else {
		fmt.Printf("  manual (0)\n")
	}
	fmt.Printf("  %s\n", i18n.T(r.loc, "repl.card.editHint"))
}

// cardNative runs /card native add|remove — one command ability at a time.
func (r *repl) cardNative(rest []string) {
	if len(rest) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.card.native.usage"))
		return
	}
	verb, tokens := rest[0], rest[1:]
	pos, fl, err := parseCardFlags(tokens)
	if err != nil {
		fmt.Println(err)
		return
	}
	if r.cardPath == "" {
		fmt.Println(i18n.T(r.loc, "repl.card.none"))
		return
	}
	switch verb {
	case "add":
		if len(pos) != 1 {
			fmt.Println(i18n.T(r.loc, "repl.card.native.usage"))
			return
		}
		tier := 1
		if v, ok := fl["tier"]; ok {
			if tier, err = parseAgentTier(v); err != nil {
				fmt.Println(err)
				return
			}
		}
		ab := ledger.NativeAbility{
			ID:          pos[0],
			Command:     fl["command"],
			Args:        splitCSV(fl["args"]),
			Tier:        tier,
			Description: fl["description"],
		}
		if ab.Command == "" {
			fmt.Println(i18n.T(r.loc, "repl.card.native.usage"))
			return
		}
		if err := cardmut.NativeAdd(r.cardPath, ab); err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(i18n.Tf(r.loc, "repl.card.done", "id", ab.ID))
		r.reloadCardLive()
	case "remove", "rm":
		if len(pos) != 1 {
			fmt.Println(i18n.T(r.loc, "repl.card.native.usage"))
			return
		}
		if err := cardmut.NativeRemove(r.cardPath, pos[0]); err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(i18n.Tf(r.loc, "repl.card.done", "id", pos[0]))
		r.reloadCardLive()
	default:
		fmt.Println(i18n.T(r.loc, "repl.card.native.usage"))
	}
}

// cardAgent runs /card agent add|remove|set — the agent CLIs the router
// delegates to.
func (r *repl) cardAgent(rest []string) {
	if len(rest) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.card.agent.usage"))
		return
	}
	verb, tokens := rest[0], rest[1:]
	pos, fl, err := parseCardFlags(tokens)
	if err != nil {
		fmt.Println(err)
		return
	}
	if r.cardPath == "" {
		fmt.Println(i18n.T(r.loc, "repl.card.none"))
		return
	}
	switch verb {
	case "add":
		if len(pos) != 1 || fl["adapter"] == "" {
			fmt.Println(i18n.T(r.loc, "repl.card.agent.usage"))
			return
		}
		tier := 2 // fail-closed default, same as the loader's zero value
		if v, ok := fl["tier"]; ok {
			if tier, err = parseAgentTier(v); err != nil {
				fmt.Println(err)
				return
			}
		}
		ag := ledger.Agent{
			Adapter:      fl["adapter"],
			InstallCheck: fl["install-check"],
			Capabilities: splitCSV(fl["capabilities"]),
			BestAt:       splitCSV(fl["best-at"]),
			NotFor:       splitCSV(fl["not-for"]),
			CostTier:     fl["cost-tier"],
			Tier:         tier,
		}
		if err := cardmut.AgentAdd(r.cardPath, pos[0], ag); err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(i18n.Tf(r.loc, "repl.card.done", "id", pos[0]))
		r.reloadCardLive()
	case "remove", "rm":
		if len(pos) != 1 {
			fmt.Println(i18n.T(r.loc, "repl.card.agent.usage"))
			return
		}
		if err := cardmut.AgentRemove(r.cardPath, pos[0]); err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(i18n.Tf(r.loc, "repl.card.done", "id", pos[0]))
		r.reloadCardLive()
	case "set":
		if len(pos) < 2 {
			fmt.Println(i18n.T(r.loc, "repl.card.agent.usage"))
			return
		}
		upd, err := parseAgentUpdate(pos[1:])
		if err != nil {
			fmt.Println(err)
			return
		}
		if err := cardmut.AgentSet(r.cardPath, pos[0], upd); err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(i18n.Tf(r.loc, "repl.card.done", "id", pos[0]))
		r.reloadCardLive()
	default:
		fmt.Println(i18n.T(r.loc, "repl.card.agent.usage"))
	}
}

// cardManual runs /card manual add|remove — the human-performed abilities.
func (r *repl) cardManual(rest []string) {
	if len(rest) == 0 {
		fmt.Println(i18n.T(r.loc, "repl.card.manual.usage"))
		return
	}
	verb, tokens := rest[0], rest[1:]
	pos, fl, err := parseCardFlags(tokens)
	if err != nil {
		fmt.Println(err)
		return
	}
	if r.cardPath == "" {
		fmt.Println(i18n.T(r.loc, "repl.card.none"))
		return
	}
	switch verb {
	case "add":
		if len(pos) != 1 || fl["notify"] == "" {
			fmt.Println(i18n.T(r.loc, "repl.card.manual.usage"))
			return
		}
		ab := ledger.ManualAbility{ID: pos[0], Notify: fl["notify"]}
		if err := cardmut.ManualAdd(r.cardPath, ab); err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(i18n.Tf(r.loc, "repl.card.done", "id", ab.ID))
		r.reloadCardLive()
	case "remove", "rm":
		if len(pos) != 1 {
			fmt.Println(i18n.T(r.loc, "repl.card.manual.usage"))
			return
		}
		if err := cardmut.ManualRemove(r.cardPath, pos[0]); err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(i18n.Tf(r.loc, "repl.card.done", "id", pos[0]))
		r.reloadCardLive()
	default:
		fmt.Println(i18n.T(r.loc, "repl.card.manual.usage"))
	}
}

// reloadCardLive is the REPL's whole reason to have /card: the engine's
// scheduler is in-process, so the edit is applied by Engine.ReloadCard with
// no restart and no SIGHUP. Without an engine (no model configured) the
// fallback is the CLI's daemon-side flow, and the line says which path ran.
func (r *repl) reloadCardLive() {
	if r.engine == nil {
		fmt.Println(i18n.T(r.loc, "repl.card.noEngine"))
		notifyDaemonReload()
		return
	}
	if err := r.engine.ReloadCard(r.cardPath); err != nil {
		fmt.Println(i18n.Tf(r.loc, "repl.card.reloadFail", "err", err.Error()))
		return
	}
	fmt.Println(i18n.T(r.loc, "repl.card.live"))
}

// parseCardFlags splits a tokenized /card argument tail into positionals and
// --flag value pairs. Accepts "--key value", "--key=value" and the
// underscore spellings of the hyphenated flags, because the CLI accepts both
// and a user who learned one must not be told the other is wrong.
func parseCardFlags(tokens []string) ([]string, map[string]string, error) {
	var pos []string
	fl := make(map[string]string)
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if !strings.HasPrefix(t, "-") || t == "-" {
			pos = append(pos, t)
			continue
		}
		key := strings.TrimLeft(t, "-")
		if key == "" {
			continue
		}
		key = strings.ReplaceAll(key, "_", "-")
		if eq := strings.IndexByte(key, '='); eq >= 0 {
			fl[key[:eq]] = key[eq+1:]
			continue
		}
		if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
			fl[key] = tokens[i+1]
			i++
			continue
		}
		fl[key] = ""
	}
	return pos, fl, nil
}

// cmdNodesAdd implements /nodes add <host:port>: append the peer to
// config.yaml (generating network.shared_secret when missing), keep the
// in-memory config in step, then dial through the engine so the peer (and
// its capability hello) is part of this session immediately — the plan's
// "写 config 之外，引擎在跑就直接拨号，免重启".
func (r *repl) cmdNodesAdd(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		fmt.Println(i18n.T(r.loc, "repl.nodes.add.usage"))
		return
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		fmt.Println(i18n.Tf(r.loc, "cli.nodes.badaddr", "addr", addr))
		return
	}
	if r.cfg.Network.SharedSecret == "" {
		secret, err := generateSharedSecret()
		if err != nil {
			fmt.Println(i18n.Tf(r.loc, "repl.err", "err", err.Error()))
			return
		}
		r.cfg.Network.SharedSecret = secret
		fmt.Println(i18n.T(r.loc, "cli.nodes.secret.gen"))
	}
	if slices.Contains(r.cfg.Network.Peers, addr) {
		fmt.Println(i18n.Tf(r.loc, "cli.nodes.add.exists", "addr", addr))
		return
	}
	r.cfg.Network.Peers = append(r.cfg.Network.Peers, addr)
	if err := config.UpdateNetworkSection(configWritePath(r.configPath), config.NetworkConfig{
		ListenAddr:   r.cfg.Network.ListenAddr,
		SharedSecret: r.cfg.Network.SharedSecret,
		Peers:        r.cfg.Network.Peers,
	}); err != nil {
		fmt.Println(i18n.Tf(r.loc, "repl.err", "err", err.Error()))
		return
	}
	fmt.Println(i18n.Tf(r.loc, "cli.nodes.add.done", "addr", addr))

	// Live dial: async, because the dialer's timeout would otherwise freeze
	// the prompt on an offline peer — the same reasoning as startup dials.
	if r.engine != nil {
		go func() {
			if err := r.engine.DialPeer(context.Background(), addr); err != nil {
				fmt.Println(i18n.Tf(r.loc, "repl.nodes.dialFail", "addr", addr))
				return
			}
			fmt.Println(i18n.Tf(r.loc, "repl.nodes.dialed", "addr", addr))
		}()
		return
	}
	fmt.Println(i18n.T(r.loc, "repl.nodes.noEngine"))
	printJoinGuide(r.loc, r.cfg)
}

// cmdNodesDisconnect implements /nodes disconnect <addr> — the peer leaves
// the dial list; the ledger row (if any) stays until it goes stale and
// `nodes remove` clears it.
func (r *repl) cmdNodesDisconnect(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		fmt.Println(i18n.T(r.loc, "repl.nodes.add.usage"))
		return
	}
	remaining := make([]string, 0, len(r.cfg.Network.Peers))
	for _, p := range r.cfg.Network.Peers {
		if p != addr {
			remaining = append(remaining, p)
		}
	}
	if len(remaining) == len(r.cfg.Network.Peers) {
		fmt.Println(i18n.Tf(r.loc, "cli.nodes.disconnect.none", "addr", addr))
		return
	}
	r.cfg.Network.Peers = remaining
	if err := config.UpdateNetworkSection(configWritePath(r.configPath), config.NetworkConfig{
		Peers: remaining,
	}); err != nil {
		fmt.Println(i18n.Tf(r.loc, "repl.err", "err", err.Error()))
		return
	}
	fmt.Println(i18n.Tf(r.loc, "cli.nodes.disconnect.done", "addr", addr))
	fmt.Println(i18n.T(r.loc, "cli.nodes.restart"))
}

// cmdNodesInvite implements /nodes invite — the join guide, no config change.
func (r *repl) cmdNodesInvite() {
	if r.cfg.Network.SharedSecret == "" {
		secret, err := generateSharedSecret()
		if err != nil {
			fmt.Println(i18n.Tf(r.loc, "repl.err", "err", err.Error()))
			return
		}
		r.cfg.Network.SharedSecret = secret
		if err := config.UpdateNetworkSection(configWritePath(r.configPath), config.NetworkConfig{
			ListenAddr:   r.cfg.Network.ListenAddr,
			SharedSecret: secret,
		}); err != nil {
			fmt.Println(i18n.Tf(r.loc, "repl.err", "err", err.Error()))
			return
		}
		fmt.Println(i18n.T(r.loc, "cli.nodes.secret.gen"))
	}
	printJoinGuide(r.loc, r.cfg)
}
