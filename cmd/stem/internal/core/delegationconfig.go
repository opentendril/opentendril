package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Delegation grant storage (control-plane config lane).
//
// Grants live in the Stem's OWN .tendril directory — .tendril/grants.yaml —
// and nowhere else. Unlike substrates.yaml (credentials/connections, searched
// across candidate locations), grants are deliberately loaded from this one
// control-plane location and are never discovered inside a Substrate
// checkout: a grants file carried by a cloned repository must not be able to
// widen what that repository's Sprouts may do (no Substrate self-escalation).
// A missing file is not an error — it is the secure default: zero grants,
// delegation impossible, all non-delegated behavior unchanged.

// DelegationGrantsFilename is the grants file name inside the Stem's own
// .tendril control-plane directory.
const DelegationGrantsFilename = "grants.yaml"

// delegationGrantsFile maps .tendril/grants.yaml. Grants are keyed by
// subject — the delegation-subject / Phytomer / mesh trust-root identity
// exercising them.
type delegationGrantsFile struct {
	Grants map[string]delegationGrantSpec `yaml:"grants"`
}

// delegationGrantSpec is one subject's grant as configured.
type delegationGrantSpec struct {
	// OperationClasses allow-lists the delegable operation-classes.
	OperationClasses []string `yaml:"operationClasses"`
	// Substrates scopes the grant to named substrates.
	Substrates []string `yaml:"substrates"`
	// Egress allow-lists reachable hosts; empty means deny-all.
	Egress []string `yaml:"egress,omitempty"`
	// Expires ends the grant: an RFC 3339 timestamp or a bare YYYY-MM-DD date
	// (which expires at the start of that UTC day). Empty means no expiry.
	Expires string `yaml:"expires,omitempty"`
	// ConfirmAbove escalates invocations back to the Botanist.
	ConfirmAbove delegationConfirmSpec `yaml:"confirmAbove,omitempty"`
}

// delegationConfirmSpec bounds a grant with a human-confirmation threshold.
type delegationConfirmSpec struct {
	// Impact is "low", "medium", or "high".
	Impact string `yaml:"impact,omitempty"`
}

// LoadDelegationGrants reads the delegation grants from the Stem's
// control-plane directory (<tendrilDir>/grants.yaml). A missing file yields
// zero grants — the secure default. A malformed file is an error so a typo
// never silently loosens or reshapes policy; callers should degrade to zero
// grants (deny all delegation), never fail open.
func LoadDelegationGrants(tendrilDir string) ([]DelegationGrant, error) {
	path := filepath.Join(strings.TrimSpace(tendrilDir), DelegationGrantsFilename)

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read delegation grants %s: %w", path, err)
	}

	var file delegationGrantsFile
	if err := yaml.Unmarshal(content, &file); err != nil {
		return nil, fmt.Errorf("decode delegation grants %s: %w", path, err)
	}

	grants := make([]DelegationGrant, 0, len(file.Grants))
	for subject, spec := range file.Grants {
		grant, err := grantFromSpec(subject, spec)
		if err != nil {
			return nil, fmt.Errorf("delegation grants %s: %w", path, err)
		}
		grants = append(grants, grant)
	}

	// Map iteration is unordered; sort by subject so loading is deterministic.
	sort.Slice(grants, func(i, j int) bool { return grants[i].Subject < grants[j].Subject })
	return grants, nil
}

// grantFromSpec validates and converts one configured grant. Every grant must
// name at least one operation-class and one substrate: a grant that can match
// nothing is a configuration mistake, surfaced at load rather than silently
// carried.
func grantFromSpec(subject string, spec delegationGrantSpec) (DelegationGrant, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return DelegationGrant{}, fmt.Errorf("grant with an empty subject")
	}

	operationClasses := trimNonEmpty(spec.OperationClasses)
	if len(operationClasses) == 0 {
		return DelegationGrant{}, fmt.Errorf("grant for subject %q names no operationClasses", subject)
	}
	substrates := trimNonEmpty(spec.Substrates)
	if len(substrates) == 0 {
		return DelegationGrant{}, fmt.Errorf("grant for subject %q names no substrates", subject)
	}

	grant := DelegationGrant{
		Subject:          subject,
		OperationClasses: operationClasses,
		Substrates:       substrates,
		Egress:           trimNonEmpty(spec.Egress),
	}

	if raw := strings.TrimSpace(spec.Expires); raw != "" {
		expires, err := parseDelegationExpiry(raw)
		if err != nil {
			return DelegationGrant{}, fmt.Errorf("grant for subject %q: %w", subject, err)
		}
		grant.Expires = expires
	}

	if impact := strings.ToLower(strings.TrimSpace(spec.ConfirmAbove.Impact)); impact != "" {
		switch impact {
		case DelegationImpactLow, DelegationImpactMedium, DelegationImpactHigh:
			grant.ConfirmAboveImpact = impact
		default:
			return DelegationGrant{}, fmt.Errorf(
				"grant for subject %q: confirmAbove.impact %q is not one of low, medium, high",
				subject, spec.ConfirmAbove.Impact)
		}
	}

	return grant, nil
}

// parseDelegationExpiry accepts an RFC 3339 timestamp or a bare date.
func parseDelegationExpiry(raw string) (time.Time, error) {
	if expires, err := time.Parse(time.RFC3339, raw); err == nil {
		return expires, nil
	}
	if expires, err := time.Parse("2006-01-02", raw); err == nil {
		return expires.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("expires %q is neither an RFC 3339 timestamp nor a YYYY-MM-DD date", raw)
}

func trimNonEmpty(values []string) []string {
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		if v := strings.TrimSpace(value); v != "" {
			trimmed = append(trimmed, v)
		}
	}
	return trimmed
}
