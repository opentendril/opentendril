package conductor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// ErrWorkspaceAbsent is returned when a managed substrate's checkout directory does not exist.
// This allows adapters to map it to a 409 Conflict instead of a 500 Server Error.
var ErrWorkspaceAbsent = errors.New("managed checkout is absent")

// checkoutPlan is the resolved destination for a foreign substrate clone.
// Design RFC / implementation plan, slice 4.
type checkoutPlan struct {
	// dir is the target directory. Empty means "generate an ephemeral temp dir".
	dir string
	// persistent is true for managed/path checkouts (not removed after the run).
	persistent bool
	// tendrilOwned distinguishes a directory Tendril created and maintains
	// (managed mode) from one the operator chose and edits themselves (path
	// mode). The refresh discards local state, which is correct for the
	// former and destructive for the latter.
	tendrilOwned bool
}

// resolveCheckoutPlan maps a CheckoutSpec to a destination directory + lifetime.
//   - ""/"ephemeral": a throwaway dir under os.TempDir() (removed after the run).
//   - "managed":      a persistent, Tendril-owned dir distinct from human checkouts.
//   - "path":         an explicit, persistent user-chosen path.
func resolveCheckoutPlan(name string, checkout CheckoutSpec) (checkoutPlan, error) {
	switch strings.ToLower(strings.TrimSpace(checkout.Mode)) {
	case "", "ephemeral":
		return checkoutPlan{dir: "", persistent: false}, nil
	case "managed":
		return checkoutPlan{dir: managedCheckoutDir(name), persistent: true, tendrilOwned: true}, nil
	case "path":
		p := expandHome(strings.TrimSpace(checkout.Path))
		if p == "" {
			return checkoutPlan{}, fmt.Errorf("checkout mode \"path\" requires a path")
		}
		return checkoutPlan{dir: p, persistent: true}, nil
	default:
		return checkoutPlan{}, fmt.Errorf("unknown checkout mode %q", checkout.Mode)
	}
}

// ResolveSubstrateWorkspace returns the directory the operation runs in for the given Substrate.
// It resolves the path by consulting the checkout plan.
func ResolveSubstrateWorkspace(substrate string, spec *SubstrateSpec) (string, error) {
	workspace := strings.TrimSpace(substrate)
	if workspace == "" {
		return "", fmt.Errorf("substrate is required")
	}

	if spec != nil {
		if trimmed := strings.TrimSpace(spec.Path); trimmed != "" {
			workspace = trimmed
		}
		if mode := strings.ToLower(strings.TrimSpace(spec.Checkout.Mode)); mode != "" && mode != "path" {
			plan, err := resolveCheckoutPlan(substrate, spec.Checkout)
			if err != nil {
				return "", err
			}
			if plan.dir != "" {
				workspace = plan.dir
			}
		}
	}

	managed := spec != nil && strings.ToLower(strings.TrimSpace(spec.Checkout.Mode)) == "managed"
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() || (managed && !checkoutHasGitMetadata(workspace)) {
		// Distinguish a missing or empty managed checkout (409) from a bad path (500).
		// A placeholder directory with no .git is not a checkout: mounting it
		// gives the Terrarium an empty /app. git-rev-parse walks parents, so
		// only this directory's own .git counts.
		if managed {
			return "", fmt.Errorf("%w: managed checkout for substrate %q is missing", ErrWorkspaceAbsent, substrate)
		}
		return "", fmt.Errorf("substrate %q does not resolve to a local workspace directory (operations run against a local checkout)", substrate)
	}

	return workspace, nil
}

// checkoutHasGitMetadata reports that path is itself a git checkout — a .git
// directory or file in THIS directory. git rev-parse walks parents, so an
// empty managed placeholder under another repository must not count as ready.
func checkoutHasGitMetadata(path string) bool {
	info, err := os.Stat(filepath.Join(strings.TrimSpace(path), ".git"))
	if err != nil {
		return false
	}
	return info.IsDir() || info.Mode().IsRegular()
}

// MaterializeManagedCheckouts clones or refreshes all managed substrates on startup.
// A clone failure is logged but does not prevent startup.
func MaterializeManagedCheckouts(ctx context.Context, config *SubstratesConfig) {
	if config == nil {
		log.Printf("0 managed Substrates configured")
		return
	}

	var managedCount int
	for _, spec := range config.Substrates {
		if strings.ToLower(strings.TrimSpace(spec.Checkout.Mode)) == "managed" {
			managedCount++
		}
	}

	if managedCount == 0 {
		log.Printf("0 managed Substrates configured")
		return
	}

	log.Printf("Materializing %d managed Substrates", managedCount)

	for name, spec := range config.Substrates {
		if strings.ToLower(strings.TrimSpace(spec.Checkout.Mode)) == "managed" {
			log.Printf("Materializing managed Substrate %q", name)

			dir := managedCheckoutDir(name)
			_, err := os.Stat(dir)
			isRefresh := (err == nil)

			cred, err := resolveSubstrateCredential(spec, config.Credentials)
			if err != nil {
				log.Printf("⚠️ Managed checkout materialization for substrate %q failed to resolve credentials: %v", name, err)
				continue
			}
			_, _, err = cloneNamedForeignSubstrate(name, spec.URL, spec.Branch, cred)
			if err != nil {
				log.Printf("⚠️ Managed checkout materialization for substrate %q failed: %v", name, err)
			} else {
				if isRefresh {
					log.Printf("Substrate %q refreshed", name)
				} else {
					log.Printf("Substrate %q cloned", name)
				}
			}
		}
	}
}

// managedCheckoutRoot is the Tendril-owned base for managed checkouts — deliberately
// separate from any human-editable clone. Overridable via env for tests/ops.
func managedCheckoutRoot() string {
	if v := strings.TrimSpace(os.Getenv("TENDRIL_MANAGED_CHECKOUT_ROOT")); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".tendril", "substrates")
	}
	return filepath.Join(os.TempDir(), "opentendril-managed-substrates")
}

func managedCheckoutDir(name string) string {
	return filepath.Join(managedCheckoutRoot(), sanitizeTempComponent(name))
}

// intendedLocalWorkspace is the on-disk directory a named Substrate would use,
// without requiring that directory to exist yet. Used when workspace
// resolution fails so the execution plan still does not fall back to the
// Stem working directory.
func intendedLocalWorkspace(name string, spec *SubstrateSpec) string {
	if spec == nil {
		return ""
	}
	mode := strings.ToLower(strings.TrimSpace(spec.Checkout.Mode))
	if mode != "" && mode != "path" {
		plan, err := resolveCheckoutPlan(name, spec.Checkout)
		if err == nil && strings.TrimSpace(plan.dir) != "" {
			return plan.dir
		}
	}
	if mode == "path" {
		return expandHome(strings.TrimSpace(spec.Checkout.Path))
	}
	return expandHome(strings.TrimSpace(spec.Path))
}

// uniqueConfiguredSubstrate returns the sole named Substrate when the
// configuration has exactly one. Greenhouse chat does not name a Substrate;
// on a governed install that one name is the managed checkout to use.
func uniqueConfiguredSubstrate(config *SubstratesConfig) (string, bool) {
	if config == nil || len(config.Substrates) != 1 {
		return "", false
	}
	for name := range config.Substrates {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return "", false
		}
		return trimmed, true
	}
	return "", false
}

// isUnsuitableImplicitWorkspace reports that the process working directory
// must not be used as a Substrate. The governed Stem runs from its home,
// which holds the control plane and the rootless Docker data-root. Scanning
// or bind-mounting that tree is how repo-map generation opened a containerd
// snapshot workdir, and it would also place credentials in the Terrarium.
func isUnsuitableImplicitWorkspace(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	if dockerDataRootPresent(trimmed) || controlPlanePresent(trimmed) {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return sameFilePath(trimmed, home) && !isGitRepo(trimmed)
}

func dockerDataRootPresent(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".local", "share", "docker"))
	return err == nil && info.IsDir()
}

func controlPlanePresent(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".tendril", "api-key"))
	return err == nil
}

func escapedManagedCheckout(requested, resolved string) bool {
	rootAbs, err := filepath.Abs(managedCheckoutRoot())
	if err != nil {
		return false
	}
	reqAbs, err := filepath.Abs(requested)
	if err != nil {
		return false
	}
	resAbs, err := filepath.Abs(resolved)
	if err != nil {
		return false
	}
	if !pathIsUnder(reqAbs, rootAbs) && !sameFilePath(reqAbs, rootAbs) {
		return false
	}
	return !pathIsUnder(resAbs, rootAbs) && !sameFilePath(resAbs, rootAbs)
}

func sameFilePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return filepath.Clean(absA) == filepath.Clean(absB)
}

func pathIsUnder(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ephemeralCheckoutPath returns a unique throwaway clone path under TempDir.
func ephemeralCheckoutPath(name string) (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	runID := hex.EncodeToString(buf)
	prefix := "opentendril-substrate"
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		prefix = fmt.Sprintf("%s-%s", prefix, sanitizeTempComponent(trimmed))
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%s", prefix, runID)), nil
}

// refreshExistingCheckout brings a persistent checkout up to date and clean:
// fetch, then hard-reset to the target branch. Because a foreign substrate is
// edited in place, this guarantees each run starts from a pristine tree —
// discarding any residue from a prior (e.g. read-only) run.
//
// That discarding is correct for a directory Tendril owns and maintains
// (managed mode), and destructive for one the operator chose and edits
// themselves (path mode): a hard reset there silently deletes a human's
// uncommitted work. So when the checkout is NOT Tendril-owned and has local
// changes, the refresh refuses instead, and says what it found. Losing an
// operator's work to make room for a run is never the right trade — the run
// can wait, the work cannot be recovered.
func refreshExistingCheckout(dir, branch string, gitEnv []string, tendrilOwned bool) error {
	ctx := context.Background()

	if !tendrilOwned {
		status, err := runGitCommandRawOutput(ctx, dir, "status", "--porcelain", "-uall", "-z")
		if err != nil {
			return fmt.Errorf("refresh checkout %q: %w", dir, err)
		}
		if strings.TrimSpace(strings.ReplaceAll(status, "\x00", "")) != "" {
			return fmt.Errorf("refusing to refresh %q: it is your own checkout (checkout mode \"path\") and it has uncommitted changes, which this refresh would discard — commit or set those changes aside, or point the substrate at checkout mode \"managed\" so Tendril works in its own clone", dir)
		}
	}
	// Only the network fetch needs auth (gitEnv); checkout/reset are local.
	if _, err := runGitCommandWithEnv(ctx, dir, gitEnv, "fetch", "origin"); err != nil {
		return fmt.Errorf("refresh managed checkout %q: %w", dir, err)
	}
	if strings.TrimSpace(branch) != "" {
		if _, err := runGitCommandWithEnv(ctx, dir, gitEnv, "checkout", branch); err != nil {
			return fmt.Errorf("refresh managed checkout %q: %w", dir, err)
		}
		if _, err := runGitCommandWithEnv(ctx, dir, gitEnv, "reset", "--hard", "origin/"+branch); err != nil {
			return fmt.Errorf("refresh managed checkout %q: %w", dir, err)
		}
	} else {
		// No explicit branch: best-effort reset to the tracked upstream.
		_, _ = runGitCommandWithEnv(ctx, dir, gitEnv, "reset", "--hard", "@{u}")
	}
	return nil
}
