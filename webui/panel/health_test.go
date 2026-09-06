package panel_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/storage"
	"github.com/Xustalis/OpenPanda/webui/panel"
)

func TestHealthzEndpoint(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatal(err)
	}

	store := core.NewTaskStore(db, nil)
	h := panel.New(panel.Deps{
		Store: store,
		DB:    db,
		Token: "secret-token",
	})

	// 1. GET /api/healthz without auth header should succeed (unauthenticated probe)
	req := httptest.NewRequest(http.MethodGet, "/api/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var data map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if data["status"] != "ok" {
		t.Fatalf("expected status 'ok', got %v", data["status"])
	}
	if data["database"] != "ok" {
		t.Fatalf("expected database 'ok', got %v", data["database"])
	}

	// 2. GET /healthz alias
	req2 := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /healthz, got %d", rec2.Code)
	}

	// 3. Degraded state when database is closed
	_ = db.Close()
	req3 := httptest.NewRequest(http.MethodGet, "/api/healthz", nil)
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when DB is down, got %d", rec3.Code)
	}
	var data3 map[string]any
	if err := json.Unmarshal(rec3.Body.Bytes(), &data3); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if data3["status"] != "degraded" {
		t.Fatalf("expected status 'degraded', got %v", data3["status"])
	}
}
