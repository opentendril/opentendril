package receptors

import (
	"encoding/json"

	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

type MCPHandler struct {
	substratesConfig *conductor.SubstratesConfig
	sessions         *session.Manager
	history          *historydb.Store
	defaultSessionID string
	core             core.Core
	// delegation gates delegated-class capability invocations (see
	// core.DelegatedCapabilityNames) against the active grants. A nil gate
	// denies every delegated-class invocation: with no delegation configured,
	// delegated capabilities are unreachable over MCP while every
	// non-delegated capability dispatches untouched.
	delegation *DelegationGate
	// pollen is the Pollen bound to this MCP
	// connection at bind-time. The pollen is a property of the trusted
	// connection — never declared per-invocation in tool arguments, so a
	// caller can never self-declare its own pollen. Empty means no pollen
	// is bound and every delegated-class invocation is denied (deny-closed).
	pollen string
}

func NewMCPHandler() *MCPHandler {
	if err := SyncGenotypeIndex(); err != nil {
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

// WithDelegation binds the delegation gate and the bind-time Pollen onto this MCP adapter and returns it for chaining. Every
// delegated-class tool invocation on this connection is authorized as that
// one pollen against the active grants — the same per-invocation
// authorization the REST adapters apply.
func (h *MCPHandler) WithDelegation(gate *DelegationGate, pollen string) *MCPHandler {
	h.delegation = gate
	h.pollen = strings.TrimSpace(pollen)
	return h
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
		if err := SyncGenotypeIndex(); err != nil {
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
		if !validConfigFileName(name) {
			return h.formatError(req.ID, -32602, "Invalid genotype name", nil)
		}

		root := resolveRepoRoot("")

		searchDirs, _ := conductor.DefinitionSearchPath(root, conductor.DefinitionKindGenotypes)

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
		return h.handleToolsList(req.ID)
	case "tools/call":
		return h.handleToolsCall(req.ID, req.Params)
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
