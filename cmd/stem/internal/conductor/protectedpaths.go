package conductor

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Protected paths: files a Sprout must not be able to rewrite, because they
// decide what every later run may do. A change to one must reach a human.
//
// Enforcement is here, on the trusted side, rather than asked of the Sprout.
//
// The list lives in the repository under change, so it describes that
// repository's kernel. A Substrate that carries no list declares nothing
// protected.

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

// loadProtectedPaths reads the list from a repository checkout. Three distinct
// outcomes: absent means no rules and no error; present and valid yields its
// rules; present and malformed is an error callers must treat as a refusal.
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

// checkProtectedPaths refuses a set of changed paths against the repository's own
// list.
//
// The rules are read from the checkout as it stands BEFORE the merge, never from
// the commit being merged: otherwise a commit could delete the list in the same
// change that edits a kernel file.
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
