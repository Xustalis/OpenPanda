package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// policyHandler builds a panel over a real config file, so the persist half of
// every write is exercised rather than only the in-memory half.
func policyHandler(t *testing.T) (http.Handler, *config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "node:\n  name: test\napproval:\n  mode: on-request\nrouting:\n  tools_policy: minimal\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cfg := &config.Config{}
	cfg.Approval.Mode = config.ApprovalModeOnRequest
	cfg.Routing.ToolsPolicy = config.ToolsPolicyMinimal
	cfg.Memory.Limits.Project = 30000
	h := New(Deps{
		Store:      newTestStore(t),
		Cfg:        cfg,
		ConfigPath: path,
		StaticDir:  t.TempDir(),
		Token:      testToken,
	})
	return h, cfg, path
}

// TestPolicySettingsRoundTrip is the console's half of the approval gate: the
// setting a user is most likely to change after watching the queue for an
// afternoon used to require leaving the console.
func TestPolicySettingsRoundTrip(t *testing.T) {
	h, cfg, path := policyHandler(t)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodGet, "/api/settings/policy", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("get = %d, body %s", rr.Code, rr.Body.String())
	}
	var got policySettingsJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ApprovalMode != config.ApprovalModeOnRequest || got.ToolsPolicy != config.ToolsPolicyMinimal {
		t.Fatalf("initial policy = %+v", got)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, authedReq(http.MethodPut, "/api/settings/policy",
		strings.NewReader(`{"approval_mode":"never","tools_policy":"extended","limit_project":12345}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("put = %d, body %s", rr.Code, rr.Body.String())
	}
	// Applied to the live config the engine and core read from...
	if cfg.Approval.Mode != config.ApprovalModeNever || cfg.Routing.ToolsPolicy != config.ToolsPolicyExtended {
		t.Fatalf("live config not updated: %+v %+v", cfg.Approval, cfg.Routing)
	}
	if cfg.Memory.Limits.Project != 12345 {
		t.Fatalf("limit not applied: %d", cfg.Memory.Limits.Project)
	}
	// ...and persisted, or it would silently revert on the next start.
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"never", "extended", "12345"} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("config file missing %q:\n%s", want, saved)
		}
	}
	// A field the request omitted must be left alone.
	if cfg.Injection.NormalizedModel() != config.InjectionModelAuto {
		t.Errorf("omitted field changed: injection = %q", cfg.Injection.Model)
	}
}

// TestPolicySettingsRejectsBadValues: validation happens before any write, since a
// half-applied policy is worse than a rejected one — the user cannot tell which
// half took.
func TestPolicySettingsRejectsBadValues(t *testing.T) {
	h, cfg, path := policyHandler(t)
	before, _ := os.ReadFile(path)
	for _, body := range []string{
		`{"approval_mode":"sometimes"}`,
		`{"tools_policy":"everything"}`,
		`{"injection_model":"maybe"}`,
		`{"limit_project":-1}`,
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authedReq(http.MethodPut, "/api/settings/policy", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("put %s = %d, want 400", body, rr.Code)
		}
	}
	if cfg.Approval.Mode != config.ApprovalModeOnRequest {
		t.Errorf("a rejected write changed the live config: %q", cfg.Approval.Mode)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("a rejected write touched the config file:\n%s", after)
	}
}
