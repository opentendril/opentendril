package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSyncGenotypeIndexWritesMetadata(t *testing.T) {
	root := chdirTempDir(t)
	genotypesDir := filepath.Join(root, ".tendril", "genotypes")
	if err := os.MkdirAll(genotypesDir, 0o755); err != nil {
		t.Fatalf("mkdir genotypes dir: %v", err)
	}

	alphaWords := make([]string, 21)
	for i := range alphaWords {
		alphaWords[i] = fmt.Sprintf("word-%02d", i+1)
	}

	writeJSONFile(t, filepath.Join(genotypesDir, "alpha.json"), map[string]any{
		"name":         "alpha",
		"instructions": strings.Join(alphaWords, " "),
	})
	writeJSONFile(t, filepath.Join(genotypesDir, "beta.json"), map[string]any{
		"name":         "beta",
		"description":  "Explicit description",
		"instructions": "ignored instructions",
	})

	if err := syncGenotypeIndex(); err != nil {
		t.Fatalf("syncGenotypeIndex failed: %v", err)
	}

	indexPath := filepath.Join(genotypesDir, "index.yaml")
	content, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index.yaml: %v", err)
	}

	var index genotypeIndex
	if err := yaml.Unmarshal(content, &index); err != nil {
		t.Fatalf("decode index.yaml: %v", err)
	}

	if len(index.Genotypes) != 2 {
		t.Fatalf("index genotype count = %d, want 2", len(index.Genotypes))
	}
	if index.Genotypes[0].Name != "alpha" {
		t.Fatalf("first genotype name = %q, want alpha", index.Genotypes[0].Name)
	}
	if index.Genotypes[0].Description != strings.Join(alphaWords[:20], " ") {
		t.Fatalf("alpha description = %q, want first 20 words", index.Genotypes[0].Description)
	}
	if index.Genotypes[1].Name != "beta" {
		t.Fatalf("second genotype name = %q, want beta", index.Genotypes[1].Name)
	}
	if index.Genotypes[1].Description != "Explicit description" {
		t.Fatalf("beta description = %q, want Explicit description", index.Genotypes[1].Description)
	}
}

func TestMCPResourcesListAndRead(t *testing.T) {
	root := chdirTempDir(t)
	genotypesDir := filepath.Join(root, ".tendril", "genotypes")
	if err := os.MkdirAll(genotypesDir, 0o755); err != nil {
		t.Fatalf("mkdir genotypes dir: %v", err)
	}

	alphaWords := make([]string, 21)
	for i := range alphaWords {
		alphaWords[i] = fmt.Sprintf("alpha-%02d", i+1)
	}

	writeJSONFile(t, filepath.Join(genotypesDir, "alpha.json"), map[string]any{
		"name":         "alpha",
		"instructions": strings.Join(alphaWords, " "),
	})
	writeJSONFile(t, filepath.Join(genotypesDir, "beta.json"), map[string]any{
		"name":         "beta",
		"description":  "Beta genotype",
		"instructions": "beta instructions",
		"plasmids":     []string{"react-conventions"},
	})

	handler := NewMCPHandler()

	staleIndex := []byte("genotypes:\n  - name: alpha\n    description: stale description\n")
	if err := os.WriteFile(filepath.Join(genotypesDir, "index.yaml"), staleIndex, 0o644); err != nil {
		t.Fatalf("write stale index: %v", err)
	}

	listRespBytes := handler.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`))
	var listResp struct {
		JSONRPC string `json:"jsonrpc"`
		Result  struct {
			Resources []struct {
				URI         string `json:"uri"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"resources"`
		} `json:"result"`
		Error *mcpError `json:"error"`
	}
	if err := json.Unmarshal(listRespBytes, &listResp); err != nil {
		t.Fatalf("decode resources/list response: %v", err)
	}
	if listResp.Error != nil {
		t.Fatalf("resources/list returned error: %+v", listResp.Error)
	}
	if len(listResp.Result.Resources) != 2 {
		t.Fatalf("resources count = %d, want 2", len(listResp.Result.Resources))
	}
	if listResp.Result.Resources[0].URI != "genotype://alpha" {
		t.Fatalf("alpha URI = %q, want genotype://alpha", listResp.Result.Resources[0].URI)
	}
	if listResp.Result.Resources[0].Name != "alpha" {
		t.Fatalf("alpha name = %q, want alpha", listResp.Result.Resources[0].Name)
	}
	if listResp.Result.Resources[0].Description != strings.Join(alphaWords[:20], " ") {
		t.Fatalf("alpha description = %q, want first 20 words", listResp.Result.Resources[0].Description)
	}
	if listResp.Result.Resources[1].Description != "Beta genotype" {
		t.Fatalf("beta description = %q, want Beta genotype", listResp.Result.Resources[1].Description)
	}

	indexContent, err := os.ReadFile(filepath.Join(genotypesDir, "index.yaml"))
	if err != nil {
		t.Fatalf("read synced index: %v", err)
	}
	var index genotypeIndex
	if err := yaml.Unmarshal(indexContent, &index); err != nil {
		t.Fatalf("decode synced index: %v", err)
	}
	if len(index.Genotypes) != 2 {
		t.Fatalf("synced index genotype count = %d, want 2", len(index.Genotypes))
	}
	if index.Genotypes[0].Description != strings.Join(alphaWords[:20], " ") {
		t.Fatalf("synced alpha description = %q, want first 20 words", index.Genotypes[0].Description)
	}

	readRespBytes := handler.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"genotype://beta"}}`))
	var readResp struct {
		JSONRPC string `json:"jsonrpc"`
		Result  struct {
			Contents []struct {
				URI      string `json:"uri"`
				MimeType string `json:"mimeType"`
				Text     string `json:"text"`
			} `json:"contents"`
		} `json:"result"`
		Error *mcpError `json:"error"`
	}
	if err := json.Unmarshal(readRespBytes, &readResp); err != nil {
		t.Fatalf("decode resources/read response: %v", err)
	}
	if readResp.Error != nil {
		t.Fatalf("resources/read returned error: %+v", readResp.Error)
	}
	if len(readResp.Result.Contents) != 1 {
		t.Fatalf("resources/read content count = %d, want 1", len(readResp.Result.Contents))
	}
	if readResp.Result.Contents[0].URI != "genotype://beta" {
		t.Fatalf("read URI = %q, want genotype://beta", readResp.Result.Contents[0].URI)
	}
	if readResp.Result.Contents[0].MimeType != "application/json" {
		t.Fatalf("read mimeType = %q, want application/json", readResp.Result.Contents[0].MimeType)
	}

	var genotypePayload map[string]any
	if err := json.Unmarshal([]byte(readResp.Result.Contents[0].Text), &genotypePayload); err != nil {
		t.Fatalf("decode genotype payload: %v", err)
	}
	if genotypePayload["name"] != "beta" {
		t.Fatalf("read name = %v, want beta", genotypePayload["name"])
	}
	if genotypePayload["description"] != "Beta genotype" {
		t.Fatalf("read description = %v, want Beta genotype", genotypePayload["description"])
	}
}

func TestUploadGenotypeSyncsIndex(t *testing.T) {
	root := chdirTempDir(t)
	tendrilDir := filepath.Join(root, ".tendril")
	handler := NewConfigHandler(tendrilDir)

	payload := map[string]any{
		"name":         "gamma",
		"description":  "Gamma description",
		"instructions": "Gamma instructions for the genotype upload path.",
		"plasmids":     []string{"react-conventions"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/config/genotypes", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.UploadGenotype(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, want %d", rec.Code, http.StatusCreated)
	}

	indexContent, err := os.ReadFile(filepath.Join(tendrilDir, "genotypes", "index.yaml"))
	if err != nil {
		t.Fatalf("read synced index: %v", err)
	}
	var index genotypeIndex
	if err := yaml.Unmarshal(indexContent, &index); err != nil {
		t.Fatalf("decode synced index: %v", err)
	}
	if len(index.Genotypes) != 1 {
		t.Fatalf("index genotype count = %d, want 1", len(index.Genotypes))
	}
	if index.Genotypes[0].Name != "gamma" {
		t.Fatalf("index name = %q, want gamma", index.Genotypes[0].Name)
	}
	if index.Genotypes[0].Description != "Gamma description" {
		t.Fatalf("index description = %q, want Gamma description", index.Genotypes[0].Description)
	}

	genotypePath := filepath.Join(tendrilDir, "genotypes", "gamma.json")
	if _, err := os.Stat(genotypePath); err != nil {
		t.Fatalf("expected genotype file to exist: %v", err)
	}
}

func chdirTempDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	return dir
}

func writeJSONFile(t *testing.T, path string, payload map[string]any) {
	t.Helper()

	content, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	content = append(content, '\n')

	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
