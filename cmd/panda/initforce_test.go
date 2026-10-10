// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// mergePreservedConfig is the --force contract: user-owned settings survive
// the rewrite, machine-observed node fields are re-detected, and an explicit
// new model choice beats the preserved section.
func TestMergePreservedConfig(t *testing.T) {
	old := []byte(`
node:
  name: old-name
  resource_class: Micro
  kind: vm
  identity: keep-me
network:
  listen_addr: "127.0.0.1:9999"
  shared_secret: "s3cret"
  peers: ["ws://peer:7836"]
model:
  provider: deepseek
  model: deepseek-chat
  base_url: "https://api.deepseek.com"
ui:
  locale: zh-CN
`)
	def := &config.Config{
		Node: config.NodeConfig{
			Name: "new-host", ResourceClass: "Standard", Kind: "physical", Identity: "fresh-vm",
		},
		Network: config.NetworkConfig{SharedSecret: "fresh-secret"},
		Model:   config.ModelConfig{Provider: "newpick", Model: "x"},
	}
	if !mergePreservedConfig(old, def, false) {
		t.Fatal("merge should succeed")
	}
	// Machine fields re-detected.
	if def.Node.Name != "new-host" || def.Node.ResourceClass != "Standard" || def.Node.Kind != "physical" {
		t.Errorf("node fields should be re-detected, got %+v", def.Node)
	}
	// Identity is a user choice, kept.
	if def.Node.Identity != "keep-me" {
		t.Errorf("identity = %q, want keep-me", def.Node.Identity)
	}
	// User-owned sections survive.
	if def.Network.SharedSecret != "s3cret" || def.Network.ListenAddr != "127.0.0.1:9999" {
		t.Errorf("network section should be preserved, got %+v", def.Network)
	}
	if len(def.Network.Peers) != 1 || def.Network.Peers[0] != "ws://peer:7836" {
		t.Errorf("peers should be preserved, got %v", def.Network.Peers)
	}
	if def.UI.Locale != "zh-CN" {
		t.Errorf("ui.locale = %q, want zh-CN", def.UI.Locale)
	}
	// No fresh model answer → old model section wins.
	if def.Model.Provider != "deepseek" || def.Model.Model != "deepseek-chat" {
		t.Errorf("old model should be kept, got %+v", def.Model)
	}
}

func TestMergePreservedConfigNewModelWins(t *testing.T) {
	old := []byte("model:\n  provider: old\n  model: old-model\n")
	def := &config.Config{
		Node:  config.NodeConfig{Name: "h", ResourceClass: "Standard", Kind: "physical"},
		Model: config.ModelConfig{Provider: "newpick", Model: "new-model"},
	}
	if !mergePreservedConfig(old, def, true) {
		t.Fatal("merge should succeed")
	}
	if def.Model.Provider != "newpick" || def.Model.Model != "new-model" {
		t.Errorf("explicit model choice should win, got %+v", def.Model)
	}
}

func TestMergePreservedConfigAbsentIdentity(t *testing.T) {
	// An old file without node.identity adopts the fresh detection's VM id.
	old := []byte("node:\n  name: old\n")
	def := &config.Config{
		Node: config.NodeConfig{Name: "h", ResourceClass: "Standard", Kind: "vm", Identity: "vm-1"},
	}
	if !mergePreservedConfig(old, def, false) {
		t.Fatal("merge should succeed")
	}
	if def.Node.Identity != "vm-1" {
		t.Errorf("identity = %q, want fresh vm-1", def.Node.Identity)
	}
}

func TestMergePreservedConfigBadYAML(t *testing.T) {
	def := &config.Config{Node: config.NodeConfig{Name: "h"}}
	if mergePreservedConfig([]byte("{not yaml: ["), def, false) {
		t.Fatal("unparseable old config should report false")
	}
	if def.Node.Name != "h" {
		t.Error("def must be untouched on parse failure")
	}
}
