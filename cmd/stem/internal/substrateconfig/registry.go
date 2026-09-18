// Package substrateconfig owns persistent mutation of the Stem's Substrate
// registry. It is a control-plane implementation package, not a governed
// capability.
package substrateconfig

import (
	"bytes"
	"fmt"
	"io"
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

// ReadResult describes the active canonical/default registry without resolving
// any credential material. Source is the file that was selected; Target is the
// canonical destination used when no registry exists yet.
type ReadResult struct {
	Config *conductor.SubstratesConfig
	Source string
	Target string
}

// AddRequest contains the Botanist-controlled values needed to add one
// Substrate and its deterministic connection profile.
type AddRequest struct {
	Name          string
	Repo          string
	Posture       string
	PostureSet    bool
	AppID         string
	KeyPath       string
	TokenEnv      string
	SignKey       string
	IdentityName  string
	IdentityEmail string
	Checkout      string
	CheckoutPath  string
	CheckoutSet   bool
	PathSet       bool
	Branch        string
	BranchSet     bool
}

// UpdateRequest describes the supported no-credential-mutation patch surface.
// The boolean fields preserve the distinction between an omitted flag and an
// explicitly supplied empty value.
type UpdateRequest struct {
	Name         string
	Repo         string
	RepoSet      bool
	Branch       string
	BranchSet    bool
	Checkout     string
	CheckoutSet  bool
	CheckoutPath string
	PathSet      bool
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

// LoadCanonical reads the ordinary canonical/default registry. It intentionally
// decodes only stored configuration and validates patience bounds; it does not
// resolve PAT values, read key files, or mint App installation tokens.
func LoadCanonical() (ReadResult, error) {
	canonical, err := CanonicalPath()
	if err != nil {
		return ReadResult{}, err
	}
	candidates := conductor.SubstrateConfigCandidates("")
	if len(candidates) == 0 {
		return ReadResult{}, fmt.Errorf("resolve canonical/default substrate registry paths")
	}

	result := ReadResult{Target: canonical}
	for index, candidate := range candidates {
		config, exists, err := readStoredCandidate(candidate, index == 0)
		if err != nil {
			return result, err
		}
		if !exists {
			continue
		}
		result.Config = config
		result.Source = candidate
		return result, nil
	}

	result.Config = &conductor.SubstratesConfig{}
	return result, nil
}

func readStoredCandidate(path string, canonical bool) (*conductor.SubstratesConfig, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("stat substrate registry %s: %w", path, err)
	}
	if info.IsDir() {
		if canonical {
			return nil, false, fmt.Errorf("canonical substrate registry %s is a directory", path)
		}
		return nil, false, nil
	}
	if !info.Mode().IsRegular() {
		if canonical {
			return nil, false, fmt.Errorf("canonical substrate registry %s is not a regular file", path)
		}
		return nil, false, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("open substrate registry %s: %w", path, err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, false, fmt.Errorf("read substrate registry %s: %w", path, err)
	}
	config, err := parseStoredConfig(path, content)
	if err != nil {
		return nil, false, err
	}
	return config, true, nil
}

func parseStoredConfig(path string, content []byte) (*conductor.SubstratesConfig, error) {
	_, config, err := parseValidated(path, content)
	return config, err
}

// AddCanonical validates and atomically adds a Substrate and its connection
// profile to the canonical registry.
func AddCanonical(request AddRequest) (MutationResult, error) {
	if err := validateAddRequest(&request); err != nil {
		return MutationResult{}, err
	}
	return MutateCanonical(func(root *yaml.Node) error {
		return addNode(root, request)
	})
}

// UpdateCanonical validates and atomically applies a supported Substrate patch
// to the canonical registry.
func UpdateCanonical(request UpdateRequest) (MutationResult, error) {
	if err := validateUpdateRequest(&request); err != nil {
		return MutationResult{}, err
	}
	return MutateCanonical(func(root *yaml.Node) error {
		return updateNode(root, request)
	})
}

func validateAddRequest(request *AddRequest) error {
	request.Name = strings.TrimSpace(request.Name)
	request.Repo = strings.TrimSpace(request.Repo)
	request.Posture = strings.ToLower(strings.TrimSpace(request.Posture))
	request.AppID = strings.TrimSpace(request.AppID)
	request.KeyPath = strings.TrimSpace(request.KeyPath)
	request.TokenEnv = strings.TrimSpace(request.TokenEnv)
	request.SignKey = strings.TrimSpace(request.SignKey)
	request.IdentityName = strings.TrimSpace(request.IdentityName)
	request.IdentityEmail = strings.TrimSpace(request.IdentityEmail)
	request.Checkout = strings.ToLower(strings.TrimSpace(request.Checkout))
	request.CheckoutPath = strings.TrimSpace(request.CheckoutPath)
	request.Branch = strings.TrimSpace(request.Branch)
	if request.Name == "" {
		return fmt.Errorf("Substrate name is required")
	}
	if err := validateRepo(request.Repo); err != nil {
		return err
	}
	if request.Posture == "" && request.PostureSet {
		return fmt.Errorf("--posture must be app or pat")
	}
	if request.Posture == "" {
		request.Posture = "app"
	}
	if request.Posture != string(conductor.CredentialApp) && request.Posture != string(conductor.CredentialPAT) {
		return fmt.Errorf("--posture must be app or pat, got %q", request.Posture)
	}
	if request.Checkout == "" && request.CheckoutSet {
		return fmt.Errorf("--checkout must be managed, path, or ephemeral")
	}
	if request.Checkout == "" {
		request.Checkout = "managed"
	}
	if err := validateCheckout(request.Checkout); err != nil {
		return err
	}
	if request.Checkout == "path" && (!request.PathSet || request.CheckoutPath == "") {
		return fmt.Errorf("--checkout path requires --path")
	}
	if request.Checkout != "path" && request.PathSet {
		return fmt.Errorf("--path is only valid with --checkout path")
	}
	if request.BranchSet && request.Branch == "" {
		return fmt.Errorf("--branch must be non-empty")
	}
	if request.Posture == string(conductor.CredentialApp) {
		if request.AppID == "" || request.KeyPath == "" {
			return fmt.Errorf("posture app requires --app-id and --key <pem path>")
		}
	} else {
		if request.TokenEnv == "" {
			request.TokenEnv = "GITHUB_TOKEN"
		}
		if request.SignKey == "" {
			return fmt.Errorf("posture pat requires --sign-key <gpg key id>")
		}
		if request.IdentityName == "" || request.IdentityEmail == "" {
			return fmt.Errorf("posture pat requires --identity-name and --identity-email")
		}
	}
	return nil
}

func validateUpdateRequest(request *UpdateRequest) error {
	request.Name = strings.TrimSpace(request.Name)
	request.Repo = strings.TrimSpace(request.Repo)
	request.Branch = strings.TrimSpace(request.Branch)
	request.Checkout = strings.ToLower(strings.TrimSpace(request.Checkout))
	request.CheckoutPath = strings.TrimSpace(request.CheckoutPath)
	if request.Name == "" {
		return fmt.Errorf("Substrate name is required")
	}
	if !request.RepoSet && !request.BranchSet && !request.CheckoutSet && !request.PathSet {
		return fmt.Errorf("at least one update flag is required")
	}
	if request.RepoSet {
		if err := validateRepo(request.Repo); err != nil {
			return err
		}
	}
	if request.BranchSet && request.Branch == "" {
		return fmt.Errorf("--branch must be non-empty")
	}
	if request.CheckoutSet {
		if err := validateCheckout(request.Checkout); err != nil {
			return err
		}
		if request.Checkout == "path" && (!request.PathSet || request.CheckoutPath == "") {
			return fmt.Errorf("changing checkout to path requires --path")
		}
		if request.Checkout != "path" && request.PathSet {
			return fmt.Errorf("--path is invalid with --checkout %s", request.Checkout)
		}
	} else if request.PathSet && request.CheckoutPath == "" {
		return fmt.Errorf("--path must be non-empty")
	}
	return nil
}

func validateRepo(repo string) error {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("--repo must be owner/repository with non-empty owner and repository components")
	}
	if strings.ContainsAny(parts[0], " \t\r\n") || strings.ContainsAny(parts[1], " \t\r\n") {
		return fmt.Errorf("--repo must be owner/repository without whitespace")
	}
	return nil
}

func validateCheckout(mode string) error {
	switch mode {
	case "managed", "path", "ephemeral":
		return nil
	default:
		return fmt.Errorf("--checkout must be managed, path, or ephemeral, got %q", mode)
	}
}

func addNode(root *yaml.Node, request AddRequest) error {
	credentials, err := ensureSection(root, "credentials")
	if err != nil {
		return err
	}
	substrates, err := ensureSection(root, "substrates")
	if err != nil {
		return err
	}
	profileName := request.Name + "-connection"
	if _, existing := mappingEntry(credentials, profileName); existing != nil {
		return fmt.Errorf("credential profile %q already exists", profileName)
	}
	if _, existing := mappingEntry(substrates, request.Name); existing != nil {
		return fmt.Errorf("Substrate %q already exists", request.Name)
	}

	profile := emptyMapping()
	auth := emptyMapping()
	if request.Posture == "app" {
		setScalar(auth, "method", "app")
		setScalar(auth, "appId", request.AppID)
		setScalar(auth, "privateKeyPath", request.KeyPath)
		setMapEntry(profile, "auth", auth)
		setScalar(profile, "commit", conductor.CommitModeAPI)
	} else {
		setScalar(auth, "method", "pat")
		setScalar(auth, "env", request.TokenEnv)
		setMapEntry(profile, "auth", auth)
		sign := emptyMapping()
		setScalar(sign, "method", "gpg")
		setScalar(sign, "key", request.SignKey)
		setMapEntry(profile, "sign", sign)
		identity := emptyMapping()
		setScalar(identity, "name", request.IdentityName)
		setScalar(identity, "email", request.IdentityEmail)
		setMapEntry(profile, "identity", identity)
	}
	setMapEntry(credentials, profileName, profile)

	spec := emptyMapping()
	setScalar(spec, "url", "https://github.com/"+request.Repo)
	setScalar(spec, "profile", profileName)
	checkout := emptyMapping()
	setScalar(checkout, "mode", request.Checkout)
	if request.Checkout == "path" {
		setScalar(checkout, "path", request.CheckoutPath)
	}
	setMapEntry(spec, "checkout", checkout)
	if request.BranchSet {
		setScalar(spec, "branch", request.Branch)
	}
	setMapEntry(substrates, request.Name, spec)
	return nil
}

func updateNode(root *yaml.Node, request UpdateRequest) error {
	substrates, err := findSection(root, "substrates")
	if err != nil {
		return err
	}
	_, spec := mappingEntry(substrates, request.Name)
	if spec == nil {
		return fmt.Errorf("Substrate %q not found", request.Name)
	}
	if spec.Kind != yaml.MappingNode {
		return fmt.Errorf("Substrate %q is not a mapping", request.Name)
	}

	if request.RepoSet {
		setScalar(spec, "url", "https://github.com/"+request.Repo)
	}
	if request.BranchSet {
		setScalar(spec, "branch", request.Branch)
	}
	if request.CheckoutSet || request.PathSet {
		checkout, err := ensureNestedMapping(spec, "checkout")
		if err != nil {
			return err
		}
		existingMode := scalarValue(checkout, "mode")
		if request.CheckoutSet {
			setScalar(checkout, "mode", request.Checkout)
			if request.Checkout == "path" {
				setScalar(checkout, "path", request.CheckoutPath)
			} else if existingMode == "path" || hasMappingKey(checkout, "path") {
				removeMappingKey(checkout, "path")
			}
		} else {
			if existingMode != "path" {
				return fmt.Errorf("--path without --checkout is valid only for an existing path checkout")
			}
			setScalar(checkout, "path", request.CheckoutPath)
		}
	}
	return nil
}

func findSection(root *yaml.Node, name string) (*yaml.Node, error) {
	_, section := mappingEntry(root, name)
	if section == nil {
		return nil, fmt.Errorf("substrate registry has no %s section", name)
	}
	if section.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("substrate registry %s section is not a mapping", name)
	}
	return section, nil
}

func ensureSection(root *yaml.Node, name string) (*yaml.Node, error) {
	if _, section := mappingEntry(root, name); section != nil {
		if section.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("substrate registry %s section is not a mapping", name)
		}
		return section, nil
	}
	section := emptyMapping()
	setMapEntry(root, name, section)
	return section, nil
}

func ensureNestedMapping(parent *yaml.Node, name string) (*yaml.Node, error) {
	if _, value := mappingEntry(parent, name); value != nil {
		if value.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("Substrate checkout is not a mapping")
		}
		return value, nil
	}
	value := emptyMapping()
	setMapEntry(parent, name, value)
	return value, nil
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func mappingEntry(mapping *yaml.Node, name string) (int, *yaml.Node) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return -1, nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if strings.TrimSpace(mapping.Content[index].Value) == name {
			return index, mapping.Content[index+1]
		}
	}
	return -1, nil
}

func setMapEntry(mapping *yaml.Node, name string, value *yaml.Node) {
	if index, _ := mappingEntry(mapping, name); index >= 0 {
		mapping.Content[index+1] = value
		return
	}
	mapping.Content = append(mapping.Content, scalarNode(name), value)
}

func setScalar(mapping *yaml.Node, name, value string) {
	if index, existing := mappingEntry(mapping, name); index >= 0 {
		if existing.Kind == yaml.ScalarNode {
			existing.Tag = "!!str"
			existing.Value = value
			return
		}
		mapping.Content[index+1] = scalarNode(value)
		return
	}
	setMapEntry(mapping, name, scalarNode(value))
}

func scalarValue(mapping *yaml.Node, name string) string {
	_, value := mappingEntry(mapping, name)
	if value == nil || value.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(value.Value))
}

func hasMappingKey(mapping *yaml.Node, name string) bool {
	index, _ := mappingEntry(mapping, name)
	return index >= 0
}

func removeMappingKey(mapping *yaml.Node, name string) {
	if index, _ := mappingEntry(mapping, name); index >= 0 {
		mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
	}
}

func emptyMapping() *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode}
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func parseAndValidate(path string, content []byte) (*yaml.Node, error) {
	root, _, err := parseValidated(path, content)
	return root, err
}

func parseValidated(path string, content []byte) (*yaml.Node, *conductor.SubstratesConfig, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return nil, nil, fmt.Errorf("decode substrate registry %s: %w", path, err)
	}
	if len(document.Content) == 0 {
		return emptyMapping(), &conductor.SubstratesConfig{}, nil
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("substrate registry %s: expected a top-level mapping", path)
	}
	if err := validateRegistrySections(path, root); err != nil {
		return nil, nil, err
	}

	var config conductor.SubstratesConfig
	if err := root.Decode(&config); err != nil {
		return nil, nil, fmt.Errorf("decode substrate registry %s: %w", path, err)
	}
	if err := conductor.ValidateSubstratesConfig(path, &config); err != nil {
		return nil, nil, err
	}
	return root, &config, nil
}

func validateRegistrySections(path string, root *yaml.Node) error {
	for index := 0; index+1 < len(root.Content); index += 2 {
		name := strings.TrimSpace(root.Content[index].Value)
		if name != "substrates" && name != "credentials" {
			continue
		}
		if root.Content[index+1].Kind != yaml.MappingNode {
			return fmt.Errorf("substrate registry %s: %s must be a mapping", path, name)
		}
	}
	return nil
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
