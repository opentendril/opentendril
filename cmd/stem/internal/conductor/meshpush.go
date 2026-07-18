package conductor

import (
	"context"

	"github.com/opentendril/opentendril/cmd/stem/internal/mesh"
)

func delegateGitPushIfConfigured(ctx context.Context, workspace, branch, commitMessage string) (bool, error) {
	client := mesh.NewClientFromEnv()
	if client == nil {
		return false, nil
	}

	_, err := client.DelegatePush(ctx, workspace, branch, commitMessage)
	if err != nil {
		return true, err
	}

	return true, nil
}
