package conductor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/opentendril/opentendril/cmd/stem/internal/rhizome"
)

const (
	taskContextMaxBytesEnv     = "TENDRIL_TASK_CONTEXT_MAX_BYTES"
	taskContextItemMaxBytesEnv = "TENDRIL_TASK_CONTEXT_ITEM_MAX_BYTES"
	taskContextMaxItemsEnv     = "TENDRIL_TASK_CONTEXT_MAX_ITEMS"

	taskContextDefaultMaxBytes     = 4096
	taskContextDefaultItemMaxBytes = 1024
	taskContextDefaultMaxItems     = 8
	taskContextHardMaxBytes        = 8192
	taskContextHardMaxItems        = 32

	taskContextEvidenceAnchor = "file-anchor"
	taskContextEvidenceSymbol = "rhizome-symbol"
	taskContextEvidenceGit    = "git-state-path"
)

var taskContextLexicalTermPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]{0,63}`)

type taskContextSettings struct {
	maxBytes     int
	itemMaxBytes int
	maxItems     int
}

type taskContextAssemblyInput struct {
	TaskPrompt              string
	ConfiguredSubstrateName string
	SourceRepository        string
	ExecutionWorkspace      string
	StartingRevision        string
}

type taskContextManifestItem struct {
	Kind            string
	Path            string
	Symbols         []string
	ContentIdentity string
	Bytes           int
}

// taskContextSelectionManifest is deliberately in-memory in Slice 1. Its
// shape is the provenance preparation that a later slice can publish without
// putting the selected Substrate evidence into the observation transcript.
type taskContextSelectionManifest struct {
	TaskPromptIdentity      string
	ConfiguredSubstrateName string
	SourceRepository        string
	ExecutionWorkspace      string
	StartingRevision        string
	Items                   []taskContextManifestItem
}

type taskContextAssembly struct {
	Rendered string
	Manifest taskContextSelectionManifest
}

type taskContextContextKey struct{}

func withTaskContext(ctx context.Context, rendered string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, taskContextContextKey{}, strings.TrimSpace(rendered))
}

func taskContextFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if rendered, ok := ctx.Value(taskContextContextKey{}).(string); ok {
		return strings.TrimSpace(rendered)
	}
	return ""
}

type taskContextRhizomeIndex interface {
	GetFile(ctx context.Context, repositoryName string, path string) (rhizome.FileRecord, bool, error)
	SearchSymbols(ctx context.Context, repositoryName string, query string, limit int) ([]rhizome.Symbol, error)
}

type taskContextSourceFile struct {
	content []byte
	hash    string
}

type taskContextCandidate struct {
	kind     string
	path     string
	priority int
	symbols  []rhizome.Symbol
}

func taskContextSettingsFromEnvironment() (taskContextSettings, error) {
	settings := taskContextSettings{
		maxBytes:     taskContextDefaultMaxBytes,
		itemMaxBytes: taskContextDefaultItemMaxBytes,
		maxItems:     taskContextDefaultMaxItems,
	}

	var err error
	if settings.maxBytes, err = taskContextSettingInt(taskContextMaxBytesEnv, settings.maxBytes); err != nil {
		return taskContextSettings{}, err
	}
	if settings.itemMaxBytes, err = taskContextSettingInt(taskContextItemMaxBytesEnv, settings.itemMaxBytes); err != nil {
		return taskContextSettings{}, err
	}
	if settings.maxItems, err = taskContextSettingInt(taskContextMaxItemsEnv, settings.maxItems); err != nil {
		return taskContextSettings{}, err
	}

	if settings.maxBytes > taskContextHardMaxBytes {
		return taskContextSettings{}, fmt.Errorf("%s exceeds hard ceiling of %d bytes", taskContextMaxBytesEnv, taskContextHardMaxBytes)
	}
	if settings.maxItems > taskContextHardMaxItems {
		return taskContextSettings{}, fmt.Errorf("%s exceeds hard ceiling of %d items", taskContextMaxItemsEnv, taskContextHardMaxItems)
	}
	if settings.itemMaxBytes > settings.maxBytes {
		return taskContextSettings{}, fmt.Errorf("%s cannot exceed %s", taskContextItemMaxBytesEnv, taskContextMaxBytesEnv)
	}
	return settings, nil
}

func taskContextSettingInt(name string, defaultValue int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, value)
	}
	return parsed, nil
}

func assembleTaskContext(ctx context.Context, input taskContextAssemblyInput, index taskContextRhizomeIndex, repositoryName string) (taskContextAssembly, error) {
	settings, err := taskContextSettingsFromEnvironment()
	if err != nil {
		return taskContextAssembly{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	manifest := taskContextSelectionManifest{
		TaskPromptIdentity:      taskContextContentIdentity([]byte(input.TaskPrompt)),
		ConfiguredSubstrateName: strings.TrimSpace(input.ConfiguredSubstrateName),
		SourceRepository:        strings.TrimSpace(input.SourceRepository),
		ExecutionWorkspace:      strings.TrimSpace(input.ExecutionWorkspace),
		StartingRevision:        strings.TrimSpace(input.StartingRevision),
		Items:                   []taskContextManifestItem{},
	}
	assembly := taskContextAssembly{Manifest: manifest}

	workspaceRoot, err := taskContextWorkspaceRoot(input.ExecutionWorkspace)
	if err != nil {
		return assembly, err
	}

	candidates := make(map[string]taskContextCandidate)
	for _, path := range extractTaskContextFileAnchors(input.TaskPrompt, workspaceRoot) {
		candidates[path] = taskContextCandidate{kind: taskContextEvidenceAnchor, path: path, priority: 0}
	}

	if index != nil && strings.TrimSpace(repositoryName) != "" {
		terms := extractTaskContextLexicalTerms(input.TaskPrompt)
		symbolsByPath := make(map[string][]rhizome.Symbol)
		for _, term := range terms {
			query := safeTaskContextFTSQuery(term)
			if query == "" {
				continue
			}
			results, searchErr := index.SearchSymbols(ctx, repositoryName, query, 64)
			if searchErr != nil {
				return assembly, fmt.Errorf("search Rhizome symbols: %w", searchErr)
			}
			for _, symbol := range results {
				path, pathErr := cleanTaskContextRepositoryPath(symbol.FilePath)
				if pathErr != nil {
					continue
				}
				symbolsByPath[path] = append(symbolsByPath[path], symbol)
			}
		}

		paths := make([]string, 0, len(symbolsByPath))
		for path := range symbolsByPath {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if _, alreadyAnchored := candidates[path]; alreadyAnchored {
				continue
			}
			symbols := symbolsByPath[path]
			sort.Slice(symbols, func(i, j int) bool {
				if symbols[i].Name == symbols[j].Name {
					return symbols[i].LineStart < symbols[j].LineStart
				}
				return symbols[i].Name < symbols[j].Name
			})
			candidates[path] = taskContextCandidate{kind: taskContextEvidenceSymbol, path: path, priority: 1, symbols: uniqueTaskContextSymbols(symbols)}
		}
	}

	for _, path := range taskContextGitStatePaths(ctx, input.ExecutionWorkspace) {
		if _, alreadySelected := candidates[path]; alreadySelected {
			continue
		}
		candidates[path] = taskContextCandidate{kind: taskContextEvidenceGit, path: path, priority: 2}
	}

	ordered := make([]taskContextCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, candidate)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].priority != ordered[j].priority {
			return ordered[i].priority < ordered[j].priority
		}
		if ordered[i].path != ordered[j].path {
			return ordered[i].path < ordered[j].path
		}
		return ordered[i].kind < ordered[j].kind
	})

	usedBytes := 0
	for _, candidate := range ordered {
		if len(manifest.Items) >= settings.maxItems {
			break
		}

		var source taskContextSourceFile
		if candidate.kind == taskContextEvidenceSymbol {
			if index == nil || strings.TrimSpace(repositoryName) == "" {
				continue
			}
			indexed, found, getErr := index.GetFile(ctx, repositoryName, candidate.path)
			if getErr != nil {
				return assembly, fmt.Errorf("retrieve Rhizome file record for %s: %w", candidate.path, getErr)
			}
			if !found {
				continue
			}
			source, err = readTaskContextSourceFile(workspaceRoot, candidate.path)
			if err != nil || !strings.EqualFold(indexed.Hash, source.hash) {
				// A stale symbol stub is never evidence. A path that disappeared or
				// escaped the workspace is treated the same way: omit the candidate.
				continue
			}
		} else {
			source, err = readTaskContextSourceFile(workspaceRoot, candidate.path)
			if err != nil {
				continue
			}
		}

		rendered := renderTaskContextCandidate(candidate, source)
		remaining := settings.maxBytes - usedBytes
		if usedBytes > 0 {
			remaining -= 2
		}
		if remaining <= 0 {
			break
		}
		itemBudget := settings.itemMaxBytes
		if itemBudget > remaining {
			itemBudget = remaining
		}
		rendered = truncateTaskContextEvidence(rendered, itemBudget)
		if rendered == "" {
			continue
		}
		if usedBytes > 0 {
			assembly.Rendered += "\n\n"
			usedBytes += 2
		}
		assembly.Rendered += rendered
		usedBytes += len(rendered)
		manifestItem := taskContextManifestItem{
			Kind:            candidate.kind,
			Path:            candidate.path,
			ContentIdentity: source.hash,
			Bytes:           len(rendered),
		}
		for _, symbol := range candidate.symbols {
			manifestItem.Symbols = append(manifestItem.Symbols, symbol.Name)
		}
		manifest.Items = append(manifest.Items, manifestItem)
	}

	assembly.Manifest = manifest
	return assembly, nil
}

func taskContextWorkspaceRoot(workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", fmt.Errorf("task-context execution workspace is empty")
	}
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve task-context execution workspace: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve task-context execution workspace symlinks: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("stat task-context execution workspace: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("task-context execution workspace is not a directory")
	}
	return canonical, nil
}

func cleanTaskContextRepositoryPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") || strings.Contains(path, ":") {
		return "", fmt.Errorf("path is not repository-relative")
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path escapes repository root")
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if strings.TrimSpace(segment) == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("path is not clean")
		}
	}
	if taskContextForbiddenPath(cleaned) {
		return "", fmt.Errorf("path is excluded")
	}
	return cleaned, nil
}

func taskContextForbiddenPath(path string) bool {
	segments := strings.Split(filepath.ToSlash(path), "/")
	for _, segment := range segments {
		if strings.EqualFold(segment, ".git") || strings.EqualFold(segment, ".tendril") {
			return true
		}
	}
	for _, segment := range segments {
		base := strings.ToLower(strings.TrimSpace(segment))
		switch base {
		case ".env", ".npmrc", ".pypirc", "credentials", "credentials.json", "secret", "secrets", "passwords", "id_rsa", "id_ed25519", "id_ecdsa":
			return true
		}
		if strings.HasPrefix(base, ".env.") || strings.Contains(base, "private-key") || strings.Contains(base, "private_key") {
			return true
		}
		switch strings.ToLower(filepath.Ext(base)) {
		case ".pem", ".key", ".p12", ".pfx":
			return true
		}
	}
	return false
}

func taskContextPathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." {
		return false
	}
	return relative != "" && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readTaskContextSourceFile(workspaceRoot, relativePath string) (taskContextSourceFile, error) {
	cleaned, err := cleanTaskContextRepositoryPath(relativePath)
	if err != nil {
		return taskContextSourceFile{}, err
	}
	candidate := filepath.Join(workspaceRoot, filepath.FromSlash(cleaned))
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil || !taskContextPathWithin(workspaceRoot, canonical) {
		return taskContextSourceFile{}, fmt.Errorf("path does not resolve inside execution workspace")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() {
		return taskContextSourceFile{}, fmt.Errorf("path is not a regular file")
	}
	content, err := os.ReadFile(canonical)
	if err != nil {
		return taskContextSourceFile{}, fmt.Errorf("read %s: %w", cleaned, err)
	}
	return taskContextSourceFile{content: content, hash: taskContextContentIdentity(content)}, nil
}

func taskContextContentIdentity(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func extractTaskContextFileAnchors(prompt, workspaceRoot string) []string {
	seen := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(prompt, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("`'\"()[]{}<>,;:!?=", r)
	}) {
		token = strings.TrimRight(token, ".")
		if token == "" {
			continue
		}
		path, err := cleanTaskContextRepositoryPath(token)
		if err == nil {
			if !strings.Contains(path, "/") && !strings.Contains(filepath.Base(path), ".") {
				info, statErr := os.Stat(filepath.Join(workspaceRoot, filepath.FromSlash(path)))
				if statErr != nil || !info.Mode().IsRegular() {
					continue
				}
			}
			seen[path] = struct{}{}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func extractTaskContextLexicalTerms(prompt string) []string {
	seen := make(map[string]string)
	for _, term := range taskContextLexicalTermPattern.FindAllString(prompt, -1) {
		upper := strings.ToUpper(term)
		if upper == "AND" || upper == "OR" || upper == "NOT" || upper == "NEAR" {
			continue
		}
		key := strings.ToLower(term)
		seen[key] = term
	}
	terms := make([]string, 0, len(seen))
	for _, term := range seen {
		terms = append(terms, term)
	}
	sort.Slice(terms, func(i, j int) bool {
		left, right := strings.ToLower(terms[i]), strings.ToLower(terms[j])
		if left == right {
			return terms[i] < terms[j]
		}
		return left < right
	})
	if len(terms) > 16 {
		terms = terms[:16]
	}
	return terms
}

func safeTaskContextFTSQuery(term string) string {
	term = strings.TrimSpace(term)
	if term == "" {
		return ""
	}
	for _, character := range term {
		if !(unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_') {
			return ""
		}
	}
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}

func uniqueTaskContextSymbols(symbols []rhizome.Symbol) []rhizome.Symbol {
	seen := make(map[string]struct{}, len(symbols))
	unique := make([]rhizome.Symbol, 0, len(symbols))
	for _, symbol := range symbols {
		key := fmt.Sprintf("%s:%d:%d", symbol.Name, symbol.LineStart, symbol.LineEnd)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, symbol)
	}
	return unique
}

func taskContextGitStatePaths(ctx context.Context, workspace string) []string {
	if strings.TrimSpace(workspace) == "" {
		return nil
	}
	output, _, err := runGitCommandBoundedRawOutput(ctx, workspace, 64*1024, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil
	}
	parts := strings.Split(output, "\x00")
	seen := make(map[string]struct{})
	for index := 0; index < len(parts); index++ {
		part := parts[index]
		if len(part) < 4 {
			continue
		}
		path := part[3:]
		if part[0] == 'R' || part[0] == 'C' {
			if index+1 < len(parts) && parts[index+1] != "" {
				index++
			}
		}
		if cleaned, cleanErr := cleanTaskContextRepositoryPath(path); cleanErr == nil {
			seen[cleaned] = struct{}{}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func renderTaskContextCandidate(candidate taskContextCandidate, source taskContextSourceFile) string {
	var builder strings.Builder
	switch candidate.kind {
	case taskContextEvidenceAnchor:
		builder.WriteString("Explicit Substrate file anchor: ")
	case taskContextEvidenceSymbol:
		builder.WriteString("Rhizome symbol evidence: ")
	case taskContextEvidenceGit:
		builder.WriteString("Current Git-state path: ")
	}
	builder.WriteString("`")
	builder.WriteString(candidate.path)
	builder.WriteString("`\n")
	if len(candidate.symbols) > 0 {
		builder.WriteString("Symbols: ")
		names := make([]string, 0, len(candidate.symbols))
		for _, symbol := range candidate.symbols {
			names = append(names, symbol.Name)
		}
		builder.WriteString(strings.Join(names, ", "))
		builder.WriteString("\n")
		builder.WriteString(taskContextSymbolSource(source.content, candidate.symbols))
	} else {
		builder.Write(source.content)
	}
	return strings.TrimSpace(builder.String())
}

func taskContextSymbolSource(content []byte, symbols []rhizome.Symbol) string {
	if len(symbols) == 0 {
		return string(content)
	}
	start, end := 0, 0
	for _, symbol := range symbols {
		if start == 0 || (symbol.LineStart > 0 && symbol.LineStart < start) {
			start = symbol.LineStart
		}
		if symbol.LineEnd > end {
			end = symbol.LineEnd
		}
	}
	if start <= 0 || end < start {
		return string(content)
	}
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	if start > len(lines) {
		return ""
	}
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start-1:end], "\n")
}

func truncateTaskContextEvidence(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(content) <= limit {
		return content
	}
	marker := "\n[truncated]"
	if limit <= len(marker) {
		return marker[:limit]
	}
	cutLimit := limit - len(marker)
	cut := content[:cutLimit]
	if index := strings.LastIndexByte(cut, '\n'); index > 0 {
		cut = cut[:index]
	}
	return cut + marker
}
