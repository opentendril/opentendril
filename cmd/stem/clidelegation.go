package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
)

// The command line as a governed surface.
//
// When TENDRIL_POLLEN is set, an invocation is treated as delegated: the
// operation-class is authorised against the grants, the decision is audited, and
// the work runs in that Pollen's isolated workspace. Unset, nothing is gated.
//
// This is accident prevention and audit, NOT a security boundary. The Pollen is
// DECLARED BY THE CALLER: at a terminal a Pollinator owns its own environment and
// can declare any Pollen it likes. A caller running as the same operating-system
// user as the Stem can read its credentials and rewrite its grants regardless.
// The boundary is real only when the Stem runs as its own principal.

// envPollenCLI is the same variable the Model Context Protocol surface binds
// from. One name, so a Pollen means the same thing whichever surface a
// Pollinator reaches through.
const envPollenCLI = "TENDRIL_POLLEN"

// cliDelegation carries the gate for one command line invocation.
type cliDelegation struct {
	// Pollen is the declared identity, or "" when this is a plain Botanist
	// invocation.
	Pollen string
	gate   *receptors.DelegationGate
	// closers releases the audit lane in order.
	closers []func()
}

// newCLIDelegation prepares the gate. With no Pollen it returns a zero value
// that authorises nothing and gates nothing — the Botanist's path, untouched.
func newCLIDelegation(ctx context.Context) *cliDelegation {
	pollen := strings.TrimSpace(os.Getenv(envPollenCLI))
	if pollen == "" {
		return &cliDelegation{}
	}

	delegation := &cliDelegation{Pollen: pollen}

	tendrilDir := "./.tendril"
	grants, err := core.LoadDelegationGrants(tendrilDir)
	if err != nil {
		// A malformed grants file must never degrade into "no grants, so
		// nothing is delegated, so everything runs ungated". It fails loudly.
		fmt.Fprintf(os.Stderr, "❌ Delegation grants could not be loaded from %s: %v\n", filepath.Join(tendrilDir, core.DelegationGrantsFilename), err)
		os.Exit(1)
	}

	// The same EventBus and history.db sink the other surfaces use, so a
	// delegated command line decision is audited exactly like a delegated
	// request anywhere else.
	bus := eventbus.New()
	if history, historyErr := historydb.OpenFromEnv(ctx, resolveRepoRoot("")); historyErr == nil && history != nil {
		bus.AttachSink(history, 0)
		delegation.closers = append(delegation.closers, func() { bus.Shutdown() }, func() { history.Close() })
	} else {
		delegation.closers = append(delegation.closers, func() { bus.Shutdown() })
	}

	delegation.gate = &receptors.DelegationGate{
		Authorizer: core.NewDelegationAuthorizer(grants),
		Bus:        bus,
	}

	fmt.Fprintf(os.Stderr, "🔏 Pollen %q declared via %s: %d grant(s) loaded; delegated operations are authorized and audited\n",
		pollen, envPollenCLI, len(grants))
	fmt.Fprintln(os.Stderr, "   (a declared Pollen is an audit control, not a security boundary — see docs/GIT-CONNECTION-SETUP.md)")
	return delegation
}

// Close releases the audit lane, draining the sink before the database closes.
func (d *cliDelegation) Close() {
	for _, closer := range d.closers {
		closer()
	}
}

// Authorize gates one invocation. A plain Botanist invocation (no Pollen)
// passes through untouched and returns the context unchanged. A delegated one
// is authorised against the grants, audited either way, and — when permitted —
// returns a context carrying the Pollen, which is what routes the work into
// that Pollinator's isolated workspace.
//
// A denial exits non-zero rather than returning: there is no partial outcome
// worth continuing into, and a refusal that a caller could ignore is not a
// refusal.
func (d *cliDelegation) Authorize(ctx context.Context, operationClass, substrate string) context.Context {
	if d == nil || d.Pollen == "" {
		return ctx
	}
	if !core.IsDelegatedCapability(operationClass) {
		// Not a delegable operation-class: nothing to authorise, and nothing
		// is silently granted by having declared a Pollen.
		return ctx
	}

	decision := d.gate.Authorize(core.DelegationRequest{
		Pollen:         d.Pollen,
		OperationClass: operationClass,
		Substrate:      strings.TrimSpace(substrate),
	})
	if !decision.Authorized {
		fmt.Fprintf(os.Stderr, "❌ Delegation denied: %s\n", decision.Reason)
		fmt.Fprintf(os.Stderr, "   Pollen %q, operation-class %q, substrate %q\n", d.Pollen, operationClass, substrate)
		d.Close()
		os.Exit(1)
	}
	return core.WithPollen(ctx, d.Pollen)
}
