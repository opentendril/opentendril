package core

import (
	"context"
	"strings"
)

// The Pollen — the Pollinator's bound identity — travels from the
// surface that authenticated it to the execution adapter through the request
// context, and ONLY through the request context.
//
// It is deliberately not a field on any capability input. Those structs are
// decoded from caller-supplied JSON, so a Pollen field would be one the caller
// could name — and since the Pollen decides which isolated workspace an operation
// runs in and which grants apply, a caller naming it could claim another
// subject's workspace. It is bound by the trusted launch configuration or an
// authenticated header, stamped after authorization, and unforgeable from
// request content.
//
// The Core never reads it; it passes the context to the injected port, which
// keeps the Core transport-free.

type pollenKey struct{}

// WithPollen returns a context carrying the authorized Pollen. Surfaces call this AFTER the delegation gate has authorized the
// invocation; a blank pollen is not stored, so a non-delegated call is
// indistinguishable from one that never set it.
func WithPollen(ctx context.Context, pollen string) context.Context {
	trimmed := strings.TrimSpace(pollen)
	if trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, pollenKey{}, trimmed)
}

// PollenFromContext returns the authorized Pollen, or ""
// when the invocation is not delegated (a direct command line run, for
// instance). Callers treat "" as "no isolation required": the operator's own
// workspace, which is today's behaviour for a human at a terminal.
func PollenFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	pollen, _ := ctx.Value(pollenKey{}).(string)
	return pollen
}
