// SPDX-License-Identifier: AGPL-3.0-or-later

package superagent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/storage"
)

const fixture = `{"action":"created","installation":{"id":1},"repository":{"id":2},"sender":{"id":3,"type":"User"},"issue":{"id":4,"number":5,"title":"original title"},"comment":{"id":6,"body":"fixture text"}}`

var source = githubSource{1, 2, "offline-test-key"}

func sign(s githubSource, body string) string {
	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func record(s githubSource, db *sql.DB, delivery, body string) error {
	return s.observe(context.Background(), db, "issue_comment", delivery, sign(s, body), []byte(body))
}

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func database(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "study.db")
	db := open(t, path)
	exec(t, db, schema)
	return db, path
}

func exec(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatal(err)
	}
}

func counts(t *testing.T, db *sql.DB, facts, deliveries, pending int) {
	t.Helper()
	var actual [3]int
	err := db.QueryRow(`SELECT (SELECT count(*) FROM facts), (SELECT count(*) FROM deliveries), (SELECT count(*) FROM pending_evaluations)`).Scan(&actual[0], &actual[1], &actual[2])
	if err != nil || actual != [3]int{facts, deliveries, pending} {
		t.Fatalf("facts/deliveries/pending=%v err=%v, want %v", actual, err, [3]int{facts, deliveries, pending})
	}
}

func TestSemanticReplay(t *testing.T) {
	db, path := database(t)
	for _, d := range []string{"d1", "d1", "d2"} {
		if err := record(source, db, d, fixture); err != nil {
			t.Fatal(err)
		}
	}
	counts(t, db, 1, 2, 1)
	// Acknowledged evaluation must not be recreated on replay after reopen.
	exec(t, db, `DELETE FROM pending_evaluations`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, path)
	if err := record(source, db, "d3", strings.ReplaceAll(fixture, "original title", "renamed")); err != nil {
		t.Fatal(err)
	}
	counts(t, db, 1, 3, 0)
	// Same text, different comment: independent, even in the same thread.
	if err := record(source, db, "d4", strings.ReplaceAll(fixture, `"id":6`, `"id":7`)); err != nil {
		t.Fatal(err)
	}
	counts(t, db, 2, 4, 1)
}

func TestConflictsNeverReplaceEvidence(t *testing.T) {
	db, _ := database(t)
	if err := record(source, db, "d1", fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ d, from, to string }{
		{"d1", "original title", "different raw bytes"},
		{"d1", `"id":6`, `"id":7`},
		{"new", "fixture text", "changed body"},
		{"new", `"id":3`, `"id":7`},
		{"new", `"id":4`, `"id":7`},
	} {
		if err := record(source, db, tc.d, strings.ReplaceAll(fixture, tc.from, tc.to)); !errors.Is(err, errConflict) {
			t.Fatalf("%+v: %v", tc, err)
		}
		counts(t, db, 1, 1, 1)
	}
	if err := record(source, db, "d1", fixture); err != nil {
		t.Fatal("original evidence lost", err)
	}
}

func TestSourceBoundary(t *testing.T) {
	db, _ := database(t)
	for _, tc := range []struct{ name, kind, d, body, sig string }{
		{"signature", "issue_comment", "d", fixture, "invalid"},
		{"missing delivery", "issue_comment", "", fixture, ""},
		{"other event", "issues", "d", fixture, ""},
		{"edited", "issue_comment", "d", strings.ReplaceAll(fixture, "created", "edited"), ""},
		{"wrong installation", "issue_comment", "d", strings.ReplaceAll(fixture, `"id":1`, `"id":10`), ""},
		{"wrong repository", "issue_comment", "d", strings.ReplaceAll(fixture, `"id":2`, `"id":20`), ""},
		{"missing actor id", "issue_comment", "d", strings.ReplaceAll(fixture, `"id":3`, `"login":"name"`), ""},
		{"missing comment id", "issue_comment", "d", strings.ReplaceAll(fixture, `"id":6`, `"id":0`), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sig := tc.sig
			if sig == "" {
				sig = sign(source, tc.body)
			}
			if err := source.observe(t.Context(), db, tc.kind, tc.d, sig, []byte(tc.body)); err == nil {
				t.Fatal("unexpected admission")
			}
			counts(t, db, 0, 0, 0)
		})
	}
	other := githubSource{10, 2, "another-fixture-key"}
	if err := record(source, db, "same", fixture); err != nil {
		t.Fatal(err)
	}
	if err := record(other, db, "same", strings.ReplaceAll(fixture, `"id":1`, `"id":10`)); err != nil {
		t.Fatal(err)
	}
	counts(t, db, 2, 2, 2)
}

func TestFailedEvaluationInsertCanRetryAfterReopen(t *testing.T) {
	db, path := database(t)
	exec(t, db, `CREATE TRIGGER fail BEFORE INSERT ON pending_evaluations BEGIN SELECT RAISE(ABORT, 'injected evaluation write failure'); END`)
	if err := record(source, db, "d1", fixture); err == nil || !strings.Contains(err.Error(), "injected evaluation write failure") {
		t.Fatalf("wrong failure: %v", err)
	}
	counts(t, db, 0, 0, 0)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, path)
	exec(t, db, `DROP TRIGGER fail`)
	if err := record(source, db, "d1", fixture); err != nil {
		t.Fatal(err)
	}
	counts(t, db, 1, 1, 1)
}

func TestConcurrentObservations(t *testing.T) {
	db, path := database(t)
	handles := []*sql.DB{db, open(t, path)}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			<-start
			if err := record(source, handles[i%2], fmt.Sprintf("d%d", (i/2)%4), fixture); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	counts(t, db, 1, 4, 1)
}
