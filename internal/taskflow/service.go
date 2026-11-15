// SPDX-License-Identifier: AGPL-3.0-or-later

// Package taskflow owns deterministic task synchronization and execution
// coordination. It does not create a second scheduler or lend provider tokens
// to models, browsers, or mesh peers.
package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/fslock"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
	"github.com/Xustalis/OpenPanda/internal/util"
)

var ErrRuntimeHeld = errors.New("task synchronization runtime is owned by another process")
var ErrStale = errors.New("task connection changed during synchronization")
var connectionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,95}$`)

// Connection is safe for the local UI. The credential reference and generation
// are internal bookkeeping; tokens and signing material never enter SQLite.
type Connection struct {
	ID            string                     `json:"connectionId"`
	Provider      string                     `json:"provider"`
	Root          string                     `json:"rootUrl"`
	AccountID     string                     `json:"accountId"`
	InstanceID    string                     `json:"instanceId"`
	DeviceID      string                     `json:"deviceId"`
	AuthEpoch     string                     `json:"authEpoch"`
	State         string                     `json:"state"`
	FeedEpoch     string                     `json:"feedEpoch"`
	Capabilities  taskproviders.Capabilities `json:"capabilities"`
	Spaces        []taskproviders.Space      `json:"spaces"`
	Scopes        []string                   `json:"scopes"`
	LastError     string                     `json:"lastError"`
	SyncedAt      int64                      `json:"syncedAt"`
	NextSyncAt    int64                      `json:"nextSyncAt"`
	RequestedAt   int64                      `json:"requestedAt"`
	Failures      int                        `json:"failures"`
	Generation    int64                      `json:"-"`
	credentialRef string
}

type Service struct {
	db  *sql.DB
	dir string
	now func() time.Time
	// A fresh provider pins identity for each round. The seam also permits
	// deterministic fault tests without sending credentials anywhere.
	source func(Connection) (taskproviders.TaskProvider, func() error, error)
	runMu  sync.Mutex
	done   chan struct{}
	execMu sync.Mutex
	exec   *executionRuntime
	// executionClient never exposes a token to a task or a model.
	executionClient func(Connection) (*tsync.Client, error)
}

func New(db *sql.DB, dbPath string) *Service {
	dir := ""
	if dbPath != "" && dbPath != ":memory:" {
		absolute, err := filepath.Abs(dbPath)
		if err == nil {
			dir = absolute + ".task-connections"
		}
	}
	s := &Service{db: db, dir: dir, now: time.Now}
	s.executionClient = func(c Connection) (*tsync.Client, error) {
		client, device, err := s.device(c)
		if err == nil {
			client.Token = device.AccessToken
		}
		return client, err
	}
	s.source = func(c Connection) (taskproviders.TaskProvider, func() error, error) {
		client, device, err := s.device(c)
		if err != nil {
			return nil, nil, err
		}
		client.Token = device.AccessToken
		return tsync.NewProvider(client), device.Disable, nil
	}
	return s
}
func (s *Service) privateDir() error {
	if s.db == nil || s.dir == "" {
		return errors.New("task connections require a persistent database")
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(s.dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return errors.New("task credential directory must be private")
	}
	return nil
}
func (s *Service) device(c Connection) (*tsync.Client, *tsync.DeviceStore, error) {
	if err := s.privateDir(); err != nil {
		return nil, nil, err
	}
	if !connectionID.MatchString(c.ID) || c.credentialRef != c.ID+".json" {
		return nil, nil, errors.New("invalid task credential reference")
	}
	client, err := tsync.New(c.Root)
	if err != nil {
		return nil, nil, err
	}
	device, err := tsync.NewDeviceStore(filepath.Join(s.dir, c.credentialRef), client)
	return client, device, err
}

const connectionColumns = `connection_id,provider,root_url,account_id,instance_id,device_id,credential_ref,auth_epoch,state,feed_epoch,capabilities_json,spaces_json,scopes_json,last_error,synced_at,next_sync_at,requested_at,failures,generation`

func scanConnection(row interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	var caps, spaces, scopes string
	err := row.Scan(&c.ID, &c.Provider, &c.Root, &c.AccountID, &c.InstanceID, &c.DeviceID, &c.credentialRef, &c.AuthEpoch, &c.State, &c.FeedEpoch, &caps, &spaces, &scopes, &c.LastError, &c.SyncedAt, &c.NextSyncAt, &c.RequestedAt, &c.Failures, &c.Generation)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal([]byte(caps), &c.Capabilities); err != nil {
		return c, err
	}
	if err = json.Unmarshal([]byte(spaces), &c.Spaces); err != nil {
		return c, err
	}
	err = json.Unmarshal([]byte(scopes), &c.Scopes)
	return c, err
}
func (s *Service) Get(ctx context.Context, id string) (Connection, error) {
	return scanConnection(s.db.QueryRowContext(ctx, `SELECT `+connectionColumns+` FROM task_connections WHERE connection_id=?`, id))
}
func (s *Service) List(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+connectionColumns+` FROM task_connections ORDER BY connection_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Members reads only the directory for the current authorized subject. A
// revoked or replaced connection cannot expose a previous account's cache.
func (s *Service) Members(ctx context.Context, id, space string) ([]taskproviders.Member, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT d.directory_json FROM task_directories d JOIN task_connections c ON c.connection_id=d.connection_id AND c.account_id=d.account_id AND c.auth_epoch=d.auth_epoch WHERE c.connection_id=? AND c.state='connected' AND d.space_id=?`, id, space).Scan(&raw)
	if err != nil {
		return nil, err
	}
	out := []taskproviders.Member{}
	err = json.Unmarshal([]byte(raw), &out)
	return out, err
}
func (s *Service) lockConnection(ctx context.Context, id string) (func(), error) {
	if !connectionID.MatchString(id) {
		return nil, errors.New("invalid task connection ID")
	}
	if err := s.privateDir(); err != nil {
		return nil, err
	}
	return fslock.AcquireStrict(ctx, filepath.Join(s.dir, id+".connection.lock"))
}

type ConnectOptions struct {
	ID     string   `json:"connectionId"`
	Root   string   `json:"rootUrl"`
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}
type ConnectResult struct {
	Connection    Connection          `json:"connection"`
	Authorization tsync.Authorization `json:"authorization"`
}

func (s *Service) Connect(ctx context.Context, opts ConnectOptions) (ConnectResult, error) {
	if opts.ID == "" {
		var err error
		opts.ID, err = util.UUIDv7()
		if err != nil {
			return ConnectResult{}, err
		}
	}
	unlock, err := s.lockConnection(ctx, opts.ID)
	if err != nil {
		return ConnectResult{}, err
	}
	defer unlock()
	client, err := tsync.New(opts.Root)
	if err != nil {
		return ConnectResult{}, err
	}
	if len(opts.Scopes) == 0 {
		opts.Scopes = []string{"tasks:read", "members:read"}
	}
	if opts.Name == "" {
		opts.Name = "Panda"
	}
	c := Connection{ID: opts.ID, Provider: "tsync", Root: client.Root, credentialRef: opts.ID + ".json"}
	old, err := s.Get(ctx, c.ID)
	if err == nil && old.Root != c.Root {
		return ConnectResult{}, errors.New("reconnection cannot change service root")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ConnectResult{}, err
	}
	raw, _ := json.Marshal(opts.Scopes)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ConnectResult{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO task_connections(connection_id,provider,root_url,credential_ref,scopes_json,state,updated_at) VALUES(?,?,?,?,?,'authorizing',?) ON CONFLICT(connection_id) DO UPDATE SET state='authorizing',generation=generation+1,account_id='',device_id='',auth_epoch='',instance_id='',feed_epoch='',spaces_json='[]',capabilities_json='{}',scopes_json=excluded.scopes_json,last_error='',next_sync_at=0,failures=0,updated_at=excluded.updated_at`, c.ID, c.Provider, c.Root, c.credentialRef, string(raw), s.now().UnixMilli())
	if err == nil {
		err = clearConnection(ctx, tx, c.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return ConnectResult{}, err
	}
	_, device, err := s.device(c)
	if err != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE task_connections SET state='reconnect',last_error='credential_unavailable' WHERE connection_id=?`, c.ID)
		return ConnectResult{}, err
	}
	previous, requested := slices.Clone(old.Scopes), slices.Clone(opts.Scopes)
	slices.Sort(previous)
	slices.Sort(requested)
	if old.State != "authorizing" || !slices.Equal(previous, requested) {
		if err = device.DisableContext(ctx); err != nil && !errors.Is(err, taskproviders.ErrReconnect) {
			_, _ = s.db.ExecContext(ctx, `UPDATE task_connections SET state='reconnect',last_error='credential_unavailable' WHERE connection_id=?`, c.ID)
			return ConnectResult{}, err
		}
	}
	auth, err := device.Begin(ctx, opts.Name, opts.Scopes)
	if err != nil {
		// A failed initial grant must not leave an old token eligible for refresh.
		_ = device.Disable()
		_, _ = s.db.ExecContext(ctx, `UPDATE task_connections SET state='reconnect',last_error='authorization_failed' WHERE connection_id=?`, c.ID)
		return ConnectResult{}, err
	}
	c, err = s.Get(ctx, c.ID)
	return ConnectResult{Connection: c, Authorization: auth}, err
}
func (s *Service) FinishConnect(ctx context.Context, id string) (Connection, error) {
	unlock, err := s.lockConnection(ctx, id)
	if err != nil {
		return Connection{}, err
	}
	defer unlock()
	c, err := s.Get(ctx, id)
	if err != nil {
		return c, err
	}
	if c.State != "authorizing" {
		return c, ErrStale
	}
	_, device, err := s.device(c)
	if err == nil {
		err = device.Exchange(ctx)
	}
	if err != nil {
		return c, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE task_connections SET state='connected',requested_at=?,next_sync_at=0,last_error='',updated_at=? WHERE connection_id=? AND generation=? AND state='authorizing'`, s.now().UnixMilli(), s.now().UnixMilli(), id, c.Generation)
	if err == nil {
		err = requireOne(result)
	}
	if err != nil {
		return c, err
	}
	return s.Get(ctx, id)
}
func (s *Service) Disconnect(ctx context.Context, id string) error {
	unlock, err := s.lockConnection(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE task_connections SET state='disconnected',generation=generation+1,last_error='',spaces_json='[]',updated_at=? WHERE connection_id=?`, s.now().UnixMilli(), id)
	if err == nil {
		err = clearConnection(ctx, tx, id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return err
	}
	_, device, err := s.device(c)
	if err == nil {
		err = device.Disconnect(ctx)
	}
	if err != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE task_connections SET last_error='revoke_pending' WHERE connection_id=? AND state='disconnected'`, id)
	}
	return err
}
func (s *Service) RequestSync(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE task_connections SET requested_at=?,next_sync_at=0 WHERE connection_id=? AND state IN ('connected','authorizing','unsupported')`, s.now().UnixMilli(), id)
	if err != nil {
		return err
	}
	return requireOne(result)
}
func requireOne(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStale
	}
	return nil
}
func clearConnection(ctx context.Context, tx *sql.Tx, id string) error {
	if err := invalidateAutomation(ctx, tx, id, ""); err != nil {
		return err
	}
	for _, table := range []string{"task_snapshots", "task_directories", "task_space_cursors", "task_sync_events", "task_policies", "task_project_bindings"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE connection_id=?`, id); err != nil {
			return err
		}
	}
	return nil
}
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
