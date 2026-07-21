package conductor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Owned references: every branch Tendril creates is recorded at creation and
// reclaimed when its purpose ends.
//
// This exists because the system was littering. A run would create a
// protective branch (docker.go's sprout/task-<id>) and abandon it; nothing
// ever removed it. Worktrees were already reclaimed properly — the deferred
// removeShadowWorktree calls in sequence.go and parallelsprouting.go — so the
// pattern was understood, it had simply never been applied to branches.
//
// The result was measurable: branches left behind by finished runs, authored
// by the automation identity, still present after a full manual cleanup. And
// they were unreachable by git.prune, because they had never been pushed and
// no remote could vouch for them — so the cleanup capability was structurally
// incapable of clearing the mess the system itself produced. That is the sign
// of a symptom being treated instead of a cause.
//
// The cause is unowned creation. A reference created without a recorded
// purpose has no moment at which anyone can say it is finished, so it survives
// forever by default. Recording ownership at creation gives it that moment.

// ownedRefsFileName is the registry, kept in the Stem's own directory — never
// inside a substrate, so a repository can neither observe nor rewrite what
// Tendril owns.
const ownedRefsFileName = "owned-refs.json"

// ownedRefsMu serializes registry reads and writes within the process. The
// registry is small and written rarely; correctness matters more than
// concurrency here.
var ownedRefsMu sync.Mutex

// OwnedRefPurpose describes why a reference exists, which is what makes
// reclamation decidable later.
type OwnedRefPurpose string

const (
	// PurposeDelegatedWorkspace is a branch created to give a delegation
	// subject somewhere to work.
	PurposeDelegatedWorkspace OwnedRefPurpose = "delegated-workspace"
	// PurposeSproutIsolation is a branch created to keep a Sprout run off the
	// default branch.
	PurposeSproutIsolation OwnedRefPurpose = "sprout-isolation"
)

// OwnedRef is one reference Tendril created and is responsible for.
type OwnedRef struct {
	// Repository is the substrate checkout the branch lives in.
	Repository string `json:"repository"`
	// Branch is the reference itself.
	Branch string `json:"branch"`
	// Purpose is why it was created.
	Purpose OwnedRefPurpose `json:"purpose"`
	// Subject is the delegation subject it belongs to, when it has one.
	Subject string `json:"subject,omitempty"`
	// Base is the commit the branch was cut from, which is what makes "has
	// this produced anything yet" answerable without a network call.
	Base string `json:"base,omitempty"`
	// CreatedAt records when, so an operator can see the age of anything left
	// behind.
	CreatedAt time.Time `json:"createdAt"`
}

// ownedRefsPath returns the registry path.
func ownedRefsPath() string {
	return filepath.Join(expandHome("~/.tendril"), ownedRefsFileName)
}

// loadOwnedRefs reads the registry. A missing or unreadable registry yields an
// empty list rather than an error: losing track of ownership must never block
// the work itself, and the worst case is the pre-existing behaviour of a
// reference nobody reclaims.
func loadOwnedRefs() []OwnedRef {
	data, err := os.ReadFile(ownedRefsPath())
	if err != nil {
		return nil
	}
	var refs []OwnedRef
	if err := json.Unmarshal(data, &refs); err != nil {
		return nil
	}
	return refs
}

// saveOwnedRefs writes the registry atomically, so an interrupted write cannot
// leave a half-registry that loses ownership of live branches.
func saveOwnedRefs(refs []OwnedRef) error {
	path := ownedRefsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create Tendril directory: %w", err)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Repository != refs[j].Repository {
			return refs[i].Repository < refs[j].Repository
		}
		return refs[i].Branch < refs[j].Branch
	})
	data, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// RegisterOwnedRef records a reference Tendril has created. Registration is
// the act that makes reclamation possible later, so it happens at creation and
// never afterwards.
func RegisterOwnedRef(ref OwnedRef) error {
	if strings.TrimSpace(ref.Repository) == "" || strings.TrimSpace(ref.Branch) == "" {
		return fmt.Errorf("an owned reference needs both a repository and a branch")
	}
	if ref.CreatedAt.IsZero() {
		ref.CreatedAt = time.Now().UTC()
	}
	ref.Repository = filepath.Clean(ref.Repository)

	ownedRefsMu.Lock()
	defer ownedRefsMu.Unlock()

	refs := loadOwnedRefs()
	for i, existing := range refs {
		if existing.Repository == ref.Repository && existing.Branch == ref.Branch {
			refs[i] = ref
			return saveOwnedRefs(refs)
		}
	}
	return saveOwnedRefs(append(refs, ref))
}

// ForgetOwnedRef drops a reference from the registry, after it has been
// reclaimed or once it is no longer Tendril's responsibility.
func ForgetOwnedRef(repository, branch string) error {
	ownedRefsMu.Lock()
	defer ownedRefsMu.Unlock()

	repository = filepath.Clean(repository)
	refs := loadOwnedRefs()
	kept := refs[:0]
	for _, ref := range refs {
		if ref.Repository == repository && ref.Branch == branch {
			continue
		}
		kept = append(kept, ref)
	}
	return saveOwnedRefs(kept)
}

// OwnedRefsFor returns the references Tendril owns in a repository.
func OwnedRefsFor(repository string) []OwnedRef {
	ownedRefsMu.Lock()
	defer ownedRefsMu.Unlock()

	repository = filepath.Clean(repository)
	var out []OwnedRef
	for _, ref := range loadOwnedRefs() {
		if ref.Repository == repository {
			out = append(out, ref)
		}
	}
	return out
}
