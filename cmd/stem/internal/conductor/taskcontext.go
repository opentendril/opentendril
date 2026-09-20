package conductor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
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
	taskContextEvidenceTest   = "associated-test"
	taskContextEvidenceDoc    = "associated-documentation"
	taskContextEvidenceMemory = "project-memory"
)

const (
	taskContextSelectionReasonExplicitFile            = "explicit-file-anchor"
	taskContextSelectionReasonExactSymbol             = "exact-symbol-anchor"
	taskContextSelectionReasonLexicalSymbol           = "lexical-symbol-anchor"
	taskContextSelectionReasonGitState                = "git-state-path"
	taskContextSelectionReasonAssociatedTest          = "associated-test"
	taskContextSelectionReasonAssociatedDocumentation = "associated-documentation"
	taskContextSelectionReasonLocalMemory             = "source-local-memory"
)

const (
	taskContextOmissionBudgetBytes       = "budget-bytes"
	taskContextOmissionBudgetItems       = "budget-items"
	taskContextOmissionStaleEvidence     = "stale-evidence"
	taskContextOmissionPathSecurity      = "path-security"
	taskContextOmissionUnreadable        = "unreadable"
	taskContextOmissionNotFound          = "not-found"
	taskContextOmissionMemoryMissing     = "memory-missing"
	taskContextOmissionMemoryUnavailable = "memory-unavailable"
	taskContextOmissionMemoryUnbound     = "memory-unbound"
)

var errTaskContextPathSecurity = errors.New("task-context path security rejection")

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
	MemoryIndex             taskContextMemoryIndex
	MemoryRepositoryName    string
	MemoryOmissionReason    string
}

type taskContextManifestItem struct {
	Kind             string
	SourceClass      string
	Path             string
	SourceIdentity   string
	SelectionReason  string
	Symbols          []string
	ContentIdentity  string
	ContentReference string
	Bytes            int
	AdmittedBytes    int
	Truncated        bool
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
	EffectiveMaxBytes       int
	EffectiveItemMaxBytes   int
	EffectiveMaxItems       int
	CandidateCount          int
	AdmittedCount           int
	AdmittedBytes           int
	OmissionCounts          map[string]int
	Items                   []taskContextManifestItem
}

type taskContextAssembly struct {
	Rendered string
	Manifest taskContextSelectionManifest
}

type taskContextRhizomeIndex interface {
	GetFile(ctx context.Context, repositoryName string, path string) (rhizome.FileRecord, bool, error)
	SearchSymbols(ctx context.Context, repositoryName string, query string, limit int) ([]rhizome.Symbol, error)
}

type taskContextMemoryIndex interface {
	SearchMemories(ctx context.Context, repositoryName string, query string, category string, limit int) ([]rhizome.Memory, error)
}

type taskContextSourceFile struct {
	content   []byte
	hash      string
	truncated bool
}

type taskContextCandidate struct {
	kind            string
	path            string
	dedupeKey       string
	priority        int
	selectionReason string
	symbols         []rhizome.Symbol
	memory          *rhizome.Memory
}

type taskContextCandidateQueue struct {
	pending []taskContextCandidate
}

func newTaskContextCandidateQueue(candidates map[string]taskContextCandidate) taskContextCandidateQueue {
	pending := make([]taskContextCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		pending = append(pending, candidate)
	}
	sort.Slice(pending, func(i, j int) bool {
		return taskContextCandidateLess(pending[i], pending[j])
	})
	return taskContextCandidateQueue{pending: pending}
}

func (queue *taskContextCandidateQueue) push(candidate taskContextCandidate) {
	insertAt := sort.Search(len(queue.pending), func(index int) bool {
		return taskContextCandidateLess(candidate, queue.pending[index])
	})
	queue.pending = append(queue.pending, taskContextCandidate{})
	copy(queue.pending[insertAt+1:], queue.pending[insertAt:])
	queue.pending[insertAt] = candidate
}

func (queue *taskContextCandidateQueue) pop() (taskContextCandidate, bool) {
	if len(queue.pending) == 0 {
		return taskContextCandidate{}, false
	}
	candidate := queue.pending[0]
	queue.pending = queue.pending[1:]
	return candidate, true
}

func taskContextCandidateLess(left, right taskContextCandidate) bool {
	if left.priority != right.priority {
		return left.priority < right.priority
	}
	if left.path != right.path {
		return left.path < right.path
	}
	return left.kind < right.kind
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
		EffectiveMaxBytes:       settings.maxBytes,
		EffectiveItemMaxBytes:   settings.itemMaxBytes,
		EffectiveMaxItems:       settings.maxItems,
		OmissionCounts:          make(map[string]int),
		Items:                   []taskContextManifestItem{},
	}
	assembly := taskContextAssembly{Manifest: manifest}

	candidates := make(map[string]taskContextCandidate)
	taskContextAddOmission(&manifest, taskContextOmissionPathSecurity, taskContextInvalidFileAnchorCount(input.TaskPrompt))
	for _, path := range extractTaskContextFileAnchors(input.TaskPrompt) {
		candidates[path] = taskContextCandidate{
			kind:            taskContextEvidenceAnchor,
			path:            path,
			dedupeKey:       path,
			priority:        0,
			selectionReason: taskContextSelectionReasonExplicitFile,
		}
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
			reason := taskContextSelectionReasonLexicalSymbol
			if priority == 1 {
				reason = taskContextSelectionReasonExactSymbol
			}
			candidates[path] = taskContextCandidate{
				kind:            taskContextEvidenceSymbol,
				path:            path,
				dedupeKey:       path,
				priority:        priority,
				selectionReason: reason,
				symbols:         uniqueTaskContextSymbols(symbols),
			}
		}
	}

	for _, path := range taskContextGitStatePaths(ctx, workspaceRoot) {
		if _, alreadySelected := candidates[path]; alreadySelected {
			continue
		}
		candidates[path] = taskContextCandidate{
			kind:            taskContextEvidenceGit,
			path:            path,
			dedupeKey:       path,
			priority:        3,
			selectionReason: taskContextSelectionReasonGitState,
		}
	}

	if input.MemoryOmissionReason != "" {
		taskContextAddOmission(&manifest, input.MemoryOmissionReason, 1)
	}
	if input.MemoryIndex != nil && strings.TrimSpace(input.MemoryRepositoryName) != "" {
		memoryCandidates, unbound, memoryErr := taskContextMemoryCandidates(ctx, input.MemoryIndex, input.MemoryRepositoryName, input.TaskPrompt)
		if memoryErr != nil {
			taskContextAddOmission(&manifest, taskContextOmissionMemoryUnavailable, 1)
		} else {
			if unbound > 0 {
				taskContextAddOmission(&manifest, taskContextOmissionMemoryUnbound, unbound)
			}
			for _, candidate := range memoryCandidates {
				candidates[candidate.dedupeKey] = candidate
			}
		}
	}

	queue := newTaskContextCandidateQueue(candidates)
	manifest.CandidateCount = len(candidates)
	usedBytes := 0
	for {
		candidate, ok := queue.pop()
		if !ok {
			break
		}
		if len(manifest.Items) >= settings.maxItems {
			taskContextAddOmission(&manifest, taskContextOmissionBudgetItems, len(queue.pending)+1)
			break
		}

		remaining := settings.maxBytes - usedBytes
		if usedBytes > 0 {
			remaining -= 2
		}
		if remaining <= 0 {
			taskContextAddOmission(&manifest, taskContextOmissionBudgetBytes, len(queue.pending)+1)
			break
		}
		itemBudget := settings.itemMaxBytes
		if itemBudget > remaining {
			itemBudget = remaining
		}

		var source taskContextSourceFile
		if candidate.memory != nil {
			source = taskContextSourceFile{
				content: []byte(candidate.memory.Content),
				hash:    taskContextContentIdentity([]byte(candidate.memory.Content)),
			}
		} else if candidate.kind == taskContextEvidenceSymbol {
			if index == nil || strings.TrimSpace(repositoryName) == "" {
				taskContextAddOmission(&manifest, taskContextOmissionStaleEvidence, 1)
				continue
			}
			indexed, found, getErr := index.GetFile(ctx, repositoryName, candidate.path)
			if getErr != nil {
				return assembly, fmt.Errorf("retrieve Rhizome file record for %s: %w", candidate.path, getErr)
			}
			if !found {
				taskContextAddOmission(&manifest, taskContextOmissionStaleEvidence, 1)
				continue
			}
			source, err = readTaskContextSourceFile(workspace, candidate.path, itemBudget, candidate.symbols)
			if err != nil {
				if errors.Is(err, errTaskContextPathSecurity) {
					taskContextAddOmission(&manifest, taskContextOmissionPathSecurity, 1)
				} else {
					taskContextAddOmission(&manifest, taskContextOmissionUnreadable, 1)
				}
				continue
			}
			if !strings.EqualFold(indexed.Hash, source.hash) {
				// A stale symbol stub is never evidence. A path that disappeared or
				// escaped the workspace is treated the same way: omit the candidate.
				taskContextAddOmission(&manifest, taskContextOmissionStaleEvidence, 1)
				continue
			}
		} else {
			source, err = readTaskContextSourceFile(workspace, candidate.path, itemBudget, nil)
			if err != nil {
				if errors.Is(err, errTaskContextPathSecurity) {
					taskContextAddOmission(&manifest, taskContextOmissionPathSecurity, 1)
				} else if errors.Is(err, os.ErrNotExist) {
					taskContextAddOmission(&manifest, taskContextOmissionNotFound, 1)
				} else {
					taskContextAddOmission(&manifest, taskContextOmissionUnreadable, 1)
				}
				continue
			}
		}

		rendered := renderTaskContextCandidate(candidate, source)
		var renderedTruncated bool
		rendered, renderedTruncated = truncateTaskContextEvidenceWithStatus(rendered, itemBudget)
		if rendered == "" {
			taskContextAddOmission(&manifest, taskContextOmissionUnreadable, 1)
			continue
		}
		if usedBytes > 0 {
			assembly.Rendered += "\n\n"
			usedBytes += 2
		}
		assembly.Rendered += rendered
		usedBytes += len(rendered)
		manifestItem := taskContextManifestItem{
			Kind:             candidate.kind,
			SourceClass:      candidate.kind,
			Path:             candidate.path,
			SourceIdentity:   candidate.path,
			SelectionReason:  candidate.selectionReason,
			ContentIdentity:  source.hash,
			ContentReference: taskContextShortContentReference(source.hash),
			Bytes:            len(rendered),
			AdmittedBytes:    len(rendered),
			Truncated:        source.truncated || renderedTruncated,
		}
		for _, symbol := range candidate.symbols {
			manifestItem.Symbols = append(manifestItem.Symbols, symbol.Name)
		}
		manifest.Items = append(manifest.Items, manifestItem)
		if candidate.memory == nil && candidate.kind != taskContextEvidenceTest && candidate.kind != taskContextEvidenceDoc {
			for _, associated := range taskContextAssociatedCandidates(candidate.path) {
				if !taskContextAssociatedCandidateExists(workspace, associated.path) {
					continue
				}
				if _, exists := candidates[associated.dedupeKey]; exists {
					continue
				}
				candidates[associated.dedupeKey] = associated
				queue.push(associated)
			}
		}
	}

	manifest.CandidateCount = len(candidates)
	manifest.AdmittedCount = len(manifest.Items)
	manifest.AdmittedBytes = usedBytes
	assembly.Manifest = manifest
	return assembly, nil
}

func taskContextAddOmission(manifest *taskContextSelectionManifest, reason string, count int) {
	if manifest == nil || count <= 0 || strings.TrimSpace(reason) == "" {
		return
	}
	if manifest.OmissionCounts == nil {
		manifest.OmissionCounts = make(map[string]int)
	}
	manifest.OmissionCounts[reason] += count
}

func taskContextMemoryCandidates(ctx context.Context, index taskContextMemoryIndex, repositoryName, prompt string) ([]taskContextCandidate, int, error) {
	terms := extractTaskContextLexicalTerms(prompt)
	if len(terms) == 0 {
		terms = []string{"*"}
	}

	memoriesByKey := make(map[string]rhizome.Memory)
	unbound := 0
	for _, term := range terms {
		query := term
		if term != "*" {
			query = safeTaskContextFTSQuery(term)
			if query == "" {
				continue
			}
		}
		memories, err := index.SearchMemories(ctx, repositoryName, query, "", 32)
		if err != nil {
			return nil, 0, err
		}
		sort.Slice(memories, func(i, j int) bool {
			left := strings.Join([]string{memories[i].RepositoryName, memories[i].Category, memories[i].Title, memories[i].Content}, "\x00")
			right := strings.Join([]string{memories[j].RepositoryName, memories[j].Category, memories[j].Title, memories[j].Content}, "\x00")
			return left < right
		})
		for _, memory := range memories {
			if memory.RepositoryName != repositoryName {
				unbound++
				continue
			}
			key := strings.Join([]string{memory.RepositoryName, memory.Category, memory.Title}, "\x00")
			if _, exists := memoriesByKey[key]; !exists {
				memoriesByKey[key] = memory
			}
		}
	}

	keys := make([]string, 0, len(memoriesByKey))
	for key := range memoriesByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	candidates := make([]taskContextCandidate, 0, len(keys))
	for _, key := range keys {
		memory := memoriesByKey[key]
		identity := taskContextShortReference(key)
		candidates = append(candidates, taskContextCandidate{
			kind:            taskContextEvidenceMemory,
			path:            "memory/" + identity,
			dedupeKey:       "memory:" + key,
			priority:        5,
			selectionReason: taskContextSelectionReasonLocalMemory,
			memory:          &memory,
		})
	}
	return candidates, unbound, nil
}

func taskContextAssociatedCandidates(sourcePath string) []taskContextCandidate {
	cleaned, err := cleanTaskContextRepositoryPath(sourcePath)
	if err != nil || taskContextIsAssociatedTestPath(cleaned) {
		return nil
	}

	extension := strings.ToLower(filepath.Ext(cleaned))
	base := strings.TrimSuffix(filepath.Base(cleaned), filepath.Ext(cleaned))
	directory := filepath.ToSlash(filepath.Dir(cleaned))
	if directory == "." {
		directory = ""
	}

	paths := make(map[string]string)
	add := func(path, kind string) {
		cleanedPath, cleanErr := cleanTaskContextRepositoryPath(path)
		if cleanErr != nil || cleanedPath == cleaned {
			return
		}
		paths[cleanedPath] = kind
	}

	switch extension {
	case ".go":
		add(filepath.Join(directory, base+"_test.go"), taskContextEvidenceTest)
	case ".py":
		add(filepath.Join(directory, "test_"+base+".py"), taskContextEvidenceTest)
		add(filepath.Join(directory, base+"_test.py"), taskContextEvidenceTest)
	case ".ts", ".tsx":
		add(filepath.Join(directory, base+".test"+extension), taskContextEvidenceTest)
		add(filepath.Join(directory, base+".spec"+extension), taskContextEvidenceTest)
	case ".js", ".jsx", ".mjs", ".cjs":
		add(filepath.Join(directory, base+".test"+extension), taskContextEvidenceTest)
		add(filepath.Join(directory, base+".spec"+extension), taskContextEvidenceTest)
	default:
		return nil
	}

	add(filepath.Join(directory, base+".md"), taskContextEvidenceDoc)
	add(filepath.Join(directory, base+".mdx"), taskContextEvidenceDoc)
	if directory != "" {
		add("README.md", taskContextEvidenceDoc)
		add(filepath.Join("docs", base+".md"), taskContextEvidenceDoc)
		add(filepath.Join("docs", base+".mdx"), taskContextEvidenceDoc)
	} else {
		add("README.md", taskContextEvidenceDoc)
		add("CONTRIBUTING.md", taskContextEvidenceDoc)
	}

	pathsList := make([]string, 0, len(paths))
	for path := range paths {
		pathsList = append(pathsList, path)
	}
	sort.Strings(pathsList)
	candidates := make([]taskContextCandidate, 0, len(pathsList))
	for _, path := range pathsList {
		kind := paths[path]
		reason := taskContextSelectionReasonAssociatedDocumentation
		if kind == taskContextEvidenceTest {
			reason = taskContextSelectionReasonAssociatedTest
		}
		candidates = append(candidates, taskContextCandidate{
			kind:            kind,
			path:            path,
			dedupeKey:       path,
			priority:        4,
			selectionReason: reason,
		})
	}
	return candidates
}

func taskContextAssociatedCandidateExists(workspace *os.Root, path string) bool {
	if workspace == nil {
		return false
	}
	if err := taskContextRejectRootSymlinks(workspace, path); err != nil {
		return true
	}
	info, err := workspace.Stat(filepath.FromSlash(path))
	if err != nil {
		// Preserve candidates whose path exists but cannot be safely inspected so
		// the admission path records a stable security omission. Missing
		// conventional names are not candidates and do not inflate provenance.
		return !os.IsNotExist(err)
	}
	return info.Mode().IsRegular()
}

func taskContextRejectRootSymlinks(workspace *os.Root, relativePath string) error {
	cleaned, err := cleanTaskContextRepositoryPath(relativePath)
	if err != nil {
		return fmt.Errorf("%w: %v", errTaskContextPathSecurity, err)
	}
	partial := ""
	for _, segment := range strings.Split(cleaned, "/") {
		if partial == "" {
			partial = segment
		} else {
			partial += "/" + segment
		}
		info, statErr := workspace.Lstat(filepath.FromSlash(partial))
		if statErr != nil {
			if os.IsNotExist(statErr) {
				return nil
			}
			return fmt.Errorf("%w: lstat %s: %v", errTaskContextPathSecurity, partial, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink %s", errTaskContextPathSecurity, partial)
		}
	}
	return nil
}

func taskContextIsAssociatedTestPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasPrefix(base, "test_") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_test.py")
}

func taskContextInvalidFileAnchorCount(prompt string) int {
	count := 0
	for _, token := range strings.FieldsFunc(prompt, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("`'\"()[]{}<>,;:!?=", r)
	}) {
		if token == "" || !taskContextLooksLikePathToken(token) {
			continue
		}
		if _, err := cleanTaskContextRepositoryPath(strings.TrimRight(token, ".")); err != nil {
			count++
		}
	}
	return count
}

func taskContextLooksLikePathToken(token string) bool {
	return strings.Contains(token, "/") || strings.Contains(token, "\\") || strings.HasPrefix(token, ".") || filepath.Ext(token) != "" || taskContextForbiddenPath(token)
}

func taskContextShortReference(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func taskContextShortContentReference(identity string) string {
	identity = strings.TrimSpace(identity)
	if len(identity) >= 12 {
		return identity[:12]
	}
	return taskContextShortReference(identity)
}

func taskContextSourceLocalMemoryAvailability(sourceRepository string) string {
	_, _, _, availability := taskContextSourceLocalMemoryPaths(sourceRepository)
	return availability
}

func taskContextSourceLocalMemoryPaths(sourceRepository string) (string, string, string, string) {
	canonical, err := taskContextCanonicalDirectory(sourceRepository, "source repository")
	if err != nil {
		return "", "", "", taskContextOmissionMemoryUnavailable
	}
	statePath := filepath.Join(canonical, tendrilStateDirectory)
	stateInfo, err := os.Lstat(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return canonical, "", "", taskContextOmissionMemoryMissing
		}
		return canonical, "", "", taskContextOmissionMemoryUnavailable
	}
	if stateInfo.Mode()&os.ModeSymlink != 0 || !stateInfo.IsDir() {
		return canonical, "", "", taskContextOmissionMemoryUnavailable
	}
	databasePath := filepath.Join(statePath, rhizomeIndexDatabase)
	databaseInfo, err := os.Lstat(databasePath)
	if err != nil {
		if os.IsNotExist(err) {
			return canonical, databasePath, "", taskContextOmissionMemoryMissing
		}
		return canonical, databasePath, "", taskContextOmissionMemoryUnavailable
	}
	if databaseInfo.Mode()&os.ModeSymlink != 0 || !databaseInfo.Mode().IsRegular() {
		return canonical, databasePath, "", taskContextOmissionMemoryUnavailable
	}
	keyPath := filepath.Join(statePath, rhizomeIndexKeyFile)
	keyInfo, err := os.Lstat(keyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return canonical, databasePath, keyPath, taskContextOmissionMemoryUnavailable
		}
		return canonical, databasePath, keyPath, taskContextOmissionMemoryUnavailable
	}
	if keyInfo.Mode()&os.ModeSymlink != 0 || !keyInfo.Mode().IsRegular() {
		return canonical, databasePath, keyPath, taskContextOmissionMemoryUnavailable
	}
	key, err := os.ReadFile(keyPath)
	if err != nil || len(key) != 32 {
		return canonical, databasePath, keyPath, taskContextOmissionMemoryUnavailable
	}
	return canonical, databasePath, keyPath, ""
}

func taskContextObservationEvent(stepID, sessionID, substrate string, manifest taskContextSelectionManifest) eventbus.Event {
	safeStepID := taskContextSafeCorrelationID(stepID)
	safeSessionID := taskContextSafeCorrelationID(sessionID)
	items := make([]map[string]interface{}, 0, len(manifest.Items))
	for _, item := range manifest.Items {
		items = append(items, map[string]interface{}{
			"sourceClass":     item.SourceClass,
			"sourceIdentity":  item.SourceIdentity,
			"selectionReason": item.SelectionReason,
			"contentRef":      item.ContentReference,
			"admittedBytes":   item.AdmittedBytes,
			"truncated":       item.Truncated,
		})
	}
	omissions := make(map[string]interface{}, len(manifest.OmissionCounts))
	for reason, count := range manifest.OmissionCounts {
		omissions[reason] = count
	}
	data := map[string]interface{}{
		"stepId":                safeStepID,
		"substrateRef":          taskContextShortReference(manifest.SourceRepository),
		"workspaceRevisionRef":  taskContextShortReference(manifest.StartingRevision),
		"effectiveMaxBytes":     manifest.EffectiveMaxBytes,
		"effectiveItemMaxBytes": manifest.EffectiveItemMaxBytes,
		"effectiveMaxItems":     manifest.EffectiveMaxItems,
		"candidateCount":        manifest.CandidateCount,
		"admittedCount":         manifest.AdmittedCount,
		"admittedBytes":         manifest.AdmittedBytes,
		"omissionCounts":        omissions,
		"items":                 items,
	}
	if strings.TrimSpace(substrate) != "" {
		data["substrate"] = strings.TrimSpace(substrate)
	}
	return eventbus.Event{
		Type:      eventbus.EventTaskContextAssembled,
		Source:    safeStepID,
		SessionID: safeSessionID,
		Data:      data,
	}
}

func taskContextSafeCorrelationID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 128 && !strings.ContainsAny(value, "/\\:\r\n\x00") {
		return value
	}
	return "ref-" + taskContextShortReference(value)
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
		return taskContextSourceFile{}, fmt.Errorf("%w: %v", errTaskContextPathSecurity, err)
	}
	if workspaceRoot == nil {
		return taskContextSourceFile{}, fmt.Errorf("execution workspace root is unavailable")
	}
	if err := taskContextRejectRootSymlinks(workspaceRoot, cleaned); err != nil {
		return taskContextSourceFile{}, err
	}
	info, err := workspaceRoot.Stat(filepath.FromSlash(cleaned))
	if err != nil {
		if os.IsNotExist(err) {
			return taskContextSourceFile{}, fmt.Errorf("path is not a regular file: %w", os.ErrNotExist)
		}
		return taskContextSourceFile{}, fmt.Errorf("%w: stat %s: %v", errTaskContextPathSecurity, cleaned, err)
	}
	if !info.Mode().IsRegular() {
		return taskContextSourceFile{}, fmt.Errorf("path is not a regular file")
	}
	file, err := workspaceRoot.Open(filepath.FromSlash(cleaned))
	if err != nil {
		if os.IsNotExist(err) {
			return taskContextSourceFile{}, fmt.Errorf("open %s: %w", cleaned, os.ErrNotExist)
		}
		return taskContextSourceFile{}, fmt.Errorf("%w: open %s: %v", errTaskContextPathSecurity, cleaned, err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return taskContextSourceFile{}, fmt.Errorf("%w: stat opened %s: %v", errTaskContextPathSecurity, cleaned, err)
	}
	if !info.Mode().IsRegular() {
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
	return taskContextSourceFile{content: evidence.Bytes(), hash: hex.EncodeToString(hash.Sum(nil)), truncated: evidence.Truncated()}, nil
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
	truncated  bool
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
	if len(chunk) == 0 {
		return
	}
	if c.limit <= len(c.content) {
		c.truncated = true
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
		if len(content) > 0 {
			c.truncated = true
		}
		return
	}
	if len(content) > remaining {
		c.truncated = true
		content = content[:remaining]
	}
	c.content = append(c.content, content...)
}

func (c *taskContextEvidenceCollector) Bytes() []byte {
	return c.content
}

func (c *taskContextEvidenceCollector) Truncated() bool {
	return c.truncated
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
	case taskContextEvidenceTest:
		builder.WriteString("Associated test evidence: ")
	case taskContextEvidenceDoc:
		builder.WriteString("Associated documentation evidence: ")
	case taskContextEvidenceMemory:
		builder.WriteString("Source-local project memory evidence: ")
	}
	builder.WriteString("`")
	builder.WriteString(candidate.path)
	builder.WriteString("`\n")
	if candidate.memory != nil {
		if strings.TrimSpace(candidate.memory.Category) != "" {
			builder.WriteString("Category: ")
			builder.WriteString(strings.TrimSpace(candidate.memory.Category))
			builder.WriteString("\n")
		}
		if strings.TrimSpace(candidate.memory.Title) != "" {
			builder.WriteString("Title: ")
			builder.WriteString(strings.TrimSpace(candidate.memory.Title))
			builder.WriteString("\n")
		}
	}
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
	truncated, _ := truncateTaskContextEvidenceWithStatus(content, limit)
	return truncated
}

func truncateTaskContextEvidenceWithStatus(content string, limit int) (string, bool) {
	if limit <= 0 {
		return "", true
	}
	if len(content) <= limit {
		return content, false
	}
	marker := "\n[truncated]"
	if limit <= len(marker) {
		return marker[:limit], true
	}
	cutLimit := limit - len(marker)
	cut := content[:cutLimit]
	if index := strings.LastIndexByte(cut, '\n'); index > 0 {
		cut = cut[:index]
	}
	return cut + marker, true
}
