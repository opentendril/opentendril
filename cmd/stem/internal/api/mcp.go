package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"

	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
			sourcePath, destPath, alreadyActive, err := injectPlasmidIntoGenome(root, name)
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
		substrateBranch, _ := params.Arguments["substrateBranch"].(string)
		statusPath := filepath.Join(resolveRepoRoot(substrate), "tendril-status.json")

		log.Printf("[MCP] Delegating transcript to Tendril step %s: %s (Substrate: %s, URL: %s)", stepID, transcript, substrate, substrateURL)
		orch := &orchestrator.DockerOrchestrator{
			Substrate:       substrate,
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

func injectPlasmidIntoGenome(root, name string) (string, string, bool, error) {
	sourcePath, err := findPlasmidSource(root, name)
	if err != nil {
		return "", "", false, err
	}

	destDir := filepath.Join(root, ".tendril", "genome")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", "", false, fmt.Errorf("create genome directory: %w", err)
	}

	destPath := filepath.Join(destDir, filepath.Base(sourcePath))
	if samePath(sourcePath, destPath) {
		return sourcePath, destPath, true, nil
	}

	if err := copyMarkdownFile(sourcePath, destPath); err != nil {
		return "", "", false, err
	}

	return sourcePath, destPath, false, nil
}

func findPlasmidSource(root, name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("missing plasmid name")
	}

	if filepath.IsAbs(trimmed) {
		if info, err := os.Stat(trimmed); err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(trimmed), ".md") {
			return trimmed, nil
		}
	}

	directCandidates := []string{
		filepath.Join(root, trimmed),
		filepath.Join(root, ".tendril", "genotypes", trimmed),
		filepath.Join(root, ".tendril", "genotypes", "plasmids", trimmed),
	}
	if filepath.Ext(trimmed) == "" {
		directCandidates = append(directCandidates,
			filepath.Join(root, trimmed+".md"),
			filepath.Join(root, ".tendril", "genotypes", trimmed+".md"),
			filepath.Join(root, ".tendril", "genotypes", "plasmids", trimmed+".md"),
		)
	}

	for _, candidate := range directCandidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(candidate), ".md") {
			return candidate, nil
		}
	}

	searchRoots := []string{
		filepath.Join(root, ".tendril", "genotypes", "plasmids"),
		filepath.Join(root, ".tendril", "genotypes"),
	}
	var matches []string
	targetBase := strings.TrimSuffix(filepath.Base(trimmed), filepath.Ext(trimmed))

	for _, searchRoot := range searchRoots {
		if info, err := os.Stat(searchRoot); err != nil || !info.IsDir() {
			continue
		}

		_ = filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
				return nil
			}

			base := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
			rel, relErr := filepath.Rel(searchRoot, path)
			if relErr != nil {
				rel = path
			}

			if strings.EqualFold(d.Name(), filepath.Base(trimmed)) ||
				strings.EqualFold(base, targetBase) ||
				strings.EqualFold(rel, trimmed) ||
				strings.EqualFold(strings.TrimSuffix(rel, filepath.Ext(rel)), strings.TrimSuffix(trimmed, filepath.Ext(trimmed))) {
				matches = append(matches, path)
			}

			return nil
		})
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("plasmid %q not found in .tendril/genotypes", trimmed)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", fmt.Errorf("plasmid %q is ambiguous; matches: %s", trimmed, strings.Join(matches, ", "))
	}
}

func copyMarkdownFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat plasmid source: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("plasmid source is a directory: %s", src)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create plasmid destination directory: %w", err)
	}
	_ = os.Remove(dst)

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open plasmid source: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create plasmid destination: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy plasmid: %w", err)
	}

	return nil
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
