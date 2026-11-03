// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// The registry's write semantics: adding never overwrites, editing rewrites one
// row, deleting one row is always possible (including the live one, which
// promotes a successor), and an endpoint that belongs to another plan of the
// same vendor is refused instead of persisted.

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"gopkg.in/yaml.v3"
)

// newModelPanel builds a panel over a config the test owns, with no engine.
// The registry endpoints never dial, so the engineless path covers them — and
// it keeps the assertions against config.yaml honest (what was written, not
// what memory happens to hold).
func newModelPanel(t *testing.T, mutate func(*config.Config)) (http.Handler, string) {
	t.Helper()
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Storage.WorkPath = tmp
	cfg.Storage.DBPath = filepath.Join(tmp, "models.db")
	if mutate != nil {
		mutate(cfg)
	}
	configPath := filepath.Join(tmp, "config.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	h := New(Deps{
		Store:      newTestStore(t),
		Cfg:        cfg,
		ConfigPath: configPath,
		StaticDir:  t.TempDir(),
		Token:      testToken,
	})
	return h, configPath
}

// readConfig parses the file the panel wrote.
func readConfig(t *testing.T, path string) config.Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

func entryURL(alias, model, base string) string {
	q := url.Values{}
	if model != "" {
		q.Set("model", model)
	}
	if base != "" {
		q.Set("base_url", base)
	}
	if len(q) == 0 {
		return "/api/models/" + url.PathEscape(alias)
	}
	return "/api/models/" + url.PathEscape(alias) + "?" + q.Encode()
}

// A second account for the same provider must become a second row. The old
// handler replaced whatever carried the same alias, so the first account's key
// silently vanished — and there was no way to keep two at once.
func TestAddModelKeepsASecondAccountSideBySide(t *testing.T) {
	h, path := newModelPanel(t, nil)

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models",
		`{"provider":"deepseek","api_key":"sk-first","alias":"work","model":"deepseek-flash"}`))
	if code != http.StatusOK {
		t.Fatalf("first add = %d %v", code, out)
	}
	if out["alias"] != "work" {
		t.Fatalf("first alias = %v, want work", out["alias"])
	}

	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/models",
		`{"provider":"deepseek","api_key":"sk-second","alias":"work","model":"deepseek-flash"}`))
	if code != http.StatusOK {
		t.Fatalf("second add = %d %v", code, out)
	}
	if out["alias"] != "work-2" {
		t.Fatalf("second alias = %v, want the suffixed free name", out["alias"])
	}

	cfg := readConfig(t, path)
	if len(cfg.Models) != 2 {
		t.Fatalf("models on disk = %d, want 2 (both accounts)", len(cfg.Models))
	}
	keys := cfg.Models[0].APIKey + " " + cfg.Models[1].APIKey
	if !strings.Contains(keys, "sk-first") || !strings.Contains(keys, "sk-second") {
		t.Fatalf("one account overwrote the other: keys = %q", keys)
	}
}

// The completion cap used to be dropped for every catalogue provider (only the
// custom branch kept it), so a typed number came back as the auto value.
func TestAddModelAppliesTheLimitsTheUserTyped(t *testing.T) {
	h, _ := newModelPanel(t, nil)

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models",
		`{"provider":"deepseek","api_key":"sk-a","alias":"cap","model":"deepseek-flash","max_tokens":8192,"context_window":123456}`))
	if code != http.StatusOK {
		t.Fatalf("add = %d %v", code, out)
	}
	if out["max_tokens"] != 8192.0 {
		t.Fatalf("max_tokens = %v, want 8192", out["max_tokens"])
	}
	if out["context_window"] != 123456.0 {
		t.Fatalf("context_window = %v, want 123456", out["context_window"])
	}
	if out["max_tokens_auto"] != nil || out["context_auto"] != nil {
		t.Fatalf("an explicit value must not read as auto: %v", out)
	}
}

// Untouched limits stay auto, and the panel reports the numbers the resolver
// picked so the form can show them as placeholders.
func TestAddModelLeavesUntouchedLimitsOnAuto(t *testing.T) {
	h, _ := newModelPanel(t, nil)

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models",
		`{"provider":"deepseek","api_key":"sk-a","alias":"auto","model":"deepseek-flash"}`))
	if code != http.StatusOK {
		t.Fatalf("add = %d %v", code, out)
	}
	if out["max_tokens_auto"] != true || out["context_auto"] != true {
		t.Fatalf("unset limits should read as auto: %v", out)
	}
	if out["auto_max_tokens"] == nil || out["auto_context_window"] == nil {
		t.Fatalf("auto values missing: %v", out)
	}
}

// Zen and Go are sibling rosters on one host; a base pasted from the other
// plan's docs must be refused at write time, because every later call would
// fail with nothing on screen to explain it.
func TestAddModelRefusesTheOtherPlansEndpoint(t *testing.T) {
	h, path := newModelPanel(t, nil)

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models",
		`{"provider":"opencode","api_key":"sk-zen","base_url":"https://opencode.ai/zen/go/v1","model":"deepseek-v4.1-flash","alias":"mixed"}`))
	if code != http.StatusBadRequest {
		t.Fatalf("mismatched plan add = %d %v, want 400", code, out)
	}
	if cfg := readConfig(t, path); len(cfg.Models) != 0 {
		t.Fatalf("a refused config reached the file: %+v", cfg.Models)
	}

	// The same base under the row that owns it is fine.
	code, out = doJSON(t, h, jsonReq(http.MethodPost, "/api/models",
		`{"provider":"opencode-go","api_key":"sk-go","base_url":"https://opencode.ai/zen/go/v1","model":"deepseek-v4.1-flash","alias":"go"}`))
	if code != http.StatusOK {
		t.Fatalf("go add = %d %v", code, out)
	}
	if out["provider"] != "opencode-go" {
		t.Fatalf("provider = %v", out["provider"])
	}
}

// A config written earlier with the mismatch still loads; it is flagged so the
// console can say what is wrong instead of leaving the user to guess.
func TestListModelsFlagsAForeignPlanEndpoint(t *testing.T) {
	h, _ := newModelPanel(t, func(c *config.Config) {
		c.Model = config.ModelConfig{
			Name:     "opencode",
			Provider: "opencode",
			APIType:  config.APITypeOpenAI,
			BaseURL:  "https://opencode.ai/zen/go",
			Model:    "deepseek-v4.1-flash",
			APIKey:   "sk-x",
		}
	})

	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/models", nil))
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	active, _ := out["active"].(map[string]any)
	if active["warning_code"] != "endpoint-owner" || active["warning_arg"] != "opencode-go" {
		t.Fatalf("active warning = %v / %v, want endpoint-owner / opencode-go",
			active["warning_code"], active["warning_arg"])
	}
}

// The live model is deletable too — refusing outright stranded the user. When
// another row exists it takes over; when it does not, the refusal says why.
func TestRemoveModelPromotesASuccessorWhenItIsLive(t *testing.T) {
	live := config.ModelConfig{
		Name: "live", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://live.example/v1", Model: "live", APIKey: "k1",
	}
	spare := config.ModelConfig{
		Name: "spare", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://spare.example/v1", Model: "spare", APIKey: "k2",
	}
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = live
		c.Models = []config.ModelConfig{live, spare}
	})

	code, out := doJSON(t, h, authedReq(http.MethodDelete,
		entryURL("live", "live", "https://live.example/v1"), nil))
	if code != http.StatusOK {
		t.Fatalf("delete live = %d %v", code, out)
	}
	if out["switched_to"] != "spare" {
		t.Fatalf("switched_to = %v, want spare", out["switched_to"])
	}
	cfg := readConfig(t, path)
	if len(cfg.Models) != 1 || cfg.Models[0].Alias() != "spare" {
		t.Fatalf("registry after delete = %+v", cfg.Models)
	}
	// The live switch is persisted, not just held in memory.
	if cfg.Model.Alias() != "spare" {
		t.Fatalf("active model = %q, want spare", cfg.Model.Alias())
	}
}

// A model written only to `model:` — an older single-model config, or one a
// user edited by hand — is the row the page paints as "in use". It had no
// delete path on any surface: the console skipped the button for an
// unregistered row and the handler only searched the registry. Removing it now
// clears the section's owner and promotes a registered row.
func TestRemoveModelClearsTheLiveRowOutsideTheRegistry(t *testing.T) {
	stale := config.ModelConfig{
		Name: "stale", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://stale.example/v1", Model: "m", APIKey: "k1",
	}
	spare := config.ModelConfig{
		Name: "spare", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://spare.example/v1", Model: "m", APIKey: "k2",
	}
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = stale
		c.Models = []config.ModelConfig{spare}
	})

	code, out := doJSON(t, h, authedReq(http.MethodDelete,
		entryURL("stale", "m", "https://stale.example/v1"), nil))
	if code != http.StatusOK {
		t.Fatalf("delete the unregistered live row = %d %v", code, out)
	}
	if out["switched_to"] != "spare" {
		t.Fatalf("switched_to = %v, want spare", out["switched_to"])
	}
	cfg := readConfig(t, path)
	if cfg.Model.Alias() != "spare" {
		t.Fatalf("live model = %q, want the promoted spare", cfg.Model.Alias())
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Alias() != "spare" {
		t.Fatalf("registry after the delete = %+v, want spare untouched", cfg.Models)
	}
}

// Removing the last model is the request a user makes when the only model
// they configured is wrong. It used to be refused with "add another one
// first" — which is the very complaint the report started from, just moved one
// step later. The section is cleared deliberately instead, the same way
// `/model remove` and the TUI panel already did it.
func TestRemoveModelClearsTheLastModel(t *testing.T) {
	only := config.ModelConfig{
		Name: "only", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://only.example/v1", Model: "only", APIKey: "k",
	}
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = only
		c.Models = []config.ModelConfig{only}
	})

	code, out := doJSON(t, h, authedReq(http.MethodDelete,
		entryURL("only", "only", "https://only.example/v1"), nil))
	if code != http.StatusOK {
		t.Fatalf("delete last = %d %v, want the row gone", code, out)
	}
	if out["cleared"] != true {
		t.Fatalf("response = %v, want cleared:true so the console can say why it is empty", out)
	}
	cfg := readConfig(t, path)
	if len(cfg.Models) != 0 {
		t.Fatalf("registry after deleting the last model = %+v", cfg.Models)
	}
	if cfg.Model.Model != "" || cfg.Model.Provider != "" || cfg.Model.BaseURL != "" {
		t.Fatalf("model: still names the deleted entry: %+v", cfg.Model)
	}
	// The in-memory config agrees with the file, so the list does not paint a
	// row that no longer exists.
	code, list := doJSON(t, h, authedReq(http.MethodGet, "/api/models", nil))
	if code != http.StatusOK {
		t.Fatalf("list after the clear = %d", code)
	}
	if rows, _ := list["models"].([]any); len(rows) != 0 {
		t.Fatalf("rows after the clear = %v", rows)
	}
	if active, _ := list["active"].(map[string]any); active["alias"] != "" {
		t.Fatalf("a cleared config still reports an active model: %v", active)
	}
}

// The reported bug: a registry row that merely shares the live row's alias —
// a different endpoint, a second account — was treated as the live row and
// could never be removed.
func TestRemoveModelDropsASameNamedRowOnAnotherEndpoint(t *testing.T) {
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = config.ModelConfig{
			Name: "opencode", Provider: "custom", APIType: config.APITypeOpenAI,
			BaseURL: "https://live.example/v1", Model: "m", APIKey: "k1",
		}
		c.Models = []config.ModelConfig{{
			Name: "opencode", Provider: "custom", APIType: config.APITypeOpenAI,
			BaseURL: "https://other.example/v1", Model: "m", APIKey: "k2",
		}}
	})

	code, out := doJSON(t, h, authedReq(http.MethodDelete,
		entryURL("opencode", "m", "https://other.example/v1"), nil))
	if code != http.StatusOK {
		t.Fatalf("delete same-named row = %d %v", code, out)
	}
	if cfg := readConfig(t, path); len(cfg.Models) != 0 {
		t.Fatalf("row still on disk: %+v", cfg.Models)
	}
	if cfg := readConfig(t, path); cfg.Model.BaseURL != "https://live.example/v1" {
		t.Fatalf("the live endpoint moved: %+v", cfg.Model)
	}
}

// Editing in place, including the live entry that was never registered (the
// row the console paints at the top of the page).
func TestUpdateModelRewritesOneRowInPlace(t *testing.T) {
	live := config.ModelConfig{
		Name: "live", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://live.example/v1", Model: "live", APIKey: "k1",
	}
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = live
		c.Models = []config.ModelConfig{live}
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPut, entryURL("live", "live", "https://live.example/v1"),
		`{"model":"live-v2","max_tokens":4096,"alias":"renamed"}`))
	if code != http.StatusOK {
		t.Fatalf("update = %d %v", code, out)
	}
	if out["alias"] != "renamed" || out["model"] != "live-v2" {
		t.Fatalf("edited row = %v", out)
	}
	if out["max_tokens"] != 4096.0 {
		t.Fatalf("max_tokens = %v, want 4096", out["max_tokens"])
	}
	if out["base_url"] != "https://live.example/v1" {
		t.Fatalf("an edit must keep the endpoint it did not restate: %v", out["base_url"])
	}
	cfg := readConfig(t, path)
	if cfg.Model.Alias() != "renamed" || cfg.Model.Model != "live-v2" {
		t.Fatalf("live entry after edit = %+v", cfg.Model)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Alias() != "renamed" {
		t.Fatalf("registry after edit = %+v", cfg.Models)
	}
	// The stored key survives an edit that did not re-type it.
	if cfg.Model.APIKey != "k1" {
		t.Fatalf("key lost on edit: %q", cfg.Model.APIKey)
	}
}

// Re-setting a limit to auto is a distinct instruction from "leave it alone" —
// without the reset flag a value could be typed once and never cleared.
func TestUpdateModelCanReturnALimitToAuto(t *testing.T) {
	live := config.ModelConfig{
		Name: "live", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://live.example/v1", Model: "live", APIKey: "k",
		MaxTokens: 4096, ContextWindow: 32768,
	}
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = live
		c.Models = []config.ModelConfig{live}
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPut, entryURL("live", "live", "https://live.example/v1"),
		`{"max_tokens_reset":true,"context_window_reset":true}`))
	if code != http.StatusOK {
		t.Fatalf("update = %d %v", code, out)
	}
	if out["max_tokens_auto"] != true || out["context_auto"] != true {
		t.Fatalf("limits not returned to auto: %v", out)
	}
	cfg := readConfig(t, path)
	if cfg.Model.MaxTokens != 0 || cfg.Model.ContextWindow != 0 {
		t.Fatalf("reset did not reach the file: %+v", cfg.Model)
	}
}

// Two accounts of one vendor share a provider id and an endpoint, so "reuse
// the key on file for this provider" cannot say which account a new row
// belongs to. Naming the row is the only unambiguous answer.
func TestAddModelBorrowsTheKeyOfTheNamedConnection(t *testing.T) {
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Models = []config.ModelConfig{
			{Name: "work", Provider: "deepseek", APIType: config.APITypeAnthropic,
				BaseURL: "https://api.deepseek.com/anthropic", Model: "deepseek-flash", APIKey: "sk-work"},
			{Name: "personal", Provider: "deepseek", APIType: config.APITypeAnthropic,
				BaseURL: "https://api.deepseek.com/anthropic", Model: "deepseek-flash", APIKey: "sk-personal"},
		}
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models",
		`{"provider":"deepseek","model":"deepseek-chat","alias":"chat-personal","reuse_from":"personal"}`))
	if code != http.StatusOK {
		t.Fatalf("add = %d %v", code, out)
	}
	cfg := readConfig(t, path)
	var got string
	for _, m := range cfg.Models {
		if m.Alias() == "chat-personal" {
			got = m.APIKey
		}
	}
	if got != "sk-personal" {
		t.Fatalf("new row key = %q, want the named connection's key", got)
	}
}

// The repair the console offers for a plan/endpoint mismatch: retarget the
// live entry at the plan that actually owns the endpoint it dials.
func TestUpdateModelRepairsAPlanMismatch(t *testing.T) {
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = config.ModelConfig{
			Name: "opencode", Provider: "opencode", APIType: config.APITypeOpenAI,
			BaseURL: "https://opencode.ai/zen/go", Model: "deepseek-v4.1-flash", APIKey: "k",
		}
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPut, entryURL("opencode", "deepseek-v4.1-flash", "https://opencode.ai/zen/go"),
		`{"provider":"opencode-go","base_url":"https://opencode.ai/zen/go/v1","api_type":"openai"}`))
	if code != http.StatusOK {
		t.Fatalf("repair = %d %v", code, out)
	}
	if out["warning_code"] != nil {
		t.Fatalf("still flagged after the repair: %v", out)
	}
	cfg := readConfig(t, path)
	if cfg.Model.Provider != "opencode-go" {
		t.Fatalf("live provider = %q, want the plan that owns the endpoint", cfg.Model.Provider)
	}
	if cfg.Model.APIKey != "k" {
		t.Fatalf("key lost on repair: %q", cfg.Model.APIKey)
	}
}

// Repairing a plan mismatch (the OpenCode Zen/Go mix-up) must not ask the user
// to paste the key again: both plans are one account on one host, so the stored
// credential is still the right one.
func TestUpdateModelKeepsTheKeyWhenOnlyTheSiblingPlanChanges(t *testing.T) {
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = config.ModelConfig{
			Name: "opencode", Provider: "opencode", APIType: config.APITypeOpenAI,
			BaseURL: "https://opencode.ai/zen/v1", Model: "deepseek-v4.1-flash", APIKey: "sk-opencode",
		}
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPut,
		entryURL("opencode", "deepseek-v4.1-flash", "https://opencode.ai/zen/v1"),
		`{"provider":"opencode-go","base_url":"https://opencode.ai/zen/go/v1","api_type":"openai"}`))
	if code != http.StatusOK {
		t.Fatalf("sibling-plan switch = %d %v", code, out)
	}
	cfg := readConfig(t, path)
	if cfg.Model.APIKey != "sk-opencode" {
		t.Fatalf("key across a same-host plan switch = %q, want the stored one", cfg.Model.APIKey)
	}
	if cfg.Model.Provider != "opencode-go" {
		t.Fatalf("provider = %q, want opencode-go", cfg.Model.Provider)
	}
}

// A vendor switch is not a plan switch: carrying the old key over means dialing
// a stranger's API with the previous account's token, which fails with a 401
// nothing on screen can explain. The edit must ask for a key instead.
func TestUpdateModelRefusesAVendorSwitchWithoutAKey(t *testing.T) {
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = config.ModelConfig{
			Name: "opencode", Provider: "opencode", APIType: config.APITypeOpenAI,
			BaseURL: "https://opencode.ai/zen/v1", Model: "deepseek-v4.1-flash", APIKey: "sk-opencode",
		}
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPut,
		entryURL("opencode", "deepseek-v4.1-flash", "https://opencode.ai/zen/v1"),
		`{"provider":"deepseek","base_url":"https://api.deepseek.com","api_type":"openai","model":"deepseek-flash"}`))
	if code != http.StatusBadRequest {
		t.Fatalf("vendor switch without a key = %d %v, want 400", code, out)
	}
	if cfg := readConfig(t, path); cfg.Model.Provider != "opencode" {
		t.Fatalf("a refused edit still moved the live entry: %+v", cfg.Model)
	}
}

// A vendor switch that lands on a vendor already configured elsewhere reuses
// that vendor's own key — the one written for the account being dialed.
func TestUpdateModelAdoptsTheNewVendorsStoredKey(t *testing.T) {
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = config.ModelConfig{
			Name: "opencode", Provider: "opencode", APIType: config.APITypeOpenAI,
			BaseURL: "https://opencode.ai/zen/v1", Model: "kimi-k3", APIKey: "sk-opencode",
		}
		c.Models = []config.ModelConfig{{
			Name: "ds", Provider: "deepseek", APIType: config.APITypeOpenAI,
			BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", APIKey: "sk-deepseek",
		}}
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPut,
		entryURL("opencode", "kimi-k3", "https://opencode.ai/zen/v1"),
		`{"provider":"deepseek","base_url":"https://api.deepseek.com","api_type":"openai","model":"deepseek-flash","alias":"ds-live"}`))
	if code != http.StatusOK {
		t.Fatalf("vendor switch = %d %v", code, out)
	}
	if cfg := readConfig(t, path); cfg.Model.APIKey != "sk-deepseek" {
		t.Fatalf("key after the switch = %q, want the new vendor's own", cfg.Model.APIKey)
	}
}

// A config written before aliases were de-duplicated can hold two rows under
// one name. Testing "work" has to test the row the console pointed at, not
// whichever one the scan reached first.
func TestResolveModelRefTestsTheAddressedRow(t *testing.T) {
	cfg := config.Default()
	cfg.Models = []config.ModelConfig{
		{Name: "work", Provider: "deepseek", APIType: config.APITypeOpenAI,
			BaseURL: "https://first.example/v1", Model: "deepseek-flash", APIKey: "sk-first"},
		{Name: "work", Provider: "deepseek", APIType: config.APITypeOpenAI,
			BaseURL: "https://second.example/v1", Model: "deepseek-flash", APIKey: "sk-second"},
	}
	// No engine: the resolver only reads config, and a static handler keeps the
	// assertion on lookup order rather than on anything an engine might resolve.
	panel := &handler{cfg: cfg, cfgMu: &sync.RWMutex{}}

	got, err := panel.resolveModelRefErr(modelRefRequest{
		Alias: "work", Model: "deepseek-flash", BaseURL: "https://second.example/v1",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.APIKey != "sk-second" {
		t.Fatalf("resolved key = %q, want the row at the named endpoint", got.APIKey)
	}

	// A caller that names only the alias still gets an answer — the first row,
	// which is what the field meant before the endpoint was available.
	got, err = panel.resolveModelRefErr(modelRefRequest{Alias: "work"})
	if err != nil {
		t.Fatalf("resolve loose: %v", err)
	}
	if got.BaseURL != "https://first.example/v1" {
		t.Fatalf("loose resolve = %q, want the first row", got.BaseURL)
	}
}

// The console addresses a row with the endpoint the list reported, and the list
// rebases an alternate form onto the catalogue's own (`…/zen/go` becomes
// `…/zen/go/v1`). An exact match on the stored base then finds nothing — and
// the row the page is offering to repair answers "no such model" to its own
// fix. A name that is unique still resolves.
func TestUpdateModelAcceptsTheNormalizedAddress(t *testing.T) {
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = config.ModelConfig{
			Name: "opencode", Provider: "opencode", APIType: config.APITypeOpenAI,
			BaseURL: "https://opencode.ai/zen/go", Model: "deepseek-v4.1-flash", APIKey: "k",
		}
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPut,
		entryURL("opencode", "deepseek-v4.1-flash", "https://opencode.ai/zen/go/v1"),
		`{"provider":"opencode-go","base_url":"https://opencode.ai/zen/go/v1","api_type":"openai"}`))
	if code != http.StatusOK {
		t.Fatalf("repair through a normalized address = %d %v", code, out)
	}
	cfg := readConfig(t, path)
	if cfg.Model.Provider != "opencode-go" || cfg.Model.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("live entry after repair = %+v", cfg.Model)
	}
}

// Deleting through the same normalized address must work too, or a row the user
// can see becomes a row they cannot remove — the original complaint.
func TestRemoveModelAcceptsTheNormalizedAddress(t *testing.T) {
	live := config.ModelConfig{
		Name: "opencode", Provider: "opencode", APIType: config.APITypeOpenAI,
		BaseURL: "https://opencode.ai/zen/go", Model: "deepseek-v4.1-flash", APIKey: "k",
	}
	spare := config.ModelConfig{
		Name: "spare", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://spare.example/v1", Model: "spare", APIKey: "k2",
	}
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = live
		c.Models = []config.ModelConfig{live, spare}
	})

	code, out := doJSON(t, h, authedReq(http.MethodDelete,
		entryURL("opencode", "deepseek-v4.1-flash", "https://opencode.ai/zen/go/v1"), nil))
	if code != http.StatusOK {
		t.Fatalf("delete through a normalized address = %d %v", code, out)
	}
	if out["switched_to"] != "spare" {
		t.Fatalf("switched_to = %v, want spare", out["switched_to"])
	}
	if cfg := readConfig(t, path); len(cfg.Models) != 1 || cfg.Models[0].Alias() != "spare" {
		t.Fatalf("registry after delete = %+v", cfg.Models)
	}
}

// Two rows under one name stay ambiguous on purpose: guessing which account to
// delete is worse than asking for the endpoint again.
func TestRemoveModelKeepsADuplicateNameAmbiguous(t *testing.T) {
	a := config.ModelConfig{Name: "work", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://a.example/v1", Model: "m", APIKey: "ka"}
	b := config.ModelConfig{Name: "work", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://b.example/v1", Model: "m", APIKey: "kb"}
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = a
		c.Models = []config.ModelConfig{a, b}
	})

	code, out := doJSON(t, h, authedReq(http.MethodDelete,
		entryURL("work", "m", "https://nowhere.example/v1"), nil))
	if code != http.StatusNotFound {
		t.Fatalf("ambiguous delete = %d %v, want 404", code, out)
	}
	if cfg := readConfig(t, path); len(cfg.Models) != 2 {
		t.Fatalf("an ambiguous delete still removed a row: %+v", cfg.Models)
	}
}

// Two accounts of one vendor are told apart in the list by the hint, so the
// hint has to carry more than the last four characters.
// "Copy this account" is a per-row operation, so it needs the row. Resolved by
// alias alone it duplicated whichever account came first — and the copy then
// carried a credential that belongs to the other one, which never
// authenticates and looks exactly like a bad key.
func TestAddModelCopiesTheAddressedAccount(t *testing.T) {
	first := config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"}
	second := config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"}
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = first
		c.Models = []config.ModelConfig{first, second}
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPost, "/api/models",
		`{"provider":"deepseek","model":"deepseek-chat","alias":"work","copy_from":"ds","copy_from_key":"`+keyFingerprint("sk-second")+`"}`))
	if code != http.StatusOK {
		t.Fatalf("copy the second account = %d %v", code, out)
	}
	cfg := readConfig(t, path)
	if len(cfg.Models) != 3 {
		t.Fatalf("rows = %d, want the source pair plus the copy", len(cfg.Models))
	}
	if cfg.Models[2].APIKey != "sk-second" {
		t.Fatalf("the copy borrowed the other account's key: %+v", cfg.Models[2])
	}
}

func TestMaskKeyDistinguishesTwoAccounts(t *testing.T) {
	a, b := maskKey("sk-work-aaaaac992"), maskKey("sk-home-aaaaac992")
	if a == b {
		t.Fatalf("two keys share a hint: %q", a)
	}
	if strings.Contains(a, "aaaaa") {
		t.Fatalf("hint leaks the middle of the key: %q", a)
	}
	if !strings.HasSuffix(a, "c992") || !strings.HasPrefix(a, "sk-w") {
		t.Fatalf("hint = %q, want head and tail", a)
	}
	if maskKey("short") != "••••" {
		t.Fatalf("a short key must not be reconstructable: %q", maskKey("short"))
	}
}

// ---- addressing one account when two share every visible field ----

// A config written before names were kept unique can hold two rows under one
// alias with the same model id and endpoint; the credential is the only thing
// telling them apart, and key_fingerprint is how the console learns which one
// a click meant. Nothing consumed it, so the delete addressed the first row
// named "ds" — the other account, and the one still in use.
func TestRemoveModelAddressesTheAccountByKeyDigest(t *testing.T) {
	first := config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"}
	second := config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"}
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = first
		c.Models = []config.ModelConfig{first, second}
	})

	code, out := doJSON(t, h, authedReq(http.MethodDelete,
		entryURL("ds", "deepseek-chat", "")+"&key="+keyFingerprint("sk-second"), nil))
	if code != http.StatusOK {
		t.Fatalf("delete the second account = %d %v", code, out)
	}
	cfg := readConfig(t, path)
	if len(cfg.Models) != 1 || cfg.Models[0].APIKey != "sk-first" {
		t.Fatalf("deleted the wrong account: %+v", cfg.Models)
	}
	if cfg.Model.APIKey != "sk-first" {
		t.Fatalf("the delete moved the live model: %+v", cfg.Model)
	}
}

// The same address is what an edit needs: rewriting "the first row named ds"
// rotated the other account's credential and left the addressed one alone.
func TestUpdateModelAddressesTheAccountByKeyDigest(t *testing.T) {
	first := config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"}
	second := config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"}
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = first
		c.Models = []config.ModelConfig{first, second}
	})

	code, out := doJSON(t, h, jsonReq(http.MethodPut,
		entryURL("ds", "deepseek-chat", "")+"&key="+keyFingerprint("sk-second"),
		`{"max_tokens":1234}`))
	if code != http.StatusOK {
		t.Fatalf("update the second account = %d %v", code, out)
	}
	cfg := readConfig(t, path)
	if len(cfg.Models) != 2 {
		t.Fatalf("rows = %d, want both kept", len(cfg.Models))
	}
	if cfg.Models[0].MaxTokens == 1234 {
		t.Fatalf("the edit landed on the first account: %+v", cfg.Models)
	}
	if cfg.Models[1].MaxTokens != 1234 {
		t.Fatalf("the addressed account was not edited: %+v", cfg.Models)
	}
	if cfg.Model.APIKey != "sk-first" {
		t.Fatalf("editing a spare account took the live slot: %+v", cfg.Model)
	}
}

// Only the row the engine is actually dialing is "in use". Identity that
// stopped at alias+model+endpoint lit both accounts, so the page claimed two
// live models and its one-click switch refused both tiles as already current.
func TestListModelsMarksOnlyTheLiveAccount(t *testing.T) {
	first := config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-first"}
	second := config.ModelConfig{Name: "ds", Provider: "deepseek", Model: "deepseek-chat", APIKey: "sk-second"}
	h, _ := newModelPanel(t, func(c *config.Config) {
		c.Model = second
		c.Models = []config.ModelConfig{first, second}
	})

	code, out := doJSON(t, h, authedReq(http.MethodGet, "/api/models", nil))
	if code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	rows, _ := out["models"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	live := 0
	for _, raw := range rows {
		m, _ := raw.(map[string]any)
		if m["active"] == true {
			live++
			if m["key_fingerprint"] != keyFingerprint("sk-second") {
				t.Fatalf("wrong account marked live: %v", m)
			}
		}
	}
	if live != 1 {
		t.Fatalf("live rows = %d, want exactly the one in use", live)
	}
}

// A row's endpoint is one address even when the config spells it two ways. The
// console gets its base_url from the renderer, which reports the form the
// client will dial, so a stored "https://x/v1/" and an addressed
// "https://x/v1" are the same row — strict equality answered "no such model"
// and left the operator with a row the page could not delete.
func TestSameEntryTreatsOneEndpointSpelledTwoWaysAsOneRow(t *testing.T) {
	a := config.ModelConfig{Name: "x", BaseURL: "https://api.example/v1/", Model: "m", APIKey: "k"}
	b := config.ModelConfig{Name: "x", BaseURL: "https://api.example/v1", Model: "m", APIKey: "k"}
	if !sameEntry(a, b) {
		t.Fatal("an endpoint with and without a trailing slash is one row")
	}
	// The credential is what keeps two accounts apart, and it must still be
	// part of the identity — tolerating the endpoint must not merge them.
	c := b
	c.APIKey = "other"
	if sameEntry(a, c) {
		t.Fatal("two accounts that share the endpoint are still two rows")
	}
}

func TestRemoveModelAddressesAnEndpointSpelledWithATrailingSlash(t *testing.T) {
	stale := config.ModelConfig{
		Name: "stale", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://stale.example/v1/", Model: "m", APIKey: "k1",
	}
	spare := config.ModelConfig{
		Name: "spare", Provider: "custom", APIType: config.APITypeOpenAI,
		BaseURL: "https://spare.example/v1", Model: "m", APIKey: "k2",
	}
	h, path := newModelPanel(t, func(c *config.Config) {
		c.Model = stale
		c.Models = []config.ModelConfig{spare}
	})

	code, out := doJSON(t, h, authedReq(http.MethodDelete,
		entryURL("stale", "m", "https://stale.example/v1"), nil))
	if code != http.StatusOK {
		t.Fatalf("delete a live row whose endpoint carries a trailing slash = %d %v", code, out)
	}
	if out["switched_to"] != "spare" {
		t.Fatalf("switched_to = %v, want the promoted spare", out["switched_to"])
	}
	cfg := readConfig(t, path)
	if cfg.Model.Alias() != "spare" {
		t.Fatalf("live model = %q, want spare", cfg.Model.Alias())
	}
}
