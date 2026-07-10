package core

import (
	"context"
	"fmt"
	"strings"
)

// The substrate-grafting capability family (issue #181, slice 3). Grafting
// delegates a local substrate's latest commit through the Mycelial Mesh; both
// workspace resolution (substrates config) and the push itself live outside
// the Core in packages it is structurally forbidden from importing (the
// conductor and the mesh client — see boundary_test.go). They are therefore
// injected as transport-free function ports via WithMesh — the same template
// as GenomeOps (PR #206) and PlasmidOps.
//
// The mesh key-management commands (`tendril mesh keygen|issue-token`) are
// deliberately NOT governed: they mint the workspace's private mesh keys and
// signed tokens, a local secret authority that must not be projected onto the
// network surfaces (issue #162 posture; same rationale as plasmid.sign). The
// server-side graft endpoint (/v1/mesh/graft WebSocket) is the *receiving*
// half of the mesh and stays outside the registry as infrastructure, not a
// command.

// MeshGraftInput asks the Stem to delegate a local substrate's latest commit
// through the mesh graft endpoint.
type MeshGraftInput struct {
	// Substrate is the local substrate path or named substrate key to graft.
	Substrate string `json:"substrate"`
	// Branch optionally overrides the branch to push to.
	Branch string `json:"branch,omitempty"`
	// CommitMessage optionally overrides the delegated commit message.
	CommitMessage string `json:"commitMessage,omitempty"`
}

// MeshPromoteInput asks the Stem to promote a pull-request branch through the
// mesh graft endpoint after local validation.
type MeshPromoteInput struct {
	// Substrate is the local substrate path or named substrate key to promote.
	Substrate string `json:"substrate"`
	// Branch optionally overrides the branch to push to.
	Branch string `json:"branch,omitempty"`
	// PRNumber optionally names the pull request being promoted.
	PRNumber string `json:"prNumber,omitempty"`
	// CommitMessage optionally overrides the delegated commit message.
	CommitMessage string `json:"commitMessage,omitempty"`
}

// MeshDelegation is the outcome of a mesh graft: which local workspace was
// delegated and the commit hash the mesh accepted.
type MeshDelegation struct {
	Workspace string `json:"workspace"`
	Commit    string `json:"commit"`
}

// MeshPromotion is the outcome of a mesh PR promotion.
type MeshPromotion struct {
	Workspace string `json:"workspace"`
	Commit    string `json:"commit"`
	PRNumber  string `json:"prNumber,omitempty"`
}

// MeshOps is the injection port for substrate-grafting operations whose
// implementation lives outside the Core (substrate resolution in the
// conductor, the push in the mesh client). Either func may be nil, in which
// case the capabilities report that they are not wired rather than acting.
type MeshOps struct {
	// ResolveWorkspace maps a substrate path or named substrate key onto a
	// local git workspace root.
	ResolveWorkspace func(ctx context.Context, substrate string) (string, error)
	// DelegatePush pushes the workspace's latest commit through the mesh graft
	// endpoint and returns the accepted commit hash.
	DelegatePush func(ctx context.Context, workspace, branch, commitMessage string) (string, error)
}

// WithMesh wires the substrate-grafting operation port onto the Service and
// returns the Service for chaining.
func (s *Service) WithMesh(ops MeshOps) *Service {
	s.mesh = ops
	return s
}

func (s *Service) meshDelegate(ctx context.Context, substrate, branch, commitMessage string) (MeshDelegation, error) {
	if s.mesh.ResolveWorkspace == nil || s.mesh.DelegatePush == nil {
		return MeshDelegation{}, fmt.Errorf("mesh grafting is not wired: construct the Core with WithMesh(MeshOps{ResolveWorkspace: …, DelegatePush: …})")
	}
	if strings.TrimSpace(substrate) == "" {
		return MeshDelegation{}, fmt.Errorf("substrate is required")
	}

	workspace, err := s.mesh.ResolveWorkspace(ctx, substrate)
	if err != nil {
		return MeshDelegation{}, fmt.Errorf("resolve substrate: %w", err)
	}
	commit, err := s.mesh.DelegatePush(ctx, workspace, branch, commitMessage)
	if err != nil {
		return MeshDelegation{}, err
	}
	return MeshDelegation{Workspace: workspace, Commit: commit}, nil
}

// MeshGraft delegates the substrate's latest commit through the mesh graft
// endpoint via the injected execution port.
func (s *Service) MeshGraft(ctx context.Context, in MeshGraftInput) (MeshDelegation, error) {
	return s.meshDelegate(ctx, in.Substrate, in.Branch, in.CommitMessage)
}

// MeshPromote promotes a pull-request branch through the mesh graft endpoint.
// When no commit message is given but a PR number is, the historic default
// message "promote PR #<n>" is applied — business logic that used to live in
// the MCP adapter and now lives here, on the transport-free side.
func (s *Service) MeshPromote(ctx context.Context, in MeshPromoteInput) (MeshPromotion, error) {
	prNumber := strings.TrimSpace(in.PRNumber)
	commitMessage := in.CommitMessage
	if strings.TrimSpace(commitMessage) == "" && prNumber != "" {
		commitMessage = "promote PR #" + prNumber
	}

	delegation, err := s.meshDelegate(ctx, in.Substrate, in.Branch, commitMessage)
	if err != nil {
		return MeshPromotion{}, err
	}
	return MeshPromotion{Workspace: delegation.Workspace, Commit: delegation.Commit, PRNumber: prNumber}, nil
}

// meshCapabilities declares the substrate-grafting family's registry entries,
// bound to this Service's typed methods — identical in shape to the session,
// genome, and plasmid families.
func (s *Service) meshCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapMeshGraft,
			Description: "Delegate the latest commit from a local substrate through the mesh graft endpoint.",
			InputSchema: schemaObject(map[string]any{
				"substrate":     stringProp("The local substrate path or named substrate key to graft."),
				"branch":        stringProp("Optional branch to push to. Defaults to the current branch."),
				"commitMessage": stringProp("Optional commit message for the delegated push."),
			}, []string{"substrate"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in MeshGraftInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.MeshGraft(ctx, in)
			},
		},
		{
			Name:        CapMeshPromote,
			Description: "Promote a pull-request branch through the mesh graft endpoint after local validation.",
			InputSchema: schemaObject(map[string]any{
				"substrate":     stringProp("The local substrate path or named substrate key to promote."),
				"branch":        stringProp("Optional branch to push to. Defaults to the current branch."),
				"prNumber":      stringProp("Optional pull request number associated with the promotion."),
				"commitMessage": stringProp("Optional commit message for the delegated push."),
			}, []string{"substrate"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in MeshPromoteInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.MeshPromote(ctx, in)
			},
		},
	}
}
