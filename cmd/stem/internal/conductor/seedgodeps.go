package conductor

import (
	"bytes"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

// Canonical public Go module proxy. It is the default retrieval location for
// locked Seed Go artifacts, not implicit authority: every constructed URL is
// still authorized against the resolved Seed EgressPolicy.
const canonicalGoModuleProxyBase = "https://proxy.golang.org"

// seedGoModuleProxyBase is the Stem-side module proxy used to construct
// retrieval URLs. Tests replace it with a local listener so they never depend
// on public network access.
var seedGoModuleProxyBase = canonicalGoModuleProxyBase

const (
	// seedGoProxyRelativeRoot is the payload directory under the Terrarium
	// egress tree that becomes the file:// GOPROXY for cache population.
	seedGoProxyRelativeRoot = "go-proxy"

	// seedGoModuleVersionLimit caps unique locked module/version pairs.
	seedGoModuleVersionLimit = 512

	// seedGoModuleFetchLimit caps generated proxy object retrievals.
	seedGoModuleFetchLimit = 1536

	// seedGoMetadataFileLimit caps go.mod / go.sum / vendor/modules.txt.
	seedGoMetadataFileLimit = 2 << 20

	// seedGoModuleAggregateLimit caps the sum of retrieved proxy objects.
	seedGoModuleAggregateLimit = 256 << 20
)

const (
	goModuleInfoSuffix = ".info"
	goModuleModSuffix  = ".mod"
	goModuleZipSuffix  = ".zip"
)

// goModuleVersion is one locked module path and version.
type goModuleVersion struct {
	Path    string
	Version string
}

func (m goModuleVersion) key() string {
	return m.Path + "@" + m.Version
}

type seedGoMetadataSnapshot struct {
	goMod  []byte
	goSum  []byte
	hasMod bool
}

func seedGoPrepErrorf(format string, args ...any) error {
	return fmt.Errorf("prepare Seed Go verification: "+format, args...)
}

func seedVerifyInvokesGo(command []string) bool {
	if len(command) == 0 {
		return false
	}
	return filepath.Base(strings.TrimSpace(command[0])) == "go"
}

// configureSeedGoVerification arms a Seed verify run that needs Go module
// material. Non-Go predicates and candidates without go.mod are left unchanged.
func configureSeedGoVerification(root string, command []string, execution *StomaExecution) error {
	if execution == nil {
		return seedGoPrepErrorf("stoma execution is required")
	}
	if !seedVerifyInvokesGo(command) {
		return nil
	}
	goModPath := filepath.Join(root, "go.mod")
	info, err := os.Stat(goModPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return seedGoPrepErrorf("stat go.mod: %w", err)
	}
	if !info.Mode().IsRegular() {
		return seedGoPrepErrorf("go.mod is not a regular file")
	}

	hasWork, err := candidateHasGoWork(root)
	if err != nil {
		return err
	}
	if hasWork {
		return seedGoPrepErrorf("go.work is unsupported for Seed Go verification")
	}

	// Seed Go verification must not consult incidental host module caches,
	// and the candidate itself is mounted read-only for preparation and the
	// predicate. Container-local module cache and GOCACHE stay writable.
	execution.SkipHostModuleCache = true
	execution.ReadOnlyWorkspace = true

	validVendor, err := hasValidGoVendorTree(root)
	if err != nil {
		return err
	}
	if validVendor {
		execution.GoVendorMode = true
		return nil
	}

	fetches, err := planSeedGoModuleFetches(root)
	if err != nil {
		return err
	}
	if len(fetches) == 0 {
		return nil
	}
	execution.Fetches = fetches
	execution.PopulateGoModuleCache = true
	return nil
}

func snapshotSeedGoMetadata(root string) (seedGoMetadataSnapshot, error) {
	var snapshot seedGoMetadataSnapshot
	goMod, err := readOptionalBoundedFile(filepath.Join(root, "go.mod"), seedGoMetadataFileLimit)
	if err != nil {
		return seedGoMetadataSnapshot{}, seedGoPrepErrorf("read go.mod: %w", err)
	}
	if goMod == nil {
		return snapshot, nil
	}
	snapshot.hasMod = true
	snapshot.goMod = goMod
	goSum, err := readOptionalBoundedFile(filepath.Join(root, "go.sum"), seedGoMetadataFileLimit)
	if err != nil {
		return seedGoMetadataSnapshot{}, seedGoPrepErrorf("read go.sum: %w", err)
	}
	snapshot.goSum = goSum
	return snapshot, nil
}

func (s seedGoMetadataSnapshot) assertUnchanged(root string) error {
	if !s.hasMod {
		return nil
	}
	goMod, err := readOptionalBoundedFile(filepath.Join(root, "go.mod"), seedGoMetadataFileLimit)
	if err != nil {
		return seedGoPrepErrorf("re-read go.mod: %w", err)
	}
	if !bytes.Equal(s.goMod, goMod) {
		return seedGoPrepErrorf("go.mod changed during verification")
	}
	goSum, err := readOptionalBoundedFile(filepath.Join(root, "go.sum"), seedGoMetadataFileLimit)
	if err != nil {
		return seedGoPrepErrorf("re-read go.sum: %w", err)
	}
	if !bytes.Equal(s.goSum, goSum) {
		return seedGoPrepErrorf("go.sum changed during verification")
	}
	return nil
}

func candidateHasGoWork(root string) (bool, error) {
	_, err := os.Lstat(filepath.Join(root, "go.work"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, seedGoPrepErrorf("stat go.work: %w", err)
	}
	return true, nil
}

func hasValidGoVendorTree(root string) (bool, error) {
	modulesTxt := filepath.Join(root, "vendor", "modules.txt")
	info, err := os.Stat(modulesTxt)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, seedGoPrepErrorf("stat vendor/modules.txt: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, seedGoPrepErrorf("vendor/modules.txt is not a regular file")
	}
	content, err := readBoundedFile(modulesTxt, seedGoMetadataFileLimit)
	if err != nil {
		return false, seedGoPrepErrorf("read vendor/modules.txt: %w", err)
	}
	return vendorModulesTxtIsValid(content), nil
}

func vendorModulesTxtIsValid(content []byte) bool {
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "# "))
		if len(fields) < 2 {
			continue
		}
		if err := module.Check(fields[0], fields[1]); err != nil {
			return false
		}
		return true
	}
	return false
}

func planSeedGoModuleFetches(root string) ([]StomaFetch, error) {
	modBytes, err := readBoundedFile(filepath.Join(root, "go.mod"), seedGoMetadataFileLimit)
	if err != nil {
		return nil, seedGoPrepErrorf("read go.mod: %w", err)
	}
	sumBytes, err := readOptionalBoundedFile(filepath.Join(root, "go.sum"), seedGoMetadataFileLimit)
	if err != nil {
		return nil, seedGoPrepErrorf("read go.sum: %w", err)
	}
	modules, err := lockedGoModuleVersions(modBytes, sumBytes)
	if err != nil {
		return nil, err
	}
	return seedGoModuleFetches(seedGoModuleProxyBase, modules)
}

func lockedGoModuleVersions(modBytes, sumBytes []byte) ([]goModuleVersion, error) {
	parsed, err := modfile.Parse("go.mod", modBytes, nil)
	if err != nil {
		return nil, seedGoPrepErrorf("parse go.mod: %w", err)
	}
	if parsed.Module == nil || strings.TrimSpace(parsed.Module.Mod.Path) == "" {
		return nil, seedGoPrepErrorf("go.mod names no module path")
	}
	mainPath := parsed.Module.Mod.Path

	directoryReplaced := map[string]bool{}
	for _, replacement := range parsed.Replace {
		if replacement == nil {
			continue
		}
		if strings.TrimSpace(replacement.New.Version) == "" || modfile.IsDirectoryPath(replacement.New.Path) {
			directoryReplaced[replacement.Old.Path] = true
		}
	}

	var required []goModuleVersion
	for _, req := range parsed.Require {
		if req == nil || req.Mod.Path == "" || req.Mod.Path == mainPath {
			continue
		}
		if directoryReplaced[req.Mod.Path] {
			continue
		}
		if err := module.Check(req.Mod.Path, req.Mod.Version); err != nil {
			return nil, seedGoPrepErrorf("go.mod require %s@%s: %w", req.Mod.Path, req.Mod.Version, err)
		}
		required = append(required, goModuleVersion{Path: req.Mod.Path, Version: req.Mod.Version})
	}

	if len(required) > 0 && len(bytes.TrimSpace(sumBytes)) == 0 {
		return nil, seedGoPrepErrorf("go.mod requires modules but go.sum is missing or empty")
	}

	locked, err := parseGoSumModules(sumBytes)
	if err != nil {
		return nil, err
	}

	lockedSet := make(map[string]goModuleVersion, len(locked))
	for _, item := range locked {
		if item.Path == mainPath || directoryReplaced[item.Path] {
			continue
		}
		lockedSet[item.key()] = item
	}

	for _, req := range required {
		if _, ok := lockedSet[req.key()]; !ok {
			return nil, seedGoPrepErrorf("go.mod require %s@%s is not locked in go.sum", req.Path, req.Version)
		}
	}

	modules := make([]goModuleVersion, 0, len(lockedSet))
	for _, item := range lockedSet {
		modules = append(modules, item)
	}
	sort.Slice(modules, func(i, j int) bool {
		if modules[i].Path != modules[j].Path {
			return modules[i].Path < modules[j].Path
		}
		return modules[i].Version < modules[j].Version
	})
	if len(modules) > seedGoModuleVersionLimit {
		return nil, seedGoPrepErrorf("locked module/version count %d exceeds the %d bound", len(modules), seedGoModuleVersionLimit)
	}
	return modules, nil
}

func parseGoSumModules(sumBytes []byte) ([]goModuleVersion, error) {
	if len(bytes.TrimSpace(sumBytes)) == 0 {
		return nil, nil
	}
	seen := map[string]goModuleVersion{}
	for lineNumber, raw := range strings.Split(string(sumBytes), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, seedGoPrepErrorf("go.sum line %d is malformed", lineNumber+1)
		}
		path := fields[0]
		version := fields[1]
		if strings.HasSuffix(version, "/go.mod") {
			version = strings.TrimSuffix(version, "/go.mod")
		}
		if err := module.Check(path, version); err != nil {
			return nil, seedGoPrepErrorf("go.sum module %s@%s: %w", path, version, err)
		}
		item := goModuleVersion{Path: path, Version: version}
		seen[item.key()] = item
	}
	modules := make([]goModuleVersion, 0, len(seen))
	for _, item := range seen {
		modules = append(modules, item)
	}
	return modules, nil
}

func seedGoModuleFetches(proxyBase string, modules []goModuleVersion) ([]StomaFetch, error) {
	if len(modules) == 0 {
		return nil, nil
	}
	fetches := make([]StomaFetch, 0, len(modules)*3)
	for _, item := range modules {
		for _, suffix := range []string{goModuleInfoSuffix, goModuleModSuffix, goModuleZipSuffix} {
			fetch, err := seedGoModuleFetch(proxyBase, item, suffix)
			if err != nil {
				return nil, err
			}
			fetches = append(fetches, fetch)
		}
	}
	if len(fetches) > seedGoModuleFetchLimit {
		return nil, seedGoPrepErrorf("generated fetch count %d exceeds the %d bound", len(fetches), seedGoModuleFetchLimit)
	}
	return fetches, nil
}

func seedGoModuleFetch(proxyBase string, item goModuleVersion, suffix string) (StomaFetch, error) {
	escapedPath, err := module.EscapePath(item.Path)
	if err != nil {
		return StomaFetch{}, seedGoPrepErrorf("escape module path %q: %w", item.Path, err)
	}
	escapedVersion, err := module.EscapeVersion(item.Version)
	if err != nil {
		return StomaFetch{}, seedGoPrepErrorf("escape module version %q: %w", item.Version, err)
	}
	base := strings.TrimRight(strings.TrimSpace(proxyBase), "/")
	if base == "" {
		return StomaFetch{}, seedGoPrepErrorf("module proxy base is required")
	}
	object := escapedPath + "/@v/" + escapedVersion + suffix
	return StomaFetch{
		URL:  base + "/" + object,
		Path: pathpkg.Join(seedGoProxyRelativeRoot, object),
	}, nil
}

func seedGoProxyFileURL() string {
	return "file://" + stomaEgressDirectory + "/" + seedGoProxyRelativeRoot + "/"
}

func readBoundedFile(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readBoundedReader(file, limit, path)
}

func readOptionalBoundedFile(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	return readBoundedReader(file, limit, path)
}

func readBoundedReader(reader io.Reader, limit int, name string) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(content) > limit {
		return nil, fmt.Errorf("%s exceeds the %d-byte bound", name, limit)
	}
	return content, nil
}
