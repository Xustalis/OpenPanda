package main

// Command init bootstraps a new node in a single question. Hardware
// detection (detectCard) fills in the node name (hostname), resource class,
// and kind — plus a VM identity when the host probes as a guest — so the
// only prompt left is whether to configure a model now (Enter = skip; the
// web settings page can do it later). Two flags drop even that question:
//
//	panda init                    # one question, Enter-first defaults
//	panda init --defaults         # zero prompts; model section baked in from
//	                              # OPENPANDA_MODEL_API_KEY / OPENPANDA_MODEL when set
//	panda init --non-interactive  # CI: never wait for input (auto when stdin
//	                              # is not a terminal)
//	panda init --config ./config.yaml --card ./capabilities.yaml
import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/hwinfo"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"gopkg.in/yaml.v3"
)

func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	configPath := fs.String("config", "", "path to write config.yaml (default: user config dir)")
	cardPath := fs.String("card", "", "path to write capabilities.yaml (default: <config dir>/capabilities.yaml)")
	defaultsMode := fs.Bool("defaults", false,
		"zero prompts: take every detected/default value; the model section is baked in from OPENPANDA_MODEL_API_KEY / OPENPANDA_MODEL when set, otherwise left for the web settings page")
	nonInteractive := fs.Bool("non-interactive", false,
		"CI: never wait for input — every would-be prompt takes its default (model setup skipped); auto-enabled when stdin is not a terminal. Unlike --defaults, env model vars are NOT written into the file (the daemon still reads them live at startup)")
	fs.Parse(args)

	loc := i18n.Detect()
	target := resolveInitConfigPath(*configPath)

	if exists(target) {
		fmt.Println(i18n.Tf(loc, "init.exists", "path", target))
		os.Exit(1)
	}

	// The hardware scan replaces the node prompts: name from the hostname,
	// resource class from RAM, kind from the VM probe (identity only then).
	card := detectCard()
	def := config.Default()
	def.Node.Name = orDefault(card.Device, def.Node.Name)
	def.Node.ResourceClass = orDefault(card.ResourceClass, def.Node.ResourceClass)
	def.Node.Kind = config.NodeKindPhysical
	if detectVM() {
		def.Node.Kind = config.NodeKindVM
		def.Node.Identity = autoVMIdentity()
	}
	fmt.Println(i18n.Tf(loc, "init.node.summary", "name", def.Node.Name,
		"class", def.Node.ResourceClass, "kind", def.Node.Kind))
	if def.Node.Kind == config.NodeKindVM {
		fmt.Println(i18n.Tf(loc, "init.node.vm", "identity", def.Node.Identity))
	}

	// Model setup is the single question. --defaults, --non-interactive, and
	// a non-TTY stdin all answer it with the default (skip) without reading.
	modelConfigured := false
	switch {
	case *defaultsMode:
		if adoptEnvModel(def) {
			modelConfigured = true
			fmt.Println(i18n.T(loc, "init.model.env"))
		}
	case *nonInteractive || !stdinIsTTY():
		// Nothing to ask: defaults only, never block on input.
	default:
		in := bufio.NewReader(os.Stdin)
		if askYes(in, i18n.T(loc, "init.model.ask")) {
			prompt := func(label, fallback string) string {
				if fallback != "" {
					fmt.Printf("%s [%s]: ", label, fallback)
				} else {
					fmt.Printf("%s: ", label)
				}
				s, _ := in.ReadString('\n')
				s = strings.TrimSpace(s)
				if s == "" {
					return fallback
				}
				return s
			}
			// promptValid re-asks while the answer fails accept(); empty keeps the default.
			promptValid := func(label, fallback string, accept func(string) bool) string {
				for {
					s := prompt(label, fallback)
					if accept(s) {
						return s
					}
					fmt.Println(i18n.T(loc, "init.invalid"))
				}
			}
			def.Model.APIType = orDefault(promptValid(i18n.T(loc, "init.model.apitype"), config.APITypeAnthropic,
				func(s string) bool { return s == config.APITypeAnthropic || s == config.APITypeOpenAI }),
				config.APITypeAnthropic)
			def.Model.BaseURL = orDefault(prompt(i18n.T(loc, "init.model.baseurl"), def.Model.BaseURL), def.Model.BaseURL)
			def.Model.Model = orDefault(prompt(i18n.T(loc, "init.model.name"), def.Model.Model), def.Model.Model)
			def.Model.APIKey = prompt(i18n.T(loc, "init.model.apikey"), "")
			modelConfigured = true
		}
	}
	if !modelConfigured {
		fmt.Println(i18n.T(loc, "init.model.skipped"))
	}

	// Belt and braces: never write a config the node would refuse to load.
	if err := def.Validate(); err != nil {
		fatal("validate config", err)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fatal("create config dir", err)
	}
	data, err := yaml.Marshal(def)
	if err != nil {
		fatal("render config", err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		fatal("write config", err)
	}
	fmt.Println(i18n.Tf(loc, "init.config.written", "path", target))

	// Capability card next to the config unless the user named one.
	cardOut := *cardPath
	if cardOut == "" {
		cardOut = filepath.Join(filepath.Dir(target), "capabilities.yaml")
	}
	cardData, err := yaml.Marshal(card)
	if err != nil {
		fatal("render card", err)
	}
	if err := os.WriteFile(cardOut, cardData, 0o644); err != nil {
		fatal("write card", err)
	}
	fmt.Println(i18n.Tf(loc, "init.card.written", "path", cardOut))

	fmt.Println(i18n.Tf(loc, "init.next", "config", target, "card", cardOut))
}

// askYes prints question with a [y/N] hint and reports whether the answer
// means yes. Empty input — the shown default — means no.
func askYes(in *bufio.Reader, question string) bool {
	fmt.Printf("%s [y/N]: ", question)
	s, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "是", "好", "行", "はい", "sí", "si", "ja", "j":
		return true
	}
	return false
}

// adoptEnvModel copies the model env vars into the generated config so a
// --defaults run bakes them into the file. Only the two documented vars are
// adopted; every other OPENPANDA_MODEL_* override keeps applying live at
// config load time, exactly as before.
func adoptEnvModel(def *config.Config) bool {
	adopted := false
	if v := os.Getenv("OPENPANDA_MODEL_API_KEY"); v != "" {
		def.Model.APIKey = v
		adopted = true
	}
	if v := os.Getenv("OPENPANDA_MODEL"); v != "" {
		def.Model.Model = v
		adopted = true
	}
	return adopted
}

// vmVendorKeywords are the hypervisor vendor markers probed in platform
// identification strings when deciding whether the host is a VM guest.
var vmVendorKeywords = []string{
	"vmware", "virtualbox", "parallels", "kvm", "qemu",
	"xen", "hyper-v", "hyperv", "bhyve", "virtual machine",
}

// detectVM best-effort reports whether this machine is a VM guest, so init
// can preset node.kind=vm and an identity without asking. On macOS
// kern.hv_support is useless as a signal — it is 1 on every Apple Silicon
// Mac (the OS itself runs on Apple's hypervisor), so vendor strings are
// matched instead. On Linux the cpuinfo hypervisor flag or DMI names decide.
// On Windows the BIOS vendor keys in the registry carry the same DMI strings.
func detectVM() bool {
	switch runtime.GOOS {
	case "darwin":
		blob := strings.ToLower(hwinfo.Probe("sysctl", "-n", "hw.model") + " " +
			hwinfo.Probe("ioreg", "-rn", "IOPlatformExpertDevice"))
		return containsAny(blob, vmVendorKeywords)
	case "linux":
		if hwinfo.Probe("sh", "-c", "grep -m1 hypervisor /proc/cpuinfo") != "" {
			return true
		}
		blob := strings.ToLower(hwinfo.Probe("sh", "-c",
			"cat /sys/class/dmi/id/product_name /sys/class/dmi/id/sys_vendor 2>/dev/null"))
		return containsAny(blob, vmVendorKeywords)
	case "windows":
		blob := strings.ToLower(hwinfo.Probe("reg", "query",
			`HKLM\HARDWARE\DESCRIPTION\System\BIOS`, "/s"))
		return containsAny(blob, vmVendorKeywords)
	}
	return false
}

// containsAny reports whether s contains any of the keywords.
func containsAny(s string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// autoVMIdentity derives a stable VM identity without prompting: the
// hostname with a vm- prefix keeps it distinct from the physical host's
// fingerprint; the machine identity is the fallback when the hostname is
// unknown.
func autoVMIdentity() string {
	if h := hwinfo.Hostname(); h != "" && h != "unknown-host" {
		return "vm-" + h
	}
	return config.MachineIdentity()
}

// resolveInitConfigPath picks where init writes: an explicit flag or
// OPENPANDA_CONFIG_PATH wins, then a user-writable user config dir, then the
// system default. This matches ResolvePath's read order, so a config written
// here is read back automatically by `panda web` and the daemon.
func resolveInitConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("OPENPANDA_CONFIG_PATH"); env != "" {
		return env
	}
	if p, err := config.UserConfigPath(); err == nil {
		return p
	}
	return config.DefaultPath
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
