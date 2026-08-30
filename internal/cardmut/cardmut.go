// Package cardmut implements structured edits to a node's capability card
// (capabilities.yaml): add/remove/set of native abilities, agents and manual
// abilities without opening an editor.
//
// Every mutation follows the same pipeline — read the YAML as a yaml.Node,
// edit only the touched subtree, re-validate the whole document through
// ledger.LoadCard, keep a .bak, then install the new file with a same-directory
// rename. Editing as yaml.Node (instead of unmarshal → struct → marshal) is
// what keeps the comments a user hand-wrote in the untouched sections; only
// the fields a command actually names are regenerated. Validating before the
// write is what keeps a typo from taking the node down at its next start.
//
// The package is the single mutation path shared by the CLI (`panda card
// native add …`), the REPL/TUI (/card …) and the web panel's card API, so all
// three front ends agree on semantics — notably "add an existing id" and
// "remove a missing id" are errors, not silent no-ops, because a card edit a
// user cannot distinguish from a failed one is worse than no edit at all.
package cardmut

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Xustalis/OpenPanda/internal/ledger"
	"gopkg.in/yaml.v3"
)

// NativeAdd appends one native ability to the card. An existing id is an error
// (remove it first, or pick another id) — overwriting silently would hide a
// typo that meant to edit, not replace.
func NativeAdd(path string, ab ledger.NativeAbility) error {
	return mutate(path, func(top *yaml.Node) error {
		native, err := ensureSequence(top, "native")
		if err != nil {
			return err
		}
		for _, id := range sequenceIDs(native) {
			if id == ab.ID {
				return fmt.Errorf("native ability %q already exists (remove it first: panda card native remove %s)", ab.ID, ab.ID)
			}
		}
		node, err := structNode(ab)
		if err != nil {
			return err
		}
		native.Content = append(native.Content, node)
		return nil
	})
}

// NativeRemove deletes the native ability with the given id.
func NativeRemove(path string, id string) error {
	return mutate(path, func(top *yaml.Node) error {
		native := mapValue(top, "native")
		if native == nil || !removeSequenceItemByID(native, id) {
			return fmt.Errorf("native ability %q not found", id)
		}
		return nil
	})
}

// AgentAdd registers one agent CLI under name. An existing name is an error,
// mirroring NativeAdd.
func AgentAdd(path string, name string, ag ledger.Agent) error {
	if name == "" {
		return fmt.Errorf("agent name must not be empty")
	}
	return mutate(path, func(top *yaml.Node) error {
		agents, err := ensureMapping(top, "agents")
		if err != nil {
			return err
		}
		if mapValue(agents, name) != nil {
			return fmt.Errorf("agent %q already exists (remove it first: panda card agent remove %s)", name, name)
		}
		node, err := structNode(ag)
		if err != nil {
			return err
		}
		key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
		agents.Content = append(agents.Content, key, node)
		return nil
	})
}

// AgentRemove deletes the named agent from the card.
func AgentRemove(path string, name string) error {
	return mutate(path, func(top *yaml.Node) error {
		agents := mapValue(top, "agents")
		if agents == nil || !removeMapKey(agents, name) {
			return fmt.Errorf("agent %q not found", name)
		}
		return nil
	})
}

// AgentUpdate carries the fields AgentSet may change. A nil field leaves the
// card's value alone; only the non-nil ones are rewritten, so a one-field
// `agent set opencode tier=1` does not clobber best_at or capabilities.
type AgentUpdate struct {
	Adapter      *string
	InstallCheck *string
	Capabilities *[]string
	BestAt       *[]string
	NotFor       *[]string
	CostTier     *string
	Tier         *int
}

// AgentSet applies a partial update to one agent, field by field, so the
// agent's other entries — and their comments — survive untouched.
func AgentSet(path string, name string, upd AgentUpdate) error {
	return mutate(path, func(top *yaml.Node) error {
		agents := mapValue(top, "agents")
		ag := mapValue(agents, name)
		if ag == nil {
			return fmt.Errorf("agent %q not found", name)
		}
		if upd.Adapter != nil {
			setMapScalar(ag, "adapter", *upd.Adapter)
		}
		if upd.InstallCheck != nil {
			setMapScalar(ag, "install_check", *upd.InstallCheck)
		}
		if upd.Capabilities != nil {
			setMapSeq(ag, "capabilities", *upd.Capabilities)
		}
		if upd.BestAt != nil {
			setMapSeq(ag, "best_at", *upd.BestAt)
		}
		if upd.NotFor != nil {
			setMapSeq(ag, "not_for", *upd.NotFor)
		}
		if upd.CostTier != nil {
			setMapScalar(ag, "cost_tier", *upd.CostTier)
		}
		if upd.Tier != nil {
			setMapScalarTagged(ag, "tier", fmt.Sprint(*upd.Tier), "!!int")
		}
		return nil
	})
}

// ManualAdd appends one manual (human-performed) ability.
func ManualAdd(path string, ab ledger.ManualAbility) error {
	return mutate(path, func(top *yaml.Node) error {
		manual, err := ensureSequence(top, "manual")
		if err != nil {
			return err
		}
		for _, id := range sequenceIDs(manual) {
			if id == ab.ID {
				return fmt.Errorf("manual ability %q already exists", ab.ID)
			}
		}
		node, err := structNode(ab)
		if err != nil {
			return err
		}
		manual.Content = append(manual.Content, node)
		return nil
	})
}

// ManualRemove deletes the manual ability with the given id.
func ManualRemove(path string, id string) error {
	return mutate(path, func(top *yaml.Node) error {
		manual := mapValue(top, "manual")
		if manual == nil || !removeSequenceItemByID(manual, id) {
			return fmt.Errorf("manual ability %q not found", id)
		}
		return nil
	})
}

// WriteRaw installs a whole-card replacement (the raw YAML editor path —
// `panda card edit` and the panel's PUT /api/card). Same safety contract as
// the structured edits: the candidate document must round-trip through
// ledger.LoadCard before anything is written, the previous card is kept as
// .bak, and the install is a same-directory rename so a concurrent reader
// never sees a truncated file. A failed validation leaves the file exactly
// as it was.
func WriteRaw(path string, data []byte) error {
	if err := validateBytes(data); err != nil {
		return fmt.Errorf("edited card is invalid, nothing was written: %w", err)
	}
	if err := backup(path); err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".capabilities-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// mutate is the shared pipeline: read the card as a document, run the edit,
// then validate → back up → install atomically. A failed edit or a failed
// validation leaves the file on disk exactly as it was.
func mutate(path string, edit func(top *yaml.Node) error) error {
	root, err := loadDoc(path)
	if err != nil {
		return err
	}
	top := root.Content[0]
	if err := edit(top); err != nil {
		return err
	}
	return installDoc(path, root)
}

// loadDoc reads the card into its document node. A missing card is an error
// rather than a fresh document: this package edits a card that exists, and
// inventing device/chip/capacity out of thin air is `panda card rescan
// --write`'s job, not a mutation's.
func loadDoc(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no capability card at %s — create one first with `panda card rescan --write`", path)
		}
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse card %s: %w", path, err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("card %s: not a YAML mapping", path)
	}
	return &root, nil
}

// installDoc validates the edited document through the loader the daemon uses,
// keeps a .bak of the previous card, then writes via temp file + rename so a
// concurrent reader sees the old card or the new one, never a truncated mix.
func installDoc(path string, root *yaml.Node) error {
	data, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	if err := validateBytes(data); err != nil {
		return fmt.Errorf("edited card is invalid, nothing was written: %w", err)
	}
	if err := backup(path); err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".capabilities-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// validateBytes round-trips a candidate card through ledger.LoadCard's own
// checks without touching the target file.
func validateBytes(data []byte) error {
	tmp, err := os.CreateTemp("", "panda-cardmut-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	_, lerr := ledger.LoadCard(tmp.Name())
	return lerr
}

// backup copies the current card to <path>.bak, same contract as `panda card
// edit`'s: a mutation overwrites tuned values, and the way back must not
// depend on remembering them.
func backup(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.WriteFile(path+".bak", data, 0o644)
}

// mapValue returns the value node paired with key in mapping m, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// ensureMapping returns the mapping stored under top key, creating the entry
// (with a neutral style) when absent. A present-but-wrong-kind entry is an
// error the user must fix in the file, not something to silently overwrite.
func ensureMapping(top *yaml.Node, key string) (*yaml.Node, error) {
	if v := mapValue(top, key); v != nil {
		if v.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("card field %q is not a mapping — fix it in the file first", key)
		}
		return v, nil
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	top.Content = append(top.Content, k, v)
	return v, nil
}

// ensureSequence returns the sequence stored under top key, creating the entry
// when absent.
func ensureSequence(top *yaml.Node, key string) (*yaml.Node, error) {
	if v := mapValue(top, key); v != nil {
		if v.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("card field %q is not a list — fix it in the file first", key)
		}
		return v, nil
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	top.Content = append(top.Content, k, v)
	return v, nil
}

// structNode renders v (a card struct) as the equivalent YAML node tree, so a
// freshly added entry carries proper tags and styles without hand-building
// every field node.
func structNode(v any) (*yaml.Node, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty document for %T", v)
	}
	return doc.Content[0], nil
}

// sequenceIDs lists the id field of every mapping item in sequence s.
func sequenceIDs(s *yaml.Node) []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.Content))
	for _, item := range s.Content {
		if id := mapValue(item, "id"); id != nil {
			out = append(out, id.Value)
		}
	}
	return out
}

// removeSequenceItemByID drops the mapping item whose id field equals id.
// Reports whether anything was removed.
func removeSequenceItemByID(s *yaml.Node, id string) bool {
	if s == nil || s.Kind != yaml.SequenceNode {
		return false
	}
	for i, item := range s.Content {
		if f := mapValue(item, "id"); f != nil && f.Value == id {
			s.Content = append(s.Content[:i], s.Content[i+1:]...)
			return true
		}
	}
	return false
}

// removeMapKey drops key (and its value) from mapping m. Reports whether the
// key existed.
func removeMapKey(m *yaml.Node, key string) bool {
	if m == nil || m.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

// setMapScalar upserts key: value in mapping m, preserving position and the
// comments of neighbouring entries (existing duplicates of the key are dropped
// first).
func setMapScalar(m *yaml.Node, key, value string) {
	setMapScalarTagged(m, key, value, "!!str")
}

// setMapScalarTagged is setMapScalar with an explicit scalar tag — needed
// where the card struct's field type is not a string (Agent.Tier is an int,
// and a !!str node would serialize as tier: "1", which LoadCard refuses).
func setMapScalarTagged(m *yaml.Node, key, value, tag string) {
	removeMapKey(m, key)
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
	m.Content = append(m.Content, k, v)
}

// setMapSeq upserts key: [values…] in mapping m, as a block-style sequence of
// scalars.
func setMapSeq(m *yaml.Node, key string, values []string) {
	removeMapKey(m, key)
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, item := range values {
		v.Content = append(v.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item})
	}
	m.Content = append(m.Content, k, v)
}
