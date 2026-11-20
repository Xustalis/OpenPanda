// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestMigrateTracksUserVersion(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if want := migrations[len(migrations)-1].Version; version != want {
		t.Fatalf("user_version = %d, want %d", version, want)
	}
}

func TestMigrateDetachesLegacyClearedGoalRounds(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, m := range migrations {
		if m.Version > 76 {
			break
		}
		if err = runMigration(db, m); err != nil {
			t.Fatalf("migration %d: %v", m.Version, err)
		}
	}
	for _, query := range []string{
		`INSERT INTO ask_rounds(round_id,session_id,locale,lease_until,created_at) VALUES('old-round','old-session','en',1,1)`,
		`INSERT INTO conversation_goals(id,session_id,objective,status,round_id,lease_owner,lease_until,created_at,updated_at) VALUES
 ('old','old-session','objective','cleared','old-round','owner',1,1,1),
 ('orphan','deleted-session','objective','cleared','missing-round','owner',1,1,1),
 ('live','live-session','objective','paused','live-round','owner',1,1,1)`,
		`INSERT INTO conversation_goal_rounds(round_id,goal_id,generation,entry_tokens) VALUES('old-round','old',1,17)`,
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	var stale int
	if err = db.QueryRow(`SELECT COUNT(*) FROM conversation_goals WHERE status='cleared' AND (round_id!='' OR lease_owner!='' OR lease_until!=0)`).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("migration left %d stale round pointers: %v", stale, err)
	}
	var live string
	if err = db.QueryRow(`SELECT round_id FROM conversation_goals WHERE id='live'`).Scan(&live); err != nil || live != "live-round" {
		t.Fatalf("migration touched the live round: %q, %v", live, err)
	}
	var usage int
	if err = db.QueryRow(`SELECT entry_tokens FROM conversation_goal_rounds WHERE goal_id='old'`).Scan(&usage); err != nil || usage != 17 {
		t.Fatalf("migration lost the original ledger: %d, %v", usage, err)
	}
}

func TestMigrateFromV0(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	createV0Schema(t, db)

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate from v0: %v", err)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if want := migrations[len(migrations)-1].Version; version != want {
		t.Fatalf("user_version = %d, want %d", version, want)
	}

	for _, tc := range []struct{ table, col string }{
		{"tasks", "authorized"},
		{"tasks", "requires_json"},
		{"employee_cache", "resource_profile_json"},
	} {
		if !columnExists(t, db, tc.table, tc.col) {
			t.Fatalf("column %s.%s missing after migration", tc.table, tc.col)
		}
	}
}

func TestMigrateIdempotentFromV0(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	createV0Schema(t, db)

	for i := 0; i < 3; i++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("migrate %d: %v", i+1, err)
		}
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if want := migrations[len(migrations)-1].Version; version != want {
		t.Fatalf("user_version = %d, want %d", version, want)
	}
}

// createV0Schema simulates a pre-versioning Phase 0 database: the baseline
// tables exist but the columns added by v2-v4 are missing.
func createV0Schema(t *testing.T, db *sql.DB) {
	t.Helper()
	baseline := []string{
		`CREATE TABLE employee_cache (
			id TEXT PRIMARY KEY, name TEXT, department TEXT, chip TEXT,
			native_json TEXT, agents_json TEXT, manual_json TEXT,
			capacity_json TEXT, status TEXT, last_seen INTEGER, scheduler_tier INTEGER
		)`,
		`CREATE TABLE tasks (
			task_id TEXT PRIMARY KEY, parent_id TEXT, project TEXT,
			title TEXT, state TEXT NOT NULL, owner_node TEXT NOT NULL,
			attempt_id TEXT NOT NULL, state_version INTEGER NOT NULL DEFAULT 0,
			lease_expires_at INTEGER, chain_json TEXT, context_type TEXT,
			context_hash TEXT, intent TEXT, spec_json TEXT, result_json TEXT,
			complexity REAL, risk TEXT, resource_json TEXT, model_tier INT,
			created_at INTEGER, updated_at INTEGER
		)`,
	}
	for _, stmt := range baseline {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create v0 table: %v", err)
		}
	}
}

// TestMigrateNewerDatabaseIsRefused pins the D-1 contract: an old binary
// meeting a database that a newer binary already migrated must fail loudly,
// not silently pass and run stale assumptions against a newer schema.
func TestMigrateNewerDatabaseIsRefused(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 999`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	err = Migrate(db)
	if err == nil {
		t.Fatal("migrate on a newer database must fail, got nil")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Fatalf("error should explain the version mismatch, got: %v", err)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 999 {
		t.Fatalf("user_version = %d, want 999 (a refused migrate must not write)", version)
	}
}

// TestMigrateConcurrentHandles simulates two processes starting on the same
// data directory at once (daemon + panel is the everyday case): two handles
// migrate the same file concurrently, and the BEGIN IMMEDIATE + in-tx
// recheck must make every version apply exactly once.
func TestMigrateConcurrentHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")

	// Instrument the migration list: count how many times each version's
	// Apply body actually runs. Restore the original slice when done.
	orig := migrations
	defer func() { migrations = orig }()
	var mu sync.Mutex
	counts := map[int]int{}
	instrumented := make([]Migration, 0, len(orig))
	for _, m := range orig {
		apply := m.Apply
		version := m.Version
		m.Apply = func(tx MigrationExec) error {
			mu.Lock()
			counts[version]++
			mu.Unlock()
			return apply(tx)
		}
		instrumented = append(instrumented, m)
	}
	migrations = instrumented

	dbA, err := Open(path)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	defer dbA.Close()
	dbB, err := Open(path)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	defer dbB.Close()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = Migrate(dbA) }()
	go func() { defer wg.Done(); errs[1] = Migrate(dbB) }()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("handle %c migrate failed: %v", 'A'+i, err)
		}
	}

	want := orig[len(orig)-1].Version
	var version int
	if err := dbA.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != want {
		t.Fatalf("user_version = %d, want %d", version, want)
	}
	for _, m := range orig {
		if counts[m.Version] != 1 {
			t.Errorf("migration v%d (%s) applied %d times, want exactly 1", m.Version, m.Name, counts[m.Version])
		}
	}
}

// TestMigrateUnderPoolPressure pins the conn-pinning contract: Open gives
// file-backed handles a multi-connection pool, so statements issued through
// the raw *sql.DB between a migration's BEGIN and COMMIT would land on
// other connections — running outside the tx or blocking on its write lock.
// Hammering the pool while Migrate runs must not disturb it.
func TestMigrateUnderPoolPressure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pooled.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					var n int
					_ = db.QueryRow(`SELECT 1`).Scan(&n)
				}
			}
		}()
	}
	migErr := Migrate(db)
	close(stop)
	wg.Wait()
	if migErr != nil {
		t.Fatalf("migrate under pool pressure: %v", migErr)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if want := migrations[len(migrations)-1].Version; version != want {
		t.Fatalf("user_version = %d, want %d", version, want)
	}
}

func TestMigrateBackfillsHashChain(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Create a V6-era schema: tables exist, prev_hash columns exist, but rows were
	// written with NULL prev_hash before the backfill logic was available.
	baseline := []string{
		`CREATE TABLE audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER, who TEXT,
			what TEXT, target TEXT, result TEXT, detail TEXT, prev_hash TEXT
		)`,
		`CREATE TABLE task_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT, ts INTEGER,
			type TEXT, data_json TEXT, prev_hash TEXT
		)`,
	}
	for _, stmt := range baseline {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 6`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	// Insert two audit rows with NULL prev_hash.
	if _, err := db.Exec(`INSERT INTO audit_log (ts, who, what, target, result, detail, prev_hash)
		VALUES (1, 'a', 'x', 't1', 'ok', 'd1', NULL)`); err != nil {
		t.Fatalf("insert audit 1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO audit_log (ts, who, what, target, result, detail, prev_hash)
		VALUES (2, 'a', 'y', 't2', 'ok', 'd2', NULL)`); err != nil {
		t.Fatalf("insert audit 2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO audit_log (ts, who, what, target, result, detail, prev_hash)
		VALUES (3, NULL, NULL, NULL, NULL, NULL, NULL)`); err != nil {
		t.Fatalf("insert audit 3 with NULLs: %v", err)
	}

	// Insert events for two tasks: first event NULL, second event NULL.
	if _, err := db.Exec(`INSERT INTO task_events (task_id, ts, type, data_json, prev_hash)
		VALUES ('task-a', 1, 'submit', '{}', NULL)`); err != nil {
		t.Fatalf("insert event 1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_events (task_id, ts, type, data_json, prev_hash)
		VALUES ('task-a', 2, 'result', '{}', NULL)`); err != nil {
		t.Fatalf("insert event 2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_events (task_id, ts, type, data_json, prev_hash)
		VALUES ('task-b', 3, 'submit', '{}', NULL)`); err != nil {
		t.Fatalf("insert event 3: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_events (task_id, ts, type, data_json, prev_hash)
		VALUES ('task-c', 4, NULL, NULL, NULL)`); err != nil {
		t.Fatalf("insert event 4 with NULLs: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate from v6: %v", err)
	}

	// Verify the audit chain is intact.
	rows, err := db.Query(`SELECT prev_hash, ts,
		COALESCE(who, ''), COALESCE(what, ''), COALESCE(target, ''),
		COALESCE(result, ''), COALESCE(detail, '')
		FROM audit_log ORDER BY id ASC`)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	var prevHash string
	for rows.Next() {
		var h string
		var ts int64
		var who, what, target, result, detail string
		if err := rows.Scan(&h, &ts, &who, &what, &target, &result, &detail); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		if h != prevHash {
			t.Fatalf("audit prev_hash mismatch: got %q, want %q", h, prevHash)
		}
		prevHash = hashAudit(h, ts, who, what, target, result, detail)
	}

	// Verify task-a's event chain: first event empty, second equals hash of first.
	var first, second string
	if err := db.QueryRow(`SELECT prev_hash FROM task_events WHERE task_id = 'task-a' ORDER BY id ASC LIMIT 1`).Scan(&first); err != nil {
		t.Fatalf("scan task-a first: %v", err)
	}
	if first != "" {
		t.Fatalf("task-a genesis prev_hash = %q, want empty", first)
	}
	if err := db.QueryRow(`SELECT prev_hash FROM task_events WHERE task_id = 'task-a' ORDER BY id ASC LIMIT 1 OFFSET 1`).Scan(&second); err != nil {
		t.Fatalf("scan task-a second: %v", err)
	}
	wantSecond := hashEvent("", "task-a", 1, "submit", "{}")
	if second != wantSecond {
		t.Fatalf("task-a second prev_hash = %q, want %q", second, wantSecond)
	}

	// Verify task-b's genesis event is empty.
	var b string
	if err := db.QueryRow(`SELECT prev_hash FROM task_events WHERE task_id = 'task-b'`).Scan(&b); err != nil {
		t.Fatalf("scan task-b: %v", err)
	}
	if b != "" {
		t.Fatalf("task-b genesis prev_hash = %q, want empty", b)
	}
}

// TestMigrateV11EntryCache verifies the entry_cache table is created both on
// a fresh database and on a legacy database pinned at v10 (the pre-cache
// schema), and that re-running Migrate on either is a no-op.
func TestMigrateV11EntryCache(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int // user_version to pin before migrating; 0 = fresh db
	}{
		{"fresh database", 0},
		{"legacy v10 database", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(":memory:")
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer db.Close()

			if tc.version > 0 {
				if _, err := db.Exec(`PRAGMA user_version = 10`); err != nil {
					t.Fatalf("set user_version: %v", err)
				}
			}
			if err := Migrate(db); err != nil {
				t.Fatalf("migrate: %v", err)
			}

			var one int
			if err := db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='entry_cache'`).Scan(&one); err != nil {
				t.Fatalf("entry_cache table missing after migration: %v", err)
			}
			var version int
			if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
				t.Fatalf("read user_version: %v", err)
			}
			if want := migrations[len(migrations)-1].Version; version != want {
				t.Fatalf("user_version = %d, want %d", version, want)
			}

			// Idempotent: a second run must not fail or duplicate anything.
			if err := Migrate(db); err != nil {
				t.Fatalf("re-migrate: %v", err)
			}

			// The table is writable in the shape the entry package expects.
			if _, err := db.Exec(`INSERT INTO entry_cache (ns, k1, k2, output_json, created_at) VALUES ('classify', 'a', 'b', '{}', 0)`); err != nil {
				t.Fatalf("insert entry_cache: %v", err)
			}
			var blob string
			if err := db.QueryRow(`SELECT output_json FROM entry_cache WHERE ns='classify' AND k1='a' AND k2='b'`).Scan(&blob); err != nil {
				t.Fatalf("select entry_cache: %v", err)
			}
			if blob != "{}" {
				t.Fatalf("output_json = %q, want {}", blob)
			}
		})
	}
}

// V20 lands the mesh-budget and weighted-routing columns (§4.1/§6.1) on both
// paths: a fresh database and a v19-era file migrated forward.
func TestMigrateV20Columns(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, tc := range []struct{ table, col string }{
		{"tasks", "token_budget"},
		{"employee_cache", "links_json"},
	} {
		if !columnExists(t, db, tc.table, tc.col) {
			t.Fatalf("column %s.%s missing on fresh schema", tc.table, tc.col)
		}
	}
	// Legacy rows see the documented defaults: token_budget=0 (unbounded),
	// links_json='' (no metrics yet — the unknown-link cost applies).
	var budget int64
	var links string
	if err := db.QueryRow(`SELECT token_budget FROM tasks LIMIT 1`).Scan(&budget); err == nil && budget != 0 {
		t.Fatalf("fresh task token_budget = %d, want 0", budget)
	}
	if err := db.QueryRow(`SELECT links_json FROM employee_cache LIMIT 1`).Scan(&links); err == nil && links != "" {
		t.Fatalf("fresh links_json = %q, want ''", links)
	}
}

// V36 re-keys parked outbox custody from instance ids to stable "k:" keys
// wherever the directory holds a proven pub_key (Batch-6). Rows for key-less
// or unknown peers keep their instance key, and a node that helloed under two
// instance ids collapses to one stable row rather than deadlocking the
// migration on the (peer, task_id) primary key.
func TestMigrateV36RekeysOutboxes(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Pre-migration shape: instance-id peers, two of them instances of the
	// same stable identity (a restart that helloed twice).
	for _, q := range []string{
		`INSERT INTO employee_cache (id, pub_key, status, last_seen) VALUES ('b-old', 'pubB', 'offline', 0)`,
		`INSERT INTO employee_cache (id, pub_key, status, last_seen) VALUES ('b-new', 'pubB', 'offline', 0)`,
		`INSERT INTO employee_cache (id, pub_key, status, last_seen) VALUES ('keyless', '', 'offline', 0)`,
		`INSERT INTO result_outbox (peer, task_id, payload_json, created_at) VALUES ('b-old', 't1', '{}', 1)`,
		`INSERT INTO result_outbox (peer, task_id, payload_json, created_at) VALUES ('b-new', 't1', '{"r":2}', 2)`,
		`INSERT INTO result_outbox (peer, task_id, payload_json, created_at) VALUES ('keyless', 't2', '{}', 1)`,
		`INSERT INTO result_outbox (peer, task_id, payload_json, created_at) VALUES ('ghost', 't3', '{}', 1)`,
		`INSERT INTO cancel_outbox (peer, task_id, reason, created_at) VALUES ('b-old', 't1', 'x', 1)`,
		`INSERT INTO task_outbox (peer, task_id, payload_json, transport_type, ttl, created_at) VALUES ('b-old', 't9', '{}', 'dtn', 0, 1)`,
		`INSERT INTO artifact_push_outbox (peer, hash, task_id, total, created_at) VALUES ('b-old', 'h1', 't1', 10, 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}

	// The migration body runs on a pinned conn; on a single-conn :memory:
	// pool it must be released before the verification queries take one.
	mconn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	if err := migrateV36(migrationTx{mconn}); err != nil {
		mconn.Close()
		t.Fatalf("migrateV36: %v", err)
	}
	mconn.Close()

	// Two instance rows for the same task collapse to one stable row.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM result_outbox WHERE peer = 'k:pubB'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stable rows = %d, want 1 (err %v)", n, err)
	}
	for _, tbl := range []string{"cancel_outbox", "task_outbox", "artifact_push_outbox"} {
		if err := db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s WHERE peer = 'k:pubB'`, tbl)).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s stable rows = %d, want 1 (err %v)", tbl, n, err)
		}
	}
	// Instance-keyed rows for the keyed peer are gone; key-less and unknown
	// peers are untouched.
	if err := db.QueryRow(`SELECT count(*) FROM result_outbox WHERE peer IN ('b-old','b-new')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("instance rows left = %d (err %v)", n, err)
	}
	for _, peer := range []string{"keyless", "ghost"} {
		if err := db.QueryRow(`SELECT count(*) FROM result_outbox WHERE peer = ?`, peer).Scan(&n); err != nil || n != 1 {
			t.Fatalf("row for %s lost (n=%d, err=%v)", peer, n, err)
		}
	}
	// Idempotent: a second application changes nothing.
	mconn, err = db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	if err := migrateV36(migrationTx{mconn}); err != nil {
		mconn.Close()
		t.Fatalf("migrateV36 rerun: %v", err)
	}
	mconn.Close()
}

// V50 adds tasks.spawn_depth (sub-agent generation) with a 0 default so
// rows written before the column existed read as depth-0 roots; V51 adds
// the inherited spawn_cap bound beside it; V52 adds scope_paths_json — the
// declared project scope a delegated child carries across hops.
func TestMigrateV50TasksSpawnDepth(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, col := range []string{"spawn_depth", "spawn_cap", "scope_paths_json", "isolate"} {
		if !columnExists(t, db, "tasks", col) {
			t.Fatalf("tasks.%s missing after migrate", col)
		}
	}
	// A row inserted without the column set defaults to depth 0 (root).
	if _, err := db.Exec(`INSERT INTO tasks (task_id, state, owner_node, attempt_id) VALUES ('t-root', 'done', 'n1', 'a1')`); err != nil {
		t.Fatalf("insert legacy task: %v", err)
	}
	var depth int
	if err := db.QueryRow(`SELECT spawn_depth FROM tasks WHERE task_id='t-root'`).Scan(&depth); err != nil {
		t.Fatalf("read spawn_depth: %v", err)
	}
	if depth != 0 {
		t.Fatalf("legacy row spawn_depth = %d, want 0", depth)
	}
}

// V56 lands the cross-network mesh columns on employee_cache (design §6.2):
// the X25519 identity peers seal transit to, advertised dial addresses and
// relay services, plus the directory-gossip bookkeeping (learned_via,
// seen_at, content_hash) P2 diffs against.
func TestMigrateV56EmployeeTransitDir(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, col := range []string{"x_pub", "addrs", "relay", "learned_via", "seen_at", "content_hash"} {
		if !columnExists(t, db, "employee_cache", col) {
			t.Fatalf("employee_cache.%s missing after migrate", col)
		}
	}
	// Pre-existing rows read the documented defaults — an empty claim, not
	// a NULL that decode sites would have to special-case.
	if _, err := db.Exec(
		`INSERT INTO employee_cache (id, status, last_seen) VALUES ('legacy', 'offline', 0)`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	var xpub, addrs, relay, via string
	var seen int64
	if err := db.QueryRow(
		`SELECT x_pub, addrs, relay, learned_via, seen_at FROM employee_cache WHERE id='legacy'`).
		Scan(&xpub, &addrs, &relay, &via, &seen); err != nil {
		t.Fatalf("read legacy defaults: %v", err)
	}
	if xpub != "" || addrs != "" || relay != "" || via != "" || seen != 0 {
		t.Fatalf("legacy defaults = %q,%q,%q,%q,%d — want all-empty", xpub, addrs, relay, via, seen)
	}
	// Idempotent: a second run must not fail or duplicate the columns.
	if err := Migrate(db); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}

// V57 lands employee_cache.prov: the self-asserted build provenance
// (release | self | unsigned) stamped from hello/heartbeat. Pre-existing
// rows default to empty — "never reported" — distinct from an honest
// "unsigned" a new-format dev peer advertises.
func TestMigrateV57EmployeeProv(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !columnExists(t, db, "employee_cache", "prov") {
		t.Fatal("employee_cache.prov missing after migrate")
	}
	if _, err := db.Exec(
		`INSERT INTO employee_cache (id, status, last_seen) VALUES ('legacy', 'offline', 0)`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	var prov string
	if err := db.QueryRow(`SELECT prov FROM employee_cache WHERE id='legacy'`).Scan(&prov); err != nil {
		t.Fatalf("read legacy prov: %v", err)
	}
	if prov != "" {
		t.Fatalf("legacy prov = %q, want empty (never reported)", prov)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}

func columnExists(t *testing.T, db *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == col {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	return false
}
