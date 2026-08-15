package push

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// Subscription is a browser PushSubscription as serialized by
// PushSubscription.toJSON(): the delivery endpoint plus the ECDH public key and
// auth secret needed to encrypt messages for that device.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Notification is the JSON payload delivered to the browser. The service worker
// reads title/body/id to render and group the notification (webui/web/pwa/sw.js).
// Icon and Badge carry the PWA icon URLs so a push shows the app mark even on
// a platform that does not render the manifest icon automatically (P2-17).
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	ID    string `json:"id"`
	Icon  string `json:"icon,omitempty"`
	Badge string `json:"badge,omitempty"`
}

// Store persists push subscriptions in SQLite, one row per device.
type Store struct {
	db *sql.DB
}

// NewStore wraps db for subscription CRUD. The table is created by storage.Migrate.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Add upserts a subscription keyed by endpoint, so a browser that re-subscribes
// (e.g. after a permission change) replaces its old keys in place.
func (s *Store) Add(ctx context.Context, sub Subscription) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO push_subscriptions (endpoint, p256dh, auth, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh=excluded.p256dh, auth=excluded.auth`,
		sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("add push subscription: %w", err)
	}
	return nil
}

// Remove deletes the subscription for endpoint.
func (s *Store) Remove(ctx context.Context, endpoint string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	if err != nil {
		return fmt.Errorf("remove push subscription: %w", err)
	}
	return nil
}

// All lists every stored subscription, oldest first.
func (s *Store) All(ctx context.Context) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT endpoint, p256dh, auth FROM push_subscriptions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var subs []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.Endpoint, &sub.Keys.P256dh, &sub.Keys.Auth); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

// Service owns the VAPID identity, the subscription store, and delivery.
type Service struct {
	keys   *VAPIDKeys
	store  *Store
	client *http.Client
	logger *slog.Logger
}

// NewService builds a push Service. logger may be nil.
func NewService(keys *VAPIDKeys, store *Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		keys:   keys,
		store:  store,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: logger,
	}
}

// PublicKey returns the base64url applicationServerKey the browser subscribes with.
func (s *Service) PublicKey() string { return s.keys.PublicKey() }

// Subscribe validates and stores a browser subscription.
func (s *Service) Subscribe(ctx context.Context, sub Subscription) error {
	if sub.Endpoint == "" || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		return errors.New("invalid subscription: endpoint, p256dh and auth are required")
	}
	if err := validateEndpoint(sub.Endpoint); err != nil {
		return err
	}
	// Validate the keys now so a malformed subscription fails at subscribe
	// time rather than silently at send time.
	if _, err := decodeKey(sub.Keys.P256dh, 65); err != nil {
		return fmt.Errorf("subscription p256dh: %w", err)
	}
	if _, err := decodeKey(sub.Keys.Auth, 16); err != nil {
		return fmt.Errorf("subscription auth: %w", err)
	}
	return s.store.Add(ctx, sub)
}

// Unsubscribe removes the subscription for endpoint.
func (s *Service) Unsubscribe(ctx context.Context, endpoint string) error {
	return s.store.Remove(ctx, endpoint)
}

// Notify broadcasts one notification to every subscription. Per-device failures
// are logged, not propagated: a single dead endpoint must not suppress pushes
// to healthy devices.
func (s *Service) Notify(ctx context.Context, n Notification) error {
	subs, err := s.store.All(ctx)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if err := s.send(ctx, sub, n); err != nil {
			s.logger.Warn("push send failed", "endpoint", sub.Endpoint, "err", err)
		}
	}
	return nil
}

// send encrypts and delivers one notification to one subscription.
func (s *Service) send(ctx context.Context, sub Subscription, n Notification) error {
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	uaPublic, err := decodeKey(sub.Keys.P256dh, 65)
	if err != nil {
		return err
	}
	authSecret, err := decodeKey(sub.Keys.Auth, 16)
	if err != nil {
		return err
	}
	body, err := encrypt(uaPublic, authSecret, payload)
	if err != nil {
		return err
	}

	authz, err := s.keys.authorization(origin(sub.Endpoint), time.Now().Unix())
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authz)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "86400")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
		// The subscription is gone (uninstalled/revoked): drop it so we stop
		// pushing to a dead endpoint. Best-effort; the request ctx may already
		// be done, so use a detached one.
		_ = s.store.Remove(context.Background(), sub.Endpoint)
		return fmt.Errorf("subscription gone: %s", resp.Status)
	default:
		return fmt.Errorf("push service %s", resp.Status)
	}
}

// decodeKey decodes a base64url key of exactly want bytes, tolerating either
// padded or unpadded input.
func decodeKey(s string, want int) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.URLEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, errors.New("invalid base64url")
	}
	if len(b) != want {
		return nil, fmt.Errorf("key length %d, want %d", len(b), want)
	}
	return b, nil
}

// origin extracts scheme://host from a push endpoint, which VAPID requires as
// the JWT audience.
func origin(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// validateEndpoint rejects non-HTTPS or hostless push endpoints (P2-4). The
// browser's push service is always https://; accepting an arbitrary scheme would
// let a leaked panel token turn the daemon into an SSRF proxy (e.g. an
// http://169.254.169.254/... metadata endpoint).
func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("subscription endpoint: invalid URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("subscription endpoint: scheme must be https")
	}
	if u.Host == "" {
		return fmt.Errorf("subscription endpoint: missing host")
	}
	return nil
}
