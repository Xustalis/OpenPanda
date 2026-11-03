// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// init must prove the endpoint before "You're ready". The probe is the same
// one-word completion the runtime uses; this pins both the success and the
// failure side without touching a real provider.
func TestProbeModelEndpoint(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":1,"model":"mock",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer ok.Close()

	mc := config.ModelConfig{
		Provider: "custom",
		APIType:  config.APITypeOpenAI,
		BaseURL:  ok.URL,
		Model:    "mock",
		APIKey:   "test-key",
	}
	reply, err := probeModelEndpoint(mc)
	if err != nil {
		t.Fatalf("probe against a healthy endpoint: %v", err)
	}
	if reply == "" {
		t.Fatal("probe returned an empty reply")
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer bad.Close()
	mc.BaseURL = bad.URL
	if _, err := probeModelEndpoint(mc); err == nil {
		t.Fatal("probe against a failing endpoint returned nil error")
	}
}
