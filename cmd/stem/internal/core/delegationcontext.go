package core

import (
	"context"
	"strings"
)

// The delegation subject travels from the surface that authenticated it to the
// execution adapter through the request context, and ONLY through the request
// context.
//
// It is deliberately not a field on any capability input. Those structs are
// decoded from agent-supplied JSON, so a subject field would be a subject an
// agent could name — and since the subject decides which isolated workspace an
// operation runs in (and which grants apply to it), an agent that could name it
// could claim another agent's workspace. The subject is bound by the trusted
// launch configuration (the Model Context Protocol connection) or by an
// authenticated header (the Representational State Transfer surface), stamped
// here after authorization, and is unforgeable from request content.
//
// The Core itself never reads it: it passes the context through to the
// injected execution port, exactly as it passes the context to any other port.
// That keeps the Core transport-free — a context value carries no transport
// type — while letting the adapter layer resolve a per-subject workspace.

type delegationSubjectKey struct{}

// WithDelegationSubject returns a context carrying the authorized delegation
// subject. Surfaces call this AFTER the delegation gate has authorized the
// invocation; a blank subject is not stored, so a non-delegated call is
// indistinguishable from one that never set it.
func WithDelegationSubject(ctx context.Context, subject string) context.Context {
	trimmed := strings.TrimSpace(subject)
	if trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, delegationSubjectKey{}, trimmed)
}

// DelegationSubjectFromContext returns the authorized delegation subject, or ""
// when the invocation is not delegated (a direct command line run, for
// instance). Callers treat "" as "no isolation required": the operator's own
// workspace, which is today's behaviour for a human at a terminal.
func DelegationSubjectFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	subject, _ := ctx.Value(delegationSubjectKey{}).(string)
	return subject
}
