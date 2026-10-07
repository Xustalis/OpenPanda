// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"os"
	"strings"
	"testing"
)

func TestUpdateModelsSectionRoundTrip(t *testing.T) {
	p := writeTemp(t, "node:\n  name: \"n\"\n")
	models := []ModelConfig{
		{Name: "deepseek", Provider: "deepseek", APIType: "anthropic", BaseURL: "https://api.deepseek.com/anthropic", APIKey: "sk-1", Model: "deepseek-v4-flash", MaxTokens: 4096},
		{Name: "claude", Provider: "claude", APIType: "anthropic", BaseURL: "https://api.anthropic.com", APIKey: "sk-2", Model: "claude-sonnet-4-5", MaxTokens: 8192},
	}
	if err := UpdateModelsSection(p, models); err != nil {
		t.Fatalf("write models: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(cfg.Models))
	}
	if cfg.Models[0].Name != "deepseek" || cfg.Models[0].APIKey != "sk-1" {
		t.Fatalf("models[0] = %+v", cfg.Models[0])
	}
	if cfg.Models[1].Model != "claude-sonnet-4-5" {
		t.Fatalf("models[1] = %+v", cfg.Models[1])
	}
}

func TestUpdateModelsSectionEmptyRemovesKey(t *testing.T) {
	p := writeTemp(t, "node:\n  name: \"n\"\n")
	models := []ModelConfig{{Name: "a", Model: "m"}}
	if err := UpdateModelsSection(p, models); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := UpdateModelsSection(p, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Models) != 0 {
		t.Fatalf("expected empty registry, got %+v", cfg.Models)
	}
}

func TestUpdateModelsSectionPreservesOtherSections(t *testing.T) {
	p := writeTemp(t, "node:\n  name: \"keep-me\"\n  kind: \"physical\"\n")
	if err := UpdateModelsSection(p, []ModelConfig{{Name: "a", Model: "m"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Node.Name != "keep-me" {
		t.Fatalf("node section clobbered: %+v", cfg.Node)
	}
}

// TestUpdateModelSectionMaxRetries verifies the retry budget round-trips:
// a positive value lands in the file, and writing 0 removes the key so the
// built-in default applies on reload.
func TestUpdateModelSectionMaxRetries(t *testing.T) {
	p := writeTemp(t, "node:\n  name: \"n\"\n")
	mc := ModelConfig{BaseURL: "https://api.anthropic.com", Model: "m", MaxRetries: 50}
	if err := UpdateModelSection(p, mc); err != nil {
		t.Fatalf("UpdateModelSection: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.MaxRetries != 50 {
		t.Errorf("model.max_retries = %d, want 50", cfg.Model.MaxRetries)
	}

	mc.MaxRetries = 0
	if err := UpdateModelSection(p, mc); err != nil {
		t.Fatalf("UpdateModelSection clear: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "max_retries") {
		t.Errorf("max_retries=0 should remove the key:\n%s", data)
	}
}

func TestModelConfigAlias(t *testing.T) {
	if got := (ModelConfig{Name: "x", Model: "m"}).Alias(); got != "x" {
		t.Fatalf("alias = %q, want x", got)
	}
	if got := (ModelConfig{Model: "m"}).Alias(); got != "m" {
		t.Fatalf("alias = %q, want m", got)
	}
}

func TestUpdateModelSectionPersistsProviderAndName(t *testing.T) {
	p := writeTemp(t, "node:\n  name: \"n\"\n")
	mc := ModelConfig{
		Name:          "claude-primary",
		Provider:      "claude",
		APIType:       "anthropic",
		BaseURL:       "https://api.anthropic.com",
		APIKey:        "sk-ant",
		Model:         "claude-sonnet-4-5",
		MaxTokens:     8192,
		ContextWindow: 200000,
	}
	if err := UpdateModelSection(p, mc); err != nil {
		t.Fatalf("UpdateModelSection: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model.Name != "claude-primary" {
		t.Errorf("model.name = %q, want claude-primary", cfg.Model.Name)
	}
	if cfg.Model.Provider != "claude" {
		t.Errorf("model.provider = %q, want claude", cfg.Model.Provider)
	}
	if cfg.Model.ContextWindow != 200000 {
		t.Errorf("model.context_window = %d, want 200000", cfg.Model.ContextWindow)
	}
	if cfg.Model.MaxTokens != 8192 {
		t.Errorf("model.max_tokens = %d, want 8192", cfg.Model.MaxTokens)
	}
}
