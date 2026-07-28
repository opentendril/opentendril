//go:build darwin

package healthmon

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

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
