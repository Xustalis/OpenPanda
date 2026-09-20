package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/updater"
)

func TestUpdateCheckFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]struct {
			TagName    string `json:"tag_name"`
			Body       string `json:"body"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}{
			{TagName: "v0.0.8", Body: "Release 0.0.8", Prerelease: false},
			{TagName: "v0.0.8-preview", Body: "Preview 0.0.8", Prerelease: true},
		})
	}))
	defer srv.Close()

	cleanup := updater.SetAPIBaseForTest(srv.URL)
	defer cleanup()

	// Direct test of updater Manager with current = "0.0.8-preview"
	m := updater.New(updater.Options{
		Current: "0.0.8-preview",
	})
	if err := m.Check(context.Background()); err != nil {
		t.Fatalf("m.Check failed: %v", err)
	}
	st := m.Status()
	if !st.Available {
		t.Fatalf("expected update to be available, got %+v", st)
	}
	if st.Latest != "0.0.8" {
		t.Fatalf("expected latest version 0.0.8, got %q", st.Latest)
	}
}
