package skills

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestClassifyFetchURL covers the URL-level policy: https for anything that is
// not a loopback literal, loopback keeps http for local dev.
func TestClassifyFetchURL(t *testing.T) {
	cases := []struct {
		url    string
		policy fetchPolicy
		ok     bool
	}{
		{"https://example.com/SKILL.md", fetchPublic, true},
		{"https://raw.githubusercontent.com/o/r/main/SKILL.md", fetchPublic, true},
		{"http://example.com/SKILL.md", 0, false},             // plaintext to a remote host
		{"http://169.254.169.254/latest/meta-data", 0, false}, // metadata endpoint, plaintext
		{"https://192.168.1.1/x.md", fetchPublic, true},       // https classifies; the dialer blocks the IP
		{"http://127.0.0.1:8080/SKILL.md", fetchLoopback, true},
		{"http://localhost:8080/SKILL.md", fetchLoopback, true},
		{"http://[::1]:8080/SKILL.md", fetchLoopback, true},
		{"ftp://example.com/x.md", 0, false},
		{"https:///no-host", 0, false},
		{"file:///etc/passwd", 0, false},
	}
	for _, c := range cases {
		_, p, err := classifyFetchURL(c.url)
		if c.ok && err != nil {
			t.Errorf("%s: unexpected refusal: %v", c.url, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected refusal, got policy %d", c.url, p)
			continue
		}
		if c.ok && p != c.policy {
			t.Errorf("%s: policy = %d, want %d", c.url, p, c.policy)
		}
	}
}

// TestGuardedDialRefusesPrivate verifies the connect-time guard: under
// fetchPublic no resolved private/loopback/link-local address is dialed, and
// under fetchLoopback nothing else is — the DNS answer is checked at the
// moment the connection would open, so a rebind gains nothing.
func TestGuardedDialRefusesPrivate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))

	dialPub := guardedDialContext(fetchPublic)
	dialLoop := guardedDialContext(fetchLoopback)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	blocked := []string{
		"127.0.0.1:" + port,  // loopback under public policy
		"169.254.169.254:80", // cloud metadata
		"10.0.0.1:80",        // RFC1918
		"192.168.1.1:80",     // RFC1918
		"[fe80::1]:80",       // v6 link-local
		"[::1]:" + port,      // v6 loopback under public policy
		"0.0.0.0:80",         // unspecified
	}
	for _, addr := range blocked {
		if c, err := dialPub(ctx, "tcp", addr); err == nil {
			c.Close()
			t.Errorf("fetchPublic dialed %s — SSRF guard failed", addr)
		}
	}
	// Loopback policy reaches loopback and nothing else.
	c, err := dialLoop(ctx, "tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("fetchLoopback could not dial the test server: %v", err)
	}
	c.Close()
	if c, err := dialLoop(ctx, "tcp", "192.168.1.1:80"); err == nil {
		c.Close()
		t.Error("fetchLoopback dialed a private address")
	}
}

// TestImportURLRefusesPlaintextRemote: an http URL to a non-loopback host is
// refused before a single packet leaves — no DNS, no dial.
func TestImportURLRefusesPlaintextRemote(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, u := range []string{
		"http://example.com/SKILL.md",
		"http://169.254.169.254/latest/meta-data",
		"ftp://example.com/x.md",
		// Classifies as public-https, then the dialer refuses the private IP.
		"https://192.168.1.1/x.md",
		"https://10.0.0.1/x.md",
	} {
		if _, err := store.ImportURL(context.Background(), u, ImportOptions{}); err == nil {
			t.Errorf("ImportURL(%s) was not refused", u)
		}
	}
}

// TestImportURLLoopbackStillWorks keeps the dev workflow intact: a skill
// served over http on loopback imports fine.
func TestImportURLLoopbackStillWorks(t *testing.T) {
	body := "---\nname: local-skill\ndescription: d\n---\ncontent"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer ts.Close()
	store := NewStore(t.TempDir())
	sk, err := store.ImportURL(context.Background(), ts.URL+"/SKILL.md", ImportOptions{})
	if err != nil {
		t.Fatalf("loopback import refused: %v", err)
	}
	if len(sk) != 1 || sk[0].Name != "local-skill" {
		t.Fatalf("imported %v", sk)
	}
}

// TestImportURLRedirectAcrossPolicy verifies a loopback server cannot bounce
// the fetch outward: a redirect whose target classifies differently is
// refused.
func TestImportURLRedirectAcrossPolicy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/SKILL.md", http.StatusFound)
	}))
	defer ts.Close()
	store := NewStore(t.TempDir())
	_, err := store.ImportURL(context.Background(), ts.URL+"/SKILL.md", ImportOptions{})
	if err == nil || !strings.Contains(err.Error(), "policy") {
		t.Fatalf("cross-policy redirect not refused cleanly: %v", err)
	}
}
