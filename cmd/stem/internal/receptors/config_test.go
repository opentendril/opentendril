package receptors

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestValidConfigFileName(t *testing.T) {
	valid := []string{"frontend-dev", "go_rules", "a.b", "alpha01", "..hidden"}
	for _, name := range valid {
		if !validConfigFileName(name) {
			t.Errorf("validConfigFileName(%q) = false, want true", name)
		}
	}

	invalid := []string{"", ".", "..", "../evil", "a/b", `a\b`, "/abs", `..\..\evil`}
	for _, name := range invalid {
		if validConfigFileName(name) {
			t.Errorf("validConfigFileName(%q) = true, want false", name)
		}
	}
}

// TestUploadGenotypeRejectsTraversalNames proves the REST config surface can
// never write a genotype outside the genotypes directory: a traversal name is
// rejected with 400 and no file appears at the escaped path.
func TestUploadGenotypeRejectsTraversalNames(t *testing.T) {
	root := chdirTempDir(t)
	handler := NewConfigHandler(filepath.Join(root, ".tendril"))

	escaped := filepath.Join(root, "escaped.json")
	for _, name := range []string{"../../escaped", "..", "a/b", `a\b`} {
		body, err := json.Marshal(map[string]any{
			"name":         name,
			"instructions": "should never be written",
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/v1/config/genotypes", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		handler.UploadGenotype(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("UploadGenotype(%q) status = %d, want %d", name, rec.Code, http.StatusBadRequest)
		}
	}

	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatalf("traversal name escaped the genotypes directory: %s exists", escaped)
	}
}

func TestUploadGenotypeAcceptsValidName(t *testing.T) {
	root := chdirTempDir(t)
	handler := NewConfigHandler(filepath.Join(root, ".tendril"))

	body, err := json.Marshal(map[string]any{
		"name":         "frontend-dev",
		"instructions": "You are a frontend developer.",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/config/genotypes", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.UploadGenotype(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("UploadGenotype status = %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	target := filepath.Join(root, ".tendril", "genotypes", "frontend-dev.json")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected genotype file at %s: %v", target, err)
	}
}

// TestUploadTriggerRejectsTraversalFilenames proves the trigger upload rejects
// filenames that would resolve outside the hormonal-triggers directory. (A
// filename with forward-slash components is already reduced to its base name
// by mime/multipart before the handler sees it, so the handler's own boundary
// covers the remaining traversal shapes: bare ".." and backslash separators.)
func TestUploadTriggerRejectsTraversalFilenames(t *testing.T) {
	root := chdirTempDir(t)
	handler := NewConfigHandler(filepath.Join(root, ".tendril"))

	for _, filename := range []string{"..", `..\escaped.sh`} {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := part.Write([]byte("#!/bin/sh\n")); err != nil {
			t.Fatalf("write form file: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close multipart writer: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/v1/config/triggers", &buf)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()
		handler.UploadTrigger(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("UploadTrigger(%q) status = %d, want %d", filename, rec.Code, http.StatusBadRequest)
		}
	}
}

// TestMCPCreateGenotypeRejectsTraversalNames proves the MCP surface enforces
// the same filename boundary as the REST config surface.
func TestMCPCreateGenotypeRejectsTraversalNames(t *testing.T) {
	root := chdirTempDir(t)
	handler := NewMCPHandler()

	for _, name := range []string{"../../escaped", "..", "a/b", `a\b`} {
		reqBytes, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name": "createGenotype",
				"arguments": map[string]any{
					"name":         name,
					"instructions": "should never be written",
				},
			},
		})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}

		respBytes := handler.ProcessMCPMessage(reqBytes)
		var resp struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Errorf("createGenotype(%q) expected -32602 Invalid name error, got %s", name, string(respBytes))
		}
	}

	escaped := filepath.Join(root, "escaped.json")
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatalf("traversal name escaped the genotypes directory: %s exists", escaped)
	}
}
