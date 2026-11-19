// SPDX-License-Identifier: AGPL-3.0-or-later

// An offline implementation study. The application must never import it.
package superagent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/intake"
)

// A source here is specifically a github.com App installation and repository.
// Each test creates its own empty database, not a Panda migration.
const schema = `
CREATE TABLE facts (
  source TEXT NOT NULL, comment INTEGER NOT NULL, evidence TEXT NOT NULL,
  PRIMARY KEY(source, comment));
CREATE TABLE deliveries (
  source TEXT NOT NULL, delivery TEXT NOT NULL, comment INTEGER NOT NULL, raw_hash BLOB NOT NULL,
  PRIMARY KEY(source, delivery), FOREIGN KEY(source, comment) REFERENCES facts(source, comment));
CREATE TABLE pending_evaluations (
  source TEXT NOT NULL, comment INTEGER NOT NULL, PRIMARY KEY(source, comment),
  FOREIGN KEY(source, comment) REFERENCES facts(source, comment));`

var errConflict = errors.New("delivery or immutable comment fact conflicts with recorded evidence")

type identity struct {
	ID int64 `json:"id"`
}

type githubSource struct {
	installation, repo int64
	secret             string
}

// observe only models issue_comment.created; it does not grant the actor
// permission or schedule work. Keep only the body digest for this experiment.
func (s githubSource) observe(ctx context.Context, db *sql.DB, kind, delivery, signature string, body []byte) error {
	if kind != "issue_comment" || delivery == "" || delivery != strings.TrimSpace(delivery) || len(delivery) > 128 || len(body) > 1<<20 || !intake.VerifyGitHubSignature(s.secret, body, signature) {
		return errors.New("missing or invalid delivery evidence")
	}
	var p struct {
		Action                   string
		Installation, Repository identity
		Sender                   struct {
			ID   int64
			Type string
		}
		Issue   struct{ ID, Number int64 }
		Comment struct {
			ID   int64
			Body *string
		}
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return err
	}
	if p.Action != "created" || s.installation <= 0 || s.repo <= 0 || p.Installation.ID != s.installation || p.Repository.ID != s.repo || p.Issue.ID <= 0 || p.Issue.Number <= 0 || p.Comment.ID <= 0 || p.Comment.Body == nil || p.Sender.ID <= 0 || p.Sender.Type == "" {
		return errors.New("unrecognized source or incomplete created comment")
	}
	source := fmt.Sprintf("github.com/app/%d/repo/%d", s.installation, s.repo)
	// Login, display name, issue title and serialization can change across
	// observations. They are not the identity or evidence of the create action.
	evidence, err := json.Marshal(struct {
		Issue, Number, Sender  int64
		SenderType, BodySHA256 string
	}{p.Issue.ID, p.Issue.Number, p.Sender.ID, p.Sender.Type, fmt.Sprintf("%x", sha256.Sum256([]byte(*p.Comment.Body)))})
	if err != nil {
		return err
	}
	raw := sha256.Sum256(body)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A conditional conflict update is deliberately avoided: retries must
	// never rewrite the first evidence. All three records commit together.
	res, err := tx.ExecContext(ctx, `INSERT INTO facts VALUES(?,?,?) ON CONFLICT(source,comment) DO NOTHING`, source, p.Comment.ID, string(evidence))
	if err != nil {
		return err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT evidence FROM facts WHERE source=? AND comment=?`, source, p.Comment.ID).Scan(&old); err != nil {
			return err
		}
		if old != string(evidence) {
			return errConflict
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries VALUES(?,?,?,?) ON CONFLICT(source,delivery) DO NOTHING`, source, delivery, p.Comment.ID, raw[:]); err != nil {
		return err
	}
	var matches bool
	if err := tx.QueryRowContext(ctx, `SELECT comment=? AND raw_hash=? FROM deliveries WHERE source=? AND delivery=?`, p.Comment.ID, raw[:], source, delivery).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return errConflict
	}
	if inserted == 1 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pending_evaluations VALUES(?,?)`, source, p.Comment.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
