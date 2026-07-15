package core

import (
	"context"
	"fmt"
	"strings"
)

// The substrate-grafting capability family. Grafting
// delegates a local substrate's latest commit through the Mycelial Mesh; both
// workspace resolution (substrates config) and the push itself live outside
// the Core in packages it is structurally forbidden from importing (the
// conductor and the mesh client — see boundary_test.go). They are therefore
// injected as transport-free function ports via WithMesh — the same template
// as GenomeOps (PR) and PlasmidOps.
//
// The mesh key-management commands (`tendril mesh keygen|issue-token`) are
// deliberately NOT governed: they mint the workspace's private mesh keys and
// signed tokens, a local secret authority that must not be projected onto the
// network surfaces (posture; same rationale as plasmid.sign). The
// server-side graft endpoint (/v1/mesh/graft WebSocket) is the *receiving*
// half of the mesh and stays outside the registry as infrastructure, not a
// command.
//
// The mesh trait inbox commands (`tendril mesh trait list|accept|reject`) are
// governed because they manipulate shared pending-trait state rather than
// minting local secrets.

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

// MeshOps is the injection port for mesh operations whose implementation lives
// outside the Core (substrate resolution in the conductor, the push in the
// mesh client, and the local trait inbox in the transport layer). Either func
// may be nil, in which case the capabilities report that they are not wired
// rather than acting.
type MeshOps struct {
	// ResolveWorkspace maps a substrate path or named substrate key onto a
	// local git workspace root.
	ResolveWorkspace func(ctx context.Context, substrate string) (string, error)
	// DelegatePush pushes the workspace's latest commit through the mesh graft
	// endpoint and returns the accepted commit hash.
	DelegatePush func(ctx context.Context, workspace, branch, commitMessage string) (string, error)
	// ListPendingTraits returns the pending foreign traits waiting in local state.
	ListPendingTraits func(ctx context.Context) ([]any, error)
	// AcceptTrait marks a pending trait as accepted.
	AcceptTrait func(ctx context.Context, traitID string) error
	// RejectTrait marks a pending trait as rejected.
	RejectTrait func(ctx context.Context, traitID string) error
}

// WithMesh wires the mesh operation port onto the Service and returns the
// Service for chaining.
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

func (s *Service) meshTraitList(ctx context.Context) (MeshTraitListOutput, error) {
	if s.mesh.ListPendingTraits == nil {
		return MeshTraitListOutput{}, fmt.Errorf("mesh trait listing is not wired: construct the Core with WithMesh(MeshOps{ListPendingTraits: …})")
	}

	traits, err := s.mesh.ListPendingTraits(ctx)
	if err != nil {
		return MeshTraitListOutput{}, err
	}
	if traits == nil {
		traits = []any{}
	}
	return MeshTraitListOutput{Traits: traits}, nil
}

// MeshTraitList returns the pending foreign traits via the injected execution port.
func (s *Service) MeshTraitList(ctx context.Context, _ MeshTraitListInput) (MeshTraitListOutput, error) {
	return s.meshTraitList(ctx)
}

// MeshTraitAccept accepts one pending foreign trait via the injected execution port.
func (s *Service) MeshTraitAccept(ctx context.Context, in MeshTraitAcceptInput) (MeshTraitAcceptOutput, error) {
	if s.mesh.AcceptTrait == nil {
		return MeshTraitAcceptOutput{}, fmt.Errorf("mesh trait acceptance is not wired: construct the Core with WithMesh(MeshOps{AcceptTrait: …})")
	}
	traitID := strings.TrimSpace(in.TraitID)
	if traitID == "" {
		return MeshTraitAcceptOutput{}, fmt.Errorf("trait id is required")
	}
	if err := s.mesh.AcceptTrait(ctx, traitID); err != nil {
		return MeshTraitAcceptOutput{}, err
	}
	return MeshTraitAcceptOutput{TraitID: traitID, Status: "accepted"}, nil
}

// MeshTraitReject rejects one pending foreign trait via the injected execution port.
func (s *Service) MeshTraitReject(ctx context.Context, in MeshTraitRejectInput) (MeshTraitRejectOutput, error) {
	if s.mesh.RejectTrait == nil {
		return MeshTraitRejectOutput{}, fmt.Errorf("mesh trait rejection is not wired: construct the Core with WithMesh(MeshOps{RejectTrait: …})")
	}
	traitID := strings.TrimSpace(in.TraitID)
	if traitID == "" {
		return MeshTraitRejectOutput{}, fmt.Errorf("trait id is required")
	}
	if err := s.mesh.RejectTrait(ctx, traitID); err != nil {
		return MeshTraitRejectOutput{}, err
	}
	return MeshTraitRejectOutput{TraitID: traitID, Status: "rejected"}, nil
}

// meshCapabilities declares the mesh family's registry entries, bound to this
// Service's typed methods — identical in shape to the session, genome,
// plasmid, and other governed families.
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
		{
			Name:        CapMeshTraitList,
			Description: "List pending foreign traits awaiting local governance.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				return s.MeshTraitList(ctx, MeshTraitListInput{})
			},
		},
		{
			Name:        CapMeshTraitAccept,
			Description: "Accept one pending foreign trait into local state.",
			InputSchema: schemaObject(map[string]any{
				"traitId": stringProp("The pending trait identifier."),
			}, []string{"traitId"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in MeshTraitAcceptInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.MeshTraitAccept(ctx, in)
			},
		},
		{
			Name:        CapMeshTraitReject,
			Description: "Reject one pending foreign trait.",
			InputSchema: schemaObject(map[string]any{
				"traitId": stringProp("The pending trait identifier."),
			}, []string{"traitId"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in MeshTraitRejectInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.MeshTraitReject(ctx, in)
			},
		},
	}
}
