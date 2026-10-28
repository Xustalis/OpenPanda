// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/commander"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/storage"
)

// newTestDB builds a migrated, empty database the growth check can count.
func newTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "panda.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return path
}

// checkDBGrowth must stay silent when the database cannot be read — the
// sibling db.ok check is the one that reports reachability.
func TestCheckDBGrowthUnreachable(t *testing.T) {
	cfg := &config.Config{Storage: config.StorageConfig{
		DBPath: filepath.Join(t.TempDir(), "missing", "panda.db"),
	}}
	if c := checkDBGrowth(cfg); c.Key != "" {
		t.Fatalf("expected silent check on missing db, got %q", c.Key)
	}
}

// An empty database reports growth counts and passes.
func TestCheckDBGrowthEmpty(t *testing.T) {
	cfg := &config.Config{Storage: config.StorageConfig{DBPath: newTestDB(t)}}
	c := checkDBGrowth(cfg)
	if c.Key != "doctor.db.growth.ok" || !c.OK {
		t.Fatalf("expected doctor.db.growth.ok pass, got %+v", c)
	}
}

// A large settled backlog with retention disabled is the configuration the
// check exists to catch; enabling the knob must silence it.
func TestCheckDBGrowthSettledBacklog(t *testing.T) {
	path := newTestDB(t)
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for i := 0; i < doctorSettledWarn+1; i++ {
		if _, err := tx.Exec(
			`INSERT INTO tasks (task_id, state, owner_node, attempt_id) VALUES (?, 'done', 'n', ?)`,
			fmt.Sprintf("t-%d", i), fmt.Sprintf("a-%d", i)); err != nil {
			t.Fatalf("seed task %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	db.Close()

	zero := 0
	cfg := &config.Config{Storage: config.StorageConfig{
		DBPath:            path,
		TaskRetentionDays: &zero, // explicitly keep forever
	}}
	if c := checkDBGrowth(cfg); c.Key != "doctor.db.growth.no" || c.OK {
		t.Fatalf("expected doctor.db.growth.no, got %+v", c)
	}
	cfg.Storage.TaskRetentionDays = nil // default retention is armed
	if c := checkDBGrowth(cfg); c.Key != "doctor.db.growth.ok" || !c.OK {
		t.Fatalf("armed retention should pass, got %+v", c)
	}
}

// Run's own contract: every check carries its i18n key and a verdict — the
// report never renders a blank line.
func TestRunChecksAreWellFormed(t *testing.T) {
	for _, c := range Run(filepath.Join(t.TempDir(), "missing-config.yaml")) {
		if c.Key == "" {
			t.Fatalf("check with empty key: %+v", c)
		}
	}
}

// The 401-with-no-key case is a pass, not a failure — but only when the
// endpoint detail really is an auth-class status and no key was probed.
func TestEndpointProbeKeyless(t *testing.T) {
	for _, tc := range []struct {
		detail, apiKey string
		want           bool
	}{
		{"http 401", "", true},
		{"http 403", "", true},
		{"http 429", "", true},
		{"http 200", "", false},      // healthy response is just a pass
		{"http 401", "sk-x", false},  // with a key, 401 means the key was rejected
		{"http 500", "", false},      // server error is unreachable, not "unproven"
		{"timeout", "", false},       // transport failure
		{"http 401 extra", "", true}, // detail may append latency etc.
	} {
		if got := endpointProbeKeyless(tc.detail, tc.apiKey); got != tc.want {
			t.Fatalf("endpointProbeKeyless(%q, %q) = %v, want %v", tc.detail, tc.apiKey, got, tc.want)
		}
	}
}

// A model the resolver cannot point anywhere used to be silent here: the
// endpoint probe was skipped and the only visible line was "api key
// configured", which is precisely the state that hands a key to nobody. The
// check must fire whenever the injection path would refuse — and stay quiet on
// a fresh node, where modelkey.no already carries the message.
func TestMissingEndpointCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.ModelConfig
		want string // empty means "no line"
		pair string
	}{
		{
			name: "custom row with no base_url",
			cfg:  config.ModelConfig{Provider: "custom", APIKey: "sk-x"},
			want: "doctor.modelendpoint.none",
			pair: "custom",
		},
		{
			name: "a misspelled provider id has the same shape",
			cfg:  config.ModelConfig{Provider: "deapseek", APIKey: "sk-x"},
			want: "doctor.modelendpoint.none",
			pair: "deapseek",
		},
		{
			name: "a key with neither provider nor base_url",
			cfg:  config.ModelConfig{APIKey: "sk-x"},
			want: "doctor.modelendpoint.unset",
		},
		{
			name: "fresh install: nothing is named, so nothing to explain",
			cfg:  config.ModelConfig{},
		},
		{
			name: "whitespace is not a provider",
			cfg:  config.ModelConfig{Provider: "  ", BaseURL: "\t"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, show := missingEndpointCheck(tc.cfg)
			if tc.want == "" {
				if show {
					t.Fatalf("expected no line, got %+v", c)
				}
				return
			}
			if !show || c.Key != tc.want || c.OK {
				t.Fatalf("want failing %s, got show=%v %+v", tc.want, show, c)
			}
			// The line must never leave an unresolved placeholder: it is
			// rendered through Tf, which keeps "{provider}" verbatim.
			for _, loc := range i18n.Locales {
				msg := i18n.Tf(loc, c.Key, c.Pairs...)
				if strings.Contains(msg, "{") {
					t.Fatalf("%s leaves a placeholder: %q", loc, msg)
				}
				if tc.pair != "" && !strings.Contains(msg, tc.pair) {
					t.Fatalf("%s drops the provider id: %q", loc, msg)
				}
			}
		})
	}
}

// The check must fire exactly when the injection path refuses, otherwise
// doctor says "fine" about a model that will be dropped at dispatch.
func TestMissingEndpointCheckTracksTheResolver(t *testing.T) {
	for _, cfg := range []config.ModelConfig{
		{Provider: "custom", APIKey: "sk-x"},
		{Provider: "custom", APIKey: "sk-x", APIType: config.APITypeAnthropic},
		{Provider: "nosuchprovider", APIKey: "sk-x"},
		{Provider: "deepseek", APIKey: "sk-x"},
		{Provider: "opencode-go", APIKey: "sk-x"},
	} {
		ep := commander.EffectiveBaseURL(cfg)
		_, show := missingEndpointCheck(cfg)
		if ep == "" && !show {
			t.Fatalf("resolver returns no endpoint for %q but doctor stays silent", cfg.Provider)
		}
		if ep != "" && show {
			t.Fatalf("resolver resolves %q to %q yet doctor reports no endpoint", cfg.Provider, ep)
		}
	}
}
