package receptors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// CoreCapabilityNames returns the governed capability names this MCP adapter
// projects as tools. The parity tests assert this equals core.CapabilityNames().
func (h *MCPHandler) CoreCapabilityNames() []string {
	if h.core == nil {
		return nil
	}
	names := make([]string, 0, len(h.core.Capabilities()))
	for _, capability := range h.core.Capabilities() {
		names = append(names, capability.Name)
	}
	sort.Strings(names)
	return names
}

// coreToolDefs projects the Core capability registry into MCP tool definitions.
// The listed name is the adapter's primary Pollinator-visible identifier
// (MCPToolName), not the canonical Core capability name.
func (h *MCPHandler) coreToolDefs() []map[string]interface{} {
	if h.core == nil {
		return nil
	}
	defs := make([]map[string]interface{}, 0, len(h.core.Capabilities()))
	for _, capability := range h.core.Capabilities() {
		defs = append(defs, map[string]interface{}{
			"name":        MCPToolName(capability.Name),
			"description": capability.Description,
			"inputSchema": capability.InputSchema,
		})
	}
	return defs
}

// isCoreCapability reports whether a tool name is a governed Core capability.
func (h *MCPHandler) isCoreCapability(name string) bool {
	if h.core == nil {
		return false
	}
	for _, capability := range h.core.Capabilities() {
		if capability.Name == name {
			return true
		}
	}
	return false
}

// authorizeDelegatedTool gates one delegated-class tool invocation against
// the bind-time pollen and the active grants. Deny-closed: with no gate
// bound or no pollen bound the invocation is denied outright, so no MCP path
// can ever reach a delegated capability ungoverned. The substrate is read
// from the tool arguments' "substrate" field — the field every delegated
// capability carries.
func (h *MCPHandler) authorizeDelegatedTool(operationClass string, args map[string]interface{}) core.DelegationDecision {
	substrate, _ := args["substrate"].(string)
	request := core.DelegationRequest{
		Pollen:         h.pollen,
		OperationClass: operationClass,
		Substrate:      strings.TrimSpace(substrate),
		Impact:         core.CapabilityImpact(operationClass),
	}
	if h.delegation == nil || h.pollen == "" {
		decision := core.DelegationDecision{Reason: "delegation is not configured for this MCP session"}
		// Audit the denial when a gate is wired; a missing gate makes the
		// audit impossible (best-effort, like every gate decision) but never
		// weakens the enforcement.
		h.delegation.audit(request, decision)
		return decision
	}
	return h.delegation.Authorize(request)
}

// formatDelegationDenied renders a denied delegated-class invocation as an
// MCP error tool-result — the same envelope shape callCoreCapability uses for
// a failed capability — so MCP clients see the denial as a tool outcome
// rather than a protocol error.
func (h *MCPHandler) formatDelegationDenied(id interface{}, decision core.DelegationDecision) []byte {
	return h.formatResult(id, map[string]interface{}{
		"content": []map[string]interface{}{{"type": "text", "text": "delegation denied: " + decision.Reason}},
		"isError": true,
	})
}

// callCoreCapability invokes a Core capability through the generic capability
// registry and wraps its JSON result in an MCP tool-result envelope. Adapter
// translation only — no business logic.
func (h *MCPHandler) callCoreCapability(id interface{}, name string, args map[string]interface{}) []byte {
	return h.callCoreCapabilityAs(context.Background(), id, name, args)
}

// callCoreCapabilityAs dispatches with a caller-supplied context, so a
// delegated invocation can carry its authorized pollen. The pollen selects
// the isolated workspace the operation runs in and therefore never travels in
// the arguments: a caller that could name it could claim another subject's
// workspace. It is bound by the trusted launch configuration at connection
// time and stamped here, after authorization.
func (h *MCPHandler) callCoreCapabilityAs(ctx context.Context, id interface{}, name string, args map[string]interface{}) []byte {
	result, err := h.core.Invoke(ctx, name, args)
	return h.formatCapabilityResult(id, result, err)
}

// callStomaPass dispatches stoma.pass through the Core's typed
// method rather than the generic capability registry. The typed path exists
// for one reason: the egress allow-list is grant material with no JSON surface
// on the input type, so no argument decode — generic or typed — can ever carry
// it. Only this adapter places the authorized grant's allow-list onto the run,
// exactly like the REST stoma adapter.
func (h *MCPHandler) callStomaPass(id interface{}, args map[string]interface{}, decision core.DelegationDecision) []byte {
	encoded, err := json.Marshal(args)
	if err != nil {
		return h.formatError(id, -32602, "Invalid params", err.Error())
	}
	var in core.StomaPassInput
	if err := json.Unmarshal(encoded, &in); err != nil {
		return h.formatError(id, -32602, "Invalid params", err.Error())
	}

	// Egress is grant material: it has no JSON surface on the input type, so
	// the decode above can never have populated it. It is set below — and only
	// below — from the authorized delegation grant. Without a grant the empty
	// list stands: deny-all egress with zero configuration.
	if decision.Grant != nil {
		in.Egress = decision.Grant.Egress
	}
	if strings.TrimSpace(in.Origin) == "" {
		// Origin is MCP-surface metadata (exactly like the REST adapter
		// stamping its own origin), so the adapter fills the unset value
		// before the Core runs.
		in.Origin = session.OriginMCP
	}

	result, runErr := h.core.StomaPass(context.Background(), in)
	return h.formatCapabilityResult(id, result, runErr)
}

// callSeedGrow is the typed dispatch for seed.grow. Like callStomaPass, the
// Seed's egress allow-list is grant material with no JSON surface, so the
// adapter places the authorized grant's allow-list itself rather than trusting
// the generic registry decode.
func (h *MCPHandler) callSeedGrow(id interface{}, args map[string]interface{}, decision core.DelegationDecision) []byte {
	encoded, err := json.Marshal(args)
	if err != nil {
		return h.formatError(id, -32602, "Invalid params", err.Error())
	}
	var in core.SeedGrowInput
	if err := json.Unmarshal(encoded, &in); err != nil {
		return h.formatError(id, -32602, "Invalid params", err.Error())
	}

	// Egress is grant material: the decode above can never have populated it.
	// It is set below — and only below — from the authorized delegation grant.
	// Without a grant the empty list stands: deny-all egress.
	if decision.Grant != nil {
		in.Egress = decision.Grant.Egress
	}
	if strings.TrimSpace(in.Origin) == "" {
		in.Origin = session.OriginMCP
	}

	result, runErr := h.core.SeedGrow(context.Background(), in)
	return h.formatCapabilityResult(id, result, runErr)
}

// formatCapabilityResult wraps one capability outcome in the MCP tool-result
// envelope (content plus isError) shared by every capability dispatch path.
func (h *MCPHandler) formatCapabilityResult(id interface{}, result interface{}, err error) []byte {
	if err != nil {
		text := err.Error()
		if errors.Is(err, core.ErrNotFound) {
			text = "session not found"
		}
		return h.formatResult(id, map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": text}},
			"isError": true,
		})
	}

	payload, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr != nil {
		return h.formatError(id, -32603, "Internal error", marshalErr.Error())
	}
	return h.formatResult(id, map[string]interface{}{
		"content": []map[string]interface{}{{"type": "text", "text": string(payload)}},
		"isError": false,
	})
}

// summarizeSequenceResult renders the legacy runSequence text summary from
// the Core's transport-free run result.
func summarizeSequenceResult(result core.SequenceRunResult) string {
	if result.Name == "" && len(result.Steps) == 0 {
		return "Sequence run completed."
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Sequence %s", result.Name))
	if strings.TrimSpace(result.Substrate) != "" {
		lines = append(lines, fmt.Sprintf("Substrate: %s", filepath.ToSlash(result.Substrate)))
	}
	if strings.TrimSpace(result.Branch) != "" {
		lines = append(lines, fmt.Sprintf("Branch: %s", result.Branch))
	}
	lines = append(lines, "Steps:")
	for _, step := range result.Steps {
		line := fmt.Sprintf("- %s: %s", step.ID, step.Status)
		if strings.TrimSpace(step.Transcript) != "" {
			line += fmt.Sprintf(" | %s", strings.TrimSpace(step.Transcript))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (h *MCPHandler) handleToolsList(id interface{}) []byte {
	tools := []map[string]interface{}{
		{
			"name":        "runSequence",
			"description": "Deprecated alias of the governed sequence.grow capability. Runs a YAML sequence from .tendril/sequences/ or a relative path using the parallel sequence meristem.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pathOrName": map[string]interface{}{
						"type":        "string",
						"description": "The sequence YAML file path or sequence name to run.",
					},
				},
				"required": []string{"pathOrName"},
			},
		},
		{
			"name":        "sproutTendril",
			"description": "Deprecated alias of the governed sprout.grow capability. Delegates a complex coding task to the autonomous OpenTendril brain. Use this tool when you need a Pollinator to run terminal commands, debug complex errors, search the web, or execute multi-step engineering tasks inside a secure terrarium.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"transcript": map[string]interface{}{
						"type":        "string",
						"description": "A clear, actionable description of the transcript (task) for Tendril to execute.",
					},
					"stepId": map[string]interface{}{
						"type":        "string",
						"description": "Optional stable step identifier for a structured sequence run.",
					},
					"sessionId": map[string]interface{}{
						"type":        "string",
						"description": "Optional Tendril session identifier binding this run to a unified chat session (its preferences, models, and history).",
					},
					"substrate": map[string]interface{}{
						"type":        "string",
						"description": "The absolute path or named substrate key for the target repository workspace. E.g. /home/user/project or core",
					},
					"substrateUrl": map[string]interface{}{
						"type":        "string",
						"description": "Optional remote repository URL override to clone and operate on dynamically. E.g. https://github.com/opentendril/opentendril.git",
					},
					"substrateBranch": map[string]interface{}{
						"type":        "string",
						"description": "Optional branch name to clone if substrateUrl is provided.",
					},
				},
				"required": []string{"transcript", "substrate"},
			},
		},
		{
			"name":        "createGenotype",
			"description": "Deprecated alias of the governed genotype.create capability. Dynamically create or update an OpenTendril genotype (core identity/persona). Creates a new JSON configuration file in the genotypes directory. This allows you to define a new base role before sprouting a tendril.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "The unique name of the genotype (e.g. 'frontend-dev'). Do not use spaces or special characters.",
					},
					"instructions": map[string]interface{}{
						"type":        "string",
						"description": "The system prompt or instructions detailing exactly what this genotype's core identity or role is.",
					},
				},
				"required": []string{"name", "instructions"},
			},
		},
		{
			"name":        "viewGenome",
			"description": "Deprecated alias of the governed genome.view capability. Returns the concatenated contents of all Markdown files in .tendril/genome/ so the Pollinator can read active repository rules and guidelines.",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "reduceGenome",
			"description": "Deprecated alias of the governed genome.reduce capability. Deduplicates, compresses, and merges the epigenetic rules in .tendril/genome/epigenetics.md to prevent context window bloat.",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "injectPlasmid",
			"description": "Deprecated alias of the governed plasmid.inject capability. Injects a modular plasmid rule file (e.g. go-rules, react-style) into the active project genome.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "The plasmid name to inject into the active genome.",
					},
				},
				"required": []string{"name"},
			},
		},
		{
			"name":        "graftSubstrate",
			"description": "Deprecated alias of the governed mesh.graft capability. Delegates the latest commit from a local substrate to the mesh graft endpoint and streams central validation logs.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"substrate": map[string]interface{}{
						"type":        "string",
						"description": "The local substrate path or named substrate key to graft.",
					},
					"branch": map[string]interface{}{
						"type":        "string",
						"description": "Optional branch to push to. Defaults to the current branch.",
					},
					"commit-message": map[string]interface{}{
						"type":        "string",
						"description": "Optional commit message for the delegated push.",
					},
				},
				"required": []string{"substrate"},
			},
		},
		{
			"name":        "promotePR",
			"description": "Deprecated alias of the governed mesh.promote capability. Promotes a pull request branch through the mesh graft endpoint after local validation has completed.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"substrate": map[string]interface{}{
						"type":        "string",
						"description": "The local substrate path or named substrate key to promote.",
					},
					"branch": map[string]interface{}{
						"type":        "string",
						"description": "Optional branch to push to. Defaults to the current branch.",
					},
					"pr-number": map[string]interface{}{
						"type":        "string",
						"description": "Optional pull request number associated with the promotion.",
					},
					"commit-message": map[string]interface{}{
						"type":        "string",
						"description": "Optional commit message for the delegated push.",
					},
				},
				"required": []string{"substrate"},
			},
		},
	}
	// Project one primary MCP identifier per Core capability. Compatibility
	// aliases stay listed above; canonical dotted names are not republished.
	tools = append(tools, h.coreToolDefs()...)
	return h.formatResult(id, map[string]interface{}{
		"tools": tools,
	})
}

func (h *MCPHandler) handleToolsCall(id interface{}, rawParams json.RawMessage) []byte {
	var params struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return h.formatError(id, -32602, "Invalid params", err.Error())
	}

	canonical, ok := ResolveMCPToolName(params.Name)
	if !ok {
		return h.formatError(id, -32601, "Tool not found", nil)
	}

	if _, isAlias := mcpCompatibilityAliases[params.Name]; !isAlias {
		// Primary projection or canonical dotted name: dispatch through the
		// Core path using the resolved canonical identity. Authorization
		// always sees that identity, never the transport spelling.
		if !h.isCoreCapability(canonical) {
			return h.formatError(id, -32601, "Tool not found", nil)
		}
		var decision core.DelegationDecision
		callCtx := context.Background()
		if core.IsDelegatedCapability(canonical) {
			decision = h.authorizeDelegatedTool(canonical, params.Arguments)
			if !decision.Authorized {
				return h.formatDelegationDenied(id, decision)
			}
			callCtx = core.WithPollen(callCtx, h.pollen)
		}
		if canonical == core.CapSproutGrow {
			// Origin and the pinned stdio session are MCP-surface metadata
			// (exactly like the REST adapter stamping its own origin), so
			// the adapter fills unset values before the Core runs.
			if params.Arguments == nil {
				params.Arguments = map[string]interface{}{}
			}
			if id, _ := params.Arguments["sessionId"].(string); strings.TrimSpace(id) == "" && h.defaultSessionID != "" {
				params.Arguments["sessionId"] = h.defaultSessionID
			}
			if origin, _ := params.Arguments["origin"].(string); strings.TrimSpace(origin) == "" {
				params.Arguments["origin"] = session.OriginMCP
			}
		}
		if canonical == core.CapStomaPass {
			// stoma.pass alone needs the typed dispatch: its egress
			// allow-list is grant material with no JSON surface, so the
			// generic registry decode can never carry it — the adapter
			// places the authorized grant's allow-list itself.
			return h.callStomaPass(id, params.Arguments, decision)
		}
		if canonical == core.CapSeedGrow {
			// seed.grow carries the same grant-material egress allow-list as
			// stoma.pass (json:"-"), so it takes the same typed
			// dispatch: the adapter places the authorized grant's allow-list
			// itself rather than trusting the generic registry decode.
			return h.callSeedGrow(id, params.Arguments, decision)
		}
		resBytes := h.callCoreCapabilityAs(callCtx, id, canonical, params.Arguments)
		if canonical == core.CapGenotypeCreate && !strings.Contains(string(resBytes), `"isError":true`) {
			if err := SyncGenotypeIndex(); err != nil {
				log.Printf("[MCP] Failed to sync genotype index after %s: %v", canonical, err)
			}
		}
		return resBytes
	}

	// Deprecated aliases of the governed genome capabilities:
	// same Core, legacy tool names and text rendering preserved for
	// existing MCP clients. Adapter translation only — the business logic
	// that used to live inline here is now in core / the orchestrator port.
	if params.Name == "viewGenome" {
		if h.core == nil {
			return h.formatError(id, -32603, "Internal error", "Core capability service is not configured.")
		}
		seeds, err := h.core.GenomeView(context.Background())
		if err != nil {
			return h.formatResult(id, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "Failed to read genome: " + err.Error(),
					},
				},
				"isError": true,
			})
		}

		if len(seeds) == 0 {
			return h.formatResult(id, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "No genome Markdown files found in .tendril/genome/.",
					},
				},
				"isError": false,
			})
		}

		parts := make([]string, 0, len(seeds))
		for _, seed := range seeds {
			parts = append(parts, seed.Content)
		}
		return h.formatResult(id, map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": strings.Join(parts, "\n\n"),
				},
			},
			"isError": false,
		})
	}

	if params.Name == "reduceGenome" {
		if h.core == nil {
			return h.formatError(id, -32603, "Internal error", "Core capability service is not configured.")
		}
		path, err := h.core.GenomeReduce(context.Background())
		if err != nil {
			return h.formatResult(id, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "Failed to reduce genome: " + err.Error(),
					},
				},
				"isError": true,
			})
		}

		return h.formatResult(id, map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": fmt.Sprintf("Successfully reduced genome at %s.", filepath.ToSlash(path)),
				},
			},
			"isError": false,
		})
	}

	// Deprecated alias of the governed plasmid.inject capability:
	// same Core, legacy tool name and text rendering preserved
	// for existing MCP clients. Adapter translation only — the business
	// logic that used to live inline here is now behind the Core's
	// PlasmidOperations port.
	if params.Name == "injectPlasmid" {
		name, ok := params.Arguments["name"].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return h.formatError(id, -32602, "Invalid arguments", "The 'name' parameter is required.")
		}
		if h.core == nil {
			return h.formatError(id, -32603, "Internal error", "Core capability service is not configured.")
		}

		injection, err := h.core.PlasmidInject(context.Background(), core.PlasmidInjectInput{Name: name})
		if err != nil {
			return h.formatResult(id, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "Failed to inject plasmid: " + err.Error(),
					},
				},
				"isError": true,
			})
		}

		message := fmt.Sprintf("Injected plasmid %s -> %s.", injection.Source, injection.Dest)
		if injection.AlreadyActive {
			message = fmt.Sprintf("Plasmid already active: %s.", injection.Dest)
		}

		return h.formatResult(id, map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": message,
				},
			},
			"isError": false,
		})
	}

	// Deprecated aliases of the governed mesh.graft / mesh.promote
	// capabilities: same Core, legacy tool names, legacy
	// kebab-case argument keys, and text rendering preserved for existing
	// MCP clients. Adapter translation only — the resolution/push logic
	// that used to live inline here is now behind the Core's MeshOperations port.
	if params.Name == "graftSubstrate" {
		substrate, ok := params.Arguments["substrate"].(string)
		if !ok || strings.TrimSpace(substrate) == "" {
			return h.formatError(id, -32602, "Invalid arguments", "The 'substrate' parameter is required.")
		}
		if h.core == nil {
			return h.formatError(id, -32603, "Internal error", "Core capability service is not configured.")
		}

		branch, _ := params.Arguments["branch"].(string)
		commitMessage, _ := params.Arguments["commit-message"].(string)

		delegation, err := h.core.MeshGraft(context.Background(), core.MeshGraftInput{
			Substrate:     substrate,
			Branch:        branch,
			CommitMessage: commitMessage,
		})
		if err != nil {
			return h.formatResult(id, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "Mesh graft failed: " + err.Error(),
					},
				},
				"isError": true,
			})
		}

		message := fmt.Sprintf("Delegated substrate %s through mesh graft. Commit %s.", filepath.ToSlash(delegation.Workspace), delegation.Commit)
		return h.formatResult(id, map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": message,
				},
			},
			"isError": false,
		})
	}

	if params.Name == "promotePR" {
		substrate, ok := params.Arguments["substrate"].(string)
		if !ok || strings.TrimSpace(substrate) == "" {
			return h.formatError(id, -32602, "Invalid arguments", "The 'substrate' parameter is required.")
		}
		if h.core == nil {
			return h.formatError(id, -32603, "Internal error", "Core capability service is not configured.")
		}

		branch, _ := params.Arguments["branch"].(string)
		prNumber, _ := params.Arguments["pr-number"].(string)
		commitMessage, _ := params.Arguments["commit-message"].(string)

		promotion, err := h.core.MeshPromote(context.Background(), core.MeshPromoteInput{
			Substrate:     substrate,
			Branch:        branch,
			PRNumber:      prNumber,
			CommitMessage: commitMessage,
		})
		if err != nil {
			return h.formatResult(id, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "PR promotion failed: " + err.Error(),
					},
				},
				"isError": true,
			})
		}

		message := fmt.Sprintf("Promoted pull request via mesh graft for %s. Commit %s.", filepath.ToSlash(promotion.Workspace), promotion.Commit)
		if promotion.PRNumber != "" {
			message = fmt.Sprintf("Promoted pull request #%s via mesh graft for %s. Commit %s.", promotion.PRNumber, filepath.ToSlash(promotion.Workspace), promotion.Commit)
		}

		return h.formatResult(id, map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": message,
				},
			},
			"isError": false,
		})
	}

	if params.Name == "createGenotype" {
		// Deprecated alias of the governed genotype.create capability.
		if _, ok := params.Arguments["substrate"]; !ok {
			// Inject fallback substrate for legacy clients
			params.Arguments["substrate"] = "core"
		}

		decision := h.authorizeDelegatedTool(core.CapGenotypeCreate, params.Arguments)
		if !decision.Authorized {
			return h.formatDelegationDenied(id, decision)
		}
		callCtx := core.WithPollen(context.Background(), h.pollen)

		resBytes := h.callCoreCapabilityAs(callCtx, id, core.CapGenotypeCreate, params.Arguments)
		if !strings.Contains(string(resBytes), `"isError":true`) {
			if err := SyncGenotypeIndex(); err != nil {
				log.Printf("[MCP] Failed to sync genotype index after createGenotype: %v", err)
			}
		}
		return resBytes
	}

	// Deprecated alias of the governed sequence.grow capability:
	// same Core, legacy tool name, legacy argument-key fallbacks
	// (path/sequence), and text rendering preserved for existing MCP
	// clients. Adapter translation only — the execution that used to run
	// inline here is now behind the Core's SequenceOperations port.
	if params.Name == "runSequence" {
		pathOrName, ok := params.Arguments["pathOrName"].(string)
		if !ok || strings.TrimSpace(pathOrName) == "" {
			if alt, altOK := params.Arguments["path"].(string); altOK {
				pathOrName = alt
			}
		}
		if strings.TrimSpace(pathOrName) == "" {
			if alt, altOK := params.Arguments["sequence"].(string); altOK {
				pathOrName = alt
			}
		}
		if strings.TrimSpace(pathOrName) == "" {
			return h.formatError(id, -32602, "Invalid arguments", "The 'pathOrName' parameter is required.")
		}
		if h.core == nil {
			return h.formatError(id, -32603, "Internal error", "Core capability service is not configured.")
		}

		result, runErr := h.core.SequenceRun(context.Background(), core.SequenceRunInput{PathOrName: pathOrName})
		summary := summarizeSequenceResult(result)
		if runErr != nil {
			return h.formatResult(id, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": fmt.Sprintf("Sequence run failed: %v\n\n%s", runErr, summary),
					},
				},
				"isError": true,
			})
		}

		return h.formatResult(id, map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": summary,
				},
			},
			"isError": false,
		})
	}

	// Deprecated alias of the governed sprout.grow capability:
	// same Core, legacy tool name, legacy protocol errors, and
	// text rendering preserved for existing MCP clients. Adapter
	// translation only — the substrate resolution, terrarium execution,
	// and run recording that used to live inline here are now behind the
	// Core's SproutOperations port.
	if params.Name != "sproutTendril" {
		return h.formatError(id, -32601, "Tool not found", nil)
	}

	transcript, ok := params.Arguments["transcript"].(string)
	substrate, subOk := params.Arguments["substrate"].(string)
	if !ok || !subOk || strings.TrimSpace(transcript) == "" || strings.TrimSpace(substrate) == "" {
		return h.formatError(id, -32602, "Invalid arguments", "The 'transcript' and 'substrate' parameters are required.")
	}
	if h.core == nil {
		return h.formatError(id, -32603, "Internal error", "Core capability service is not configured.")
	}

	// This deprecated alias reaches the delegated sprout.grow
	// operation-class, so it passes the same delegation gate as the
	// canonical tool — no alias path may reach a delegated capability
	// ungoverned.
	if decision := h.authorizeDelegatedTool(core.CapSproutGrow, params.Arguments); !decision.Authorized {
		return h.formatDelegationDenied(id, decision)
	}

	stepID, _ := params.Arguments["stepId"].(string)
	sessionID, _ := params.Arguments["sessionId"].(string)
	substrateURL, _ := params.Arguments["substrateUrl"].(string)
	substrateBranch, _ := params.Arguments["substrateBranch"].(string)
	if strings.TrimSpace(sessionID) == "" {
		// The pinned stdio session is MCP-surface metadata (the historic
		// resolveSession fallback), so the adapter fills it in before the
		// transport-free Core binds session preferences.
		sessionID = h.defaultSessionID
	}

	result, err := h.core.SproutRun(context.Background(), core.SproutRunInput{
		Transcript:      transcript,
		Substrate:       substrate,
		StepID:          stepID,
		SessionID:       sessionID,
		SubstrateURL:    substrateURL,
		SubstrateBranch: substrateBranch,
		Origin:          session.OriginMCP,
	})
	if err != nil {
		log.Printf("[MCP] Tendril execution failed: %v", err)
		return h.formatResult(id, map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "Task execution failed: " + err.Error(),
				},
			},
			"isError": true,
		})
	}

	return h.formatResult(id, map[string]interface{}{
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": result.Output,
			},
		},
		"isError": false,
	})

}
