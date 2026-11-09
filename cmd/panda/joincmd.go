// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda join` — the cold-start front door (cross-network mesh §2). A node
// with zero information needs exactly one address to knock on, and a URI is
// a way to carry it:
//
//	panda join 'panda://join?b=203.0.113.5:7836&k=k:<introducer-pub>'
//
// The flow is the existing pairing ceremony, unchanged: dial → derive a
// pairing code → the introducer's operator confirms → the secret arrives
// sealed. What the URI adds is that the joiner can check two things before
// accepting anything — that the responder is the identity the operator was
// told to expect (k=), and that the derived code matches the one they were
// given (c=). Either mismatch aborts; a wrong k= just moves on to the next
// contact point, because ANY member may legitimately be listed as one.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/trust"
)

// runJoin implements `panda join <uri>`.
func runJoin(args []string) {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	trustLevel := fs.String("trust", "", "trust level pinned on the joined peer (mesh|guarded|open; default mesh)")
	fs.Parse(reorderFlags(fs, args))
	rest := fs.Args()
	loc := i18n.Detect()
	if lv, err := trust.ParseLevel(*trustLevel); err != nil {
		fatal("bad trust level", err)
	} else {
		*trustLevel = trust.StoredLevel(lv)
	}
	if len(rest) != 1 {
		fatal("usage", fmt.Errorf("%s", i18n.T(loc, "cli.join.usage")))
	}
	spec, err := core.ParseJoinURI(rest[0])
	if err != nil {
		fatal("parse join URI", err)
	}
	joinWithSpec(os.Stdout, *configPath, spec, *trustLevel)
}

// joinWithSpec runs the join against a parsed URI. Split from runJoin so the
// TUI/REPL surface can enter through the same door.
func joinWithSpec(w io.Writer, configPath string, spec *core.JoinSpec, trustLevel string) {
	loc := i18n.Detect()
	cfg, err := config.Load(configPath)
	if err != nil {
		fatal("load config", err)
	}
	db, _, err := panelStore(cfg)
	if err != nil {
		fatal("open store", err)
	}
	defer db.Close()

	pub, _, _ := core.LoadNodeKey(db)
	if len(pub) == 0 {
		fatal("join", errors.New("this node has no identity key yet — run `panda daemon` once so it can mint one"))
	}
	selfID := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())

	pc, dialAddr := dialJoinBootstraps(w, loc, spec, selfID, cfg, pub)
	if pc == nil {
		fatal("join", fmt.Errorf("%s", i18n.T(loc, "cli.pair.unreachable")))
	}
	defer pc.Close()

	// The code pin is checked BEFORE the operator waits on anything. Unlike a
	// wrong introducer (skipped inside the dial loop, because any member may
	// legitimately be a contact point), a code mismatch stops the join
	// outright: the DH exchange completed but the far end is not the party
	// whose code the operator holds. That is the MITM detection firing, and
	// no secret may move.
	if !spec.CodeMatches(pc.Code) {
		fatal("join", fmt.Errorf("%s", i18n.Tf(loc, "cli.join.codemismatch",
			"want", spec.Code, "got", strings.ReplaceAll(pc.Code, "-", ""))))
	}

	p := pal()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+p.Accent(i18n.Tf(loc, "cli.pair.code", "code", pc.Code)))
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T(loc, "cli.pair.waiting"))

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute+30*time.Second)
	defer cancel()
	secret, err := pc.WaitSecret(ctx)
	if err != nil {
		var rj *core.PairRejected
		switch {
		case errors.As(err, &rj):
			fatal("join", fmt.Errorf("%s", i18n.Tf(loc, "cli.pair.rejected", "reason", rj.Reason)))
		case errors.Is(err, core.ErrPairExpired), errors.Is(err, context.DeadlineExceeded):
			fatal("join", fmt.Errorf("%s", i18n.T(loc, "cli.pair.timeout")))
		default:
			fatal("waiting", fmt.Errorf("%s — %s", err, i18n.T(loc, "cli.pair.daemonhint")))
		}
	}
	adoptPairedSecret(w, loc, configPath, cfg, secret, dialAddr, trustLevel)
}

// dialJoinBootstraps walks the URI's contact points in order and returns the
// first pairing client that answered. A contact point that cannot be dialed
// is reported and skipped — the list exists precisely so one dead address is
// not the end of the join.
func dialJoinBootstraps(w io.Writer, loc i18n.Locale, spec *core.JoinSpec, selfID string, cfg *config.Config, pub []byte) (*core.PairClient, string) {
	target := ""
	if spec.Introducer != "" {
		// With k= the contact point is asked to RELAY the handshake to the
		// introducer instead of answering it — the cross-NAT path (P4b). A
		// directly reachable node answers either way.
		target = "k:" + spec.Introducer
	}
	for _, b := range spec.Bootstraps {
		addr := normalizeDialable(b)
		fmt.Fprintln(w, i18n.Tf(loc, "cli.pair.start", "name", b, "addr", addr))
		pc, err := core.DialPairTo(context.Background(), addr, target, selfID, cfg.Node.Name, cfg.Network.ListenAddr, pub)
		if err != nil {
			var rj *core.PairRejected
			if errors.As(err, &rj) {
				// A contact point that refuses is a policy answer, not a
				// transport failure — say so instead of trying the next one
				// as if the address were merely dead.
				fmt.Fprintln(w, i18n.Tf(loc, "cli.pair.rejected", "reason", rj.Reason))
				continue
			}
			fmt.Fprintln(w, i18n.Tf(loc, "cli.join.unreachable", "addr", addr))
			continue
		}
		// A contact point may be any online member, so an identity that does
		// not match k= is not proof of an attack — it is simply not the
		// introducer. Close it and try the next address; if none matches, the
		// join fails with the unroutable message rather than accepting a
		// stranger's secret.
		if !spec.IntroducerMatches(pc.PeerPub) {
			fmt.Fprintln(w, i18n.Tf(loc, "cli.join.wrongkey", "addr", addr, "key", pc.PeerPub))
			pc.Close()
			continue
		}
		return pc, addr
	}
	return nil, ""
}
