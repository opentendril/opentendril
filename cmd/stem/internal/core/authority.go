package core

import (
	"fmt"
	"strings"
	"time"
)

// Authority reads current Pollinator credentials and delegation grants from
// the Stem-owned control-plane directory for each new admission.
type Authority struct {
	controlPlaneDir string
	pendingStore    *PendingConfirmationStore
	pendingTTL      time.Duration
}

// NewAuthority creates a live authority source for the Stem control-plane
// directory. It retains no credential or grant snapshot.
func NewAuthority(controlPlaneDir string) *Authority {
	return &Authority{controlPlaneDir: strings.TrimSpace(controlPlaneDir)}
}

// WithPendingStore attaches the existing pending-confirmation store used across
// grant reloads. Reloading grants must not discard pending confirmation state.
func (a *Authority) WithPendingStore(store *PendingConfirmationStore, ttl time.Duration) *Authority {
	if a == nil {
		return nil
	}
	a.pendingStore = store
	a.pendingTTL = ttl
	return a
}

// ResolvePollinatorCredential resolves a presented durable root against the
// current credential store. Store errors are returned to the caller so that
// authentication fails closed rather than using an earlier snapshot.
func (a *Authority) ResolvePollinatorCredential(secret string) (string, error) {
	if a == nil || a.controlPlaneDir == "" {
		return "", fmt.Errorf("Stem Pollinator authority is not configured")
	}
	credentials, err := LoadPollinatorCredentials(a.controlPlaneDir)
	if err != nil {
		return "", err
	}
	return ResolvePollenFromCredential(credentials, secret), nil
}

// AuthorizeDelegation evaluates a new governed admission against the current
// grants file. A failed read returns a deny decision and an error for internal
// logging; no previously loaded grant set is retained or reused.
func (a *Authority) AuthorizeDelegation(request DelegationRequest) (DelegationDecision, error) {
	if a == nil || a.controlPlaneDir == "" {
		return delegationDenied("delegation authority is not configured"), fmt.Errorf("Stem delegation authority is not configured")
	}
	grants, err := LoadDelegationGrants(a.controlPlaneDir)
	if err != nil {
		return delegationDenied("current delegation grants could not be loaded"), err
	}
	authorizer := NewDelegationAuthorizer(grants)
	authorizer.WithPendingStore(a.pendingStore, a.pendingTTL)
	return authorizer.Authorize(request), nil
}
