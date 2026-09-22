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

const weaviateMemoryClass = "TendrilMemory"

type WeaviateMemoryBackend struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

func NewWeaviateMemoryBackend(config MemoryConfig) (*WeaviateMemoryBackend, error) {
	if strings.TrimSpace(config.WeaviateBaseURL) == "" {
		return nil, fmt.Errorf("TENDRIL_WEAVIATE_BASE_URL is required")
	}
	return &WeaviateMemoryBackend{
		apiKey:  config.WeaviateAPIKey,
		baseURL: strings.TrimRight(config.WeaviateBaseURL, "/"),
		client:  &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (b *WeaviateMemoryBackend) StoreMemory(ctx context.Context, memory Memory) error {
	if memory.StableID == "" {
		memory.StableID = StableMemoryIdentity(memory.RepositoryName, memory.Title)
	}
	if err := memory.Validate(); err != nil {
		return fmt.Errorf("invalid memory before persistence: %w", err)
	}

	if memory.CreatedAt.IsZero() {
		memory.CreatedAt = time.Now().UTC()
	}

	properties := map[string]any{
		"repositoryName": memory.RepositoryName,
		"category":       memory.Category,
		"title":          memory.Title,
		"content":        memory.Content,
		"tags":           memory.Tags,
		"createdAt":      memory.CreatedAt.UTC().Format(time.RFC3339Nano),
		"sessionId":      memory.SessionID,
	}
	if memory.Origin != "" {
		properties["origin"] = string(memory.Origin)
		properties["authority"] = string(memory.Authority)
		properties["status"] = string(memory.Status)
		properties["kind"] = string(memory.Kind)
		properties["provenance"] = memory.Provenance
		properties["sourceClass"] = memory.SourceClass
		properties["sourceIdentity"] = memory.SourceIdentity
		properties["contentIdentity"] = memory.ContentIdentity
		properties["revisionIdentity"] = memory.RevisionIdentity
		properties["stableId"] = memory.StableID
		properties["revisionMetadata"] = memory.RevisionMetadata
		properties["supersession"] = memory.Supersession
	}

	payload := map[string]any{
		"class":      weaviateMemoryClass,
		"id":         deterministicUUID(memory.RepositoryName, memory.Title),
		"properties": properties,
	}

	return b.doJSON(ctx, http.MethodPost, "/v1/objects", payload, nil)
}

func (b *WeaviateMemoryBackend) GetMemory(ctx context.Context, repositoryName string, title string) (Memory, bool, error) {
	objectID := deterministicUUID(repositoryName, title)
	requestURL := "/v1/objects/" + weaviateMemoryClass + "/" + objectID
	var response struct {
		Properties map[string]any `json:"properties"`
	}
	err := b.doJSON(ctx, http.MethodGet, requestURL, nil, &response)
	if err != nil {
		if strings.Contains(err.Error(), "status 404") {
			return Memory{}, false, nil
		}
		return Memory{}, false, err
	}
	memory, err := memoryFromMetadata(response.Properties)
	if err != nil {
		return Memory{}, false, err
	}
	if memory.RepositoryName != repositoryName || memory.Title != title {
		return Memory{}, false, fmt.Errorf("exact memory identity mismatch: returned %q/%q, expected %q/%q", memory.RepositoryName, memory.Title, repositoryName, title)
	}
	return memory, true, nil
}

func (b *WeaviateMemoryBackend) SearchMemories(ctx context.Context, repositoryName string, query string, category string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 20
	}

	where := fmt.Sprintf(`{path:["repositoryName"],operator:Equal,valueText:%q}`, repositoryName)
	if strings.TrimSpace(category) != "" {
		where = fmt.Sprintf(`{operator:And,operands:[%s,{path:["category"],operator:Equal,valueText:%q}]}`, where, category)
	}

	graphql := fmt.Sprintf(`{
Get {
  %s(
    bm25: {query: %q}
    where: %s
    limit: %d
  ) {
    repositoryName
    category
    title
    content
    tags
    createdAt
    sessionId
    origin
    authority
    status
    kind
    provenance
    sourceClass
    sourceIdentity
    contentIdentity
    revisionIdentity
    stableId
    revisionMetadata
    supersession
  }
}
}`, weaviateMemoryClass, query, where, limit)

	var response struct {
		Data struct {
			Get map[string][]map[string]any `json:"Get"`
		} `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	if err := b.doJSON(ctx, http.MethodPost, "/v1/graphql", map[string]any{"query": graphql}, &response); err != nil {
		return nil, err
	}
	if len(response.Errors) > 0 {
		return nil, fmt.Errorf("weaviate graphql errors: %v", response.Errors)
	}

	items := response.Data.Get[weaviateMemoryClass]
	memories := make([]Memory, 0, len(items))
	for _, item := range items {
		mem, err := memoryFromMetadata(item)
		if err != nil {
			return nil, err
		}
		memories = append(memories, mem)
	}
	return memories, nil
}

func (b *WeaviateMemoryBackend) ListMemories(ctx context.Context, repositoryName string, category string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 20
	}

	requestURL := fmt.Sprintf("/v1/objects?class=%s&limit=%d", url.QueryEscape(weaviateMemoryClass), limit)
	var response struct {
		Objects []struct {
			Properties map[string]any `json:"properties"`
		} `json:"objects"`
	}
	if err := b.doJSON(ctx, http.MethodGet, requestURL, nil, &response); err != nil {
		return nil, err
	}

	memories := make([]Memory, 0, len(response.Objects))
	for _, object := range response.Objects {
		memory, err := memoryFromMetadata(object.Properties)
		if err != nil {
			return nil, err
		}
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

func (b *WeaviateMemoryBackend) DeleteMemory(ctx context.Context, repositoryName string, title string) error {
	objectID := deterministicUUID(repositoryName, title)
	return b.doJSON(ctx, http.MethodDelete, "/v1/objects/"+weaviateMemoryClass+"/"+objectID, nil, nil)
}

func (b *WeaviateMemoryBackend) doJSON(ctx context.Context, method string, path string, payload any, response any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode weaviate request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, b.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create weaviate request: %w", err)
	}
	if b.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.client.Do(request)
	if err != nil {
		return fmt.Errorf("weaviate request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		content, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("weaviate request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(content)))
	}
	if response == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return fmt.Errorf("decode weaviate response: %w", err)
	}
	return nil
}

func deterministicUUID(repositoryName string, title string) string {
	sum := sha256.Sum256([]byte(repositoryName + "\x00" + title))
	hexValue := hex.EncodeToString(sum[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexValue[:8], hexValue[8:12], hexValue[12:16], hexValue[16:20], hexValue[20:32])
}
