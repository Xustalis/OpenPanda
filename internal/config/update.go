package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// UpdateSectionField persists one scalar string field inside a (possibly
// nested) YAML section of the config file at path, e.g. UpdateSectionField(
// path, []string{"approval"}, "mode", "never"). Like UpdateModelSection it
// round-trips the document as a yaml.Node so every other section — and its
// comments — survives byte-for-byte. Missing sections are created on the way
// down; an empty value removes the field so defaults apply again. The file is
// created from defaults when missing.
func UpdateSectionField(path string, section []string, key, value string) error {
	m, root, err := locateSection(path, section)
	if err != nil {
		return err
	}
	setMapField(m, key, value)
	return writeDoc(path, root)
}

// UpdateSectionFieldInt is UpdateSectionField for integer fields: the value
// lands with an !!int tag so numeric config fields (memory.limits.*) reload
// as numbers instead of quoted strings.
func UpdateSectionFieldInt(path string, section []string, key string, value int) error {
	m, root, err := locateSection(path, section)
	if err != nil {
		return err
	}
	setMapIntField(m, key, value)
	return writeDoc(path, root)
}

// setMapIntField upserts key: <int> in mapping node m (removing the key when
// value is zero is NOT implied — callers pass the concrete number; use
// setMapField with "" to remove).
func setMapIntField(m *yaml.Node, key string, value int) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1].Value = strconv.Itoa(value)
			m.Content[i+1].Tag = "!!int"
			m.Content[i+1].Style = 0
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)},
	)
}

// locateSection loads the document at path for an edit and walks (creating as
// needed) the nested section path, returning the innermost mapping node.
func locateSection(path string, section []string) (*yaml.Node, *yaml.Node, error) {
	root, top, err := loadDocForUpdate(path)
	if err != nil {
		return nil, nil, err
	}
	m := top
	for _, name := range section {
		child := mappingValue(m, name)
		if child == nil {
			keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			m.Content = append(m.Content, keyNode, child)
		}
		if child.Kind != yaml.MappingNode {
			return nil, nil, fmt.Errorf("config %s: %s is not a mapping", path, name)
		}
		m = child
	}
	return m, root, nil
}

// UpdateSectionList persists one string-list field inside a (possibly nested)
// YAML section, e.g. UpdateSectionList(path, []string{"routing"},
// "preferred_agents", []string{"codex"}). Comments elsewhere survive as with
// UpdateSectionField; an empty list removes the field.
func UpdateSectionList(path string, section []string, key string, values []string) error {
	m, root, err := locateSection(path, section)
	if err != nil {
		return err
	}

	// Drop any existing entry for the key first (a duplicate key would be a
	// parse error on reload).
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			break
		}
	}
	if len(values) > 0 {
		items := make([]*yaml.Node, 0, len(values))
		for _, v := range values {
			items = append(items, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
		}
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items},
		)
	}

	return writeDoc(path, root)
}

// loadDocForUpdate reads the config at path into a yaml.Node document for an
// in-place edit, creating a defaults-based document when the file is missing
// (the same contract UpdateModelSection/UpdateMCPSection follow).
func loadDocForUpdate(path string) (root *yaml.Node, top *yaml.Node, err error) {
	data, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		root = &yaml.Node{}
		if err := yaml.Unmarshal(data, root); err != nil {
			return nil, nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
			return nil, nil, fmt.Errorf("config %s: not a YAML mapping", path)
		}
		return root, root.Content[0], nil
	case os.IsNotExist(rerr):
		// Materialize defaults on disk first so the edit lands in a complete,
		// valid file (mirrors UpdateModelSection's missing-file behavior).
		out, merr := yaml.Marshal(Default())
		if merr != nil {
			return nil, nil, merr
		}
		if werr := os.WriteFile(path, out, 0o600); werr != nil {
			return nil, nil, werr
		}
		root = &yaml.Node{}
		if uerr := yaml.Unmarshal(out, root); uerr != nil || len(root.Content) == 0 {
			return nil, nil, fmt.Errorf("config %s: not a YAML mapping", path)
		}
		return root, root.Content[0], nil
	default:
		return nil, nil, fmt.Errorf("read config %s: %w", path, rerr)
	}
}

// writeDoc serializes the edited document back to path with secret-safe
// permissions.
func writeDoc(path string, root *yaml.Node) error {
	out, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return err
	}
	hardenSecretPerms(path, out)
	return nil
}
