package receptors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/conductor"
	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/historydb"
	"github.com/opentendril/core/cmd/stem/internal/session"
	"gopkg.in/yaml.v3"
)

type MCPHandler struct {
	substratesConfig *conductor.SubstratesConfig
	sessions         *session.Manager
	history          *historydb.Store
	defaultSessionID string
	core             core.Core
}

func NewMCPHandler() *MCPHandler {
	if err := syncGenotypeIndex(); err != nil {
		log.Printf("[MCP] Failed to sync genotype index on startup: %v", err)
	}

	substratesConfig, err := conductor.LoadSubstratesConfig("")
	if err != nil {
		log.Printf("[MCP] Failed to load substrates config on startup: %v", err)
	}

	names := make([]string, 0)
	if substratesConfig != nil {
		names = make([]string, 0, len(substratesConfig.Substrates))
		for name := range substratesConfig.Substrates {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	if len(names) == 0 {
		log.Printf("[MCP] Loaded substrates config. Named substrates: none")
	} else {
		log.Printf("[MCP] Loaded substrates config. Named substrates: %s", strings.Join(names, ", "))
	}

	return &MCPHandler{substratesConfig: substratesConfig}
}

// WithSessions binds the unified SessionManager (and optional history store)
// so MCP interactions share state with the CLI and REST surfaces.
func (h *MCPHandler) WithSessions(manager *session.Manager, history *historydb.Store) *MCPHandler {
	h.sessions = manager
	h.history = history
	return h
}

// WithDefaultSession pins the Tendril session that MCP calls bind to when the
// client does not pass an explicit sessionId (e.g. one session per stdio
// server process).
func (h *MCPHandler) WithDefaultSession(sessionID string) *MCPHandler {
	h.defaultSessionID = sessionID
	return h
}

// WithCore binds the transport-free Core so this MCP adapter projects the same
// session-lifecycle capabilities as the REST and CLI surfaces.
func (h *MCPHandler) WithCore(coreSvc core.Core) *MCPHandler {
	h.core = coreSvc
	return h
}

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
func (h *MCPHandler) coreToolDefs() []map[string]interface{} {
	if h.core == nil {
		return nil
	}
	defs := make([]map[string]interface{}, 0, len(h.core.Capabilities()))
	for _, capability := range h.core.Capabilities() {
		defs = append(defs, map[string]interface{}{
			"name":        capability.Name,
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

// callCoreCapability invokes a Core capability and wraps its JSON result in an
// MCP tool-result envelope. Adapter translation only — no business logic.
func (h *MCPHandler) callCoreCapability(id interface{}, name string, args map[string]interface{}) []byte {
	result, err := h.core.Invoke(context.Background(), name, args)
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

func (h *MCPHandler) SetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1", h.HandleMCP)
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *mcpError   `json:"error,omitempty"`
}

type mcpError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (h *MCPHandler) HandleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req mcpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write(h.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error"}}`)))
		return
	}

	reqBytes, _ := json.Marshal(req)
	respBytes := h.ProcessMCPMessage(reqBytes)

	w.Header().Set("Content-Type", "application/json")
	w.Write(respBytes)
}

func (h *MCPHandler) ProcessMCPMessage(reqBytes []byte) []byte {
	var req mcpRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		return h.formatError(nil, -32700, "Parse error", err.Error())
	}

	switch req.Method {
	case "initialize":
		return h.formatResult(req.ID, map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]string{
				"name":    "opentendril",
				"version": "0.1.0",
			},
			"capabilities": map[string]interface{}{
				"tools":     map[string]interface{}{},
				"resources": map[string]interface{}{},
			},
		})

	case "notifications/initialized":
		// Just acknowledge without response
		return nil

	case "resources/list":
		root := resolveRepoRoot("")
		if err := syncGenotypeIndex(); err != nil {
			log.Printf("[MCP] Failed to sync genotype index before listing resources: %v", err)
		}

		index, err := loadGenotypeIndex(root)
		if err != nil {
			log.Printf("[MCP] Failed to load genotype index: %v", err)
			index, err = collectGenotypeIndex(root)
			if err != nil {
				log.Printf("[MCP] Failed to scan genotype metadata: %v", err)
			}
		}

		resources := make([]map[string]interface{}, 0, len(index.Genotypes))
		for _, genotype := range index.Genotypes {
			resources = append(resources, map[string]interface{}{
				"uri":         "genotype://" + genotype.Name,
				"name":        genotype.Name,
				"description": genotype.Description,
				"mimeType":    "application/json",
			})
		}

		return h.formatResult(req.ID, map[string]interface{}{
			"resources": resources,
		})

	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return h.formatError(req.ID, -32602, "Invalid params", err.Error())
		}

		if !strings.HasPrefix(params.URI, "genotype://") {
			return h.formatError(req.ID, -32602, "Invalid URI scheme", nil)
		}

		name := strings.TrimPrefix(params.URI, "genotype://")
		if strings.Contains(name, "/") || strings.Contains(name, "\\") || name == "" {
			return h.formatError(req.ID, -32602, "Invalid genotype name", nil)
		}

		root := resolveRepoRoot("")

		var searchDirs []string
		if configDir, err := os.UserConfigDir(); err == nil {
			searchDirs = append(searchDirs, filepath.Join(configDir, "opentendril", "genotypes"))
		}
		searchDirs = append(searchDirs, filepath.Join("/etc", "opentendril", "genotypes"))
		searchDirs = append(searchDirs, filepath.Join(root, ".tendril", "genotypes"))

		var content []byte
		var err error
		for _, dir := range searchDirs {
			filePath := filepath.Join(dir, name+".json")
			if c, readErr := os.ReadFile(filePath); readErr == nil {
				content = c
				err = nil
				break
			} else {
				err = readErr
			}
		}

		if content == nil {
			if os.IsNotExist(err) {
				return h.formatError(req.ID, -32602, "Resource not found", nil)
			}
			return h.formatError(req.ID, -32603, "Internal error", err.Error())
		}

		return h.formatResult(req.ID, map[string]interface{}{
			"contents": []map[string]interface{}{
				{
					"uri":      params.URI,
					"mimeType": "application/json",
					"text":     string(content),
				},
			},
		})

	case "tools/list":
		tools := []map[string]interface{}{
			{
				"name":        "runSequence",
				"description": "Deprecated alias of the governed sequence.run capability. Runs a YAML sequence from .tendril/sequences/ or a relative path using the parallel sequence meristem.",
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
				"description": "Deprecated alias of the governed sprout.run capability. Delegates a complex coding task to the autonomous OpenTendril brain. Use this tool when you need an agent to run terminal commands, debug complex errors, search the web, or execute multi-step engineering tasks inside a secure terrarium.",
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
							"description": "Optional remote repository URL override to clone and operate on dynamically. E.g. https://github.com/opentendril/core.git",
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
				"description": "Dynamically create or update an OpenTendril genotype (core identity/persona). Creates a new JSON configuration file in the genotypes directory. This allows you to define a new base role before sprouting a tendril.",
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
				"description": "Deprecated alias of the governed genome.view capability. Returns the concatenated contents of all Markdown files in .tendril/genome/ so the agent can read active repository rules and guidelines.",
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
		// Interface parity: project the Core session capabilities as MCP
		// tools so this surface stays in lockstep with REST and the CLI.
		tools = append(tools, h.coreToolDefs()...)
		return h.formatResult(req.ID, map[string]interface{}{
			"tools": tools,
		})
	case "tools/call":
		var params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return h.formatError(req.ID, -32602, "Invalid params", err.Error())
		}

		// Interface parity: Core session capabilities are dispatched
		// through the shared service, identically to REST and the CLI.
		if h.isCoreCapability(params.Name) {
			if params.Name == core.CapSproutRun {
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
			return h.callCoreCapability(req.ID, params.Name, params.Arguments)
		}

		// Deprecated aliases of the governed genome capabilities:
		// same Core, legacy tool names and text rendering preserved for
		// existing MCP clients. Adapter translation only — the business logic
		// that used to live inline here is now in core / the orchestrator port.
		if params.Name == "viewGenome" {
			if h.core == nil {
				return h.formatError(req.ID, -32603, "Internal error", "Core capability service is not configured.")
			}
			seeds, err := h.core.GenomeView(context.Background())
			if err != nil {
				return h.formatResult(req.ID, map[string]interface{}{
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
				return h.formatResult(req.ID, map[string]interface{}{
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
			return h.formatResult(req.ID, map[string]interface{}{
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
				return h.formatError(req.ID, -32603, "Internal error", "Core capability service is not configured.")
			}
			path, err := h.core.GenomeReduce(context.Background())
			if err != nil {
				return h.formatResult(req.ID, map[string]interface{}{
					"content": []map[string]interface{}{
						{
							"type": "text",
							"text": "Failed to reduce genome: " + err.Error(),
						},
					},
					"isError": true,
				})
			}

			return h.formatResult(req.ID, map[string]interface{}{
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
				return h.formatError(req.ID, -32602, "Invalid arguments", "The 'name' parameter is required.")
			}
			if h.core == nil {
				return h.formatError(req.ID, -32603, "Internal error", "Core capability service is not configured.")
			}

			injection, err := h.core.PlasmidInject(context.Background(), core.PlasmidInjectInput{Name: name})
			if err != nil {
				return h.formatResult(req.ID, map[string]interface{}{
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

			return h.formatResult(req.ID, map[string]interface{}{
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
				return h.formatError(req.ID, -32602, "Invalid arguments", "The 'substrate' parameter is required.")
			}
			if h.core == nil {
				return h.formatError(req.ID, -32603, "Internal error", "Core capability service is not configured.")
			}

			branch, _ := params.Arguments["branch"].(string)
			commitMessage, _ := params.Arguments["commit-message"].(string)

			delegation, err := h.core.MeshGraft(context.Background(), core.MeshGraftInput{
				Substrate:     substrate,
				Branch:        branch,
				CommitMessage: commitMessage,
			})
			if err != nil {
				return h.formatResult(req.ID, map[string]interface{}{
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
			return h.formatResult(req.ID, map[string]interface{}{
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
				return h.formatError(req.ID, -32602, "Invalid arguments", "The 'substrate' parameter is required.")
			}
			if h.core == nil {
				return h.formatError(req.ID, -32603, "Internal error", "Core capability service is not configured.")
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
				return h.formatResult(req.ID, map[string]interface{}{
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

			return h.formatResult(req.ID, map[string]interface{}{
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
			name, nameOk := params.Arguments["name"].(string)
			instructions, instOk := params.Arguments["instructions"].(string)
			if !nameOk || !instOk || name == "" || instructions == "" {
				return h.formatError(req.ID, -32602, "Invalid arguments", "The 'name' and 'instructions' parameters are required.")
			}
			if strings.Contains(name, "/") || strings.Contains(name, "\\") {
				return h.formatError(req.ID, -32602, "Invalid name", "The 'name' cannot contain slashes.")
			}

			genotypesDir := "./.tendril/genotypes"
			os.MkdirAll(genotypesDir, 0755)

			payload := map[string]interface{}{
				"name":         name,
				"instructions": instructions,
			}
			fileContent, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return h.formatError(req.ID, -32603, "Internal error", err.Error())
			}

			targetPath := filepath.Join(genotypesDir, name+".json")
			if err := os.WriteFile(targetPath, fileContent, 0644); err != nil {
				return h.formatError(req.ID, -32603, "Failed to write genotype", err.Error())
			}

			log.Printf("[MCP] Dynamically created genotype: %s", name)
			if err := syncGenotypeIndex(); err != nil {
				log.Printf("[MCP] Failed to sync genotype index after createGenotype: %v", err)
			}
			return h.formatResult(req.ID, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": fmt.Sprintf("Successfully created genotype '%s'. You can now use it.", name),
					},
				},
				"isError": false,
			})
		}

		// Deprecated alias of the governed sequence.run capability:
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
				return h.formatError(req.ID, -32602, "Invalid arguments", "The 'pathOrName' parameter is required.")
			}
			if h.core == nil {
				return h.formatError(req.ID, -32603, "Internal error", "Core capability service is not configured.")
			}

			result, runErr := h.core.SequenceRun(context.Background(), core.SequenceRunInput{PathOrName: pathOrName})
			summary := summarizeSequenceResult(result)
			if runErr != nil {
				return h.formatResult(req.ID, map[string]interface{}{
					"content": []map[string]interface{}{
						{
							"type": "text",
							"text": fmt.Sprintf("Sequence run failed: %v\n\n%s", runErr, summary),
						},
					},
					"isError": true,
				})
			}

			return h.formatResult(req.ID, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": summary,
					},
				},
				"isError": false,
			})
		}

		// Deprecated alias of the governed sprout.run capability:
		// same Core, legacy tool name, legacy protocol errors, and
		// text rendering preserved for existing MCP clients. Adapter
		// translation only — the substrate resolution, terrarium execution,
		// and run recording that used to live inline here are now behind the
		// Core's SproutOperations port.
		if params.Name != "sproutTendril" {
			return h.formatError(req.ID, -32601, "Tool not found", nil)
		}

		transcript, ok := params.Arguments["transcript"].(string)
		substrate, subOk := params.Arguments["substrate"].(string)
		if !ok || !subOk || strings.TrimSpace(transcript) == "" || strings.TrimSpace(substrate) == "" {
			return h.formatError(req.ID, -32602, "Invalid arguments", "The 'transcript' and 'substrate' parameters are required.")
		}
		if h.core == nil {
			return h.formatError(req.ID, -32603, "Internal error", "Core capability service is not configured.")
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
			return h.formatResult(req.ID, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "Task execution failed: " + err.Error(),
					},
				},
				"isError": true,
			})
		}

		return h.formatResult(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": result.Output,
				},
			},
			"isError": false,
		})

	default:
		return h.formatError(req.ID, -32601, "Method not found", nil)
	}
}

func (h *MCPHandler) formatResult(id interface{}, result interface{}) []byte {
	b, _ := json.Marshal(mcpResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
	return b
}

func (h *MCPHandler) formatError(id interface{}, code int, msg string, data interface{}) []byte {
	b, _ := json.Marshal(mcpResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &mcpError{
			Code:    code,
			Message: msg,
			Data:    data,
		},
	})
	return b
}

type genotypeIndex struct {
	Genotypes []genotypeIndexEntry `yaml:"genotypes"`
}

type genotypeIndexEntry struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
}

type genotypeMetadata struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Instructions string   `json:"instructions"`
	Plasmids     []string `json:"plasmids,omitempty"`
	DenyPlasmids []string `json:"denyPlasmids,omitempty"`
}

func syncGenotypeIndex() error {
	root := resolveRepoRoot("")
	index, err := collectGenotypeIndex(root)
	if writeErr := writeGenotypeIndex(root, index); writeErr != nil {
		return writeErr
	}
	return err
}

func collectGenotypeIndex(root string) (genotypeIndex, error) {
	var searchDirs []string

	if configDir, err := os.UserConfigDir(); err == nil {
		searchDirs = append(searchDirs, filepath.Join(configDir, "opentendril", "genotypes"))
	}
	searchDirs = append(searchDirs, filepath.Join("/etc", "opentendril", "genotypes"))
	searchDirs = append(searchDirs, filepath.Join(root, ".tendril", "genotypes"))

	genotypeMap := make(map[string]genotypeIndexEntry)
	var errs []error

	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("read genotypes directory %s: %w", dir, err))
			}
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
				continue
			}

			filePath := filepath.Join(dir, entry.Name())
			content, err := os.ReadFile(filePath)
			if err != nil {
				errs = append(errs, fmt.Errorf("read genotype %s: %w", filePath, err))
				continue
			}

			var genotype genotypeMetadata
			if err := json.Unmarshal(content, &genotype); err != nil {
				errs = append(errs, fmt.Errorf("decode genotype %s: %w", filePath, err))
				continue
			}

			name := strings.TrimSpace(genotype.Name)
			if name == "" {
				name = strings.TrimSuffix(entry.Name(), ".json")
			}

			if _, exists := genotypeMap[name]; exists {
				continue
			}

			description := strings.TrimSpace(genotype.Description)
			if description == "" {
				description = firstNWords(genotype.Instructions, 20)
			}

			genotypeMap[name] = genotypeIndexEntry{
				Name:        name,
				Description: description,
			}
		}
	}

	index := genotypeIndex{Genotypes: make([]genotypeIndexEntry, 0, len(genotypeMap))}
	for _, entry := range genotypeMap {
		index.Genotypes = append(index.Genotypes, entry)
	}

	sort.Slice(index.Genotypes, func(i, j int) bool {
		return strings.ToLower(index.Genotypes[i].Name) < strings.ToLower(index.Genotypes[j].Name)
	})

	return index, errors.Join(errs...)
}

func loadGenotypeIndex(root string) (genotypeIndex, error) {
	indexPath := filepath.Join(root, ".tendril", "genotypes", "index.yaml")
	content, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return genotypeIndex{Genotypes: []genotypeIndexEntry{}}, nil
		}
		return genotypeIndex{}, fmt.Errorf("read genotype index: %w", err)
	}

	var index genotypeIndex
	if err := yaml.Unmarshal(content, &index); err != nil {
		return genotypeIndex{}, fmt.Errorf("decode genotype index: %w", err)
	}

	if index.Genotypes == nil {
		index.Genotypes = []genotypeIndexEntry{}
	}

	return index, nil
}

func writeGenotypeIndex(root string, index genotypeIndex) error {
	genotypesDir := filepath.Join(root, ".tendril", "genotypes")
	if err := os.MkdirAll(genotypesDir, 0o755); err != nil {
		return fmt.Errorf("create genotypes directory: %w", err)
	}

	content, err := yaml.Marshal(index)
	if err != nil {
		return fmt.Errorf("encode genotype index: %w", err)
	}

	indexPath := filepath.Join(genotypesDir, "index.yaml")
	if err := os.WriteFile(indexPath, content, 0o644); err != nil {
		return fmt.Errorf("write genotype index: %w", err)
	}

	return nil
}

func firstNWords(text string, limit int) string {
	if limit <= 0 {
		return ""
	}

	words := strings.Fields(strings.TrimSpace(text))
	if len(words) <= limit {
		return strings.Join(words, " ")
	}

	return strings.Join(words[:limit], " ")
}

func resolveRepoRoot(path string) string {
	if strings.TrimSpace(path) == "" {
		wd, err := os.Getwd()
		if err != nil {
			path = "."
		} else {
			path = wd
		}
	}

	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return path
	}

	root := strings.TrimSpace(string(output))
	if root == "" {
		return path
	}

	return root
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

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}

	aAbs, err := filepath.Abs(a)
	if err != nil {
		aAbs = filepath.Clean(a)
	}
	bAbs, err := filepath.Abs(b)
	if err != nil {
		bAbs = filepath.Clean(b)
	}

	return filepath.Clean(aAbs) == filepath.Clean(bAbs)
}

func mustRel(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return target
	}
	return rel
}
