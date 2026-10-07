// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda auth` — subscription OAuth login. `panda auth login anthropic`
// runs the provider's PKCE consent flow (browser → paste the code the
// callback page shows) and stores the token set under the CLI state dir;
// setting `model.auth: anthropic` in config then authenticates the entry
// model with that subscription instead of an api_key.

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/auth"
	"github.com/Xustalis/OpenPanda/internal/i18n"
)

func runAuth(args []string) {
	if len(args) == 0 {
		authUsage()
		os.Exit(2)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "login":
		runAuthLogin(rest)
	case "logout":
		runAuthLogout(rest)
	case "status":
		runAuthStatus(rest)
	case "help", "-h", "--help":
		authUsage()
	default:
		fmt.Fprintf(os.Stderr, "panda: unknown auth verb %q\n", verb)
		authUsage()
		os.Exit(2)
	}
}

func authUsage() {
	fmt.Fprintln(os.Stderr, "usage: panda auth <verb>")
	fmt.Fprintln(os.Stderr, "  login <provider>    sign in with a subscription (providers: "+strings.Join(auth.ProviderIDs(), ", ")+")")
	fmt.Fprintln(os.Stderr, "  logout <provider>   remove the stored token")
	fmt.Fprintln(os.Stderr, "  status              show stored credentials and expiry")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "after `panda auth login anthropic`, set `model.auth: anthropic` in config.yaml")
	fmt.Fprintln(os.Stderr, "to authenticate the entry model with the subscription instead of an api_key.")
}

func runAuthLogin(args []string) {
	fs := flag.NewFlagSet("auth login", flag.ExitOnError)
	fs.Parse(args)
	provider := strings.TrimSpace(fs.Arg(0))
	if provider == "" {
		fmt.Fprintln(os.Stderr, "usage: panda auth login <provider>")
		os.Exit(2)
	}
	p, err := auth.Lookup(provider)
	if err != nil {
		fmt.Fprintf(os.Stderr, "panda: unknown provider %q — one of: %s\n", provider, strings.Join(auth.ProviderIDs(), ", "))
		os.Exit(2)
	}
	verifier, challenge, state, err := auth.NewPKCE()
	if err != nil {
		fatal("pkce", err)
	}
	url := p.AuthorizeURLFor(challenge, state)
	loc := i18n.Detect()
	fmt.Println(i18n.Tf(loc, "cli.auth.open", "label", p.Label))
	fmt.Println()
	fmt.Println("  " + url)
	fmt.Println()
	tryOpenBrowser(url)
	fmt.Println(i18n.T(loc, "cli.auth.paste"))
	fmt.Print("> ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		fatal("read code", err)
	}
	code, gotState := parsePastedCode(strings.TrimSpace(line))
	if code == "" {
		fatal("exchange", fmt.Errorf("%s", i18n.T(loc, "cli.auth.badcode")))
	}
	if gotState != "" && gotState != state {
		fatal("exchange", fmt.Errorf("%s", i18n.T(loc, "cli.auth.badstate")))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tok, err := p.Exchange(ctx, code, verifier, state)
	if err != nil {
		fatal("exchange", err)
	}
	store := auth.OpenStore(auth.DefaultStateDir())
	if err := store.Put(provider, *tok); err != nil {
		fatal("save token", err)
	}
	fmt.Println(i18n.Tf(loc, "cli.auth.saved", "provider", provider, "path", store.Path()))
	fmt.Println(i18n.Tf(loc, "cli.auth.hint", "provider", provider))
}

// parsePastedCode accepts the "code#state" form the hosted callback page
// displays, a bare code, or a full pasted callback URL with ?code=&state=.
func parsePastedCode(s string) (code, state string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if i := strings.Index(s, "code="); i >= 0 {
		// A pasted URL or fragment containing query parameters.
		q := s[i:]
		if j := strings.IndexAny(q, " \t"); j >= 0 {
			q = q[:j]
		}
		for _, kv := range strings.FieldsFunc(q, func(r rune) bool { return r == '&' || r == '#' || r == '?' }) {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch k {
			case "code":
				code = v
			case "state":
				state = v
			}
		}
		return code, state
	}
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// tryOpenBrowser best-effort opens the consent URL; a headless box simply
// leaves the printed URL for manual copy.
func tryOpenBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func runAuthLogout(args []string) {
	fs := flag.NewFlagSet("auth logout", flag.ExitOnError)
	fs.Parse(args)
	provider := strings.TrimSpace(fs.Arg(0))
	if provider == "" {
		fmt.Fprintln(os.Stderr, "usage: panda auth logout <provider>")
		os.Exit(2)
	}
	store := auth.OpenStore(auth.DefaultStateDir())
	if err := store.Delete(provider); err != nil {
		fatal("logout", err)
	}
	fmt.Println(i18n.Tf(i18n.Detect(), "cli.auth.loggedOut", "provider", provider))
}

func runAuthStatus(args []string) {
	fs := flag.NewFlagSet("auth status", flag.ExitOnError)
	fs.Parse(args)
	store := auth.OpenStore(auth.DefaultStateDir())
	tokens, err := store.Tokens()
	if err != nil {
		fatal("read tokens", err)
	}
	loc := i18n.Detect()
	if len(tokens) == 0 {
		fmt.Println(i18n.T(loc, "cli.auth.none"))
		return
	}
	ids := make([]string, 0, len(tokens))
	for id := range tokens {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		tok := tokens[id]
		p, _ := auth.Lookup(id)
		label := p.Label
		if label == "" {
			label = id
		}
		expires := i18n.T(loc, "cli.auth.never")
		if !tok.ExpiresAt.IsZero() {
			if tok.Expired(time.Now()) {
				expires = i18n.Tf(loc, "cli.auth.expired", "at", tok.ExpiresAt.Format("2006-01-02 15:04"))
			} else {
				expires = i18n.Tf(loc, "cli.auth.expires", "at", tok.ExpiresAt.Format("2006-01-02 15:04"))
			}
		}
		refresh := ""
		if tok.RefreshToken != "" {
			refresh = " (+refresh)"
		}
		fmt.Printf("%-12s %-28s %s%s\n", id, label, expires, refresh)
	}
}
