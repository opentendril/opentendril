package conductor

import "errors"

type failureAction int

const (
	failureActionRetry failureAction = iota
	failureActionPause
	failureActionHalt
	failureActionUnknownMode
)

var errRetryExhausted = errors.New("retries exhausted")

func decideFailureAction(onFailureMode string, retriesLeft int) (failureAction, error) {
	switch onFailureMode {
	case sequenceOnFailureRetry:
		if retriesLeft > 0 {
			return failureActionRetry, nil
		}
		return failureActionRetry, errRetryExhausted
	case sequenceOnFailurePause:
		return failureActionPause, nil
	case sequenceOnFailureHalt:
		return failureActionHalt, nil
	default:
		return failureActionUnknownMode, nil
	}
}
