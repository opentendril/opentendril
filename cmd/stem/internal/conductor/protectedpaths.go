package conductor

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Protected paths — the kernel a Sprout must not be able to rewrite.
//
// OpenTendril builds itself, which means a Sprout can be asked to change the
// orchestrator that is currently running it. Some of those files decide what
// every later run is permitted to do: the capability registry, the continuous
// integration that enforces the rules, the governance documents, this guard.
// A change to one of them must reach a human before it lands.
//
// The protection is enforced HERE, on the trusted side, rather than asked of
// the Sprout. That distinction is the whole point. A rule the editing party is
// asked to honour constrains only a party that chooses to honour it — the same
// weakness a declared Pollen had before credentials replaced it. A Sprout that
// ignores every convention still cannot merge a commit this refuses.
//
// The list lives in the repository under change rather than in the Stem's own
// tree, because it describes that repository's kernel. A Substrate belonging to
// somebody else declares nothing protected, which is the correct reading: there
// is no orchestrator of ours inside it to protect.

// protectedPathsFile is the single definition, relative to a repository root.
const protectedPathsFile = ".github/protected-paths"

// protectedPathRule is one pattern and the line it came from, so a refusal can
// say which rule matched rather than only that something did.
type protectedPathRule struct {
	Pattern string
	Line    int
	// Directory is set when the pattern ended in "/", meaning it matches every
	// path beneath it rather than a path equal to it.
	Directory bool
}

// loadProtectedPaths reads the list from a repository checkout.
//
// The three outcomes are deliberately distinct:
//
//   - the file is absent  → no rules, no error. Nothing is declared protected
//     in this repository, which is the only sensible reading for a Substrate
//     that is not this project.
//   - the file is present and parses → its rules.
//   - the file is present and malformed → an error, which callers must treat as
//     a refusal. A damaged control must never degrade into an absent one.
func loadProtectedPaths(repoRoot string) ([]protectedPathRule, error) {
	path := filepath.Join(repoRoot, protectedPathsFile)

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read protected paths %s: %w", path, err)
	}
	defer file.Close()

	rules := []protectedPathRule{}
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		rule := protectedPathRule{Pattern: text, Line: line}
		if strings.HasSuffix(text, "/") {
			rule.Directory = true
			rule.Pattern = strings.TrimSuffix(text, "/")
			if rule.Pattern == "" {
				return nil, fmt.Errorf("%s:%d: %q matches the whole repository", path, line, text)
			}
		} else if _, err := filepath.Match(text, "probe"); err != nil {
			// A pattern that cannot be compiled would silently match nothing,
			// so it is an error rather than a line to skip.
			return nil, fmt.Errorf("%s:%d: malformed pattern %q: %w", path, line, text, err)
		}
		rules = append(rules, rule)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read protected paths %s: %w", path, err)
	}

	return rules, nil
}

// matchProtectedPath returns the rule a path violates, or nil when it is clear.
func matchProtectedPath(rules []protectedPathRule, candidate string) *protectedPathRule {
	clean := filepath.ToSlash(strings.TrimSpace(candidate))
	if clean == "" {
		return nil
	}

	for index := range rules {
		rule := rules[index]
		if rule.Directory {
			if clean == rule.Pattern || strings.HasPrefix(clean, rule.Pattern+"/") {
				return &rules[index]
			}
			continue
		}
		if clean == rule.Pattern {
			return &rules[index]
		}
		if matched, err := filepath.Match(rule.Pattern, clean); err == nil && matched {
			return &rules[index]
		}
	}
	return nil
}

// protectedPathViolation names a refused change in terms an operator can act on.
type protectedPathViolation struct {
	Path string
	Rule protectedPathRule
}

func (v protectedPathViolation) Error() string {
	return fmt.Sprintf(
		"refusing to merge: %q is a protected path (matched %q at %s:%d).\n"+
			"These files decide what every later run may do, so a change to one reaches a\n"+
			"human before it lands. Open a pull request for this change and have it reviewed;\n"+
			"the Stem will not integrate it directly.",
		v.Path, v.Rule.Pattern, protectedPathsFile, v.Rule.Line)
}

// checkProtectedPaths refuses a set of changed paths against the repository's
// own list.
//
// The rules are read from the checkout as it stands BEFORE the merge, never
// from the commit being merged. That ordering is load-bearing: reading the
// incoming commit would let a Sprout delete the list in the same change that
// edits a kernel file, and disable the guard in the act of tripping it.
func checkProtectedPaths(repoRoot string, changed []string) error {
	rules, err := loadProtectedPaths(repoRoot)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		return nil
	}

	for _, candidate := range changed {
		if rule := matchProtectedPath(rules, candidate); rule != nil {
			return protectedPathViolation{Path: candidate, Rule: *rule}
		}
	}
	return nil
}
