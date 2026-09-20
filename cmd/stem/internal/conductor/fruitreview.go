package conductor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrFruitRepositoryUnavailable = errors.New("Fruit repository is unavailable")

// FruitReviewObservation is factual forge evidence for one persisted Fruit.
// The Core owns interpretation of these records.
type FruitReviewObservation struct {
	Records   []FruitReviewRecord
	Ambiguous bool
}

// FruitReviewRecord is the forge response for one pull request. CommitLookup
// proves the forge query was addressed to the persisted exact commit. A
// branch lookup is authoritative only when HeadCommit is equal to that commit.
type FruitReviewRecord struct {
	Number       int
	State        string
	Merged       bool
	HeadCommit   string
	CommitLookup bool
}

// ObserveFruitReview asks GitHub for both exact-commit and branch-head review
// evidence. The branch query is retained for closed-unmerged reviews, which
// GitHub may omit from the commit-to-pull-request association. It never
// selects a convenient PR by branch name alone.
func ObserveFruitReview(ctx context.Context, repository, branch, commit string, credential ResolvedCredential) (FruitReviewObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	identity := NormalizeFruitRepository(repository)
	if !strings.HasPrefix(strings.ToLower(identity), "github.com/") {
		return FruitReviewObservation{}, fmt.Errorf("Fruit review evidence requires a GitHub repository identity")
	}
	owner, repo, err := parseOwnerRepo(identity)
	if err != nil {
		return FruitReviewObservation{}, err
	}
	token, err := pullRequestAPIToken(ctx, credential, identity)
	if err != nil {
		return FruitReviewObservation{}, err
	}

	byCommit, err := lookupFruitPullRequestsForCommit(ctx, owner, repo, commit, token)
	if err != nil {
		return FruitReviewObservation{}, err
	}
	byHead, err := lookupFruitPullRequestsForHead(ctx, owner, repo, branch, token)
	if err != nil {
		return FruitReviewObservation{Records: fruitReviewRecords(byCommit, true)}, err
	}

	seen := make(map[int]FruitReviewRecord, len(byCommit)+len(byHead))
	ambiguous := false
	for _, pull := range byCommit {
		record := fruitReviewRecord(pull, true)
		seen[record.Number] = record
	}
	for _, pull := range byHead {
		record := fruitReviewRecord(pull, false)
		if existing, ok := seen[record.Number]; ok {
			if fruitReviewStatesConflict(existing, record) {
				ambiguous = true
			}
			if strings.TrimSpace(existing.HeadCommit) != "" && strings.TrimSpace(record.HeadCommit) != "" && !strings.EqualFold(strings.TrimSpace(existing.HeadCommit), strings.TrimSpace(record.HeadCommit)) {
				ambiguous = true
			}
			// The exact-commit query is stronger than a branch query. Preserve
			// it, but retain an exact branch head when the commit query omitted
			// it so closed-unmerged evidence can still prove head equality.
			if existing.HeadCommit == "" || strings.EqualFold(strings.TrimSpace(record.HeadCommit), strings.TrimSpace(commit)) {
				existing.HeadCommit = record.HeadCommit
			}
			seen[record.Number] = existing
			continue
		}
		seen[record.Number] = record
	}

	result := FruitReviewObservation{Records: make([]FruitReviewRecord, 0, len(seen))}
	for _, record := range seen {
		result.Records = append(result.Records, record)
	}
	sort.Slice(result.Records, func(i, j int) bool {
		if result.Records[i].Number != result.Records[j].Number {
			return result.Records[i].Number < result.Records[j].Number
		}
		if result.Records[i].State != result.Records[j].State {
			return result.Records[i].State < result.Records[j].State
		}
		return result.Records[i].HeadCommit < result.Records[j].HeadCommit
	})
	return FruitReviewObservation{Records: result.Records, Ambiguous: ambiguous}, nil
}

func fruitReviewRecord(pull forgeFruitPullRequest, commitLookup bool) FruitReviewRecord {
	return FruitReviewRecord{
		Number:       pull.Number,
		State:        pull.State,
		Merged:       strings.TrimSpace(pull.MergedAt) != "",
		HeadCommit:   pull.Head.SHA,
		CommitLookup: commitLookup,
	}
}

func fruitReviewRecords(pulls []forgeFruitPullRequest, commitLookup bool) []FruitReviewRecord {
	records := make([]FruitReviewRecord, 0, len(pulls))
	for _, pull := range pulls {
		records = append(records, fruitReviewRecord(pull, commitLookup))
	}
	return records
}

func fruitReviewStatesConflict(left, right FruitReviewRecord) bool {
	if left.Merged != right.Merged {
		return true
	}
	leftState := strings.ToLower(strings.TrimSpace(left.State))
	rightState := strings.ToLower(strings.TrimSpace(right.State))
	return leftState != "" && rightState != "" && leftState != rightState
}

type forgeFruitPullRequest struct {
	Number   int    `json:"number"`
	State    string `json:"state"`
	MergedAt string `json:"merged_at"`
	Head     struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

func lookupFruitPullRequestsForCommit(ctx context.Context, owner, repo, commit, token string) ([]forgeFruitPullRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/pulls", owner, repo, url.PathEscape(strings.TrimSpace(commit)))
	return lookupFruitPullRequests(ctx, path, token, true)
}

func lookupFruitPullRequestsForHead(ctx context.Context, owner, repo, branch, token string) ([]forgeFruitPullRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=all&head=%s", owner, repo, url.QueryEscape(owner+":"+strings.TrimSpace(branch)))
	return lookupFruitPullRequests(ctx, path, token, false)
}

func lookupFruitPullRequests(ctx context.Context, path, token string, commitLookup bool) ([]forgeFruitPullRequest, error) {
	const pageSize = 100
	const maxPages = 1000
	all := make([]forgeFruitPullRequest, 0)
	for page := 1; ; page++ {
		if page > maxPages {
			return nil, fmt.Errorf("GitHub review evidence exceeded the pagination bound")
		}
		separator := "&"
		if !strings.Contains(path, "?") {
			separator = "?"
		}
		pagePath := fmt.Sprintf("%s%sper_page=%d&page=%d", path, separator, pageSize, page)
		var pulls []forgeFruitPullRequest
		if err := githubRESTRequest(ctx, http.MethodGet, pagePath, token, nil, &pulls); err != nil {
			// GitHub uses 404/422 when a commit is not known to the repository
			// and 404 when a branch-head collection has no result. Those are
			// successful no-review answers only on the first page; later-page
			// failures remain unavailable evidence.
			if page == 1 && strings.Contains(err.Error(), " returned 404") {
				return []forgeFruitPullRequest{}, nil
			}
			if page == 1 && commitLookup && strings.Contains(err.Error(), " returned 422") {
				return []forgeFruitPullRequest{}, nil
			}
			return nil, err
		}
		all = append(all, pulls...)
		if len(pulls) < pageSize {
			return all, nil
		}
	}
}

// FruitRemoteRefObservation records the exact current remote branch ref.
// Exists is false only when the forge proves the ref is absent.
type FruitRemoteRefObservation struct {
	Exists bool
	Commit string
}

// ObserveFruitRemoteRef reads a GitHub branch ref without using ancestry,
// messages, or branch naming conventions. The caller supplies a repository
// identity already matched to the persisted Fruit claim.
func ObserveFruitRemoteRef(ctx context.Context, repository, branch string, credential ResolvedCredential) (FruitRemoteRefObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	identity := NormalizeFruitRepository(repository)
	if !strings.HasPrefix(strings.ToLower(identity), "github.com/") {
		return FruitRemoteRefObservation{}, fmt.Errorf("Fruit remote-ref evidence requires a GitHub repository identity")
	}
	owner, repo, err := parseOwnerRepo(identity)
	if err != nil {
		return FruitRemoteRefObservation{}, err
	}
	token, err := pullRequestAPIToken(ctx, credential, identity)
	if err != nil {
		return FruitRemoteRefObservation{}, err
	}
	repositoryPath := fmt.Sprintf("/repos/%s/%s", owner, repo)
	found, err := githubReadREST(ctx, repositoryPath, token, nil)
	if err != nil {
		return FruitRemoteRefObservation{}, err
	}
	if !found {
		return FruitRemoteRefObservation{}, ErrFruitRepositoryUnavailable
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	path := fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, url.PathEscape(strings.TrimSpace(branch)))
	found, err = githubReadREST(ctx, path, token, &ref)
	if err != nil {
		return FruitRemoteRefObservation{}, err
	}
	if !found {
		return FruitRemoteRefObservation{}, nil
	}
	if strings.TrimSpace(ref.Object.SHA) == "" {
		return FruitRemoteRefObservation{}, fmt.Errorf("GitHub returned an empty remote ref object")
	}
	return FruitRemoteRefObservation{Exists: true, Commit: strings.TrimSpace(ref.Object.SHA)}, nil
}

// ObserveFruitLocalCommit checks only the persisted private workspace and the
// exact persisted commit. It does not search arbitrary repositories or branch
// names.
func ObserveFruitLocalCommit(ctx context.Context, workspace, commit string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	workspace = strings.TrimSpace(workspace)
	commit = strings.TrimSpace(commit)
	if workspace == "" || commit == "" {
		return false, fmt.Errorf("Fruit local evidence requires a workspace and commit")
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("Fruit workspace is not a directory")
	}
	resolved, err := runGitCommand(ctx, workspace, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{commit}")
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(resolved), commit), nil
}

// NormalizeFruitRepository exposes the credential-free repository identity
// normalization used when Fruit provenance is captured.
func NormalizeFruitRepository(raw string) string {
	return normalizeFruitRemote(raw)
}

// FruitRepositoriesMatch compares a current Substrate resolution with the
// persisted identity. GitHub owner/repository names are case-insensitive;
// other normalized remote identities remain exact.
func FruitRepositoriesMatch(left, right string) bool {
	left = normalizeFruitRemote(left)
	right = normalizeFruitRemote(right)
	if strings.HasPrefix(strings.ToLower(left), "github.com/") || strings.HasPrefix(strings.ToLower(right), "github.com/") {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// ResolveRepositoryIdentity reads the repository identity from the given
// checkout using the same provenance machinery used at Fruit creation.
func ResolveRepositoryIdentity(ctx context.Context, workspace string) (string, error) {
	provenance, err := captureFruitProvenance(ctx, filepath.Clean(strings.TrimSpace(workspace)), "", "", "", time.Time{})
	if err != nil {
		return "", err
	}
	return provenance.Repository, nil
}
