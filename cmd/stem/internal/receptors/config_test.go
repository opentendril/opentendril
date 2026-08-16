package receptors

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
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
	handler := NewConfigHandler(core.NewService(nil), filepath.Join(root, ".tendril"))

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
	handler := NewConfigHandler(core.NewService(nil), filepath.Join(root, ".tendril"))

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

// TestMCPCreateGenotypeRejectsTraversalNames proves the MCP surface enforces
// the same filename boundary as the REST config surface.
func TestMCPCreateGenotypeRejectsTraversalNames(t *testing.T) {
	root := chdirTempDir(t)
	grant := core.DelegationGrant{
		Pollen:           "test-pollen",
		OperationClasses: []string{core.CapGenotypeCreate},
		Substrates:       []string{"core"},
	}
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{grant}), Bus: nil}
	handler := NewMCPHandler().WithCore(core.NewService(nil)).WithDelegation(gate, "test-pollen")

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
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !resp.Result.IsError || len(resp.Result.Content) == 0 || !strings.Contains(resp.Result.Content[0].Text, "invalid genotype name") {
			t.Errorf("createGenotype(%q) expected capability error for invalid name, got %s", name, string(respBytes))
		}
	}

	escaped := filepath.Join(root, "escaped.json")
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatalf("traversal name escaped the genotypes directory: %s exists", escaped)
	}
}

func TestListSubstratesReturnsConfiguredNames(t *testing.T) {
	root := chdirTempDir(t)
	yaml := []byte("substrates:\n  opentendril:\n    path: .\n  docs:\n    path: ./docs\n")
	if err := os.WriteFile(filepath.Join(root, "substrates.yaml"), yaml, 0o644); err != nil {
		t.Fatalf("write substrates.yaml: %v", err)
	}

	handler := NewConfigHandler(core.NewService(nil), filepath.Join(root, ".tendril"))
	req := httptest.NewRequest(http.MethodGet, "/v1/config/substrates", nil)
	rec := httptest.NewRecorder()
	handler.ListSubstrates(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ListSubstrates status = %d, body %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Substrates []string `json:"substrates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Substrates) != 2 || payload.Substrates[0] != "docs" || payload.Substrates[1] != "opentendril" {
		t.Fatalf("substrates = %#v, want sorted [docs opentendril]", payload.Substrates)
	}
}

func TestListSubstratesEmptyWhenUnconfigured(t *testing.T) {
	chdirTempDir(t)
	handler := NewConfigHandler(core.NewService(nil), t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/v1/config/substrates", nil)
	rec := httptest.NewRecorder()
	handler.ListSubstrates(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ListSubstrates status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"substrates":[]`) && !strings.Contains(rec.Body.String(), `"substrates": []`) {
		t.Fatalf("unconfigured list = %s, want empty substrates array", rec.Body.String())
	}
}
