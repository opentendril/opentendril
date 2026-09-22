package rhizome

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AllowedBotanistKinds lists the knowledge kinds a Botanist may assert directly.
var AllowedBotanistKinds = []Kind{
	KindObservation,
	KindFact,
	KindConstraint,
	KindCorrection,
	KindRejectedInterpretation,
}

// evidenceManifestVersion is a stable identifier for the current manifest
// encoding. Slice 4 staleness checks rely on this remaining stable.
const evidenceManifestVersion = "v1"

// EvidenceFile records a single validated evidence file.
type EvidenceFile struct {
	// RelativePath is the canonical repository-relative path.
	RelativePath string `json:"relativePath"`
	// ContentHash is the SHA-256 hex digest of the file bytes at bind time.
	ContentHash string `json:"contentHash"`
}

// EvidenceManifest is the versioned, stable encoding stored in RevisionMetadata.
// Slice 4 may read this without modification.
type EvidenceManifest struct {
	Version string         `json:"version"`
	Files   []EvidenceFile `json:"files"`
	// ManifestHash is the SHA-256 hex digest of the canonical JSON of the
	// files list (sorted by RelativePath) and serves as ContentIdentity.
	ManifestHash string `json:"manifestHash"`
}

// BotanistAddIntent carries the caller-supplied parameters for a Botanist add
// operation before policy transforms them into a fully resolved Memory.
type BotanistAddIntent struct {
	RepositoryName string
	Category       string
	Title          string
	Content        string
	Tags           string
	SessionID      string
	// Kind is the requested knowledge kind. Defaults to observation when empty.
	Kind Kind
	// EvidencePaths contains repository-relative paths supplied via --evidence.
	// Empty means no evidence binding; the memory is repository-wide.
	EvidencePaths []string
	// SubstrateRoot is the absolute path to the repository root. Required when
	// EvidencePaths is non-empty; used to validate and hash evidence files.
	SubstrateRoot string
}

// SupersedeIntent carries the caller-supplied parameters for a supersession.
type SupersedeIntent struct {
	RepositoryName string
	OldTitle       string
	NewTitle       string
	Category       string
	Tags           string
	Content        string
	EvidencePaths  []string
	SubstrateRoot  string
}

// ApplyBotanistAdd validates the intent and constructs a fully resolved Memory
// with origin=botanist, authority=botanist, status=established. It does not
// call the backend; the caller is responsible for persisting the result.
func ApplyBotanistAdd(intent BotanistAddIntent) (Memory, error) {
	if strings.TrimSpace(intent.Title) == "" {
		return Memory{}, fmt.Errorf("title is required")
	}
	if strings.TrimSpace(intent.Content) == "" {
		return Memory{}, fmt.Errorf("content is required")
	}

	kind := intent.Kind
	if kind == "" {
		kind = KindObservation
	}
	if !isBotanistKind(kind) {
		return Memory{}, fmt.Errorf("unsupported kind %q: allowed kinds are observation, fact, constraint, correction, rejected-interpretation", kind)
	}

	mem := Memory{
		RepositoryName: intent.RepositoryName,
		Category:       intent.Category,
		Title:          strings.TrimSpace(intent.Title),
		Content:        strings.TrimSpace(intent.Content),
		Tags:           intent.Tags,
		SessionID:      intent.SessionID,
		CreatedAt:      time.Now().UTC(),
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		Kind:           kind,
	}
	mem.StableID = StableMemoryIdentity(mem.RepositoryName, mem.Title)

	if len(intent.EvidencePaths) > 0 {
		manifest, err := BindEvidence(intent.EvidencePaths, intent.SubstrateRoot)
		if err != nil {
			return Memory{}, fmt.Errorf("evidence binding failed: %w", err)
		}
		encoded, err := json.Marshal(manifest)
		if err != nil {
			return Memory{}, fmt.Errorf("encode evidence manifest: %w", err)
		}
		revision, err := resolveGitRevision(intent.SubstrateRoot)
		if err != nil {
			return Memory{}, fmt.Errorf("resolve current revision: %w", err)
		}
		mem.RevisionMetadata = string(encoded)
		mem.ContentIdentity = manifest.ManifestHash
		mem.RevisionIdentity = revision
		mem.SourceClass = "botanist-substrate-file-evidence-" + evidenceManifestVersion
		mem.SourceIdentity = "substrate:" + manifest.ManifestHash
	}

	if err := mem.Validate(); err != nil {
		return Memory{}, fmt.Errorf("constructed memory is invalid: %w", err)
	}
	return mem, nil
}

// ApplyConfirm transitions a proposed memory to established under Botanist
// authority while preserving the original origin and all provenance fields.
// The target must exist, be unique (exact title match), and have status=proposed.
func ApplyConfirm(ctx context.Context, backend MemoryBackend, repositoryName, title string) (Memory, error) {
	target, err := exactMemoryLookup(ctx, backend, repositoryName, title)
	if err != nil {
		return Memory{}, fmt.Errorf("confirm: %w", err)
	}

	if target.Status != StatusProposed {
		return Memory{}, fmt.Errorf("confirm requires a proposed memory; %q has status %q", title, target.Status)
	}

	confirmed := target
	confirmed.Authority = AuthorityBotanist
	confirmed.Status = StatusEstablished

	if err := confirmed.Validate(); err != nil {
		return Memory{}, fmt.Errorf("confirm produces invalid envelope: %w", err)
	}
	if err := backend.StoreMemory(ctx, confirmed); err != nil {
		return Memory{}, fmt.Errorf("persist confirmation: %w", err)
	}
	return confirmed, nil
}

// ApplyReject transitions a proposed memory to rejected under Botanist
// authority. Content and origin are preserved; the record is not deleted.
// The target must exist, be unique, and have status=proposed.
func ApplyReject(ctx context.Context, backend MemoryBackend, repositoryName, title string) (Memory, error) {
	target, err := exactMemoryLookup(ctx, backend, repositoryName, title)
	if err != nil {
		return Memory{}, fmt.Errorf("reject: %w", err)
	}

	if target.Status != StatusProposed {
		return Memory{}, fmt.Errorf("reject requires a proposed memory; %q has status %q", title, target.Status)
	}

	rejected := target
	rejected.Authority = AuthorityBotanist
	rejected.Status = StatusRejected

	if err := rejected.Validate(); err != nil {
		return Memory{}, fmt.Errorf("reject produces invalid envelope: %w", err)
	}
	if err := backend.StoreMemory(ctx, rejected); err != nil {
		return Memory{}, fmt.Errorf("persist rejection: %w", err)
	}
	return rejected, nil
}

// ApplySupersede implements the fail-closed supersession protocol:
//
//  1. Validate and construct the replacement memory fully.
//  2. Mark the old item superseded and persist it.
//  3. Only after step 2 succeeds, persist the replacement.
//
// If step 2 fails, the replacement is not persisted.
// If step 3 fails, the old item remains superseded and the caller is notified.
// Supersession of an already superseded item is rejected.
func ApplySupersede(ctx context.Context, backend MemoryBackend, intent SupersedeIntent) (replacement Memory, err error) {
	if strings.TrimSpace(intent.OldTitle) == "" {
		return Memory{}, fmt.Errorf("supersede: old title is required")
	}
	if strings.TrimSpace(intent.NewTitle) == "" {
		return Memory{}, fmt.Errorf("supersede: new title is required")
	}
	if intent.OldTitle == intent.NewTitle {
		return Memory{}, fmt.Errorf("supersede: replacement title must differ from old title")
	}

	old, err := exactMemoryLookup(ctx, backend, intent.RepositoryName, intent.OldTitle)
	if err != nil {
		return Memory{}, fmt.Errorf("supersede: %w", err)
	}
	if old.Status == StatusSuperseded {
		return Memory{}, fmt.Errorf("supersede: %q is already superseded", intent.OldTitle)
	}

	// Step 1: construct and fully validate the replacement.
	addIntent := BotanistAddIntent{
		RepositoryName: intent.RepositoryName,
		Category:       intent.Category,
		Title:          intent.NewTitle,
		Content:        intent.Content,
		Tags:           intent.Tags,
		Kind:           KindCorrection,
		EvidencePaths:  intent.EvidencePaths,
		SubstrateRoot:  intent.SubstrateRoot,
	}
	if addIntent.Category == "" {
		addIntent.Category = old.Category
	}
	if addIntent.Tags == "" {
		addIntent.Tags = old.Tags
	}
	replacement, err = ApplyBotanistAdd(addIntent)
	if err != nil {
		return Memory{}, fmt.Errorf("supersede: build replacement: %w", err)
	}

	// Step 2: mark old item superseded before persisting the replacement.
	supersededOld := old
	supersededOld.Status = StatusSuperseded
	supersededOld.Supersession = replacement.StableID

	if err = backend.StoreMemory(ctx, supersededOld); err != nil {
		return Memory{}, fmt.Errorf("supersede: mark old item superseded: %w", err)
	}

	// Step 3: persist the replacement. If this fails, the old item stays
	// superseded. Do not attempt to restore it.
	if err = backend.StoreMemory(ctx, replacement); err != nil {
		return Memory{}, fmt.Errorf(
			"supersede: old item %q is now superseded but the replacement %q could not be persisted: %w; "+
				"the replacement must be re-submitted manually",
			intent.OldTitle, intent.NewTitle, err,
		)
	}

	return replacement, nil
}

// BindEvidence validates each supplied path against the substrate root,
// hashes the file contents, resolves the current Git commit, and returns a
// deterministic evidence manifest. Evidence paths must be repository-relative
// and non-traversing.
//
// Rules enforced:
//   - Absolute paths are rejected.
//   - ".." components are rejected.
//   - Missing files are rejected.
//   - Directories are rejected.
//   - Symlinks whose resolved target escapes substrateRoot are rejected.
//   - Paths are cleaned and sorted deterministically.
//   - No absolute host path appears in the returned manifest.
func BindEvidence(relativePaths []string, substrateRoot string) (EvidenceManifest, error) {
	if len(relativePaths) == 0 {
		return EvidenceManifest{}, fmt.Errorf("BindEvidence: no paths supplied")
	}
	if substrateRoot == "" {
		return EvidenceManifest{}, fmt.Errorf("BindEvidence: substrate root is required")
	}

	absRoot, err := filepath.Abs(substrateRoot)
	if err != nil {
		return EvidenceManifest{}, fmt.Errorf("BindEvidence: resolve substrate root: %w", err)
	}

	// Canonicalize and deduplicate.
	seen := make(map[string]bool, len(relativePaths))
	var canonicalPaths []string
	for _, raw := range relativePaths {
		if filepath.IsAbs(raw) {
			return EvidenceManifest{}, fmt.Errorf("BindEvidence: absolute evidence path rejected: %q", raw)
		}
		clean := filepath.Clean(raw)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return EvidenceManifest{}, fmt.Errorf("BindEvidence: traversal rejected: %q", raw)
		}
		if !seen[clean] {
			seen[clean] = true
			canonicalPaths = append(canonicalPaths, clean)
		}
	}
	sort.Strings(canonicalPaths)

	files := make([]EvidenceFile, 0, len(canonicalPaths))
	for _, relPath := range canonicalPaths {
		absPath := filepath.Join(absRoot, relPath)

		// Resolve symlinks to detect escapes.
		resolved, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				return EvidenceManifest{}, fmt.Errorf("BindEvidence: path does not exist: %q", relPath)
			}
			return EvidenceManifest{}, fmt.Errorf("BindEvidence: resolve path %q: %w", relPath, err)
		}
		resolvedAbs, err := filepath.Abs(resolved)
		if err != nil {
			return EvidenceManifest{}, fmt.Errorf("BindEvidence: resolve absolute path for %q: %w", relPath, err)
		}
		if !strings.HasPrefix(resolvedAbs, absRoot+string(filepath.Separator)) && resolvedAbs != absRoot {
			return EvidenceManifest{}, fmt.Errorf("BindEvidence: path %q resolves outside substrate root", relPath)
		}

		info, err := os.Lstat(resolvedAbs)
		if err != nil {
			return EvidenceManifest{}, fmt.Errorf("BindEvidence: stat %q: %w", relPath, err)
		}
		if info.IsDir() {
			return EvidenceManifest{}, fmt.Errorf("BindEvidence: path is a directory: %q", relPath)
		}

		data, err := os.ReadFile(resolvedAbs)
		if err != nil {
			return EvidenceManifest{}, fmt.Errorf("BindEvidence: read %q: %w", relPath, err)
		}
		sum := sha256.Sum256(data)
		files = append(files, EvidenceFile{
			RelativePath: relPath,
			ContentHash:  hex.EncodeToString(sum[:]),
		})
	}

	// Compute a deterministic manifest hash over the sorted file list.
	manifestHash, err := hashEvidenceFiles(files)
	if err != nil {
		return EvidenceManifest{}, fmt.Errorf("BindEvidence: compute manifest hash: %w", err)
	}

	return EvidenceManifest{
		Version:      evidenceManifestVersion,
		Files:        files,
		ManifestHash: manifestHash,
	}, nil
}

// hashEvidenceFiles returns a SHA-256 hex digest of the canonical JSON
// representation of the files slice. The files must already be sorted.
func hashEvidenceFiles(files []EvidenceFile) (string, error) {
	encoded, err := json.Marshal(files)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// resolveGitRevision returns the current HEAD commit SHA for the repository
// at root. It fails closed: if the revision cannot be determined, an error is
// returned and the caller must not persist evidence metadata.
func resolveGitRevision(root string) (string, error) {
	cmd := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve Git revision at %q: %w", root, err)
	}
	rev := strings.TrimSpace(string(out))
	if rev == "" {
		return "", fmt.Errorf("resolve Git revision at %q: empty output", root)
	}
	return rev, nil
}

// exactMemoryLookup finds a memory with an exact title match in the given
// repository. It fails closed:
//   - returns an error if no record with that exact title exists;
//   - returns an error if multiple records share the same title (ambiguous).
//
// Only the MemoryBackend.ListMemories interface is used; the backend interface
// is not widened in this slice.
func exactMemoryLookup(ctx context.Context, backend MemoryBackend, repositoryName, title string) (Memory, error) {
	// Fetch a broad set and filter locally to avoid relying on FTS inexact
	// matching semantics. Limit is generous; exact comparison is the authority.
	all, err := backend.ListMemories(ctx, repositoryName, "", 10000)
	if err != nil {
		return Memory{}, fmt.Errorf("list memories for exact lookup: %w", err)
	}

	var matches []Memory
	for _, m := range all {
		if m.Title == title {
			matches = append(matches, m)
		}
	}

	switch len(matches) {
	case 0:
		return Memory{}, fmt.Errorf("no memory found with exact title %q in repository %q", title, repositoryName)
	case 1:
		return matches[0], nil
	default:
		return Memory{}, fmt.Errorf("ambiguous: %d records share the exact title %q in repository %q; explicit disambiguation required", len(matches), title, repositoryName)
	}
}

// isBotanistKind reports whether kind is in the allowed set for direct
// Botanist assertions.
func isBotanistKind(kind Kind) bool {
	for _, allowed := range AllowedBotanistKinds {
		if kind == allowed {
			return true
		}
	}
	return false
}

// ValidateBotanistKind returns an error if kind is not an allowed Botanist kind.
func ValidateBotanistKind(kind Kind) error {
	if !isBotanistKind(kind) {
		return fmt.Errorf("unsupported kind %q: allowed kinds are observation, fact, constraint, correction, rejected-interpretation", kind)
	}
	return nil
}
