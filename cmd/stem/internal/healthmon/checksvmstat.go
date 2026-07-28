package healthmon

import (
	"fmt"
	"strconv"
	"strings"
)

// parseVMStatAvailableKB approximates available memory from macOS `vm_stat`
// output as (free + inactive) pages — Darwin has no single kernel-computed
// "available" figure the way Linux's MemAvailable is. Pure string/arithmetic
// logic with no Darwin-specific dependency, kept in a platform-agnostic file
// (unlike readMemAvailableKB in checks_darwin.go) so it is actually exercised
// by `go test` on the Linux CI runners this repo has, not just cross-compiled.
func parseVMStatAvailableKB(vmStatOutput string, pageSizeBytes uint64) (uint64, error) {
	var freePages, inactivePages uint64
	var foundFree, foundInactive bool

	lines := strings.Split(vmStatOutput, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Pages free:") {
			valStr := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "Pages free:"), "."))
			val, err := strconv.ParseUint(valStr, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse Pages free: %w", err)
			}
			freePages = val
			foundFree = true
		} else if strings.HasPrefix(line, "Pages inactive:") {
			valStr := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "Pages inactive:"), "."))
			val, err := strconv.ParseUint(valStr, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse Pages inactive: %w", err)
			}
			inactivePages = val
			foundInactive = true
		}
	}

	if !foundFree || !foundInactive {
		return 0, fmt.Errorf("missing Pages free or Pages inactive in vm_stat output")
	}

	availableBytes := (freePages + inactivePages) * pageSizeBytes
	return availableBytes / 1024, nil
}
