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
		wantAction    failureAction
		wantErr       error
	}{
		{
			name:          "retry mode with retries remaining",
			onFailureMode: sequenceOnFailureRetry,
			retriesLeft:   3,
			wantAction:    failureActionRetry,
			wantErr:       nil,
		},
		{
			name:          "retry mode exhausted (0 left)",
			onFailureMode: sequenceOnFailureRetry,
			retriesLeft:   0,
			wantAction:    failureActionRetry,
			wantErr:       errRetryExhausted,
		},
		{
			name:          "retry mode exhausted (negative left)",
			onFailureMode: sequenceOnFailureRetry,
			retriesLeft:   -1,
			wantAction:    failureActionRetry,
			wantErr:       errRetryExhausted,
		},
		{
			name:          "pause mode",
			onFailureMode: sequenceOnFailurePause,
			retriesLeft:   0,
			wantAction:    failureActionPause,
			wantErr:       nil,
		},
		{
			name:          "halt mode",
			onFailureMode: sequenceOnFailureHalt,
			retriesLeft:   0,
			wantAction:    failureActionHalt,
			wantErr:       nil,
		},
		{
			name:          "unknown garbage mode string",
			onFailureMode: "garbage",
			retriesLeft:   0,
			wantAction:    failureActionUnknownMode,
			wantErr:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAction, gotErr := decideFailureAction(tt.onFailureMode, tt.retriesLeft)
			if gotAction != tt.wantAction {
				t.Errorf("decideFailureAction() gotAction = %v, want %v", gotAction, tt.wantAction)
			}
			if !errors.Is(gotErr, tt.wantErr) {
				t.Errorf("decideFailureAction() gotErr = %v, want %v", gotErr, tt.wantErr)
			}
		})
	}
}
