// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskflow/finalaction"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/taskproviders/tsync"
	"github.com/Xustalis/OpenPanda/internal/util"
)

type WorkDelivery struct {
	Delivery  tsync.Delivery `json:"delivery"`
	AttemptID string         `json:"attemptId"`
	Files     []string       `json:"files"`
	Failure   string         `json:"failure"`
	SyncState string         `json:"syncState"`
}
type Action struct {
	ID            string                 `json:"actionId"`
	RequestID     string                 `json:"requestId"`
	DeliveryID    string                 `json:"deliveryId"`
	Revision      uint64                 `json:"deliveryRevision"`
	Fingerprint   string                 `json:"fingerprint"`
	Intent        finalaction.FileIntent `json:"intent"`
	State         string                 `json:"state"`
	Proof         tsync.SignedProof      `json:"-"`
	Receipt       *finalaction.Receipt   `json:"receipt,omitempty"`
	receiptStatus string
}
type WorkRun struct {
	RequestID  string         `json:"requestId"`
	TaskKey    string         `json:"taskKey"`
	State      string         `json:"state"`
	Error      string         `json:"error"`
	Prepared   string         `json:"prepared"`
	Deliveries []WorkDelivery `json:"deliveries"`
	Actions    []Action       `json:"actions"`
}

func (s *Service) delivery(ctx context.Context, id string, revision uint64) (WorkDelivery, error) {
	var out WorkDelivery
	var raw, hash, state string
	err := s.db.QueryRowContext(ctx, `SELECT delivery_json,payload_hash,state FROM work_deliveries WHERE delivery_id=? AND revision=?`, id, revision).Scan(&raw, &hash, &state)
	if err != nil {
		return out, err
	}
	if finalaction.Hash([]byte(raw)) != hash {
		return out, errors.New("delivery content changed")
	}
	if err = json.Unmarshal([]byte(raw), &out); err != nil {
		return out, err
	}
	out.SyncState = state
	return out, nil
}
func (s *Service) action(ctx context.Context, id string) (Action, error) {
	var a Action
	var raw, proof string
	err := s.db.QueryRowContext(ctx, `SELECT action_id,request_id,delivery_id,delivery_revision,fingerprint,intent_json,state,proof_json FROM final_actions WHERE action_id=?`, id).Scan(&a.ID, &a.RequestID, &a.DeliveryID, &a.Revision, &a.Fingerprint, &raw, &a.State, &proof)
	if err != nil {
		return a, err
	}
	if err = json.Unmarshal([]byte(raw), &a.Intent); err != nil {
		return a, err
	}
	if a.Intent.Fingerprint() != a.Fingerprint || a.Intent.ActionID != a.ID || a.Intent.RequestID != a.RequestID {
		return a, errors.New("final action intent changed")
	}
	if err = json.Unmarshal([]byte(proof), &a.Proof); err != nil {
		return a, err
	}
	var receipt string
	err = s.db.QueryRowContext(ctx, `SELECT receipt_json,report_status FROM action_receipts WHERE action_id=? AND verified=1`, a.ID).Scan(&receipt, &a.receiptStatus)
	if err == nil {
		a.Receipt = &finalaction.Receipt{}
		err = json.Unmarshal([]byte(receipt), a.Receipt)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return a, err
}

// Work reads only the current connected account's business history. Revoked
// accounts cannot retrieve private drafts or action bodies through old IDs.
func (s *Service) Work(ctx context.Context, request string) (WorkRun, error) {
	var out WorkRun
	f, err := s.flow(ctx, request)
	if err != nil {
		return out, err
	}
	c, err := s.Get(ctx, f.ConnectionID)
	if err != nil {
		return out, err
	}
	if c.State != "connected" || c.AccountID != f.AccountID || c.AuthEpoch != f.AuthEpoch || c.Generation != f.Generation {
		return out, taskproviders.ErrAuthorizationChanged
	}
	out = WorkRun{RequestID: f.ID, TaskKey: f.Key, State: f.State, Error: f.Error, Prepared: f.Prepared, Deliveries: []WorkDelivery{}, Actions: []Action{}}
	rows, err := s.db.QueryContext(ctx, `SELECT delivery_id,revision FROM work_deliveries WHERE request_id=? ORDER BY revision`, request)
	if err != nil {
		return out, err
	}
	type ref struct {
		id       string
		revision uint64
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err = rows.Scan(&r.id, &r.revision); err != nil {
			break
		}
		refs = append(refs, r)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	for _, r := range refs {
		d, e := s.delivery(ctx, r.id, r.revision)
		if e != nil {
			return out, e
		}
		out.Deliveries = append(out.Deliveries, d)
	}
	rows, err = s.db.QueryContext(ctx, `SELECT action_id FROM final_actions WHERE request_id=? ORDER BY created_at`, request)
	if err != nil {
		return out, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		a, e := s.action(ctx, id)
		if e != nil {
			return out, e
		}
		out.Actions = append(out.Actions, a)
	}
	return out, nil
}

func (w WorkRun) Export() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Request: %s\nState: %s\nFailure: %s\n\n", w.RequestID, w.State, w.Error)
	for _, d := range w.Deliveries {
		fmt.Fprintf(&b, "Delivery: %s revision %d (%s)\n%s\n", d.Delivery.DeliveryID, d.Delivery.Revision, d.SyncState, d.Delivery.Summary)
		for _, step := range d.Delivery.Steps {
			fmt.Fprintf(&b, "Step: %s — %s\n", step.Title, step.Outcome)
		}
		for _, file := range d.Files {
			fmt.Fprintf(&b, "File: %s\n", file)
		}
		for _, v := range d.Delivery.Verification {
			fmt.Fprintf(&b, "Verification: %s — %s (%s)\n", v.Status, v.Summary, v.Method)
		}
		for _, r := range d.Delivery.Remaining {
			fmt.Fprintf(&b, "Remaining: %s\n", r)
		}
		if d.Failure != "" {
			fmt.Fprintf(&b, "Failure: %s\n", d.Failure)
		}
	}
	for _, a := range w.Actions {
		fmt.Fprintf(&b, "Action: %s %s %s\n", a.ID, a.State, a.Fingerprint)
		if a.Receipt != nil {
			fmt.Fprintf(&b, "Receipt: %s %s (%s)\n", a.Receipt.Filename, a.Receipt.SHA256, a.Receipt.Method)
		}
	}
	return b.String()
}

func (m *managedExecution) ensureDelivery(ctx context.Context, f flowRun, c Connection) (WorkDelivery, error) {
	s := m.runtime.service
	var id string
	var revision uint64
	err := s.db.QueryRowContext(ctx, `SELECT delivery_id,revision FROM work_deliveries WHERE request_id=? ORDER BY revision DESC LIMIT 1`, f.ID).Scan(&id, &revision)
	if err == nil {
		return s.delivery(ctx, id, revision)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WorkDelivery{}, err
	}
	id, err = util.UUIDv7()
	if err != nil {
		return WorkDelivery{}, err
	}
	event, err := util.UUIDv7()
	if err != nil {
		return WorkDelivery{}, err
	}
	scope := f.Claim.Scope
	scope.FullGoal = f.Policy.FinalAction && f.Policy.Publication != nil && f.Policy.Publication.FullGoal
	if !scope.FullGoal && len(scope.Objectives) == 0 {
		scope.Objectives = []string{"Bounded preparation"}
	}
	remaining := []string{}
	if !scope.FullGoal {
		remaining = append(remaining, "Goal coverage has not been confirmed; review remaining work.")
	}
	if !f.Policy.FinalAction {
		remaining = append(remaining, "No final action is authorized; this is a preparation draft.")
	}
	d := WorkDelivery{AttemptID: f.PrepareAttempt, Files: []string{}, SyncState: "pending", Delivery: tsync.Delivery{
		DeliveryID: id, Revision: 1, SpaceID: f.Snapshot.Key.SpaceID, TaskID: f.Snapshot.Key.TaskID, EventID: event, RequestID: f.ID, ExecutionID: f.ExecutionID, Fence: f.Record.Fence,
		InputRevision: f.Input, AssignmentRevision: f.Assignment, Generation: f.Claim.Generation, Scope: scope, Summary: f.Prepared,
		Steps:        []tsync.DeliveryStep{{Title: "Prepare", Outcome: "Bounded tool-free preparation completed; final action and receipt are tracked separately."}},
		Verification: []tsync.Verification{{Status: "passed", Method: "sha256-payload", Summary: "Prepared payload is bound to this request and attempt.", EvidenceHashes: []string{finalaction.Hash([]byte(f.Prepared))}}},
		Remaining:    remaining, ArtifactRefs: []tsync.ArtifactRef{},
	}}
	var action *Action
	if f.Policy.FinalAction && f.Policy.Publication != nil {
		actionID, e := util.UUIDv7()
		if e != nil {
			return d, e
		}
		intent := finalaction.FileIntent{ActionID: actionID, RequestID: f.ID, AttemptID: f.PrepareAttempt, AccountID: c.AccountID, DeviceID: c.DeviceID, InputRevision: f.Input, AssignmentRevision: f.Assignment, Generation: f.Automation,
			Directory: f.Policy.Publication.Directory, Filename: strings.ReplaceAll(f.Policy.Publication.Filename, "{request}", f.ID), Content: f.Prepared, SHA256: finalaction.Hash([]byte(f.Prepared)), ExpiresAt: f.Policy.ExpiresAt}
		intent.DirectoryIdentity = f.Policy.Publication.DirectoryIdentity
		if err = intent.Validate(); err != nil {
			return d, err
		}
		action = &Action{ID: actionID, RequestID: f.ID, DeliveryID: id, Revision: 1, Fingerprint: intent.Fingerprint(), Intent: intent, State: "prepared"}
		d.Files = append(d.Files, intent.Directory+"/"+intent.Filename)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return d, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	err = s.write(ctx, m.owner, c, func(tx *sql.Tx) error {
		if err := s.requireActiveRun(ctx, tx, f.ID, m.owner); err != nil {
			return err
		}
		if err := s.automationSnapshotTx(ctx, tx, c, f.Policy, f.Snapshot); err != nil {
			return err
		}
		if time.Until(m.deadline) <= 0 || m.ctx.Err() != nil {
			return errors.New("execution lease expired")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO work_deliveries(delivery_id,revision,request_id,attempt_id,payload_hash,delivery_json,state,created_at) VALUES(?,1,?,?,?,?,'pending',?)`, id, f.ID, f.PrepareAttempt, finalaction.Hash(raw), string(raw), s.now().UnixMilli())
		if err != nil {
			return err
		}
		if action != nil {
			body, _ := json.Marshal(action.Intent)
			_, err = tx.ExecContext(ctx, `INSERT INTO final_actions(action_id,request_id,attempt_id,fingerprint,intent_json,state,expires_at,delivery_id,delivery_revision,created_at,updated_at) VALUES(?,?,?,?,?,'prepared',?,?,1,?,?)`, action.ID, f.ID, f.PrepareAttempt, action.Fingerprint, string(body), action.Intent.ExpiresAt, id, s.now().UnixMilli(), s.now().UnixMilli())
			if err != nil {
				return err
			}
		}
		return s.enqueueOutbox(ctx, tx, f, "delivery", d.Delivery.EventID, d.Delivery)
	})
	return d, err
}

func consentPayload(f flowRun, c Connection, d WorkDelivery, decision, nonce string, now time.Time) tsync.ConsentPayload {
	hash, _ := tsync.ScopeHash(d.Delivery.Scope)
	return tsync.ConsentPayload{AccountID: c.AccountID, DeviceID: c.DeviceID, ExecutionID: f.ExecutionID, DeliveryID: d.Delivery.DeliveryID, DeliveryRevision: d.Delivery.Revision, Decision: decision, InputRevision: f.Input, AssignmentRevision: f.Assignment, Generation: f.Claim.Generation, ScopeHash: hash,
		IssuedAt: now.UTC().Format("2006-01-02T15:04:05.000Z"), ExpiresAt: now.Add(5 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z"), Nonce: nonce}
}

// consentAuthority verifies the live device and versions independently of a
// core approval. It grants neither new execution nor any additional tools.
func (s *Service) consentAuthority(ctx context.Context, f flowRun, required ...string) (Connection, error) {
	c, err := s.Get(ctx, f.ConnectionID)
	if err != nil {
		return c, err
	}
	if c.State != "connected" || c.AccountID != f.AccountID || c.DeviceID != f.Claim.DeviceID || c.AuthEpoch != f.AuthEpoch || c.Generation != f.Generation {
		return c, taskproviders.ErrAuthorizationChanged
	}
	client, err := s.executionClient(c)
	if err != nil {
		return c, err
	}
	me, err := client.Me(ctx)
	if err != nil {
		return c, err
	}
	if me.Account.ID != c.AccountID || !me.Account.Active || me.Device == nil || me.Device.ID != c.DeviceID || me.Device.Revoked || fmt.Sprint(me.AuthEpoch) != c.AuthEpoch {
		return c, taskproviders.ErrAuthorizationChanged
	}
	c.Scopes = me.Scopes
	c.Spaces = nil
	for _, sp := range me.Spaces {
		if !sp.Archived {
			c.Spaces = append(c.Spaces, taskproviders.Space{SpaceID: sp.SpaceID, WorkspaceID: sp.WorkspaceID, Scopes: sp.Scopes})
		}
	}
	space, ok := authorizedSpace(c, f.Snapshot.Key.SpaceID)
	if !ok || space.WorkspaceID != f.Snapshot.Key.WorkspaceID {
		return c, taskproviders.ErrAuthorizationChanged
	}
	required = append(required, "tasks:read")
	for _, scope := range required {
		if !slices.Contains(c.Scopes, scope) || !slices.Contains(space.Scopes, scope) {
			return c, taskproviders.ErrAuthorizationChanged
		}
	}
	wire, err := client.Task(ctx, f.Snapshot.Key)
	if err != nil {
		return c, err
	}
	if wire.Key != f.Snapshot.Key || wire.Versions.Input != f.Input || wire.Versions.Assignment != f.Assignment || fmt.Sprint(wire.Versions.Generation) != f.Automation || !wire.Permissions.Read {
		return c, taskproviders.ErrAuthorizationChanged
	}
	return c, nil
}

// ApproveAction is the local user's specific final-submit consent. A changed
// fingerprint or delivery revision cannot reuse it. Mode after_execution uses
// the same immutable consent binding under explicit local preauthorization.
func (s *Service) ApproveAction(ctx context.Context, id, fingerprint string) error {
	a, err := s.action(ctx, id)
	if err != nil {
		return err
	}
	f, err := s.flow(ctx, a.RequestID)
	if err != nil {
		return err
	}
	if f.Policy.Mode != taskproviders.BeforeSubmit {
		return taskproviders.ErrUnsupported
	}
	return s.approveAction(ctx, f, a, fingerprint, false)
}
func (s *Service) approveAction(ctx context.Context, f flowRun, a Action, fingerprint string, preauthorized bool) error {
	if a.Fingerprint != fingerprint || a.Intent.ExpiresAt <= s.now().UnixMilli() || (a.State != "prepared" && a.State != "approved") {
		return taskproviders.ErrConflict
	}
	if err := a.Intent.Validate(); err != nil {
		return err
	}
	c, err := s.consentAuthority(ctx, f, "execution:report")
	if err != nil {
		return err
	}
	p, err := s.Policy(ctx, c.ID, f.Snapshot.Key.SpaceID)
	if err != nil {
		return err
	}
	if digest(p) != digest(f.Policy) || !p.FinalAction || p.validate(s.now()) != nil {
		return taskproviders.ErrAuthorizationChanged
	}
	if preauthorized && p.Mode != taskproviders.AfterExecution {
		return taskproviders.ErrUnsupported
	}
	d, err := s.delivery(ctx, a.DeliveryID, a.Revision)
	if err != nil {
		return err
	}
	nonce, err := util.UUIDv7()
	if err != nil {
		return err
	}
	proof, err := s.signConsent(ctx, c, consentPayload(f, c, d, "submit:"+a.ID+":"+fingerprint, nonce, s.now()))
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(proof)
	claim, err := util.UUIDv7()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE task_connections SET updated_at=updated_at WHERE connection_id=? AND account_id=? AND generation=? AND auth_epoch=? AND state='connected'`, c.ID, c.AccountID, c.Generation, c.AuthEpoch)
	if err == nil {
		err = requireOne(r)
	}
	if err != nil {
		return err
	}
	if err = s.automationSnapshotTx(ctx, tx, c, p, f.Snapshot); err != nil {
		return err
	}
	var runState string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM task_flow_runs WHERE request_id=?`, f.ID).Scan(&runState); err != nil {
		return err
	}
	if stopped(taskproviders.ExecutionState(runState)) {
		return taskproviders.ErrConflict
	}
	// Retries retain the original consent proof, event identity and expiry.
	// Once expired, another explicit local approval may renew this exact intent.
	var currentState, currentProof string
	if err = tx.QueryRowContext(ctx, `SELECT state,proof_json FROM final_actions WHERE action_id=? AND fingerprint=?`, a.ID, fingerprint).Scan(&currentState, &currentProof); err != nil {
		return err
	}
	if currentState != "prepared" && currentState != "approved" {
		return taskproviders.ErrConflict
	}
	if currentState == "approved" {
		var old tsync.SignedProof
		if json.Unmarshal([]byte(currentProof), &old) == nil {
			expires, parseErr := time.Parse(time.RFC3339Nano, old.Payload.ExpiresAt)
			var valid int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_records WHERE action_id=? AND fingerprint=? AND approver=? AND decision IN ('submit','preauthorized_submit') AND expires_at>?`, a.ID, fingerprint, c.AccountID, s.now().UnixMilli()).Scan(&valid); err != nil {
				return err
			}
			if parseErr == nil && expires.After(s.now()) && valid == 1 {
				return tx.Commit()
			}
		}
	}
	r, err = tx.ExecContext(ctx, `UPDATE final_actions SET state='approved',proof_json=?,updated_at=? WHERE action_id=? AND fingerprint=? AND state IN ('prepared','approved') AND EXISTS(SELECT 1 FROM work_deliveries d WHERE d.delivery_id=? AND d.revision=? AND d.revision=(SELECT MAX(revision) FROM work_deliveries WHERE delivery_id=d.delivery_id))`, string(raw), s.now().UnixMilli(), a.ID, fingerprint, a.DeliveryID, a.Revision)
	if err == nil {
		err = requireOne(r)
	}
	if err != nil {
		return err
	}
	decision := "submit"
	if preauthorized {
		decision = "preauthorized_submit"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO approval_records(approval_id,action_id,fingerprint,approver,decision,expires_at,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(action_id,fingerprint,approver,decision) DO UPDATE SET expires_at=excluded.expires_at`, nonce, a.ID, fingerprint, c.AccountID, decision, s.now().Add(5*time.Minute).UnixMilli(), s.now().UnixMilli())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_flow_runs SET state=CASE WHEN state IN ('awaiting_submit','awaiting_acceptance') THEN 'recovering' ELSE state END,claim_id=CASE WHEN state IN ('awaiting_submit','awaiting_acceptance') THEN ? ELSE claim_id END,next_at=0 WHERE request_id=? AND state NOT IN ('paused','cancelled','superseded','succeeded')`, claim, f.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
