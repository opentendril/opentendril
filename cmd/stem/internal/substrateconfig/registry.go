// Package substrateconfig owns persistent mutation of the Stem's Substrate
// registry. It is a control-plane implementation package, not a governed
// capability.
package substrateconfig

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"gopkg.in/yaml.v3"
)

// NodeMutator changes a parsed top-level Substrate registry mapping. The
// mutation is durable only if the resulting complete document validates.
type NodeMutator func(root *yaml.Node) error

// MutationResult describes the source and destination selected by a registry
// mutation.
type MutationResult struct {
	Destination    string
	Source         string
	LegacyPath     string
	Created        bool
	ImportedLegacy bool
	LegacyIgnored  bool
}

// CanonicalPath returns the account-global mutable registry path.
func CanonicalPath() (string, error) {
	return conductor.CanonicalSubstrateConfigPath()
}

// MutateCanonical applies a node mutation to the canonical registry. When the
// canonical registry is absent, a valid legacy home-root registry is imported
// into the in-memory node before the mutation; the legacy file is never
// changed.
func MutateCanonical(mutator NodeMutator) (MutationResult, error) {
	path, err := CanonicalPath()
	if err != nil {
		return MutationResult{}, err
	}
	return Mutate(path, mutator)
}

// Mutate applies a node mutation to path. The canonical path receives the
// bounded legacy-import behavior; explicit alternate paths use only their own
// existing content or a fresh empty mapping.
func Mutate(path string, mutator NodeMutator) (MutationResult, error) {
	if mutator == nil {
		return MutationResult{}, fmt.Errorf("substrate registry mutation is required")
	}
	if strings.TrimSpace(path) == "" {
		return MutationResult{}, fmt.Errorf("substrate registry path is required")
	}

	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return MutationResult{}, fmt.Errorf("resolve substrate registry path %q: %w", path, err)
	}
	canonical, err := CanonicalPath()
	if err != nil {
		return MutationResult{}, err
	}
	canonical, err = filepath.Abs(filepath.Clean(canonical))
	if err != nil {
		return MutationResult{}, fmt.Errorf("resolve canonical substrate registry path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return MutationResult{}, fmt.Errorf("create substrate registry directory %s: %w", filepath.Dir(path), err)
	}

	result := MutationResult{Destination: path}
	var (
		root *yaml.Node
		mode os.FileMode = 0o644
	)

	info, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		if info.IsDir() {
			return result, fmt.Errorf("substrate registry %s is a directory", path)
		}
		if !info.Mode().IsRegular() {
			return result, fmt.Errorf("substrate registry %s is not a regular file", path)
		}
		mode = info.Mode().Perm()
		content, err := os.ReadFile(path)
		if err != nil {
			return result, fmt.Errorf("read substrate registry %s: %w", path, err)
		}
		root, err = parseAndValidate(path, content)
		if err != nil {
			return result, err
		}
		if path == canonical {
			if legacy, legacyErr := conductor.LegacySubstrateConfigPath(); legacyErr == nil && regularFileExists(legacy) {
				result.LegacyPath = legacy
				result.LegacyIgnored = true
			}
		}
	case os.IsNotExist(statErr):
		if path == canonical {
			legacy, legacyErr := conductor.LegacySubstrateConfigPath()
			if legacyErr != nil {
				return result, legacyErr
			}
			result.LegacyPath = legacy
			if regularFileExists(legacy) {
				content, err := os.ReadFile(legacy)
				if err != nil {
					return result, fmt.Errorf("read legacy substrate registry %s: %w", legacy, err)
				}
				root, err = parseAndValidate(legacy, content)
				if err != nil {
					return result, err
				}
				result.Source = legacy
				result.ImportedLegacy = true
			} else {
				root = emptyMapping()
				result.Created = true
			}
		} else {
			root = emptyMapping()
			result.Created = true
		}
	default:
		return result, fmt.Errorf("stat substrate registry %s: %w", path, statErr)
	}

	if err := mutator(root); err != nil {
		return result, err
	}

	content, err := marshalMapping(root)
	if err != nil {
		return result, fmt.Errorf("encode substrate registry %s: %w", path, err)
	}
	if _, err := parseAndValidate(path, content); err != nil {
		return result, fmt.Errorf("validate substrate registry %s: %w", path, err)
	}

	if err := atomicReplace(path, content, mode); err != nil {
		return result, err
	}
	return result, nil
}

func emptyMapping() *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode}
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func parseAndValidate(path string, content []byte) (*yaml.Node, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return nil, fmt.Errorf("decode substrate registry %s: %w", path, err)
	}
	if len(document.Content) == 0 {
		return emptyMapping(), nil
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("substrate registry %s: expected a top-level mapping", path)
	}

	var config conductor.SubstratesConfig
	if err := yaml.Unmarshal(content, &config); err != nil {
		return nil, fmt.Errorf("decode substrate registry %s: %w", path, err)
	}
	if err := conductor.ValidateSubstratesConfig(path, &config); err != nil {
		return nil, err
	}
	return root, nil
}

func marshalMapping(root *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		_ = encoder.Close()
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func atomicReplace(path string, content []byte, mode os.FileMode) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create substrate registry directory %s: %w", directory, err)
	}

	temporary, err := os.CreateTemp(directory, ".substrates.yaml-*")
	if err != nil {
		return fmt.Errorf("create temporary substrate registry in %s: %w", directory, err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()

	if err := temporary.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("set substrate registry mode %s: %w", temporaryPath, err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write temporary substrate registry %s: %w", temporaryPath, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary substrate registry %s: %w", temporaryPath, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary substrate registry %s: %w", temporaryPath, err)
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace substrate registry %s: %w", path, err)
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open substrate registry directory %s after replacement: %w", directory, err)
	}
	syncErr := directoryFile.Sync()
	closeErr := directoryFile.Close()
	if syncErr != nil {
		return fmt.Errorf("sync substrate registry directory %s: %w", directory, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close substrate registry directory %s: %w", directory, closeErr)
	}
	return nil
}
