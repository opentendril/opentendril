package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/orchestrator"
	"gopkg.in/yaml.v3"
)

type MCPHandler struct {
	substratesConfig *orchestrator.SubstratesConfig
}

func NewMCPHandler() *MCPHandler {
	if err := syncGenotypeIndex(); err != nil {
		log.Printf("[MCP] Failed to sync genotype index on startup: %v", err)
	}

	substratesConfig, err := orchestrator.LoadSubstratesConfig("")
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
		filePath := filepath.Join(root, ".tendril", "genotypes", name+".json")
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
					"name":        "runSequence",
					"description": "Runs a YAML sequence from .tendril/sequences/ or a relative path using the parallel sequence conductor.",
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
					"description": "Delegates a complex coding task to the autonomous OpenTendril brain. Use this tool when you need an agent to run terminal commands, debug complex errors, search the web, or execute multi-step engineering tasks inside a secure sandbox.",
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
					"description": "Returns the concatenated contents of all Markdown files in .tendril/genome/ so the agent can read active repository rules and guidelines.",
					"inputSchema": map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					},
				},
				{
					"name":        "reduceGenome",
					"description": "Deduplicates, compresses, and merges the epigenetic rules in .tendril/genome/epigenetics.md to prevent context window bloat.",
					"inputSchema": map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					},
				},
				{
					"name":        "injectPlasmid",
					"description": "Injects a modular plasmid rule file (e.g. go-rules, react-style) into the active project genome.",
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

		if params.Name == "viewGenome" {
			root := resolveRepoRoot("")
			body, count, err := readGenomeMarkdown(root)
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

			if count == 0 {
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

			return h.formatResult(req.ID, map[string]interface{}{
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": body,
					},
				},
				"isError": false,
			})
		}

		if params.Name == "reduceGenome" {
			root := resolveRepoRoot("")
			chronicler := orchestrator.NewEpigeneticChronicler(root)
			if err := chronicler.ReduceGenomeFile(context.Background()); err != nil {
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
						"text": fmt.Sprintf("Successfully reduced genome at %s.", filepath.ToSlash(filepath.Join(root, ".tendril", "genome", "epigenetics.md"))),
					},
				},
				"isError": false,
			})
		}

		if params.Name == "injectPlasmid" {
			name, ok := params.Arguments["name"].(string)
			if !ok || strings.TrimSpace(name) == "" {
				return h.formatError(req.ID, -32602, "Invalid arguments", "The 'name' parameter is required.")
			}

			root := resolveRepoRoot("")
			sourcePath, destPath, alreadyActive, err := orchestrator.InjectPlasmidIntoGenome(root, name)
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

			message := fmt.Sprintf("Injected plasmid %s -> %s.", filepath.ToSlash(mustRel(root, sourcePath)), filepath.ToSlash(mustRel(root, destPath)))
			if alreadyActive {
				message = fmt.Sprintf("Plasmid already active: %s.", filepath.ToSlash(mustRel(root, destPath)))
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

			seq, runErr := orchestrator.RunSequence(context.Background(), pathOrName, orchestrator.SequenceRunOptions{
				Stdout:      io.Discard,
				Stderr:      os.Stderr,
				Interactive: false,
			})
			summary := summarizeSequenceResult(seq)
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

		if params.Name != "sproutTendril" {
			return h.formatError(req.ID, -32601, "Tool not found", nil)
		}

		transcript, ok := params.Arguments["transcript"].(string)
		substrate, subOk := params.Arguments["substrate"].(string)
		if !ok || !subOk || strings.TrimSpace(transcript) == "" || strings.TrimSpace(substrate) == "" {
			return h.formatError(req.ID, -32602, "Invalid arguments", "The 'transcript' and 'substrate' parameters are required.")
		}

		stepID, _ := params.Arguments["stepId"].(string)
		if strings.TrimSpace(stepID) == "" {
			stepID = fmt.Sprintf("step-%d", time.Now().UTC().UnixNano())
		}

		substrateURL, _ := params.Arguments["substrateUrl"].(string)
		explicitSubstrateURL := strings.TrimSpace(substrateURL)
		substrateBranch, _ := params.Arguments["substrateBranch"].(string)
		resolvedSubstrate := strings.TrimSpace(substrate)
		substrateIsNamed := false
		substrateHasLocalPath := false
		if substrateSpec, isName := orchestrator.ResolveSubstrate(substrate, h.substratesConfig); isName && substrateSpec != nil {
			substrateIsNamed = true
			if strings.TrimSpace(substrateURL) == "" {
				substrateURL = strings.TrimSpace(substrateSpec.URL)
			}
			if strings.TrimSpace(substrateBranch) == "" {
				substrateBranch = strings.TrimSpace(substrateSpec.Branch)
			}
			if trimmedPath := strings.TrimSpace(substrateSpec.Path); trimmedPath != "" {
				if info, err := os.Stat(trimmedPath); err == nil && info.IsDir() {
					resolvedSubstrate = trimmedPath
					substrateHasLocalPath = true
				}
			}
		}

		statusPath := ""
		if explicitSubstrateURL == "" {
			if !substrateIsNamed || substrateHasLocalPath {
				if resolvedSubstrate != "" {
					statusPath = filepath.Join(resolveRepoRoot(resolvedSubstrate), "tendril-status.json")
				}
			}
		}

		log.Printf("[MCP] Delegating transcript to Tendril step %s: %s (Substrate: %s, URL: %s)", stepID, transcript, resolvedSubstrate, substrateURL)
		orch := &orchestrator.DockerOrchestrator{
			Substrate:       resolvedSubstrate,
			SubstrateURL:    substrateURL,
			SubstrateBranch: substrateBranch,
			StepID:          stepID,
			StatusPath:      statusPath,
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
	genotypesDir := filepath.Join(root, ".tendril", "genotypes")
	entries, err := os.ReadDir(genotypesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return genotypeIndex{Genotypes: []genotypeIndexEntry{}}, nil
		}
		return genotypeIndex{}, fmt.Errorf("read genotypes directory: %w", err)
	}

	index := genotypeIndex{Genotypes: []genotypeIndexEntry{}}
	var errs []error

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}

		filePath := filepath.Join(genotypesDir, entry.Name())
		content, err := os.ReadFile(filePath)
		if err != nil {
			errs = append(errs, fmt.Errorf("read genotype %s: %w", entry.Name(), err))
			continue
		}

		var genotype genotypeMetadata
		if err := json.Unmarshal(content, &genotype); err != nil {
			errs = append(errs, fmt.Errorf("decode genotype %s: %w", entry.Name(), err))
			continue
		}

		name := strings.TrimSpace(genotype.Name)
		if name == "" {
			name = strings.TrimSuffix(entry.Name(), ".json")
		}

		description := strings.TrimSpace(genotype.Description)
		if description == "" {
			description = firstNWords(genotype.Instructions, 20)
		}

		index.Genotypes = append(index.Genotypes, genotypeIndexEntry{
			Name:        name,
			Description: description,
		})
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

func readGenomeMarkdown(root string) (string, int, error) {
	genomeDir := filepath.Join(root, ".tendril", "genome")
	entries, err := os.ReadDir(genomeDir)
	if err != nil && !os.IsNotExist(err) {
		return "", 0, fmt.Errorf("read genome directory: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		files = append(files, filepath.Join(genomeDir, entry.Name()))
	}

	sort.Strings(files)
	if len(files) == 0 {
		return "", 0, nil
	}

	var parts []string
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return "", 0, fmt.Errorf("read genome file %s: %w", path, err)
		}
		parts = append(parts, string(content))
	}

	return strings.Join(parts, "\n\n"), len(files), nil
}

func summarizeSequenceResult(seq *orchestrator.Sequence) string {
	if seq == nil {
		return "Sequence run completed."
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Sequence %s", seq.Name))
	if strings.TrimSpace(seq.Substrate) != "" {
		lines = append(lines, fmt.Sprintf("Substrate: %s", filepath.ToSlash(seq.Substrate)))
	}
	if strings.TrimSpace(seq.Branch) != "" {
		lines = append(lines, fmt.Sprintf("Branch: %s", seq.Branch))
	}
	lines = append(lines, "Steps:")
	for _, step := range seq.Steps {
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
