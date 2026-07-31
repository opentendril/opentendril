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

	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		// Distinguish a missing managed checkout (409) from a bad path (500)
		if spec != nil && strings.ToLower(strings.TrimSpace(spec.Checkout.Mode)) == "managed" {
			return "", fmt.Errorf("%w: managed checkout for substrate %q is missing", ErrWorkspaceAbsent, substrate)
		}
		return "", fmt.Errorf("substrate %q does not resolve to a local workspace directory (operations run against a local checkout)", substrate)
	}

	return workspace, nil
}

// MaterializeManagedCheckouts clones or refreshes all managed substrates on startup.
// A clone failure is logged but does not prevent startup.
func MaterializeManagedCheckouts(ctx context.Context, config *SubstratesConfig) {
	for name, spec := range config.Substrates {
		if strings.ToLower(strings.TrimSpace(spec.Checkout.Mode)) == "managed" {
			cred, err := resolveSubstrateCredential(spec, config.Credentials)
			if err != nil {
				log.Printf("⚠️ Managed checkout materialization for substrate %q failed to resolve credentials: %v", name, err)
				continue
			}
			_, _, err = cloneNamedForeignSubstrate(name, spec.URL, spec.Branch, cred)
			if err != nil {
				log.Printf("⚠️ Managed checkout materialization for substrate %q failed: %v", name, err)
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
