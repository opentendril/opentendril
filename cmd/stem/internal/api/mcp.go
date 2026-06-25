package api

import (
	"context"
	"encoding/json"
	"fmt"

	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/orchestrator"
)

type MCPHandler struct{}

func NewMCPHandler() *MCPHandler {
	return &MCPHandler{}
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
		genotypesDir := "./.tendril/genotypes"
		entries, err := os.ReadDir(genotypesDir)
		if err != nil && !os.IsNotExist(err) {
			return h.formatResult(req.ID, map[string]interface{}{"resources": []interface{}{}})
		}

		var resources []map[string]interface{}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
				name := strings.TrimSuffix(entry.Name(), ".json")
				resources = append(resources, map[string]interface{}{
					"uri":      "genotype://" + name,
					"name":     name,
					"mimeType": "application/json",
				})
			}
		}

		if resources == nil {
			resources = []map[string]interface{}{}
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

		filePath := filepath.Join("./.tendril/genotypes", name+".json")
		content, err := os.ReadFile(filePath)
		if err != nil {
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
		return h.formatResult(req.ID, map[string]interface{}{
			"tools": []map[string]interface{}{
				{
					"name":        "sproutTendril",
					"description": "Delegates a complex coding task to the autonomous OpenTendril brain. Use this tool when you need an agent to run terminal commands, debug complex errors, search the web, or execute multi-step engineering tasks inside a secure sandbox.",
					"inputSchema": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"transcript": map[string]interface{}{
								"type":        "string",
								"description": "A clear, actionable description of the transcript (task) for Tendril to execute.",
							},
							"substrate": map[string]interface{}{
								"type":        "string",
								"description": "The absolute path to the target repository workspace (the 'substrate'). E.g. /home/user/project",
							},
							"substrateUrl": map[string]interface{}{
								"type":        "string",
								"description": "Optional remote repository URL to clone and operate on dynamically. E.g. https://github.com/opentendril/core.git",
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
			},
		})
	case "tools/call":
		var params struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return h.formatError(req.ID, -32602, "Invalid params", err.Error())
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

		if params.Name != "sproutTendril" {
			return h.formatError(req.ID, -32601, "Tool not found", nil)
		}

		transcript, ok := params.Arguments["transcript"].(string)
		substrate, subOk := params.Arguments["substrate"].(string)
		if !ok || !subOk || strings.TrimSpace(transcript) == "" || strings.TrimSpace(substrate) == "" {
			return h.formatError(req.ID, -32602, "Invalid arguments", "The 'transcript' and 'substrate' parameters are required.")
		}

		substrateURL, _ := params.Arguments["substrateUrl"].(string)
		substrateBranch, _ := params.Arguments["substrateBranch"].(string)

		log.Printf("[MCP] Delegating transcript to Tendril: %s (Substrate: %s, URL: %s)", transcript, substrate, substrateURL)
		orch := &orchestrator.DockerOrchestrator{
			ImageName:       "opentendril-tendril:latest",
			Substrate:       substrate,
			SubstrateURL:    substrateURL,
			SubstrateBranch: substrateBranch,
		}
		output, err := orch.RunTendril(context.Background(), transcript)
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
					"text": output,
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
