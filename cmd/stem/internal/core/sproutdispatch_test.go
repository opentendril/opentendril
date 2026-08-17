package core_test

import (
	"context"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestNotifySproutDispatchFiresRegisteredHook(t *testing.T) {
	var got core.SproutDispatch
	ctx := core.WithSproutDispatchHook(context.Background(), func(dispatch core.SproutDispatch) {
		got = dispatch
	})

	core.NotifySproutDispatch(ctx, core.SproutDispatch{SessionID: "tendril-1", StepID: "step-1"})
	if got.SessionID != "tendril-1" || got.StepID != "step-1" {
		t.Fatalf("hook received %+v, want tendril-1/step-1", got)
	}
}

func TestNotifySproutDispatchIsNoopWithoutHook(t *testing.T) {
	core.NotifySproutDispatch(context.Background(), core.SproutDispatch{SessionID: "tendril-1"})
	core.NotifySproutDispatch(nil, core.SproutDispatch{SessionID: "tendril-1"})
}
