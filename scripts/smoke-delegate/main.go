// Command smoke-delegate verifies cross-process task delegation without the
// entry model: it becomes an ephemeral scheduler participant (the same
// pattern internal/askengine uses — a fresh node id, the node's capability
// card, peers dialed once), submits a task requiring an ability only a peer
// has, and reports where the task ran and what it produced.
//
// It exists for loopback and real-device verification runs:
//
//	go build -o /tmp/panda ./cmd/panda
//	/tmp/panda --config nodeA/config.yaml --card nodeA/capabilities.yaml &  # daemon A
//	/tmp/panda --config nodeB/config.yaml --card nodeB/capabilities.yaml &  # daemon B (has sys:smoke)
//	go run ./scripts/smoke-delegate --config nodeA/config.yaml \
//	    --card nodeA/capabilities.yaml --requires sys:smoke
//
// Exit 0 means the task reached done on a peer; anything else is a failure
// with the routing/execution error on stderr.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

func main() {
	configPath := flag.String("config", "", "path to config.yaml (its network.peers are dialed)")
	cardPath := flag.String("card", "", "path to the submitter's capabilities.yaml")
	requires := flag.String("requires", "sys:smoke", "ability id the task requires (must live on a peer)")
	title := flag.String("title", "smoke: delegation round-trip", "task title")
	timeout := flag.Duration("timeout", 20*time.Second, "overall deadline")
	flag.Parse()

	if *configPath == "" || *cardPath == "" {
		fmt.Fprintln(os.Stderr, "usage: smoke-delegate --config PATH --card PATH [--requires id] [--title s]")
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	if cfg.Network.SharedSecret == "" {
		fatal("config", fmt.Errorf("network.shared_secret is not set — peers would refuse our hello"))
	}
	card, err := ledger.LoadCard(*cardPath)
	if err != nil {
		fatal("load capabilities", err)
	}

	// A private throwaway DB for the ephemeral's slice of the capability
	// directory: what it learns arrives over the wire from dialed peers, so
	// a delegation seen here really crossed a process boundary.
	tmp, err := os.MkdirTemp("", "smoke-delegate-*")
	if err != nil {
		fatal("temp dir", err)
	}
	defer os.RemoveAll(tmp)
	db, err := storage.Open(filepath.Join(tmp, "dir.db"))
	if err != nil {
		fatal("open db", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		fatal("migrate db", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	sched := core.NewCore(db, core.EphemeralNodeID(cfg.Node.Name), card, 5, logger, cfg.Model)
	sched.SetRouterPolicy(cfg.Injection, cfg.Routing)
	sched.SetSharedSecret(cfg.Network.SharedSecret)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	for _, peer := range cfg.Network.Peers {
		if err := sched.DialPeer(ctx, peer); err != nil {
			fatal("dial peer "+peer, err)
		}
	}
	// Wait until at least one dialed peer shows up online in the directory.
	deadline := time.Now().Add(5 * time.Second)
	online := 0
	for time.Now().Before(deadline) {
		if nodes, err := ledger.Query(db, "online", ""); err == nil && len(nodes) > 0 {
			online = len(nodes)
			break
		}
		select {
		case <-ctx.Done():
			fatal("wait peers", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if online == 0 {
		fatal("wait peers", fmt.Errorf("no online peers in the capability directory after 5s"))
	}

	task, result, err := sched.Submit(ctx, core.TaskInput{
		Title:    *title,
		Intent:   "smoke round-trip: " + *requires,
		Requires: []string{*requires},
	})
	if err != nil {
		fatal("submit", err)
	}

	fmt.Printf("task      %s\n", task.TaskID)
	fmt.Printf("state     %s\n", task.State)
	// The submitter's row keeps owner = the creating node; the executor's
	// own queue (`panda queue` on the peer) shows where it actually ran.
	fmt.Printf("owner     %s (submitter view; check the peer's queue for the executor row)\n", task.OwnerNode)
	fmt.Printf("ok        %v\n", result.OK)
	if result.Stdout != "" {
		fmt.Printf("stdout    %s", result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprintf(os.Stderr, "stderr    %s\n", result.Stderr)
	}
	if task.State != core.StateDone || !result.OK {
		os.Exit(1)
	}
}

func fatal(step string, err error) {
	fmt.Fprintf(os.Stderr, "smoke-delegate: %s: %v\n", step, err)
	os.Exit(1)
}
