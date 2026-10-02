//go:build !linux || (!amd64 && !arm64)

package conductor

import (
	"context"
	"fmt"
)

func lockWorkspaceAcrossProcesses(context.Context, string) (func(), error) {
	return nil, fmt.Errorf("cross-process workspace locking is unavailable on this platform")
}
