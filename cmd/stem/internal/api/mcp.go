package api

import (
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
		h.sendError(w, nil, -32700, "Parse error", err.Error())
		return
	}

	switch req.Method {
	case "resources/list":
		genotypesDir := "./.tendril/genotypes"
		entries, err := os.ReadDir(genotypesDir)
		if err != nil && !os.IsNotExist(err) {
			h.sendError(w, req.ID, -32603, "Internal error", err.Error())
			return
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

		h.sendResult(w, req.ID, map[string]interface{}{
			"resources": resources,
		})

	case "resources/read":
		var params struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			h.sendError(w, req.ID, -32602, "Invalid params", err.Error())
			return
		}

		if !strings.HasPrefix(params.URI, "genotype://") {
			h.sendError(w, req.ID, -32602, "Invalid URI scheme", nil)
			return
		}

		name := strings.TrimPrefix(params.URI, "genotype://")
		if strings.Contains(name, "/") || strings.Contains(name, "\\") || name == "" {
			h.sendError(w, req.ID, -32602, "Invalid genotype name", nil)
			return
		}

		filePath := filepath.Join("./.tendril/genotypes", name+".json")
		content, err := os.ReadFile(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				h.sendError(w, req.ID, -32602, "Resource not found", nil)
			} else {
				h.sendError(w, req.ID, -32603, "Internal error", err.Error())
			}
			return
		}

		h.sendResult(w, req.ID, map[string]interface{}{
			"contents": []map[string]interface{}{
				{
					"uri":      params.URI,
					"mimeType": "application/json",
					"text":     string(content),
				},
			},
		})

	case "tools/list":
		h.sendResult(w, req.ID, map[string]interface{}{
			"tools": []map[string]interface{}{
				{
					"name":        "sproutTendril",
					"description": "Delegates a complex coding task to the autonomous OpenTendril brain. Use this tool when you need an agent to run terminal commands, debug complex errors, search the web, or execute multi-step engineering tasks inside a secure sandbox.",
					"inputSchema": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"task": map[string]interface{}{
								"type":        "string",
								"description": "A clear, actionable description of the task for Tendril to execute.",
							},
						},
						"required": []string{"task"},
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
			h.sendError(w, req.ID, -32602, "Invalid params", err.Error())
			return
		}

		if params.Name == "createGenotype" {
			name, nameOk := params.Arguments["name"].(string)
			instructions, instOk := params.Arguments["instructions"].(string)
			if !nameOk || !instOk || name == "" || instructions == "" {
				h.sendError(w, req.ID, -32602, "Invalid arguments", "The 'name' and 'instructions' parameters are required.")
				return
			}
			if strings.Contains(name, "/") || strings.Contains(name, "\\") {
				h.sendError(w, req.ID, -32602, "Invalid name", "The 'name' cannot contain slashes.")
				return
			}

			genotypesDir := "./.tendril/genotypes"
			os.MkdirAll(genotypesDir, 0755)

			payload := map[string]interface{}{
				"name":         name,
				"instructions": instructions,
			}
			fileContent, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				h.sendError(w, req.ID, -32603, "Internal error", err.Error())
				return
			}

			targetPath := filepath.Join(genotypesDir, name+".json")
			if err := os.WriteFile(targetPath, fileContent, 0644); err != nil {
				h.sendError(w, req.ID, -32603, "Failed to write genotype", err.Error())
				return
			}

			log.Printf("[MCP] Dynamically created genotype: %s", name)
			h.sendResult(w, req.ID, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": fmt.Sprintf("Successfully created genotype '%s'. You can now use it.", name),
					},
				},
				"isError": false,
			})
			return
		}

		if params.Name != "sproutTendril" {
			h.sendError(w, req.ID, -32601, "Tool not found", nil)
			return
		}

		task, ok := params.Arguments["task"].(string)
		if !ok || strings.TrimSpace(task) == "" {
			h.sendError(w, req.ID, -32602, "Invalid arguments", "The 'task' parameter is required.")
			return
		}

		log.Printf("[MCP] Delegating task to Tendril: %s", task)
		orch := &orchestrator.DockerOrchestrator{
			ImageName: "opentendril-tendril:latest",
		}
		output, err := orch.RunTendril(r.Context(), task)
		if err != nil {
			log.Printf("[MCP] Tendril execution failed: %v", err)
			h.sendResult(w, req.ID, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "Task execution failed: " + err.Error(),
					},
				},
				"isError": true,
			})
			return
		}

		h.sendResult(w, req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": output,
				},
			},
			"isError": false,
		})

	default:
		h.sendError(w, req.ID, -32601, "Method not found", nil)
	}
}

func (h *MCPHandler) sendResult(w http.ResponseWriter, id interface{}, result interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(mcpResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (h *MCPHandler) sendError(w http.ResponseWriter, id interface{}, code int, msg string, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(mcpResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &mcpError{
			Code:    code,
			Message: msg,
			Data:    data,
		},
	})
}
