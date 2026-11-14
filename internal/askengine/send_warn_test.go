// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/ledger"
	"github.com/Xustalis/OpenPanda/internal/testdb"
)

// TestSchedulerlessSendWarnsDeadQueue pins the store-bound probe on a
// scheduler-less engine: a listening panel port is not a queue consumer when
// nothing heartbeats THIS store. The old process-level fallback read the
// console's own socket as a live consumer and reported a silent success for a
// note that could never be read.
func TestSchedulerlessSendWarnsDeadQueue(t *testing.T) {
	dir := t.TempDir()
	db := testdb.OpenFile(t, filepath.Join(dir, "probe.db"))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	cfg := &config.Config{
		Node:    config.NodeConfig{Name: "warn-probe", Kind: config.NodeKindVM, Identity: "warn-probe-identity"},
		Network: config.NetworkConfig{PanelAddr: ln.Addr().String()},
	}
	nodeID := core.RuntimeNodeID(cfg.Node.Name, cfg.Node.Kind, cfg.Node.EffectiveIdentity())
	store := core.NewTaskStore(db, slog.New(slog.DiscardHandler))
	ctx := context.Background()
	task, err := store.Create(ctx, "", "proj", "queued target", nodeID, []string{nodeID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Queue(ctx, task.TaskID, nodeID); err != nil {
		t.Fatalf("queue: %v", err)
	}

	// No scheduler: SendTaskMessage's durable-only path. The listening port
	// must not make it look like a consumer.
	e := &Engine{
		db:     db,
		cfg:    cfg,
		logger: slog.New(slog.DiscardHandler),
		locale: i18n.English,
	}
	status, warn, err := e.SendTaskMessage(ctx, task.TaskID, "read this later", false)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if status != "queued" {
		t.Fatalf("status = %q, want queued", status)
	}
	if warn == "" {
		t.Fatal("a queued task on a store with no heartbeat must carry the no-consumer warning")
	}

	// A foreign heartbeat into this store is the only thing that proves a
	// consumer; once present, the advisory clears even though the port is
	// still just this test's listener.
	if err := ledger.Register(db, ledger.Card{Device: "warn-probe"}, nodeID, 1); err != nil {
		t.Fatalf("register heartbeat row: %v", err)
	}
	_, warn, err = e.SendTaskMessage(ctx, task.TaskID, "and this", false)
	if err != nil {
		t.Fatalf("send with heartbeat: %v", err)
	}
	if warn != "" {
		t.Fatalf("a store-backed heartbeat must clear the warning, got %q", warn)
	}
}
