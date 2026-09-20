package conductor

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Fruit publication state is deliberately closed. The state describes what
// the Stem knows about the exact Fruit ref, not whether a Botanist later
// accepts or merges it.
const (
	FruitPublicationLocalOnly = "local-only"
	FruitPublicationPublished = "published"
	FruitPublicationFailed    = "publication-failed"
)

// FruitProvenance is the structural evidence captured at Fruit creation. The
// workspace locator is private operational evidence for later local
// verification; it is never part of a Pollinator-facing response.
type FruitProvenance struct {
	Repository       string
	Workspace        string
	Branch           string
	Commit           string
	PublicationState string
	CreatedAt        time.Time
}

// normalizeFruitRemote turns a Git remote into a credential-free, stable
// repository identity. GitHub remotes retain the canonical host/owner/repo
// shape; other remotes retain their normalized host/path shape.
func normalizeFruitRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	if !strings.Contains(raw, "://") {
		if colon := strings.IndexByte(raw, ':'); colon > 0 && !strings.HasPrefix(raw, "/") {
			host := raw[:colon]
			pathPart := raw[colon+1:]
			if at := strings.LastIndexByte(host, '@'); at >= 0 {
				host = host[at+1:]
			}
			return normalizedFruitRemoteParts(host, pathPart)
		}
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return strings.TrimSuffix(strings.TrimSpace(raw), ".git")
	}
	host := parsed.Host
	pathPart := parsed.Path
	if host == "" {
		return strings.TrimSuffix(filepath.Clean(pathPart), ".git")
	}
	return normalizedFruitRemoteParts(host, pathPart)
}

func normalizedFruitRemoteParts(host, pathPart string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	pathPart = strings.TrimSpace(strings.Trim(pathPart, "/"))
	pathPart = strings.TrimSuffix(pathPart, ".git")
	pathPart = strings.Trim(pathPart, "/")
	if host == "" {
		return pathPart
	}
	if pathPart == "" {
		return host
	}
	return host + "/" + pathPart
}

// captureFruitProvenance resolves identity from the repository that actually
// produced the commit. It deliberately does not infer identity from a branch
// name or from the mutable Substrate alias used to dispatch the run.
func captureFruitProvenance(ctx context.Context, executionPath, branch, commit, publicationState string, createdAt time.Time) (FruitProvenance, error) {
	root, err := runGitCommand(ctx, executionPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return FruitProvenance{}, fmt.Errorf("resolve Fruit repository root: %w", err)
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return FruitProvenance{}, fmt.Errorf("resolve Fruit repository root: empty path")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return FruitProvenance{}, fmt.Errorf("resolve Fruit repository path: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	root = filepath.Clean(root)

	remotes, err := runGitCommand(ctx, root, "remote")
	if err != nil {
		return FruitProvenance{}, fmt.Errorf("inspect Fruit repository remotes: %w", err)
	}
	remoteNames := strings.Fields(remotes)
	sort.Strings(remoteNames)
	if len(remoteNames) > 1 {
		for i, name := range remoteNames {
			if name == "origin" {
				remoteNames[0], remoteNames[i] = remoteNames[i], remoteNames[0]
				break
			}
		}
	}

	repository := ""
	for _, name := range remoteNames {
		remoteURL, remoteErr := runGitCommand(ctx, root, "remote", "get-url", name)
		if remoteErr != nil {
			continue
		}
		repository = normalizeFruitRemote(remoteURL)
		if repository != "" {
			break
		}
	}
	if repository == "" {
		repository = root
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return FruitProvenance{
		Repository:       repository,
		Workspace:        root,
		Branch:           strings.TrimSpace(branch),
		Commit:           strings.TrimSpace(commit),
		PublicationState: strings.TrimSpace(publicationState),
		CreatedAt:        createdAt.UTC(),
	}, nil
}
