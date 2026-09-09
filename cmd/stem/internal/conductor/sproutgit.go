package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// managedGitDiffResponse implements the Stem-mediated bounded gitDiff capability.
func (a *Sprout) managedGitDiffResponse(ctx context.Context, call ToolCall) ToolResponse {
	if !checkoutHasGitMetadata(a.workspace) {
		return ToolResponse{
			Status: "error",
			Error:  "gitDiff refused: workspace is not a valid git repository checkout",
		}
	}

	for k := range call.Arguments {
		if k != "cached" && k != "paths" {
			return ToolResponse{
				Status: "error",
				Error:  fmt.Sprintf("gitDiff refused: unsupported argument %q", k),
			}
		}
	}

	cached, _ := coerceBoolArg(call.Arguments, "cached")
	paths, err := coerceStringSliceArg(call.Arguments, "paths")
	if err != nil {
		return ToolResponse{
			Status: "error",
			Error:  fmt.Sprintf("gitDiff refused: %v", err),
		}
	}

	var validatedPaths []string
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(p))
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") {
			return ToolResponse{
				Status: "error",
				Error:  fmt.Sprintf("gitDiff refused: absolute path %q is not allowed", p),
			}
		}
		if clean == ".." || strings.HasPrefix(clean, "../") {
			return ToolResponse{
				Status: "error",
				Error:  fmt.Sprintf("gitDiff refused: path %q traverses outside the workspace", p),
			}
		}

		normalized := filepath.ToSlash(strings.TrimSpace(p))
		for strings.Contains(normalized, "//") {
			normalized = strings.ReplaceAll(normalized, "//", "/")
		}
		if normalized == "~" || strings.HasPrefix(normalized, "~/") {
			return ToolResponse{
				Status: "error",
				Error:  fmt.Sprintf("gitDiff refused: path %q is not a repository-relative workspace path", p),
			}
		}
		if strings.Contains(normalized, "/.tendril/run-workspaces/") ||
			strings.HasPrefix(normalized, ".tendril/run-workspaces/") ||
			normalized == ".tendril/run-workspaces" {
			return ToolResponse{
				Status: "error",
				Error:  fmt.Sprintf("gitDiff refused: path %q is not a repository-relative workspace path", p),
			}
		}

		validatedPaths = append(validatedPaths, clean)
	}

	args := []string{"diff", "--no-color", "--binary"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	if len(validatedPaths) > 0 {
		args = append(args, validatedPaths...)
	}

	diffOut, err := runGitCommandRawOutput(ctx, a.workspace, args...)
	if err != nil {
		// Do not expose host workspace absolute paths, linked-worktree Git metadata paths, or source-repository .git paths.
		return ToolResponse{
			Status: "error",
			Error:  "git diff execution failed",
		}
	}

	return ToolResponse{
		Status: "success",
		Output: map[string]any{
			"diff":   diffOut,
			"cached": cached,
			"paths":  paths,
		},
	}
}

func coerceBoolArg(args map[string]any, key string) (bool, bool) {
	value, ok := args[key]
	if !ok || value == nil {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(typed)
		if err != nil {
			return false, false
		}
		return parsed, true
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	default:
		return false, false
	}
}

func coerceStringSliceArg(args map[string]any, key string) ([]string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case []string:
		return typed, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, fmt.Sprint(item))
		}
		return out, nil
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, nil
		}
		return []string{typed}, nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var out []string
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("expected string array for %q", key)
		}
		return out, nil
	}
}
