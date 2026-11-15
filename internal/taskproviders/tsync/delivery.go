// SPDX-License-Identifier: AGPL-3.0-or-later

package tsync

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

type Verification struct {
	Status         string   `json:"status"`
	Method         string   `json:"method"`
	Summary        string   `json:"summary"`
	EvidenceHashes []string `json:"evidenceHashes"`
}
type DeliveryStep struct {
	Title   string `json:"title"`
	Outcome string `json:"outcome"`
}
type ArtifactRef struct {
	ArtifactID  string `json:"artifactId"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        uint64 `json:"size"`
	SHA256      string `json:"sha256"`
}
type Delivery struct {
	DeliveryID         string         `json:"deliveryId"`
	Revision           uint64         `json:"revision"`
	SpaceID            string         `json:"spaceId"`
	TaskID             string         `json:"taskId"`
	EventID            string         `json:"eventId"`
	RequestID          string         `json:"requestId"`
	ExecutionID        string         `json:"executionId"`
	Fence              Fence          `json:"fence"`
	CausedBy           *string        `json:"causedBy"`
	InputRevision      string         `json:"inputRevision"`
	AssignmentRevision string         `json:"assignmentRevision"`
	Generation         uint64         `json:"automationGeneration"`
	Scope              ExecutionScope `json:"scope"`
	Summary            string         `json:"summary"`
	Steps              []DeliveryStep `json:"steps"`
	Verification       []Verification `json:"verification"`
	Remaining          []string       `json:"remaining"`
	ArtifactRefs       []ArtifactRef  `json:"artifactRefs"`
}
type ConsentPayload struct {
	AccountID          string `json:"accountId"`
	DeviceID           string `json:"deviceId"`
	ExecutionID        string `json:"executionId"`
	DeliveryID         string `json:"deliveryId"`
	DeliveryRevision   uint64 `json:"deliveryRevision"`
	Decision           string `json:"decision"`
	InputRevision      string `json:"inputRevision"`
	AssignmentRevision string `json:"assignmentRevision"`
	Generation         uint64 `json:"automationGeneration"`
	ScopeHash          string `json:"scopeHash"`
	IssuedAt           string `json:"issuedAt"`
	ExpiresAt          string `json:"expiresAt"`
	Nonce              string `json:"nonce"`
}
type SignedProof struct {
	Payload   ConsentPayload `json:"payload"`
	Signature string         `json:"signature"`
}
type FinalAction struct {
	ActionID         string       `json:"actionId"`
	EventID          string       `json:"eventId"`
	RequestID        string       `json:"requestId"`
	ExecutionID      string       `json:"executionId"`
	Fence            Fence        `json:"fence"`
	CausedBy         *string      `json:"causedBy"`
	DeliveryID       string       `json:"deliveryId"`
	DeliveryRevision uint64       `json:"deliveryRevision"`
	Status           string       `json:"status"`
	Fingerprint      string       `json:"actionFingerprint"`
	ConsentProof     SignedProof  `json:"consentProof"`
	ReceiptHash      *string      `json:"receiptHash"`
	Verification     Verification `json:"verification"`
}
type DeliveryDecision struct {
	DecisionID   string       `json:"decisionId"`
	Revision     uint64       `json:"revision"`
	Decision     string       `json:"decision"`
	Reason       *string      `json:"reason"`
	ConsentProof *SignedProof `json:"consentProof"`
}
type DeliveryDecisionRecord struct {
	DecisionID         string       `json:"decisionId"`
	DeliveryID         string       `json:"deliveryId"`
	Revision           uint64       `json:"revision"`
	Decision           string       `json:"decision"`
	AccountID          string       `json:"approverAccountId"`
	DecidedAt          string       `json:"decidedAt"`
	Completed          bool         `json:"completedTask"`
	InputRevision      string       `json:"inputRevision"`
	AssignmentRevision string       `json:"assignmentRevision"`
	Generation         uint64       `json:"automationGeneration"`
	ScopeHash          string       `json:"scopeHash"`
	Reason             *string      `json:"reason"`
	ConsentProof       *SignedProof `json:"consentProof"`
}

func ConsentMessage(p ConsentPayload) ([]byte, error) {
	return JSONBytes([]any{p.AccountID, p.DeviceID, p.ExecutionID, p.DeliveryID, p.DeliveryRevision, p.Decision, p.InputRevision, p.AssignmentRevision, p.Generation, p.ScopeHash, p.IssuedAt, p.ExpiresAt, p.Nonce})
}
func ScopeHash(scope ExecutionScope) (string, error) {
	if scope.Objectives == nil {
		scope.Objectives = []string{}
	}
	b, err := JSONBytes(map[string]any{"fullGoal": scope.FullGoal, "objectives": scope.Objectives})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// SignConsent never exports the device seed. Only the local consent service
// calls it, after checking the live account, exact delivery and local authority.
func (s *DeviceStore) SignConsent(ctx context.Context, p ConsentPayload) (SignedProof, error) {
	var out SignedProof
	issued, err := time.Parse("2006-01-02T15:04:05.000Z", p.IssuedAt)
	if err != nil {
		return out, errors.New("canonical consent issue time required")
	}
	expires, err := time.Parse("2006-01-02T15:04:05.000Z", p.ExpiresAt)
	if err != nil || !expires.After(s.now()) || issued.After(s.now().Add(30*time.Second)) || !expires.After(issued) || expires.Sub(issued) > 10*time.Minute {
		return out, errors.New("consent expired or outside allowed lifetime")
	}
	if p.AccountID == "" || p.ExecutionID == "" || p.DeliveryID == "" || p.Nonce == "" || p.InputRevision == "" || p.AssignmentRevision == "" || p.DeliveryRevision == 0 || len(p.ScopeHash) != 64 {
		return out, errors.New("incomplete consent")
	}
	scope := "execution:report"
	if p.Decision == "accept" || p.Decision == "return" {
		scope = "delivery:report-decision"
	} else if !strings.HasPrefix(p.Decision, "submit:") {
		return out, errors.New("unsupported consent decision")
	}
	err = s.transaction(ctx, false, func(state *deviceState) error {
		if state.Reconnect || state.Grant != nil || state.Token == nil || state.Token.DeviceID != p.DeviceID || !slices.Contains(state.Scopes, scope) {
			return taskproviders.ErrReconnect
		}
		seed, err := hex.DecodeString(state.Seed)
		if err != nil {
			return err
		}
		message, err := ConsentMessage(p)
		if err != nil {
			return err
		}
		out = SignedProof{Payload: p, Signature: hex.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), message))}
		return nil
	})
	return out, err
}

func (c *Client) ReportDelivery(ctx context.Context, d Delivery) (Delivery, error) {
	var out Delivery
	err := c.request(ctx, "POST", executionPath(d.ExecutionID, "/deliveries"), nil, d, d.EventID, "", &out)
	return out, err
}
func (c *Client) ReportFinalAction(ctx context.Context, a FinalAction) (FinalAction, error) {
	var out FinalAction
	err := c.request(ctx, "POST", executionPath(a.ExecutionID, "/final-actions"), nil, a, a.EventID, "", &out)
	return out, err
}
func (c *Client) DecideDelivery(ctx context.Context, id string, d DeliveryDecision) (DeliveryDecisionRecord, error) {
	var out DeliveryDecisionRecord
	err := c.request(ctx, "POST", "v1/integration/deliveries/"+url.PathEscape(id)+"/decisions", nil, d, d.DecisionID, "", &out)
	return out, err
}
