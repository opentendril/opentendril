package receptors

import (
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// lockedPrimaryMCPNames is the authoritative current projection table: one
// primary lower-camelCase identifier per core.CapabilityNames() entry.
var lockedPrimaryMCPNames = map[string]string{
	"phytomer.create":   "phytomerCreate",
	"phytomer.list":     "phytomerList",
	"phytomer.get":      "phytomerGet",
	"phytomer.update":   "phytomerUpdate",
	"phytomer.delete":   "phytomerDelete",
	"phytomer.history":  "phytomerHistory",
	"genome.view":       "genomeView",
	"genome.reduce":     "genomeReduce",
	"genome.evolve":     "genomeEvolve",
	"genotype.create":   "genotypeCreate",
	"plasmid.list":      "plasmidList",
	"plasmid.inject":    "plasmidInject",
	"mesh.graft":        "meshGraft",
	"mesh.promote":      "meshPromote",
	"mesh.trait.list":   "meshTraitList",
	"mesh.trait.accept": "meshTraitAccept",
	"mesh.trait.reject": "meshTraitReject",
	"sequence.list":     "sequenceList",
	"sequence.grow":     "sequenceGrow",
	"sprout.grow":       "sproutGrow",
	"stoma.pass":        "stomaPass",
	"seed.grow":         "seedGrow",
	"git.commit":        "gitCommit",
	"git.push":          "gitPush",
	"git.pr":            "gitPr",
	"git.branch":        "gitBranch",
	"git.status":        "gitStatus",
	"git.branch.list":   "gitBranchList",
	"git.prune":         "gitPrune",
}

var lockedCompatibilityAliases = map[string]string{
	"runSequence":    core.CapSequenceGrow,
	"sproutTendril":  core.CapSproutGrow,
	"createGenotype": core.CapGenotypeCreate,
	"viewGenome":     core.CapGenomeView,
	"reduceGenome":   core.CapGenomeReduce,
	"injectPlasmid":  core.CapPlasmidInject,
	"graftSubstrate": core.CapMeshGraft,
	"promotePR":      core.CapMeshPromote,
}

func TestMCPToolNameLockedTable(t *testing.T) {
	names := core.CapabilityNames()
	if len(names) != len(lockedPrimaryMCPNames) {
		t.Fatalf("CapabilityNames() has %d entries, locked table has %d", len(names), len(lockedPrimaryMCPNames))
	}

	seen := map[string]struct{}{}
	for _, name := range names {
		seen[name] = struct{}{}
		want, ok := lockedPrimaryMCPNames[name]
		if !ok {
			t.Errorf("CapabilityNames() entry %q missing from locked table", name)
			continue
		}
		if got := MCPToolName(name); got != want {
			t.Errorf("MCPToolName(%q) = %q, want %q", name, got, want)
		}
	}
	for canonical, primary := range lockedPrimaryMCPNames {
		if _, ok := seen[canonical]; !ok {
			t.Errorf("locked table entry %q not in CapabilityNames()", canonical)
		}
		if MCPToolName(canonical) != primary {
			t.Errorf("MCPToolName(%q) = %q, want %q", canonical, MCPToolName(canonical), primary)
		}
	}
}

func TestMCPToolNameHasNoDots(t *testing.T) {
	for _, name := range core.CapabilityNames() {
		got := MCPToolName(name)
		if strings.Contains(got, ".") {
			t.Errorf("MCPToolName(%q) = %q contains '.'", name, got)
		}
	}
}

func TestMCPToolNameInjective(t *testing.T) {
	seen := map[string]string{}
	for _, name := range core.CapabilityNames() {
		primary := MCPToolName(name)
		if other, ok := seen[primary]; ok {
			t.Errorf("projection collision: %q and %q both project to %q", other, name, primary)
			continue
		}
		seen[primary] = name
	}
}

func TestResolveMCPToolNamePrimary(t *testing.T) {
	for _, canonical := range core.CapabilityNames() {
		primary := MCPToolName(canonical)
		got, ok := ResolveMCPToolName(primary)
		if !ok || got != canonical {
			t.Errorf("ResolveMCPToolName(%q) = (%q, %v), want (%q, true)", primary, got, ok, canonical)
		}
	}
}

func TestResolveMCPToolNameAliases(t *testing.T) {
	if len(mcpCompatibilityAliases) != len(lockedCompatibilityAliases) {
		t.Fatalf("alias table has %d entries, want %d", len(mcpCompatibilityAliases), len(lockedCompatibilityAliases))
	}
	for alias, want := range lockedCompatibilityAliases {
		got, ok := ResolveMCPToolName(alias)
		if !ok || got != want {
			t.Errorf("ResolveMCPToolName(%q) = (%q, %v), want (%q, true)", alias, got, ok, want)
		}
		if table, ok := mcpCompatibilityAliases[alias]; !ok || table != want {
			t.Errorf("mcpCompatibilityAliases[%q] = %q, want %q", alias, table, want)
		}
	}
}

func TestResolveMCPToolNameCanonical(t *testing.T) {
	for _, canonical := range core.CapabilityNames() {
		got, ok := ResolveMCPToolName(canonical)
		if !ok || got != canonical {
			t.Errorf("ResolveMCPToolName(%q) = (%q, %v), want (%q, true)", canonical, got, ok, canonical)
		}
	}
}

func TestResolveMCPToolNameRejectsUnknown(t *testing.T) {
	rejected := []string{
		"",
		"git-status",
		"git_status",
		"GitStatus",
		"git.status.extra",
		"GIT_STATUS",
		"git_Status",
		"Git.Status",
		"git status",
		"gitstatus",
		"git",
		"status",
		" git.status",
		"git.status ",
		" gitStatus",
		"gitStatus ",
		"git_status ",
		"unknown_tool",
		"sprout.tendril",
		"sprout_grow",
		"run_sequence",
		"RunSequence",
		"GIT.STATUS",
		"mesh_trait_list",
		"sequence_grow",
	}
	for _, name := range rejected {
		if got, ok := ResolveMCPToolName(name); ok {
			t.Errorf("ResolveMCPToolName(%q) = (%q, true), want ok=false", name, got)
		}
	}
}

func TestResolveMCPToolNameDoesNotReverseCamelCase(t *testing.T) {
	canonicals := []string{"mesh.trait.list"}
	got, ok := resolveMCPToolNameAgainst("meshTraitList", canonicals, nil)
	if !ok || got != "mesh.trait.list" {
		t.Fatalf("Resolve(%q) = (%q, %v), want (%q, true)", "meshTraitList", got, ok, "mesh.trait.list")
	}

	// Heuristic reverse-camelCase of meshTraitList could invent these
	// spellings. They must not resolve unless they are themselves a
	// canonical name or a primary projection in the supplied set.
	invented := []string{
		"mesh.traitList",
		"meshTrait.list",
		"MeshTraitList",
		"meshtraitlist",
		"mesh_trait_list",
		"mesh-trait-list",
	}
	for _, name := range invented {
		if invertedGot, invertedOK := resolveMCPToolNameAgainst(name, canonicals, nil); invertedOK {
			t.Errorf("heuristic reverse %q resolved to %q", name, invertedGot)
		}
	}
}

func TestMCPIdentifierAcceptedNamesAreUnique(t *testing.T) {
	bindings, err := mcpNameBindings(core.CapabilityNames(), mcpCompatibilityAliases)
	if err != nil {
		t.Fatalf("live MCP name bindings collided: %v", err)
	}
	for name, want := range bindings {
		got, ok := ResolveMCPToolName(name)
		if !ok || got != want {
			t.Errorf("ResolveMCPToolName(%q) = (%q, %v), want (%q, true)", name, got, ok, want)
		}
	}
}

func TestMCPIdentifierFutureProjectionCollision(t *testing.T) {
	canonicals := []string{"foo.bar.baz", "fooBar.baz"}
	if MCPToolName(canonicals[0]) != "fooBarBaz" || MCPToolName(canonicals[1]) != "fooBarBaz" {
		t.Fatalf("fixture does not collide: %q %q", MCPToolName(canonicals[0]), MCPToolName(canonicals[1]))
	}
	if _, err := mcpNameBindings(canonicals, nil); err == nil {
		t.Fatal("expected projection collision for foo.bar.baz and fooBar.baz")
	}
}

func TestMCPIdentifierAliasCollision(t *testing.T) {
	canonicals := []string{core.CapGitStatus, core.CapSproutGrow}
	aliases := map[string]string{"gitStatus": core.CapSproutGrow}
	if _, err := mcpNameBindings(canonicals, aliases); err == nil {
		t.Fatal("expected alias collision: gitStatus must not resolve to both git.status and sprout.grow")
	}
}

func TestMCPIdentifierAllowsUnderscoreInCanonical(t *testing.T) {
	canonicals := []string{"foo_bar.baz", core.CapGitStatus}
	bindings, err := mcpNameBindings(canonicals, nil)
	if err != nil {
		t.Fatalf("canonical name containing '_' was rejected: %v", err)
	}
	if got := bindings["foo_barBaz"]; got != "foo_bar.baz" {
		t.Fatalf("foo_barBaz -> %q, want foo_bar.baz", got)
	}
	if got := MCPToolName("foo_bar.baz"); got != "foo_barBaz" {
		t.Fatalf("MCPToolName(foo_bar.baz) = %q, want foo_barBaz", got)
	}
}

func TestMCPIdentifierSameCapabilityOverlap(t *testing.T) {
	canonicals := []string{core.CapSproutGrow}
	aliases := map[string]string{"sproutGrow": core.CapSproutGrow}
	if _, err := mcpNameBindings(canonicals, aliases); err != nil {
		t.Fatalf("same-capability alias/primary/canonical overlap rejected: %v", err)
	}
}
