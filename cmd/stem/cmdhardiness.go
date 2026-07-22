package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// `tendril hardiness` — what this Terroir can actually withstand.
//
// In horticulture, hardiness is whether the conditions at a given site permit a
// plant to survive: not how the specimen is doing today, but what the ground it
// stands on will support. That is exactly this command's question, and it is why
// it is not `health` — health is the runtime liveness monitor, asking how the
// organism is doing right now. Hardiness asks what this site supports at all,
// and the answer changes when the deployment changes, not minute to minute.
//
// Tier 2 makes a real boundary possible; it does not make one exist. A Stem
// running as the same operating-system user as its Pollinators can hold every
// credential correctly and still be walked around, because that user can read
// the private key, rewrite the grants, or ignore the binary. Whether the
// boundary is real is a property of the deployment, and the honest thing is to
// measure it and say so — especially now that the stated direction points at
// Ramets on servers, where "it is just my laptop" stops being true.

type hardinessFinding struct {
	// Severity is "ok", "note" or "weak".
	Severity string
	Title    string
	Detail   string
}

func runHardinessCmd(ctx context.Context, args []string) {
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "-h", "--help", "help":
			fmt.Println("Usage: tendril hardiness")
			fmt.Println()
			fmt.Println("  Reports what this Terroir can withstand — how strong the delegation")
			fmt.Println("  boundary actually is here, as opposed to how the Ramet is running")
			fmt.Println("  (which is `tendril health`):")
			fmt.Println("  whether the Stem has its own principal, whether its credentials are")
			fmt.Println("  readable by the Pollinators it serves, and how it is reachable.")
			return
		}
	}

	tendrilDir := "./.tendril"
	findings := collectHardinessFindings(ctx, tendrilDir)

	weak := 0
	for _, finding := range findings {
		icon := map[string]string{"ok": "✅", "note": "ℹ️ ", "weak": "⚠️ "}[finding.Severity]
		fmt.Printf("%s %s\n", icon, finding.Title)
		if finding.Detail != "" {
			for _, line := range strings.Split(finding.Detail, "\n") {
				fmt.Printf("     %s\n", line)
			}
		}
		if finding.Severity == "weak" {
			weak++
		}
	}

	fmt.Println()
	if weak == 0 {
		fmt.Println("This Terroir is hardy: the delegation boundary is enforced by the operating system.")
		return
	}
	fmt.Printf("%d condition(s) mean delegation here is ADVISORY, not enforced.\n", weak)
	fmt.Println("A Pollinator running as this user can read what the Stem holds and act")
	fmt.Println("outside the governed path. Grants and audit still record intent and catch")
	fmt.Println("accidents — they do not constrain a caller that chooses otherwise.")
}

// collectHardinessFindings measures the conditions that decide whether delegation
// is enforced or merely recorded.
func collectHardinessFindings(ctx context.Context, tendrilDir string) []hardinessFinding {
	findings := []hardinessFinding{}

	current, err := user.Current()
	username := "unknown"
	if err == nil {
		username = current.Username
	}

	// 1. Does the Stem have a principal of its own? Approximated by asking
	//    whether the control-plane directory belongs to somebody else: if this
	//    user owns it, this user can rewrite policy and read secrets.
	ownsControlPlane, ownerName := pathOwnedByCurrentUser(tendrilDir)
	if ownsControlPlane {
		findings = append(findings, hardinessFinding{
			Severity: "weak",
			Title:    fmt.Sprintf("The Stem shares a principal with its callers (%s)", username),
			Detail: "This user owns " + tendrilDir + ", so a Pollinator running as this user can\n" +
				"rewrite grants.yaml, read issued credentials, and bypass the binary entirely.\n" +
				"Run the Stem as its own operating-system user to make the boundary real.",
		})
	} else {
		findings = append(findings, hardinessFinding{
			Severity: "ok",
			Title:    fmt.Sprintf("The Stem has its own principal (%s owns %s)", ownerName, tendrilDir),
		})
	}

	// 2. Are the secrets readable by this user? This is the specific failure
	//    that let the organism's own credential be borrowed.
	readable := readableSecrets(tendrilDir)
	if len(readable) > 0 {
		findings = append(findings, hardinessFinding{
			Severity: "weak",
			Title:    fmt.Sprintf("%d credential file(s) are readable by this user", len(readable)),
			Detail: "  " + strings.Join(readable, "\n  ") + "\n" +
				"A Pollinator that can read a credential can use it directly, without asking\n" +
				"the Stem and without appearing in the audit lane.",
		})
	} else {
		findings = append(findings, hardinessFinding{Severity: "ok", Title: "No credential files are readable by this user"})
	}

	// 3. Escalation paths that defeat a separate principal before it starts.
	//    File ownership is necessary and nowhere near sufficient: a caller that
	//    can reach a rootful container daemon, or sudo to the Stem's user, does
	//    not need to read a file it is not permitted to read.
	findings = append(findings, escalationFindings()...)

	// 4. Can somebody else rewrite what the Stem runs? Ownership of the
	//    credentials is pointless if the binary that enforces the boundary can
	//    be replaced by the accounts it is meant to constrain.
	findings = append(findings, executableIntegrityFinding())

	// 5. Can somebody else rewrite the configuration that decides whether a
	//    Sprout may escape its Terrarium onto the host?
	findings = append(findings, hostExecutionConfigFinding())

	// 6. Can callers prove an identity, or must they declare one?
	credentials, credentialsErr := core.LoadPollinatorCredentials(tendrilDir)
	switch {
	case credentialsErr != nil:
		findings = append(findings, hardinessFinding{
			Severity: "weak",
			Title:    "The Pollinator credential store could not be read",
			Detail:   credentialsErr.Error() + "\nEvery credential-bearing caller is denied until this is fixed.",
		})
	case len(credentials) == 0:
		findings = append(findings, hardinessFinding{
			Severity: "note",
			Title:    "No Pollinator credentials issued — every Pollen is DECLARED, not proven",
			Detail: "Callers name themselves, so the grant model records intent rather than\n" +
				"enforcing identity. Issue one with: tendril pollinator issue --pollen <name>",
		})
	default:
		active := 0
		for _, credential := range credentials {
			if credential.Active() {
				active++
			}
		}
		if active == 0 {
			// Issued-but-all-revoked is not the same as issued: nobody can
			// prove anything, and saying "credentials issued" would imply a
			// strength that is not there.
			findings = append(findings, hardinessFinding{
				Severity: "note",
				Title:    fmt.Sprintf("%d Pollinator credential(s) exist but NONE are active", len(credentials)),
				Detail: "Every credential-bearing request is denied. Callers can still DECLARE a\n" +
					"Pollen, which is an audit control rather than a boundary.",
			})
			break
		}
		findings = append(findings, hardinessFinding{
			Severity: "ok",
			Title:    fmt.Sprintf("%d active Pollinator credential(s) — those callers PROVE their Pollen", active),
		})
	}

	// 7. Is anything granted at all? No grants is the secure default, and
	//    saying so avoids an operator wondering why everything is denied.
	grants, grantsErr := core.LoadDelegationGrants(tendrilDir)
	switch {
	case grantsErr != nil:
		findings = append(findings, hardinessFinding{Severity: "weak", Title: "The grants file could not be read", Detail: grantsErr.Error()})
	case len(grants) == 0:
		findings = append(findings, hardinessFinding{Severity: "note", Title: "No grants configured — every delegated invocation is denied (secure default)"})
	default:
		findings = append(findings, hardinessFinding{Severity: "ok", Title: fmt.Sprintf("%d grant(s) configured", len(grants))})
	}

	return findings
}

// pathOwnedByCurrentUser reports whether this user owns the path, and the
// owner's name when it can be resolved.
func pathOwnedByCurrentUser(path string) (bool, string) {
	info, err := os.Stat(path)
	if err != nil {
		// A control-plane directory that does not exist yet will be created by
		// whoever runs the Stem — which is this user.
		return true, "nobody yet"
	}
	owner, ok := fileOwnerUID(info)
	if !ok {
		return true, "unknown"
	}
	name := fmt.Sprintf("uid %d", owner)
	if resolved, err := user.LookupId(fmt.Sprintf("%d", owner)); err == nil {
		name = resolved.Username
	}
	return owner == os.Getuid(), name
}

// readableSecrets lists credential material this user can actually open. It
// opens rather than inspecting the mode, because that is the question that
// matters — permissions can be satisfied through group membership.
func readableSecrets(tendrilDir string) []string {
	candidates := []string{
		filepath.Join(tendrilDir, core.PollinatorCredentialsFilename),
		filepath.Join(tendrilDir, "api-key"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		matches, _ := filepath.Glob(filepath.Join(home, ".tendril", "*.pem"))
		candidates = append(candidates, matches...)
		candidates = append(candidates, filepath.Join(home, ".tendril", core.PollinatorCredentialsFilename))
	}

	seen := map[string]bool{}
	readable := []string{}
	for _, candidate := range candidates {
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		file, err := os.Open(candidate)
		if err != nil {
			continue
		}
		file.Close()
		readable = append(readable, candidate)
	}
	return readable
}

// escalationFindings reports the ways this user can become another user, which
// is the question file permissions cannot answer.
//
// A separate operating-system principal for the Stem is defeated — completely,
// not partially — by either of these, so reporting ownership without reporting
// these would describe a boundary that is not there.
func escalationFindings() []hardinessFinding {
	findings := []hardinessFinding{}

	// Membership of the container-daemon group is equivalent to root: a member
	// can bind-mount the whole filesystem into a container and read or write
	// anything as root, whatever a file's owner and mode say.
	if inGroup("docker") && !dockerIsRootless() {
		findings = append(findings, hardinessFinding{
			Severity: "weak",
			Title:    "This user is in the \"docker\" group with a rootful daemon — that is root",
			Detail: "A member can run a container that bind-mounts the whole filesystem and read\n" +
				"or write anything as root, so no file ownership protects the Stem's credentials\n" +
				"from this user. Use rootless Docker (set DOCKER_HOST to the Stem user's own\n" +
				"socket) or the Firecracker provider, which needs only /dev/kvm.",
		})
	} else if inGroup("docker") {
		findings = append(findings, hardinessFinding{
			Severity: "ok",
			Title:    "Container access is rootless — group membership is not root here",
		})
	}

	// A cached or passwordless sudo ticket lets a caller simply become the
	// Stem's user, which makes the separation cosmetic.
	if canSudoWithoutPassword() {
		findings = append(findings, hardinessFinding{
			Severity: "weak",
			Title:    "This user can sudo without being asked for a password",
			Detail: "Anything running as this user can become another user — including the Stem's —\n" +
				"without a human present. Note that sudo also CACHES credentials for several\n" +
				"minutes by default, so a recent authentication counts as passwordless.\n" +
				"Require a password and set timestamp_timeout=0 for the rule that reaches the\n" +
				"Stem's user, or administer that account from a different session entirely.",
		})
	}

	return findings
}

// Executable integrity: whether an account other than the owner can replace the
// binary the Stem runs. The whole resolution chain is inspected — the binary,
// each symbolic link followed, and every ancestor directory — because replacing
// a file needs write permission on its directory, not on the file.
//
// Group-write is reported with a qualification: it is only an exposure if the
// group has members besides the owner, which cannot be determined portably.
// Root is out of scope; the boundary measured is against Pollinator-hosting
// accounts.

// maxExecutableLinkHops bounds the symbolic-link walk. The value matches the
// conventional kernel limit; a chain longer than this is a loop in practice.
const maxExecutableLinkHops = 40

// executableIntegrityFinding measures the binary this process is running from.
// Run as the Stem it names the Stem's binary; run as another account it names
// that account's. The finding states which it answered.
func executableIntegrityFinding() hardinessFinding {
	executable, err := os.Executable()
	if err != nil {
		return hardinessFinding{
			Severity: "note",
			Title:    "The running binary could not be located, so its integrity is unknown",
			Detail: err.Error() + "\n" +
				"This is not a pass: whether somebody else can replace what the Stem runs\n" +
				"has not been established.",
		}
	}
	return executableIntegrityFindingFor(executable)
}

// executableIntegrityFindingFor is the measurement itself, separated from
// os.Executable so it can be exercised against a constructed tree.
func executableIntegrityFindingFor(executable string) hardinessFinding {
	inspected, unresolved := executableResolutionChain(executable)

	exposures := []string{}
	unreadable := []string{}
	// A path that broke the link walk is also in the inspected set, so both
	// sources are merged through one seen-set rather than listed twice.
	noted := map[string]bool{}
	noteUnreadable := func(path string) {
		if noted[path] {
			return
		}
		noted[path] = true
		unreadable = append(unreadable, path)
	}
	for _, path := range unresolved {
		noteUnreadable(path)
	}
	for _, path := range inspected {
		exposure, examined := pathWritableByOthers(path)
		switch {
		case !examined:
			noteUnreadable(path)
		case exposure != "":
			exposures = append(exposures, fmt.Sprintf("%s (%s)", path, exposure))
		}
	}

	if len(exposures) > 0 {
		detail := "  " + strings.Join(exposures, "\n  ") + "\n" +
			"An account that can write any of these can replace the binary the Stem\n" +
			"executes, and the next start runs whatever it was replaced with. A\n" +
			"group-writable path is only an exposure if that group has members besides\n" +
			"the owner — check the group before deciding this is harmless."
		if len(unreadable) > 0 {
			detail += "\nAlso not examined: " + strings.Join(unreadable, ", ")
		}
		return hardinessFinding{
			Severity: "weak",
			Title:    fmt.Sprintf("%d path(s) on the running binary's resolution chain are writable by others", len(exposures)),
			Detail:   detail,
		}
	}

	if len(unreadable) > 0 {
		return hardinessFinding{
			Severity: "note",
			Title:    "The running binary's resolution chain could not be fully examined",
			Detail: "  " + strings.Join(unreadable, "\n  ") + "\n" +
				"This is not a pass: these paths may or may not be writable by another\n" +
				"account, and the difference has not been established.",
		}
	}

	return hardinessFinding{
		Severity: "ok",
		Title:    fmt.Sprintf("Nothing on the running binary's resolution chain is writable by others (%s)", executable),
	}
}

// executableResolutionChain lists every path whose permissions decide whether the
// executable can be replaced: the binary, each symbolic link followed, each
// link's target, and every ancestor directory of those.
//
// The second return value lists paths that could not be examined, which the
// caller must report rather than treat as clean.
func executableResolutionChain(executable string) (inspect []string, unresolved []string) {
	seen := map[string]bool{}
	add := func(path string) {
		for current := filepath.Clean(path); ; current = filepath.Dir(current) {
			if !seen[current] {
				seen[current] = true
				inspect = append(inspect, current)
			}
			if parent := filepath.Dir(current); parent == current {
				break
			}
		}
	}

	current := filepath.Clean(executable)
	for hop := 0; hop < maxExecutableLinkHops; hop++ {
		add(current)

		info, err := os.Lstat(current)
		if err != nil {
			unresolved = append(unresolved, current)
			break
		}
		if info.Mode()&os.ModeSymlink == 0 {
			break
		}

		target, err := os.Readlink(current)
		if err != nil {
			unresolved = append(unresolved, current)
			break
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(current), target)
		}
		current = filepath.Clean(target)
	}

	return inspect, unresolved
}

// pathWritableByOthers reports whether a path carries the group-write or
// other-write permission bit, and whether the question could be answered.
//
// Symbolic links are skipped: their permission bits are always 0777 on Linux, so
// judging one by its own mode reports an exposure on every system. The directory
// holding a link, and the link's target, are inspected as their own entries.
//
// A sticky directory is not an exposure even when world-writable: only an entry's
// owner may replace it.
func pathWritableByOthers(path string) (exposure string, examined bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", true
	}
	if info.IsDir() && info.Mode()&os.ModeSticky != 0 {
		return "", true
	}

	mode := info.Mode().Perm()
	groupWritable := mode&0o020 != 0
	otherWritable := mode&0o002 != 0

	switch {
	case groupWritable && otherWritable:
		return "group- and world-writable", true
	case otherWritable:
		return "world-writable", true
	case groupWritable:
		return "group-writable", true
	}
	return "", true
}

// Host-execution configuration: whether an account other than the owner can
// declare a substrate that runs on the host. Host execution is default-deny
// behind TENDRIL_ALLOW_HOST_EXECUTION, but which substrates use it is decided by
// a file, so the question is who can write that file rather than where it sits.
//
// The inspected paths come from the loader, so the check cannot examine
// different files from the ones the Stem reads.

// hostExecutionConfigFinding measures the exposure of the configuration that
// decides host execution.
func hostExecutionConfigFinding() hardinessFinding {
	candidates := conductor.SubstrateConfigCandidates("")

	exposures := []string{}
	unreadable := []string{}
	present := 0
	for _, candidate := range candidates {
		if _, err := os.Lstat(candidate); err != nil {
			// Absent is ordinary — the Stem searches several locations and uses
			// the first that exists. Only an existing-but-unexaminable file is
			// worth reporting.
			if !os.IsNotExist(err) {
				unreadable = append(unreadable, candidate)
			}
			continue
		}
		present++
		exposure, examined := pathWritableByOthers(candidate)
		switch {
		case !examined:
			unreadable = append(unreadable, candidate)
		case exposure != "":
			exposures = append(exposures, fmt.Sprintf("%s (%s)", candidate, exposure))
		}
	}

	gateOpen, gateKnown := hostExecutionGateState()
	declared := hostProviderDeclared()

	if len(exposures) == 0 && len(unreadable) == 0 {
		if present == 0 {
			return hardinessFinding{Severity: "ok", Title: "No substrate configuration is present to grant host execution"}
		}
		return hardinessFinding{
			Severity: "ok",
			Title:    fmt.Sprintf("Substrate configuration is not writable by others (%d file(s) checked)", present),
		}
	}

	detail := ""
	if len(exposures) > 0 {
		detail = "  " + strings.Join(exposures, "\n  ") + "\n"
	}
	if len(unreadable) > 0 {
		detail += "  not examined: " + strings.Join(unreadable, ", ") + "\n"
	}

	// Exposure matters most when host execution is actually reachable. Both
	// signals are reported, because an operator fixing this needs to know which
	// of them made it urgent.
	switch {
	case gateKnown && gateOpen:
		return hardinessFinding{
			Severity: "weak",
			Title:    "Substrate configuration is writable by others AND host execution is enabled",
			Detail: detail +
				"An account that can write these files can declare a substrate with\n" +
				"provider: host, and a Sprout then runs directly on this host with the\n" +
				"Stem's own credentials and reach — outside any Terrarium.",
		}
	case declared:
		return hardinessFinding{
			Severity: "weak",
			Title:    "Substrate configuration is writable by others AND declares a host substrate",
			Detail: detail +
				"A host substrate is configured here, so the only thing standing between a\n" +
				"writer of these files and execution on this host is the runtime environment\n" +
				"gate, which this check could not confirm from here.",
		}
	default:
		return hardinessFinding{
			Severity: "note",
			Title:    "Substrate configuration is writable by others",
			Detail: detail +
				"Host execution is not indicated here, so this is not an escape route today.\n" +
				"It becomes one the moment TENDRIL_ALLOW_HOST_EXECUTION is set, and whether\n" +
				"the Stem's own service has it set cannot be seen from this invocation.",
		}
	}
}

// hostExecutionGateState reports whether the runtime gate is open, and whether
// that could be established. The variable is this process's environment, which
// is the Stem's own only when run as the Stem from its working directory. An
// unset variable means "not visible", never "not set".
func hostExecutionGateState() (open bool, known bool) {
	raw, present := os.LookupEnv(terrariumAllowHostExecutionEnv)
	if !present {
		return false, false
	}
	return strings.EqualFold(strings.TrimSpace(raw), "true"), true
}

// terrariumAllowHostExecutionEnv mirrors the terrarium factory's gate variable,
// named here rather than imported to keep the posture report off the execution
// path.
const terrariumAllowHostExecutionEnv = "TENDRIL_ALLOW_HOST_EXECUTION"

// hostProviderDeclared reports whether any configured substrate asks for the
// host provider. A malformed or absent configuration answers no: this is a
// severity signal, and the configuration's own validity is reported elsewhere.
func hostProviderDeclared() bool {
	config, err := conductor.LoadSubstratesConfig("")
	if err != nil || config == nil {
		return false
	}
	for _, spec := range config.Substrates {
		if strings.EqualFold(strings.TrimSpace(spec.Provider), "host") {
			return true
		}
	}
	return false
}

// inGroup reports whether the current user belongs to a named group.
func inGroup(name string) bool {
	current, err := user.Current()
	if err != nil {
		return false
	}
	ids, err := current.GroupIds()
	if err != nil {
		return false
	}
	for _, id := range ids {
		if group, err := user.LookupGroupId(id); err == nil && group.Name == name {
			return true
		}
	}
	return false
}

// dockerIsRootless asks the daemon whether it is running rootless. A rootless
// daemon cannot grant root on the host, so group membership stops being an
// escalation path.
func dockerIsRootless() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.SecurityOptions}}").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), "rootless")
}

// canSudoWithoutPassword reports whether sudo would run right now with no
// prompt — either because it is configured NOPASSWD or because a cached
// timestamp is still valid. Both mean an unattended caller can escalate.
func canSudoWithoutPassword() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// -n never prompts: it fails instead, which is the answer we want.
	return exec.CommandContext(ctx, "sudo", "-n", "true").Run() == nil
}
