package core

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Delegation grant storage (control-plane config lane).
//
// Grants live in the Stem's OWN .tendril directory — .tendril/grants.yaml —
// and nowhere else. Unlike substrates.yaml (credentials/connections, searched
// across candidate locations), grants are deliberately loaded from this one
// control-plane location and are never discovered inside a Substrate
// checkout: a grants file carried by a cloned repository must not be able to
// widen what that repository's Sprouts may do (no Substrate self-escalation).
// A missing file is not an error — it is the secure default: zero grants,
// delegation impossible, all non-delegated behavior unchanged.

// DelegationGrantsFilename is the grants file name inside the Stem's own
// .tendril control-plane directory.
const DelegationGrantsFilename = "grants.yaml"

// delegationGrantsFile maps .tendril/grants.yaml. Grants are keyed by
// pollen — the Pollinator / Phytomer / mesh trust-root identity
// exercising them.
type delegationGrantsFile struct {
	Grants map[string]delegationGrantSpec `yaml:"grants"`
}

// delegationGrantSpec is one subject's grant as configured.
type delegationGrantSpec struct {
	// OperationClasses allow-lists the delegable operation-classes.
	OperationClasses []string `yaml:"operationClasses"`
	// Substrates scopes the grant to named substrates.
	Substrates []string `yaml:"substrates"`
	// Egress allow-lists reachable hosts; empty means deny-all.
	Egress []string `yaml:"egress,omitempty"`
	// Expires ends the grant: an RFC 3339 timestamp or a bare YYYY-MM-DD date
	// (which expires at the start of that UTC day). Empty means no expiry.
	Expires string `yaml:"expires,omitempty"`
	// ConfirmAbove escalates invocations back to the Botanist.
	ConfirmAbove delegationConfirmSpec `yaml:"confirmAbove,omitempty"`
}

// delegationConfirmSpec bounds a grant with a human-confirmation threshold.
type delegationConfirmSpec struct {
	// Impact is "low", "medium", or "high".
	Impact string `yaml:"impact,omitempty"`
}

// LoadDelegationGrants reads the delegation grants from the Stem's
// control-plane directory (<tendrilDir>/grants.yaml). A missing file yields
// zero grants — the secure default. A malformed file is an error so a typo
// never silently loosens or reshapes policy; callers should degrade to zero
// grants (deny all delegation), never fail open.
func LoadDelegationGrants(tendrilDir string) ([]DelegationGrant, error) {
	path := filepath.Join(strings.TrimSpace(tendrilDir), DelegationGrantsFilename)

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read delegation grants %s: %w", path, err)
	}

	var file delegationGrantsFile
	if err := yaml.Unmarshal(content, &file); err != nil {
		return nil, fmt.Errorf("decode delegation grants %s: %w", path, err)
	}

	grants := make([]DelegationGrant, 0, len(file.Grants))
	for pollen, spec := range file.Grants {
		grant, err := grantFromSpec(pollen, spec)
		if err != nil {
			return nil, fmt.Errorf("delegation grants %s: %w", path, err)
		}
		grants = append(grants, grant)
	}

	// Map iteration is unordered; sort by pollen so loading is deterministic.
	sort.Slice(grants, func(i, j int) bool { return grants[i].Pollen < grants[j].Pollen })
	return grants, nil
}

// grantFromSpec validates and converts one configured grant. Every grant must
// name at least one operation-class and one substrate: a grant that can match
// nothing is a configuration mistake, surfaced at load rather than silently
// carried.
func grantFromSpec(pollen string, spec delegationGrantSpec) (DelegationGrant, error) {
	pollen = strings.TrimSpace(pollen)
	if pollen == "" {
		return DelegationGrant{}, fmt.Errorf("grant with an empty pollen")
	}

	operationClasses := trimNonEmpty(spec.OperationClasses)
	if len(operationClasses) == 0 {
		return DelegationGrant{}, fmt.Errorf("grant for pollen %q names no operationClasses", pollen)
	}
	substrates := trimNonEmpty(spec.Substrates)
	if len(substrates) == 0 {
		return DelegationGrant{}, fmt.Errorf("grant for pollen %q names no substrates", pollen)
	}

	grant := DelegationGrant{
		Pollen:           pollen,
		OperationClasses: operationClasses,
		Substrates:       substrates,
		Egress:           trimNonEmpty(spec.Egress),
	}

	if raw := strings.TrimSpace(spec.Expires); raw != "" {
		expires, err := parseDelegationExpiry(raw)
		if err != nil {
			return DelegationGrant{}, fmt.Errorf("grant for pollen %q: %w", pollen, err)
		}
		grant.Expires = expires
	}

	if impact := strings.ToLower(strings.TrimSpace(spec.ConfirmAbove.Impact)); impact != "" {
		switch impact {
		case DelegationImpactLow, DelegationImpactMedium, DelegationImpactHigh:
			grant.ConfirmAboveImpact = impact
		default:
			return DelegationGrant{}, fmt.Errorf(
				"grant for pollen %q: confirmAbove.impact %q is not one of low, medium, high",
				pollen, spec.ConfirmAbove.Impact)
		}
	}

	return grant, nil
}

// parseDelegationExpiry accepts an RFC 3339 timestamp or a bare date.
func parseDelegationExpiry(raw string) (time.Time, error) {
	if expires, err := time.Parse(time.RFC3339, raw); err == nil {
		return expires, nil
	}
	if expires, err := time.Parse("2006-01-02", raw); err == nil {
		return expires.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("expires %q is neither an RFC 3339 timestamp nor a YYYY-MM-DD date", raw)
}

func trimNonEmpty(values []string) []string {
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		if v := strings.TrimSpace(value); v != "" {
			trimmed = append(trimmed, v)
		}
	}
	return trimmed
}

// Grant mutation (Botanist control-plane).
//
// Operation-classes on a grant apply to every Substrate listed for that Pollen.
// This seam therefore refuses a Substrate-specific add or revoke when the
// existing grant names more than one Substrate: the current file shape cannot
// represent that change without widening or ambiguously revoking authority.
// It also never creates a new Pollen grant and never consults a grants file
// other than the one the caller named. The CLI adapter passes the Stem
// control-plane directory, never a Substrate checkout.

const defaultDelegationGrantsFileMode os.FileMode = 0o600

// ValidateGrantableOperation reports whether name may appear on a control-plane
// grant. Grantable classes are the delegated capability set plus the
// sprout.watch view. Unknown names and canonical but non-delegable commands
// fail closed. Wildcards are never grantable.
func ValidateGrantableOperation(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("operation class is empty")
	}
	if strings.ContainsAny(name, "*?") {
		return fmt.Errorf("operation class %q is a wildcard; grants name exact operation classes", name)
	}
	if isGrantableOperation(name) {
		return nil
	}
	for _, capability := range CapabilityNames() {
		if capability == name {
			return fmt.Errorf("operation class %q is not delegable", name)
		}
	}
	return fmt.Errorf("unknown operation class %q", name)
}

func isGrantableOperation(name string) bool {
	if name == CapSproutWatch {
		return true
	}
	return IsDelegatedCapability(name)
}

func normalizeGrantableOperations(operations []string) ([]string, error) {
	seen := make(map[string]bool, len(operations))
	normalized := make([]string, 0, len(operations))
	for _, raw := range operations {
		name := strings.TrimSpace(raw)
		if err := ValidateGrantableOperation(name); err != nil {
			return nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		normalized = append(normalized, name)
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("at least one operation class is required")
	}
	return normalized, nil
}

func validateGrantIdentity(pollen, substrate string) error {
	if strings.TrimSpace(pollen) == "" {
		return fmt.Errorf("pollen is empty")
	}
	if strings.TrimSpace(substrate) == "" {
		return fmt.Errorf("substrate is empty")
	}
	return nil
}

// AddGrantOperationClasses adds the named operation classes to an existing
// single-Substrate grant for pollen in the control-plane grants file. Adding a
// class that is already present is a no-op. Unrelated fields, other Pollens,
// and existing operation classes are preserved. The named Pollen/Substrate
// grant must already exist; this does not create one.
func AddGrantOperationClasses(tendrilDir, pollen, substrate string, operations []string) error {
	return mutateGrantOperationClasses(tendrilDir, pollen, substrate, operations, grantMutationAdd)
}

// RevokeGrantOperationClasses removes the named operation classes from an
// existing single-Substrate grant for pollen. Removing a class that is not
// present is a no-op. If no operation classes remain, the Pollen's grant is
// removed rather than left empty (an empty grant is a configuration error and
// must never become a permissive state). A missing grants file or missing
// Pollen is a no-op.
func RevokeGrantOperationClasses(tendrilDir, pollen, substrate string, operations []string) error {
	return mutateGrantOperationClasses(tendrilDir, pollen, substrate, operations, grantMutationRevoke)
}

type grantMutationKind int

const (
	grantMutationAdd grantMutationKind = iota
	grantMutationRevoke
)

func mutateGrantOperationClasses(tendrilDir, pollen, substrate string, operations []string, kind grantMutationKind) error {
	if err := validateGrantIdentity(pollen, substrate); err != nil {
		return err
	}
	normalized, err := normalizeGrantableOperations(operations)
	if err != nil {
		return err
	}
	pollen = strings.TrimSpace(pollen)
	substrate = strings.TrimSpace(substrate)
	path := filepath.Join(strings.TrimSpace(tendrilDir), DelegationGrantsFilename)

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if kind == grantMutationRevoke {
				return nil
			}
			return fmt.Errorf("delegation grants file %s does not exist; grant extends an existing control-plane grant and does not create one", path)
		}
		return fmt.Errorf("read delegation grants %s: %w", path, err)
	}

	info, statErr := os.Stat(path)
	perm := defaultDelegationGrantsFileMode
	if statErr == nil {
		if mode := info.Mode().Perm(); mode != 0 {
			perm = mode
		}
	}

	if _, err := decodeDelegationGrants(content, path); err != nil {
		return err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return fmt.Errorf("decode delegation grants %s: %w", path, err)
	}
	grantsNode, pollenNode, err := locateGrantNodes(&doc, path, pollen, kind)
	if err != nil {
		return err
	}
	if pollenNode == nil {
		// Revoke of a Pollen that is not present is a no-op.
		return nil
	}

	changed, err := applyGrantOperationMutation(pollenNode, pollen, substrate, normalized, kind)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}

	if classes := yamlMapValue(pollenNode, "operationClasses"); classes == nil || (classes.Kind == yaml.SequenceNode && len(yamlSequenceValues(classes)) == 0) {
		deleteYAMLMapKey(grantsNode, pollen)
	}

	encoded, err := encodeYAMLMappingDocument(&doc)
	if err != nil {
		return fmt.Errorf("encode delegation grants %s: %w", path, err)
	}
	if _, err := decodeDelegationGrants(encoded, path); err != nil {
		return fmt.Errorf("refusing to write invalid delegation grants %s: %w", path, err)
	}
	if err := writeFileAtomically(path, encoded, perm); err != nil {
		return fmt.Errorf("write delegation grants %s: %w", path, err)
	}
	return nil
}

func decodeDelegationGrants(content []byte, path string) (*delegationGrantsFile, error) {
	var file delegationGrantsFile
	if err := yaml.Unmarshal(content, &file); err != nil {
		return nil, fmt.Errorf("decode delegation grants %s: %w", path, err)
	}
	for pollen, spec := range file.Grants {
		if _, err := grantFromSpec(pollen, spec); err != nil {
			return nil, fmt.Errorf("delegation grants %s: %w", path, err)
		}
	}
	return &file, nil
}

func locateGrantNodes(doc *yaml.Node, path, pollen string, kind grantMutationKind) (grantsNode, pollenNode *yaml.Node, err error) {
	root := yamlDocumentMapping(doc)
	if root == nil {
		return nil, nil, fmt.Errorf("decode delegation grants %s: expected a top-level mapping", path)
	}
	grantsNode = yamlMapValue(root, "grants")
	if grantsNode == nil {
		if kind == grantMutationRevoke {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("no grant exists for pollen %q; grant extends an existing control-plane grant and does not create one", pollen)
	}
	if grantsNode.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("decode delegation grants %s: grants is not a mapping", path)
	}
	pollenNode = yamlMapValue(grantsNode, pollen)
	if pollenNode == nil {
		if kind == grantMutationRevoke {
			return grantsNode, nil, nil
		}
		return nil, nil, fmt.Errorf("no grant exists for pollen %q; grant extends an existing control-plane grant and does not create one", pollen)
	}
	if pollenNode.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("delegation grants %s: grant for pollen %q is not a mapping", path, pollen)
	}
	return grantsNode, pollenNode, nil
}

func applyGrantOperationMutation(pollenNode *yaml.Node, pollen, substrate string, operations []string, kind grantMutationKind) (bool, error) {
	substratesNode := yamlMapValue(pollenNode, "substrates")
	if substratesNode == nil || substratesNode.Kind != yaml.SequenceNode {
		return false, fmt.Errorf("grant for pollen %q names no substrates", pollen)
	}
	substrates := uniquePreserveOrder(yamlSequenceValues(substratesNode))
	if len(substrates) == 0 {
		return false, fmt.Errorf("grant for pollen %q names no substrates", pollen)
	}
	if !containsExact(substrates, substrate) {
		return false, fmt.Errorf("no grant for pollen %q covers substrate %q", pollen, substrate)
	}
	if len(substrates) > 1 {
		others := make([]string, 0, len(substrates)-1)
		for _, name := range substrates {
			if name != substrate {
				others = append(others, name)
			}
		}
		if kind == grantMutationAdd {
			return false, fmt.Errorf("cannot add operation classes to pollen %q for substrate %q: the existing grant also covers %s; operationClasses apply to every listed substrate, and adding them would widen authority across all of them", pollen, substrate, strings.Join(others, ", "))
		}
		return false, fmt.Errorf("cannot revoke operation classes from pollen %q for substrate %q: the existing grant also covers %s; operationClasses apply to every listed substrate, and removing them would revoke authority across all of them", pollen, substrate, strings.Join(others, ", "))
	}

	classesNode := yamlMapValue(pollenNode, "operationClasses")
	if classesNode == nil || classesNode.Kind != yaml.SequenceNode {
		return false, fmt.Errorf("grant for pollen %q names no operationClasses", pollen)
	}

	existing := yamlSequenceValues(classesNode)
	switch kind {
	case grantMutationAdd:
		added := false
		for _, operation := range operations {
			if containsExact(existing, operation) {
				continue
			}
			classesNode.Content = append(classesNode.Content, yamlScalarNode(operation))
			existing = append(existing, operation)
			added = true
		}
		return added, nil
	case grantMutationRevoke:
		remove := make(map[string]bool, len(operations))
		for _, operation := range operations {
			remove[operation] = true
		}
		kept := make([]*yaml.Node, 0, len(classesNode.Content))
		removed := false
		for _, node := range classesNode.Content {
			if remove[strings.TrimSpace(node.Value)] {
				removed = true
				continue
			}
			kept = append(kept, node)
		}
		if !removed {
			return false, nil
		}
		classesNode.Content = kept
		return true, nil
	default:
		return false, fmt.Errorf("unknown grant mutation")
	}
}

func yamlDocumentMapping(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.MappingNode {
		return doc
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 && doc.Content[0].Kind == yaml.MappingNode {
		return doc.Content[0]
	}
	return nil
}

func yamlMapValue(m *yaml.Node, key string) *yaml.Node {
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

func deleteYAMLMapKey(m *yaml.Node, key string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func yamlSequenceValues(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	values := make([]string, 0, len(n.Content))
	for _, node := range n.Content {
		if v := strings.TrimSpace(node.Value); v != "" {
			values = append(values, v)
		}
	}
	return values
}

func yamlScalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func uniquePreserveOrder(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func encodeYAMLMappingDocument(doc *yaml.Node) ([]byte, error) {
	root := yamlDocumentMapping(doc)
	if root == nil {
		return nil, fmt.Errorf("expected a mapping document")
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		_ = enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeFileAtomically(path string, data []byte, perm os.FileMode) error {
	if perm == 0 {
		perm = defaultDelegationGrantsFileMode
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+DelegationGrantsFilename+".*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	success = true
	return nil
}
