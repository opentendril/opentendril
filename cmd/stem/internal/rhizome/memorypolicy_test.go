package rhizome

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---- fake backend for policy tests ----

type fakeMemoryBackend struct {
	records  map[string]Memory // keyed by title (single-repo tests)
	storeErr error             // if non-nil, StoreMemory returns this
	// storeErrOnTitle causes StoreMemory to fail for a specific title.
	storeErrOnTitle string
	// storeCallOrder records titles in store call order to prove ordering.
	storeCallOrder []string
}

func newFakeBackend() *fakeMemoryBackend {
	return &fakeMemoryBackend{records: make(map[string]Memory)}
}

func (f *fakeMemoryBackend) StoreMemory(_ context.Context, m Memory) error {
	if f.storeErrOnTitle != "" && m.Title == f.storeErrOnTitle {
		return f.storeErr
	}
	if f.storeErr != nil && f.storeErrOnTitle == "" {
		return f.storeErr
	}
	f.storeCallOrder = append(f.storeCallOrder, m.Title+"/"+string(m.Status))
	f.records[m.Title] = m
	return nil
}

func (f *fakeMemoryBackend) GetMemory(_ context.Context, repositoryName string, title string) (Memory, bool, error) {
	m, ok := f.records[title]
	if ok && m.RepositoryName == repositoryName {
		return m, true, nil
	}
	return Memory{}, false, nil
}

func (f *fakeMemoryBackend) ListMemories(_ context.Context, _ string, _ string, _ int) ([]Memory, error) {
	result := make([]Memory, 0, len(f.records))
	for _, m := range f.records {
		result = append(result, m)
	}
	return result, nil
}

func (f *fakeMemoryBackend) SearchMemories(_ context.Context, _ string, _ string, _ string, _ int) ([]Memory, error) {
	return nil, nil
}

func (f *fakeMemoryBackend) DeleteMemory(_ context.Context, _ string, title string) error {
	delete(f.records, title)
	return nil
}

func (f *fakeMemoryBackend) seed(m Memory) {
	f.records[m.Title] = m
}

// ---- helper to make a temp Git repo ----

func makeTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.invalid")
	runGit(t, dir, "config", "user.name", "Test")
	// Create an initial commit so HEAD is valid.
	f := filepath.Join(dir, "README.md")
	if err := os.WriteFile(f, []byte("# test"), 0o644); err != nil {
		t.Fatalf("WriteFile README: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// ======================================================================
// BotanistAdd policy
// ======================================================================

func TestBotanistAddPolicyDefaults(t *testing.T) {
	mem, err := ApplyBotanistAdd(BotanistAddIntent{
		RepositoryName: "owner/repo",
		Category:       "Design",
		Title:          "Alpha Principle",
		Content:        "Keep interfaces narrow.",
	})
	if err != nil {
		t.Fatalf("ApplyBotanistAdd returned error: %v", err)
	}
	if mem.Origin != OriginBotanist {
		t.Errorf("origin: want botanist, got %q", mem.Origin)
	}
	if mem.Authority != AuthorityBotanist {
		t.Errorf("authority: want botanist, got %q", mem.Authority)
	}
	if mem.Status != StatusEstablished {
		t.Errorf("status: want established, got %q", mem.Status)
	}
	if mem.Kind != KindObservation {
		t.Errorf("kind: want observation (default), got %q", mem.Kind)
	}
	if mem.StableID == "" {
		t.Error("StableID must not be empty")
	}
}

func TestBotanistAddPolicyAllowedKinds(t *testing.T) {
	for _, kind := range AllowedBotanistKinds {
		t.Run(string(kind), func(t *testing.T) {
			_, err := ApplyBotanistAdd(BotanistAddIntent{
				RepositoryName: "owner/repo",
				Title:          "Title " + string(kind),
				Content:        "content",
				Kind:           kind,
			})
			if err != nil {
				t.Errorf("kind %q unexpectedly rejected: %v", kind, err)
			}
		})
	}
}

func TestBotanistAddPolicyUnknownKindRejected(t *testing.T) {
	_, err := ApplyBotanistAdd(BotanistAddIntent{
		RepositoryName: "owner/repo",
		Title:          "Bad Kind Test",
		Content:        "content",
		Kind:           "verdict", // not allowed
	})
	if err == nil {
		t.Fatal("expected error for unknown kind, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported kind") {
		t.Errorf("expected unsupported kind error, got: %v", err)
	}
}

func TestBotanistAddPolicyCallerCannotSetOriginOrAuthority(t *testing.T) {
	// The caller supplies only the BotanistAddIntent; origin/authority/status
	// are policy outputs. Verify that the result is always botanist/botanist/established
	// regardless of what a caller might try to embed in the title/content.
	mem, err := ApplyBotanistAdd(BotanistAddIntent{
		RepositoryName: "owner/repo",
		Title:          "Policy Override Attempt",
		Content:        "origin=mycorrhizal authority=deterministic",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mem.Origin != OriginBotanist || mem.Authority != AuthorityBotanist || mem.Status != StatusEstablished {
		t.Errorf("policy override not prevented: origin=%q authority=%q status=%q", mem.Origin, mem.Authority, mem.Status)
	}
}

func TestBotanistAddPolicyEmptyTitleRejected(t *testing.T) {
	_, err := ApplyBotanistAdd(BotanistAddIntent{
		RepositoryName: "owner/repo",
		Content:        "content",
	})
	if err == nil {
		t.Fatal("expected error for empty title")
	}
}

func TestBotanistAddPolicyEmptyContentRejected(t *testing.T) {
	_, err := ApplyBotanistAdd(BotanistAddIntent{
		RepositoryName: "owner/repo",
		Title:          "Some Title",
	})
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

// ======================================================================
// Confirm policy
// ======================================================================

func TestConfirmPolicyMycorrhizalProposal(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "AI Proposal",
		Content:        "Use narrow interfaces.",
		Origin:         OriginMycorrhizal,
		Authority:      AuthorityNone,
		Status:         StatusProposed,
		Kind:           KindObservation,
		StableID:       StableMemoryIdentity("owner/repo", "AI Proposal"),
	})

	confirmed, err := ApplyConfirm(ctx, backend, "owner/repo", "AI Proposal")
	if err != nil {
		t.Fatalf("ApplyConfirm returned error: %v", err)
	}
	if confirmed.Origin != OriginMycorrhizal {
		t.Errorf("origin must be preserved as mycorrhizal, got %q", confirmed.Origin)
	}
	if confirmed.Authority != AuthorityBotanist {
		t.Errorf("authority must become botanist, got %q", confirmed.Authority)
	}
	if confirmed.Status != StatusEstablished {
		t.Errorf("status must become established, got %q", confirmed.Status)
	}
	if confirmed.Content != "Use narrow interfaces." {
		t.Errorf("content must be preserved, got %q", confirmed.Content)
	}
	if confirmed.Title != "AI Proposal" {
		t.Errorf("title must be preserved, got %q", confirmed.Title)
	}
}

func TestConfirmAlreadyEstablishedRejected(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Established Memory",
		Content:        "already done",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "Established Memory"),
	})

	_, err := ApplyConfirm(ctx, backend, "owner/repo", "Established Memory")
	if err == nil {
		t.Fatal("expected error for confirming already established memory")
	}
	if !strings.Contains(err.Error(), "requires a proposed memory") {
		t.Errorf("expected requires a proposed memory error, got: %v", err)
	}
}

func TestConfirmRejectedStateRejected(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Rejected Memory",
		Content:        "rejected",
		Origin:         OriginMycorrhizal,
		Authority:      AuthorityBotanist,
		Status:         StatusRejected,
		StableID:       StableMemoryIdentity("owner/repo", "Rejected Memory"),
	})

	_, err := ApplyConfirm(ctx, backend, "owner/repo", "Rejected Memory")
	if err == nil {
		t.Fatal("expected error for confirming rejected memory")
	}
}

func TestConfirmMissingTitleFails(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()

	_, err := ApplyConfirm(ctx, backend, "owner/repo", "Does Not Exist")
	if err == nil {
		t.Fatal("expected error for missing title")
	}
	if !strings.Contains(err.Error(), "no memory found") {
		t.Errorf("expected no memory found error, got: %v", err)
	}
}

// ======================================================================
// Reject policy
// ======================================================================

func TestRejectPolicyPreservesOriginAndContent(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	originalContent := "Use shadow tables."
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "AI Proposal B",
		Content:        originalContent,
		Origin:         OriginMycorrhizal,
		Authority:      AuthorityNone,
		Status:         StatusProposed,
		Tags:           "db,design",
		StableID:       StableMemoryIdentity("owner/repo", "AI Proposal B"),
	})

	rejected, err := ApplyReject(ctx, backend, "owner/repo", "AI Proposal B")
	if err != nil {
		t.Fatalf("ApplyReject returned error: %v", err)
	}
	if rejected.Origin != OriginMycorrhizal {
		t.Errorf("origin must be preserved: want mycorrhizal, got %q", rejected.Origin)
	}
	if rejected.Authority != AuthorityBotanist {
		t.Errorf("authority must become botanist, got %q", rejected.Authority)
	}
	if rejected.Status != StatusRejected {
		t.Errorf("status must become rejected, got %q", rejected.Status)
	}
	if rejected.Content != originalContent {
		t.Errorf("content must be preserved, got %q", rejected.Content)
	}
	if rejected.Tags != "db,design" {
		t.Errorf("tags must be preserved, got %q", rejected.Tags)
	}
}

func TestRejectEstablishedRejected(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Established Memory",
		Content:        "established",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "Established Memory"),
	})

	_, err := ApplyReject(ctx, backend, "owner/repo", "Established Memory")
	if err == nil {
		t.Fatal("expected error for rejecting established memory")
	}
}

func TestRejectSupersededRejected(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Superseded Memory",
		Content:        "old",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusSuperseded,
		StableID:       StableMemoryIdentity("owner/repo", "Superseded Memory"),
	})

	_, err := ApplyReject(ctx, backend, "owner/repo", "Superseded Memory")
	if err == nil {
		t.Fatal("expected error for rejecting superseded memory")
	}
}

// ======================================================================
// Supersession policy
// ======================================================================

func TestSupersessionPreservesOldRecord(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Old Principle",
		Content:        "Do X.",
		Category:       "Design",
		Tags:           "core",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "Old Principle"),
	})

	_, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "Old Principle",
		NewTitle:       "New Principle",
		Content:        "Do Y instead.",
	})
	if err != nil {
		t.Fatalf("ApplySupersede returned error: %v", err)
	}

	// Old record must still exist in the backend.
	old, exists := backend.records["Old Principle"]
	if !exists {
		t.Fatal("old record was deleted; it must be preserved")
	}
	if old.Status != StatusSuperseded {
		t.Errorf("old record status: want superseded, got %q", old.Status)
	}
}

func TestSupersessionOldItemBecomesSuperseded(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Old Design",
		Content:        "Use A.",
		Category:       "Design",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "Old Design"),
	})

	replacement, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "Old Design",
		NewTitle:       "New Design",
		Content:        "Use B.",
	})
	if err != nil {
		t.Fatalf("ApplySupersede returned error: %v", err)
	}

	old := backend.records["Old Design"]
	if old.Status != StatusSuperseded {
		t.Errorf("old status: want superseded, got %q", old.Status)
	}
	if old.Supersession != replacement.StableID {
		t.Errorf("old.Supersession: want %q, got %q", replacement.StableID, old.Supersession)
	}
}

func TestSupersessionReplacementIsEstablishedBotanistCorrection(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Old Thing",
		Content:        "Old content.",
		Category:       "Patterns",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "Old Thing"),
	})

	replacement, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "Old Thing",
		NewTitle:       "New Thing",
		Content:        "New content.",
	})
	if err != nil {
		t.Fatalf("ApplySupersede returned error: %v", err)
	}
	if replacement.Origin != OriginBotanist {
		t.Errorf("replacement origin: want botanist, got %q", replacement.Origin)
	}
	if replacement.Authority != AuthorityBotanist {
		t.Errorf("replacement authority: want botanist, got %q", replacement.Authority)
	}
	if replacement.Status != StatusEstablished {
		t.Errorf("replacement status: want established, got %q", replacement.Status)
	}
	if replacement.Kind != KindCorrection {
		t.Errorf("replacement kind: want correction, got %q", replacement.Kind)
	}
}

func TestSupersessionOldSupersessionPointsToReplacementStableID(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "V1",
		Content:        "V1 content.",
		Category:       "Patterns",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "V1"),
	})

	replacement, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "V1",
		NewTitle:       "V2",
		Content:        "V2 content.",
	})
	if err != nil {
		t.Fatalf("ApplySupersede: %v", err)
	}
	old := backend.records["V1"]
	if old.Supersession != replacement.StableID {
		t.Errorf("supersession pointer: want %q, got %q", replacement.StableID, old.Supersession)
	}
	if replacement.StableID != StableMemoryIdentity("owner/repo", "V2") {
		t.Errorf("replacement StableID mismatch")
	}
}

func TestSupersessionSameNewTitleAsOldRejected(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Same",
		Content:        "content",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "Same"),
	})

	_, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "Same",
		NewTitle:       "Same",
		Content:        "content",
	})
	if err == nil {
		t.Fatal("expected error for same old and new title")
	}
	if !strings.Contains(err.Error(), "must differ") {
		t.Errorf("expected 'must differ' error, got: %v", err)
	}
}

func TestSupersessionMissingOldTitleFails(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()

	_, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "Ghost",
		NewTitle:       "New",
		Content:        "content",
	})
	if err == nil {
		t.Fatal("expected error for missing old title")
	}
}

func TestSupersessionAlreadySupersededRejected(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Already Gone",
		Content:        "old",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusSuperseded,
		StableID:       StableMemoryIdentity("owner/repo", "Already Gone"),
	})

	_, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "Already Gone",
		NewTitle:       "New",
		Content:        "content",
	})
	if err == nil {
		t.Fatal("expected error for superseding an already superseded item")
	}
	if !strings.Contains(err.Error(), "already superseded") {
		t.Errorf("expected already superseded error, got: %v", err)
	}
}

// TestSupersessionOldWriteFailurePreventsReplacement proves the safe ordering:
// if marking the old item superseded fails, the replacement is never persisted.
func TestSupersessionOldWriteFailurePreventsReplacement(t *testing.T) {
	ctx := context.Background()

	// Use a backend that fails on StoreMemory for the old title (when its
	// status is superseded).
	backend := &fakeMemoryBackend{
		records:         make(map[string]Memory),
		storeErr:        errors.New("simulated old-write failure"),
		storeErrOnTitle: "Old Item",
	}
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Old Item",
		Content:        "old content",
		Category:       "Design",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "Old Item"),
	})

	_, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "Old Item",
		NewTitle:       "New Item",
		Content:        "new content",
	})
	if err == nil {
		t.Fatal("expected error when old-write fails")
	}
	// Replacement must not have been persisted.
	if _, exists := backend.records["New Item"]; exists {
		t.Error("replacement was persisted despite old-write failure; ordering violated")
	}
}

// TestSupersessionReplacementWriteFailureLeavesOldSuperseded proves that if
// the replacement write fails after the old item is superseded, the old item
// stays superseded (fail-closed).
func TestSupersessionReplacementWriteFailureLeavesOldSuperseded(t *testing.T) {
	ctx := context.Background()

	backend := &orderingFakeBackend{
		records:             make(map[string]Memory),
		failOnReplacementOf: "Old Item B",
	}
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Old Item B",
		Content:        "old content",
		Category:       "Design",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "Old Item B"),
	})

	_, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "Old Item B",
		NewTitle:       "New Item B",
		Content:        "new content",
	})
	if err == nil {
		t.Fatal("expected error when replacement write fails")
	}
	if !strings.Contains(err.Error(), "old item") {
		t.Errorf("error should mention old item, got: %v", err)
	}
	// Old item must remain superseded.
	old := backend.records["Old Item B"]
	if old.Status != StatusSuperseded {
		t.Errorf("old item should remain superseded, got %q", old.Status)
	}
}

// orderingFakeBackend fails the replacement write after the old item is updated.
type orderingFakeBackend struct {
	records             map[string]Memory
	failOnReplacementOf string // after this old title is superseded, fail next new write
	oldSuperseded       bool
}

func (b *orderingFakeBackend) seed(m Memory) { b.records[m.Title] = m }

func (b *orderingFakeBackend) StoreMemory(_ context.Context, m Memory) error {
	// If we see the old item being marked superseded, record it.
	if m.Title == b.failOnReplacementOf && m.Status == StatusSuperseded {
		b.records[m.Title] = m
		b.oldSuperseded = true
		return nil
	}
	// Once old is superseded, fail the next write (the replacement).
	if b.oldSuperseded && m.Title != b.failOnReplacementOf {
		return errors.New("simulated replacement write failure")
	}
	b.records[m.Title] = m
	return nil
}

func (b *orderingFakeBackend) GetMemory(_ context.Context, repositoryName string, title string) (Memory, bool, error) {
	m, ok := b.records[title]
	if ok && m.RepositoryName == repositoryName {
		return m, true, nil
	}
	return Memory{}, false, nil
}

func (b *orderingFakeBackend) ListMemories(_ context.Context, _ string, _ string, _ int) ([]Memory, error) {
	result := make([]Memory, 0, len(b.records))
	for _, m := range b.records {
		result = append(result, m)
	}
	return result, nil
}

func (b *orderingFakeBackend) SearchMemories(_ context.Context, _ string, _ string, _ string, _ int) ([]Memory, error) {
	return nil, nil
}

func (b *orderingFakeBackend) DeleteMemory(_ context.Context, _ string, title string) error {
	delete(b.records, title)
	return nil
}

// ======================================================================
// Exact-title lookup
// ======================================================================

func TestExactMemoryLookupNoMatch(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()

	_, err := exactMemoryLookup(ctx, backend, "owner/repo", "Nonexistent")
	if err == nil {
		t.Fatal("expected error for missing title")
	}
	if !strings.Contains(err.Error(), "no memory found") {
		t.Errorf("expected no memory found error, got: %v", err)
	}
}

func TestExactMemoryLookupAmbiguousFails(t *testing.T) {
	ctx := context.Background()

	// Use a backend that can return multiple records with the same title.
	backend := &duplicateTitleBackend{
		items: []Memory{
			{RepositoryName: "owner/repo", Title: "Shared", Status: StatusProposed, Origin: OriginMycorrhizal, Authority: AuthorityNone, StableID: StableMemoryIdentity("owner/repo", "Shared")},
			{RepositoryName: "owner/repo", Title: "Shared", Status: StatusProposed, Origin: OriginMycorrhizal, Authority: AuthorityNone, StableID: StableMemoryIdentity("owner/repo", "Shared")},
		},
	}

	_, err := exactMemoryLookup(ctx, backend, "owner/repo", "Shared")
	if err == nil {
		t.Fatal("expected error for ambiguous title")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected ambiguous error, got: %v", err)
	}
}

type duplicateTitleBackend struct{ items []Memory }

func (d *duplicateTitleBackend) GetMemory(_ context.Context, _ string, _ string) (Memory, bool, error) {
	return Memory{}, false, fmt.Errorf("ambiguous exact hit simulation")
}

func (d *duplicateTitleBackend) ListMemories(_ context.Context, _ string, _ string, _ int) ([]Memory, error) {
	return d.items, nil
}
func (d *duplicateTitleBackend) SearchMemories(_ context.Context, _ string, _ string, _ string, _ int) ([]Memory, error) {
	return nil, nil
}
func (d *duplicateTitleBackend) StoreMemory(_ context.Context, _ Memory) error { return nil }
func (d *duplicateTitleBackend) DeleteMemory(_ context.Context, _ string, _ string) error {
	return nil
}

func TestExactMemoryLookupDoesNotFuzzyMatch(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Exact Title Alpha",
		Content:        "content",
		Origin:         OriginMycorrhizal,
		Authority:      AuthorityNone,
		Status:         StatusProposed,
		StableID:       StableMemoryIdentity("owner/repo", "Exact Title Alpha"),
	})

	// A near-match should not be found.
	_, err := exactMemoryLookup(ctx, backend, "owner/repo", "Exact Title")
	if err == nil {
		t.Fatal("expected no match for partial/fuzzy title, got nil error")
	}
}

// ======================================================================
// Evidence binding
// ======================================================================

func TestBindEvidenceSingleFile(t *testing.T) {
	dir := makeTempGitRepo(t)
	f := filepath.Join(dir, "evidence.txt")
	content := []byte("important evidence")
	if err := os.WriteFile(f, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manifest, err := BindEvidence([]string{"evidence.txt"}, dir)
	if err != nil {
		t.Fatalf("BindEvidence: %v", err)
	}
	if manifest.Version != evidenceManifestVersion {
		t.Errorf("version: want %q, got %q", evidenceManifestVersion, manifest.Version)
	}
	if len(manifest.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(manifest.Files))
	}
	if manifest.Files[0].RelativePath != "evidence.txt" {
		t.Errorf("relative path: want evidence.txt, got %q", manifest.Files[0].RelativePath)
	}
	// Verify hash is the SHA-256 of the content.
	wantHash := sha256Sum(t, content)
	if manifest.Files[0].ContentHash != wantHash {
		t.Errorf("content hash mismatch: want %q, got %q", wantHash, manifest.Files[0].ContentHash)
	}
	if manifest.ManifestHash == "" {
		t.Error("manifest hash must not be empty")
	}
}

func TestBindEvidenceMultipleFilesSortedDeterministically(t *testing.T) {
	dir := makeTempGitRepo(t)
	for _, name := range []string{"z.txt", "a.txt", "m.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+" content"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}

	// Supply paths out of order.
	manifest, err := BindEvidence([]string{"z.txt", "a.txt", "m.txt"}, dir)
	if err != nil {
		t.Fatalf("BindEvidence: %v", err)
	}
	if len(manifest.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(manifest.Files))
	}
	if manifest.Files[0].RelativePath != "a.txt" || manifest.Files[1].RelativePath != "m.txt" || manifest.Files[2].RelativePath != "z.txt" {
		t.Errorf("files not sorted: %v", manifest.Files)
	}
}

func TestBindEvidenceManifestHashIsDeterministic(t *testing.T) {
	dir := makeTempGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("fixed"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	m1, err := BindEvidence([]string{"x.txt"}, dir)
	if err != nil {
		t.Fatalf("first BindEvidence: %v", err)
	}
	m2, err := BindEvidence([]string{"x.txt"}, dir)
	if err != nil {
		t.Fatalf("second BindEvidence: %v", err)
	}
	if m1.ManifestHash != m2.ManifestHash {
		t.Errorf("manifest hash not deterministic: %q vs %q", m1.ManifestHash, m2.ManifestHash)
	}
}

func TestBindEvidenceAbsolutePathRejected(t *testing.T) {
	dir := makeTempGitRepo(t)
	_, err := BindEvidence([]string{"/etc/passwd"}, dir)
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
	if !strings.Contains(err.Error(), "absolute evidence path rejected") {
		t.Errorf("expected absolute path error, got: %v", err)
	}
}

func TestBindEvidenceTraversalRejected(t *testing.T) {
	dir := makeTempGitRepo(t)
	_, err := BindEvidence([]string{"../secret.txt"}, dir)
	if err == nil {
		t.Fatal("expected error for traversal path")
	}
	if !strings.Contains(err.Error(), "traversal rejected") {
		t.Errorf("expected traversal rejected error, got: %v", err)
	}
}

func TestBindEvidenceMissingFileRejected(t *testing.T) {
	dir := makeTempGitRepo(t)
	_, err := BindEvidence([]string{"does-not-exist.txt"}, dir)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected does not exist error, got: %v", err)
	}
}

func TestBindEvidenceDirectoryRejected(t *testing.T) {
	dir := makeTempGitRepo(t)
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	_, err := BindEvidence([]string{"subdir"}, dir)
	if err == nil {
		t.Fatal("expected error for directory")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("expected directory error, got: %v", err)
	}
}

func TestBindEvidenceSymlinkOutsideRootRejected(t *testing.T) {
	dir := makeTempGitRepo(t)
	// Create a file outside the repo and a symlink pointing to it.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile outside: %v", err)
	}
	symlink := filepath.Join(dir, "escape.txt")
	if err := os.Symlink(secret, symlink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := BindEvidence([]string{"escape.txt"}, dir)
	if err == nil {
		t.Fatal("expected error for symlink escaping root")
	}
	if !strings.Contains(err.Error(), "resolves outside substrate root") {
		t.Errorf("expected resolves outside substrate root error, got: %v", err)
	}
}

func TestBindEvidenceNoAbsoluteHostPathInResult(t *testing.T) {
	dir := makeTempGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "local.txt"), []byte("local"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	manifest, err := BindEvidence([]string{"local.txt"}, dir)
	if err != nil {
		t.Fatalf("BindEvidence: %v", err)
	}
	// The manifest must not expose the absolute host path.
	for _, f := range manifest.Files {
		if filepath.IsAbs(f.RelativePath) {
			t.Errorf("absolute host path in manifest: %q", f.RelativePath)
		}
	}
	// SourceIdentity set by ApplyBotanistAdd also must not expose abs path;
	// verify the manifest hash is used instead.
	if strings.Contains(manifest.ManifestHash, dir) {
		t.Errorf("substrate root appears in manifest hash: %s", dir)
	}
}

func TestBindEvidenceRepeatedBindingProducesIdenticalMetadata(t *testing.T) {
	dir := makeTempGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "stable.txt"), []byte("unchanged"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	m1, err := BindEvidence([]string{"stable.txt"}, dir)
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	m2, err := BindEvidence([]string{"stable.txt"}, dir)
	if err != nil {
		t.Fatalf("second bind: %v", err)
	}
	if m1.ManifestHash != m2.ManifestHash || m1.Files[0].ContentHash != m2.Files[0].ContentHash {
		t.Error("repeated binding over unchanged evidence produced different metadata")
	}
}

// TestBotanistAddWithEvidenceRecordsRevision verifies that evidence binding
// populates the revision identity from the Git repo.
func TestBotanistAddWithEvidenceRecordsRevision(t *testing.T) {
	dir := makeTempGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("important"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	mem, err := ApplyBotanistAdd(BotanistAddIntent{
		RepositoryName: "owner/repo",
		Title:          "Evidence Test",
		Content:        "Some content.",
		EvidencePaths:  []string{"note.txt"},
		SubstrateRoot:  dir,
	})
	if err != nil {
		t.Fatalf("ApplyBotanistAdd: %v", err)
	}
	if mem.RevisionIdentity == "" {
		t.Error("RevisionIdentity must be set when evidence is bound")
	}
	if mem.ContentIdentity == "" {
		t.Error("ContentIdentity must be set when evidence is bound")
	}
	if mem.RevisionMetadata == "" {
		t.Error("RevisionMetadata must contain the evidence manifest")
	}
	if mem.SourceClass == "" {
		t.Error("SourceClass must be set")
	}
	// Verify no absolute paths appear in the persisted metadata.
	if strings.Contains(mem.SourceIdentity, dir) {
		t.Errorf("absolute path leaked into SourceIdentity: %s", mem.SourceIdentity)
	}
	if strings.Contains(mem.RevisionMetadata, dir) {
		t.Errorf("absolute path leaked into RevisionMetadata: %s", mem.RevisionMetadata)
	}
}

// TestBotanistAddWithoutEvidenceRemainsRepositoryWide checks that a memory
// without evidence does not have any evidence-specific fields set.
func TestBotanistAddWithoutEvidenceRemainsRepositoryWide(t *testing.T) {
	mem, err := ApplyBotanistAdd(BotanistAddIntent{
		RepositoryName: "owner/repo",
		Title:          "No Evidence",
		Content:        "repository-wide observation",
	})
	if err != nil {
		t.Fatalf("ApplyBotanistAdd: %v", err)
	}
	if mem.RevisionMetadata != "" {
		t.Errorf("RevisionMetadata should be empty without evidence, got %q", mem.RevisionMetadata)
	}
	if mem.RevisionIdentity != "" {
		t.Errorf("RevisionIdentity should be empty without evidence, got %q", mem.RevisionIdentity)
	}
}

// ======================================================================
// Helpers
// ======================================================================

func sha256Sum(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestSupersessionStoreCallOrdering(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Ordered Old",
		Content:        "old",
		Category:       "Design",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "Ordered Old"),
	})

	_, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "Ordered Old",
		NewTitle:       "Ordered New",
		Content:        "new",
	})
	if err != nil {
		t.Fatalf("ApplySupersede: %v", err)
	}

	// The first call must be to mark "Ordered Old" as superseded.
	if len(backend.storeCallOrder) < 2 {
		t.Fatalf("expected at least 2 store calls, got %d: %v", len(backend.storeCallOrder), backend.storeCallOrder)
	}
	if backend.storeCallOrder[0] != "Ordered Old/"+string(StatusSuperseded) {
		t.Errorf("first store call must mark old item superseded; got %q", backend.storeCallOrder[0])
	}
	if backend.storeCallOrder[1] != "Ordered New/"+string(StatusEstablished) {
		t.Errorf("second store call must persist replacement; got %q", backend.storeCallOrder[1])
	}
}

// TestLegacyMemoryNotSilentlyPromoted verifies that confirm/reject/supersede
// require an explicit proposed status and cannot silently promote a
// legacy/unclassified memory via confirm.
func TestLegacyMemoryNotSilentlyPromoted(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Legacy Item",
		Content:        "old content",
		Origin:         OriginLegacy,
		Authority:      AuthorityNone,
		Status:         StatusUnclassified,
		StableID:       StableMemoryIdentity("owner/repo", "Legacy Item"),
	})

	_, err := ApplyConfirm(ctx, backend, "owner/repo", "Legacy Item")
	if err == nil {
		t.Fatal("expected error: legacy/unclassified must not be silently confirmed")
	}
	if !strings.Contains(err.Error(), "requires a proposed memory") {
		t.Errorf("expected requires a proposed memory error, got: %v", err)
	}
}

// TestSupersessionWhitespaceTitleNormalizationFailsBeforeWrite proves that when
// the requested replacement title normalizes to the same identity as the old
// title, the supersession is rejected before any backend write occurs.
//
// Concretely: old title "Rule", replacement input " Rule " must fail supersession
// because ApplyBotanistAdd trims the new title to "Rule" and the StableID
// of "Rule" == StableID of "Rule".
func TestSupersessionWhitespaceTitleNormalizationFailsBeforeWrite(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Rule",
		Content:        "Enforce rule.",
		Category:       "Design",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "Rule"),
	})

	initialCallCount := len(backend.storeCallOrder)

	_, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "Rule",
		NewTitle:       " Rule ", // normalizes to "Rule" in ApplyBotanistAdd
		Content:        "Enforce rule (updated).",
	})
	if err == nil {
		t.Fatal("expected error: replacement title ' Rule ' normalizes to 'Rule', which conflicts with old identity")
	}
	if !strings.Contains(err.Error(), "must differ") {
		t.Errorf("expected 'must differ' error, got: %v", err)
	}

	// No write must have occurred.
	if len(backend.storeCallOrder) != initialCallCount {
		t.Errorf("expected no store writes before identity check, got %v", backend.storeCallOrder[initialCallCount:])
	}
}

// TestSupersessionExistingReplacementIdentityPreventsOldMutation proves that if
// a memory with the replacement identity already exists, the old record is not
// mutated.
func TestSupersessionExistingReplacementIdentityPreventsOldMutation(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "Old Rule",
		Content:        "Old content.",
		Category:       "Design",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "Old Rule"),
	})
	// Pre-seed the replacement identity so the collision check fires.
	backend.seed(Memory{
		RepositoryName: "owner/repo",
		Title:          "New Rule",
		Content:        "Already exists.",
		Category:       "Design",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		StableID:       StableMemoryIdentity("owner/repo", "New Rule"),
	})

	initialOldStatus := backend.records["Old Rule"].Status
	initialCallCount := len(backend.storeCallOrder)

	_, err := ApplySupersede(ctx, backend, SupersedeIntent{
		RepositoryName: "owner/repo",
		OldTitle:       "Old Rule",
		NewTitle:       "New Rule",
		Content:        "Replacement content.",
	})
	if err == nil {
		t.Fatal("expected error: replacement identity already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}

	// Old record status must remain unchanged.
	if backend.records["Old Rule"].Status != initialOldStatus {
		t.Errorf("old record status changed despite collision: want %q, got %q",
			initialOldStatus, backend.records["Old Rule"].Status)
	}
	// No store writes must have occurred.
	if len(backend.storeCallOrder) != initialCallCount {
		t.Errorf("expected no store writes on collision, got %v", backend.storeCallOrder[initialCallCount:])
	}
}
