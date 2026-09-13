package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeAuthorityGrants(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, DelegationGrantsFilename), []byte(contents), 0o600); err != nil {
		t.Fatalf("write grants: %v", err)
	}
}

func TestAuthorityResolvesCurrentPollinatorCredentials(t *testing.T) {
	dir := t.TempDir()
	secret, _, err := IssuePollinatorCredential(dir, "claude", "laptop")
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}
	authority := NewAuthority(dir)

	if pollen, err := authority.ResolvePollinatorCredential(secret); err != nil || pollen != "claude" {
		t.Fatalf("resolve before revocation = %q, %v; want claude, nil", pollen, err)
	}
	if revoked, err := RevokePollinatorCredentials(dir, "claude"); err != nil || revoked != 1 {
		t.Fatalf("revoke = %d, %v; want 1, nil", revoked, err)
	}
	if pollen, err := authority.ResolvePollinatorCredential(secret); err != nil || pollen != "" {
		t.Fatalf("resolve after revocation = %q, %v; want empty, nil", pollen, err)
	}
}

func TestAuthorityMissingCredentialStoreHasNoCredentials(t *testing.T) {
	pollen, err := NewAuthority(t.TempDir()).ResolvePollinatorCredential("tendril_refresh_unknown")
	if err != nil || pollen != "" {
		t.Fatalf("resolve from missing store = %q, %v; want empty, nil", pollen, err)
	}
}

func TestAuthorityCredentialStoreFailuresFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		breakFile func(t *testing.T, path string)
	}{
		{
			name: "malformed",
			breakFile: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
					t.Fatalf("malform credential store: %v", err)
				}
			},
		},
		{
			name: "unreadable",
			breakFile: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove credential store: %v", err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("replace credential store with directory: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			secret, _, err := IssuePollinatorCredential(dir, "claude", "")
			if err != nil {
				t.Fatalf("issue credential: %v", err)
			}
			authority := NewAuthority(dir)
			if pollen, err := authority.ResolvePollinatorCredential(secret); err != nil || pollen != "claude" {
				t.Fatalf("initial resolve = %q, %v; want claude, nil", pollen, err)
			}

			tc.breakFile(t, filepath.Join(dir, PollinatorCredentialsFilename))
			if pollen, err := authority.ResolvePollinatorCredential(secret); err == nil || pollen != "" {
				t.Fatalf("resolve from failed store = %q, %v; want empty and an error", pollen, err)
			}
		})
	}
}

func TestAuthorityAuthorizesCurrentGrants(t *testing.T) {
	dir := t.TempDir()
	writeAuthorityGrants(t, dir, "grants:\n  claude:\n    operationClasses: [sprout.grow, sprout.watch]\n    substrates: [core]\n")
	authority := NewAuthority(dir)
	grow := DelegationRequest{Pollen: "claude", OperationClass: CapSproutGrow, Substrate: "core"}
	watch := DelegationRequest{Pollen: "claude", OperationClass: CapSproutWatch, Substrate: "core"}

	if decision, err := authority.AuthorizeDelegation(grow); err != nil || !decision.Authorized {
		t.Fatalf("initial grant decision = %+v, %v; want authorized, nil", decision, err)
	}
	writeAuthorityGrants(t, dir, "grants:\n  claude:\n    operationClasses: [sprout.watch]\n    substrates: [core]\n")
	if decision, err := authority.AuthorizeDelegation(grow); err != nil || decision.Authorized {
		t.Fatalf("decision after narrowing = %+v, %v; want denied, nil", decision, err)
	}
	if decision, err := authority.AuthorizeDelegation(watch); err != nil || !decision.Authorized {
		t.Fatalf("remaining grant decision = %+v, %v; want authorized, nil", decision, err)
	}

	if err := os.Remove(filepath.Join(dir, DelegationGrantsFilename)); err != nil {
		t.Fatalf("remove grants file: %v", err)
	}
	if decision, err := authority.AuthorizeDelegation(watch); err != nil || decision.Authorized {
		t.Fatalf("decision after grant removal = %+v, %v; want denied, nil", decision, err)
	}
}

func TestAuthorityGrantReadFailuresFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		breakFile func(t *testing.T, path string)
	}{
		{
			name: "malformed",
			breakFile: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("grants: [broken"), 0o600); err != nil {
					t.Fatalf("malform grants: %v", err)
				}
			},
		},
		{
			name: "unreadable",
			breakFile: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove grants: %v", err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("replace grants with directory: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeAuthorityGrants(t, dir, "grants:\n  claude:\n    operationClasses: [sprout.grow]\n    substrates: [core]\n")
			authority := NewAuthority(dir)
			request := DelegationRequest{Pollen: "claude", OperationClass: CapSproutGrow, Substrate: "core"}
			if decision, err := authority.AuthorizeDelegation(request); err != nil || !decision.Authorized {
				t.Fatalf("initial decision = %+v, %v; want authorized, nil", decision, err)
			}

			tc.breakFile(t, filepath.Join(dir, DelegationGrantsFilename))
			if decision, err := authority.AuthorizeDelegation(request); err == nil || decision.Authorized {
				t.Fatalf("decision from failed grant state = %+v, %v; want denied and an error", decision, err)
			}
		})
	}
}

func TestAuthorityReusesPendingConfirmationStoreAcrossGrantLoads(t *testing.T) {
	dir := t.TempDir()
	writeAuthorityGrants(t, dir, "grants:\n  claude:\n    operationClasses: [sprout.grow]\n    substrates: [core]\n    confirmAbove: {impact: high}\n")
	pending := NewPendingConfirmationStore()
	authority := NewAuthority(dir).WithPendingStore(pending, time.Hour)
	request := DelegationRequest{
		Pollen:         "claude",
		OperationClass: CapSproutGrow,
		Substrate:      "core",
		Impact:         DelegationImpactHigh,
	}

	first, err := authority.AuthorizeDelegation(request)
	if err != nil || !first.PendingConfirmation || first.ConfirmationID == "" {
		t.Fatalf("initial confirmation decision = %+v, %v; want pending, nil", first, err)
	}
	if len(pending.List()) != 1 {
		t.Fatalf("pending confirmations = %d, want 1", len(pending.List()))
	}
	if err := pending.Approve(first.ConfirmationID); err != nil {
		t.Fatalf("approve pending confirmation: %v", err)
	}

	second, err := authority.AuthorizeDelegation(request)
	if err != nil || !second.Authorized || second.PendingConfirmation {
		t.Fatalf("decision after approval = %+v, %v; want authorized, nil", second, err)
	}
}
