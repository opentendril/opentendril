package main

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Merge/append mode for `tendril git setup`. A deployment has multiple
// repositories and multiple agent subjects, so re-running setup must be
// additive: upsert this run's entries into an existing substrates.yaml /
// grants.yaml while preserving every other entry AND the user's comments and
// formatting. The work is done on the YAML node tree (a surgical insert/update)
// rather than by re-marshalling typed structs, so hand-edits survive intact.
//
// Rules:
//   - A new named entry (credential profile, substrate) is added freely.
//   - An existing named entry is overwritten only with --force (otherwise a
//     clear error) — a specific connection is never silently replaced.
//   - A grant subject's operation-classes and substrates are UNIONED, so
//     granting an existing agent access to a new repository is additive and
//     needs no --force.

// fileExists reports whether path is an existing file.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// upsertSubstrates writes a fresh substrates.yaml (with header comments) when
// none exists, or merges this run's connection into an existing file.
func upsertSubstrates(path string, o gitSetupOptions) error {
	if !fileExists(path) {
		if err := os.WriteFile(path, []byte(renderSubstratesYAML(o)), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "🌱 Wrote %s\n", path)
		return nil
	}
	if err := mergeConnection(path, o); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "🌱 Updated %s (connection %q)\n", path, o.substrate)
	return nil
}

// upsertGrants writes a fresh grants.yaml when none exists, or merges this run's
// subject into an existing file (unioning its operation-classes and substrates).
func upsertGrants(path string, o gitSetupOptions) error {
	if !fileExists(path) {
		if err := os.WriteFile(path, []byte(renderGrantsYAML(o)), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "🌱 Wrote %s\n", path)
		return nil
	}
	if err := mergeGrant(path, o); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "🌱 Updated %s (subject %q)\n", path, o.grantSubject)
	return nil
}

// loadYAMLMapping reads path's top-level mapping node, or returns a fresh empty
// mapping when the file does not exist. Comments on existing nodes are retained
// (yaml.v3 records them on the node tree and re-emits them on marshal).
func loadYAMLMapping(path string) (*yaml.Node, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &yaml.Node{Kind: yaml.MappingNode}, nil
		}
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: expected a top-level mapping", path)
	}
	return root, nil
}

// writeYAMLMapping marshals root back to path at a 2-space indent, matching the
// style of a freshly generated file.
func writeYAMLMapping(path string, root *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		_ = enc.Close()
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// mapValue returns the value node for key within mapping m and the value's
// index in m.Content, or (nil, -1) when the key is absent.
func mapValue(m *yaml.Node, key string) (*yaml.Node, int) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1], i + 1
		}
	}
	return nil, -1
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

// getOrCreateMapping returns the mapping value for key under m, creating an
// empty mapping (and its key) when absent.
func getOrCreateMapping(m *yaml.Node, key string) *yaml.Node {
	if v, _ := mapValue(m, key); v != nil {
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode}
	m.Content = append(m.Content, scalarNode(key), v)
	return v
}

// getOrCreateSequence returns the sequence value for key under m, creating an
// empty sequence (and its key) when absent.
func getOrCreateSequence(m *yaml.Node, key string) *yaml.Node {
	if v, _ := mapValue(m, key); v != nil {
		return v
	}
	// Flow style ([a, b]) matches the compact sequences the fresh-file
	// templates emit, so freshly created and preserved lists render alike.
	v := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	m.Content = append(m.Content, scalarNode(key), v)
	return v
}

// setMapEntry upserts key->value in m; an existing key is replaced only when
// force is set, otherwise it errors so a specific entry is never clobbered.
func setMapEntry(m *yaml.Node, key string, value *yaml.Node, force bool) error {
	if v, idx := mapValue(m, key); v != nil {
		if !force {
			return fmt.Errorf("%q already exists (pass --force to update it)", key)
		}
		m.Content[idx] = value
		return nil
	}
	m.Content = append(m.Content, scalarNode(key), value)
	return nil
}

// ensureSequenceContains appends value to seq when not already present (union).
func ensureSequenceContains(seq *yaml.Node, value string) {
	for _, e := range seq.Content {
		if e.Value == value {
			return
		}
	}
	seq.Content = append(seq.Content, scalarNode(value))
}

// parseMappingFragment parses a YAML fragment into its top-level mapping node,
// used to turn a rendered value block into a node ready to insert.
func parseMappingFragment(fragment string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fragment), &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("rendered fragment is not a mapping")
	}
	return doc.Content[0], nil
}

// mergeConnection upserts the run's credential profile and substrate into an
// existing substrates.yaml, preserving every other entry.
func mergeConnection(path string, o gitSetupOptions) error {
	root, err := loadYAMLMapping(path)
	if err != nil {
		return err
	}
	profile := o.substrate + "-connection"
	profileNode, err := parseMappingFragment(renderProfileValueYAML(o))
	if err != nil {
		return err
	}
	if err := setMapEntry(getOrCreateMapping(root, "credentials"), profile, profileNode, o.force); err != nil {
		return fmt.Errorf("credentials.%s: %w", profile, err)
	}
	substrateNode, err := parseMappingFragment(renderSubstrateValueYAML(o))
	if err != nil {
		return err
	}
	if err := setMapEntry(getOrCreateMapping(root, "substrates"), o.substrate, substrateNode, o.force); err != nil {
		return fmt.Errorf("substrates.%s: %w", o.substrate, err)
	}
	return writeYAMLMapping(path, root)
}

// mergeGrant upserts the run's subject into an existing grants.yaml. When the
// subject already exists, its operation-classes and substrates are unioned, so
// authorising an existing agent on a new repository is additive.
func mergeGrant(path string, o gitSetupOptions) error {
	root, err := loadYAMLMapping(path)
	if err != nil {
		return err
	}
	grants := getOrCreateMapping(root, "grants")
	subject, _ := mapValue(grants, o.grantSubject)
	if subject == nil {
		subject = &yaml.Node{Kind: yaml.MappingNode}
		grants.Content = append(grants.Content, scalarNode(o.grantSubject), subject)
	}
	classes := getOrCreateSequence(subject, "operationClasses")
	ensureSequenceContains(classes, "git.status")
	ensureSequenceContains(classes, "git.branch.list")
	ensureSequenceContains(classes, "git.branch")
	ensureSequenceContains(classes, "git.commit")
	ensureSequenceContains(classes, "git.push")
	ensureSequenceContains(classes, "git.pr")
	ensureSequenceContains(getOrCreateSequence(subject, "substrates"), o.substrate)
	return writeYAMLMapping(path, root)
}
