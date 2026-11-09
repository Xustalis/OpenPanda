// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

func TestModelListReadsConfigWithoutSchedulerOrSecrets(t *testing.T) {
	m := config.ModelConfig{Name: "local-model", Provider: "custom", Model: "small-model", APIKey: "do-not-expose", BaseURL: "https://private.example/token"}
	e := &Engine{cfg: &config.Config{Model: m, Models: []config.ModelConfig{m}}}
	r := entry.NewRegistry()
	registerModelTools(r, e)
	tool, ok := r.Lookup("model_list")
	if !ok || tool.Tier != defense.TierReversible {
		t.Fatal("missing read-only tool")
	}
	out, err := tool.Run(context.Background(), nil)
	if err != nil || !strings.Contains(out, "small-model") || !strings.Contains(out, "active") {
		t.Fatalf("%s: %v", out, err)
	}
	for _, secret := range []string{m.APIKey, m.BaseURL, "api_key", "base_url"} {
		if strings.Contains(out, secret) {
			t.Fatalf("model list leaked %q", secret)
		}
	}
	e.cfgMu.Lock()
	e.cfg.Model.Model = "updated-model"
	e.cfgMu.Unlock()
	out, err = tool.Run(context.Background(), nil)
	if err != nil || !strings.Contains(out, "updated-model") {
		t.Fatalf("tool captured stale configuration: %s %v", out, err)
	}
}
