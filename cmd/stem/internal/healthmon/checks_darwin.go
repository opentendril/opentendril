//go:build darwin

package healthmon

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

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

func readMemAvailableKB(_ string) (uint64, error) {
	outPageSize, err := exec.Command("sysctl", "-n", "hw.pagesize").Output()
	if err != nil {
		return 0, fmt.Errorf("sysctl hw.pagesize failed: %w", err)
	}
	pageSizeStr := strings.TrimSpace(string(outPageSize))
	pageSizeBytes, err := strconv.ParseUint(pageSizeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse page size: %w", err)
	}

	outVMStat, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, fmt.Errorf("vm_stat failed: %w", err)
	}

	return parseVMStatAvailableKB(string(outVMStat), pageSizeBytes)
}
