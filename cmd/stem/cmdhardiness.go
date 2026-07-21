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

	// 4. Can callers prove an identity, or must they declare one?
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

	// 5. Is anything granted at all? No grants is the secure default, and
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
