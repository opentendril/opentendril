package rhizome

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type PineconeMemoryBackend struct {
	apiKey    string
	baseURL   string
	client    *http.Client
	dimension int
}

func NewPineconeMemoryBackend(config MemoryConfig) (*PineconeMemoryBackend, error) {
	if strings.TrimSpace(config.PineconeBaseURL) == "" {
		return nil, fmt.Errorf("TENDRIL_PINECONE_BASE_URL is required")
	}
	if strings.TrimSpace(config.PineconeAPIKey) == "" {
		return nil, fmt.Errorf("TENDRIL_PINECONE_API_KEY is required")
	}
	dimension := config.PineconeDimension
	if dimension <= 0 {
		dimension = 8
	}
	return &PineconeMemoryBackend{
		apiKey:    config.PineconeAPIKey,
		baseURL:   strings.TrimRight(config.PineconeBaseURL, "/"),
		client:    &http.Client{Timeout: 15 * time.Second},
		dimension: dimension,
	}, nil
}

func (b *PineconeMemoryBackend) StoreMemory(ctx context.Context, memory Memory) error {
	if memory.CreatedAt.IsZero() {
		memory.CreatedAt = time.Now().UTC()
	}

	payload := map[string]any{
		"vectors": []map[string]any{{
			"id":       memoryID(memory.RepositoryName, memory.Title),
			"values":   textVector(memory.Title+" "+memory.Tags+" "+memory.Content, b.dimension),
			"metadata": pineconeMemoryMetadata(memory),
		}},
	}

	return b.doJSON(ctx, http.MethodPost, "/vectors/upsert", payload, nil)
}

func (b *PineconeMemoryBackend) SearchMemories(ctx context.Context, repositoryName string, query string, category string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 20
	}

	filter := map[string]any{"repositoryName": map[string]any{"$eq": repositoryName}}
	if strings.TrimSpace(category) != "" {
		filter["category"] = map[string]any{"$eq": category}
	}
	payload := map[string]any{
		"vector":          textVector(query, b.dimension),
		"topK":            limit,
		"includeMetadata": true,
		"filter":          filter,
	}

	var response struct {
		Matches []struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"matches"`
	}
	if err := b.doJSON(ctx, http.MethodPost, "/query", payload, &response); err != nil {
		return nil, err
	}

	memories := make([]Memory, 0, len(response.Matches))
	for _, match := range response.Matches {
		memories = append(memories, memoryFromMetadata(match.Metadata))
	}
	return memories, nil
}

func (b *PineconeMemoryBackend) ListMemories(ctx context.Context, repositoryName string, category string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 20
	}

	listPath := "/vectors/list?prefix=" + url.QueryEscape(repositoryName+"::") + "&limit=" + fmt.Sprint(limit)
	var listResponse struct {
		Vectors []struct {
			ID string `json:"id"`
		} `json:"vectors"`
	}
	if err := b.doJSON(ctx, http.MethodGet, listPath, nil, &listResponse); err != nil {
		return nil, err
	}
	if len(listResponse.Vectors) == 0 {
		return nil, nil
	}

	ids := make([]string, 0, len(listResponse.Vectors))
	for _, vector := range listResponse.Vectors {
		ids = append(ids, vector.ID)
	}
	fetchPayload := map[string]any{"ids": ids}
	var fetchResponse struct {
		Vectors map[string]struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"vectors"`
	}
	if err := b.doJSON(ctx, http.MethodPost, "/vectors/fetch", fetchPayload, &fetchResponse); err != nil {
		return nil, err
	}

	memories := make([]Memory, 0, len(fetchResponse.Vectors))
	for _, vector := range fetchResponse.Vectors {
		memory := memoryFromMetadata(vector.Metadata)
		if memory.RepositoryName != repositoryName {
			continue
		}
		if strings.TrimSpace(category) != "" && memory.Category != category {
			continue
		}
		memories = append(memories, memory)
	}
	return memories, nil
}

func (b *PineconeMemoryBackend) DeleteMemory(ctx context.Context, repositoryName string, title string) error {
	payload := map[string]any{"ids": []string{memoryID(repositoryName, title)}}
	return b.doJSON(ctx, http.MethodPost, "/vectors/delete", payload, nil)
}

func (b *PineconeMemoryBackend) doJSON(ctx context.Context, method string, path string, payload any, response any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode pinecone request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, b.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create pinecone request: %w", err)
	}
	request.Header.Set("Api-Key", b.apiKey)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.client.Do(request)
	if err != nil {
		return fmt.Errorf("pinecone request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		content, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("pinecone request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(content)))
	}
	if response == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return fmt.Errorf("decode pinecone response: %w", err)
	}
	return nil
}

func pineconeMemoryMetadata(memory Memory) map[string]any {
	return map[string]any{
		"repositoryName": memory.RepositoryName,
		"category":       memory.Category,
		"title":          memory.Title,
		"content":        memory.Content,
		"tags":           memory.Tags,
		"createdAt":      memory.CreatedAt.UTC().Format(time.RFC3339Nano),
		"sessionId":      memory.SessionID,
	}
}

func memoryFromMetadata(metadata map[string]any) Memory {
	memory := Memory{
		RepositoryName: stringMetadata(metadata, "repositoryName"),
		Category:       stringMetadata(metadata, "category"),
		Title:          stringMetadata(metadata, "title"),
		Content:        stringMetadata(metadata, "content"),
		Tags:           stringMetadata(metadata, "tags"),
		SessionID:      stringMetadata(metadata, "sessionId"),
	}
	if parsed, err := time.Parse(time.RFC3339Nano, stringMetadata(metadata, "createdAt")); err == nil {
		memory.CreatedAt = parsed
	}
	return memory
}

func stringMetadata(metadata map[string]any, key string) string {
	if value, ok := metadata[key].(string); ok {
		return value
	}
	return ""
}

func memoryID(repositoryName string, title string) string {
	sum := sha256.Sum256([]byte(repositoryName + "\x00" + title))
	return repositoryName + "::" + hex.EncodeToString(sum[:12])
}

func textVector(text string, dimension int) []float64 {
	if dimension <= 0 {
		dimension = 8
	}
	vector := make([]float64, dimension)
	if strings.TrimSpace(text) == "" {
		vector[0] = 1
		return vector
	}
	for index, char := range []byte(strings.ToLower(text)) {
		vector[index%dimension] += float64(char) / 255
	}
	return vector
}
