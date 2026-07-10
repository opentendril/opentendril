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
}

// SubstrateSpec describes one named substrate entry.
type SubstrateSpec struct {
	Path     string `yaml:"path,omitempty"`
	URL      string `yaml:"url"`
	Branch   string `yaml:"branch,omitempty"`
	Auth     string `yaml:"auth,omitempty"`
	ReadOnly bool   `yaml:"readonly,omitempty"`
	// Provider selects the terrarium backend ("docker", "host", "gvisor", "firecracker").
	// Defaults to "docker" when omitted.
	Provider string `yaml:"provider,omitempty"`
	// Command overrides the container entrypoint when provider is "host".
	Command []string `yaml:"command,omitempty"`
}

type substrateExecutionPlan struct {
	name        string
	hostPath    string
	cloneURL    string
	cloneBranch string
	authRef     string
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
		plan.authRef = strings.TrimSpace(spec.Auth)
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
}

func validateSubstratesConfig(sourcePath string, config *SubstratesConfig) {
	if config == nil {
		return
	}

	for name, spec := range config.Substrates {
		if strings.TrimSpace(spec.URL) == "" && strings.TrimSpace(spec.Path) == "" {
			log.Printf("[Substrates] Warning: substrate %q in %s has neither a path nor a URL", name, sourcePath)
		}
		if authRef := strings.TrimSpace(spec.Auth); authRef != "" {
			if _, ok := os.LookupEnv(authRef); !ok {
				log.Printf("[Substrates] Warning: substrate %q references auth env %q, which is not set", name, authRef)
			}
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
	spec.Auth = strings.TrimSpace(spec.Auth)
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
