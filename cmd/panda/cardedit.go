package main

// `panda card native|agent|manual add|remove|set …` — the structured card
// edits. Where `panda card edit` opens an editor and `panda card set` touches
// scalar fields, these verbs add and remove the list/map entries a card is
// actually made of: one native command, one agent CLI, one manual ability at
// a time, through internal/cardmut so comments in the untouched sections
// survive and nothing invalid ever reaches disk.
//
//	panda card native add <id> --command <cmd> [--args a,b] [--tier 1|2] [--description …]
//	panda card native remove <id>
//	panda card agent add <name> --adapter <script> [--install-check …] [--capabilities a,b]
//	                                                     [--best-at a,b] [--not-for a,b]
//	                                                     [--cost-tier …] [--tier 1|2]
//	panda card agent remove <name>
//	panda card agent set <name> <field>=<value>…
//	panda card manual add <id> --notify <contact>
//	panda card manual remove <id>
//
// Every successful write ends with notifyDaemonReload: a running daemon is
// SIGHUPed into hot-reloading the card, and when none is running the user is
// told what to do instead of discovering a stale card at the next delegation.

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/cardmut"
	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// cardMutValueFlags lists every value-carrying flag the structured edits
// accept, keyed by bare name so -flag and --flag spellings both match during
// the verb scan (same trick as cardSubcommand's cardValueFlags).
var cardMutValueFlags = map[string]bool{
	"command": true, "args": true, "tier": true, "description": true,
	"notify": true, "adapter": true, "install-check": true, "install_check": true,
	"capabilities": true, "best-at": true, "best_at": true,
	"not-for": true, "not_for": true, "cost-tier": true, "cost_tier": true,
}

// cardMutDashFlags is cardMutValueFlags plus the global --config/--card, keyed
// by the exact tokens reorderFlags sees (both dash spellings registered).
var cardMutDashFlags = func() map[string]bool {
	m := map[string]bool{"--config": true, "--card": true, "-config": true, "-card": true}
	for name := range cardMutValueFlags {
		m["--"+name] = true
		m["-"+name] = true
	}
	return m
}()

// cardVerb splits `card native add id --command x` into the level-2 verb
// ("add") and the rest of argv with flags hoisted ahead of positionals — the
// same contract cardSubcommand provides one level up.
func cardVerb(args []string) (string, []string) {
	verb := ""
	verbIdx := -1
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && cardMutValueFlags[strings.TrimLeft(a, "-")] {
				i++ // this flag's value is not the verb
			}
			continue
		}
		verb, verbIdx = a, i
		break
	}
	rest := args
	if verbIdx >= 0 {
		rest = append(append([]string{}, args[:verbIdx]...), args[verbIdx+1:]...)
	}
	return verb, reorderFlags(rest, cardMutDashFlags)
}

// runCardNative implements `panda card native …`.
func runCardNative(args []string) {
	verb, rest := cardVerb(args)
	fs := flag.NewFlagSet("card native "+verb, flag.ExitOnError)
	cardFlag := fs.String("card", "", "path to capabilities.yaml (default: discovered)")
	command := fs.String("command", "", "the command to run (required)")
	argList := fs.String("args", "", "comma-separated arguments passed to the command")
	tier := fs.Int("tier", 1, "1=reversible (default) | 2=irreversible, needs approval")
	description := fs.String("description", "", "one-line description of what the command does")
	fs.Parse(rest)
	positional := fs.Args()
	path := cardTargetPath(*cardFlag)

	switch verb {
	case "add":
		if len(positional) != 1 || *command == "" {
			fmt.Fprintln(os.Stderr, "usage: panda card native add <id> --command <cmd> [--args a,b] [--tier 1|2] [--description …]")
			os.Exit(2)
		}
		if *tier != 1 && *tier != 2 {
			fmt.Fprintf(os.Stderr, "panda: --tier must be 1 or 2, got %d\n", *tier)
			os.Exit(2)
		}
		ab := ledger.NativeAbility{
			ID:          positional[0],
			Command:     *command,
			Args:        splitCSV(*argList),
			Tier:        *tier,
			Description: *description,
		}
		if err := cardmut.NativeAdd(path, ab); err != nil {
			fatal("add native ability", err)
		}
		cardEditDone(path, "native", ab.ID, "added")
	case "remove", "rm":
		if len(positional) != 1 {
			fmt.Fprintln(os.Stderr, "usage: panda card native remove <id>")
			os.Exit(2)
		}
		if err := cardmut.NativeRemove(path, positional[0]); err != nil {
			fatal("remove native ability", err)
		}
		cardEditDone(path, "native", positional[0], "removed")
	default:
		fmt.Fprintln(os.Stderr, "usage: panda card native add|remove …")
		os.Exit(2)
	}
}

// runCardAgent implements `panda card agent …`. The add default tier is 2
// (fail-closed — an agent CLI can run arbitrary shell through the model),
// matching the zero-value semantics the loader documents.
func runCardAgent(args []string) {
	verb, rest := cardVerb(args)
	fs := flag.NewFlagSet("card agent "+verb, flag.ExitOnError)
	cardFlag := fs.String("card", "", "path to capabilities.yaml (default: discovered)")
	adapter := fs.String("adapter", "", "adapter script in adapters/ (required for add)")
	installCheck := fs.String("install-check", "", "command that proves the CLI is installed (e.g. 'codex --version')")
	capabilities := fs.String("capabilities", "", "comma-separated capability tags (e.g. shell,files,code)")
	bestAt := fs.String("best-at", "", "comma-separated descriptions of what it is best at")
	notFor := fs.String("not-for", "", "comma-separated things it should not be routed")
	costTier := fs.String("cost-tier", "", "cost tier hint (e.g. low|medium|high)")
	tier := fs.Int("tier", 2, "1=reversible | 2=irreversible, needs approval (default)")
	fs.Parse(rest)
	positional := fs.Args()
	path := cardTargetPath(*cardFlag)

	switch verb {
	case "add":
		if len(positional) != 1 || *adapter == "" {
			fmt.Fprintln(os.Stderr, "usage: panda card agent add <name> --adapter <script> [--install-check …] [--capabilities a,b]")
			fmt.Fprintln(os.Stderr, "                                [--best-at a,b] [--not-for a,b] [--cost-tier …] [--tier 1|2]")
			os.Exit(2)
		}
		if *tier != 1 && *tier != 2 {
			fmt.Fprintf(os.Stderr, "panda: --tier must be 1 or 2, got %d\n", *tier)
			os.Exit(2)
		}
		ag := ledger.Agent{
			Adapter:      *adapter,
			InstallCheck: *installCheck,
			Capabilities: splitCSV(*capabilities),
			BestAt:       splitCSV(*bestAt),
			NotFor:       splitCSV(*notFor),
			CostTier:     *costTier,
			Tier:         *tier,
		}
		if err := cardmut.AgentAdd(path, positional[0], ag); err != nil {
			fatal("add agent", err)
		}
		cardEditDone(path, "agent", positional[0], "added")
	case "remove", "rm":
		if len(positional) != 1 {
			fmt.Fprintln(os.Stderr, "usage: panda card agent remove <name>")
			os.Exit(2)
		}
		if err := cardmut.AgentRemove(path, positional[0]); err != nil {
			fatal("remove agent", err)
		}
		cardEditDone(path, "agent", positional[0], "removed")
	case "set":
		if len(positional) < 2 {
			fmt.Fprintln(os.Stderr, "usage: panda card agent set <name> <field>=<value> …")
			fmt.Fprintln(os.Stderr, "fields: adapter, install_check, capabilities, best_at, not_for, cost_tier, tier")
			os.Exit(2)
		}
		name := positional[0]
		upd, err := parseAgentUpdate(positional[1:])
		if err != nil {
			fatal("parse assignment", err)
		}
		if err := cardmut.AgentSet(path, name, upd); err != nil {
			fatal("set agent fields", err)
		}
		cardEditDone(path, "agent", name, "updated")
	default:
		fmt.Fprintln(os.Stderr, "usage: panda card agent add|remove|set …")
		os.Exit(2)
	}
}

// runCardManual implements `panda card manual …` — the human-performed
// abilities, which carry a notify contact rather than a command.
func runCardManual(args []string) {
	verb, rest := cardVerb(args)
	fs := flag.NewFlagSet("card manual "+verb, flag.ExitOnError)
	cardFlag := fs.String("card", "", "path to capabilities.yaml (default: discovered)")
	notify := fs.String("notify", "", "how to reach the human (required for add)")
	fs.Parse(rest)
	positional := fs.Args()
	path := cardTargetPath(*cardFlag)

	switch verb {
	case "add":
		if len(positional) != 1 || *notify == "" {
			fmt.Fprintln(os.Stderr, "usage: panda card manual add <id> --notify <contact>")
			os.Exit(2)
		}
		ab := ledger.ManualAbility{ID: positional[0], Notify: *notify}
		if err := cardmut.ManualAdd(path, ab); err != nil {
			fatal("add manual ability", err)
		}
		cardEditDone(path, "manual", ab.ID, "added")
	case "remove", "rm":
		if len(positional) != 1 {
			fmt.Fprintln(os.Stderr, "usage: panda card manual remove <id>")
			os.Exit(2)
		}
		if err := cardmut.ManualRemove(path, positional[0]); err != nil {
			fatal("remove manual ability", err)
		}
		cardEditDone(path, "manual", positional[0], "removed")
	default:
		fmt.Fprintln(os.Stderr, "usage: panda card manual add|remove …")
		os.Exit(2)
	}
}

// parseAgentUpdate turns `tier=2 capabilities=code,shell` assignments into a
// cardmut.AgentUpdate, applying only the named fields. Comma values build
// lists; tier must be 1 or 2.
func parseAgentUpdate(assignments []string) (cardmut.AgentUpdate, error) {
	var upd cardmut.AgentUpdate
	for _, a := range assignments {
		field, value, ok := strings.Cut(a, "=")
		if !ok {
			return upd, fmt.Errorf("%q is not <field>=<value>", a)
		}
		field = strings.TrimSpace(field)
		value = strings.TrimSpace(value)
		switch field {
		case "adapter":
			v := value
			upd.Adapter = &v
		case "install_check":
			v := value
			upd.InstallCheck = &v
		case "capabilities":
			v := splitCSV(value)
			upd.Capabilities = &v
		case "best_at":
			v := splitCSV(value)
			upd.BestAt = &v
		case "not_for":
			v := splitCSV(value)
			upd.NotFor = &v
		case "cost_tier":
			v := value
			upd.CostTier = &v
		case "tier":
			n, err := parseAgentTier(value)
			if err != nil {
				return upd, err
			}
			upd.Tier = &n
		default:
			return upd, fmt.Errorf("unknown agent field %q (adapter, install_check, capabilities, best_at, not_for, cost_tier, tier)", field)
		}
	}
	return upd, nil
}

// parseAgentTier validates a tier= assignment. A typo here would silently
// downgrade or upgrade an agent's approval class, so it is checked, not
// defaulted.
func parseAgentTier(value string) (int, error) {
	switch value {
	case "1":
		return 1, nil
	case "2":
		return 2, nil
	}
	return 0, fmt.Errorf("tier %q invalid (1 or 2)", value)
}

// splitCSV splits a comma-separated flag value into a list; empty input means
// "no value given" and yields nil, which the cardmut setters treat as "leave
// the field out" rather than "empty the field".
func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// cardEditDone is the shared success line for a structured card write: the
// file, the entry, and the reload outcome — the daemon is SIGHUPed when one
// is running so the edit is live without a restart.
func cardEditDone(path, kind, id, action string) {
	if jsonOutput {
		emitJSON(map[string]string{"path": path, "kind": kind, "id": id, "action": action})
		return
	}
	fmt.Printf("%s %s %q %s\n", path, kind, id, action)
	notifyDaemonReload()
}
