// SPDX-License-Identifier: AGPL-3.0-or-later

// Package configsync coordinates model defaults across processes sharing a
// config and database. Polling is intentional: it also works on Windows and
// after atomic file replacement, without signalling an unverified PID.
package configsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

const Interval = time.Second
const statusPrefix = "runtime_model:"

// Status is an acknowledgement of a specific on-disk revision, not a claim
// that already-running attempts have switched models. No credentials or
// raw errors (which may contain credentials) are persisted here.
type Status struct {
	PID      int    `json:"pid"`
	Role     string `json:"role"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Revision string `json:"revision"`
	Applied  bool   `json:"applied"`
	Code     string `json:"code,omitempty"`
	SeenAt   int64  `json:"seen_at"`
}

type Report struct {
	Revision  string   `json:"revision"`
	Processes []Status `json:"processes"`
}

type Watcher struct {
	mu         sync.Mutex
	modelOnly  bool
	db         *sql.DB
	path, role string
	current    func() config.RuntimeConfig
	apply      func(config.RuntimeConfig) error
}

func New(db *sql.DB, path, role string, current func() config.ModelConfig, apply func(config.ModelConfig) error) *Watcher {
	w := NewRuntime(db, path, role,
		func() config.RuntimeConfig { return config.RuntimeConfig{Model: current()} },
		func(c config.RuntimeConfig) error { return apply(c.Model) })
	w.modelOnly = true
	return w
}

// NewRuntime observes model, per-agent selections and delegation limits using
// the same file load and acknowledgement as model-only observers.
func NewRuntime(db *sql.DB, path, role string, current func() config.RuntimeConfig, apply func(config.RuntimeConfig) error) *Watcher {
	return &Watcher{db: db, path: path, role: role, current: current, apply: apply}
}

func revision(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func pathKey(path string) string {
	abs, _ := filepath.Abs(path)
	return revision([]byte(filepath.Clean(abs)))
}

func (w *Watcher) key() string {
	return statusPrefix + pathKey(w.path) + ":" + strconv.Itoa(os.Getpid()) + ":" + w.role
}

// Sync makes one bounded observation. Missing/deleted files never mean
// "clear": only an explicitly persisted empty model can revoke credentials.
// A rejected/partially-written config leaves the last good runtime intact.
// Sync is serialized with background polling and pre-execution refresh.
func (w *Watcher) Sync(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}
	st := Status{PID: os.Getpid(), Role: w.role, Revision: revision(raw), SeenAt: time.Now().Unix()}
	cfg, loadErr := config.Load(w.path)
	after, readErr := os.ReadFile(w.path)
	if readErr != nil || !bytes.Equal(raw, after) {
		return errors.New("model config changed during load")
	}
	var desired config.RuntimeConfig
	if cfg != nil {
		desired = cfg.RuntimeConfig()
	}
	if w.modelOnly && cfg != nil {
		desired = config.RuntimeConfig{Model: cfg.Model}
	}
	if loadErr != nil {
		st.Code = "config_invalid"
	} else if !reflect.DeepEqual(w.current(), desired) {
		if err := w.apply(desired); err != nil {
			st.Code = "apply_failed"
		} else {
			st.Applied = true
		}
	} else {
		st.Applied = true
	}
	if loadErr == nil {
		st.Provider = cfg.Model.Provider
		st.Model = cfg.Model.Model
	}
	data, _ := json.Marshal(st)
	if w.db != nil {
		if _, err := w.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, w.key(), string(data)); err != nil {
			return err
		}
	}
	if st.Code != "" {
		return errors.New(st.Code)
	}
	return nil
}

func (w *Watcher) Run(ctx context.Context) {
	defer func() {
		if w.db != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, _ = w.db.ExecContext(cleanup, `DELETE FROM settings WHERE key=?`, w.key())
		}
	}()
	tick := time.NewTicker(Interval)
	defer tick.Stop()
	for {
		_ = w.Sync(ctx) // persistent secret-free status exposes rejection; retry on next tick
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// ReadReport returns only live acknowledgements. A different revision is
// visibly pending, never falsely reported as a successful model switch.
func ReadReport(ctx context.Context, db *sql.DB, path string) (Report, error) {
	out := Report{Processes: []Status{}}
	if path == "" {
		return out, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	out.Revision = revision(raw)
	if db == nil {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT value FROM settings WHERE key LIKE ? ORDER BY key`, statusPrefix+pathKey(path)+":%")
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return out, err
		}
		var st Status
		if json.Unmarshal([]byte(raw), &st) != nil || st.SeenAt < time.Now().Add(-5*Interval).Unix() {
			continue
		}
		st.Applied = st.Applied && st.Revision == out.Revision
		out.Processes = append(out.Processes, st)
	}
	return out, rows.Err()
}
