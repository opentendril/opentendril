package conductor

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// SubstratesConfig defines named substrate mappings loaded from YAML.
type SubstratesConfig struct {
	Substrates map[string]SubstrateSpec `yaml:"substrates"`
	// Credentials defines reusable named credential profiles that substrates
	// reference by name via SubstrateSpec.Profile. Design RFC.
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
	// Identity optionally configures the commit identity for this substrate.
	Identity IdentitySpec `yaml:"identity,omitempty"`
	// Checkout controls where a foreign substrate is materialized.
	Checkout CheckoutSpec `yaml:"checkout,omitempty"`
	// Commit selects the git.commit execution mode: "local" (the default)
	// commits with the Stem's own git in the workspace; "api" creates the
	// commit remotely via the GitHub GraphQL createCommitOnBranch mutation,
	// signed server-side by GitHub (requires auth method "app"). Design RFC.
	Commit string `yaml:"commit,omitempty"`
	// ProtectDefaultBranch refuses a delegated commit whose target branch is
	// this repository's default branch. It defaults to TRUE when unset (hence
	// the pointer: an absent value must mean protected, not permitted), and is
	// set false only for a repository where committing straight to the default
	// branch is legitimate. Hardening is opt-out; loosening is a knowing act,
	// recorded once in configuration rather than decided per invocation.
	ProtectDefaultBranch *bool `yaml:"protectDefaultBranch,omitempty"`
	// Profile references a named entry under the top-level `credentials:` map,
	// supplying auth/sign for this substrate without repeating them inline.
	Profile  string `yaml:"profile,omitempty"`
	ReadOnly bool   `yaml:"readonly,omitempty"`
	// Provider selects the terrarium backend ("docker", "host", "gvisor", "firecracker").
	// Defaults to "docker" when omitted.
	Provider string `yaml:"provider,omitempty"`
	// Command overrides the container entrypoint when provider is "host".
	Command []string `yaml:"command,omitempty"`
	// Patience bounds how long the Stem gives this substrate's work.
	Patience PatienceSpec `yaml:"patience,omitempty"`
}

// PatienceSpec bounds how long the Stem waits on work for one substrate.
// Values are Go duration strings ("20m", "1h30m").
type PatienceSpec struct {
	// Growth bounds one growth. It is applied as a deadline on the context
	// that governs the run, never as a watchdog value handed to the
	// terrarium: the watchdog is derived from the context deadline, so the
	// deadline is the single place the bound is expressed. Empty leaves the
	// run governed by whatever deadline the caller already carries.
	Growth string `yaml:"growth,omitempty"`
	// Reap bounds how long the work itself may keep going, which is a
	// different question from Growth: once the Stem stops waiting, nothing is
	// listening to the terrarium and only a wall clock can end it. This is the
	// backstop that ends one, and it is deliberately much longer than Growth —
	// an absence of signs of life is never itself evidence that a run is dead,
	// so the reaper answers "is anyone still waiting?" and nothing else.
	// Empty leaves the terrarium's own watchdog as the only backstop.
	Reap string `yaml:"reap,omitempty"`
	// Scratch is neither of the above: it bounds nothing and ends nothing. It
	// is how often a running growth's workspace is actively asked whether it
	// is still changing — the scratch test — and asking is what lets diff
	// growth suppress the suspicion that a quiet run has stopped. Empty leaves
	// the probe off entirely, which is today's behaviour.
	Scratch string `yaml:"scratch,omitempty"`
}

// GrowthBudget parses Growth into a duration. An empty value yields zero and no
// error — an unconfigured patience leaves the run bounded by the caller alone.
// A value that does not parse, or that is not positive, is an error rather than
// a silent zero: a zero budget would bound the run to nothing and abandon it
// the instant it started, which is never what an operator wrote a value to mean.
func (p PatienceSpec) GrowthBudget() (time.Duration, error) {
	return parsePatienceBudget("patience.growth", p.Growth)
}

// ReapBudget parses Reap into a duration, on the same terms as GrowthBudget: an
// empty value yields zero and no error, and anything that cannot be honoured
// fails the load by name. A zero reaper would end every run the instant it
// started, which is the opposite of a backstop.
func (p PatienceSpec) ReapBudget() (time.Duration, error) {
	return parsePatienceBudget("patience.reap", p.Reap)
}

// parsePatienceBudget parses one patience field, naming the field and the
// offending value in any error. Every patience field shares it so a value that
// cannot be honoured fails the load identically wherever it was written — the
// fields differ in what they govern, never in how strictly they are read.
func parsePatienceBudget(field, value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}

	budget, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a duration: %w", field, trimmed, err)
	}
	if budget <= 0 {
		return 0, fmt.Errorf("%s %q must be greater than zero", field, trimmed)
	}

	return budget, nil
}

// ScratchInterval parses Scratch into a duration, on the same terms as the two
// budgets above: an empty value yields zero and no error, leaving the active
// probe off, and anything that cannot be honoured fails the load by name. A zero
// interval would ask the workspace the same question in a tight loop, which is
// never what an operator wrote a value to mean.
func (p PatienceSpec) ScratchInterval() (time.Duration, error) {
	return parsePatienceBudget("patience.scratch", p.Scratch)
}

// AuthSpec describes a substrate's authentication method. Design RFC.
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
	// ExposeToken optionally exposes the resolved token to the Terrarium under
	// the standard GITHUB_TOKEN names for in-container tooling. Default false.
	ExposeToken bool `yaml:"exposeToken,omitempty"`
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

// SignSpec configures optional commit signing. Design RFC.
type SignSpec struct {
	// Method is "ssh" or "gpg". Empty disables signing.
	Method string `yaml:"method,omitempty"`
	// Key is the signing key reference (SSH key path or GPG key id).
	Key string `yaml:"key,omitempty"`
}

// IdentitySpec configures an optional commit identity (author/committer name
// and email). Design RFC. When both fields are empty, no identity is applied
// and the ambient git identity in the terrarium is used.
type IdentitySpec struct {
	// Name is the commit author/committer name.
	Name string `yaml:"name,omitempty"`
	// Email is the commit author/committer email.
	Email string `yaml:"email,omitempty"`
}

// CheckoutSpec controls where a foreign substrate is checked out. Design RFC.
type CheckoutSpec struct {
	// Mode is "ephemeral" (default, /tmp), "managed" (persistent Tendril-owned dir),
	// or "path" (explicit Path below).
	Mode string `yaml:"mode,omitempty"`
	Path string `yaml:"path,omitempty"`
}

// CredentialProfile is a reusable named bundle of auth + signing config that
// substrates reference by name via SubstrateSpec.Profile. Design RFC.
type CredentialProfile struct {
	Auth     AuthSpec     `yaml:"auth,omitempty"`
	Sign     SignSpec     `yaml:"sign,omitempty"`
	Identity IdentitySpec `yaml:"identity,omitempty"`
	// Commit selects the git.commit execution mode for substrates using this
	// profile: "local" (the default) or "api" — see SubstrateSpec.Commit.
	Commit string `yaml:"commit,omitempty"`
}

type substrateExecutionPlan struct {
	name                     string
	hostPath                 string
	cloneURL                 string
	cloneBranch              string
	authRef                  string
	credential               ResolvedCredential
	readOnly                 bool
	named                    bool
	remoteClone              bool
	provider                 string
	command                  []string
	allowDefaultBranchCommit bool
	// growthBudget is the resolved patience.growth for this substrate, zero
	// when unconfigured. Callers apply it to the context that bounds how long
	// they wait for the run.
	growthBudget time.Duration
	// reapBudget is the resolved patience.reap for this substrate, zero when
	// unconfigured. Callers apply it to the context that bounds the work
	// itself, which outlives the wait.
	reapBudget time.Duration
	// scratchInterval is the resolved patience.scratch for this substrate, zero
	// when unconfigured. Callers hand it to the dormancy watcher, which uses it
	// as the period of its active workspace probe; zero leaves the probe off.
	scratchInterval time.Duration
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
		if err := validateSubstratePatience(candidate, &config); err != nil {
			return nil, err
		}
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
		if env := strings.TrimSpace(os.Getenv("TENDRIL_SUBSTRATE")); env != "" {
			plan.hostPath = env
		} else {
			plan.hostPath = mustGetwd()
			if plan.name == "" && isUnsuitableImplicitWorkspace(plan.hostPath) {
				name, ok := uniqueConfiguredSubstrate(config)
				if !ok {
					return nil, fmt.Errorf("substrate is required: %s is the Stem working directory, not a repository checkout", plan.hostPath)
				}
				plan.name = name
			}
		}
	}

	var resolutionErr error
	spec, isName := ResolveSubstrate(plan.name, config)
	if isName && spec != nil {
		plan.named = true
		plan.readOnly = spec.ReadOnly

		resolvedPath, err := ResolveSubstrateWorkspace(plan.name, spec)
		if err != nil {
			resolutionErr = err
			// Keep the intended checkout even when it is not on disk yet so
			// the plan does not fall back to the Stem working directory
			// (control plane + rootless Docker data-root).
			if intended := intendedLocalWorkspace(plan.name, spec); intended != "" {
				plan.hostPath = intended
			}
		} else if resolvedPath != "" {
			plan.hostPath = resolvedPath
		}
		// A named Substrate is never a relative directory name. A leftover
		// ./<name> in the Stem cwd (or Docker creating that name as a volume
		// source) would otherwise become the -v source — an empty /app —
		// instead of the populated managed checkout.
		if intended := intendedLocalWorkspace(plan.name, spec); intended != "" {
			if plan.hostPath == plan.name || !filepath.IsAbs(plan.hostPath) {
				plan.hostPath = intended
			}
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
		// are preserved; ssh/none carry no env.
		plan.authRef = credential.TokenEnv
		plan.provider = strings.ToLower(strings.TrimSpace(spec.Provider))
		plan.command = spec.Command

		if spec.ProtectDefaultBranch != nil && !*spec.ProtectDefaultBranch {
			plan.allowDefaultBranchCommit = true
		}

		growthBudget, err := spec.Patience.GrowthBudget()
		if err != nil {
			return nil, fmt.Errorf("substrate %q: %w", plan.name, err)
		}
		plan.growthBudget = growthBudget

		reapBudget, err := spec.Patience.ReapBudget()
		if err != nil {
			return nil, fmt.Errorf("substrate %q: %w", plan.name, err)
		}
		plan.reapBudget = reapBudget

		scratchInterval, err := spec.Patience.ScratchInterval()
		if err != nil {
			return nil, fmt.Errorf("substrate %q: %w", plan.name, err)
		}
		plan.scratchInterval = scratchInterval
	}

	if plan.hostPath == "" {
		return nil, fmt.Errorf("substrate path is empty")
	}

	explicitURL := strings.TrimSpace(d.SubstrateURL) != ""
	localPathExists := pathExists(plan.hostPath)
	if errors.Is(resolutionErr, ErrWorkspaceAbsent) {
		// Chat/Greenhouse sets only the Substrate name. A managed placeholder
		// directory then looks like a local workspace and skips clone/fetch,
		// so the Terrarium bind-mounts an empty /app. CLI copies the named
		// URL onto the orchestrator and always remotes; treat an unpopulated
		// managed checkout as absent so the name-only path clones too.
		localPathExists = false
	}

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
			if resolutionErr != nil {
				return nil, resolutionErr
			}
			return nil, fmt.Errorf("substrate path %s does not exist", plan.hostPath)
		}
		if abs, err := filepath.Abs(plan.hostPath); err == nil {
			plan.hostPath = abs
		}
		plan.hostPath = repoRoot(plan.hostPath)
	}

	return plan, nil
}

// SubstrateConfigCandidates lists the paths the Stem searches for a substrates
// configuration, in order. An empty root means the process's working directory.
//
// Exported so the posture report measures the files the Stem actually reads
// rather than re-deriving them.
func SubstrateConfigCandidates(root string) []string {
	searchRoot := strings.TrimSpace(root)
	if searchRoot == "" {
		searchRoot = mustGetwd()
	}
	return substrateConfigCandidates(searchRoot)
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
			trimIdentitySpec(&profile.Identity)
			profile.Commit = strings.ToLower(strings.TrimSpace(profile.Commit))
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

// trimIdentitySpec normalizes an IdentitySpec in place.
func trimIdentitySpec(identity *IdentitySpec) {
	if identity == nil {
		return
	}
	identity.Name = strings.TrimSpace(identity.Name)
	identity.Email = strings.TrimSpace(identity.Email)
}

// validateSubstratePatience rejects a configuration whose patience cannot be
// honoured. It is an error rather than a warning because the alternative is a
// bound the operator asked for and did not get: a warning scrolls past and the
// run proceeds under the default, which is indistinguishable from the setting
// having worked.
func validateSubstratePatience(sourcePath string, config *SubstratesConfig) error {
	if config == nil {
		return nil
	}

	for _, name := range substrateConfigNames(config) {
		spec := config.Substrates[name]
		if _, err := spec.Patience.GrowthBudget(); err != nil {
			return fmt.Errorf("substrates config %s: substrate %q: %w", sourcePath, name, err)
		}
		if _, err := spec.Patience.ReapBudget(); err != nil {
			return fmt.Errorf("substrates config %s: substrate %q: %w", sourcePath, name, err)
		}
		if _, err := spec.Patience.ScratchInterval(); err != nil {
			return fmt.Errorf("substrates config %s: substrate %q: %w", sourcePath, name, err)
		}
	}

	return nil
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
	trimIdentitySpec(&spec.Identity)
	spec.Checkout.Mode = strings.ToLower(strings.TrimSpace(spec.Checkout.Mode))
	spec.Checkout.Path = strings.TrimSpace(spec.Checkout.Path)
	spec.Commit = strings.ToLower(strings.TrimSpace(spec.Commit))
	spec.Profile = strings.TrimSpace(spec.Profile)
	spec.Provider = strings.ToLower(strings.TrimSpace(spec.Provider))
	spec.Patience.Growth = strings.TrimSpace(spec.Patience.Growth)
	spec.Patience.Reap = strings.TrimSpace(spec.Patience.Reap)
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
