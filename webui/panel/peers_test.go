// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// TestPeersListEditRemove walks the dial-list management surface the device
// card uses: list what is configured, rename an entry in place, refuse a
// collision, and remove one. The config file is checked after each write —
// the live copy alone is not proof the next daemon start sees the change.
func TestPeersListEditRemove(t *testing.T) {
	h, _, configPath, _ := newCardPanel(t)

	if code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/nodes/add",
		`{"addr":"192.168.1.20:7836","trust":"mesh"}`)); code != http.StatusOK {
		t.Fatalf("add A = %d %v", code, out)
	}
	if code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/nodes/add",
		`{"addr":"192.168.1.30:7836","trust":"guarded"}`)); code != http.StatusOK {
		t.Fatalf("add B = %d %v", code, out)
	}

	code, view := doJSON(t, h, authedReq(http.MethodGet, "/api/peers", nil))
	if code != http.StatusOK {
		t.Fatalf("list peers = %d", code)
	}
	if view["has_secret"] != true {
		t.Errorf("has_secret = %v, want true after the first add", view["has_secret"])
	}
	rows, _ := view["peers"].([]any)
	if len(rows) != 2 {
		t.Fatalf("peers = %v, want 2 rows", view["peers"])
	}
	byAddr := map[string]map[string]any{}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		byAddr[row["addr"].(string)] = row
	}
	if byAddr["192.168.1.30:7836"]["trust"] != "guarded" {
		t.Errorf("B trust = %v, want guarded", byAddr["192.168.1.30:7836"]["trust"])
	}
	if byAddr["192.168.1.20:7836"]["trust"] != "mesh" {
		t.Errorf("A trust = %v, want mesh", byAddr["192.168.1.20:7836"]["trust"])
	}

	// Rename A → C and change its tier in one write.
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/nodes/add",
		`{"addr":"192.168.1.21:7836","original_addr":"192.168.1.20:7836","trust":"open"}`))
	if code != http.StatusOK {
		t.Fatalf("edit A = %d %v", code, out)
	}
	if out["added"] != false || out["updated"] != true {
		t.Errorf("edit added/updated = %v/%v, want false/true", out["added"], out["updated"])
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "192.168.1.20:7836") {
		t.Errorf("old address survived the edit:\n%s", data)
	}
	if !strings.Contains(string(data), "192.168.1.21:7836") || !strings.Contains(string(data), "trust: open") {
		t.Errorf("edited peer not written:\n%s", data)
	}

	// A rename onto B's address is a conflict, not a silent merge.
	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/nodes/add",
		`{"addr":"192.168.1.30:7836","original_addr":"192.168.1.21:7836","trust":"open"}`))
	if code != http.StatusConflict {
		t.Errorf("rename collision = %d, want 409", code)
	}
	// Editing an address that is not in the list is a 404.
	code, _ = doJSON(t, h, jsonReq(http.MethodPost, "/api/nodes/add",
		`{"addr":"192.168.1.99:7836","original_addr":"192.168.1.98:7836","trust":"mesh"}`))
	if code != http.StatusNotFound {
		t.Errorf("edit missing = %d, want 404", code)
	}

	// Remove C.
	target := "/api/peers?addr=" + url.QueryEscape("192.168.1.21:7836")
	code, out = doJSON(t, h, authedReq(http.MethodDelete, target, nil))
	if code != http.StatusOK || out["removed"] != true {
		t.Fatalf("remove C = %d %v", code, out)
	}
	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "192.168.1.21:7836") {
		t.Errorf("removed peer still in config:\n%s", data)
	}
	if !strings.Contains(string(data), "192.168.1.30:7836") {
		t.Errorf("removing one peer dropped another:\n%s", data)
	}
	code, _ = doJSON(t, h, authedReq(http.MethodDelete, "/api/peers?addr="+url.QueryEscape("192.168.1.21:7836"), nil))
	if code != http.StatusNotFound {
		t.Errorf("remove missing = %d, want 404", code)
	}
}

// TestPeersAddNormalizesDuplicate: the same peer written as a bare host:port
// and as ws://host:port is one dial-list entry, not two. The second add keeps
// the original spelling and only reconciles the tier.
func TestPeersAddNormalizesDuplicate(t *testing.T) {
	h, _, configPath, _ := newCardPanel(t)

	if code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/nodes/add",
		`{"addr":"192.168.1.50:7836","trust":"mesh"}`)); code != http.StatusOK {
		t.Fatalf("add bare = %d %v", code, out)
	}
	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/nodes/add",
		`{"addr":"ws://192.168.1.50:7836","trust":"guarded"}`))
	if code != http.StatusOK {
		t.Fatalf("add ws = %d %v", code, out)
	}
	if out["added"] != false || out["updated"] != true {
		t.Errorf("duplicate add added/updated = %v/%v, want false/true", out["added"], out["updated"])
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "192.168.1.50:7836"); n != 1 {
		t.Errorf("address appears %d times, want 1:\n%s", n, data)
	}
	if !strings.Contains(string(data), "trust: guarded") {
		t.Errorf("tier not reconciled on duplicate add:\n%s", data)
	}

	code, view := doJSON(t, h, authedReq(http.MethodGet, "/api/peers", nil))
	if code != http.StatusOK {
		t.Fatalf("list peers = %d", code)
	}
	if rows, _ := view["peers"].([]any); len(rows) != 1 {
		t.Errorf("peers = %v, want one normalized entry", view["peers"])
	}
}

// TestPeersWithoutConfig: the peers surface degrades to 503, like the other
// config-backed endpoints, instead of panicking.
func TestPeersWithoutConfig(t *testing.T) {
	h := New(Deps{Store: newTestStore(t), StaticDir: t.TempDir(), Token: testToken})
	if code, _ := doJSON(t, h, authedReq(http.MethodGet, "/api/peers", nil)); code != http.StatusServiceUnavailable {
		t.Errorf("GET peers without cfg = %d, want 503", code)
	}
	if code, _ := doJSON(t, h, authedReq(http.MethodDelete, "/api/peers?addr=x", nil)); code != http.StatusServiceUnavailable {
		t.Errorf("DELETE peers without cfg = %d, want 503", code)
	}
}
