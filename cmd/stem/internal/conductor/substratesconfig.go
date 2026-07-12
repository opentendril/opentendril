package conductor

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// SubstratesConfig defines named substrate mappings loaded from YAML.
type SubstratesConfig struct {
	Substrates map[string]SubstrateSpec `yaml:"substrates"`
	// Credentials defines reusable named credential profiles that substrates
	// reference by name via SubstrateSpec.Profile. Design RFC #222.
	Credentials map[string]CredentialProfile `yaml:"credentials,omitempty"`
}

// SubstrateSpec describes one named substrate entry.
type SubstrateSpec struct {
	Path   string `yaml:"path,omitempty"`
	URL    string `yaml:"url"`
	Branch string `yaml:"branch,omitempty"`
	// Auth describes how the substrate authenticates to its remote. It accepts
	// either a bare env-var name (e.g. `auth: GITHUB_TOKEN`, which is treated
	// as method "pat") or a mapping (`auth: {method: ssh, key: ~/.ssh/id_ot}`).
	Auth AuthSpec `yaml:"auth,omitempty"`
	// Sign optionally configures commit signing for this substrate.
	Sign SignSpec `yaml:"sign,omitempty"`
	// Checkout controls where a foreign substrate is materialized.
	Checkout CheckoutSpec `yaml:"checkout,omitempty"`
	// Profile references a named entry under the top-level `credentials:` map,
	// supplying auth/sign for this substrate without repeating them inline.
	Profile  string `yaml:"profile,omitempty"`
	ReadOnly bool   `yaml:"readonly,omitempty"`
	// Provider selects the terrarium backend ("docker", "host", "gvisor", "firecracker").
	// Defaults to "docker" when omitted.
	Provider string `yaml:"provider,omitempty"`
	// Command overrides the container entrypoint when provider is "host".
	Command []string `yaml:"command,omitempty"`
}

// AuthSpec describes a substrate's authentication method. Design RFC #222.
// Back-compat: a bare scalar decodes to {Method: "pat", Env: <scalar>}.
type AuthSpec struct {
	// Method is one of "pat", "ssh", "none", or "app". Empty means "unspecified"
	// (a PAT is resolved from the referenced/ambient env).
	Method string `yaml:"method,omitempty"`
	// Env names the environment variable holding the PAT (method "pat").
	Env string `yaml:"env,omitempty"`
	// Key is a filesystem path to an SSH private key (method "ssh").
	Key string `yaml:"key,omitempty"`
	// GitHub App fields (method "app"). The Stem mints short-lived installation
	// tokens from these instead of holding a long-lived PAT.
	AppID          string `yaml:"appId,omitempty"`
	InstallationID int64  `yaml:"installationId,omitempty"` // optional — auto-discovered when 0
	PrivateKeyPath string `yaml:"privateKeyPath,omitempty"` // path to the App .pem
	PrivateKeyEnv  string `yaml:"privateKeyEnv,omitempty"`  // or env holding the PEM contents
}

// UnmarshalYAML accepts either a scalar env-var name (back-compat) or a mapping.
func (a *AuthSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		a.Method = "pat"
		a.Env = strings.TrimSpace(value.Value)
		return nil
	}
	// Decode into a type alias to avoid recursing back into this method.
	type rawAuthSpec AuthSpec
	var raw rawAuthSpec
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*a = AuthSpec(raw)
	return nil
}

// SignSpec configures optional commit signing. Design RFC #222.
type SignSpec struct {
	// Method is "ssh" or "gpg". Empty disables signing.
	Method string `yaml:"method,omitempty"`
	// Key is the signing key reference (SSH key path or GPG key id).
	Key string `yaml:"key,omitempty"`
}

// CheckoutSpec controls where a foreign substrate is checked out. Design RFC #222.
type CheckoutSpec struct {
	// Mode is "ephemeral" (default, /tmp), "managed" (persistent OT-owned dir),
	// or "path" (explicit Path below).
	Mode string `yaml:"mode,omitempty"`
	Path string `yaml:"path,omitempty"`
}

// CredentialProfile is a reusable named bundle of auth + signing config that
// substrates reference by name via SubstrateSpec.Profile. Design RFC #222.
type CredentialProfile struct {
	Auth AuthSpec `yaml:"auth,omitempty"`
	Sign SignSpec `yaml:"sign,omitempty"`
}

type substrateExecutionPlan struct {
	name        string
	hostPath    string
	cloneURL    string
	cloneBranch string
	authRef     string
	credential  ResolvedCredential
	readOnly    bool
	named       bool
	remoteClone bool
	provider    string
	command     []string
}

// LoadSubstratesConfig searches for the active substrates.yaml and parses it.
func LoadSubstratesConfig(root string) (*SubstratesConfig, error) {
	searchRoot := strings.TrimSpace(root)
	if searchRoot == "" {
		searchRoot = mustGetwd()
	}

	for _, candidate := range substrateConfigCandidates(searchRoot) {
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat substrates config %s: %w", candidate, err)
		}
		if info.IsDir() {
			continue
		}

		content, err := os.ReadFile(candidate)
		if err != nil {
			return nil, fmt.Errorf("read substrates config %s: %w", candidate, err)
		}

		var config SubstratesConfig
		if err := yaml.Unmarshal(content, &config); err != nil {
			return nil, fmt.Errorf("decode substrates config %s: %w", candidate, err)
		}

		normalizeSubstratesConfig(&config)
		validateSubstratesConfig(candidate, &config)

		return &config, nil
	}

	return nil, nil
}

// ResolveSubstrate resolves a named substrate or treats the input as a path.
func ResolveSubstrate(nameOrPath string, config *SubstratesConfig) (*SubstrateSpec, bool) {
	trimmed := strings.TrimSpace(nameOrPath)
	if trimmed == "" {
		return nil, false
	}

	if config != nil && len(config.Substrates) > 0 {
		if spec, ok := config.Substrates[trimmed]; ok {
			copySpec := spec
			trimSubstrateSpec(&copySpec)
			return &copySpec, true
		}
	}

	return &SubstrateSpec{Path: trimmed}, false
}

func resolveSubstrateExecutionPlan(d *DockerOrchestrator, config *SubstratesConfig) (*substrateExecutionPlan, error) {
	if d == nil {
		return nil, fmt.Errorf("docker orchestrator is nil")
	}

	plan := &substrateExecutionPlan{
		name:        strings.TrimSpace(d.Substrate),
		hostPath:    strings.TrimSpace(d.Substrate),
		cloneURL:    strings.TrimSpace(d.SubstrateURL),
		cloneBranch: strings.TrimSpace(d.SubstrateBranch),
	}
	if plan.hostPath == "" {
		plan.hostPath = getEnvOrDefault("OPENTENDRIL_SUBSTRATE", mustGetwd())
	}

	if spec, isName := ResolveSubstrate(plan.name, config); isName && spec != nil {
		plan.named = true
		plan.readOnly = spec.ReadOnly

		if trimmed := strings.TrimSpace(spec.Path); trimmed != "" {
			plan.hostPath = trimmed
		}
		if plan.cloneURL == "" {
			plan.cloneURL = strings.TrimSpace(spec.URL)
		}
		if plan.cloneBranch == "" {
			plan.cloneBranch = strings.TrimSpace(spec.Branch)
		}
		var profiles map[string]CredentialProfile
		if config != nil {
			profiles = config.Credentials
		}
		credential, err := resolveSubstrateCredential(*spec, profiles)
		if err != nil {
			return nil, fmt.Errorf("substrate %q: %w", plan.name, err)
		}
		plan.credential = credential
		// Keep authRef populated for the PAT path so the terrarium clone/push
		// (slice 3) and today's behavior are preserved; ssh/none carry no env.
		plan.authRef = credential.TokenEnv
		plan.provider = strings.ToLower(strings.TrimSpace(spec.Provider))
		plan.command = spec.Command
	}

	if plan.hostPath == "" {
		return nil, fmt.Errorf("substrate path is empty")
	}

	explicitURL := strings.TrimSpace(d.SubstrateURL) != ""
	localPathExists := pathExists(plan.hostPath)

	if explicitURL {
		if plan.cloneURL == "" {
			return nil, fmt.Errorf("substrate %q has no URL to clone", plan.name)
		}
		plan.remoteClone = true
	} else if plan.named && plan.cloneURL != "" && !localPathExists {
		plan.remoteClone = true
	}

	if !plan.remoteClone {
		if !localPathExists {
			return nil, fmt.Errorf("substrate path %s does not exist", plan.hostPath)
		}
		plan.hostPath = repoRoot(plan.hostPath)
	}

	return plan, nil
}

func substrateConfigCandidates(root string) []string {
	base := strings.TrimSpace(root)
	if base == "" {
		base = mustGetwd()
	}

	candidates := []string{
		filepath.Join(base, "substrates.yaml"),
		filepath.Join(base, ".tendril", "substrates.yaml"),
		filepath.Join(repoRoot(base), "substrates.yaml"),
	}

	seen := make(map[string]struct{}, len(candidates))
	unique := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		normalized := filepath.Clean(candidate)
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		unique = append(unique, normalized)
	}

	return unique
}

func normalizeSubstratesConfig(config *SubstratesConfig) {
	if config == nil {
		return
	}

	normalized := make(map[string]SubstrateSpec, len(config.Substrates))
	for name, spec := range config.Substrates {
		trimmedName := strings.TrimSpace(name)
		if trimmedName == "" {
			log.Printf("[Substrates] Warning: encountered substrate entry with an empty name; skipping")
			continue
		}

		trimSubstrateSpec(&spec)
		normalized[trimmedName] = spec
	}

	config.Substrates = normalized

	if len(config.Credentials) > 0 {
		normalizedProfiles := make(map[string]CredentialProfile, len(config.Credentials))
		for name, profile := range config.Credentials {
			trimmedName := strings.TrimSpace(name)
			if trimmedName == "" {
				log.Printf("[Substrates] Warning: encountered credential profile with an empty name; skipping")
				continue
			}
			trimAuthSpec(&profile.Auth)
			trimSignSpec(&profile.Sign)
			normalizedProfiles[trimmedName] = profile
		}
		config.Credentials = normalizedProfiles
	}
}

// trimAuthSpec normalizes an AuthSpec in place (method lower-cased, fields trimmed).
func trimAuthSpec(auth *AuthSpec) {
	if auth == nil {
		return
	}
	auth.Method = strings.ToLower(strings.TrimSpace(auth.Method))
	auth.Env = strings.TrimSpace(auth.Env)
	auth.Key = strings.TrimSpace(auth.Key)
	auth.AppID = strings.TrimSpace(auth.AppID)
	auth.PrivateKeyPath = strings.TrimSpace(auth.PrivateKeyPath)
	auth.PrivateKeyEnv = strings.TrimSpace(auth.PrivateKeyEnv)
}

// trimSignSpec normalizes a SignSpec in place.
func trimSignSpec(sign *SignSpec) {
	if sign == nil {
		return
	}
	sign.Method = strings.ToLower(strings.TrimSpace(sign.Method))
	sign.Key = strings.TrimSpace(sign.Key)
}

func validateSubstratesConfig(sourcePath string, config *SubstratesConfig) {
	if config == nil {
		return
	}

	for name, spec := range config.Substrates {
		if strings.TrimSpace(spec.URL) == "" && strings.TrimSpace(spec.Path) == "" {
			log.Printf("[Substrates] Warning: substrate %q in %s has neither a path nor a URL", name, sourcePath)
		}
		if warning := credentialWarning(spec, config.Credentials); warning != "" {
			log.Printf("[Substrates] Warning: substrate %q %s", name, warning)
		}
	}
}

func trimSubstrateSpec(spec *SubstrateSpec) {
	if spec == nil {
		return
	}

	spec.Path = strings.TrimSpace(spec.Path)
	spec.URL = strings.TrimSpace(spec.URL)
	spec.Branch = strings.TrimSpace(spec.Branch)
	trimAuthSpec(&spec.Auth)
	trimSignSpec(&spec.Sign)
	spec.Checkout.Mode = strings.ToLower(strings.TrimSpace(spec.Checkout.Mode))
	spec.Checkout.Path = strings.TrimSpace(spec.Checkout.Path)
	spec.Profile = strings.TrimSpace(spec.Profile)
	spec.Provider = strings.ToLower(strings.TrimSpace(spec.Provider))
}

func pathExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func sanitizeTempComponent(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "foreign"
	}

	var builder strings.Builder
	for _, r := range trimmed {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}

	cleaned := strings.Trim(builder.String(), "-_.")
	if cleaned == "" {
		return "foreign"
	}

	return cleaned
}

func substrateConfigNames(config *SubstratesConfig) []string {
	if config == nil || len(config.Substrates) == 0 {
		return nil
	}

	names := make([]string, 0, len(config.Substrates))
	for name := range config.Substrates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
