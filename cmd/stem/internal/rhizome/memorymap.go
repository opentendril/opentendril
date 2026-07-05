package rhizome

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func GenerateMemoryMap(ctx context.Context, backend MemoryBackend, repositoryName string, query string, limit int) (string, error) {
	if backend == nil {
		return "", fmt.Errorf("memory backend is required")
	}

	memories, err := backend.SearchMemories(ctx, repositoryName, query, "", limit)
	if err != nil {
		return "", err
	}
	if len(memories) == 0 {
		return "", nil
	}

	grouped := make(map[string][]Memory)
	categories := make([]string, 0)
	for _, memory := range memories {
		category := strings.TrimSpace(memory.Category)
		if category == "" {
			category = "Uncategorized"
		}
		if _, ok := grouped[category]; !ok {
			categories = append(categories, category)
		}
		grouped[category] = append(grouped[category], memory)
	}
	sort.Strings(categories)

	var builder strings.Builder
	builder.WriteString("# Memory Map\n\n")
	builder.WriteString("## Repository\n")
	builder.WriteString(repositoryName)
	builder.WriteString("\n")

	for _, category := range categories {
		builder.WriteString("\n## ")
		builder.WriteString(category)
		builder.WriteString("\n")
		for _, memory := range grouped[category] {
			fmt.Fprintf(&builder, "- **%s** (%s)\n", memory.Title, memory.CreatedAt.Format("2006-01-02"))
			builder.WriteString("  ")
			builder.WriteString(memory.Content)
			builder.WriteString("\n")
		}
	}

	return builder.String(), nil
}
