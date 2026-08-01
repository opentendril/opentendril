package conductor

import (
	"errors"
	"testing"
)

func TestDecideFailureAction(t *testing.T) {
	tests := []struct {
		name          string
		onFailureMode string
		retriesLeft   int
		kind          failureKind
		wantAction    failureAction
		wantErr       error
	}{
		{
			name:          "retry mode with retries remaining",
			onFailureMode: sequenceOnFailureRetry,
			retriesLeft:   3,
			kind:          failureKindStandard,
			wantAction:    failureActionRetry,
			wantErr:       nil,
		},
		{
			name:          "retry mode exhausted (0 left)",
			onFailureMode: sequenceOnFailureRetry,
			retriesLeft:   0,
			kind:          failureKindStandard,
			wantAction:    failureActionRetry,
			wantErr:       errRetryExhausted,
		},
		{
			name:          "retry mode exhausted (negative left)",
			onFailureMode: sequenceOnFailureRetry,
			retriesLeft:   -1,
			kind:          failureKindStandard,
			wantAction:    failureActionRetry,
			wantErr:       errRetryExhausted,
		},
		{
			name:          "pause mode",
			onFailureMode: sequenceOnFailurePause,
			retriesLeft:   0,
			kind:          failureKindStandard,
			wantAction:    failureActionPause,
			wantErr:       nil,
		},
		{
			name:          "halt mode",
			onFailureMode: sequenceOnFailureHalt,
			retriesLeft:   0,
			kind:          failureKindStandard,
			wantAction:    failureActionHalt,
			wantErr:       nil,
		},
		{
			name:          "unknown garbage mode string",
			onFailureMode: "garbage",
			retriesLeft:   0,
			kind:          failureKindStandard,
			wantAction:    failureActionUnknownMode,
			wantErr:       nil,
		},
		{
			name:          "timeout under retry returns halt with budget intact",
			onFailureMode: sequenceOnFailureRetry,
			retriesLeft:   3,
			kind:          failureKindTimeout,
			wantAction:    failureActionHalt,
			wantErr:       nil,
		},
		{
			name:          "timeout under pause still pauses",
			onFailureMode: sequenceOnFailurePause,
			retriesLeft:   0,
			kind:          failureKindTimeout,
			wantAction:    failureActionPause,
			wantErr:       nil,
		},
		{
			name:          "timeout under halt still halts",
			onFailureMode: sequenceOnFailureHalt,
			retriesLeft:   0,
			kind:          failureKindTimeout,
			wantAction:    failureActionHalt,
			wantErr:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAction, gotErr := decideFailureAction(tt.onFailureMode, tt.retriesLeft, tt.kind)
			if gotAction != tt.wantAction {
				t.Errorf("decideFailureAction() gotAction = %v, want %v", gotAction, tt.wantAction)
			}
			if !errors.Is(gotErr, tt.wantErr) {
				t.Errorf("decideFailureAction() gotErr = %v, want %v", gotErr, tt.wantErr)
			}
		})
	}
}
