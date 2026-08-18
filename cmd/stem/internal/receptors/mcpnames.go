package receptors

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// mcpCompatibilityAliases maps the existing deprecated MCP tool names onto
// the canonical Core capabilities those aliases already dispatch to. The
// table is adapter-owned: it is not a second capability authority.
var mcpCompatibilityAliases = map[string]string{
	"runSequence":    core.CapSequenceGrow,
	"sproutTendril":  core.CapSproutGrow,
	"createGenotype": core.CapGenotypeCreate,
	"viewGenome":     core.CapGenomeView,
	"reduceGenome":   core.CapGenomeReduce,
	"injectPlasmid":  core.CapPlasmidInject,
	"graftSubstrate": core.CapMeshGraft,
	"promotePR":      core.CapMeshPromote,
}

// MCPToolName returns the primary Pollinator-visible MCP identifier for a
// canonical Core capability name: split on ".", keep the first segment,
// capitalize the first character of each following segment, and concatenate
// with no delimiter.
//
//	git.status      -> gitStatus
//	git.branch.list -> gitBranchList
//
// Uniqueness is a property of the capability set, not of this transform: two
// different canonical names must never share one primary identifier.
func MCPToolName(canonical string) string {
	parts := strings.Split(canonical, ".")
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(parts[0])
	for _, part := range parts[1:] {
		b.WriteString(capitalizeFirst(part))
	}
	return b.String()
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// ResolveMCPToolName maps an inbound MCP tool name to exactly one canonical
// Core capability. Accepted families, in order:
//
//  1. deprecated compatibility aliases
//  2. primary camelCase projections of core.CapabilityNames()
//  3. the canonical capability identifiers themselves
//
// Unknown, empty, hyphenated, underscored, and case-folded names fail closed.
// Primary names are looked up from the live CapabilityNames() projection set;
// they are not recovered by reversing camelCase or by replacing "_" with ".".
func ResolveMCPToolName(name string) (string, bool) {
	return resolveMCPToolNameAgainst(name, core.CapabilityNames(), mcpCompatibilityAliases)
}

func resolveMCPToolNameAgainst(name string, canonicals []string, aliases map[string]string) (string, bool) {
	if name == "" {
		return "", false
	}
	if canonical, ok := aliases[name]; ok {
		return canonical, true
	}
	if canonical, ok := mcpPrimaryIndex(canonicals)[name]; ok {
		return canonical, true
	}
	for _, canonical := range canonicals {
		if canonical == name {
			return canonical, true
		}
	}
	return "", false
}

func mcpPrimaryIndex(canonicals []string) map[string]string {
	index := make(map[string]string, len(canonicals))
	for _, canonical := range canonicals {
		index[MCPToolName(canonical)] = canonical
	}
	return index
}

// mcpNameBindings builds the accepted-name → canonical map for the three
// families (primary projection, canonical identifier, compatibility alias).
// It fails if any accepted string would resolve to two different canonical
// capabilities, including a future projection collision. A canonical name
// may contain "_" when the resulting bindings stay unique.
func mcpNameBindings(canonicals []string, aliases map[string]string) (map[string]string, error) {
	known := make(map[string]struct{}, len(canonicals))
	for _, canonical := range canonicals {
		if canonical == "" {
			return nil, fmt.Errorf("empty canonical capability name")
		}
		if _, exists := known[canonical]; exists {
			return nil, fmt.Errorf("duplicate canonical capability %q", canonical)
		}
		known[canonical] = struct{}{}
	}

	bindings := make(map[string]string, len(canonicals)*2+len(aliases))
	bind := func(name, canonical string) error {
		if name == "" {
			return fmt.Errorf("empty MCP tool name for %q", canonical)
		}
		if existing, ok := bindings[name]; ok && existing != canonical {
			return fmt.Errorf("MCP tool name %q resolves to both %q and %q", name, existing, canonical)
		}
		bindings[name] = canonical
		return nil
	}

	for _, canonical := range canonicals {
		if err := bind(MCPToolName(canonical), canonical); err != nil {
			return nil, err
		}
		if err := bind(canonical, canonical); err != nil {
			return nil, err
		}
	}
	for alias, canonical := range aliases {
		if _, ok := known[canonical]; !ok {
			return nil, fmt.Errorf("compatibility alias %q targets unknown capability %q", alias, canonical)
		}
		if err := bind(alias, canonical); err != nil {
			return nil, err
		}
	}
	return bindings, nil
}
