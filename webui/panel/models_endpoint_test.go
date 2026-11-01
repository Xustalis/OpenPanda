// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// doRaw runs a request and returns the status plus the raw body, for the
// endpoints that answer an error with plain text (http.Error) rather than
// a JSON envelope.
func doRaw(t *testing.T, h http.Handler, req *http.Request) (int, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code, rr.Body.String()
}

// The console takes a model endpoint from the request body and the daemon
// then dials it. A caller holding the panel token could aim that round trip
// at the instance metadata service (169.254.169.254) and read the answer
// back through the console's own response — a token leak would become a
// cloud-credential leak, with the daemon as the confused deputy.
func TestCustomModelEndpointRefusesMetadataAddress(t *testing.T) {
	h, _, _ := newEnginePanel(t)

	for _, base := range []string{
		"https://169.254.169.254/latest/meta-data/",
		"http://169.254.169.254/",
		"https://100.100.100.200/latest/meta-data/",
	} {
		body := `{"provider":"custom","base_url":"` + base + `","model":"m"}`
		code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models", body))
		if code != http.StatusBadRequest {
			t.Fatalf("add model with base_url %s: status = %d, want 400 (out %v)", base, code, out)
		}
	}
}

func TestCustomModelEndpointRefusesMetadataAddressOnFetch(t *testing.T) {
	h, _, _ := newEnginePanel(t)

	body := `{"provider":"custom","base_url":"https://169.254.169.254/latest/meta-data/","model":"m"}`
	code, raw := doRaw(t, h, jsonReq(http.MethodPost, "/api/models/fetch", body))
	if code != http.StatusBadRequest {
		t.Fatalf("fetch with a metadata base_url: status = %d, want 400 (body %s)", code, raw)
	}
	if !strings.Contains(raw, "metadata") {
		t.Fatalf("body = %q, want it to name the metadata address", raw)
	}
}

// A self-hosted model on the LAN is a normal setup — a private address stays
// allowed. What is refused is the credential service, not the home network.
//
// The check is exercised directly rather than through the endpoint: the
// network guard in internal/entry independently requires https for any
// non-loopback address, so an http:// LAN endpoint is refused by that rule
// and not by this one.
func TestCustomModelEndpointAllowsPrivateAddress(t *testing.T) {
	for _, base := range []string{
		"http://192.168.1.50:11434/v1",
		"https://10.0.0.7:8080/v1",
		"http://127.0.0.1:11434/v1",
	} {
		if err := checkCustomBaseURL(base); err != nil {
			t.Fatalf("checkCustomBaseURL(%q) = %v, want nil", base, err)
		}
	}
	for _, base := range []string{
		"https://169.254.169.254/latest/meta-data/",
		"http://169.254.169.254/",
		"https://100.100.100.200/",
		"https://fe80::1/",
	} {
		if err := checkCustomBaseURL(base); err == nil {
			t.Fatalf("checkCustomBaseURL(%q) = nil, want a refusal", base)
		}
	}
	// A malformed or schemeless endpoint is refused too — it would fail
	// later anyway, but failing here keeps the reason clear.
	for _, base := range []string{"", "ftp://example.com", "not a url", "https://"} {
		if err := checkCustomBaseURL(base); err == nil {
			t.Fatalf("checkCustomBaseURL(%q) = nil, want a refusal", base)
		}
	}
}

// A metadata address must never reach the config file either: the endpoint
// is persisted there and dialed later by the daemon.
func TestCustomModelEndpointNeverPersisted(t *testing.T) {
	h, _, configPath := newEnginePanel(t)

	body := `{"provider":"custom","base_url":"https://169.254.169.254/","model":"leaky-model"}`
	code, _ := doJSON(t, h, jsonReq(http.MethodPost, "/api/models", body))
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "169.254.169.254") {
		t.Fatalf("the refused endpoint reached the config file: %q", data)
	}
}

// The console can never show a saved secret, so "fetch the model list" on an
// endpoint that is already on file arrives keyless and used to die with
// "no model api key configured" before it dialed. The resolver borrows the
// stored key — matched on the endpoint itself, never across hosts: a
// credential belongs to the host that issued it.
func TestResolveModelRefBorrowsKeyForTheSameEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.Model = config.ModelConfig{
		Name: "relay", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "http://127.0.0.1:3065/v1", Model: "sn-flash", APIKey: "sk-relay",
	}
	panel := &handler{cfg: cfg, cfgMu: &sync.RWMutex{}}

	got, err := panel.resolveModelRefErr(modelRefRequest{
		Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "http://127.0.0.1:3065/v1/", Model: "sn-flash",
	})
	if err != nil {
		t.Fatalf("resolve same endpoint: %v", err)
	}
	if got.APIKey != "sk-relay" {
		t.Fatalf("key = %q, want the stored key for this endpoint", got.APIKey)
	}

	// The same relay under another host is a different account: sending the
	// key there would be a credential leak, so it stays empty.
	other, err := panel.resolveModelRefErr(modelRefRequest{
		Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://elsewhere.example/v1", Model: "sn-flash",
	})
	if err != nil {
		t.Fatalf("resolve other endpoint: %v", err)
	}
	if other.APIKey != "" {
		t.Fatalf("key leaked to a different host: %q", other.APIKey)
	}
}

// A copy that names its source gets the source's key even when the endpoint
// spelling differs — the caller said "this one", so the lookup is by alias.
func TestResolveModelRefCopyFromSuppliesTheKey(t *testing.T) {
	cfg := config.Default()
	cfg.Models = []config.ModelConfig{{
		Name: "relay", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "http://127.0.0.1:3065/v1", Model: "sn-flash", APIKey: "sk-relay",
	}}
	panel := &handler{cfg: cfg, cfgMu: &sync.RWMutex{}}

	got, err := panel.resolveModelRefErr(modelRefRequest{
		Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "http://127.0.0.1:3999/v1", Model: "other-model",
		CopyFrom: "relay",
	})
	if err != nil {
		t.Fatalf("resolve copy: %v", err)
	}
	if got.APIKey != "sk-relay" {
		t.Fatalf("key = %q, want the source's key", got.APIKey)
	}
}

// Extending a saved custom relay must keep its key. The Add tile prefills the
// row and submits without a key, and the client sends reuse_from; the custom
// branch used to ignore it, persist NoAuth: true and toast "saved" — the user
// ended up with a keyless duplicate of a working entry.
func TestAddModelExtendsCustomRelayKeepsItsKey(t *testing.T) {
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Models = []config.ModelConfig{{
			Name: "relay", Provider: "custom", APIType: config.APITypeOpenAI,
			BaseURL: "http://127.0.0.1:3065/v1", Model: "m-1", APIKey: "sk-source",
		}}
	})

	body := `{"provider":"custom","api_type":"openai","base_url":"http://127.0.0.1:3065/v1",` +
		`"model":"m-2","alias":"relay-2","reuse_from":"relay"}`
	if code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models", body)); code != http.StatusOK {
		t.Fatalf("extend: status = %d, want 200 (out %v)", code, out)
	}

	cfg := readConfig(t, path)
	for _, m := range cfg.Models {
		if m.Alias() != "relay-2" {
			continue
		}
		if m.APIKey != "sk-source" {
			t.Fatalf("key = %q, want the source relay's key", m.APIKey)
		}
		if m.NoAuth {
			t.Fatal("extended relay was persisted as no-auth")
		}
		return
	}
	t.Fatal("relay-2 was not persisted")
}

// "Duplicate" is one click in the console, and the row it lands must be a
// working entry: the secret, the raw params/headers and the plaintext waiver
// are exactly what the page can never re-send, so copy_from carries them.
func TestAddModelCopyCarriesCredentialAndOverrides(t *testing.T) {
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Models = []config.ModelConfig{{
			Name: "relay", Provider: "custom", APIType: config.APITypeOpenAI,
			BaseURL: "http://127.0.0.1:3065/v1", Model: "m-1", APIKey: "sk-source",
			Params:  map[string]any{"temperature": 0.3},
			Headers: map[string]string{"X-Tenant": "blue"},
		}}
	})

	body := `{"provider":"custom","api_type":"openai","base_url":"http://127.0.0.1:3065/v1",` +
		`"model":"m-2","alias":"relay-copy","copy_from":"relay"}`
	if code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models", body)); code != http.StatusOK {
		t.Fatalf("copy: status = %d, want 200 (out %v)", code, out)
	}

	cfg := readConfig(t, path)
	var copyEntry, source *config.ModelConfig
	for i := range cfg.Models {
		switch cfg.Models[i].Alias() {
		case "relay-copy":
			copyEntry = &cfg.Models[i]
		case "relay":
			source = &cfg.Models[i]
		}
	}
	if copyEntry == nil {
		t.Fatalf("copy entry missing from config: %+v", cfg.Models)
	}
	if copyEntry.Model != "m-2" {
		t.Fatalf("copy model = %q, want the request's m-2", copyEntry.Model)
	}
	if copyEntry.APIKey != "sk-source" {
		t.Fatalf("copy api_key = %q, want the source's key", copyEntry.APIKey)
	}
	if fmt.Sprint(copyEntry.Params["temperature"]) != "0.3" {
		t.Fatalf("copy params = %v, want the source's temperature", copyEntry.Params)
	}
	if copyEntry.Headers["X-Tenant"] != "blue" {
		t.Fatalf("copy headers = %v, want the source's X-Tenant", copyEntry.Headers)
	}
	if source == nil || source.Model != "m-1" {
		t.Fatalf("source entry was overwritten: %+v", source)
	}
}
