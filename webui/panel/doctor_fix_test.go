// SPDX-License-Identifier: AGPL-3.0-or-later

package panel_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/testdb"
	"github.com/Xustalis/OpenPanda/webui/panel"
)

// The console's one-click repair rides POST /api/doctor/fix: the route has to
// exist, the body has to select the repair, and the edit has to land on disk
// through the same lock every other config writer holds. This covers the
// wiring the unit tests in internal/doctor cannot see.
func TestDoctorFixRepairsLegacyExposure(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	body := `node:
  name: doctor-panel-test
network:
  listen_addr: "0.0.0.0:27836"
  panel_addr: "127.0.0.1:27840"
  udp_listen: "off"
storage:
  db_path: "` + filepath.ToSlash(filepath.Join(root, "test.db")) + `"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.OpenFile(t, filepath.Join(root, "test.db"))
	h := panel.New(panel.Deps{
		Store:      core.NewTaskStore(db, nil),
		DB:         db,
		Cfg:        cfg,
		ConfigPath: cfgPath,
		Token:      "doctor-token",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/doctor/fix",
		strings.NewReader(`{"keys":["doctor.network.exposure.legacy"],"exposure":"loopback"}`))
	req.Header.Set("Authorization", "Bearer doctor-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/doctor/fix = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Results []struct {
			Key     string   `json:"key"`
			OK      bool     `json:"ok"`
			Msg     string   `json:"msg"`
			Pairs   []string `json:"pairs"`
			Restart bool     `json:"restart"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, rec.Body.String())
	}
	if len(out.Results) != 1 || !out.Results[0].OK {
		t.Fatalf("results = %+v, want one success", out.Results)
	}
	if !out.Results[0].Restart {
		t.Fatal("exposure repair must report that a restart is needed")
	}

	// The file is the contract: the next start reads it.
	repaired, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("repaired config does not load: %v", err)
	}
	if repaired.Network.Exposure() != config.ExposureLoopback {
		t.Fatalf("exposure = %q, want loopback", repaired.Network.Exposure())
	}
	if !config.IsLoopbackBind(repaired.Network.ListenAddr) {
		t.Fatalf("listen_addr = %q, want loopback", repaired.Network.ListenAddr)
	}
}

// A repair for a check that is not failing must do nothing — the endpoint
// runs the suite itself rather than trusting the keys a client sent.
func TestDoctorFixIgnoresPassingChecks(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	body := `node:
  name: doctor-panel-pass
network:
  exposure_mode: loopback
  listen_addr: "127.0.0.1:27836"
  udp_listen: "off"
storage:
  db_path: "` + filepath.ToSlash(filepath.Join(root, "test.db")) + `"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	db := testdb.OpenFile(t, filepath.Join(root, "test.db"))
	h := panel.New(panel.Deps{
		Store:      core.NewTaskStore(db, nil),
		DB:         db,
		Cfg:        cfg,
		ConfigPath: cfgPath,
		Token:      "doctor-token",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/doctor/fix",
		strings.NewReader(`{"keys":["doctor.network.exposure.legacy"],"exposure":"lan"}`))
	req.Header.Set("Authorization", "Bearer doctor-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/doctor/fix = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(out.Results) != 0 {
		t.Fatalf("a passing check was repaired: %s", rec.Body.String())
	}
	after, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Network.Exposure() != config.ExposureLoopback {
		t.Fatalf("exposure changed to %q", after.Network.Exposure())
	}
}
