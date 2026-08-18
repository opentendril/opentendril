package receptors

import (
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// lockedPrimaryMCPNames is the authoritative current projection table: one
// primary underscore identifier per core.CapabilityNames() entry.
var lockedPrimaryMCPNames = map[string]string{
	"phytomer.create":   "phytomer_create",
	"phytomer.list":     "phytomer_list",
	"phytomer.get":      "phytomer_get",
	"phytomer.update":   "phytomer_update",
	"phytomer.delete":   "phytomer_delete",
	"phytomer.history":  "phytomer_history",
	"genome.view":       "genome_view",
	"genome.reduce":     "genome_reduce",
	"genome.evolve":     "genome_evolve",
	"genotype.create":   "genotype_create",
	"plasmid.list":      "plasmid_list",
	"plasmid.inject":    "plasmid_inject",
	"mesh.graft":        "mesh_graft",
	"mesh.promote":      "mesh_promote",
	"mesh.trait.list":   "mesh_trait_list",
	"mesh.trait.accept": "mesh_trait_accept",
	"mesh.trait.reject": "mesh_trait_reject",
	"sequence.list":     "sequence_list",
	"sequence.grow":     "sequence_grow",
	"sprout.grow":       "sprout_grow",
	"stoma.pass":        "stoma_pass",
	"seed.grow":         "seed_grow",
	"git.commit":        "git_commit",
	"git.push":          "git_push",
	"git.pr":            "git_pr",
	"git.branch":        "git_branch",
	"git.status":        "git_status",
	"git.branch.list":   "git_branch_list",
	"git.prune":         "git_prune",
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
		"git_status ",
		"unknown_tool",
		"sprout.tendril",
		"run_sequence",
		"RunSequence",
		"GIT.STATUS",
	}
	for _, name := range rejected {
		if got, ok := ResolveMCPToolName(name); ok {
			t.Errorf("ResolveMCPToolName(%q) = (%q, true), want ok=false", name, got)
		}
	}
}

func TestResolveMCPToolNameDoesNotInvertUnderscores(t *testing.T) {
	canonicals := []string{"foo_bar.baz"}
	got, ok := resolveMCPToolNameAgainst("foo_bar_baz", canonicals, nil)
	if !ok || got != "foo_bar.baz" {
		t.Fatalf("Resolve(%q) = (%q, %v), want (%q, true)", "foo_bar_baz", got, ok, "foo_bar.baz")
	}
	inverted := strings.ReplaceAll("foo_bar_baz", "_", ".")
	if inverted == got {
		t.Fatalf("resolution inverted '_' to produce %q", got)
	}
	if invertedGot, invertedOK := resolveMCPToolNameAgainst(inverted, canonicals, nil); invertedOK {
		t.Fatalf("mechanical inversion %q resolved to %q", inverted, invertedGot)
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
	canonicals := []string{"foo.bar_baz", "foo_bar.baz"}
	if MCPToolName(canonicals[0]) != "foo_bar_baz" || MCPToolName(canonicals[1]) != "foo_bar_baz" {
		t.Fatalf("fixture does not collide: %q %q", MCPToolName(canonicals[0]), MCPToolName(canonicals[1]))
	}
	if _, err := mcpNameBindings(canonicals, nil); err == nil {
		t.Fatal("expected projection collision for foo.bar_baz and foo_bar.baz")
	}
}

func TestMCPIdentifierAliasCollision(t *testing.T) {
	canonicals := []string{core.CapGitStatus, core.CapSproutGrow}
	aliases := map[string]string{"git_status": core.CapSproutGrow}
	if _, err := mcpNameBindings(canonicals, aliases); err == nil {
		t.Fatal("expected alias collision: git_status must not resolve to both git.status and sprout.grow")
	}
}

func TestMCPIdentifierAllowsUnderscoreInCanonical(t *testing.T) {
	canonicals := []string{"foo_bar.baz", core.CapGitStatus}
	bindings, err := mcpNameBindings(canonicals, nil)
	if err != nil {
		t.Fatalf("canonical name containing '_' was rejected: %v", err)
	}
	if got := bindings["foo_bar_baz"]; got != "foo_bar.baz" {
		t.Fatalf("foo_bar_baz -> %q, want foo_bar.baz", got)
	}
	if got := MCPToolName("foo_bar.baz"); got != "foo_bar_baz" {
		t.Fatalf("MCPToolName(foo_bar.baz) = %q, want foo_bar_baz", got)
	}
}

func TestMCPIdentifierSameCapabilityOverlap(t *testing.T) {
	canonicals := []string{"seed_grow"}
	aliases := map[string]string{"seed_grow": "seed_grow"}
	if _, err := mcpNameBindings(canonicals, aliases); err != nil {
		t.Fatalf("same-capability alias/primary/canonical overlap rejected: %v", err)
	}
}
