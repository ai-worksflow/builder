package delivery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	defaultNPMRegistry = "https://registry.npmjs.org"
	defaultGoProxy     = "https://proxy.golang.org"
	defaultGoSumDB     = "sum.golang.org"
)

type DependencyPolicy struct {
	NPMRegistry string
	GoProxy     string
	GoSumDB     string
}

type DependencyPreparationRequest struct {
	Ecosystem string
}

type DependencyPreparer interface {
	PrepareDependencies(context.Context, string, DependencyPreparationRequest) (SandboxResult, error)
}

type dependencyPolicyProvider interface {
	DependencyPolicy() DependencyPolicy
}

type dependencyPlan struct {
	ecosystem string
	files     []WorkspaceFile
}

var npmIntegrityPattern = regexp.MustCompile(`^(?:sha512|sha384|sha256)-[A-Za-z0-9+/]+={0,2}(?:\s+(?:sha512|sha384|sha256)-[A-Za-z0-9+/]+={0,2})*$`)

func secureDependencyPolicy(sandbox Sandbox) DependencyPolicy {
	if provider, ok := sandbox.(dependencyPolicyProvider); ok {
		return provider.DependencyPolicy()
	}
	return DependencyPolicy{NPMRegistry: defaultNPMRegistry, GoProxy: defaultGoProxy, GoSumDB: defaultGoSumDB}
}

func validateResolverURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return nil, errors.New("resolver endpoint must be a credential-free HTTPS origin or path without query or fragment")
	}
	if parsed.Hostname() == "" || strings.ContainsAny(parsed.Host, "\r\n\x00") {
		return nil, errors.New("resolver endpoint host is invalid")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return parsed, nil
}

func validateDependencyWorkspace(files []WorkspaceFile, policy DependencyPolicy) (dependencyPlan, []Diagnostic) {
	byPath := make(map[string]WorkspaceFile, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}
	if manifest, ok := byPath["package.json"]; ok {
		lock, hasLock := byPath["package-lock.json"]
		diagnostics := validateNodeDependencyFiles(manifest, lock, hasLock, policy.NPMRegistry)
		plan := dependencyPlan{ecosystem: "node", files: []WorkspaceFile{manifest}}
		if hasLock {
			plan.files = append(plan.files, lock)
		}
		return plan, diagnostics
	}
	if manifest, ok := byPath["go.mod"]; ok {
		sum, hasSum := byPath["go.sum"]
		diagnostics := validateGoDependencyFiles(manifest, hasSum)
		plan := dependencyPlan{ecosystem: "go", files: []WorkspaceFile{manifest}}
		if hasSum {
			plan.files = append(plan.files, sum)
		}
		return plan, diagnostics
	}
	return dependencyPlan{}, nil
}

func validateNodeDependencyFiles(manifest, lock WorkspaceFile, hasLock bool, registry string) []Diagnostic {
	diagnostics := []Diagnostic{}
	if _, found := SensitiveFinding(manifest.Content); found {
		diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "dependency_manifest_secret", Severity: SeverityError, Message: "package.json contains credential-like content and cannot enter the network resolver.", Path: manifest.Path})
	}
	var packageJSON struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		Scripts         map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(manifest.Content), &packageJSON); err != nil {
		return []Diagnostic{{CheckID: CheckDependency, Code: "package_json_invalid", Severity: SeverityError, Message: "package.json is not valid JSON.", Path: manifest.Path}}
	}
	if !hasLock {
		return []Diagnostic{{CheckID: CheckDependency, Code: "package_lock_required", Severity: SeverityError, Message: "Node dependency preparation requires package-lock.json; pnpm and yarn locks are not accepted by the fixed npm resolver.", Path: manifest.Path}}
	}
	for name, version := range packageJSON.Dependencies {
		if forbiddenManifestDependencyVersion(version) {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "dependency_source_forbidden", Severity: SeverityError, Message: "Dependency " + name + " must resolve through the configured npm registry.", Path: manifest.Path})
		} else if unsafeManifestDependencyVersion(version) {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "dependency_unpinned", Severity: SeverityWarning, Message: "Dependency " + name + " uses a non-reproducible manifest range; package-lock.json remains authoritative.", Path: manifest.Path})
		}
	}
	for name, version := range packageJSON.DevDependencies {
		if forbiddenManifestDependencyVersion(version) {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "dependency_source_forbidden", Severity: SeverityError, Message: "Development dependency " + name + " must resolve through the configured npm registry.", Path: manifest.Path})
		} else if unsafeManifestDependencyVersion(version) {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "dependency_unpinned", Severity: SeverityWarning, Message: "Development dependency " + name + " uses a non-reproducible manifest range; package-lock.json remains authoritative.", Path: manifest.Path})
		}
	}
	registryURL, err := validateResolverURL(registry)
	if err != nil {
		return []Diagnostic{{CheckID: CheckDependency, Code: "resolver_policy_invalid", Severity: SeverityError, Message: "The configured npm registry policy is invalid."}}
	}
	var lockJSON struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
			Link      bool   `json:"link"`
		} `json:"packages"`
		Dependencies map[string]json.RawMessage `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(lock.Content), &lockJSON); err != nil || lockJSON.LockfileVersion < 2 || len(lockJSON.Packages) == 0 {
		return []Diagnostic{{CheckID: CheckDependency, Code: "package_lock_invalid", Severity: SeverityError, Message: "package-lock.json must be a valid npm lockfileVersion 2 or newer.", Path: lock.Path}}
	}
	if _, found := SensitiveFinding(lock.Content); found {
		diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "dependency_lock_secret", Severity: SeverityError, Message: "package-lock.json contains credential-like content and cannot enter the network resolver.", Path: lock.Path})
	}
	for path, entry := range lockJSON.Packages {
		if path == "" {
			continue
		}
		if entry.Link || !strings.HasPrefix(path, "node_modules/") {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "package_lock_local_reference", Severity: SeverityError, Message: "package-lock.json contains a local or linked package, which is forbidden in the isolated resolver.", Path: lock.Path})
			continue
		}
		if !validNPMIntegrity(strings.TrimSpace(entry.Integrity)) {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "package_lock_integrity_missing", Severity: SeverityError, Message: "Every locked npm package must include a supported Subresource Integrity digest.", Path: lock.Path})
		}
		if err := validateLockedRegistryURL(entry.Resolved, registryURL); err != nil {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "package_lock_registry_mismatch", Severity: SeverityError, Message: err.Error(), Path: lock.Path})
		}
	}
	for name := range packageJSON.Dependencies {
		if _, ok := lockJSON.Packages["node_modules/"+name]; !ok {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "package_lock_incomplete", Severity: SeverityError, Message: "package-lock.json does not pin dependency " + name + ".", Path: lock.Path})
		}
	}
	for name := range packageJSON.DevDependencies {
		if _, ok := lockJSON.Packages["node_modules/"+name]; !ok {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "package_lock_incomplete", Severity: SeverityError, Message: "package-lock.json does not pin development dependency " + name + ".", Path: lock.Path})
		}
	}
	for _, script := range []string{"preinstall", "install", "postinstall"} {
		if strings.TrimSpace(packageJSON.Scripts[script]) != "" {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "install_script_ignored", Severity: SeverityWarning, Message: "The npm lifecycle script " + script + " will be disabled by --ignore-scripts.", Path: manifest.Path})
		}
	}
	return deduplicateDiagnostics(diagnostics)
}

func validNPMIntegrity(value string) bool {
	if !npmIntegrityPattern.MatchString(value) {
		return false
	}
	expected := map[string]int{"sha256": 32, "sha384": 48, "sha512": 64}
	for _, token := range strings.Fields(value) {
		parts := strings.SplitN(token, "-", 2)
		if len(parts) != 2 {
			return false
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(parts[1])
		if err != nil || len(decoded) != expected[parts[0]] {
			return false
		}
	}
	return true
}

func unsafeManifestDependencyVersion(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "" || value == "*" || value == "latest"
}

func forbiddenManifestDependencyVersion(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "http:") || strings.HasPrefix(value, "https:") || strings.HasPrefix(value, "git+") ||
		strings.HasPrefix(value, "git:") || strings.HasPrefix(value, "github:") || strings.HasPrefix(value, "file:") ||
		strings.HasPrefix(value, "workspace:") || strings.HasPrefix(value, "link:") || strings.HasPrefix(value, "npm:")
}

func validateLockedRegistryURL(raw string, registry *url.URL) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("locked npm package URL must be a credential-free HTTPS registry URL")
	}
	if !strings.EqualFold(parsed.Host, registry.Host) {
		return errors.New("locked npm package URL does not match the configured registry origin")
	}
	registryPath := strings.TrimSuffix(registry.EscapedPath(), "/")
	if registryPath != "" && parsed.EscapedPath() != registryPath && !strings.HasPrefix(parsed.EscapedPath(), registryPath+"/") {
		return errors.New("locked npm package URL escapes the configured registry path")
	}
	return nil
}

func validateGoDependencyFiles(manifest WorkspaceFile, hasSum bool) []Diagnostic {
	diagnostics := []Diagnostic{}
	if _, found := SensitiveFinding(manifest.Content); found {
		diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "dependency_manifest_secret", Severity: SeverityError, Message: "go.mod contains credential-like content and cannot enter the network resolver.", Path: manifest.Path})
	}
	lines := strings.Split(manifest.Content, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(strings.SplitN(raw, "//", 2)[0])
		if !strings.HasPrefix(line, "replace ") && !strings.Contains(line, "=>") {
			continue
		}
		parts := strings.SplitN(line, "=>", 2)
		if len(parts) != 2 {
			continue
		}
		target := strings.Fields(strings.TrimSpace(parts[1]))
		if len(target) == 0 || isLocalGoReplacement(target[0]) {
			diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "go_local_replace_forbidden", Severity: SeverityError, Message: "go.mod local replace directives are forbidden because the network resolver mounts only go.mod and go.sum.", Path: manifest.Path})
		}
	}
	if goModHasExternalRequirements(manifest.Content) && !hasSum {
		diagnostics = append(diagnostics, Diagnostic{CheckID: CheckDependency, Code: "go_sum_required", Severity: SeverityError, Message: "go.sum is required for externally versioned modules.", Path: manifest.Path})
	}
	return diagnostics
}

func isLocalGoReplacement(target string) bool {
	return target == "." || target == ".." || strings.HasPrefix(target, "./") || strings.HasPrefix(target, "../") ||
		strings.HasPrefix(target, "~/") || filepath.IsAbs(target) || strings.Contains(target, "\\") ||
		regexp.MustCompile(`^[A-Za-z]:`).MatchString(target) || strings.HasPrefix(strings.ToLower(target), "file:")
}

func goModHasExternalRequirements(value string) bool {
	inBlock := false
	for _, raw := range strings.Split(value, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "//", 2)[0])
		switch {
		case line == "require (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case strings.HasPrefix(line, "require "):
			return true
		case inBlock && line != "":
			return true
		}
	}
	return false
}

func materializeDependencyPlan(baseDirectory string, plan dependencyPlan) (string, func(), error) {
	if plan.ecosystem == "" || len(plan.files) == 0 {
		return "", nil, nil
	}
	directory, err := os.MkdirTemp(baseDirectory, "worksflow-dependencies-")
	if err != nil {
		return "", nil, wrapInternal("create isolated dependency directory", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if err := os.Chmod(directory, 0o700); err != nil {
		cleanup()
		return "", nil, wrapInternal("protect isolated dependency directory", err)
	}
	allowed := map[string]bool{"package.json": true, "package-lock.json": true, "go.mod": true, "go.sum": true}
	for _, file := range plan.files {
		if !allowed[file.Path] || filepath.Base(file.Path) != file.Path {
			cleanup()
			return "", nil, Invalid("dependencies", "resolver input may contain only root manifest and lock files")
		}
		if err := os.WriteFile(filepath.Join(directory, file.Path), []byte(file.Content), 0o600); err != nil {
			cleanup()
			return "", nil, wrapInternal("materialize dependency manifest", err)
		}
	}
	return directory, cleanup, nil
}

func ensurePreparedDependencyLayout(directory, ecosystem string) error {
	var target string
	switch ecosystem {
	case "node":
		target = filepath.Join(directory, "node_modules")
	case "go":
		target = filepath.Join(directory, "pkg", "mod")
	default:
		return Invalid("dependencies", "prepared dependency ecosystem is unsupported")
	}
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Invalid("dependencies", "prepared dependency root must be a real directory")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return wrapInternal("inspect prepared dependency root", err)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return wrapInternal("create empty prepared dependency root", err)
	}
	return nil
}

func hasErrorDiagnostic(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == SeverityError {
			return true
		}
	}
	return false
}

func deduplicateDiagnostics(values []Diagnostic) []Diagnostic {
	seen := map[string]bool{}
	result := make([]Diagnostic, 0, len(values))
	for _, value := range values {
		key := fmt.Sprintf("%s\x00%s\x00%s", value.Code, value.Message, value.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Code != result[j].Code {
			return result[i].Code < result[j].Code
		}
		return result[i].Message < result[j].Message
	})
	return result
}
