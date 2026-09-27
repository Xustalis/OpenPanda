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
	"net"
	"os"
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
	case "set":
		r.cardSet(fields[1:])
	case "rescan", "scan", "refresh":
		r.cardRescan(fields[1:])
	default:
		r.outln(i18n.T(r.loc, "repl.card.usage"))
	}
}

// cardSet runs /card set <field>=<value>… — the scalar tuner fields of
// `panda card set` (device, resource_class, chip, capacity.*, resource_profile.*).
// List/map fields stay with the CLI editor verbs; ReloadCard applies live.
func (r *repl) cardSet(rest []string) {
	pos, _, err := parseCardFlags(rest)
	if err != nil {
		r.outln(err)
		return
	}
	if len(pos) == 0 {
		r.outln("usage: /card set <field>=<value> [<field>=<value>…]")
		r.outln("fields: device, resource_class, chip, capacity.cpu_cores, capacity.ram_gb,")
		r.outln("        capacity.max_concurrent_tasks, resource_profile.cpu,")
		r.outln("        resource_profile.ram_gb, resource_profile.gpu_vram_gb,")
		r.outln("        resource_profile.duration_hint")
		return
	}
	path := r.ensureCard()
	if path == "" {
		r.outln(i18n.T(r.loc, "repl.card.none"))
		return
	}
	card, err := ledger.LoadCard(path)
	if err != nil {
		r.outln(i18n.Tf(r.loc, "repl.card.loadFail", "err", err.Error()))
		return
	}
	for _, a := range pos {
		field, value, ok := strings.Cut(a, "=")
		if !ok {
			r.outln(i18n.Tf(r.loc, "repl.card.badAssign", "a", a))
			return
		}
		if err := setCardField(&card, strings.TrimSpace(field), strings.TrimSpace(value)); err != nil {
			r.outln(err)
			return
		}
	}
	if err := writeCard(path, card, true); err != nil {
		r.storeErr(err)
		return
	}
	r.outln(path + " updated")
	r.reloadCardLive()
}

// cardRescan runs /card rescan [--write] — re-probe the machine and diff the
// result against the card. Dry-run by default, exactly like the CLI verb;
// --write merges (keeping a .bak) and reloads the live card in-engine.
func (r *repl) cardRescan(rest []string) {
	write := false
	for _, f := range rest {
		if f == "--write" || f == "-write" || f == "-w" {
			write = true
		}
	}
	path := r.cardPathNow()
	if path == "" {
		path = ensureDefaultCardPath()
	}
	old, err := ledger.LoadCard(path)
	created := false
	if err != nil {
		if !os.IsNotExist(underlyingErr(err)) {
			r.outln(i18n.Tf(r.loc, "repl.card.loadFail", "err", err.Error()))
			return
		}
		created = true
	}
	scanned := detectCard()
	merged, diffs := mergeCard(old, scanned)
	if created {
		merged = scanned
	}
	if created {
		r.outf("no card at %s — the scan will create one\n", path)
	}
	if len(diffs) == 0 && !created {
		r.outln("card already matches this machine — nothing to change")
		return
	}
	for _, d := range diffs {
		r.outf("  %-34s %s → %s\n", d.Field, orDash(d.Old), d.New)
	}
	if !write {
		r.outln()
		r.outln("dry run — re-run with --write to apply")
		return
	}
	if err := writeCard(path, merged, true); err != nil {
		r.storeErr(err)
		return
	}
	r.setCard(path, true)
	r.outf("%s updated (%d change(s))\n", path, len(diffs))
	r.reloadCardLive()
}

func (r *repl) ensureCard() string {
	r.cardMu.Lock()
	path := r.cardPath
	fresh := false
	if path == "" {
		path = ensureDefaultCardPath()
		r.cardPath = path
		r.hasCard = path != ""
		fresh = true
	}
	r.cardMu.Unlock()
	// Only the first materialization triggers a reload — an existing card
	// path is already the one the engine loaded (or /card rescan reloaded).
	if fresh && path != "" {
		if eng := r.engine.Load(); eng != nil {
			_ = eng.ReloadCard(path)
		}
	}
	return path
}

// cardSummary prints the one-glance view of the card: what this machine is,
// what it can run, and where the file lives. Counts, not the full YAML — the
// full file is `panda card show` (or /card edit via the CLI); what a /card
// user needs mid-conversation is "did my edit land and what's on there now".
func (r *repl) cardSummary() {
	path := r.ensureCard()
	if path == "" {
		r.outln(i18n.T(r.loc, "repl.card.none"))
		return
	}
	card, err := ledger.LoadCard(path)
	if err != nil {
		r.outln(i18n.Tf(r.loc, "repl.card.loadFail", "err", err.Error()))
		return
	}
	r.outln(i18n.Tf(r.loc, "repl.card.head", "path", path))
	r.outf("  %s · %s · %s\n", orDash(card.Device), orDash(card.ResourceClass), orDash(card.Chip))
	if ids := nativeIDs(card); len(ids) > 0 {
		r.outf("  native (%d): %s\n", len(ids), strings.Join(ids, ", "))
	} else {
		r.outf("  native (0)\n")
	}
	if len(card.Agents) > 0 {
		names := make([]string, 0, len(card.Agents))
		for name := range card.Agents {
			names = append(names, name)
		}
		slices.Sort(names)
		r.outf("  agents (%d): %s\n", len(names), strings.Join(names, ", "))
	} else {
		r.outf("  agents (0)\n")
	}
	if len(card.Manual) > 0 {
		ids := make([]string, 0, len(card.Manual))
		for _, ab := range card.Manual {
			ids = append(ids, ab.ID)
		}
		r.outf("  manual (%d): %s\n", len(ids), strings.Join(ids, ", "))
	} else {
		r.outf("  manual (0)\n")
	}
	r.outf("  %s\n", i18n.T(r.loc, "repl.card.editHint"))
}

// cardNative runs /card native add|remove — one command ability at a time.
func (r *repl) cardNative(rest []string) {
	if len(rest) == 0 {
		r.outln(i18n.T(r.loc, "repl.card.native.usage"))
		return
	}
	verb, tokens := rest[0], rest[1:]
	pos, fl, err := parseCardFlags(tokens)
	if err != nil {
		r.outln(err)
		return
	}
	if r.ensureCard() == "" {
		r.outln(i18n.T(r.loc, "repl.card.none"))
		return
	}
	switch verb {
	case "add":
		if len(pos) != 1 {
			r.outln(i18n.T(r.loc, "repl.card.native.usage"))
			return
		}
		tier := 1
		if v, ok := fl["tier"]; ok {
			if tier, err = parseAgentTier(v); err != nil {
				r.outln(err)
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
			r.outln(i18n.T(r.loc, "repl.card.native.usage"))
			return
		}
		if err := cardmut.NativeAdd(r.cardPathNow(), ab); err != nil {
			r.outln(err)
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.card.done", "id", ab.ID))
		r.reloadCardLive()
	case "remove", "rm":
		if len(pos) != 1 {
			r.outln(i18n.T(r.loc, "repl.card.native.usage"))
			return
		}
		if err := cardmut.NativeRemove(r.cardPathNow(), pos[0]); err != nil {
			r.outln(err)
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.card.done", "id", pos[0]))
		r.reloadCardLive()
	default:
		r.outln(i18n.T(r.loc, "repl.card.native.usage"))
	}
}

// cardAgent runs /card agent add|remove|set — the agent CLIs the router
// delegates to.
func (r *repl) cardAgent(rest []string) {
	if len(rest) == 0 {
		r.outln(i18n.T(r.loc, "repl.card.agent.usage"))
		return
	}
	verb, tokens := rest[0], rest[1:]
	pos, fl, err := parseCardFlags(tokens)
	if err != nil {
		r.outln(err)
		return
	}
	if r.ensureCard() == "" {
		r.outln(i18n.T(r.loc, "repl.card.none"))
		return
	}
	switch verb {
	case "add":
		if len(pos) != 1 || fl["adapter"] == "" {
			r.outln(i18n.T(r.loc, "repl.card.agent.usage"))
			return
		}
		tier := 2 // fail-closed default, same as the loader's zero value
		if v, ok := fl["tier"]; ok {
			if tier, err = parseAgentTier(v); err != nil {
				r.outln(err)
				return
			}
		}
		ag := ledger.Agent{
			Adapter:      fl["adapter"],
			InstallCheck: fl["install-check"],
			Command:      fl["command"],
			Capabilities: splitCSV(fl["capabilities"]),
			BestAt:       splitCSV(fl["best-at"]),
			NotFor:       splitCSV(fl["not-for"]),
			CostTier:     fl["cost-tier"],
			Tier:         tier,
		}
		if err := cardmut.AgentAdd(r.cardPathNow(), pos[0], ag); err != nil {
			r.outln(err)
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.card.done", "id", pos[0]))
		r.reloadCardLive()
	case "remove", "rm":
		if len(pos) != 1 {
			r.outln(i18n.T(r.loc, "repl.card.agent.usage"))
			return
		}
		if err := cardmut.AgentRemove(r.cardPathNow(), pos[0]); err != nil {
			r.outln(err)
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.card.done", "id", pos[0]))
		r.reloadCardLive()
	case "set":
		if len(pos) < 2 {
			r.outln(i18n.T(r.loc, "repl.card.agent.usage"))
			return
		}
		upd, err := parseAgentUpdate(pos[1:])
		if err != nil {
			r.outln(err)
			return
		}
		if err := cardmut.AgentSet(r.cardPathNow(), pos[0], upd); err != nil {
			r.outln(err)
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.card.done", "id", pos[0]))
		r.reloadCardLive()
	default:
		r.outln(i18n.T(r.loc, "repl.card.agent.usage"))
	}
}

// cardManual runs /card manual add|remove — the human-performed abilities.
func (r *repl) cardManual(rest []string) {
	if len(rest) == 0 {
		r.outln(i18n.T(r.loc, "repl.card.manual.usage"))
		return
	}
	verb, tokens := rest[0], rest[1:]
	pos, fl, err := parseCardFlags(tokens)
	if err != nil {
		r.outln(err)
		return
	}
	if r.ensureCard() == "" {
		r.outln(i18n.T(r.loc, "repl.card.none"))
		return
	}
	switch verb {
	case "add":
		if len(pos) != 1 || fl["notify"] == "" {
			r.outln(i18n.T(r.loc, "repl.card.manual.usage"))
			return
		}
		ab := ledger.ManualAbility{ID: pos[0], Notify: fl["notify"]}
		if err := cardmut.ManualAdd(r.cardPathNow(), ab); err != nil {
			r.outln(err)
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.card.done", "id", ab.ID))
		r.reloadCardLive()
	case "remove", "rm":
		if len(pos) != 1 {
			r.outln(i18n.T(r.loc, "repl.card.manual.usage"))
			return
		}
		if err := cardmut.ManualRemove(r.cardPathNow(), pos[0]); err != nil {
			r.outln(err)
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.card.done", "id", pos[0]))
		r.reloadCardLive()
	default:
		r.outln(i18n.T(r.loc, "repl.card.manual.usage"))
	}
}

// reloadCardLive is the REPL's whole reason to have /card: the engine's
// scheduler is in-process, so the edit is applied by Engine.ReloadCard with
// no restart and no SIGHUP. Without an engine (no model configured) the
// fallback is the CLI's daemon-side flow, and the line says which path ran.
func (r *repl) reloadCardLive() {
	if r.engine.Load() == nil {
		r.outln(i18n.T(r.loc, "repl.card.noEngine"))
		notifyDaemonReloadTo(r.commandOutput())
		return
	}
	cardPath, _ := r.cardInfo()
	if err := r.engine.Load().ReloadCard(cardPath); err != nil {
		r.outln(i18n.Tf(r.loc, "repl.card.reloadFail", "err", err.Error()))
		return
	}
	r.outln(i18n.T(r.loc, "repl.card.live"))
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
		r.outln(i18n.T(r.loc, "repl.nodes.add.usage"))
		return
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		r.outln(i18n.Tf(r.loc, "cli.nodes.badaddr", "addr", addr))
		return
	}
	var haveSecret bool
	r.readConfig(func(c *config.Config) { haveSecret = c.Network.SharedSecret != "" })
	if !haveSecret {
		secret, err := generateSharedSecret()
		if err != nil {
			r.outln(i18n.Tf(r.loc, "repl.err", "err", err.Error()))
			return
		}
		r.mutateConfig(func(c *config.Config) { c.Network.SharedSecret = secret })
		r.outln(i18n.T(r.loc, "cli.nodes.secret.gen"))
	}
	// Exists-check, append, and persist ride one critical section — the
	// exists test outside the lock was a check-then-act gap, and
	// UpdateNetworkSection rewrites the whole section.
	var exists bool
	err := r.mutateConfigErr(func(c *config.Config) error {
		if exists = slices.Contains(c.Network.Peers, addr); exists {
			return nil
		}
		c.Network.Peers = append(c.Network.Peers, addr)
		return config.UpdateNetworkSection(configWritePath(r.configPath), config.NetworkConfig{
			ListenAddr:   c.Network.ListenAddr,
			SharedSecret: c.Network.SharedSecret,
			Peers:        slices.Clone(c.Network.Peers),
		})
	})
	if err != nil {
		r.outln(i18n.Tf(r.loc, "repl.err", "err", err.Error()))
		return
	}
	if exists {
		r.outln(i18n.Tf(r.loc, "cli.nodes.add.exists", "addr", addr))
		return
	}
	r.outln(i18n.Tf(r.loc, "cli.nodes.add.done", "addr", addr))

	// Live dial through the in-process engine. Synchronous now: an async
	// goroutine outlived dispatchWithIO, so its result writes hit the command
	// writer after execDoneMsg and were silently dropped by the TUI — a
	// dial that reported nothing at all. The command context bounds it (Esc
	// cancels), matching every other network verb here.
	if r.engine.Load() != nil {
		if err := r.engine.Load().DialPeer(r.commandContext(), addr); err != nil {
			r.outln(i18n.Tf(r.loc, "repl.nodes.dialFail", "addr", addr))
			return
		}
		r.outln(i18n.Tf(r.loc, "repl.nodes.dialed", "addr", addr))
		return
	}
	r.outln(i18n.T(r.loc, "repl.nodes.noEngine"))
	r.readConfig(func(c *config.Config) { printJoinGuideTo(r.commandOutput(), r.loc, c) })
}

// cmdNodesDisconnect implements /nodes disconnect <addr> — the peer leaves
// the dial list; the ledger row (if any) stays until it goes stale and
// `nodes remove` clears it.
func (r *repl) cmdNodesDisconnect(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		r.outln(i18n.T(r.loc, "repl.nodes.add.usage"))
		return
	}
	var remaining []string
	var found bool
	err := r.mutateConfigErr(func(c *config.Config) error {
		remaining = make([]string, 0, len(c.Network.Peers))
		for _, p := range c.Network.Peers {
			if p != addr {
				remaining = append(remaining, p)
			}
		}
		found = len(remaining) != len(c.Network.Peers)
		if !found {
			return nil
		}
		c.Network.Peers = remaining
		return config.UpdateNetworkSection(configWritePath(r.configPath), config.NetworkConfig{
			Peers: remaining,
		})
	})
	if err != nil {
		r.outln(i18n.Tf(r.loc, "repl.err", "err", err.Error()))
		return
	}
	if !found {
		r.outln(i18n.Tf(r.loc, "cli.nodes.disconnect.none", "addr", addr))
		return
	}
	r.outln(i18n.Tf(r.loc, "cli.nodes.disconnect.done", "addr", addr))
	r.outln(i18n.T(r.loc, "cli.nodes.restart"))
}

// cmdNodesInvite implements /nodes invite — the join guide, no config change.
func (r *repl) cmdNodesInvite() {
	var haveSecret bool
	r.readConfig(func(c *config.Config) { haveSecret = c.Network.SharedSecret != "" })
	if !haveSecret {
		secret, err := generateSharedSecret()
		if err != nil {
			r.outln(i18n.Tf(r.loc, "repl.err", "err", err.Error()))
			return
		}
		// Mutate + persist in one section: a peer sharing cfgMu could
		// otherwise interleave a whole-section rewrite between them.
		if err := r.mutateConfigErr(func(c *config.Config) error {
			c.Network.SharedSecret = secret
			return config.UpdateNetworkSection(configWritePath(r.configPath), config.NetworkConfig{
				ListenAddr:   c.Network.ListenAddr,
				SharedSecret: secret,
				Peers:        slices.Clone(c.Network.Peers),
			})
		}); err != nil {
			r.outln(i18n.Tf(r.loc, "repl.err", "err", err.Error()))
			return
		}
		r.outln(i18n.T(r.loc, "cli.nodes.secret.gen"))
	}
	r.readConfig(func(c *config.Config) { printJoinGuideTo(r.commandOutput(), r.loc, c) })
}
