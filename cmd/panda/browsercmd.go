// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda browser` is the CLI half of the node's headless browser. The agent
// tier that speaks MCP gets the same operations as panda_browser_* tools; this
// is the path for agents (and people) that only have a shell.
//
// The verbs are deliberately small and composable: open a page, snapshot its
// controls and accessibility tree, click/type/scroll, read console and
// network, eval, screenshot. State lives in the browser host, so a sequence of
// CLI calls acts on one tab.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/browser"
	"github.com/Xustalis/OpenPanda/internal/config"
)

func runBrowser(args []string) {
	if len(args) == 0 {
		fail(2, "usage: panda browser <open|snapshot|screenshot|click|type|press|scroll|eval|console|network|viewport|close>")
	}
	verb := strings.ToLower(strings.TrimSpace(args[0]))
	if verb == "host" {
		runBrowserHost(args[1:])
		return
	}
	fs := flag.NewFlagSet("browser "+verb, flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	viewport := fs.String("viewport", "", "viewport: mobile, tablet, desktop or WIDTHxHEIGHT")
	full := fs.Bool("full", false, "screenshot: capture the whole page")
	out := fs.String("out", "", "screenshot: copy the image here")
	selector := fs.String("selector", "", "click/type: CSS selector")
	x := fs.Float64("x", -1, "click: viewport x")
	y := fs.Float64("y", -1, "click: viewport y")
	textFlag := fs.String("text", "", "type: text to enter")
	clear := fs.Bool("clear", false, "type: replace the field's value first")
	submit := fs.Bool("submit", false, "type: press Enter afterwards")
	limit := fs.Int("limit", 0, "console/network: newest N entries")
	fs.Parse(reorderFlags(fs, args[1:]))
	rest := fs.Args()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	client := browser.NewClient(browser.StateDirFor(filepath.Dir(cfg.Storage.DBPath)), executablePath())

	op := verb
	opArgs := map[string]any{}
	switch verb {
	case "open", "goto", "navigate":
		op = "open"
		if len(rest) == 0 {
			fail(2, "usage: panda browser open <url> [--viewport mobile|tablet|desktop|WxH]")
		}
		opArgs["url"] = rest[0]
		opArgs["viewport"] = *viewport
	case "snapshot", "read":
		op = "snapshot"
	case "screenshot", "shot":
		op = "screenshot"
		opArgs["full"] = *full
	case "click":
		op = "click"
		if *selector != "" {
			opArgs["selector"] = *selector
		} else if len(rest) > 0 {
			opArgs["selector"] = rest[0]
		} else if *x >= 0 && *y >= 0 {
			opArgs["x"], opArgs["y"] = *x, *y
		} else {
			fail(2, "usage: panda browser click <selector> | --selector S | --x X --y Y")
		}
	case "type", "fill", "input":
		op = "type"
		if *selector != "" {
			opArgs["selector"] = *selector
		} else if len(rest) > 0 {
			opArgs["selector"] = rest[0]
			rest = rest[1:]
		}
		text := *textFlag
		if text == "" {
			text = strings.Join(rest, " ")
		}
		if text == "" {
			fail(2, "usage: panda browser type <selector> <text> | --selector S --text T")
		}
		opArgs["text"] = text
		opArgs["clear"] = *clear
		opArgs["submit"] = *submit
	case "press", "key":
		op = "press"
		if len(rest) == 0 {
			fail(2, "usage: panda browser press <Enter|Tab|Escape|ArrowDown|...>")
		}
		opArgs["key"] = rest[0]
	case "scroll":
		op = "scroll"
		dx, dy := 0.0, 600.0
		if len(rest) >= 2 {
			dx, _ = strconv.ParseFloat(rest[0], 64)
			dy, _ = strconv.ParseFloat(rest[1], 64)
		} else if len(rest) == 1 {
			dy, _ = strconv.ParseFloat(rest[0], 64)
		}
		opArgs["dx"], opArgs["dy"] = dx, dy
	case "eval", "js":
		op = "eval"
		if len(rest) == 0 {
			fail(2, "usage: panda browser eval <expression>")
		}
		opArgs["expression"] = strings.Join(rest, " ")
	case "console", "logs":
		op = "console"
		opArgs["limit"] = *limit
	case "network", "requests":
		op = "network"
		opArgs["limit"] = *limit
	case "viewport", "resize":
		op = "viewport"
		spec := *viewport
		if spec == "" && len(rest) > 0 {
			spec = rest[0]
		}
		if spec == "" {
			fail(2, "usage: panda browser viewport <mobile|tablet|desktop|WIDTHxHEIGHT>")
		}
		opArgs["viewport"] = spec
	case "close", "quit":
		op = "close"
	default:
		fail(2, fmt.Sprintf("unknown browser verb %q", verb))
	}

	res, err := client.Do(context.Background(), op, opArgs)
	if err != nil {
		fail(1, err.Error())
	}
	if op == "screenshot" && *out != "" && res.Screenshot != "" {
		if err := copyFile(res.Screenshot, *out); err != nil {
			fail(1, err.Error())
		}
		res.Screenshot = *out
	}
	if jsonOutput {
		emitJSON(res)
		return
	}
	printBrowserResult(res)
}

// runBrowserHost is the long-lived side: one Chrome per node, shared by every
// MCP process and CLI invocation. It is spawned by the client, but running it
// by hand is useful while debugging.
func runBrowserHost(args []string) {
	fs := flag.NewFlagSet("browser host", flag.ExitOnError)
	state := fs.String("state", "", "state directory (default: <data>/browser)")
	fs.Parse(reorderFlags(fs, args))
	dir := *state
	if dir == "" {
		cfg, err := config.Load(cliConfigPath)
		if err != nil {
			fatal("load config", err)
		}
		dir = browser.StateDirFor(filepath.Dir(cfg.Storage.DBPath))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := browser.ServeHost(ctx, browser.HostOptions{
		StateDir:   dir,
		ChromePath: os.Getenv("OPENPANDA_CHROME"),
	}); err != nil {
		fail(1, err.Error())
	}
}

func executablePath() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return ""
}

func copyFile(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil && filepath.Dir(dst) != "." {
		return err
	}
	return os.WriteFile(dst, raw, 0o644)
}

func printBrowserResult(r browser.Result) {
	if r.Message != "" {
		fmt.Println(r.Message)
	}
	if r.URL != "" {
		fmt.Println(pal().Heading("url:") + " " + r.URL)
	}
	if r.Title != "" {
		fmt.Println(pal().Heading("title:") + " " + r.Title)
	}
	if r.Text != "" {
		fmt.Println(r.Text)
	}
	if r.Tree != "" {
		fmt.Println(pal().Heading("accessibility:"))
		fmt.Println(r.Tree)
	}
	if len(r.Controls) > 0 {
		fmt.Println(pal().Heading("controls:"))
		for _, c := range r.Controls {
			line := fmt.Sprintf("  %-10s %s  [%s]", c.Role, c.Name, c.Selector)
			if c.Value != "" {
				line += "  value=" + strconv.Quote(c.Value)
			}
			if c.Disabled {
				line += "  (disabled)"
			}
			fmt.Println(line)
		}
	}
	if r.Screenshot != "" {
		fmt.Printf("%s %s (%dx%d)\n", pal().Heading("screenshot:"), r.Screenshot, r.Width, r.Height)
	}
	if len(r.Console) > 0 {
		fmt.Println(pal().Heading("console:"))
		for _, e := range r.Console {
			line := fmt.Sprintf("  [%s] %s", e.Type, e.Text)
			if e.URL != "" {
				line += fmt.Sprintf(" (%s:%d)", e.URL, e.Line)
			}
			fmt.Println(line)
		}
	}
	if len(r.Network) > 0 {
		fmt.Println(pal().Heading("network:"))
		for _, e := range r.Network {
			line := fmt.Sprintf("  %-6s %s", e.Method, e.URL)
			if e.Status > 0 {
				line += fmt.Sprintf(" → %d", e.Status)
			}
			if e.Failed != "" {
				line += "  ✗ " + e.Failed
			}
			if e.DurationMS > 0 {
				line += fmt.Sprintf(" (%dms)", e.DurationMS)
			}
			fmt.Println(line)
		}
	}
	if r.Value != nil {
		fmt.Println(pal().Heading("value:"))
		printJSONValue(r.Value)
	}
}

func printJSONValue(v any) {
	switch x := v.(type) {
	case []byte:
		fmt.Println(string(x))
	case string:
		fmt.Println(x)
	default:
		emitJSON(v)
	}
}
