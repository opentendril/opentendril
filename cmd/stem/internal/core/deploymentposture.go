package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Deployment posture is a Stem Core property. install.sh records the same
// file name and environment variable for the remote-git unit. Adapters do
// not decide this policy.

const (
	DeploymentPostureFull      = "full"
	DeploymentPostureRemoteGit = "remote-git"
	DeploymentPostureFilename  = "deployment-posture"
	DeploymentPostureEnv       = "TENDRIL_DEPLOYMENT_POSTURE"
)

// IsGovernedGitCapability reports whether name is one of the existing governed
// Git capabilities. It is not a second registry: the names are the registry
// constants, and a capability that is not one of those constants is not Git.
func IsGovernedGitCapability(name string) bool {
	switch strings.TrimSpace(name) {
	case CapGitCommit, CapGitPush, CapGitPR, CapGitPRUpdate, CapGitBranch,
		CapGitStatus, CapGitBranchList, CapGitPrune, CapGitApply, CapGitFetch:
		return true
	default:
		return false
	}
}

// ResolveDeploymentPosture agrees the process environment with the posture
// file in the Stem control plane. An empty directory means the file is
// absent. A missing file and an empty environment are the full posture, so
// existing installations keep their current behavior. Any other combination
// is an error and must fail closed.
func ResolveDeploymentPosture(controlPlaneDir string) (string, error) {
	env := strings.TrimRight(os.Getenv(DeploymentPostureEnv), "\r\n")
	fileValue, present, err := readDeploymentPostureFile(controlPlaneDir)
	if err != nil {
		return "", err
	}
	switch {
	case env == "" && !present:
		return DeploymentPostureFull, nil
	case env == "" && fileValue == DeploymentPostureRemoteGit:
		return DeploymentPostureRemoteGit, nil
	case env == DeploymentPostureRemoteGit && present && fileValue == DeploymentPostureRemoteGit:
		return DeploymentPostureRemoteGit, nil
	default:
		return "", fmt.Errorf("deployment posture is ambiguous")
	}
}

func readDeploymentPostureFile(controlPlaneDir string) (string, bool, error) {
	if strings.TrimSpace(controlPlaneDir) == "" {
		return "", false, nil
	}
	path := filepath.Join(controlPlaneDir, DeploymentPostureFilename)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("deployment posture could not be read: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("deployment posture is not a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("deployment posture could not be read: %w", err)
	}
	return strings.TrimRight(string(raw), "\r\n"), true, nil
}

// postureDeniesExecution is the authoritative remote-git rule. Empty and full
// preserve every existing capability. remote-git, and any other recorded
// value, refuses every capability that is not governed Git.
func postureDeniesExecution(posture, operationClass string) bool {
	switch posture {
	case "", DeploymentPostureFull:
		return false
	default:
		return !IsGovernedGitCapability(operationClass)
	}
}

func remoteGitPostureError(capability string) error {
	return fmt.Errorf("%s is unavailable in the remote-git deployment posture", capability)
}

func (s *Service) refuseRemoteGitExecution(capability string) error {
	if s == nil || !postureDeniesExecution(s.deploymentPosture, capability) {
		return nil
	}
	return remoteGitPostureError(capability)
}
