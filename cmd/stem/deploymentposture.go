package main

import (
	"fmt"
	"os"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// stemDeploymentPosture resolves the posture from the account control plane,
// the same directory that holds delegation grants. A Substrate checkout is
// not consulted. An ambiguous or unreadable posture exits before a command
// can run.
func stemDeploymentPosture() string {
	dir, err := resolveGrantsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "deployment posture could not be resolved: %v\n", err)
		os.Exit(1)
	}
	posture, err := core.ResolveDeploymentPosture(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "deployment posture is ambiguous: %v\n", err)
		os.Exit(1)
	}
	return posture
}

func withStemDeploymentPosture(svc *core.Service) *core.Service {
	if svc == nil {
		return nil
	}
	return svc.WithDeploymentPosture(stemDeploymentPosture())
}
