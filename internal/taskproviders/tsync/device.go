// SPDX-License-Identifier: AGPL-3.0-or-later

package tsync

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/fslock"
	"github.com/Xustalis/OpenPanda/internal/taskproviders"
	"github.com/Xustalis/OpenPanda/internal/util"
)

type deviceGrant struct {
	DeviceCode           string `json:"device_code"`
	UserCode             string `json:"user_code"`
	VerificationURI      string `json:"verification_uri"`
	VerificationComplete string `json:"verification_uri_complete"`
	ExpiresIn            int64  `json:"expires_in"`
	Interval             int64  `json:"interval"`
	Challenge            string `json:"device_challenge"`
}

type deviceToken struct {
	Access    string `json:"access_token"`
	Type      string `json:"token_type"`
	ExpiresIn int64  `json:"expires_in"`
	Refresh   string `json:"refresh_token"`
	Scope     string `json:"scope"`
	DeviceID  string `json:"device_id"`
}

type deviceState struct {
	Version        int          `json:"version"`
	Root           string       `json:"root"`
	Seed           string       `json:"seed"`
	Grant          *deviceGrant `json:"grant,omitempty"`
	Token          *deviceToken `json:"token,omitempty"`
	Scopes         []string     `json:"scopes"`
	ExpiresAt      int64        `json:"expiresAt"`
	GrantExpiresAt int64        `json:"grantExpiresAt"`
	NextPollAt     int64        `json:"nextPollAt"`
	PendingRequest string       `json:"pendingRequest,omitempty"`
	PendingKind    string       `json:"pendingKind,omitempty"`
	PendingSentAt  int64        `json:"pendingSentAt,omitempty"`
	Reconnect      bool         `json:"reconnect,omitempty"`
}

// Authorization contains only the fields safe to show in the local UI. Device
// codes, signing keys and tokens remain in the credential file.
type Authorization struct {
	UserCode             string `json:"userCode"`
	VerificationURI      string `json:"verificationUri"`
	VerificationComplete string `json:"verificationUriComplete"`
	ExpiresAt            int64  `json:"expiresAt"`
	Interval             int64  `json:"interval"`
}

type OAuthError struct {
	Code       string
	RetryAfter time.Duration
}

func (e *OAuthError) Error() string { return "task device authorization: " + e.Code }
func (e *OAuthError) Is(target error) bool {
	return target == taskproviders.ErrReconnect && (e.Code == "access_denied" || e.Code == "expired_token" || e.Code == "invalid_grant" || e.Code == "invalid_token")
}

// DeviceStore serializes refresh rotation across processes with a crash-safe
// OS lock. Each transaction reloads the latest file; no process caches a token
// that another process may already have rotated.
type DeviceStore struct {
	path   string
	client *Client
	now    func() time.Time
}

func NewDeviceStore(path string, client *Client) (*DeviceStore, error) {
	if path == "" || client == nil {
		return nil, errors.New("device credential path and client required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return &DeviceStore{path: absolute, client: client, now: time.Now}, nil
}

func (s *DeviceStore) transaction(ctx context.Context, create bool, fn func(*deviceState) error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	parent, err := os.Lstat(filepath.Dir(s.path))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid device credential directory")
	}
	if runtime.GOOS != "windows" && parent.Mode().Perm()&0077 != 0 {
		return errors.New("device credential directory must have mode 0700")
	}
	release, err := fslock.AcquireStrict(ctx, s.path+".lock")
	if err != nil {
		return err
	}
	defer release()
	state := deviceState{}
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) && create {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		state = deviceState{Version: 1, Root: s.client.Root, Seed: hex.EncodeToString(key.Seed())}
		if err := s.save(&state); err != nil {
			return err
		}
	} else {
		if errors.Is(err, os.ErrNotExist) {
			return taskproviders.ErrReconnect
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
			return errors.New("device credentials require a regular private file")
		}
		b, err := os.ReadFile(s.path)
		if err != nil {
			return errors.New("cannot read device credentials")
		}
		if len(b) > 1<<20 || json.Unmarshal(b, &state) != nil {
			return errors.New("invalid device credentials")
		}
	}
	seed, err := hex.DecodeString(state.Seed)
	if state.Version != 1 || state.Root != s.client.Root || err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("device credentials do not match this connection")
	}
	// Read older credentials without letting a pending grant become a refresh.
	if state.PendingRequest != "" && state.PendingKind == "" {
		if state.Grant != nil {
			state.PendingKind = "device"
		} else {
			state.PendingKind = "refresh"
		}
	}
	if state.PendingKind != "" && state.PendingKind != "device" && state.PendingKind != "refresh" {
		return errors.New("invalid device authorization state")
	}
	return fn(&state)
}

func (s *DeviceStore) save(state *deviceState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return util.WriteFileAtomic(s.path, b, 0600)
}

var knownScopes = map[string]bool{"tasks:read": true, "tasks:write": true, "members:read": true, "artifacts:read": true, "artifacts:write": true, "execution:read": true, "execution:claim": true, "execution:report": true, "delivery:write": true, "automation:read": true, "delivery:report-decision": true}

func validateScopes(scopes []string) error {
	if len(scopes) == 0 {
		return errors.New("device scopes are required")
	}
	for _, scope := range scopes {
		if !knownScopes[scope] {
			return errors.New("unknown device scope")
		}
	}
	return nil
}

func sameScopes(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

func (s *DeviceStore) Begin(ctx context.Context, name string, scopes []string) (Authorization, error) {
	var result Authorization
	if err := validateScopes(scopes); err != nil {
		return result, err
	}
	err := s.transaction(ctx, true, func(state *deviceState) error {
		if state.Token != nil && state.Grant == nil && !state.Reconnect {
			return errors.New("device is already connected")
		}
		if state.Grant == nil || state.Reconnect || !sameScopes(state.Scopes, scopes) || state.GrantExpiresAt <= s.now().Unix() && state.PendingSentAt == 0 {
			caps, err := s.client.Capabilities(ctx)
			if err != nil {
				return err
			}
			if err := caps.Require("deviceFlow"); err != nil {
				return err
			}
			seed, _ := hex.DecodeString(state.Seed)
			key := ed25519.NewKeyFromSeed(seed)
			var grant deviceGrant
			err = s.client.oauth(ctx, "oauth/device/code", url.Values{"client_id": {ClientID}, "device_name": {name}, "scope": {strings.Join(scopes, " ")}, "public_key": {hex.EncodeToString(key.Public().(ed25519.PublicKey))}}, &grant)
			if err != nil {
				return err
			}
			if grant.DeviceCode == "" || grant.Challenge == "" || grant.UserCode == "" || grant.ExpiresIn <= 0 || grant.ExpiresIn > 86400 || grant.Interval < 1 || grant.Interval > 300 {
				return errors.New("invalid device grant")
			}
			// Authorization URLs are untrusted network input. The browser must
			// remain on this configured service and under its base path.
			if !s.client.authorizationURL(grant.VerificationURI) || grant.VerificationComplete != "" && !s.client.authorizationURL(grant.VerificationComplete) {
				return errors.New("device authorization URL differs from connection")
			}
			request, err := util.UUIDv7()
			if err != nil {
				return err
			}
			state.Grant = &grant
			state.Scopes = append([]string(nil), scopes...)
			state.PendingRequest = request
			state.PendingKind = "device"
			state.PendingSentAt = 0
			state.Token = nil
			state.ExpiresAt = 0
			state.GrantExpiresAt = s.now().Unix() + grant.ExpiresIn
			state.NextPollAt = s.now().Unix() + grant.Interval
			state.Reconnect = false
			if err := s.save(state); err != nil {
				return err
			}
		}
		g := state.Grant
		result = Authorization{UserCode: g.UserCode, VerificationURI: g.VerificationURI, VerificationComplete: g.VerificationComplete, ExpiresAt: state.GrantExpiresAt, Interval: g.Interval}
		return nil
	})
	return result, err
}

func (c *Client) authorizationURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil {
		return false
	}
	root, _ := url.Parse(c.Root)
	return u.Scheme == root.Scheme && u.Host == root.Host && u.User == nil && u.Fragment == "" && strings.HasPrefix(u.Path, strings.TrimRight(root.Path, "/")+"/")
}

func proofMessage(kind, challenge, sourceHash, requestID string) ([]byte, error) {
	if kind == "device" {
		return JSONBytes([]string{"tsync-device-v1", ClientID, challenge, sourceHash, requestID})
	}
	if kind == "refresh" {
		return JSONBytes([]string{"tsync-refresh-v1", ClientID, sourceHash, requestID})
	}
	return nil, errors.New("unknown device proof")
}

func (s *DeviceStore) exchange(ctx context.Context, state *deviceState, refresh bool) error {
	kind := "device"
	if refresh {
		kind = "refresh"
	}
	if state.PendingKind != "" && state.PendingKind != kind {
		return taskproviders.ErrReconnect
	}
	if refresh && (state.Grant != nil || state.Token == nil || state.Token.Refresh == "") {
		return taskproviders.ErrReconnect
	}
	if state.PendingRequest == "" {
		request, err := util.UUIDv7()
		if err != nil {
			return err
		}
		state.PendingRequest = request
	}
	state.PendingKind = kind
	if state.PendingSentAt == 0 {
		state.PendingSentAt = s.now().Unix()
	}
	// Persist the exact proof/request before network I/O, including on refresh.
	if err := s.save(state); err != nil {
		return err
	}
	form := url.Values{"client_id": {ClientID}, "request_id": {state.PendingRequest}}
	challenge, source := "", ""
	if refresh {
		if state.Token == nil || state.Token.Refresh == "" {
			return taskproviders.ErrReconnect
		}
		kind = "refresh"
		source = state.Token.Refresh
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", source)
	} else {
		if state.Grant == nil {
			return taskproviders.ErrReconnect
		}
		source = state.Grant.DeviceCode
		challenge = state.Grant.Challenge
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		form.Set("device_code", source)
	}
	hash := sha256.Sum256([]byte(source))
	message, _ := proofMessage(kind, challenge, hex.EncodeToString(hash[:]), state.PendingRequest)
	seed, _ := hex.DecodeString(state.Seed)
	form.Set("device_signature", hex.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), message)))
	var token deviceToken
	err := s.client.oauth(ctx, "oauth/token", form, &token)
	if err != nil {
		var problem *OAuthError
		if errors.As(err, &problem) {
			if problem.Code == "authorization_pending" || problem.Code == "slow_down" {
				state.PendingSentAt = 0
			}
			if problem.Code == "slow_down" && state.Grant != nil {
				state.Grant.Interval += 5
			}
			if !refresh && state.Grant != nil {
				state.NextPollAt = s.now().Unix() + state.Grant.Interval
				problem.RetryAfter = time.Duration(state.Grant.Interval) * time.Second
			}
		}
		if errors.Is(err, taskproviders.ErrReconnect) {
			state.Reconnect = true
			state.Grant = nil
			state.PendingRequest = ""
			state.PendingKind = ""
			state.PendingSentAt = 0
			state.Token = nil
		}
		if saveErr := s.save(state); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		return err
	}
	if token.Access == "" || token.Refresh == "" || !strings.EqualFold(token.Type, "Bearer") || token.DeviceID == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 365*86400 {
		return errors.New("invalid device token response")
	}
	if refresh && token.DeviceID != state.Token.DeviceID {
		return errors.New("device refresh changed identity")
	}
	allowed := map[string]bool{}
	previous := state.Scopes
	if refresh {
		previous = strings.Fields(state.Token.Scope)
	}
	for _, scope := range previous {
		allowed[scope] = true
	}
	if err := validateScopes(strings.Fields(token.Scope)); err != nil {
		return err
	}
	for _, scope := range strings.Fields(token.Scope) {
		if !allowed[scope] {
			return errors.New("device response expanded requested scopes")
		}
	}
	state.Token = &token
	state.Scopes = strings.Fields(token.Scope)
	state.ExpiresAt = s.now().Unix() + token.ExpiresIn
	state.Grant = nil
	state.PendingRequest = ""
	state.PendingKind = ""
	state.PendingSentAt = 0
	state.Reconnect = false
	return s.save(state)
}

func (s *DeviceStore) Exchange(ctx context.Context) error {
	return s.transaction(ctx, false, func(state *deviceState) error {
		if state.Token != nil && state.Grant == nil && !state.Reconnect {
			return nil
		}
		if state.Grant == nil || state.Reconnect {
			return taskproviders.ErrReconnect
		}
		// A possibly consumed request must be replayed even after the grant's
		// local expiry. An unsent/acknowledged-pending grant cannot be exchanged.
		if state.GrantExpiresAt <= s.now().Unix() && state.PendingSentAt == 0 {
			state.Reconnect = true
			if err := s.save(state); err != nil {
				return err
			}
			return &OAuthError{Code: "expired_token"}
		}
		if wait := state.NextPollAt - s.now().Unix(); wait > 0 {
			return &OAuthError{Code: "authorization_pending", RetryAfter: time.Duration(wait) * time.Second}
		}
		return s.exchange(ctx, state, false)
	})
}

func (s *DeviceStore) AccessToken(ctx context.Context) (string, error) {
	var result string
	err := s.transaction(ctx, false, func(state *deviceState) error {
		if state.Reconnect || state.Token == nil || state.Grant != nil || state.PendingKind == "device" {
			return taskproviders.ErrReconnect
		}
		if state.PendingRequest != "" || s.now().Unix()+60 >= state.ExpiresAt {
			if err := s.exchange(ctx, state, true); err != nil {
				return err
			}
		}
		result = state.Token.Access
		return nil
	})
	return result, err
}

// Disable is invoked on an authenticated 401. Automatic refresh must stop
// until the user explicitly reconnects, even if an old refresh token exists.
func (s *DeviceStore) Disable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.DisableContext(ctx)
}

// DisableContext also supports an explicit reconnect. Mark the old token
// unusable before requesting a new grant, including if that request fails.
func (s *DeviceStore) DisableContext(ctx context.Context) error {
	return s.transaction(ctx, false, func(state *deviceState) error {
		state.Reconnect = true
		state.Grant = nil
		state.PendingRequest, state.PendingKind, state.PendingSentAt = "", "", 0
		return s.save(state)
	})
}

func (s *DeviceStore) Disconnect(ctx context.Context) error {
	return s.transaction(ctx, false, func(state *deviceState) error {
		state.Reconnect = true
		if err := s.save(state); err != nil {
			return err
		}
		if state.Token != nil {
			if err := s.client.oauth(ctx, "oauth/revoke", url.Values{"client_id": {ClientID}, "token": {state.Token.Refresh}, "token_type_hint": {"refresh_token"}}, nil); err != nil {
				return err
			}
		}
		state.Token = nil
		state.Grant = nil
		state.PendingRequest = ""
		state.PendingKind = ""
		state.PendingSentAt = 0
		return s.save(state)
	})
}

func (c *Client) oauth(ctx context.Context, path string, form url.Values, out any) error {
	endpoint, err := taskproviders.Endpoint(c.Root, path)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "application/json")
	err = c.do(r, out)
	var problem *HTTPError
	if errors.As(err, &problem) {
		return &OAuthError{Code: problem.Code, RetryAfter: problem.RetryAfter}
	}
	return err
}
