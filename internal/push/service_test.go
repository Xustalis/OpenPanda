package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xenith/panda/internal/storage"
)

func newTestService(t *testing.T) (*Service, *Store) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db); err != nil {
		t.Fatal(err)
	}
	keys, err := GenerateVAPIDKeys("mailto:test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	return NewService(keys, store, slog.New(slog.DiscardHandler)), store
}

// testSubscription mints a valid subscriber identity (keypair + auth) and
// returns the private key plus a Subscription pointing at endpoint.
func testSubscription(t *testing.T, endpoint string) (*ecdh.PrivateKey, Subscription) {
	t.Helper()
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatal(err)
	}
	sub := Subscription{Endpoint: endpoint}
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes())
	sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(authSecret)
	return uaPriv, sub
}

func TestSubscribeRejectsMalformed(t *testing.T) {
	svc, _ := newTestService(t)
	bad := Subscription{Endpoint: "https://example.com/push/1"}
	bad.Keys.P256dh = base64.RawURLEncoding.EncodeToString([]byte("short"))
	bad.Keys.Auth = base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	if err := svc.Subscribe(context.Background(), bad); err == nil {
		t.Fatal("expected error for malformed p256dh")
	}
}

func TestNotifyEndToEnd(t *testing.T) {
	var received struct {
		authz string
		enc   string
		ttl   string
		body  []byte
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.authz = r.Header.Get("Authorization")
		received.enc = r.Header.Get("Content-Encoding")
		received.ttl = r.Header.Get("TTL")
		received.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	svc, _ := newTestService(t)
	svc.client = srv.Client() // trust the test server's self-signed cert
	uaPriv, sub := testSubscription(t, srv.URL+"/push/1")
	if err := svc.Subscribe(context.Background(), sub); err != nil {
		t.Fatal(err)
	}

	want := Notification{Title: "PANDA", Body: "needs review", ID: "t-1"}
	if err := svc.Notify(context.Background(), want); err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(received.authz, "vapid t=") || !strings.Contains(received.authz, ", k=") {
		t.Fatalf("authorization = %q", received.authz)
	}
	if received.enc != "aes128gcm" {
		t.Fatalf("content-encoding = %q, want aes128gcm", received.enc)
	}
	if received.ttl != "86400" {
		t.Fatalf("ttl = %q, want 86400", received.ttl)
	}

	pt, err := decryptForTest(received.body, uaPriv, uaPriv.PublicKey().Bytes(), decodeAuth(t, sub))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	var got Notification
	if err := json.Unmarshal(pt, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("notification = %+v, want %+v", got, want)
	}
}

func TestSendRemovesGoneSubscription(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	svc, store := newTestService(t)
	svc.client = srv.Client() // trust the test server's self-signed cert
	_, sub := testSubscription(t, srv.URL+"/push/gone")
	if err := svc.Subscribe(context.Background(), sub); err != nil {
		t.Fatal(err)
	}

	_ = svc.Notify(context.Background(), Notification{Title: "x", Body: "y", ID: "z"})

	subs, err := store.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 0 {
		t.Fatalf("gone subscription not removed, got %d", len(subs))
	}
}

func TestUnsubscribeRemoves(t *testing.T) {
	svc, store := newTestService(t)
	_, sub := testSubscription(t, "https://example.com/push/1")
	if err := svc.Subscribe(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if err := svc.Unsubscribe(context.Background(), sub.Endpoint); err != nil {
		t.Fatal(err)
	}
	subs, _ := store.All(context.Background())
	if len(subs) != 0 {
		t.Fatalf("subscription still present after unsubscribe")
	}
}

// TestSubscribeRejectsNonHTTPS verifies the endpoint validator (P2-4): only
// https endpoints with a host are accepted, so a leaked panel token cannot turn
// the daemon into an SSRF proxy against http:// metadata endpoints or file://.
func TestSubscribeRejectsNonHTTPS(t *testing.T) {
	svc, _ := newTestService(t)
	for _, ep := range []string{
		"http://169.254.169.254/latest/meta-data",
		"file:///etc/passwd",
		"https://", // hostless
	} {
		sub := Subscription{Endpoint: ep}
		sub.Keys.P256dh = "x"
		sub.Keys.Auth = "y"
		if err := svc.Subscribe(context.Background(), sub); err == nil {
			t.Fatalf("endpoint %q: expected validation error", ep)
		}
	}
}

func decodeAuth(t *testing.T, sub Subscription) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
