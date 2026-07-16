package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Change-scoped verification, host side: given the set of files a change
// touched, which Go package(s) must be tested? The answer feeds a later
// sequence step that runs `go test` only on the affected slice of the module
// inside the sealed verifier Terrarium.
//
// The governing invariant is FAIL CLOSED: uncertainty always resolves toward
// testing MORE, never less. Any ambiguity — a file that cannot be attributed
// but plausibly affects the build, a package listing that errors, a change to
// the module definition itself — widens the scope to the whole module.
// Under-scoping silently drops coverage, which is exactly the failure mode
// change-scoped verification must never introduce. Widening is reserved for
// uncertainty: files that are *known* inert (documentation, continuous
// integration configuration outside any package) legitimately produce an
// empty scope rather than a widened one.

// modulePackage describes one package of the module as reported by the Go
// toolchain: its import path, its directory, and every import edge that can
// make its tests depend on another package (ordinary imports plus the imports
// of its internal and external test files).
type modulePackage struct {
	// ImportPath is the package's full import path.
	ImportPath string
	// Directory is the package's source directory. The real lister reports
	// it absolute; scope computation normalizes it relative to the module
	// root, so a synthetic lister may return it relative already.
	Directory string
	// Imports are the package's ordinary imports.
	Imports []string
	// TestImports are the additional imports of the package's in-package
	// test files.
	TestImports []string
	// ExternalTestImports are the additional imports of the package's
	// external (package name suffixed with "_test") test files.
	ExternalTestImports []string
}

// listModulePackagesFn is the package-listing seam, injectable for tests that
// drive the attribution and reverse-dependent logic with a synthetic import
// graph instead of a real Go module.
var listModulePackagesFn = listModulePackages

// goSourceFileSuffixes are the file suffixes the Go build consumes directly.
// An unattributable changed file carrying one of these suffixes plausibly
// affects the build, so it must widen the scope instead of being dropped.
var goSourceFileSuffixes = []string{
	".go", ".s", ".S", ".sx", ".c", ".cc", ".cpp", ".cxx",
	".h", ".hh", ".hpp", ".hxx", ".m", ".f", ".F", ".for", ".f90", ".syso",
}

// moduleDefinitionFileNames are the files that define the module's dependency
// universe. A change to any of them invalidates per-package attribution
// entirely, so the scope widens to the whole module before anything else is
// inspected.
var moduleDefinitionFileNames = map[string]bool{
	"go.mod":      true,
	"go.sum":      true,
	"go.work":     true,
	"go.work.sum": true,
}

// TestScopeForChanges returns the `go test` package patterns to run for the
// given changed files, and whether it fell back to the whole module.
//
// Every changed file is attributed to its nearest enclosing package directory
// (which conservatively covers testdata fixtures and go:embed targets, since
// both always live under their package's directory), and the resulting set is
// expanded with every in-module package that transitively imports an affected
// package — including via test files — so a change to a low-level package
// always pulls in the higher-level packages whose tests exercise it.
//
// The fail-closed fallback (wholeModule true, empty package list, meaning
// "test everything", e.g. `./...`) is taken when a module definition file
// changed, when package listing fails for any reason, or when a changed file
// cannot be attributed but plausibly affects the build. Changed files that are
// known inert — documentation or tooling configuration outside every package
// directory — contribute nothing, so a change touching only such files
// legitimately yields an empty scope with nothing to test.
//
// The returned error reports only invalid input from the caller; resolution
// failures never surface as errors, they widen the scope instead.
func TestScopeForChanges(ctx context.Context, moduleRoot string, changedFiles []string) (packages []string, wholeModule bool, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(moduleRoot) == "" {
		return nil, false, fmt.Errorf("change scope module root is required")
	}

	// A change to the module definition rewrites the dependency universe for
	// every package at once — no per-package attribution is sound, so widen
	// before touching the toolchain.
	for _, changedFile := range changedFiles {
		if moduleDefinitionFileNames[path.Base(filepath.ToSlash(changedFile))] {
			return nil, true, nil
		}
	}

	modulePackages, listErr := listModulePackagesFn(ctx, moduleRoot)
	if listErr != nil {
		// Fail closed: without a trustworthy package graph every narrowing
		// decision would be a guess, so the whole module is tested.
		return nil, true, nil
	}

	packageByDirectory := make(map[string]*modulePackage, len(modulePackages))
	packageByImportPath := make(map[string]*modulePackage, len(modulePackages))
	for index := range modulePackages {
		listedPackage := &modulePackages[index]
		directory, normalizeErr := normalizePackageDirectory(moduleRoot, listedPackage.Directory)
		if normalizeErr != nil {
			// A package directory that cannot be placed inside the module
			// root makes the whole directory map unreliable: fail closed.
			return nil, true, nil
		}
		packageByDirectory[directory] = listedPackage
		packageByImportPath[listedPackage.ImportPath] = listedPackage
	}

	affected := make(map[string]bool)
	for _, changedFile := range changedFiles {
		relativeFile, normalizeErr := normalizeChangedFile(moduleRoot, changedFile)
		if normalizeErr != nil {
			// An empty entry or a path that escapes the module root most
			// plausibly means the change set and the module root disagree —
			// that is uncertainty, so widen instead of guessing.
			return nil, true, nil
		}
		attributed := false
		// Walk up from the file's directory to the nearest enclosing package
		// directory. This is what attributes testdata fixtures, go:embed
		// targets, and other package-adjacent assets to their package.
		for directory := path.Dir(relativeFile); ; directory = path.Dir(directory) {
			if listedPackage, found := packageByDirectory[directory]; found {
				affected[listedPackage.ImportPath] = true
				attributed = true
				break
			}
			if directory == "." || directory == "/" {
				break
			}
		}
		if attributed {
			continue
		}
		if changedFilePlausiblyAffectsBuild(relativeFile) {
			// A build-relevant file with no enclosing package — for example a
			// Go file whose whole package was deleted by this change — cannot
			// be attributed, so the scope widens to the whole module.
			return nil, true, nil
		}
		// Known inert: documentation or tooling configuration outside every
		// package directory contributes nothing. This is deliberately NOT a
		// widening case — widening is for uncertainty, not for inert files.
	}

	if len(affected) == 0 {
		return nil, false, nil
	}

	// Invert the in-module import graph (including test-file imports, since a
	// test importing an affected package must rerun) and expand the affected
	// set to every transitive reverse-dependent, so higher-level packages
	// consuming a changed low-level package are always retested.
	reverseDependents := make(map[string][]string)
	for _, listedPackage := range modulePackages {
		importEdges := make([]string, 0, len(listedPackage.Imports)+len(listedPackage.TestImports)+len(listedPackage.ExternalTestImports))
		importEdges = append(importEdges, listedPackage.Imports...)
		importEdges = append(importEdges, listedPackage.TestImports...)
		importEdges = append(importEdges, listedPackage.ExternalTestImports...)
		for _, imported := range importEdges {
			if _, inModule := packageByImportPath[imported]; inModule {
				reverseDependents[imported] = append(reverseDependents[imported], listedPackage.ImportPath)
			}
		}
	}
	queue := make([]string, 0, len(affected))
	for importPath := range affected {
		queue = append(queue, importPath)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, dependent := range reverseDependents[current] {
			if !affected[dependent] {
				affected[dependent] = true
				queue = append(queue, dependent)
			}
		}
	}

	packages = make([]string, 0, len(affected))
	for importPath := range affected {
		packages = append(packages, importPath)
	}
	sort.Strings(packages)
	return packages, false, nil
}

// normalizePackageDirectory turns a package directory as reported by the
// lister into a clean module-root-relative slash path ("." for the root
// package), rejecting directories outside the module root.
func normalizePackageDirectory(moduleRoot, directory string) (string, error) {
	if filepath.IsAbs(directory) {
		relative, err := filepath.Rel(moduleRoot, directory)
		if err != nil {
			return "", err
		}
		directory = relative
	}
	normalized := path.Clean(filepath.ToSlash(directory))
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("package directory %q is outside the module root", directory)
	}
	return normalized, nil
}

// normalizeChangedFile turns a changed-file path (absolute or module-root
// relative) into a clean module-root-relative slash path, rejecting empty
// entries and paths that escape the module root.
func normalizeChangedFile(moduleRoot, changedFile string) (string, error) {
	if strings.TrimSpace(changedFile) == "" {
		return "", fmt.Errorf("changed file entry is empty")
	}
	if filepath.IsAbs(changedFile) {
		relative, err := filepath.Rel(moduleRoot, changedFile)
		if err != nil {
			return "", err
		}
		changedFile = relative
	}
	normalized := path.Clean(filepath.ToSlash(changedFile))
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("changed file %q is outside the module root", changedFile)
	}
	return normalized, nil
}

// changedFilePlausiblyAffectsBuild reports whether an unattributable changed
// file could still influence compilation — a Go build source file (for
// example one belonging to a package this very change deleted) or anything
// under a vendor tree. Such files must widen the scope; everything else
// outside a package directory is known inert.
func changedFilePlausiblyAffectsBuild(relativeFile string) bool {
	for _, suffix := range goSourceFileSuffixes {
		if strings.HasSuffix(relativeFile, suffix) {
			return true
		}
	}
	for _, segment := range strings.Split(relativeFile, "/") {
		if segment == "vendor" {
			return true
		}
	}
	return false
}

// listModulePackages lists every package of the module rooted at moduleRoot
// with its import edges, via `go list -json ./...`. Resolution is pinned
// offline (GOPROXY=off): packages already present resolve deterministically
// from the local cache, and anything that would require a fetch fails — which
// the caller treats as the fail-closed whole-module fallback.
func listModulePackages(ctx context.Context, moduleRoot string) ([]modulePackage, error) {
	command := exec.CommandContext(ctx, "go", "list",
		"-json=ImportPath,Dir,Imports,TestImports,XTestImports", "./...")
	command.Dir = moduleRoot
	command.Env = append(os.Environ(), "GOPROXY=off")
	output, err := command.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("go list failed: %v: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("go list failed: %w", err)
	}

	// `go list -json` emits a stream of JSON objects, one per package.
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	var modulePackages []modulePackage
	for {
		var listed struct {
			ImportPath   string
			Dir          string
			Imports      []string
			TestImports  []string
			XTestImports []string
		}
		if decodeErr := decoder.Decode(&listed); decodeErr == io.EOF {
			break
		} else if decodeErr != nil {
			return nil, fmt.Errorf("go list output could not be decoded: %w", decodeErr)
		}
		modulePackages = append(modulePackages, modulePackage{
			ImportPath:          listed.ImportPath,
			Directory:           listed.Dir,
			Imports:             listed.Imports,
			TestImports:         listed.TestImports,
			ExternalTestImports: listed.XTestImports,
		})
	}
	return modulePackages, nil
}
