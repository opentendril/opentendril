package main

import (
	"context"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

const remoteGitCompatLine = "remote-git-compat: execution-denied git-admitted"

// handleRemoteGitCompat reports whether args invoke the installer probe.
// The probe runs before dotenv loading and model discovery so installation
// does not start a Mycorrhizal provider. message is empty unless the binary
// enforces remote-git execution denial.
func handleRemoteGitCompat(args []string) (string, int, bool) {
	if len(args) == 0 || args[0] != "remote-git-compat" {
		return "", 0, false
	}
	if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
		return "", 1, true
	}
	message, code := remoteGitCompatResult(args[1])
	return message, code, true
}

// remoteGitCompatResult checks the posture recorded in controlPlaneDir.
// Success requires that posture to be remote-git, a wired Sprout run to be
// refused before it starts, an execution grant to be denied, and an existing
// git.status grant to be admitted. Any other posture prints nothing.
func remoteGitCompatResult(controlPlaneDir string) (string, int) {
	posture, err := core.ResolveDeploymentPosture(controlPlaneDir)
	if err != nil || posture != core.DeploymentPostureRemoteGit {
		return "", 1
	}
	ran := false
	service := core.NewService(nil).
		WithDeploymentPosture(posture).
		WithSprout(core.SproutOperations{
			Run: func(context.Context, core.SproutSpec) (core.SproutRunReport, error) {
				ran = true
				return core.SproutRunReport{}, nil
			},
		})
	_, sproutErr := service.SproutRun(context.Background(), core.SproutRunInput{
		Transcript: "compat",
		Substrate:  "compat",
	})
	if ran || sproutErr == nil || !strings.Contains(sproutErr.Error(), "unavailable in the remote-git deployment posture") {
		return "", 1
	}
	grant := core.DelegationGrant{
		Pollen:           "compat",
		OperationClasses: []string{core.CapSeedGrow, core.CapGitStatus},
		Substrates:       []string{"compat"},
	}
	authorizer := core.NewDelegationAuthorizer([]core.DelegationGrant{grant}).
		WithDeploymentPosture(posture)
	seed := authorizer.Authorize(core.DelegationRequest{
		Pollen:         "compat",
		OperationClass: core.CapSeedGrow,
		Substrate:      "compat",
		Impact:         core.DelegationImpactLow,
	})
	if seed.Authorized || seed.PendingConfirmation || !strings.Contains(seed.Reason, "unavailable in the remote-git deployment posture") {
		return "", 1
	}
	status := authorizer.Authorize(core.DelegationRequest{
		Pollen:         "compat",
		OperationClass: core.CapGitStatus,
		Substrate:      "compat",
		Impact:         core.DelegationImpactLow,
	})
	if !status.Authorized {
		return "", 1
	}
	return remoteGitCompatLine, 0
}
