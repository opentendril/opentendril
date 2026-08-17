package core

import "context"

// SproutDispatch is the phytomer identity committed as ownership evidence
// before Terrarium work. Adapters may expose it on the transport (a response
// header, an early field) so a concurrent sprout.watch can name the run.
type SproutDispatch struct {
	SessionID string
	StepID    string
}

type sproutDispatchKey struct{}

// WithSproutDispatchHook registers a per-request callback invoked only after
// dispatch ownership has been persisted. The Core does not call it; the
// execution port does, once the history write has committed.
func WithSproutDispatchHook(ctx context.Context, hook func(SproutDispatch)) context.Context {
	if ctx == nil || hook == nil {
		return ctx
	}
	return context.WithValue(ctx, sproutDispatchKey{}, hook)
}

// NotifySproutDispatch fires the hook carried on ctx, if any. Callers must
// invoke it only after the opening SproutRun row is durable — this is the
// "session ready" signal, and firing it earlier reopens the race watch
// admission is trying to close.
func NotifySproutDispatch(ctx context.Context, dispatch SproutDispatch) {
	if ctx == nil {
		return
	}
	hook, _ := ctx.Value(sproutDispatchKey{}).(func(SproutDispatch))
	if hook != nil {
		hook(dispatch)
	}
}
