//go:build (!linux && !darwin) || ((linux || darwin) && !amd64 && !arm64)

package conductor

import "context"

func lockWorkspaceAcrossProcesses(context.Context, string) (func(), error) {
	return nil, ErrWorkspaceProcessLockUnavailable
}
