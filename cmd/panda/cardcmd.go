package main

// `panda card` — read, rescan and edit this node's capability card without
// hand-writing YAML.
//
//	panda card show                 # the effective card + where it came from
//	panda card rescan               # re-probe hardware + installed agents, show the diff
//	panda card rescan --write       # …and apply it (a .bak is kept)
//	panda card edit                 # open $EDITOR, validate before installing
//	panda card set <field>=<value>  # one field, no editor
//
// It exists because the card is the one file that decides what work this machine
// is offered, and until now it could only be produced once (`panda detect`, which
// overwrites) or maintained by hand. A machine that gained a GPU or a second
// agent CLI therefore kept advertising the hardware it had on the day it was set
// up — and in this system a stale card is not a cosmetic problem, it is the
// router sending the training stage to the wrong node.
//
// Two rules run through all of it: nothing is written until the user says so
// (rescan prints a diff and stops without --write), and nothing invalid is ever
// installed (every write path re-parses through ledger.LoadCard first, so a
// mistyped duration_hint fails here rather than at daemon start).

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
	"gopkg.in/yaml.v3"
)

func runCard(args []string) {
	sub, rest := cardSubcommand(args)
	switch sub {
	case "", "show":
		runCardShow(rest)
	case "rescan", "scan", "refresh":
		runCardRescan(rest)
	case "edit":
		runCardEdit(rest)
	case "set":
		runCardSet(rest)
	case "path":
		fmt.Println(orDash(cardTargetPath("")))
	default:
		fmt.Fprintf(os.Stderr, "panda: unknown card subcommand %q\n", sub)
		fmt.Fprintln(os.Stderr, "usage: panda card [show|rescan|edit|set|path]")
		os.Exit(2)
	}
}

// cardSubcommand splits `card <sub> [flags…]` into the subcommand and the rest
// of argv, then reorders the rest so the std flag package sees the flags first.
//
// The scan has to know which flags carry a value: with a naive "first word that
// does not start with -" rule, `panda card rescan --card ./capabilities.yaml`
// picks the *path* as the subcommand, because reordering hoists the flag pair in
// front of the verb.
func cardSubcommand(args []string) (string, []string) {
	sub := ""
	subIdx := -1
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && cardValueFlags[strings.TrimLeft(a, "-")] {
				i++ // this flag's value is not the subcommand
			}
			continue
		}
		sub, subIdx = a, i
		break
	}
	rest := args
	if subIdx >= 0 {
		rest = append(append([]string{}, args[:subIdx]...), args[subIdx+1:]...)
	}
	return sub, reorderFlags(rest, commonValueFlags)
}

// cardValueFlags are the value-carrying flags `panda card` accepts, keyed by
// their bare name so both -card and --card match.
var cardValueFlags = map[string]bool{"card": true, "config": true}

// cardTargetPath resolves which file `panda card` reads and writes: --card when
// given, else the discovered card, else the path `panda init` would have written
// (next to the resolved config) so `rescan --write` can create the first card on
// a node that has none.
func cardTargetPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p := defaultCardPath(); p != "" {
		return p
	}
	if cfgPath := config.ResolvePath(""); cfgPath != "" {
		return filepath.Join(filepath.Dir(cfgPath), "capabilities.yaml")
	}
	return "capabilities.yaml"
}

// runCardShow prints the effective card and the path it was loaded from. The
// path matters as much as the content: every other subcommand and the daemon
// discover the card the same way, and "which file am I actually editing" is the
// first question a multi-node setup raises.
func runCardShow(args []string) {
	fs := flag.NewFlagSet("card show", flag.ExitOnError)
	cardFlag := fs.String("card", "", "path to capabilities.yaml (default: discovered)")
	fs.Parse(args)

	path := cardTargetPath(*cardFlag)
	card, err := ledger.LoadCard(path)
	if err != nil {
		if jsonOutput {
			emitJSON(map[string]any{"path": path, "exists": false, "error": err.Error()})
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "panda: %v\n", err)
		fmt.Fprintln(os.Stderr, "hint: run `panda card rescan --write` to create one from this machine's hardware")
		os.Exit(1)
	}
	// The daemon prunes native abilities whose command is missing here, so show
	// them too — otherwise the card a user reads and the card peers are offered
	// disagree, which is the confusing half of a card copied between machines.
	shown := card
	unavailable := shown.PruneUnavailableNative()

	if jsonOutput {
		emitJSON(map[string]any{
			"path":               path,
			"exists":             true,
			"card":               card,
			"unavailable_native": unavailable,
			"effective_native":   nativeIDs(shown),
		})
		return
	}
	data, err := yaml.Marshal(card)
	if err != nil {
		fatal("render card", err)
	}
	fmt.Println("capability card  " + path)
	fmt.Println()
	fmt.Print(string(data))
	if len(unavailable) > 0 {
		fmt.Println()
		fmt.Println("warning: native abilities not runnable on this host (dropped at load): " +
			strings.Join(unavailable, ", "))
	}
}

// nativeIDs lists a card's native ability ids.
func nativeIDs(c ledger.Card) []string {
	out := make([]string, 0, len(c.Native))
	for _, ab := range c.Native {
		out = append(out, ab.ID)
	}
	return out
}

// runCardRescan re-probes the machine and merges the result into the existing
// card. Without --write it is a dry run: the diff is printed and nothing on disk
// changes, because this command's whole job is to update the numbers the router
// trusts and the user has to be able to look at them first.
func runCardRescan(args []string) {
	fs := flag.NewFlagSet("card rescan", flag.ExitOnError)
	cardFlag := fs.String("card", "", "path to capabilities.yaml (default: discovered)")
	write := fs.Bool("write", false, "apply the merge (a .bak of the old card is kept)")
	fs.Parse(args)

	path := cardTargetPath(*cardFlag)
	old, err := ledger.LoadCard(path)
	created := false
	if err != nil {
		if !os.IsNotExist(underlyingErr(err)) {
			// A card that exists but does not parse must not be silently
			// replaced by a scan — that is the one case where "fix it for you"
			// would destroy hand-written content.
			fmt.Fprintf(os.Stderr, "panda: %v\n", err)
			os.Exit(1)
		}
		created = true
	}
	scanned := detectCard()
	merged, diffs := mergeCard(old, scanned)
	if created {
		merged = scanned
	}

	if jsonOutput {
		emitJSON(map[string]any{
			"path": path, "created": created, "written": *write,
			"diff": diffs, "card": merged,
		})
		if !*write {
			return
		}
	}
	if !jsonOutput {
		if created {
			fmt.Printf("no card at %s — the scan will create one\n", path)
		}
		if len(diffs) == 0 && !created {
			fmt.Println("card already matches this machine — nothing to change")
			return
		}
		for _, d := range diffs {
			fmt.Printf("  %-34s %s → %s\n", d.Field, orDash(d.Old), d.New)
		}
		if !*write {
			fmt.Println()
			fmt.Println("dry run — re-run with --write to apply")
			return
		}
	}
	if err := writeCard(path, merged, true); err != nil {
		fatal("write card", err)
	}
	if !jsonOutput {
		fmt.Printf("%s updated (%d change(s))\n", path, len(diffs))
		fmt.Println("restart the daemon for the new card to be advertised to peers")
	}
}

// underlyingErr unwraps one level of fmt.Errorf %w so os.IsNotExist can see the
// syscall error LoadCard wrapped.
func underlyingErr(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		if inner := u.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}

// runCardEdit opens the card in the user's editor and installs it only if it
// still parses. The temp-file round trip is deliberate: editing the live card in
// place would leave a half-saved file that the daemon may read at any moment.
func runCardEdit(args []string) {
	fs := flag.NewFlagSet("card edit", flag.ExitOnError)
	cardFlag := fs.String("card", "", "path to capabilities.yaml (default: discovered)")
	fs.Parse(args)

	path := cardTargetPath(*cardFlag)
	original, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fatal("read card", err)
		}
		// Nothing to edit yet: hand the user a scanned draft rather than an
		// empty buffer, which is the difference between filling in a form and
		// remembering a schema.
		draft, mErr := yaml.Marshal(detectCard())
		if mErr != nil {
			fatal("render card draft", mErr)
		}
		original = append([]byte(cardHeader()), draft...)
	}

	tmp, err := os.CreateTemp("", "panda-card-*.yaml")
	if err != nil {
		fatal("create temp file", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(original); err != nil {
		tmp.Close()
		fatal("write temp file", err)
	}
	tmp.Close()

	if err := openEditor(tmpPath); err != nil {
		fatal("run editor", err)
	}
	edited, err := os.ReadFile(tmpPath)
	if err != nil {
		fatal("read edited card", err)
	}
	if string(edited) == string(original) {
		fmt.Println("no changes")
		return
	}
	// Validate through the same loader the daemon uses, so an invalid card can
	// never reach disk and take the node down at its next start.
	if _, err := ledger.LoadCard(tmpPath); err != nil {
		fmt.Fprintf(os.Stderr, "panda: edited card is invalid, nothing was written: %v\n", err)
		os.Exit(1)
	}
	if err := backupCard(path); err != nil {
		fatal("back up card", err)
	}
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		fatal("write card", err)
	}
	fmt.Printf("%s saved\n", path)
	fmt.Println("restart the daemon for the new card to be advertised to peers")
}

// openEditor runs the user's editor on path, wired to the real terminal.
func openEditor(path string) error {
	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		if runtime.GOOS == "windows" {
			editor = "notepad"
		} else {
			editor = "vi"
		}
	}
	// $EDITOR legitimately carries arguments ("code --wait", "emacs -nw"), so
	// it is split rather than treated as a bare binary name.
	fields := strings.Fields(editor)
	cmd := exec.Command(fields[0], append(fields[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// runCardSet edits one field without an editor — the form that works over ssh
// on a headless node and inside a provisioning script.
func runCardSet(args []string) {
	fs := flag.NewFlagSet("card set", flag.ExitOnError)
	cardFlag := fs.String("card", "", "path to capabilities.yaml (default: discovered)")
	fs.Parse(args)

	assignments := fs.Args()
	if len(assignments) == 0 {
		fmt.Fprintln(os.Stderr, "usage: panda card set <field>=<value> [<field>=<value>…]")
		fmt.Fprintln(os.Stderr, "fields: device, resource_class, chip, capacity.cpu_cores, capacity.ram_gb,")
		fmt.Fprintln(os.Stderr, "        capacity.max_concurrent_tasks, resource_profile.cpu,")
		fmt.Fprintln(os.Stderr, "        resource_profile.ram_gb, resource_profile.gpu_vram_gb,")
		fmt.Fprintln(os.Stderr, "        resource_profile.duration_hint")
		os.Exit(2)
	}
	path := cardTargetPath(*cardFlag)
	card, err := ledger.LoadCard(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "panda: %v\n", err)
		os.Exit(1)
	}
	for _, a := range assignments {
		field, value, ok := strings.Cut(a, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "panda: %q is not <field>=<value>\n", a)
			os.Exit(2)
		}
		if err := setCardField(&card, strings.TrimSpace(field), strings.TrimSpace(value)); err != nil {
			fmt.Fprintf(os.Stderr, "panda: %v\n", err)
			os.Exit(2)
		}
	}
	if err := writeCard(path, card, true); err != nil {
		fatal("write card", err)
	}
	if jsonOutput {
		emitJSON(map[string]any{"path": path, "card": card})
		return
	}
	fmt.Printf("%s updated\n", path)
	fmt.Println("restart the daemon for the new card to be advertised to peers")
}

// setCardField applies one <field>=<value> assignment. Only the scalar fields a
// user realistically tunes are exposed; native/agents/manual are lists and maps
// that belong in `panda card edit`, where a real editor can express them.
func setCardField(c *ledger.Card, field, value string) error {
	intVal := func() (int, error) {
		n, err := strconv.Atoi(value)
		if err != nil {
			return 0, fmt.Errorf("%s: %q is not a number", field, value)
		}
		return n, nil
	}
	switch field {
	case "device":
		if value == "" {
			return fmt.Errorf("device must not be empty")
		}
		c.Device = value
	case "resource_class":
		switch value {
		case "Micro", "Standard", "Full":
			c.ResourceClass = value
		default:
			return fmt.Errorf("resource_class %q invalid (Micro|Standard|Full)", value)
		}
	case "chip":
		c.Chip = value
	case "capacity.cpu_cores":
		n, err := intVal()
		if err != nil {
			return err
		}
		c.Capacity.CPUCores = n
	case "capacity.ram_gb":
		n, err := intVal()
		if err != nil {
			return err
		}
		c.Capacity.RAMGB = n
	case "capacity.max_concurrent_tasks":
		n, err := intVal()
		if err != nil {
			return err
		}
		if n < 1 {
			return fmt.Errorf("capacity.max_concurrent_tasks must be at least 1")
		}
		c.Capacity.MaxConcurrent = n
	case "resource_profile.cpu":
		n, err := intVal()
		if err != nil {
			return err
		}
		c.ResourceProfile.CPU = n
	case "resource_profile.ram_gb":
		n, err := intVal()
		if err != nil {
			return err
		}
		c.ResourceProfile.RAMGB = n
	case "resource_profile.gpu_vram_gb":
		n, err := intVal()
		if err != nil {
			return err
		}
		c.ResourceProfile.GPUVRAMGB = n
	case "resource_profile.duration_hint":
		c.ResourceProfile.DurationHint = value
	default:
		return fmt.Errorf("unknown field %q (see `panda card set` with no arguments)", field)
	}
	// The same validation the loader applies, applied before the write: `panda
	// card set` must not be a way to produce a card the daemon then refuses.
	if err := validateCardForWrite(*c); err != nil {
		return err
	}
	return nil
}

// validateCardForWrite round-trips a card through the loader's own checks
// without touching the target file.
func validateCardForWrite(c ledger.Card) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "panda-card-check-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	_, err = ledger.LoadCard(tmp.Name())
	return err
}

// writeCard renders a card to path, optionally keeping a .bak of what was there.
// The write goes through a temp file in the same directory and a rename, so a
// daemon reading the card concurrently sees either the old file or the new one,
// never a truncated one.
func writeCard(path string, card ledger.Card, backup bool) error {
	if err := validateCardForWrite(card); err != nil {
		return err
	}
	data, err := yaml.Marshal(card)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if backup {
		if err := backupCard(path); err != nil {
			return err
		}
	}
	body := append([]byte(cardHeader()), data...)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".capabilities-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// backupCard copies the current card to <path>.bak. A rescan overwrites the
// numbers a user may have tuned by hand, so there has to be a way back that
// does not involve remembering what they were.
func backupCard(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to back up
		}
		return err
	}
	return os.WriteFile(path+".bak", data, 0o644)
}

// cardHeader is the provenance comment written above a generated card. yaml.
// Marshal drops comments, so this is the only place a reader learns the file was
// machine-written and how to regenerate it.
func cardHeader() string {
	return fmt.Sprintf("# capabilities.yaml — written by `panda card` %s on %s\n"+
		"# hardware fields are re-probed by `panda card rescan`; everything else is yours.\n\n",
		versionpkg.Version, time.Now().Format("2006-01-02 15:04"))
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
