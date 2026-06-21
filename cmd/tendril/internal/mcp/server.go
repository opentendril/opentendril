package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/opentendril/cli/internal/proxy"
)

// Server handles the MCP JSON-RPC protocol over stdio.
type Server struct {
	brain *proxy.BrainClient
	in    *bufio.Reader
	out   io.Writer
}

// NewServer creates a new MCP stdio server.
func NewServer(brain *proxy.BrainClient) *Server {
	return &Server{
		brain: brain,
		in:    bufio.NewReader(os.Stdin),
		out:   os.Stdout,
	}
}

// Request is a standard JSON-RPC request
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a standard JSON-RPC response
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *Error      `json:"error,omitempty"`
}

// Error represents a JSON-RPC error
type Error struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Start begins the stdio read loop.
func (s *Server) Start() {
	// Log to stderr so we don't pollute the stdio JSON-RPC stream
	log.SetOutput(os.Stderr)
	log.Println("Starting MCP stdio server...")
	for {
		line, err := s.in.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("Error reading from stdin: %v", err)
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.sendError(nil, -32700, "Parse error", err.Error())
			continue
		}

		s.handleRequest(req)
	}
}

func (s *Server) handleRequest(req Request) {
	switch req.Method {
	case "initialize":
		s.sendResult(req.ID, map[string]interface{}{
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
		// Do nothing

	case "tools/list":
		// Proxy to the Brain
		respRaw, err := s.brain.SendMCPRequest("tools/list", nil)
		if err != nil {
			s.sendError(req.ID, -32603, "Internal error", err.Error())
			return
		}

		var jsonRpcResp Response
		if err := json.Unmarshal(respRaw, &jsonRpcResp); err != nil {
			s.sendError(req.ID, -32603, "Parse error from backend", err.Error())
			return
		}
		if jsonRpcResp.Error != nil {
			s.sendError(req.ID, jsonRpcResp.Error.Code, jsonRpcResp.Error.Message, jsonRpcResp.Error.Data)
			return
		}
		s.sendResult(req.ID, jsonRpcResp.Result)

	case "tools/call":
		var params interface{}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.sendError(req.ID, -32602, "Invalid params", err.Error())
			return
		}

		respRaw, err := s.brain.SendMCPRequest("tools/call", params)
		if err != nil {
			s.sendError(req.ID, -32603, "Internal error calling tool", err.Error())
			return
		}

		var jsonRpcResp Response
		if err := json.Unmarshal(respRaw, &jsonRpcResp); err != nil {
			s.sendError(req.ID, -32603, "Parse error from backend", err.Error())
			return
		}
		if jsonRpcResp.Error != nil {
			s.sendError(req.ID, jsonRpcResp.Error.Code, jsonRpcResp.Error.Message, jsonRpcResp.Error.Data)
			return
		}
		s.sendResult(req.ID, jsonRpcResp.Result)

	case "resources/list":
		// Mock exposing Ambient Memory as resources
		s.sendResult(req.ID, map[string]interface{}{
			"resources": []map[string]interface{}{
				{
					"uri":         "memory://ambient",
					"name":        "Ambient Vector Memory",
					"description": "The unified semantic memory graph for OpenTendril.",
				},
			},
		})
	default:
		s.sendError(req.ID, -32601, "Method not found", nil)
	}
}

func (s *Server) sendResult(id interface{}, result interface{}) {
	s.send(Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (s *Server) sendError(id interface{}, code int, msg string, data interface{}) {
	s.send(Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: msg,
			Data:    data,
		},
	})
}

func (s *Server) send(resp Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		log.Printf("Error marshaling response: %v", err)
		return
	}
	fmt.Fprintf(s.out, "%s\n", string(b))
}
