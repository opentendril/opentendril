package conductor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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

	sourceRepository, err := taskContextCanonicalDirectory(input.SourceRepository, "source repository")
	if err != nil {
		return taskContextAssembly{}, err
	}
	workspaceRoot, err := taskContextWorkspaceRoot(input.ExecutionWorkspace)
	if err != nil {
		return taskContextAssembly{}, err
	}
	workspace, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return taskContextAssembly{}, fmt.Errorf("open task-context execution workspace: %w", err)
	}
	defer workspace.Close()

	manifest := taskContextSelectionManifest{
		TaskPromptIdentity:      taskContextContentIdentity([]byte(input.TaskPrompt)),
		ConfiguredSubstrateName: strings.TrimSpace(input.ConfiguredSubstrateName),
		SourceRepository:        sourceRepository,
		ExecutionWorkspace:      workspaceRoot,
		StartingRevision:        strings.TrimSpace(input.StartingRevision),
		Items:                   []taskContextManifestItem{},
	}
	assembly := taskContextAssembly{Manifest: manifest}

	candidates := make(map[string]taskContextCandidate)
	for _, path := range extractTaskContextFileAnchors(input.TaskPrompt) {
		candidates[path] = taskContextCandidate{kind: taskContextEvidenceAnchor, path: path, priority: 0}
	}

	if index != nil && strings.TrimSpace(repositoryName) != "" {
		terms := extractTaskContextLexicalTerms(input.TaskPrompt)
		symbolsByPath := make(map[string][]rhizome.Symbol)
		exactSymbolByPath := make(map[string]bool)
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
				if taskContextSymbolNameMatches(symbol.Name, term) {
					exactSymbolByPath[path] = true
				}
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
			priority := 2
			if exactSymbolByPath[path] {
				priority = 1
			}
			sort.Slice(symbols, func(i, j int) bool {
				if symbols[i].Name == symbols[j].Name {
					if symbols[i].LineStart != symbols[j].LineStart {
						return symbols[i].LineStart < symbols[j].LineStart
					}
					if symbols[i].LineEnd != symbols[j].LineEnd {
						return symbols[i].LineEnd < symbols[j].LineEnd
					}
					return symbols[i].Type < symbols[j].Type
				}
				return symbols[i].Name < symbols[j].Name
			})
			candidates[path] = taskContextCandidate{kind: taskContextEvidenceSymbol, path: path, priority: priority, symbols: uniqueTaskContextSymbols(symbols)}
		}
	}

	for _, path := range taskContextGitStatePaths(ctx, workspaceRoot) {
		if _, alreadySelected := candidates[path]; alreadySelected {
			continue
		}
		candidates[path] = taskContextCandidate{kind: taskContextEvidenceGit, path: path, priority: 3}
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
			source, err = readTaskContextSourceFile(workspace, candidate.path, itemBudget, candidate.symbols)
			if err != nil || !strings.EqualFold(indexed.Hash, source.hash) {
				// A stale symbol stub is never evidence. A path that disappeared or
				// escaped the workspace is treated the same way: omit the candidate.
				continue
			}
		} else {
			source, err = readTaskContextSourceFile(workspace, candidate.path, itemBudget, nil)
			if err != nil {
				continue
			}
		}

		rendered := renderTaskContextCandidate(candidate, source)
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
	return taskContextCanonicalDirectory(workspace, "execution workspace")
}

func taskContextCanonicalDirectory(path, label string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("task-context %s is empty", label)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve task-context %s: %w", label, err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve task-context %s symlinks: %w", label, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("stat task-context %s: %w", label, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("task-context %s is not a directory", label)
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

func readTaskContextSourceFile(workspaceRoot *os.Root, relativePath string, evidenceLimit int, symbols []rhizome.Symbol) (taskContextSourceFile, error) {
	cleaned, err := cleanTaskContextRepositoryPath(relativePath)
	if err != nil {
		return taskContextSourceFile{}, err
	}
	if workspaceRoot == nil {
		return taskContextSourceFile{}, fmt.Errorf("execution workspace root is unavailable")
	}
	info, err := workspaceRoot.Stat(filepath.FromSlash(cleaned))
	if err != nil || !info.Mode().IsRegular() {
		return taskContextSourceFile{}, fmt.Errorf("path is not a regular file")
	}
	file, err := workspaceRoot.Open(filepath.FromSlash(cleaned))
	if err != nil {
		return taskContextSourceFile{}, fmt.Errorf("open %s: %w", cleaned, err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return taskContextSourceFile{}, fmt.Errorf("path is not a regular file")
	}

	hash := sha256.New()
	evidence := newTaskContextEvidenceCollector(evidenceLimit, symbols)
	buffer := make([]byte, 32*1024)
	for {
		readBytes, readErr := file.Read(buffer)
		if readBytes > 0 {
			_, _ = hash.Write(buffer[:readBytes])
			evidence.Write(buffer[:readBytes])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return taskContextSourceFile{}, fmt.Errorf("read %s: %w", cleaned, readErr)
		}
	}
	return taskContextSourceFile{content: evidence.Bytes(), hash: hex.EncodeToString(hash.Sum(nil))}, nil
}

func taskContextContentIdentity(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

type taskContextEvidenceCollector struct {
	limit      int
	line       int
	lineStart  int
	lineEnd    int
	symbolOnly bool
	byLineOnly bool
	content    []byte
}

func newTaskContextEvidenceCollector(limit int, symbols []rhizome.Symbol) *taskContextEvidenceCollector {
	lineStart, lineEnd := taskContextSymbolLineRange(symbols)
	return &taskContextEvidenceCollector{
		limit:      limit,
		line:       1,
		lineStart:  lineStart,
		lineEnd:    lineEnd,
		symbolOnly: len(symbols) > 0,
		byLineOnly: lineStart > 0 && lineEnd >= lineStart,
	}
}

func (c *taskContextEvidenceCollector) Write(chunk []byte) {
	if c.limit <= len(c.content) || len(chunk) == 0 {
		return
	}
	if c.symbolOnly && !c.byLineOnly {
		return
	}
	if !c.byLineOnly {
		c.appendBounded(chunk)
		return
	}

	for len(chunk) > 0 && len(c.content) < c.limit {
		newline := bytes.IndexByte(chunk, '\n')
		if newline < 0 {
			if c.line >= c.lineStart && c.line <= c.lineEnd {
				c.appendBounded(chunk)
			}
			return
		}
		lineBytes := chunk[:newline+1]
		if c.line >= c.lineStart && c.line <= c.lineEnd {
			c.appendBounded(lineBytes)
		}
		c.line++
		chunk = chunk[newline+1:]
	}
}

func (c *taskContextEvidenceCollector) appendBounded(content []byte) {
	remaining := c.limit - len(c.content)
	if remaining <= 0 {
		return
	}
	if len(content) > remaining {
		content = content[:remaining]
	}
	c.content = append(c.content, content...)
}

func (c *taskContextEvidenceCollector) Bytes() []byte {
	return c.content
}

func taskContextSymbolLineRange(symbols []rhizome.Symbol) (int, int) {
	start, end := 0, 0
	for _, symbol := range symbols {
		if symbol.LineStart <= 0 || symbol.LineEnd < symbol.LineStart {
			continue
		}
		if start == 0 || symbol.LineStart < start {
			start = symbol.LineStart
		}
		if symbol.LineEnd > end {
			end = symbol.LineEnd
		}
	}
	return start, end
}

func extractTaskContextFileAnchors(prompt string) []string {
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

func taskContextSymbolNameMatches(name, term string) bool {
	name = strings.TrimSpace(name)
	term = strings.TrimSpace(term)
	return name != "" && term != "" && strings.EqualFold(name, term)
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
	}
	builder.Write(source.content)
	return strings.TrimSpace(builder.String())
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
