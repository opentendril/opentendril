package conductor

import "errors"

type failureAction int

const (
	failureActionRetry failureAction = iota
	failureActionPause
	failureActionHalt
	failureActionUnknownMode
)

type failureKind string

const (
	failureKindStandard failureKind = "standard"
	failureKindTimeout  failureKind = "timeout"
)

var errRetryExhausted = errors.New("retries exhausted")

func decideFailureAction(onFailureMode string, retriesLeft int, kind failureKind) (failureAction, error) {
	switch onFailureMode {
	case sequenceOnFailureRetry:
		if kind == failureKindTimeout {
			return failureActionHalt, nil
		}
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
