package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type toolCall struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type toolResponse struct {
	Status string `json:"status"`
	Output any    `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type toolArgument struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
}

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Arguments   []toolArgument `json:"arguments,omitempty"`
}

type listFilesEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

type listFilesOutput struct {
	Root      string           `json:"root"`
	Entries   []listFilesEntry `json:"entries"`
	Truncated bool             `json:"truncated,omitempty"`
}

type readFileOutput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type writeFileOutput struct {
	Path         string `json:"path"`
	BytesWritten int64  `json:"bytesWritten"`
	Mode         string `json:"mode"`
}

type diffOutput struct {
	Diff   string   `json:"diff"`
	Cached bool     `json:"cached"`
	Paths  []string `json:"paths,omitempty"`
}

type commitOutput struct {
	Committed bool     `json:"committed"`
	Hash      string   `json:"hash,omitempty"`
	Message   string   `json:"message"`
	Paths     []string `json:"paths,omitempty"`
}

type commandOutput struct {
	Command  string `json:"command"`
	Cwd      string `json:"cwd"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

type toolsOutput struct {
	Tools []toolDefinition `json:"tools"`
}

var skipDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	".venv":        {},
	"venv":         {},
	"dist":         {},
	"build":        {},
	"__pycache__":  {},
}

func main() {
	workspaceRoot, err := os.Getwd()
	if err != nil {
		workspaceRoot = "."
	}
	workspaceRoot, _ = filepath.Abs(workspaceRoot)

	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)

	for {
		line, readErr := reader.ReadBytes('\n')
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				writeResponse(encoder, toolResponse{Status: "error", Error: readErr.Error()})
			}
			continue
		}

		var call toolCall
		if err := json.Unmarshal([]byte(trimmed), &call); err != nil {
			writeResponse(encoder, toolResponse{Status: "error", Error: fmt.Sprintf("invalid tool call JSON: %v", err)})
		} else {
			writeResponse(encoder, executeTool(workspaceRoot, call))
		}

		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			writeResponse(encoder, toolResponse{Status: "error", Error: readErr.Error()})
			break
		}
	}
}

func writeResponse(encoder *json.Encoder, response toolResponse) {
	_ = encoder.Encode(response)
}

func executeTool(workspaceRoot string, call toolCall) toolResponse {
	toolName := strings.TrimSpace(call.Tool)
	if toolName == "" {
		return toolResponse{Status: "error", Error: "tool name is required"}
	}

	switch toolName {
	case "readFile":
		return readFileTool(workspaceRoot, call.Arguments)
	case "writeFile":
		return writeFileTool(workspaceRoot, call.Arguments)
	case "listFiles":
		return listFilesTool(workspaceRoot, call.Arguments)
	case "gitCommit":
		return gitCommitTool(workspaceRoot, call.Arguments)
	case "gitDiff":
		return gitDiffTool(workspaceRoot, call.Arguments)
	case "execCommand":
		return execCommandTool(workspaceRoot, call.Arguments)
	case "listAvailableTools":
		return toolResponse{Status: "success", Output: toolsOutput{Tools: availableTools()}}
	default:
		return toolResponse{Status: "error", Error: fmt.Sprintf("unsupported tool %q", toolName)}
	}
}

func availableTools() []toolDefinition {
	return []toolDefinition{
		{
			Name:        "readFile",
			Description: "Read a text file from the workspace.",
			Arguments: []toolArgument{
				{Name: "path", Type: "string", Description: "Path to the file, relative to the workspace root.", Required: true},
			},
		},
		{
			Name:        "writeFile",
			Description: "Write text content to a file, creating parent directories when needed.",
			Arguments: []toolArgument{
				{Name: "path", Type: "string", Description: "Path to the file, relative to the workspace root.", Required: true},
				{Name: "content", Type: "string", Description: "The full file contents to write.", Required: true},
				{Name: "append", Type: "boolean", Description: "Append instead of overwriting the file."},
			},
		},
		{
			Name:        "listFiles",
			Description: "List files and directories under a workspace path.",
			Arguments: []toolArgument{
				{Name: "path", Type: "string", Description: "Directory to list, relative to the workspace root."},
				{Name: "maxDepth", Type: "number", Description: "Maximum recursion depth to traverse."},
				{Name: "maxEntries", Type: "number", Description: "Maximum number of entries to return."},
			},
		},
		{
			Name:        "gitCommit",
			Description: "Stage files and create a git commit.",
			Arguments: []toolArgument{
				{Name: "message", Type: "string", Description: "Commit message.", Required: true},
				{Name: "paths", Type: "string[]", Description: "Optional list of paths to stage instead of all changes."},
			},
		},
		{
			Name:        "gitDiff",
			Description: "Show the current git diff.",
			Arguments: []toolArgument{
				{Name: "cached", Type: "boolean", Description: "Show the staged diff instead of the working tree diff."},
				{Name: "paths", Type: "string[]", Description: "Optional list of paths to limit the diff."},
			},
		},
		{
			Name:        "execCommand",
			Description: "Run a shell command inside the workspace.",
			Arguments: []toolArgument{
				{Name: "command", Type: "string", Description: "Shell command to execute.", Required: true},
				{Name: "cwd", Type: "string", Description: "Optional working directory, relative to the workspace root."},
				{Name: "timeoutSeconds", Type: "number", Description: "Optional timeout in seconds."},
			},
		},
		{
			Name:        "listAvailableTools",
			Description: "Return the executor's tool catalog.",
		},
	}
}

func readFileTool(workspaceRoot string, args map[string]any) toolResponse {
	rawPath, _ := stringArg(args, "path")
	if strings.TrimSpace(rawPath) == "" {
		return toolResponse{Status: "error", Error: "readFile requires a path"}
	}
	absPath, relPath, err := resolveWorkspacePath(workspaceRoot, rawPath)
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}

	return toolResponse{
		Status: "success",
		Output: readFileOutput{
			Path:    relPath,
			Content: string(content),
		},
	}
}

func writeFileTool(workspaceRoot string, args map[string]any) toolResponse {
	rawPath, _ := stringArg(args, "path")
	content, ok := stringArg(args, "content")
	if !ok {
		return toolResponse{Status: "error", Error: "writeFile requires a string content field"}
	}
	appendMode, _ := boolArg(args, "append")

	absPath, relPath, err := resolveWorkspacePath(workspaceRoot, rawPath)
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}

	mode := os.O_WRONLY | os.O_CREATE
	if appendMode {
		mode |= os.O_APPEND
	} else {
		mode |= os.O_TRUNC
	}

	file, err := os.OpenFile(absPath, mode, 0o644)
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}
	defer file.Close()

	written, err := file.WriteString(content)
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}

	return toolResponse{
		Status: "success",
		Output: writeFileOutput{
			Path:         relPath,
			BytesWritten: int64(written),
			Mode:         map[bool]string{true: "append", false: "overwrite"}[appendMode],
		},
	}
}

func listFilesTool(workspaceRoot string, args map[string]any) toolResponse {
	rawPath, _ := stringArg(args, "path")
	if strings.TrimSpace(rawPath) == "" {
		rawPath = "."
	}
	absPath, relPath, err := resolveWorkspacePath(workspaceRoot, rawPath)
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}

	maxDepth, _ := intArg(args, "maxDepth")
	if maxDepth <= 0 {
		maxDepth = 3
	}
	maxEntries, _ := intArg(args, "maxEntries")
	if maxEntries <= 0 {
		maxEntries = 500
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}

	entries := make([]listFilesEntry, 0, min(maxEntries, 128))
	truncated := false
	if info.IsDir() {
		truncated = walkDirectory(absPath, relPath, 0, maxDepth, maxEntries, &entries)
	} else {
		entries = append(entries, entryForPath(relPath, info))
	}

	return toolResponse{
		Status: "success",
		Output: listFilesOutput{
			Root:      relPath,
			Entries:   entries,
			Truncated: truncated,
		},
	}
}

func gitCommitTool(workspaceRoot string, args map[string]any) toolResponse {
	message, ok := stringArg(args, "message")
	if !ok || strings.TrimSpace(message) == "" {
		return toolResponse{Status: "error", Error: "gitCommit requires a non-empty message"}
	}
	paths, err := stringSliceArg(args, "paths")
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}

	if len(paths) == 0 {
		if _, err := runGit(workspaceRoot, "add", "-A"); err != nil {
			return toolResponse{Status: "error", Error: err.Error()}
		}
	} else {
		resolvedPaths := make([]string, 0, len(paths))
		for _, rawPath := range paths {
			_, relPath, err := resolveWorkspacePath(workspaceRoot, rawPath)
			if err != nil {
				return toolResponse{Status: "error", Error: err.Error()}
			}
			resolvedPaths = append(resolvedPaths, relPath)
		}
		addArgs := append([]string{"add", "--"}, resolvedPaths...)
		if _, err := runGit(workspaceRoot, addArgs...); err != nil {
			return toolResponse{Status: "error", Error: err.Error()}
		}
	}

	statusOut, err := runGit(workspaceRoot, "status", "--porcelain")
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}
	if strings.TrimSpace(statusOut) == "" {
		return toolResponse{
			Status: "success",
			Output: commitOutput{
				Committed: false,
				Message:   "nothing to commit",
				Paths:     paths,
			},
		}
	}

	if _, err := runGit(workspaceRoot,
		"-c", "user.name=OpenTendril",
		"-c", "user.email=opentendril@localhost",
		"commit", "-m", message,
	); err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}

	hashOut, err := runGit(workspaceRoot, "rev-parse", "HEAD")
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}

	return toolResponse{
		Status: "success",
		Output: commitOutput{
			Committed: true,
			Hash:      strings.TrimSpace(hashOut),
			Message:   message,
			Paths:     paths,
		},
	}
}

func gitDiffTool(workspaceRoot string, args map[string]any) toolResponse {
	cached, _ := boolArg(args, "cached")
	paths, err := stringSliceArg(args, "paths")
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}

	diffArgs := []string{"diff", "--no-color", "--binary"}
	if cached {
		diffArgs = append(diffArgs, "--cached")
	}
	diffArgs = append(diffArgs, "--")
	diffArgs = append(diffArgs, paths...)

	diffOut, err := runGit(workspaceRoot, diffArgs...)
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}

	return toolResponse{
		Status: "success",
		Output: diffOutput{
			Diff:   diffOut,
			Cached: cached,
			Paths:  paths,
		},
	}
}

func execCommandTool(workspaceRoot string, args map[string]any) toolResponse {
	command, ok := stringArg(args, "command")
	if !ok || strings.TrimSpace(command) == "" {
		return toolResponse{Status: "error", Error: "execCommand requires a non-empty command"}
	}
	cwdRaw, _ := stringArg(args, "cwd")
	if strings.TrimSpace(cwdRaw) == "" {
		cwdRaw = "."
	}
	cwdAbs, cwdRel, err := resolveWorkspacePath(workspaceRoot, cwdRaw)
	if err != nil {
		return toolResponse{Status: "error", Error: err.Error()}
	}
	timeoutSeconds, _ := intArg(args, "timeoutSeconds")
	if timeoutSeconds <= 0 {
		timeoutSeconds = 120
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	cmd.Dir = cwdAbs

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			exitCode = -1
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	response := commandOutput{
		Command:  command,
		Cwd:      cwdRel,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
	}

	if err != nil {
		return toolResponse{Status: "error", Output: response, Error: err.Error()}
	}

	return toolResponse{Status: "success", Output: response}
}

func runGit(workspaceRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", workspaceRoot}, args...)...)
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if err := cmd.Run(); err != nil {
		if stderr := strings.TrimSpace(stderrBuf.String()); stderr != "" {
			return "", fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, stderr)
		}
		return "", fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
	}

	return stdoutBuf.String(), nil
}

func resolveWorkspacePath(workspaceRoot string, rawPath string) (string, string, error) {
	if strings.TrimSpace(rawPath) == "" {
		rawPath = "."
	}

	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", "", err
	}

	var absPath string
	if filepath.IsAbs(rawPath) {
		absPath = filepath.Clean(rawPath)
	} else {
		absPath = filepath.Clean(filepath.Join(absRoot, rawPath))
	}

	relPath, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", "", err
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes the workspace root", rawPath)
	}

	return absPath, filepath.ToSlash(relPath), nil
}

func walkDirectory(rootAbs string, rootRel string, depth int, maxDepth int, maxEntries int, entries *[]listFilesEntry) bool {
	childEntries, err := os.ReadDir(rootAbs)
	if err != nil {
		return false
	}

	sort.Slice(childEntries, func(i, j int) bool {
		return strings.ToLower(childEntries[i].Name()) < strings.ToLower(childEntries[j].Name())
	})

	for _, entry := range childEntries {
		if len(*entries) >= maxEntries {
			return true
		}
		if _, skip := skipDirs[entry.Name()]; skip {
			continue
		}

		childAbs := filepath.Join(rootAbs, entry.Name())
		childRel := filepath.ToSlash(filepath.Join(rootRel, entry.Name()))

		info, err := entry.Info()
		if err != nil {
			continue
		}

		*entries = append(*entries, entryForPath(childRel, info))
		if len(*entries) >= maxEntries {
			return true
		}

		if entry.IsDir() && depth+1 < maxDepth {
			if walkDirectory(childAbs, childRel, depth+1, maxDepth, maxEntries, entries) {
				return true
			}
		}
	}

	return false
}

func entryForPath(relPath string, info os.FileInfo) listFilesEntry {
	entryType := "file"
	if info.IsDir() {
		entryType = "dir"
	} else if info.Mode()&os.ModeSymlink != 0 {
		entryType = "symlink"
	}

	return listFilesEntry{
		Path: relPath,
		Type: entryType,
		Size: info.Size(),
	}
}

func stringArg(args map[string]any, key string) (string, bool) {
	value, ok := args[key]
	if !ok || value == nil {
		return "", false
	}
	if str, ok := value.(string); ok {
		return str, true
	}
	return fmt.Sprint(value), true
}

func boolArg(args map[string]any, key string) (bool, bool) {
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

func intArg(args map[string]any, key string) (int, bool) {
	value, ok := args[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func stringSliceArg(args map[string]any, key string) ([]string, error) {
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
