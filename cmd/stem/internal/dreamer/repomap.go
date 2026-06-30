package dreamer

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func GenerateRepoMap(ctx context.Context, store IndexStore, repositoryName string, query string, limit int) (string, error) {
	if store == nil {
		return "", fmt.Errorf("index store is required")
	}
	if strings.TrimSpace(query) == "" {
		query = "*"
	}

	symbols, err := store.SearchSymbols(ctx, repositoryName, query, limit)
	if err != nil {
		return "", err
	}

	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].FilePath == symbols[j].FilePath {
			if symbols[i].LineStart == symbols[j].LineStart {
				return symbols[i].Name < symbols[j].Name
			}
			return symbols[i].LineStart < symbols[j].LineStart
		}
		return symbols[i].FilePath < symbols[j].FilePath
	})

	var builder strings.Builder
	builder.WriteString("# Repo Map\n\n")
	builder.WriteString("## Repository\n")
	builder.WriteString(repositoryName)
	builder.WriteString("\n\n## Symbols\n")
	if len(symbols) == 0 {
		builder.WriteString("- none\n")
		return builder.String(), nil
	}

	currentPath := ""
	for _, symbol := range symbols {
		if symbol.FilePath != currentPath {
			currentPath = symbol.FilePath
			builder.WriteString("\n### `")
			builder.WriteString(currentPath)
			builder.WriteString("`\n")
		}
		fmt.Fprintf(&builder, "- `%s` %s lines %d-%d\n", symbol.Name, symbol.Type, symbol.LineStart, symbol.LineEnd)
		builder.WriteString("  ```\n")
		builder.WriteString(symbol.StubContent)
		builder.WriteString("\n  ```\n")
	}

	return builder.String(), nil
}
